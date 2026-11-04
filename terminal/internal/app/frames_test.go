package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// fakeGateway answers the routes the terminal uses. A chat replays script, one SSE
// block at a time, and holds at any entry "HOLD" until release is signalled.
type fakeGateway struct {
	*httptest.Server
	mu       sync.Mutex
	script   []string
	release  chan struct{}
	requests []client.ChatRequest
	boot     map[string]any
	status   map[string]any
	chats    []map[string]any
	code     int  // when set, every /v1 route answers with this status
	drop     bool // when set, every /v1 route closes the connection unanswered

	statusCode  int            // when set, the status route answers with this status
	chatCode    int            // when set, the chat route answers with this status
	chatError   map[string]any // the error body for chatCode
	convs       map[string]map[string]any
	privacyBody map[string]any
	modelsBody  map[string]any
	deleted     []string
	deletedAll  int
	deleteCode  int
	hits        map[string]int
}

func sse(event string, data any) string {
	body, _ := json.Marshal(data)
	return "event: " + event + "\ndata: " + string(body) + "\n\n"
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{release: make(chan struct{}, 8), convs: map[string]map[string]any{}, hits: map[string]int{}}
	g.boot = map[string]any{
		"application": map[string]any{"name": "Digital Commons", "version": "0.1.0", "stage": "beta", "notice": "Beta software. Not approved for production use or sensitive data."},
		"identity":    map[string]any{"user_id": "local", "display_name": "Local user", "auth_mode": "local"},
		"permissions": map[string]any{"advanced_mode": true, "manual_model_selection": true, "view_diagnostics": true, "search_documents": true, "allowed_tiers": []string{"fast", "standard", "advanced"}},
		"locality":    map[string]any{"kind": "this_device", "label": "On this computer", "description": "Your conversations stay on this computer."},
		"limits":      map[string]any{"max_message_chars": 15000, "max_attachments": 4, "max_attachment_bytes": 262144, "attachment_extensions": []string{".txt", ".md", ".csv", ".tsv"}},
		"suggestions": []map[string]any{
			{"id": "summarize", "title": "Summarize a document", "subtitle": "Get the key points quickly", "action": "attach", "prompt": "Summarize this document."},
			{"id": "compare", "title": "Compare two policies", "subtitle": "See what changed", "action": "attach_two", "prompt": "Compare these two documents."},
			{"id": "draft", "title": "Draft correspondence", "subtitle": "Letters, emails and memos", "action": "prompt", "prompt": "Help me draft a professional message to "},
			{"id": "explain", "title": "Explain a regulation", "subtitle": "In plain language", "action": "prompt", "prompt": "Explain this regulation: "},
			{"id": "find", "title": "Find information", "subtitle": "Search approved documents", "action": "documents", "prompt": ""},
			{"id": "spreadsheet", "title": "Analyze a spreadsheet", "subtitle": "Saved as CSV", "action": "attach", "prompt": "Analyze this spreadsheet."},
			{"id": "brainstorm", "title": "Brainstorm solutions", "subtitle": "Work through a problem", "action": "prompt", "prompt": "Help me brainstorm: "},
		},
	}
	g.status = map[string]any{
		"state": "ready", "reason": nil, "summary": "Ready", "locality": g.boot["locality"],
		"warmup":  map[string]any{"state": "ready", "message": ""},
		"gateway": map[string]any{"version": "0.1.0", "instance_id": "one", "uptime_s": 120, "address": "127.0.0.1:8000"},
		"inference": map[string]any{"provider": "ollama", "kind": "local", "reachable": true, "version": "0.40.13", "error": nil, "cloud_access_disabled": true,
			"missing_models": []any{}, "warmup_model": "Gemma4:12b", "loaded_models": []map[string]any{{"name": "Gemma4:12b", "size_bytes": 9000000000, "gpu_bytes": 0, "context_tokens": 8192}}},
	}
	now := time.Now().UTC()
	g.chats = []map[string]any{
		{"id": "c1", "title": "Remote work rules", "created_at": now.Format(time.RFC3339), "updated_at": now.Format(time.RFC3339), "message_count": 2},
		{"id": "c2", "title": "Budget memo draft for the finance committee meeting next quarter", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-2 * time.Hour).Format(time.RFC3339), "message_count": 4},
		{"id": "c3", "title": "Vendor letter", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-72 * time.Hour).Format(time.RFC3339), "message_count": 2},
		{"id": "c4", "title": "Snow removal contract", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-720 * time.Hour).Format(time.RFC3339), "message_count": 2},
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			g.mu.Lock()
			g.hits[r.URL.Path]++
			code, drop := g.code, g.drop
			g.mu.Unlock()
			if drop {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					conn.Close()
				}
				return
			}
			if code != 0 {
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "auth_required", "message": "Sign-in is required to use this service."}})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/v1/bootstrap", guard(func(w http.ResponseWriter, _ *http.Request) { g.mu.Lock(); defer g.mu.Unlock(); reply(w, g.boot) }))
	mux.HandleFunc("/v1/status", guard(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.statusCode != 0 {
			w.WriteHeader(g.statusCode)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "internal", "message": "Something went wrong. Please try again."}})
			return
		}
		reply(w, g.status)
	}))
	mux.HandleFunc("/v1/conversations", guard(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Method == http.MethodDelete {
			n := len(g.chats)
			g.chats = nil
			g.deletedAll++
			reply(w, map[string]any{"deleted": n})
			return
		}
		reply(w, map[string]any{"conversations": g.chats})
	}))
	mux.HandleFunc("/v1/conversations/", guard(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/conversations/")
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Method == http.MethodDelete {
			if g.deleteCode != 0 {
				w.WriteHeader(g.deleteCode)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "not_found", "message": "That item could not be found."}})
				return
			}
			g.deleted = append(g.deleted, id)
			for i, chat := range g.chats {
				if chat["id"] == id {
					g.chats = append(g.chats[:i:i], g.chats[i+1:]...)
					break
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if conv, ok := g.convs[id]; ok {
			reply(w, conv)
			return
		}
		reply(w, map[string]any{"id": "c1", "title": "Remote work rules", "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "What is the remote work policy?", "created_at": "2026-11-02T09:00:00Z", "attachments": []any{}},
			{"id": "m2", "role": "assistant", "content": "Employees may work remotely after probation.", "created_at": "2026-11-02T09:00:30Z", "finish_reason": "cancelled", "citations": []any{}},
		}})
	}))
	mux.HandleFunc("/v1/privacy", guard(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.privacyBody != nil {
			reply(w, g.privacyBody)
			return
		}
		reply(w, map[string]any{
			"locality": g.boot["locality"],
			"statements": []map[string]any{
				{"id": "processing", "label": "Where your messages go", "value": "On this computer", "status": "ok", "detail": "provider ollama at 127.0.0.1:11434"},
				{"id": "retention", "label": "How long chats are kept", "value": "90 days", "status": "info", "detail": "retention_days=90"},
				{"id": "web", "label": "Internet access", "value": "None for the assistant", "status": "ok", "detail": ""},
			},
			"notice": "This is a beta. Do not enter sensitive information.",
		})
	}))
	mux.HandleFunc("/v1/models", guard(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.modelsBody != nil {
			reply(w, g.modelsBody)
			return
		}
		reply(w, map[string]any{"provider": "ollama", "tiers": []map[string]any{
			{"tier": "fast", "label": "Fast", "description": "Quick", "enabled": true, "available": true, "model": "Gemma4:4b", "context_tokens": 4096},
			{"tier": "standard", "label": "Standard", "description": "Balanced", "enabled": true, "available": true, "model": "Gemma4:12b", "context_tokens": 8192},
			{"tier": "advanced", "label": "Advanced", "description": "Thorough", "enabled": true, "available": false, "unavailable_reason": "Not installed", "model": "Gemma4:26b", "context_tokens": 16384},
		}})
	}))
	mux.HandleFunc("/v1/chat/stream", guard(func(w http.ResponseWriter, r *http.Request) {
		var req client.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		g.requests = append(g.requests, req)
		script := g.script
		chatCode, chatError := g.chatCode, g.chatError
		g.mu.Unlock()
		if chatCode != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(chatCode)
			json.NewEncoder(w).Encode(map[string]any{"error": chatError})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, block := range script {
			if block == "HOLD" {
				select {
				case <-g.release:
				case <-r.Context().Done():
					return
				}
				continue
			}
			fmt.Fprint(w, block)
			flusher.Flush()
		}
	}))
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// driver feeds a Model the way the Bubble Tea loop does, with no terminal.
type driver struct {
	t    *testing.T
	m    *Model
	msgs chan tea.Msg
	quit bool
	seen []string // the type of every message applied
	clip *fakeClipboard
}

func (d *driver) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		defer func() { recover() }()
		d.msgs <- cmd()
	}()
}

func (d *driver) send(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.QuitMsg:
		d.quit = true
	case tea.BatchMsg:
		for _, cmd := range msg {
			d.start(cmd)
		}
	default:
		d.seen = append(d.seen, fmt.Sprintf("%T", msg))
		_, cmd := d.m.Update(msg)
		d.start(cmd)
	}
}

func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// settle applies whatever arrives for a short while.
func (d *driver) settle(wait time.Duration) {
	deadline := time.After(wait)
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			return
		}
	}
}

func (d *driver) resize(w, h int) {
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	d.m.resizing = false
	d.m.relayout()
	d.m.transcript.Rebuild()
}

func (d *driver) key(s string) {
	if msg, ok := namedKey(s); ok {
		d.send(msg)
		return
	}
	for _, r := range s {
		d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (d *driver) ctrl(r rune) { d.send(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}) }

func (d *driver) text() string {
	return ansi.Strip(d.m.View().Content)
}

var frameOut struct {
	sync.Mutex
	b strings.Builder
}

// capture records the frame, framed by rulers, and checks that it fits.
func (d *driver) capture(name string) {
	d.t.Helper()
	view := d.m.View()
	plain := strings.Split(ansi.Strip(view.Content), "\n")
	if len(plain) != d.m.height {
		d.t.Errorf("%s: %d lines, want %d", name, len(plain), d.m.height)
	}
	frameOut.Lock()
	defer frameOut.Unlock()
	fmt.Fprintf(&frameOut.b, "\n=== %s (%dx%d) cursor=%v\n", name, d.m.width, d.m.height, view.Cursor != nil)
	for i, line := range plain {
		if w := ansi.StringWidth(line); w > d.m.width {
			d.t.Errorf("%s: line %d is %d wide, window %d", name, i, w, d.m.width)
		}
		frameOut.b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

const sampleReply = `Employees may work remotely after completing their probationary period, with supervisor approval [1].

## Requirements

- A signed **agreement** on file
- A secure connection, see [the guide](https://intranet.example.gov/remote)

| Item | Owner |
|---|---|
| Laptop | IT |

` + "```go\nfunc handle(w http.ResponseWriter) {\n\tfmt.Fprintln(w, \"ok\")\n}\n```\n\nThat is all."

func replyScript(extra ...string) []string {
	script := []string{
		sse("start", map[string]any{"request_id": "r1", "conversation_id": "new1", "message_id": "m9", "title": "Remote work eligibility",
			"route":   map[string]any{"requested_tier": "auto", "tier": "standard", "locality": "this_device", "model": "Gemma4:12b", "provider": "ollama", "reason": "Code markers present"},
			"context": map[string]any{"estimated_tokens": 1240, "budget_tokens": 6144, "omitted_messages": 0}, "notices": []any{}}),
		sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "Remote Work Policy", "section": "Eligibility", "department": "Human Resources", "version": "2.1", "document_date": "2026-03-01"}}}),
		sse("status", map[string]any{"phase": "generating", "message": "Writing a reply on this computer"}),
	}
	script = append(script, extra...)
	return script
}

func TestPrintFrames(t *testing.T) {
	out := os.Getenv("DC_FRAMES_OUT")
	if out == "" {
		t.Skip("set DC_FRAMES_OUT to write the frames to a file")
	}
	t.Cleanup(func() { os.WriteFile(out, []byte(frameOut.b.String()), 0o600) })

	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.until("chats", func() bool { return len(d.m.sidebar.Chats) > 0 })
	d.capture("basic home")
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 20}, {50, 16}, {40, 12}, {40, 10}, {30, 8}} {
		d.resize(size[0], size[1])
		d.capture("basic home")
	}
	d.resize(100, 30)

	d.key("/")
	d.capture("palette")
	d.key("t")
	d.capture("palette t")
	d.key("esc")
	d.ctrl('u')
	d.m.composer.Reset()
	d.send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	d.m.composer.Reset()

	g.script = replyScript("HOLD", sse("delta", map[string]any{"text": sampleReply}), "HOLD",
		sse("done", map[string]any{"message_id": "m9", "finish_reason": "stop", "usage": map[string]any{"prompt_tokens": 152, "completion_tokens": 388, "total_tokens": 540, "first_token_ms": 2210, "total_ms": 61000, "load_ms": 78, "tokens_per_second": 6.4}}))
	d.key("What is our remote work eligibility policy?")
	d.key("enter")
	d.settle(50 * time.Millisecond)
	d.capture("waiting")
	g.release <- struct{}{}
	d.until("text", func() bool { return strings.Contains(d.m.last.reply.Text, "That is all") })
	d.settle(150 * time.Millisecond)
	d.capture("streaming")
	g.release <- struct{}{}
	d.until("done", func() bool { return d.m.active == nil })
	d.capture("basic conversation")
	d.resize(60, 20)
	d.capture("basic conversation")
	d.resize(100, 30)
	d.send(tea.MouseWheelMsg(tea.Mouse{X: 50, Y: 10, Button: tea.MouseWheelUp}))
	d.capture("scrolled up")
	d.m.transcript.GotoBottom()

	d.key("f1")
	d.capture("help")
	d.key("esc")
	d.key("f2")
	d.capture("settings")
	d.key("esc")
	d.key("/")
	d.key("privacy")
	d.key("enter")
	d.settle(50 * time.Millisecond)
	d.capture("privacy")
	d.key("esc")
	d.key("/status")
	d.key("enter")
	d.capture("status")
	d.key("esc")
	d.key("/delete")
	d.key("enter")
	d.capture("confirm")
	d.key("esc")
	d.ctrl('o')
	d.settle(50 * time.Millisecond)
	d.capture("files")
	d.key("esc")

	d.key("/mode developer")
	d.key("enter")
	d.settle(50 * time.Millisecond)
	d.resize(130, 40)
	d.capture("advanced")
	d.resize(100, 30)
	d.capture("advanced")
	d.resize(70, 24)
	d.capture("advanced")
	d.resize(130, 40)
	d.key("/model")
	d.key("enter")
	d.settle(50 * time.Millisecond)
	d.capture("model picker")
	d.key("esc")
	d.key("/status")
	d.key("enter")
	d.capture("advanced status")
	d.key("esc")
	d.ctrl('n')
	d.settle(20 * time.Millisecond)
	d.capture("advanced home")
	d.key("/mode basic")
	d.key("enter")
	d.settle(20 * time.Millisecond)

	d.resize(100, 30)
	d.send(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	d.capture("sidebar hidden")
	d.send(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})

	d.resize(70, 24)
	d.send(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	d.capture("sidebar overlay")
	d.key("esc")
	d.resize(100, 30)

	g.Close()
	d.until("down", func() bool {
		d.m.handleStatus(statusMsg{err: &client.Error{Kind: client.KindNetwork, Message: "x"}})
		return d.m.conn == connDown
	})
	d.m.status = nil
	d.capture("not connected")
}

func TestPrintErrorFrames(t *testing.T) {
	out := os.Getenv("DC_FRAMES_OUT")
	if out == "" {
		t.Skip("set DC_FRAMES_OUT to write the frames to a file")
	}
	t.Cleanup(func() { os.WriteFile(out, []byte(frameOut.b.String()), 0o600) })

	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.until("chats", func() bool { return len(d.m.sidebar.Chats) > 0 })

	// Stop part way through a reply.
	g.script = replyScript(sse("delta", map[string]any{"text": "Employees may work remotely"}), "HOLD")
	d.key("How long is probation?")
	d.key("enter")
	d.until("text", func() bool { return d.m.last.reply.Text != "" })
	d.key("esc")
	d.until("stopped", func() bool { return d.m.active == nil })
	d.capture("stopped")

	// Lost connection after start: the stream ends with no final event.
	g.script = replyScript(sse("delta", map[string]any{"text": "Partial answer"}))
	d.key("Another question")
	d.key("enter")
	d.until("lost", func() bool { return d.m.active == nil })
	d.capture("lost")

	// A gateway error, before start, that can be retried.
	g.script = []string{sse("error", map[string]any{"code": "provider_unavailable", "message": "The AI service isn't responding.", "retryable": true})}
	d.key("Third question")
	d.key("enter")
	d.until("error", func() bool { return d.m.active == nil })
	d.capture("error inline")

	// conversation_locality_changed offers New chat.
	g.script = []string{sse("error", map[string]any{"code": "conversation_locality_changed", "message": "This chat was started somewhere else. Start a new chat to continue.", "retryable": false})}
	d.key("Fourth question")
	d.key("enter")
	d.until("error", func() bool { return d.m.active == nil })
	d.capture("locality changed")

	// Composer with chips and many lines.
	d.ctrl('n')
	d.settle(20 * time.Millisecond)
	d.m.attachments = append(d.m.attachments, attachFile(client.Attachment{Name: "policy.txt"}), attachFile(client.Attachment{Name: "a very long file name for the procurement review.csv"}))
	d.m.useDocs = true
	d.m.syncChips()
	d.send(tea.PasteMsg{Content: "one\r\ntwo\nthree\nfour\nfive\nsix\nseven\neight"})
	d.capture("chips and lines")
	d.resize(60, 16)
	d.capture("chips and lines")
	d.resize(100, 30)
	d.m.composer.Reset()
	d.m.attachments, d.m.useDocs = nil, false
	d.m.syncChips()

	// Reopen a stopped chat.
	d.m.View()
	for _, zone := range d.m.zones {
		if zone.Action.Kind == "open" {
			d.send(tea.MouseClickMsg(tea.Mouse{X: zone.X + 2, Y: zone.Y, Button: tea.MouseLeft}))
			break
		}
	}
	d.until("opened", func() bool { return len(d.m.transcript.Messages) > 0 })
	d.capture("reopened")

	// A slow wait with reasoning, Advanced.
	d.key("/mode developer")
	d.key("enter")
	d.resize(130, 30)
	g.script = replyScript(sse("thinking", map[string]any{"text": "The user is asking about probation. I should check the handbook."}), "HOLD", sse("delta", map[string]any{"text": "Six months."}), "HOLD",
		sse("done", map[string]any{"message_id": "m9", "finish_reason": "length", "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "tokens_per_second": 2.0, "estimated": true}}))
	d.key("/code")
	d.key("enter")
	d.key("And after that?")
	d.key("enter")
	d.settle(80 * time.Millisecond)
	d.capture("advanced waiting")
	g.release <- struct{}{}
	d.settle(150 * time.Millisecond)
	d.capture("advanced streaming")
	g.release <- struct{}{}
	d.until("done", func() bool { return d.m.active == nil })
	d.capture("advanced finished")

	// 401, then a drop in Basic and Advanced.
	g.mu.Lock()
	g.code = 401
	g.mu.Unlock()
	d.send(pollMsg{d.m.pollGen})
	d.until("refused", func() bool { return d.m.conn == connAuth })
	d.send(clockMsg{})
	d.capture("advanced 401")
	d.key("/mode basic")
	d.key("enter")
	d.ctrl('n')
	d.resize(100, 30)
	d.m.notice = ""
	d.send(clockMsg{})
	d.capture("basic 401")

	g.mu.Lock()
	g.code = 0
	g.status["state"] = "unavailable"
	g.status["reason"] = "model_missing"
	g.mu.Unlock()
	d.settle(3200 * time.Millisecond)
	d.send(clockMsg{})
	d.capture("basic not set up")
}

// TestRealProgram runs the actual Bubble Tea loop, with real bytes through the
// input decoder, to prove the program starts, draws, sends and quits.
func TestRealProgram(t *testing.T) {
	g := newFakeGateway(t)
	g.script = replyScript(sse("delta", map[string]any{"text": "Hello from the fake."}),
		sse("done", map[string]any{"message_id": "m9", "finish_reason": "stop", "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}}))

	cfg := config.Default()
	cfg.Gateway.BaseURL = g.URL
	c := client.New(client.Options{BaseURL: g.URL, Token: "test"})
	m := New(Options{Config: cfg, Version: "test", Client: c, HomeDir: t.TempDir(), Clipboard: Clipboard{
		Write: func(string) error { return nil }, Read: func() (string, error) { return "", nil },
	}})

	in, keys := io.Pipe()
	var output syncBuffer
	rec := &recorder{Model: m}
	program := tea.NewProgram(rec, tea.WithInput(in), tea.WithOutput(&output), tea.WithWindowSize(100, 30),
		tea.WithoutSignals(), tea.WithColorProfile(colorprofile.TrueColor), tea.WithFilter(Filter))
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	wait := func(what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(rec.text(), what) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait("What can I help you with?")
	keys.Write([]byte("Hello there"))
	wait("Hello there")
	keys.Write([]byte("\r"))
	wait("Hello from the fake.")
	keys.Write([]byte{3})
	wait("Press Ctrl+C again to quit.")
	keys.Write([]byte{3})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not quit")
	}
	if !strings.Contains(g.requests[0].Message, "Hello there") {
		t.Fatalf("sent %+v", g.requests)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
