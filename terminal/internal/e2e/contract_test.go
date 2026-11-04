package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
)

// Fields the wire may leave out. Everything else must appear in both directions.
var mayBeAbsent = map[string]bool{
	"Citation.withheld": true, // sent only on a citation the caller can no longer see
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Detail    string `json:"detail"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

type errorEvent struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}

type conversationList struct {
	Conversations []client.ConversationSummary `json:"conversations"`
}

type deletedCount struct {
	Deleted int `json:"deleted"`
}

var eventTypes = map[string]reflect.Type{
	"start":     reflect.TypeFor[client.StartEvent](),
	"citations": reflect.TypeFor[client.CitationsEvent](),
	"notices":   reflect.TypeFor[client.NoticesEvent](),
	"status":    reflect.TypeFor[client.StatusEvent](),
	"thinking":  reflect.TypeFor[client.ThinkingEvent](),
	"delta":     reflect.TypeFor[client.DeltaEvent](),
	"done":      reflect.TypeFor[client.DoneEvent](),
	"error":     reflect.TypeFor[errorEvent](),
}

// jsonFields lists a struct's JSON names, following embedded structs.
func jsonFields(typ reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous {
			for name, ft := range jsonFields(f.Type) {
				fields[name] = ft
			}
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields[name] = f.Type
		}
	}
	return fields
}

// compare walks a decoded JSON value against a Go type. It reports fields only one
// side has, and values that the Go type could not hold.
func compare(t *testing.T, where string, value any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch v := value.(type) {
	case nil:
		// encoding/json leaves the zero value, which the wire types document.
	case string:
		if typ.Kind() != reflect.String {
			t.Errorf("%s: a string on the wire, %s in Go", where, typ)
		}
	case bool:
		if typ.Kind() != reflect.Bool {
			t.Errorf("%s: a boolean on the wire, %s in Go", where, typ)
		}
	case float64:
		switch typ.Kind() {
		case reflect.Int, reflect.Int64:
			if v != float64(int64(v)) {
				t.Errorf("%s: %v on the wire, %s in Go", where, v, typ)
			}
		case reflect.Float64:
		default:
			t.Errorf("%s: a number on the wire, %s in Go", where, typ)
		}
	case []any:
		if typ.Kind() != reflect.Slice {
			t.Errorf("%s: a list on the wire, %s in Go", where, typ)
			return
		}
		for i, item := range v {
			compare(t, fmt.Sprintf("%s[%d]", where, i), item, typ.Elem())
		}
	case map[string]any:
		if typ.Kind() != reflect.Struct {
			t.Errorf("%s: an object on the wire, %s in Go", where, typ)
			return
		}
		fields := jsonFields(typ)
		for name, item := range v {
			ft, ok := fields[name]
			if !ok {
				t.Errorf("%s.%s: on the wire, but %s has no such field", where, name, typ.Name())
				continue
			}
			compare(t, where+"."+name, item, ft)
		}
		for name := range fields {
			if _, ok := v[name]; !ok && !mayBeAbsent[typ.Name()+"."+name] {
				t.Errorf("%s.%s: in %s, but absent from the wire", where, name, typ.Name())
			}
		}
	}
}

func compareJSON(t *testing.T, where string, data []byte, typ reflect.Type) {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v\n%s", where, err, data)
	}
	compare(t, where, value, typ)
}

// The worked examples in docs/api.md must match the Go types too, so the three
// stay the same contract.
func TestContractDocument(t *testing.T) {
	requireE2E(t)
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]reflect.Type{
		"### Errors":                       reflect.TypeFor[errorEnvelope](),
		"### `GET /v1/bootstrap`":          reflect.TypeFor[client.Bootstrap](),
		"### `GET /v1/status`":             reflect.TypeFor[client.Status](),
		"### `GET /v1/models`":             reflect.TypeFor[client.Models](),
		"### `GET /v1/privacy`":            reflect.TypeFor[client.Privacy](),
		"### `POST /v1/chat/stream`":       reflect.TypeFor[client.ChatRequest](),
		"### `GET /v1/conversations`":      reflect.TypeFor[conversationList](),
		"### `GET /v1/conversations/{id}`": reflect.TypeFor[client.Message](),
	}
	seen := 0
	heading, fence := "", ""
	var body []string
	for line := range strings.Lines(strings.ReplaceAll(string(data), "\r\n", "\n")) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case fence == "" && strings.HasPrefix(line, "#"):
			heading = line
		case fence == "" && strings.HasPrefix(line, "```"):
			fence, body = strings.TrimPrefix(line, "```")+" ", nil
		case fence != "" && line == "```":
			if typ, ok := blocks[heading]; ok && strings.HasPrefix(fence, "json") {
				compareJSON(t, "api.md "+heading, []byte(strings.Join(body, "\n")), typ)
				seen++
			}
			if heading == "### Stream format" {
				var event string
				for _, b := range body {
					if name, ok := strings.CutPrefix(b, "event: "); ok {
						event = name
					} else if payload, ok := strings.CutPrefix(b, "data: "); ok {
						compareJSON(t, "api.md event "+event, []byte(payload), eventTypes[event])
						seen++
					}
				}
			}
			fence = ""
		case fence != "":
			body = append(body, line)
		}
	}
	if seen < 8+len(eventTypes) {
		t.Errorf("only %d examples were found in docs/api.md", seen)
	}
}

// api is a bare HTTP caller, so raw responses can be compared before any decoding.
type api struct {
	t     *testing.T
	base  string
	token string
}

func (a *api) do(method, path string, body any) (*http.Response, []byte) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		a.t.Errorf("%s %s has no X-Request-ID", method, path)
	}
	return resp, data
}

func (a *api) expect(status int, typ reflect.Type, method, path string, body any) []byte {
	a.t.Helper()
	resp, data := a.do(method, path, body)
	if resp.StatusCode != status {
		a.t.Fatalf("%s %s: HTTP %d, wanted %d\n%s", method, path, resp.StatusCode, status, data)
	}
	compareJSON(a.t, method+" "+path, data, typ)
	return data
}

type sseEvent struct {
	name string
	data []byte
}

// stream posts one turn and reads every event, checking the framing as it goes. A
// stop function, if given, ends the turn by closing the connection at the first delta.
func (a *api) stream(body client.ChatRequest, stop bool) []sseEvent {
	a.t.Helper()
	if body.Attachments == nil {
		body.Attachments = []client.Attachment{}
	}
	if body.Tier == "" {
		body.Tier = "auto"
	}
	if body.Assistant == "" {
		body.Assistant = "general"
	}
	data, err := json.Marshal(body)
	if err != nil {
		a.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/v1/chat/stream", bytes.NewReader(data))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		failure, _ := io.ReadAll(resp.Body)
		a.t.Fatalf("chat: HTTP %d\n%s", resp.StatusCode, failure)
	}
	if kind := resp.Header.Get("Content-Type"); !strings.HasPrefix(kind, "text/event-stream") {
		a.t.Errorf("chat Content-Type is %q", kind)
	}

	var events []sseEvent
	var name string
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			return events
		}
		if err != nil {
			a.t.Fatal(err)
		}
		if strings.Contains(line, "\r") {
			a.t.Errorf("a stream line has a carriage return: %q", line)
		}
		line = strings.TrimSuffix(line, "\n")
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			name = value
		} else if value, ok := strings.CutPrefix(line, "data: "); ok {
			typ, known := eventTypes[name]
			if !known {
				a.t.Fatalf("unknown event %q", name)
			}
			compareJSON(a.t, "event "+name, []byte(value), typ)
			events = append(events, sseEvent{name, []byte(value)})
			if stop && name == "delta" {
				return events
			}
		}
	}
}

func eventNames(events []sseEvent) []string {
	var names []string
	for _, e := range events {
		if len(names) == 0 || names[len(names)-1] != e.name {
			names = append(names, e.name)
		}
	}
	return names
}

func hasEvent(events []sseEvent, name string) bool {
	return slices.ContainsFunc(events, func(e sseEvent) bool { return e.name == name })
}

// assertOrder checks the sequence docs/api.md promises.
func assertOrder(t *testing.T, events []sseEvent) {
	t.Helper()
	if len(events) == 0 || events[0].name != "start" {
		t.Fatalf("the stream does not begin with start: %v", eventNames(events))
	}
	final := 0
	for i, e := range events {
		switch e.name {
		case "start":
			if i != 0 {
				t.Errorf("start at position %d", i)
			}
		case "citations":
			if i != 1 {
				t.Errorf("citations at position %d", i)
			}
		case "done", "error":
			final++
			if i != len(events)-1 {
				t.Errorf("%s at position %d of %d", e.name, i, len(events))
			}
		}
	}
	if final != 1 {
		t.Errorf("%d final events: %v", final, eventNames(events))
	}
}

var isoTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

func assertTimes(t *testing.T, where string, times ...string) {
	t.Helper()
	for _, value := range times {
		if !isoTime.MatchString(value) {
			t.Errorf("%s: %q is not UTC ISO 8601", where, value)
		}
	}
}

// Real responses from the real gateway, field by field, in both directions.
func TestContract(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.start()
	token, err := os.ReadFile(filepath.Join(dir, "local-token"))
	if err != nil {
		t.Fatal(err)
	}
	a := &api{t: t, base: gw.url(), token: strings.TrimSpace(string(token))}

	a.expect(200, reflect.TypeFor[client.Health](), "GET", "/healthz", nil)
	a.expect(200, reflect.TypeFor[client.Bootstrap](), "GET", "/v1/bootstrap", nil)
	a.expect(200, reflect.TypeFor[client.Status](), "GET", "/v1/status", nil)
	a.expect(200, reflect.TypeFor[client.Models](), "GET", "/v1/models", nil)
	a.expect(200, reflect.TypeFor[client.Privacy](), "GET", "/v1/privacy", nil)

	envelope := reflect.TypeFor[errorEnvelope]()
	a.expect(404, envelope, "GET", "/v1/conversations/absent", nil)
	a.expect(404, envelope, "DELETE", "/v1/conversations/absent", nil)
	a.expect(422, envelope, "GET", "/v1/conversations/has%20space", nil)
	a.expect(422, envelope, "GET", "/v1/conversations?limit=0", nil)
	a.expect(422, envelope, "POST", "/v1/chat/stream", map[string]any{"message": ""})
	a.expect(413, envelope, "POST", "/v1/chat/stream", map[string]any{"message": strings.Repeat("x", 15001)})
	a.expect(415, envelope, "POST", "/v1/chat/stream", map[string]any{
		"attachments": []client.Attachment{{Name: "a.exe", MediaType: "text/plain", Text: "x"}},
	})
	anonymous := &api{t: t, base: gw.url()}
	anonymous.expect(401, envelope, "GET", "/v1/status", nil)

	// Citations, generating status and done, through the approved documents.
	first := a.stream(client.ChatRequest{Message: question, UseDocuments: true}, false)
	assertOrder(t, first)
	for _, name := range []string{"citations", "status", "delta", "done"} {
		if !hasEvent(first, name) {
			t.Errorf("no %s event: %v", name, eventNames(first))
		}
	}
	var start client.StartEvent
	if err := json.Unmarshal(first[0].data, &start); err != nil {
		t.Fatal(err)
	}
	id := start.ConversationID

	// Thinking text, on the same conversation.
	thinking := a.stream(client.ChatRequest{ConversationID: &id, Message: "Think it through.", Tier: "advanced"}, false)
	assertOrder(t, thinking)
	if !hasEvent(thinking, "thinking") {
		t.Errorf("no thinking event: %v", eventNames(thinking))
	}

	// A long attachment is cut, which raises a notice and the reading status.
	long := strings.Repeat("A sentence of policy text for the attachment. ", 3000)
	attached := a.stream(client.ChatRequest{
		Message:     "Summarize this.",
		Attachments: []client.Attachment{{Name: "policy.txt", MediaType: "text/plain", Text: long}},
	}, false)
	assertOrder(t, attached)
	var cut client.StartEvent
	if err := json.Unmarshal(attached[0].data, &cut); err != nil {
		t.Fatal(err)
	}
	if len(cut.Notices) == 0 || cut.Notices[0].Code != "attachment_truncated" {
		t.Errorf("the notices were %+v", cut.Notices)
	}
	if !slices.ContainsFunc(attached, func(e sseEvent) bool {
		return e.name == "status" && bytes.Contains(e.data, []byte(`"phase":"reading"`))
	}) {
		t.Errorf("no reading status: %v", eventNames(attached))
	}
	cutID := cut.ConversationID

	// Regenerate replaces the reply and keeps the conversation.
	again := a.stream(client.ChatRequest{ConversationID: &id, Regenerate: true}, false)
	assertOrder(t, again)
	if !hasEvent(again, "done") {
		t.Errorf("regenerate did not finish: %v", eventNames(again))
	}

	// Closing the connection stops the turn. The partial text is kept and marked.
	stopped := a.stream(client.ChatRequest{Message: "Stop this one."}, true)
	if err := json.Unmarshal(stopped[0].data, &start); err != nil {
		t.Fatal(err)
	}
	stoppedID := start.ConversationID
	var stored client.Conversation
	deadline := time.Now().Add(10 * time.Second)
	for {
		data := a.expect(200, reflect.TypeFor[client.Conversation](), "GET", "/v1/conversations/"+stoppedID, nil)
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Messages) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(stored.Messages) != 2 || stored.Messages[1].FinishReason != "cancelled" {
		t.Errorf("the stopped turn is stored as %+v", stored.Messages)
	}

	data := a.expect(200, reflect.TypeFor[client.Conversation](), "GET", "/v1/conversations/"+id, nil)
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	assertTimes(t, "conversation", stored.CreatedAt, stored.UpdatedAt)
	if len(stored.Messages) != 4 {
		t.Errorf("regenerate left %d messages", len(stored.Messages))
	}
	for _, m := range stored.Messages {
		assertTimes(t, "message", m.CreatedAt)
		if m.Role == "assistant" && (m.Route == nil || m.Usage == nil) {
			t.Errorf("an assistant message has no route or usage: %+v", m)
		}
	}

	data = a.expect(200, reflect.TypeFor[client.Conversation](), "GET", "/v1/conversations/"+cutID, nil)
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.Messages[0].Attachments; len(got) != 1 || got[0].Chars == 0 {
		t.Errorf("the attachment is stored as %+v", got)
	}
	if bytes.Contains(data, []byte(`"text"`)) {
		t.Error("attachment text came back from the API")
	}

	a.expect(200, reflect.TypeFor[conversationList](), "GET", "/v1/conversations?limit=5", nil)
	if resp, body := a.do("DELETE", "/v1/conversations/"+cutID, nil); resp.StatusCode != 204 || len(body) != 0 {
		t.Errorf("delete: HTTP %d %q", resp.StatusCode, body)
	}
	a.expect(200, reflect.TypeFor[deletedCount](), "DELETE", "/v1/conversations", nil)
}
