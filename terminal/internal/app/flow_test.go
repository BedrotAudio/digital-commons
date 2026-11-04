package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A whole turn: wait, status, elapsed time, streaming, finish.
func TestFullConversation(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock, w: 120, h: 40})
	g.set(func() {
		g.script = []string{"HOLD", startOnly("new1", map[string]any{"code": "history_trimmed", "message": "Earlier parts of this conversation were left out to make room."}),
			sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "Remote Work Policy", "section": "Eligibility", "department": "Human Resources", "version": "2.1", "document_date": "2026-03-01"}}}),
			"HOLD", sse("status", map[string]any{"phase": "generating", "message": "Writing a response."}), "HOLD",
			sse("delta", map[string]any{"text": "Employees may "}), "HOLD", sse("delta", map[string]any{"text": "work remotely [1]."}), "HOLD", doneEvent("stop")}
	})
	step := func() {
		g.release <- struct{}{}
		d.settle(130 * time.Millisecond)
	}
	d.expect(views.Greeting)

	d.submit("What is our remote work eligibility policy?")
	d.settle(130 * time.Millisecond)
	d.expect("What is our remote work", "Thinking…", "■ Stop")
	d.refute(views.Greeting, "Send", "Copy")
	if d.m.composer.Value() != "" {
		t.Error("the composer kept the sent text")
	}

	step()
	d.expect("Earlier parts of this conversation were left out to make room.")
	step()
	d.expect("Writing a response.")
	d.refute("1:", "Thinking…")
	frameBefore := d.text()
	clock.Advance(9 * time.Second)
	d.settle(130 * time.Millisecond)
	d.refute("9s")
	clock.Advance(4 * time.Second)
	d.settle(130 * time.Millisecond)
	d.expect("Writing a response. · 13s")
	clock.Advance(100 * time.Second)
	d.settle(130 * time.Millisecond)
	d.expect("· 1m 53s")
	if frameBefore == d.text() {
		t.Error("the wait did not change on screen")
	}

	step()
	d.expect("Employees may", "│", "■ Stop")
	d.refute("Copy · Try again", "Thinking…", "Writing a response.")
	step()
	d.expect("work remotely [1].")
	g.release <- struct{}{}
	d.finish()

	d.expect("Sources", "[1] Remote Work Policy, Eligibility · Human Resources · v2.1, 2026-03-01", "Copy · Try again", "Send")
	d.refute("■ Stop")
	if got := d.m.sidebar.Current; got != "new1" {
		t.Errorf("sidebar current is %q", got)
	}
	d.expect("Remote work eligibility")

	req := g.lastRequest(t)
	if req.ConversationID != nil || req.Tier != "auto" || req.Assistant != "general" || req.Regenerate {
		t.Errorf("request %+v", req)
	}

	// The next turn continues the same conversation, and the actions move down.
	d.reply(g, "And contractors?", "Second reply.")
	if id := g.lastRequest(t).ConversationID; id == nil || *id != "new1" {
		t.Errorf("sent conversation %v", id)
	}
	if got := strings.Count(d.text(), "Copy · Try again"); got != 1 {
		t.Errorf("%d rows of actions", got)
	}
}

// Rendering waits for the 100 ms tick, so a burst of text costs one render.
func TestStreamingRendersOnTheTick(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.script = replyScript("HOLD") })
	d.submit("Question")
	d.until("start", func() bool { return d.m.active != nil && d.m.active.started })
	d.settle(130 * time.Millisecond)

	reply := d.m.active.reply
	id := d.m.active.id
	for i := range 20 {
		d.send(streamMsg{turn: id, ev: client.DeltaEvent{Text: "word"[:4] + string(rune('a'+i)) + " "}})
	}
	if strings.Contains(d.text(), "worda") {
		t.Error("rendered before the tick")
	}
	if !strings.HasPrefix(reply.Text, "worda") {
		t.Fatalf("text %q", reply.Text)
	}
	d.send(tickMsg{id})
	d.expect("worda", "wordt")
	d.key("esc")
	d.finish()
}

// The view follows the tail only when it was already at the bottom.
func TestFollowOnlyAtTheBottom(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	long := strings.Repeat("A paragraph of the reply.\n\n", 40)
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": long}), "HOLD", sse("delta", map[string]any{"text": long}), "HOLD", doneEvent("stop"))
	})
	d.submit("Question")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	d.settle(130 * time.Millisecond)
	if !d.m.transcript.AtBottom() {
		t.Fatal("did not follow the first text")
	}

	d.key("pgup")
	before := d.text()
	d.expect("▾ Latest")
	g.release <- struct{}{}
	d.until("more text", func() bool { return d.m.active != nil && strings.Count(d.m.active.reply.Text, "paragraph") > 40 })
	d.settle(130 * time.Millisecond)
	if d.text() != before {
		t.Error("the view moved while the reader was scrolled up")
	}

	d.clickText("▾ Latest")
	if !d.m.transcript.AtBottom() {
		t.Error("Latest did not return to the bottom")
	}
	release(d, g)
	if !d.m.transcript.AtBottom() {
		t.Error("lost the bottom at the end of the reply")
	}
}

func TestStopKeepsThePartialText(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.script = []string{startOnly("new1"), sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "Policy"}}}), sse("delta", map[string]any{"text": "Partial answer"}), "HOLD", doneEvent("stop")}
	})
	d.submit("Question")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	d.clickText("■ Stop")
	d.finish()
	d.expect("Partial answer", views.Stopped, "Copy · Try again")

	// Stopped sits directly under the text, above Sources.
	frame := d.text()
	if strings.Index(frame, views.Stopped) > strings.Index(frame, "Sources") {
		t.Errorf("Stopped is below Sources:\n%s", frame)
	}
	if strings.Index(frame, "Partial answer") > strings.Index(frame, views.Stopped) {
		t.Errorf("Stopped is above the text:\n%s", frame)
	}
}

// A stop before any text arrived still leaves a sensible row.
func TestStopBeforeAnyText(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.script = []string{"HOLD"} })
	d.submit("Question")
	d.settle(20 * time.Millisecond)
	d.key("esc")
	d.finish()
	d.expect(views.Stopped, "Try again")
	d.refute("Copy")
}

func TestTryAgainBeforeStartResends(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.m.attachments = append(d.m.attachments, attachFile(client.Attachment{Name: "policy.txt", MediaType: "text/plain", Text: "Body"}))
	d.m.syncChips()
	g.set(func() {
		g.script = []string{errorEvent("provider_unavailable", "The AI service isn't responding. Please try again in a moment.", true)}
	})
	d.submit("Question")
	d.finish()
	d.expect("The AI service isn't responding", "Try again")
	d.refute("Copy", "refused")
	if d.m.composer.Value() != "" {
		t.Error("a retryable failure should leave the text in the chat")
	}

	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Worked."}), doneEvent("stop")) })
	d.clickText("Try again")
	d.finish()
	req := g.lastRequest(t)
	if req.Regenerate || req.Message != "Question" || len(req.Attachments) != 1 || req.Attachments[0].Text != "Body" {
		t.Errorf("resend %+v", req)
	}
	if got := messageTexts(d.m, "user"); len(got) != 1 {
		t.Errorf("user messages: %q", got)
	}
	if got := messageTexts(d.m, "assistant"); len(got) != 1 || got[0] != "Worked." {
		t.Errorf("replies: %q", got)
	}
	d.refute("isn't responding")
}

func TestTryAgainAfterStartRegenerates(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "First answer.")

	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Better answer."}), "HOLD", doneEvent("stop"))
	})
	d.clickText("Try again")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	d.settle(130 * time.Millisecond)
	d.refute("First answer.")
	d.expect("Better answer.")
	release(d, g)
	req := g.lastRequest(t)
	if !req.Regenerate || req.ConversationID == nil || *req.ConversationID != "new1" || req.Message != "" || len(req.Attachments) != 0 {
		t.Errorf("regenerate %+v", req)
	}
	if got := messageTexts(d.m, "assistant"); len(got) != 1 {
		t.Errorf("replies: %q", got)
	}
}

// When a regenerate produces nothing the gateway keeps the old reply, so the old
// reply stays. Trying again must then replace it, not add to it.
func TestFailedRegenerateKeepsTheOldReplyOnce(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "First answer.")

	g.set(func() {
		g.script = []string{errorEvent("busy", "The assistant is busy right now. Please try again in a moment.", true)}
	})
	d.key("ctrl+r")
	d.finish()
	d.expect("First answer.", "The assistant is busy right now.", "Try again")

	g.set(func() {
		g.script = []string{errorEvent("busy", "The assistant is busy right now. Please try again in a moment.", true)}
	})
	d.key("ctrl+r")
	d.finish()
	if got := strings.Count(d.text(), "busy right now"); got != 1 {
		t.Errorf("%d failures on screen", got)
	}
	if !g.lastRequest(t).Regenerate {
		t.Fatal("the second try did not regenerate")
	}

	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Better answer."}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	d.refute("First answer.", "busy right now")
	d.expect("Better answer.")
	if got := messageTexts(d.m, "assistant"); len(got) != 1 {
		t.Errorf("replies: %q", got)
	}
}

func TestStoppedRegenerateThenTryAgain(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "First answer.")

	// Stopped with no text: the gateway keeps the old reply.
	g.set(func() { g.script = []string{startOnly("new1"), "HOLD"} })
	d.key("ctrl+r")
	d.until("start", func() bool { return d.m.active != nil && d.m.active.started })
	d.key("esc")
	d.finish()
	d.expect("First answer.", views.Stopped)

	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Better answer."}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	if !g.lastRequest(t).Regenerate {
		t.Fatal("did not regenerate")
	}
	d.refute("First answer.", views.Stopped)
	if got := messageTexts(d.m, "assistant"); len(got) != 1 {
		t.Errorf("replies: %q", got)
	}
}

func TestLostConnectionMidStream(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		// The stream ends with no done or error event.
		g.script = replyScript(sse("delta", map[string]any{"text": "Partial answer"}))
	})
	d.submit("Question")
	d.finish()
	d.expect("Partial answer", views.Lost, "Copy · Try again")
	d.refute("■ Stop")

	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Full answer."}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	if !g.lastRequest(t).Regenerate {
		t.Error("Try again after start must regenerate")
	}
	d.expect("Full answer.")
	d.refute(views.Lost, "Partial answer")
}

func TestLostConnectionBeforeStart(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.drop = true })
	d.submit("Question")
	d.finish()
	d.expect(views.NotRunning, "Try again")
	d.until("the status check", func() bool { return d.m.conn == connDown })

	g.set(func() {
		g.drop = false
		g.script = replyScript(sse("delta", map[string]any{"text": "Worked."}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	if g.lastRequest(t).Regenerate {
		t.Error("nothing was stored, so this must resend")
	}
	d.expect("Worked.")
}

func Test401DuringAChat(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.chatCode = 401
		g.chatError = map[string]any{"code": "auth_required", "message": "Sign-in is required to use this service.", "retryable": false}
	})
	d.submit("Keep this text")
	d.finish()
	d.expect(views.NoAccess, "○ Not connected")
	if got := d.m.composer.Value(); got != "Keep this text" {
		t.Errorf("the text was lost: %q", got)
	}
	d.refute("Try again")

	// Sending it again does not leave the failed turn behind.
	g.set(func() {
		g.chatCode, g.chatError = 0, nil
		g.script = replyScript(sse("delta", map[string]any{"text": "Worked."}), doneEvent("stop"))
	})
	d.key("enter")
	d.finish()
	if got := messageTexts(d.m, "user"); len(got) != 1 {
		t.Errorf("user messages: %q", got)
	}
}

func Test401OnAPollAndRecovery(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.code = 401 })
	d.send(pollMsg{d.m.pollGen})
	d.until("refusal", func() bool { return d.m.conn == connAuth })
	d.send(struct{}{})
	d.expect(views.NoAccess, "○ Not connected")

	g.set(func() { g.code = 0 })
	d.send(retryMsg{})
	d.until("recovery", func() bool { return d.m.conn == connUp })
	d.send(struct{}{})
	d.refute(views.NoAccess, "Not connected")
	d.expect("● On this computer")
}

func Test429Busy(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.chatCode = 429
		g.chatError = map[string]any{"code": "busy", "message": "The assistant is busy right now. Please try again in a moment.", "retryable": true}
	})
	d.submit("Question")
	d.finish()
	d.expect("The assistant is busy right now.", "Try again")

	g.set(func() {
		g.chatCode, g.chatError = 0, nil
		g.script = replyScript(sse("delta", map[string]any{"text": "Worked."}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	req := g.lastRequest(t)
	if req.Regenerate || req.Message != "Question" {
		t.Errorf("resend %+v", req)
	}
}

func Test409LocalityChanged(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "Answer.")
	g.set(func() {
		g.chatCode = 409
		g.chatError = map[string]any{"code": "conversation_locality_changed", "message": "This conversation began in a different place. Please start a new chat.", "retryable": false}
	})
	d.submit("Follow up")
	d.finish()
	d.expect("This conversation began in a different place.", "New chat")
	d.refute("Try again")
	if got := d.m.composer.Value(); got != "Follow up" {
		t.Errorf("the text was lost: %q", got)
	}

	// The New chat under the failure, not the sidebar row.
	x, y, ok := 0, 0, false
	for row, line := range strings.Split(d.text(), "\n") {
		if i := strings.LastIndex(line, "New chat"); i >= 0 && !strings.Contains(line, "+ New chat") {
			x, y, ok = ansi.StringWidth(line[:i]), row, true
		}
	}
	if !ok {
		t.Fatalf("no New chat control:\n%s", d.text())
	}
	d.click(x+2, y)
	d.settle(5 * time.Millisecond)
	if d.m.currentID != "" || len(d.m.transcript.Messages) != 0 {
		t.Error("New chat did not start one")
	}
	if got := d.m.composer.Value(); got != "Follow up" {
		t.Errorf("the draft did not carry over: %q", got)
	}
}

func TestLengthFinishNotes(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Cut off"}), doneEvent("length")) })
	d.submit("Question")
	d.finish()
	d.expect("Cut off", views.CutShort)

	g.set(func() { g.script = replyScript(doneEvent("length")) })
	d.submit("Question")
	d.finish()
	d.expect("The assistant ran out of room before answering.", "◆")
}

func TestMessageLimit(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.boot["limits"].(map[string]any)["max_message_chars"] = 20 })
	d := newDriver(t, g, 100, 30)
	text := strings.Repeat("x", 21)
	d.send(tea.PasteMsg{Content: text})
	d.key("enter")
	d.settle(5 * time.Millisecond)
	if g.requestCount() != 0 {
		t.Fatal("sent a message over the limit")
	}
	if got := d.m.composer.Value(); got != text {
		t.Fatalf("the draft changed: %d characters", len(got))
	}
	d.expect("too long")
}

func TestConnectionStates(t *testing.T) {
	cases := []struct {
		name          string
		state, reason string
		basic         []string
		advanced      []string
	}{
		{"starting", "starting", "", []string{views.GettingReady}, []string{"Warm-up", "Gemma4:12b"}},
		{"unreachable", "unavailable", "provider_unreachable", []string{views.NotResponding}, []string{views.NotResponding, "ollama", "connection refused"}},
		{"missing", "unavailable", "model_missing", []string{views.NotSetUp}, []string{"Gemma4:26b", "ollama pull Gemma4:26b"}},
		{"load failed", "unavailable", "model_load_failed", []string{views.NotSetUp}, []string{views.NotSetUp}},
		{"cloud", "unavailable", "cloud_access_enabled", []string{views.NotSetUp}, []string{"Ollama cloud access is on.", "dc.py setup"}},
		{"degraded", "degraded", "", nil, nil},
		{"ready", "ready", "", nil, nil},
	}
	for _, advanced := range []bool{false, true} {
		t.Run(modeName(advanced), func(t *testing.T) {
			g := newFakeGateway(t)
			d := newDriver(t, g, 130, 40)
			if advanced {
				enterAdvanced(t, d)
			}
			for _, c := range cases {
				g.set(func() {
					g.status["state"], g.status["reason"] = c.state, c.reason
					g.status["summary"] = "Working, with limits."
					inf := g.status["inference"].(map[string]any)
					inf["error"] = "connection refused"
					inf["missing_models"] = []map[string]any{{"tier": "advanced", "model": "Gemma4:26b", "install_hint": "ollama pull Gemma4:26b"}}
				})
				d.send(pollMsg{d.m.pollGen})
				d.until(c.name, func() bool { return d.m.status.State == c.state && d.m.status.Reason == c.reason })
				d.send(struct{}{})
				want := c.basic
				if advanced {
					want = c.advanced
				}
				d.expect(want...)
				if !advanced {
					// The composer stays enabled in every state.
					d.key("x")
					if d.m.composer.Value() != "x" {
						t.Errorf("%s: typing is off", c.name)
					}
					d.clearComposer()
				}
				if c.state == "degraded" {
					if advanced {
						d.expect("Working, with limits.")
					} else {
						d.refute("limits", "Working")
					}
				}
			}
		})
	}
}

func TestDemoIndicator(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		loc := map[string]any{"kind": "demo", "label": "Demonstration", "description": "This is a demonstration. Replies are simulated and no AI model is used."}
		g.boot["locality"], g.status["locality"], g.status["state"] = loc, loc, "demo"
	})
	d := newDriver(t, g, 100, 30)
	d.expect("● Demo mode")
	d.refute("Demonstration")
	enterAdvanced(t, d)
	d.expect("● Demo mode")
}

func TestIndicatorFollowsEveryPoll(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.expect("● On this computer")
	g.set(func() {
		g.status["locality"] = map[string]any{"kind": "organization", "label": "Organization server", "description": ""}
	})
	d.send(pollMsg{d.m.pollGen})
	d.until("indicator", func() bool { return d.m.status.Locality.Kind == "organization" })
	d.send(struct{}{})
	d.expect("● Organization server")
	d.refute("On this computer")
}

// A remote gateway can never claim to be on this computer.
func TestNonLoopbackOverride(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.privacyBody = map[string]any{
			"locality":   g.boot["locality"],
			"statements": []map[string]any{{"id": "processing", "label": "Where the AI runs", "value": "On this computer", "status": "ok", "detail": ""}, {"id": "network_exposure", "label": "Who can connect", "value": "Only programs on this computer", "status": "ok", "detail": ""}},
			"notice":     "Local processing alone does not make a system secure or compliant.",
		}
	})
	d := g.open(t, rig{baseURL: "https://ai.example.gov", w: 120, h: 40})
	d.expect("● Organization server")
	d.refute("On this computer")
	d.submit("/privacy")
	d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
	d.expect("Organization server")
	d.refute("On this computer", "on this computer")
	d.closeAll()
	d.submit("/status")
	d.refute("On this computer")
	d.closeAll()

	enterAdvanced(t, d)
	d.expect("● Organization server")
	d.refute("On this computer")

	g.set(func() { g.drop = true })
	d.send(pollMsg{d.m.pollGen})
	d.until("down", func() bool { return d.m.conn == connDown })
	d.send(struct{}{})
	d.expect(views.Unreachable)
	d.refute(views.NotRunning)
}

func TestBootstrapFailureThenRecovery(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.drop = true })
	clock := newClock()
	d := rig{clock: clock, w: 120, h: 40}.build(t, g.URL)
	d.expect(views.Connecting)
	d.until("failure", func() bool { return d.m.conn == connDown })
	d.send(struct{}{})
	d.expect(views.NotRunning, "dc.py dev", "○ Not connected")
	d.refute(views.Greeting + "x")

	// The composer works, and a send fails inline.
	d.submit("Question")
	d.finish()
	d.expect(views.NotRunning, "Try again")

	g.set(func() { g.drop = false })
	d.send(retryMsg{})
	d.until("recovery", func() bool { return d.m.conn == connUp && d.m.status != nil })
	d.send(struct{}{})
	d.expect("● On this computer")
	d.refute("Not connected")
	d.until("chats", func() bool { return len(d.m.sidebar.Chats) == g.chatCount() })
}

// The Advanced countdown, which earlier frames never caught.
func TestAdvancedRetryCountdown(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock, w: 120, h: 40})
	enterAdvanced(t, d)
	g.set(func() { g.drop = true })
	d.send(pollMsg{d.m.pollGen})
	d.until("retry", func() bool { return d.m.retrying })
	d.send(struct{}{})
	d.expect(views.NotRunning, strings.TrimPrefix(g.URL, "http://"), "Retrying in 3 s.", "○ Not connected")
	clock.Advance(time.Second)
	d.send(struct{}{})
	d.expect("Retrying in 2 s.")
}

func TestReconnectResumesPolling(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.drop = true })
	d.send(pollMsg{d.m.pollGen})
	d.until("down", func() bool { return d.m.conn == connDown })
	d.until("retry", func() bool { return d.m.retrying })
	// A second failure while a retry is waiting must not start a second chain.
	d.send(bootstrapMsg{err: &client.Error{Kind: client.KindNetwork, Message: "x", Err: errFake}})

	g.set(func() {
		g.drop = false
		g.hits["/v1/status"] = 0
	})
	d.send(retryMsg{})
	d.until("up", func() bool { return d.m.conn == connUp })
	d.until("status", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.hits["/v1/status"] > 0 })
	d.settle(20 * time.Millisecond)
	gen := d.m.pollGen
	d.send(pollMsg{gen})
	d.until("next poll", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.hits["/v1/status"] > 1 })
}

func TestInstanceChangeRefetchesBootstrap(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	enterAdvanced(t, d)
	g.set(func() {
		g.status["gateway"].(map[string]any)["instance_id"] = "two"
		g.boot["permissions"].(map[string]any)["advanced_mode"] = false
		g.hits["/v1/bootstrap"] = 0
	})
	d.send(pollMsg{d.m.pollGen})
	d.until("bootstrap", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.hits["/v1/bootstrap"] > 0 })
	d.until("mode", func() bool { return !d.m.advanced() })
	d.expect("no longer available")

	// The same id on the next poll does not refetch.
	d.settle(20 * time.Millisecond)
	g.set(func() { g.hits["/v1/bootstrap"] = 0 })
	d.send(pollMsg{d.m.pollGen})
	d.until("status", func() bool { return true })
	d.settle(40 * time.Millisecond)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hits["/v1/bootstrap"] != 0 {
		t.Error("refetched with no change")
	}
}

func TestFailedPollRefetchesBootstrap(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.drop = true; g.hits["/v1/bootstrap"] = 0 })
	d.send(pollMsg{d.m.pollGen})
	d.until("bootstrap", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.hits["/v1/bootstrap"] > 0 })
}

func TestApplicationNotice(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 50})
	d.expect("Beta software. Not approved for production use or sensitive data.")
	d.reply(g, "Question", "Answer.")
	d.refute("Beta software.")
	d.key("f1")
	for range 8 {
		d.key("pgdown")
	}
	d.expect("Beta software. Not approved for production use or sensitive data.")
}

func TestReopenedChats(t *testing.T) {
	cases := []struct {
		reason, note string
	}{
		{"stop", ""}, {"cancelled", views.Stopped}, {"length", views.CutShort}, {"error", views.Interrupted},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			g := newFakeGateway(t)
			g.set(func() { g.convs["c1"] = storedChat("c1", c.reason) })
			d := g.open(t, rig{w: 120, h: 40})
			d.clickText("Remote work rules")
			d.until("open", func() bool { return len(d.m.transcript.Messages) == 2 })
			d.expect("What is the remote work policy?", "policy.txt", "Employees may work remotely after probation.", "Sources", "[1] Remote Work Policy", "Copy · Try again")
			if c.note != "" {
				d.expect(c.note)
			}
			frame := d.text()
			if c.note != "" && strings.Index(frame, c.note) > strings.Index(frame, "Sources") {
				t.Errorf("the note is below Sources:\n%s", frame)
			}
			if d.m.sidebar.Current != "c1" {
				t.Error("the sidebar does not mark the open chat")
			}
			if d.m.currentID != "c1" {
				t.Error("the chat is not current")
			}

			g.set(func() {
				g.script = replyScript(sse("delta", map[string]any{"text": "A new reply."}), doneEvent("stop"))
			})
			d.key("ctrl+r")
			d.finish()
			req := g.lastRequest(t)
			if !req.Regenerate || req.ConversationID == nil || *req.ConversationID != "c1" {
				t.Errorf("regenerate %+v", req)
			}
			if got := messageTexts(d.m, "assistant"); len(got) != 1 {
				t.Errorf("replies: %q", got)
			}
		})
	}
}

func TestReopenedChatEndingWithAQuestion(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.convs["c1"] = map[string]any{"id": "c1", "title": "Saved", "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "Lost question", "created_at": "2026-11-02T09:00:00Z"},
		}}
	})
	d := g.open(t, rig{w: 120, h: 40})
	d.clickText("Remote work rules")
	d.until("open", func() bool { return len(d.m.transcript.Messages) > 0 })
	d.expect("Lost question", views.NoReplySaved, "Try again", "◆")
	d.refute("Copy")
}

func TestOpenFailureStaysPut(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")
	g.set(func() { g.code = 404 })
	d.do2("open", "c3")
	d.until("failure", func() bool { return d.m.notice != "" })
	d.expect("Answer.")
}

func TestNotices(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 40})
	g.set(func() {
		g.script = []string{startOnly("new1", map[string]any{"code": "attachment_truncated", "message": "Only part of policy.txt was read: about 40%, 4,000 of 10,000 characters."}, map[string]any{"code": "documents_none_found", "message": "No approved documents matched this question."}),
			sse("delta", map[string]any{"text": "Reply text."}), doneEvent("stop")}
	})
	d.submit("Question")
	d.finish()
	d.expect("Only part of policy.txt was read", "No approved documents matched this question.", "Reply text.")
	frame := d.text()
	if strings.Index(frame, "No approved") > strings.Index(frame, "Reply text.") {
		t.Errorf("notices are below the reply:\n%s", frame)
	}
}

func TestAttachCommand(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	policy := filepath.Join(home, "policy.txt")
	if err := os.WriteFile(policy, []byte("Policy body"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(home, "report.pdf")
	if err := os.WriteFile(report, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := g.open(t, rig{home: home})

	d.submit("/attach " + report)
	d.until("refusal", func() bool { return d.m.notice != "" })
	d.expect("Save PDF or Word files as text first.")
	if len(d.m.attachments) != 0 {
		t.Fatal("attached a refused file")
	}

	d.submit(`/attach "` + policy + `"`)
	d.until("chip", func() bool { return len(d.m.attachments) == 1 })
	d.expect("policy.txt ×")

	// A message may be empty when a file is attached.
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Read it."}), doneEvent("stop")) })
	d.key("enter")
	d.finish()
	req := g.lastRequest(t)
	if req.Message != "" || len(req.Attachments) != 1 || req.Attachments[0].Name != "policy.txt" || req.Attachments[0].Text != "Policy body" {
		t.Errorf("request %+v", req)
	}
	d.refute("policy.txt ×")
	d.expect("policy.txt")
}

func TestFilePicker(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	for _, name := range []string{"Alpha.TXT", "beta.csv", "gamma.pdf", ".hidden.txt"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("Body of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sub", "inner.md"), []byte("Inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := g.open(t, rig{home: home, w: 120, h: 40})
	d.key("ctrl+o")
	d.until("listing", func() bool { return d.m.picker != nil && len(d.m.picker.Entries) > 0 })
	d.expect("Alpha.TXT", "beta.csv", "sub/", "Accepted: .txt .md .csv .tsv", "Save PDF or Word files as text first.")
	d.refute("gamma.pdf", ".hidden")

	// First the folder, by keyboard.
	d.key("enter")
	d.until("subfolder", func() bool { return strings.HasSuffix(d.m.picker.Dir, "sub") })
	d.expect("inner.md")
	d.key("backspace")
	d.until("parent", func() bool { return d.m.picker.Dir == home })

	// Then a file, by mouse.
	d.clickText("beta.csv")
	d.until("chosen", func() bool { return len(d.m.attachments) == 1 })
	if d.m.topPanel() != nil {
		t.Error("the picker stayed open")
	}
	d.expect("beta.csv ×")
}

func TestSuggestionActions(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("Body"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := g.open(t, rig{home: home, w: 120, h: 40})

	// prompt: places the text and waits.
	d.clickText("Draft correspondence")
	if got := d.m.composer.Value(); got != "Help me draft a professional message to " {
		t.Fatalf("composer holds %q", got)
	}
	if g.requestCount() != 0 {
		t.Fatal("a suggestion sent by itself")
	}
	d.clearComposer()

	// attach: one file, then the text.
	d.clickText("Summarize a document")
	d.until("listing", func() bool { return d.m.picker != nil && len(d.m.picker.Entries) > 0 })
	d.clickText("one.txt")
	d.until("closed", func() bool { return d.m.topPanel() == nil })
	if got := d.m.composer.Value(); got != "Summarize this document." {
		t.Fatalf("composer holds %q", got)
	}
	d.clearComposer()
	d.m.attachments = nil
	d.m.syncChips()
	d.send(struct{}{})

	// attach_two: the picker stays for the second file.
	d.clickText("Compare two policies")
	d.until("listing", func() bool { return d.m.picker != nil && len(d.m.picker.Entries) > 0 })
	d.expect("Choose 2 files. 0 chosen so far.")
	d.clickText("one.txt")
	d.until("first", func() bool { return len(d.m.attachments) == 1 })
	if d.m.topPanel() == nil {
		t.Fatal("closed after one of two files")
	}
	d.expect("Choose 2 files. 1 chosen so far.")
	d.clickText("two.txt")
	d.until("second", func() bool { return len(d.m.attachments) == 2 })
	d.until("closed", func() bool { return d.m.topPanel() == nil })
	if got := d.m.composer.Value(); got != "Compare these two documents." {
		t.Fatalf("composer holds %q", got)
	}
	d.clearComposer()
	d.m.attachments = nil
	d.m.syncChips()
	d.send(struct{}{})

	// Cancelling a picker leaves no suggestion text behind.
	d.clickText("Compare two policies")
	d.key("esc")
	if got := d.m.composer.Value(); got != "" {
		t.Errorf("cancelled picker left %q", got)
	}

	// documents: turns the mode on.
	for i := range d.m.boot.Suggestions {
		if d.m.boot.Suggestions[i].Action == "documents" {
			d.start(d.m.suggest(i))
		}
	}
	d.send(struct{}{})
	d.expect("Approved documents ×")
}

func TestApprovedDocuments(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.submit("/search")
	d.expect("Approved documents ×")

	d.reply(g, "What is the policy?", "Answer.")
	if !g.lastRequest(t).UseDocuments {
		t.Error("the chip did not reach the request")
	}
	d.expect("Approved documents ×")
	d.submit("/search")
	d.refute("Approved documents ×")

	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.submit("/search How long is probation?")
	d.finish()
	req := g.lastRequest(t)
	if !req.UseDocuments || req.Message != "How long is probation?" {
		t.Errorf("request %+v", req)
	}
	d.refute("Approved documents ×")

	// Clicking the x removes it.
	d.submit("/search")
	x, y, ok := d.find("Approved documents ×")
	if !ok {
		t.Fatal("no chip")
	}
	d.click(x+len("Approved documents "), y)
	d.refute("Approved documents ×")

	g.set(func() { g.boot["permissions"].(map[string]any)["search_documents"] = false })
	d.send(pollMsg{d.m.pollGen})
	d.m.boot.Permissions.SearchDocuments = false
	d.submit("/search")
	d.expect("isn't available for your account")
	d.refute("Approved documents ×")
}

func TestDeleteFlows(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)

	d.submit("/delete")
	d.expect("There's nothing to delete.")
	if d.m.topPanel() != nil {
		t.Fatal("asked to confirm nothing")
	}
	d.m.notice = ""

	d.reply(g, "Question", "Answer.")
	d.submit("/delete")
	d.expect("Delete this chat?", "Cancel", "Delete")
	if d.m.topPanel().cursor != 0 {
		t.Fatal("focus is not on Cancel")
	}
	d.key("esc")
	if d.m.currentID == "" {
		t.Fatal("Esc deleted the chat")
	}
	d.submit("/delete")
	d.clickText("Cancel")
	d.settle(5 * time.Millisecond)
	if d.m.topPanel() != nil || d.m.currentID == "" || len(g.deleted) != 0 {
		t.Fatal("Cancel did not cancel")
	}

	d.submit("/delete")
	d.keys("tab", "enter")
	d.until("deleted", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.deleted) == 1 })
	d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
	if g.deleted[0] != "new1" {
		t.Errorf("deleted %v", g.deleted)
	}
	d.expect(views.Greeting, "Chat deleted.")
	if d.m.currentID != "" {
		t.Error("still holding the deleted chat")
	}
}

func TestDeleteAllFlow(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")
	d.key("f2")
	d.clickText("Delete all conversations")
	d.expect("Delete all chats?")
	if d.m.topPanel().cursor != 0 {
		t.Fatal("focus is not on Cancel")
	}
	d.key("enter")
	d.settle(5 * time.Millisecond)
	if g.deletedAll != 0 {
		t.Fatal("Enter on Cancel deleted everything")
	}
	if p := d.m.topPanel(); p == nil || p.kind != panelSettings {
		t.Fatal("Cancel should return to the panel under it")
	}
	d.clickText("Delete all conversations")
	d.keys("tab", "enter")
	d.until("deleted", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.deletedAll == 1 })
	d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
	if d.m.topPanel() != nil {
		t.Error("panels stayed open after deleting")
	}
	d.until("the empty list", func() bool { return len(d.m.sidebar.Chats) == 0 })
	d.expect(views.Greeting, "No chats yet")
}

func TestDeleteFailure(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")
	g.set(func() { g.deleteCode = 404 })
	d.submit("/delete")
	d.keys("tab", "enter")
	d.until("failure", func() bool { return strings.Contains(d.m.notice, "could not be found") })
	if len(d.m.transcript.Messages) == 0 {
		t.Error("a failed delete left the chat")
	}
}

// The table "During an active reply", row by row.
func TestPanelsThemeAndModeDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	holdReply(t, d, g, "Partial")

	d.key("f1")
	d.key("esc")
	d.key("f2")
	d.key("esc")
	d.submit("/privacy")
	d.key("esc")
	d.submit("/status")
	d.key("esc")
	d.submit("/theme light")
	if d.m.th.Name != "light" {
		t.Errorf("theme is %s", d.m.th.Name)
	}
	d.submit("/mode developer")
	if !d.m.advanced() {
		t.Error("/mode did not work during a reply")
	}
	d.submit("/mode basic")
	if d.m.active == nil || d.m.active.stopping {
		t.Fatal("something stopped the reply")
	}
	if got := d.m.composer.Value(); got != "" {
		t.Errorf("composer holds %q", got)
	}
	release(d, g)
	if d.m.transcript.Last().Note != "" {
		t.Errorf("note %q", d.m.transcript.Last().Note)
	}
}

func TestModelAndCodeApplyToTheNextTurn(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Partial"}), "HOLD", doneEvent("stop"))
	})
	d.submit("First")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	d.submit("/model fast")
	d.submit("/code")
	if d.m.active == nil || d.m.active.stopping {
		t.Fatal("the choice stopped the reply")
	}
	if first := g.lastRequest(t); first.Tier != "auto" || first.Assistant != "general" {
		t.Errorf("first request %+v", first)
	}
	release(d, g)
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.submit("Second")
	d.finish()
	if second := g.lastRequest(t); second.Tier != "fast" || second.Assistant != "code" {
		t.Errorf("second request %+v", second)
	}
}

func TestNavigatingAwayStopsTheReply(t *testing.T) {
	for _, way := range []struct {
		name string
		do   func(d *driver)
		home bool
	}{
		{"ctrl+n", func(d *driver) { d.key("ctrl+n") }, true},
		{"/new", func(d *driver) { d.submit("/new") }, true},
		{"/clear", func(d *driver) { d.submit("/clear") }, true},
		{"click new chat", func(d *driver) { d.clickText("+ New chat") }, true},
		{"open another chat", func(d *driver) { d.clickText("Vendor letter") }, false},
	} {
		t.Run(way.name, func(t *testing.T) {
			g := newFakeGateway(t)
			g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
			d := newDriver(t, g, 120, 40)
			holdReply(t, d, g, "Partial")
			way.do(d)
			d.finish()
			if way.home {
				d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
				d.expect(views.Greeting)
			} else {
				d.until("the other chat", func() bool { return d.m.currentID == "c3" })
				d.expect("Employees may work remotely after probation.")
				d.refute("Partial")
			}
		})
	}
}

// Selecting the chat already open must not stop its reply.
func TestOpeningTheCurrentChatLeavesItsReplyRunning(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	holdReply(t, d, g, "Partial")
	d.clickText("Remote work eligibility")
	d.settle(20 * time.Millisecond)
	if d.m.active == nil || d.m.active.stopping {
		t.Fatal("reselecting the open chat stopped the reply")
	}
	release(d, g)
}

func TestDeleteDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	holdReply(t, d, g, "Partial")

	d.submit("/delete")
	if d.m.active == nil || d.m.active.stopping {
		t.Fatal("asking to confirm stopped the reply")
	}
	d.keys("tab", "enter")
	// The delete waits for the stop, so nothing reaches the gateway until the
	// stream has closed. Pump nothing yet.
	if len(g.deleted) != 0 {
		t.Fatal("deleted before stopping")
	}
	d.finish()
	d.until("deleted", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.deleted) == 1 })
	d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
	d.expect(views.Greeting)
}

func TestDeleteAllDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	holdReply(t, d, g, "Partial")
	d.do("delete-all")
	d.keys("tab", "enter")
	if g.deletedAll != 0 {
		t.Fatal("deleted before stopping")
	}
	d.finish()
	d.until("deleted", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.deletedAll == 1 })
	d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
}

func TestDefaultModeConfig(t *testing.T) {
	g := newFakeGateway(t)
	dev := func(c *config.Config) { c.Application.DefaultMode = config.ModeDeveloper }
	d := g.open(t, rig{config: dev, w: 130, h: 40})
	if !d.m.advanced() {
		t.Error("configured developer default did not start Advanced")
	}
	g.set(func() { g.boot["permissions"].(map[string]any)["advanced_mode"] = false })
	b := g.open(t, rig{config: dev, w: 130, h: 40})
	if b.m.advanced() {
		t.Fatal("started Advanced with no permission")
	}
	b.expect("this is Basic mode")
}
