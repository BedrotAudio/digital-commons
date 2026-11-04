package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"digital-commons/terminal/internal/client"

	tea "charm.land/bubbletea/v2"
)

// hostile holds what a compromised model, document or gateway could send.
var hostile = []string{
	"\x1b]52;c;aGVsbG8=\x07",                          // write the clipboard
	"\x1b]0;owned\x07",                                // set the title
	"\x1b]8;;http://evil.example\x07link\x1b]8;;\x07", // hidden hyperlink
	"\x1b]11;#ff0000\x07",                             // set the background
	"\x1b[2J", "\x1b[H", "\x1b[3;3H", "\x1b[?1049l", "\x1b[?1000h", "\x1b[?25l", "\x1bc",
	"\x1b[38;2;255;0;0mred\x1b[0m", "\x1b[5mblink",
	"\x1bP1$qm\x1b\\", "\x1b_apc\x1b\\", "\x1b^pm\x1b\\",
	"\r", "\x07", "\x08", "\x7f", "\x00", "\x9b31m", "\x9d0;title\x9c", "\u0090x\u009c",
	string(rune(0x202E)) + "evil" + string(rune(0x202C)), string(rune(0x2066)) + "x" + string(rune(0x2069)), string(rune(0x200B)), string(rune(0xFEFF)),
	"&#27;[2J", "&#x1b;]52;c;aGVsbG8=&#7;", "&#155;31m",
}

func hostileText() string { return "before " + strings.Join(hostile, " middle ") + " after" }

// assertOnlyPlainText checks that the only escape sequences in a frame are SGR, and
// that no control or invisible formatting character survives in it.
func assertOnlyPlainText(t *testing.T, content, label string) {
	t.Helper()
	rest := sgrPattern.ReplaceAllString(content, "")
	for i, r := range rest {
		switch {
		case r == '\n':
		case unicode.IsControl(r):
			t.Errorf("%s: control character %U at byte %d: %q", label, r, i, excerpt(rest, i))
			return
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200B, r == 0xFEFF:
			t.Errorf("%s: invisible character %U at byte %d", label, r, i)
			return
		}
	}
}

func excerpt(s string, i int) string {
	return s[max(i-20, 0):min(i+20, len(s))]
}

// The same payload through every route that reaches the screen.
func TestHostileOutputCannotAlterAFrame(t *testing.T) {
	payload := hostileText()
	g := newFakeGateway(t)
	g.set(func() {
		g.chats[0]["title"] = payload
		g.boot["suggestions"] = []map[string]any{{"id": "x", "title": payload, "subtitle": payload, "action": "prompt", "prompt": "Help"}}
		g.boot["application"].(map[string]any)["notice"] = payload
		g.boot["locality"].(map[string]any)["label"] = "On this computer" + hostile[0]
		g.status["summary"] = payload
		g.privacyBody = map[string]any{
			"locality":   g.boot["locality"],
			"statements": []map[string]any{{"id": "x", "label": payload, "value": payload, "status": "warn", "detail": payload}},
			"notice":     payload,
		}
		g.modelsBody = map[string]any{"provider": payload, "tiers": []map[string]any{{"tier": "fast", "label": payload, "description": payload, "enabled": true, "available": false, "unavailable_reason": payload, "model": payload}}}
		g.convs["c3"] = map[string]any{"id": "c3", "title": payload, "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": payload, "created_at": "2026-11-02T09:00:00Z", "attachments": []map[string]any{{"name": payload + ".txt"}}},
			{"id": "m2", "role": "assistant", "content": payload, "created_at": "2026-11-02T09:00:30Z", "finish_reason": "stop",
				"citations": []map[string]any{{"number": 1, "title": payload, "section": payload, "department": payload}}},
		}}
	})

	markdown := "# " + payload + "\n\n" + payload + "\n\n`" + payload + "`\n\n```\n" + payload + "\n```\n\n[" + payload + "](http://evil.example/" + hostile[0] + ")\n\n- " + payload + "\n\n| " + hostile[3] + " | b |\n|---|---|\n| c | " + hostile[4] + " |\n\n<script>" + payload + "</script>\n\n![x](http://evil.example/a.png)"
	script := []string{
		startOnly("new1", map[string]any{"code": "attachment_truncated", "message": payload}),
		sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": payload, "section": payload, "department": payload, "version": payload, "document_date": payload}}}),
		sse("status", map[string]any{"phase": "generating", "message": payload}),
		sse("thinking", map[string]any{"text": payload}),
		"HOLD",
		// A sequence split across two events, which only the accumulated text reveals.
		sse("delta", map[string]any{"text": "split \x1b"}), sse("delta", map[string]any{"text": "]52;c;aGVsbG8=\x07 end\n\n" + markdown}),
		"HOLD",
		doneEvent("stop"),
	}

	for _, advanced := range []bool{false, true} {
		t.Run(modeName(advanced), func(t *testing.T) {
			d := g.open(t, rig{w: 120, h: 40})
			if advanced {
				enterAdvanced(t, d)
			}
			see := func(state string) {
				t.Helper()
				for _, s := range [][2]int{{120, 40}, {80, 24}, {50, 16}} {
					d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
					d.rewrap()
					content := d.m.View().Content
					label := fmt.Sprintf("%s %dx%d", state, s[0], s[1])
					assertOnlyPlainText(t, content, label)
					assertPainted(t, content, "dark", label, s[0], s[1])
					assertFits(t, d, state, s[0], s[1])
				}
				d.send(tea.WindowSizeMsg{Width: 120, Height: 40})
				d.rewrap()
			}
			see("home")
			d.submit("/privacy")
			d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
			see("privacy")
			d.key("esc")
			d.submit("/status")
			see("status")
			d.key("esc")
			if advanced {
				d.submit("/model")
				d.until("models", func() bool { return d.m.models != nil && !d.m.topPanel().loading })
				see("models")
				d.key("esc")
			}

			g.set(func() { g.script = script[:3] })
			d.submit("Question")
			d.finish()
			see("lost after status")

			g.set(func() { g.script = script })
			d.submit("Question")
			d.until("waiting", func() bool { return d.m.active != nil && d.m.active.reply.Thinking != "" })
			d.settle(120 * time.Millisecond)
			see("waiting")
			g.release <- struct{}{}
			d.until("streaming", func() bool { return d.m.active != nil && strings.Contains(d.m.active.reply.Text, "end") })
			d.settle(120 * time.Millisecond)
			see("streaming")
			g.release <- struct{}{}
			d.finish()
			see("conversation")
			d.key("ctrl+y")
			d.settle(5e6)
			if copied := d.clip.last(); copied != "" {
				for _, r := range copied {
					if r != '\n' && r != '\t' && unicode.IsControl(r) {
						t.Fatalf("control character %U on the clipboard", r)
					}
				}
			}

			g.set(func() { g.script = []string{errorEvent("internal", payload, true)} })
			d.submit("Question")
			d.finish()
			see("error")

			d.do2("open", "c3")
			d.until("opened", func() bool {
				return d.m.currentID == "c1" || d.m.transcript.Last() != nil && d.m.transcript.Last().Text != ""
			})
			d.settle(5e6)
			see("reopened")

			// Pasted text is cleaned before it reaches the composer.
			d.key("ctrl+n")
			d.settle(5e6)
			d.send(tea.PasteMsg{Content: payload})
			see("pasted")
			if value := d.m.composer.Value(); strings.ContainsAny(value, "\x1b\x07\x9b") {
				t.Errorf("escape in the composer: %q", value)
			}
			d.clearComposer()
		})
	}
}

// Whitespace in a title must not break the row it sits in, or every row below it
// would be drawn away from its click target.
func TestNewlinesInGatewayLabels(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.chats[0]["title"] = "First line\nSecond line\tafter a tab"
		g.chats[1]["title"] = "Next chat"
		g.convs["c2"] = storedChat("c2", "stop")
		g.boot["locality"].(map[string]any)["label"] = "On this computer\nand more"
		g.boot["suggestions"] = []map[string]any{{"id": "x", "title": "Two\nlines", "subtitle": "Sub\ntitle", "action": "prompt", "prompt": "Help"}}
	})
	d := g.open(t, rig{w: 120, h: 40})
	d.expect("First line Second line", "Next chat")
	d.clickText("Next chat")
	d.until("open", func() bool { return d.m.currentID != "" })
	if d.m.currentID != "c2" && d.m.sidebar.Current != "c2" {
		t.Errorf("clicking the row below a two-line title opened %q", d.m.sidebar.Current)
	}
	d.key("ctrl+n")
	d.settle(5e6)
	g.set(func() {
		g.script = []string{startOnly("new1"), sse("status", map[string]any{"phase": "x", "message": "Line one\nLine two"}), "HOLD"}
	})
	d.submit("Question")
	d.until("status", func() bool { return d.m.active != nil && d.m.active.reply.Status != "" })
	d.settle(120e6)
	d.expect("Line one Line two")
	d.key("esc")
	d.finish()
}

// Only the citations event can produce a Sources block. Text that looks like one
// stays plain text, and a citation from the model is just its words.
func TestSourcesComeOnlyFromTheEvent(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.script = []string{startOnly("new1"), sse("delta", map[string]any{"text": "Answer [1].\n\n[1] Made up source"}), doneEvent("stop")}
	})
	d := newDriver(t, g, 100, 30)
	d.submit("Question")
	d.finish()
	d.refute("Sources")
	if d.m.transcript.Last().Citations != nil {
		t.Error("citations appeared with no citations event")
	}

	g.set(func() {
		g.script = []string{startOnly("new1"), sse("citations", map[string]any{"citations": []client.Citation{{Number: 1, Title: "Real Policy", Withheld: true}, {Number: 2, Title: "Other Policy", Version: "3"}}}),
			sse("delta", map[string]any{"text": "Answer [1] and [2]."}), doneEvent("stop")}
	})
	d.submit("Question")
	d.finish()
	d.expect("Sources", "[1] This source is no longer available to you.", "[2] Other Policy · v3")
	d.refute("Real Policy")
}
