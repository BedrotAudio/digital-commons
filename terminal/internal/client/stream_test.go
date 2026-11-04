package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fullStream = "event: start\n" +
	`data: {"request_id":"r","conversation_id":"c","message_id":"m","title":"T","route":{"requested_tier":"auto","tier":"standard","locality":"this_device","fallback_from":null},"context":{"estimated_tokens":1240,"budget_tokens":6144,"omitted_messages":0},"notices":[{"code":"history_trimmed","message":"Older messages left out."}]}` + "\n\n" +
	"event: citations\n" +
	`data: {"citations":[{"number":1,"document_id":"d","title":"Remote Work Policy","page":null,"snippet":"Employees may"}]}` + "\n\n" +
	"event: status\n" +
	`data: {"phase":"loading_model","message":"Getting ready."}` + "\n\n" +
	"event: thinking\n" +
	`data: {"text":"hmm"}` + "\n\n" +
	"event: delta\n" +
	`data: {"text":"Here is"}` + "\n\n" +
	"event: done\n" +
	`data: {"message_id":"m","finish_reason":"stop","usage":{"prompt_tokens":152,"completion_tokens":388,"total_tokens":540,"first_token_ms":2210,"total_ms":61000,"load_ms":78,"tokens_per_second":6.4,"estimated":false}}` + "\n\n"

func collect(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("stream did not close; got %#v", out)
		}
	}
}

func flushWrite(w http.ResponseWriter, s string) {
	io.WriteString(w, s)
	w.(http.Flusher).Flush()
}

func sseServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, Token: "secret", IdleTimeout: 150 * time.Millisecond})
}

func last(t *testing.T, events []StreamEvent) StreamEvent {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events[len(events)-1]
}

func TestFullStream(t *testing.T) {
	var body map[string]any
	var headers http.Header
	c := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &body)
		flushWrite(w, fullStream)
	})
	events := collect(t, c.StreamChat(context.Background(), ChatRequest{Message: "hi"}))
	if len(events) != 6 {
		t.Fatalf("%d events: %#v", len(events), events)
	}
	start := events[0].(StartEvent)
	if start.ConversationID != "c" || start.Route.Tier != "standard" || start.Context.BudgetTokens != 6144 || start.Notices[0].Code != "history_trimmed" {
		t.Errorf("start %+v", start)
	}
	if cit := events[1].(CitationsEvent); cit.Citations[0].Title != "Remote Work Policy" || cit.Citations[0].Page != nil {
		t.Errorf("citations %+v", cit)
	}
	if st := events[2].(StatusEvent); st.Phase != "loading_model" {
		t.Errorf("status %+v", st)
	}
	if th := events[3].(ThinkingEvent); th.Text != "hmm" {
		t.Errorf("thinking %+v", th)
	}
	if d := events[4].(DeltaEvent); d.Text != "Here is" {
		t.Errorf("delta %+v", d)
	}
	done := events[5].(DoneEvent)
	if done.FinishReason != "stop" || done.Usage.TotalMS != 61000 || done.Usage.TokensPerSecond != 6.4 {
		t.Errorf("done %+v", done)
	}

	if headers.Get("Authorization") != "Bearer secret" || headers.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", headers)
	}
	if headers.Get("Origin") != "" || headers.Get("Sec-Fetch-Site") != "" {
		t.Error("browser headers sent")
	}
	want := map[string]any{
		"conversation_id": nil, "message": "hi", "attachments": []any{}, "tier": "auto",
		"assistant": "general", "use_documents": false, "regenerate": false,
	}
	for k, v := range want {
		got, ok := body[k]
		if !ok {
			t.Errorf("request is missing %s", k)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
}

func TestStreamEdgeCases(t *testing.T) {
	delta := func(s string) string { return "event: delta\ndata: {\"text\":\"" + s + "\"}\n\n" }
	done := "event: done\ndata: {\"message_id\":\"m\",\"finish_reason\":\"stop\",\"usage\":{}}\n\n"

	cases := []struct {
		name   string
		chunks []string
		want   []string // delta text, then the final event type
	}{
		{"pings ignored", []string{": ping\n\n", delta("a"), ": ping\n", ":\n\n", delta("b"), done}, []string{"a", "b", "done"}},
		{"crlf", []string{strings.ReplaceAll(delta("a")+done, "\n", "\r\n")}, []string{"a", "done"}},
		{"chunks split anywhere", splitEvery(delta("hello")+delta("world")+done, 3), []string{"hello", "world", "done"}},
		{"split one byte", splitEvery(delta("x")+done, 1), []string{"x", "done"}},
		{"unknown event skipped", []string{"event: future\ndata: {\"a\":1}\n\n", delta("a"), done}, []string{"a", "done"}},
		{"unknown event with bad json", []string{"event: future\ndata: not json\n\n", done}, []string{"done"}},
		{"unknown fields", []string{"event: delta\nid: 7\nretry: 1\ndata: {\"text\":\"a\",\"extra\":{\"x\":[1]}}\n\n", done}, []string{"a", "done"}},
		{"no space after colon", []string{"event:delta\ndata:{\"text\":\"a\"}\n\n", done}, []string{"a", "done"}},
		{"event without data", []string{"event: delta\n\n", done}, []string{"done"}},
		{"data in two lines", []string{"event: delta\ndata: {\"text\":\ndata: \"a\"}\n\n", done}, []string{"a", "done"}},
		{"error event", []string{delta("a"), "event: error\ndata: {\"code\":\"timeout\",\"message\":\"Stopped.\",\"detail\":null,\"retryable\":true}\n\n"}, []string{"a", "error"}},
		{"ends after done", []string{done, delta("late")}, []string{"done"}},
		{"close with no final", []string{delta("a")}, []string{"a", "lost"}},
		{"close mid event", []string{delta("a"), "event: done\ndata: {\"mess"}, []string{"a", "lost"}},
		{"done without blank line", []string{"event: done\ndata: {\"finish_reason\":\"stop\"}\n"}, []string{"lost"}},
		{"close before anything", nil, []string{"lost"}},
		{"bad json on known event", []string{"event: delta\ndata: not json\n\n"}, []string{"lost"}},
		{"html body", []string{"<html><body>portal</body></html>\n"}, []string{"lost"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
				flushWrite(w, "")
				for _, chunk := range c.chunks {
					flushWrite(w, chunk)
					time.Sleep(time.Millisecond)
				}
			})
			var got []string
			for _, ev := range collect(t, cl.StreamChat(context.Background(), ChatRequest{})) {
				switch e := ev.(type) {
				case DeltaEvent:
					got = append(got, e.Text)
				case DoneEvent:
					got = append(got, "done")
				case ErrorEvent:
					got = append(got, "error")
				case LostEvent:
					got = append(got, "lost")
					if e.Err.Message != messageLost || !IsNetwork(e.Err) {
						t.Errorf("lost error %+v", e.Err)
					}
				}
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		k := min(n, len(s))
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}

func TestErrorEventFields(t *testing.T) {
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		flushWrite(w, "event: error\ndata: {\"code\":\"provider_unavailable\",\"message\":\"Try\\u001b[2J later\",\"detail\":\"\\u001b]0;x\\u0007d\",\"retryable\":true}\n\n")
	})
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	e, ok := last(t, events).(ErrorEvent)
	if !ok {
		t.Fatalf("%#v", events)
	}
	if e.Err.Code != "provider_unavailable" || e.Err.Message != "Try later" || e.Err.Detail != "d" || !e.Err.Retryable {
		t.Fatalf("%+v", e.Err)
	}
	if !IsAPI(e.Err) {
		t.Errorf("kind %v", e.Err.Kind)
	}
}

func TestErrorBeforeStart(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(*Error) bool
	}{
		{"busy", 429, `{"error":{"code":"busy","message":"Please wait.","detail":null,"retryable":true}}`, IsAPIError},
		{"auth", 401, `{"error":{"code":"auth_required","message":"No.","detail":null,"retryable":false}}`, IsAuthError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cl := newServer(t, jsonHandler(t, c.status, c.body))
			events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
			if len(events) != 1 {
				t.Fatalf("%#v", events)
			}
			e, ok := events[0].(ErrorEvent)
			if !ok || !c.check(e.Err) {
				t.Fatalf("%#v", events[0])
			}
		})
	}
}

func IsAPIError(e *Error) bool  { return IsAPI(e) }
func IsAuthError(e *Error) bool { return IsAuth(e) }

func TestConnectionRefusedStream(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	events := collect(t, New(Options{BaseURL: url}).StreamChat(context.Background(), ChatRequest{}))
	if _, ok := last(t, events).(LostEvent); !ok || len(events) != 1 {
		t.Fatalf("%#v", events)
	}
}

func TestSilenceIsLost(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		flushWrite(w, "event: delta\ndata: {\"text\":\"partial\"}\n\n")
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	began := time.Now()
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	if elapsed := time.Since(began); elapsed < 100*time.Millisecond {
		t.Errorf("gave up after %v", elapsed)
	}
	if len(events) != 2 {
		t.Fatalf("%#v", events)
	}
	if d := events[0].(DeltaEvent); d.Text != "partial" {
		t.Errorf("%+v", d)
	}
	lost, ok := events[1].(LostEvent)
	if !ok {
		t.Fatalf("%#v", events[1])
	}
	if !strings.Contains(lost.Err.Error(), "no data") {
		t.Errorf("%v", lost.Err)
	}
}

func TestSilenceBeforeHeadersIsLost(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	cl := New(Options{BaseURL: srv.URL, IdleTimeout: 100 * time.Millisecond})
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	if _, ok := last(t, events).(LostEvent); !ok || len(events) != 1 {
		t.Fatalf("%#v", events)
	}
}

func TestPingsKeepAStreamAlive(t *testing.T) {
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		for range 6 {
			flushWrite(w, ": ping\n\n")
			time.Sleep(60 * time.Millisecond) // total exceeds the 150 ms idle limit
		}
		flushWrite(w, "event: done\ndata: {\"finish_reason\":\"stop\"}\n\n")
	})
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	if _, ok := last(t, events).(DoneEvent); !ok {
		t.Fatalf("%#v", events)
	}
}

func TestSlowConsumerIsNotSilence(t *testing.T) {
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) { flushWrite(w, fullStream) })
	ch := cl.StreamChat(context.Background(), ChatRequest{})
	time.Sleep(300 * time.Millisecond) // twice the idle limit before the first read
	events := collect(t, ch)
	if _, ok := last(t, events).(DoneEvent); !ok {
		t.Fatalf("%#v", events)
	}
}

func TestHugeLine(t *testing.T) {
	huge := strings.Repeat("x", 5<<20)
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		flushWrite(w, "event: delta\ndata: {\"text\":\""+huge+"\"}\n\n")
		flushWrite(w, "event: done\ndata: {\"finish_reason\":\"stop\"}\n\n")
	})
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	if len(events) != 2 {
		t.Fatalf("%d events", len(events))
	}
	if d := events[0].(DeltaEvent); len(d.Text) != len(huge) {
		t.Fatalf("delta length %d", len(d.Text))
	}
}

func TestLineBeyondCapIsLost(t *testing.T) {
	huge := strings.Repeat("x", maxLineBytes+1024)
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		flushWrite(w, "data: "+huge+"\n\n")
	})
	events := collect(t, cl.StreamChat(context.Background(), ChatRequest{}))
	if _, ok := last(t, events).(LostEvent); !ok {
		t.Fatalf("%#v", events)
	}
}

func TestCancelClosesWithNoFinalEvent(t *testing.T) {
	gone := make(chan struct{})
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		flushWrite(w, "event: delta\ndata: {\"text\":\"a\"}\n\n")
		<-r.Context().Done()
		close(gone)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := cl.StreamChat(ctx, ChatRequest{})
	if _, ok := (<-ch).(DeltaEvent); !ok {
		t.Fatal("first event")
	}
	cancel()
	for ev := range ch {
		t.Fatalf("event after cancel: %#v", ev)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the connection close")
	}
}

func TestCancelWhileWaitingForHeaders(t *testing.T) {
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the server notices a closed connection only after this
		<-r.Context().Done()
	})
	cl.idleTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	ch := cl.StreamChat(ctx, ChatRequest{})
	time.Sleep(20 * time.Millisecond)
	cancel()
	if events := collect(t, ch); len(events) != 0 {
		t.Fatalf("%#v", events)
	}
}

// A reader that walks away must only have to cancel. The goroutine has to exit
// even though the channel is full and nobody is receiving.
func TestAbandonedStreamEndsOnCancel(t *testing.T) {
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		for {
			if _, err := io.WriteString(w, "event: delta\ndata: {\"text\":\"a\"}\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := cl.StreamChat(ctx, ChatRequest{})
	<-ch
	time.Sleep(50 * time.Millisecond) // the channel buffer fills
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for streamRunning() {
		if time.Now().After(deadline) {
			t.Fatal("stream goroutine did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range ch {
	}
}

func streamRunning() bool {
	buf := make([]byte, 1<<20)
	return bytes.Contains(buf[:runtime.Stack(buf, true)], []byte("runStream"))
}

func TestHostileDeltasAreCleanedWithoutLosingText(t *testing.T) {
	// ESC[2J split across two deltas, and an OSC 52 split across three.
	deltas := []string{"safe \\u001b", "[2Jtext \\u001b]52;", "c;aGVsbG8=", "\\u0007end \\u009b2J \\u202eok\\r\\b"}
	cl := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		for _, d := range deltas {
			flushWrite(w, "event: delta\ndata: {\"text\":\""+d+"\"}\n\n")
		}
		flushWrite(w, "event: done\ndata: {\"finish_reason\":\"stop\"}\n\n")
	})
	var text strings.Builder
	for _, ev := range collect(t, cl.StreamChat(context.Background(), ChatRequest{})) {
		if d, ok := ev.(DeltaEvent); ok {
			text.WriteString(d.Text)
		}
	}
	got := text.String()
	if strings.ContainsAny(got, "\x1b\x07\x08\r\u009b\u202e") {
		t.Fatalf("control survived: %q", got)
	}
	for _, want := range []string{"safe ", "text ", "end ", "ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q in %q", want, got)
		}
	}
}
