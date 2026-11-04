package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/views"
)

func warmingUp(g *fakeGateway) {
	g.status["state"] = "starting"
	g.status["summary"] = views.GettingReady
	g.status["warmup"] = map[string]any{"state": "loading", "message": views.GettingReady}
}

func warmedUp(g *fakeGateway) {
	g.status["state"] = "ready"
	g.status["summary"] = "Ready"
	g.status["warmup"] = map[string]any{"state": "ready", "message": ""}
}

// A reply proves the assistant is ready. The warm-up line goes as soon as text
// streams, and a fresh status is asked for as soon as the reply ends, not at the
// next poll.
func TestAReplyEndsTheGettingReadyLine(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { warmingUp(g) })
	d := newDriver(t, g, 100, 30)
	d.expect(views.GettingReady)

	holdReply(t, d, g, "Here is the answer.")
	d.refute(views.GettingReady)

	g.set(func() { warmedUp(g) })
	release(d, g)
	// Until the fresh status arrives, the one in hand is older than the reply.
	d.refute(views.GettingReady)
	deadline := time.Now().Add(2 * time.Second) // the next poll is 5 seconds away
	for d.m.status.State != "ready" {
		if time.Now().After(deadline) {
			t.Fatal("no fresh status after the reply")
		}
		d.settle(20 * time.Millisecond)
	}
	d.refute(views.GettingReady)
}

// A status asked for after the reply is believed, whatever it says.
func TestAStatusAskedAfterTheReplyIsShown(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { warmingUp(g) })
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "Answer.")
	d.until("the fresh status", func() bool { return d.m.statusAnswers == d.m.answers })
	d.expect(views.GettingReady)

	// A poll that left before the reply ended, landing after it, is not.
	d.send(statusMsg{status: d.m.status, answers: d.m.answers - 1})
	d.refute(views.GettingReady)
}

// Retrieval can return a passage the reply never used. Once the reply ends, Sources
// lists only what it cites, whether it streamed or was reopened.
func TestSourcesListOnlyWhatTheReplyCites(t *testing.T) {
	two := []map[string]any{{"number": 1, "title": "Remote Work Policy"}, {"number": 2, "title": "Snow Removal Contract"}}
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.script = []string{startOnly("new1"), sse("citations", map[string]any{"citations": two}),
			sse("delta", map[string]any{"text": "Twelve days [1]."}), doneEvent("stop")}
	})
	d.submit("How many days?")
	d.finish()
	d.expect("Sources", "[1] Remote Work Policy")
	d.refute("Snow Removal")

	g.set(func() {
		g.convs["c1"] = map[string]any{"id": "c1", "title": "Saved", "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "Q", "created_at": "2026-11-02T09:00:00Z", "attachments": []any{}},
			{"id": "m2", "role": "assistant", "content": "Plowing starts at two inches [2].", "created_at": "2026-11-02T09:00:05Z", "attachments": []any{}, "citations": two, "finish_reason": "stop"},
		}}
	})
	d.clickText("Remote work rules")
	d.until("the chat to open", func() bool { return d.m.currentID == "c1" })
	d.expect("[2] Snow Removal Contract")
	d.refute("Remote Work Policy")
}

// The gateway writes nothing for a caller that leaves before start, so Try again
// after a Stop sends the same message once more, to the same chat, and the screen
// shows it once.
func TestTryAgainAfterStopBeforeStartSendsTheMessageOnce(t *testing.T) {
	for _, followUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "new chat", true: "follow-up"}[followUp], func(t *testing.T) {
			g := newFakeGateway(t)
			d := newDriver(t, g, 100, 30)
			want := []string{"pasted the wrong thing"}
			if followUp {
				d.reply(g, "first", "An answer.")
				want = []string{"first", "pasted the wrong thing"}
			}
			sent := g.requestCount()

			g.set(func() { g.script = []string{"HOLD"} }) // the gateway is still in its checks
			d.submit("pasted the wrong thing")
			d.until("the request", func() bool { return g.requestCount() == sent+1 })
			d.key("esc")
			d.finish()
			d.expect(views.Stopped, "Try again")

			g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Worked."}), doneEvent("stop")) })
			d.key("ctrl+r")
			d.finish()
			req := g.lastRequest(t)
			if req.Regenerate || req.Message != "pasted the wrong thing" || g.requestCount() != sent+2 {
				t.Errorf("request %+v after %d requests", req, g.requestCount()-sent)
			}
			if followUp != (req.ConversationID != nil) || (followUp && *req.ConversationID != "new1") {
				t.Errorf("conversation %v", req.ConversationID)
			}
			if got := messageTexts(d.m, "user"); !slices.Equal(got, want) {
				t.Errorf("user messages %q", got)
			}
		})
	}
}

// The gateway repeats a partial-read notice on Try again. The new reply shows it
// once, and a Try again that writes nothing brings the old reply back with its own.
func TestTryAgainKeepsThePartialReadNotice(t *testing.T) {
	notice := map[string]any{"code": "attachment_truncated", "message": "Only part of handbook.txt was read: about 12%, 23,883 of 200,000 characters."}
	reply := func(text string) []string {
		script := []string{startOnly("new1", notice)}
		if text != "" {
			script = append(script, sse("delta", map[string]any{"text": text}))
		}
		return append(script, doneEvent("stop"))
	}
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 30})
	g.set(func() { g.script = reply("Summary.") })
	d.submit("Summarize")
	d.finish()

	g.set(func() { g.script = reply("Another summary.") })
	d.key("ctrl+r")
	d.finish()
	d.expect("Another summary.")
	if n := strings.Count(d.text(), "Only part of handbook.txt"); n != 1 {
		t.Errorf("the notice shows %d times:\n%s", n, d.text())
	}

	g.set(func() { g.script = reply("") })
	d.key("ctrl+r")
	d.finish()
	d.expect("Another summary.", "Only part of handbook.txt", views.NoReply[:30])
}

// Reasoning text proves the same, and a reply with no text proves nothing.
func TestOnlyWrittenTextProvesReadiness(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { warmingUp(g) })
	d := newDriver(t, g, 100, 30)

	g.set(func() {
		g.script = replyScript(sse("thinking", map[string]any{"text": "Considering."}), "HOLD", doneEvent("stop"))
	})
	d.submit("Question")
	d.until("reasoning", func() bool { return d.m.active != nil && d.m.active.reply.Thinking != "" })
	d.refute(views.GettingReady)
	release(d, g)

	d.until("the fresh status", func() bool { return d.m.statusAnswers == d.m.answers })
	answers := d.m.answers
	g.set(func() { g.script = replyScript("HOLD", doneEvent("stop")) })
	d.submit("Another")
	d.until("the request", func() bool { return d.m.active != nil })
	d.expect(views.GettingReady)
	release(d, g)
	if d.m.answers != answers {
		t.Errorf("a reply with no text counted as an answer")
	}
}
