package app

import (
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// A reply with everything the renderer can draw: headings, lists, a link, a table,
// code spans, a framed code block and a citation.
const richReply = `Employees may work remotely after completing their probationary period, with supervisor approval [1].

## Requirements

1. A signed **agreement** on file, as described in *Section 4*.
2. A secure connection, see [the guide](https://intranet.example.gov/remote/guide).

> Note the ` + "`vpn`" + ` setting.

| Item | Owner | Notes |
|---|---|---|
| Laptop | IT | Issued on the first day of remote work |
| Phone | Facilities | On request |

` + "```go\nfunc handle(w http.ResponseWriter) {\n\tfmt.Fprintln(w, \"ok\") // a comment\n}\n```\n\n```\nplain block\n```\n\nThat is all."

// storedChat is a saved conversation whose last reply ended with the given reason.
func storedChat(id, reason string) map[string]any {
	return map[string]any{"id": id, "title": "Saved chat", "assistant": "code", "messages": []map[string]any{
		{"id": "m1", "role": "user", "content": "What is the remote work policy?", "created_at": "2026-11-02T09:00:00Z",
			"attachments": []map[string]any{{"name": "policy.txt", "media_type": "text/plain"}}},
		{"id": "m2", "role": "assistant", "content": "Employees may work remotely after probation.", "created_at": "2026-11-02T09:00:30Z", "finish_reason": reason,
			"citations": []map[string]any{{"number": 1, "title": "Remote Work Policy", "section": "Eligibility", "department": "Human Resources", "version": "2.1", "document_date": "2026-03-01"}}},
	}}
}

// visit is one state of the interface, reached by the walk.
type visit func(state string)

// walk takes the application through every state the suites care about, calling
// see in each one. The application keeps its mode. It leaves a clean home screen.
func walk(d *driver, g *fakeGateway, see visit) {
	m := d.m
	d.t.Helper()

	// Home.
	see("home")
	g.set(func() { g.script = nil })
	boot := *m.boot
	m.boot.Suggestions = nil
	d.send(struct{}{})
	see("home without suggestions")
	m.boot.Suggestions = boot.Suggestions
	saved := m.sidebar.Chats
	m.sidebar.Chats = nil
	see("home with no chats")
	m.sidebar.Chats = saved
	d.m.sideHidden = true
	d.send(struct{}{})
	see("sidebar hidden")
	d.m.sideHidden = false
	d.m.sideOverlay = true
	d.send(struct{}{})
	see("sidebar overlay")
	d.key("esc")

	// The palette.
	d.key("/")
	see("palette")
	d.key("t")
	see("palette filtered")
	d.key("h")
	d.key("e")
	d.key("m")
	d.key("e")
	d.key(" ")
	see("palette arguments")
	d.key("esc")
	d.clearComposer()

	// Panels.
	d.key("f1")
	see("help")
	d.keys("pgdown", "pgdown", "pgdown")
	see("help scrolled")
	d.key("esc")
	d.key("f2")
	see("settings")
	d.key("esc")
	d.submit("/status")
	see("status")
	d.key("esc")
	d.submit("/privacy")
	d.until("privacy", func() bool { return m.privacy != nil && !m.topPanel().loading })
	see("privacy")
	d.key("esc")
	if m.advanced() {
		d.submit("/model")
		d.until("models", func() bool { return m.models != nil && !m.topPanel().loading })
		see("model picker")
		d.key("esc")
		d.key("f3")
		see("inspector")
		d.key("f3")
		m.inspectorTop = true
		see("inspector overlay")
		d.key("esc")
	}
	d.key("ctrl+o")
	d.until("files", func() bool { return m.picker != nil && m.picker.Dir != "" })
	d.settle(5 * time.Millisecond)
	see("file picker")
	d.key("esc")
	d.do("delete-all")
	see("confirm")
	d.key("esc")
	d.settle(5 * time.Millisecond)
	d.closeAll()

	// The composer.
	m.attachments = append(m.attachments, attachFile(client.Attachment{Name: "policy.txt"}), attachFile(client.Attachment{Name: "a very long file name for the procurement review of the year.csv"}))
	m.useDocs = true
	m.syncChips()
	d.send(struct{}{})
	see("chips")
	d.send(tea.PasteMsg{Content: "one\r\ntwo\nthree\nfour\nfive\nsix\nseven\neight"})
	see("chips and many lines")
	d.clearComposer()
	m.attachments, m.useDocs = nil, false
	m.syncChips()
	d.send(struct{}{})
	m.setWarning("That file could not be read.")
	d.send(struct{}{})
	see("notice")
	m.notice = ""

	// A reply, from waiting to finished.
	g.set(func() {
		g.script = []string{startOnly("new1", map[string]any{"code": "history_trimmed", "message": "Earlier parts of this conversation were left out to make room."}),
			"HOLD", sse("status", map[string]any{"phase": "generating", "message": "Writing a response."}),
			sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "Remote Work Policy", "section": "Eligibility", "department": "Human Resources", "version": "2.1", "document_date": "2026-03-01"}}}),
			"HOLD", sse("thinking", map[string]any{"text": "The user asks about remote work, so I should find the policy."}), sse("delta", map[string]any{"text": richReply}), "HOLD", doneEvent("stop")}
	})
	d.submit("What is our remote work eligibility policy, and does it apply to contractors who work from home part of the week?")
	d.until("start", func() bool { return m.active != nil && m.active.started })
	d.settle(120 * time.Millisecond)
	see("waiting")
	g.release <- struct{}{}
	d.until("status", func() bool { return m.active.reply.Status != "" })
	d.settle(120 * time.Millisecond)
	see("waiting with status")
	g.release <- struct{}{}
	d.until("text", func() bool { return strings.Contains(m.active.reply.Text, "That is all") })
	d.settle(120 * time.Millisecond)
	see("streaming")
	g.release <- struct{}{}
	d.finish()
	see("conversation")
	d.key("pgup")
	d.send(struct{}{})
	see("scrolled up")
	m.transcript.GotoBottom()

	// Failures.
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Employees may work remotely"}), "HOLD")
	})
	d.submit("How long is probation?")
	d.until("text", func() bool { return m.active != nil && m.active.reply.Text != "" })
	d.key("esc")
	d.finish()
	see("stopped")
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Partial answer"})) })
	d.submit("Another question")
	d.finish()
	see("lost connection")
	g.set(func() {
		g.script = []string{errorEvent("provider_unavailable", "The AI service isn't responding. Please try again in a moment.", true)}
	})
	d.submit("Third question")
	d.finish()
	see("error with try again")
	g.set(func() {
		g.script = []string{errorEvent("conversation_locality_changed", "This conversation began in a different place. Please start a new chat.", false)}
	})
	d.submit("Fourth question")
	d.finish()
	see("locality changed")
	d.key("ctrl+n")
	d.settle(5 * time.Millisecond)
	d.clearComposer()

	// A reopened chat.
	g.set(func() { g.convs["c3"] = storedChat("c3", "length") })
	d.do2("open", "c3")
	d.until("opened", func() bool { return len(m.transcript.Messages) > 0 })
	see("reopened")
	d.key("ctrl+n")
	d.settle(5 * time.Millisecond)

	// Connection states.
	status := *m.status
	inference := *status.Inference
	inference.MissingModels = []client.MissingModel{{Tier: "standard", Model: "Gemma4:12b", InstallHint: "ollama pull Gemma4:12b"}}
	inference.Error = "connection refused"
	status.Inference = &inference
	for _, state := range []struct{ name, state, reason string }{
		{"starting", "starting", ""},
		{"not responding", "unavailable", "provider_unreachable"},
		{"not set up", "unavailable", "model_missing"},
		{"load failed", "unavailable", "model_load_failed"},
		{"cloud on", "unavailable", "cloud_access_enabled"},
		{"degraded", "degraded", ""},
	} {
		now := status
		now.State, now.Reason, now.Summary = state.state, state.reason, "Working, with limits."
		m.status = &now
		d.send(struct{}{})
		see(state.name)
	}
	m.status.State, m.status.Reason = "ready", ""
	m.status.Locality = client.Locality{Kind: "demo", Label: "Demonstration"}
	d.send(struct{}{})
	see("demo")
	m.status.Locality = m.boot.Locality
	for _, state := range []struct {
		name string
		conn connState
		err  error
	}{
		{"connecting", connConnecting, nil},
		{"not running", connDown, &client.Error{Kind: client.KindNetwork, Message: "The service can't be reached.", Err: errFake}},
		{"no access", connAuth, &client.Error{Kind: client.KindAuth, Code: "auth_required", Message: "Sign-in is required to use this service."}},
	} {
		m.conn, m.connErr = state.conn, state.err
		m.retryAt = m.now().Add(2 * time.Second)
		d.send(struct{}{})
		see(state.name)
	}
	m.conn, m.connErr = connUp, nil
	d.send(struct{}{})
}

// do runs an action as a click would.
func (d *driver) do(kind string) { d.do2(kind, "") }

func (d *driver) do2(kind, arg string) {
	d.t.Helper()
	d.start(d.m.do(views.Action{Kind: kind, Arg: arg}))
	d.send(struct{}{})
}

// closeAll shuts any panels left open.
func (d *driver) closeAll() {
	for len(d.m.panels) > 0 {
		d.key("esc")
	}
}

// messages returns the transcript's text by role, for assertions.
func messageTexts(m *Model, role string) []string {
	var out []string
	for _, msg := range m.transcript.Messages {
		if msg.Role == role {
			out = append(out, msg.Text)
		}
	}
	return out
}

func lastReply(m *Model) *components.Message { return m.transcript.Last() }

func modeName(advanced bool) string {
	if advanced {
		return "advanced"
	}
	return "basic"
}

func enterAdvanced(t *testing.T, d *driver) {
	t.Helper()
	d.submit("/mode developer")
	d.settle(2 * time.Millisecond)
	if !d.m.advanced() {
		t.Fatal("did not reach Advanced mode")
	}
	d.m.notice = ""
	d.clearComposer()
}
