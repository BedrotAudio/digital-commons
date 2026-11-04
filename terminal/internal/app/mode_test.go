package app

import (
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

func TestModeSwitchBothWays(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	if d.m.advanced() {
		t.Fatal("Basic is the default")
	}
	d.expect(views.Greeting)
	d.refute("ADVANCED")

	d.submit("/mode developer")
	if !d.m.advanced() {
		t.Fatal("/mode developer failed")
	}
	d.expect("ADVANCED", "chats", "conversation", "inspector", "gateway")
	d.refute(views.Greeting)

	d.submit("/mode basic")
	if d.m.advanced() {
		t.Fatal("/mode basic failed")
	}
	d.expect(views.Greeting)
	d.refute("ADVANCED", "inspector")

	d.submit("/mode advanced")
	if !d.m.advanced() {
		t.Fatal("/mode advanced failed")
	}

	// A conversation survives the trip.
	d.reply(g, "Question", "Answer.")
	d.expect("you · ", "assistant · standard")
	d.submit("/mode basic")
	d.expect("Question", "Answer.", "◆", "Copy · Try again")
	d.refute("you · ", "assistant · ")
}

func TestModeSwitchInSettings(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	d.key("f2")
	d.expect("● Basic", "○ Advanced")
	d.clickText("Advanced")
	if !d.m.advanced() {
		t.Fatal("Settings did not switch")
	}
	d.expect("○ Basic", "● Advanced")
	d.clickText("Basic")
	if d.m.advanced() {
		t.Fatal("Settings did not switch back")
	}
}

func TestModeRefusedWithoutPermission(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.boot["permissions"].(map[string]any)["advanced_mode"] = false })
	d := newDriver(t, g, 130, 40)
	for _, line := range []string{"/mode developer", "/mode advanced"} {
		d.submit(line)
		if d.m.advanced() {
			t.Fatal("switched with no permission")
		}
		d.expect("Advanced mode isn't available for your account.")
		d.m.notice = ""
	}
	d.key("f2")
	d.refute("Advanced")
}

func TestAdvancedCommandsExplainThemselvesInBasic(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	for _, line := range []string{"/model", "/model fast", "/code", "/copy code", "/copy code 1"} {
		d.submit(line)
		d.expect("part of Advanced mode")
		if d.m.adv.tier != "auto" || d.m.adv.assistant != "general" {
			t.Fatalf("%s changed a choice in Basic", line)
		}
		d.m.notice = ""
		d.clearComposer()
	}
}

func TestBasicAlwaysSendsAutoAndGeneral(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("/model standard")
	d.submit("/code")
	d.reply(g, "In Advanced", "Answer.")
	if req := g.lastRequest(t); req.Tier != "standard" || req.Assistant != "code" {
		t.Fatalf("Advanced sent %+v", req)
	}

	d.submit("/mode basic")
	d.reply(g, "In Basic", "Answer.")
	if req := g.lastRequest(t); req.Tier != "auto" || req.Assistant != "general" {
		t.Errorf("Basic sent %+v", req)
	}

	// Try again, both kinds, goes the same way.
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("ctrl+r")
	d.finish()
	if req := g.lastRequest(t); !req.Regenerate || req.Tier != "auto" || req.Assistant != "general" {
		t.Errorf("regenerate sent %+v", req)
	}
	g.set(func() { g.script = []string{errorEvent("busy", "The assistant is busy right now.", true)} })
	d.submit("Failing")
	d.finish()
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("ctrl+r")
	d.finish()
	if req := g.lastRequest(t); req.Regenerate || req.Tier != "auto" || req.Assistant != "general" {
		t.Errorf("resend sent %+v", req)
	}
}

func TestResendAfterSwitchingModesUsesTheNewMode(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("/model fast")
	g.set(func() { g.script = []string{errorEvent("busy", "The assistant is busy right now.", true)} })
	d.submit("Failing")
	d.finish()
	d.submit("/mode basic")
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("ctrl+r")
	d.finish()
	if req := g.lastRequest(t); req.Tier != "auto" {
		t.Errorf("resend in Basic sent tier %s", req.Tier)
	}
}

func TestAdvancedChoicesReturn(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("/model standard")
	d.submit("/code")
	d.expect("standard", "CODE")

	d.submit("/mode basic")
	d.refute("CODE")
	d.submit("/mode developer")
	d.expect("standard", "CODE")
	d.reply(g, "Back again", "Answer.")
	if req := g.lastRequest(t); req.Tier != "standard" || req.Assistant != "code" {
		t.Errorf("choices did not return: %+v", req)
	}

	d.submit("/code")
	d.refute("CODE")
	d.reply(g, "Off again", "Answer.")
	if req := g.lastRequest(t); req.Assistant != "general" {
		t.Errorf("/code did not toggle: %+v", req)
	}
}

func TestAssistantRestoredOnOpenInAdvanced(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c1"] = storedChat("c1", "stop") })
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.do2("open", "c1")
	d.until("open", func() bool { return len(d.m.transcript.Messages) == 2 })
	if d.m.adv.assistant != "code" {
		t.Errorf("assistant is %s", d.m.adv.assistant)
	}
	d.expect("CODE")

	b := newDriver(t, g, 130, 40)
	b.do2("open", "c1")
	b.until("open", func() bool { return len(b.m.transcript.Messages) == 2 })
	if b.m.adv.assistant != "general" {
		t.Errorf("opening in Basic changed the Advanced assistant to %s", b.m.adv.assistant)
	}
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	b.submit("Next")
	b.finish()
	if req := g.lastRequest(t); req.Assistant != "general" {
		t.Errorf("Basic sent %s", req.Assistant)
	}
}

func TestModelPicker(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("/model")
	d.until("models", func() bool { return d.m.models != nil && !d.m.topPanel().loading })
	d.expect("Choose a tier", "auto", "fast", "standard", "advanced", "Not installed")

	d.clickText("standard")
	if d.m.adv.tier != "standard" {
		t.Fatalf("tier is %s", d.m.adv.tier)
	}
	if d.m.topPanel() != nil {
		t.Error("the picker stayed open")
	}

	// A tier that is not available cannot be chosen.
	d.submit("/model")
	d.until("models", func() bool { return !d.m.topPanel().loading })
	d.clickText("advanced")
	if d.m.adv.tier != "standard" {
		t.Errorf("chose an unavailable tier: %s", d.m.adv.tier)
	}
	d.closeAll()
}

func TestModelPermissions(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.boot["permissions"].(map[string]any)["manual_model_selection"] = false })
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("/model fast")
	d.expect("can't choose a model")
	if d.m.adv.tier != "auto" {
		t.Errorf("tier is %s", d.m.adv.tier)
	}

	g2 := newFakeGateway(t)
	g2.set(func() { g2.boot["permissions"].(map[string]any)["allowed_tiers"] = []string{"fast"} })
	e := newDriver(t, g2, 130, 40)
	enterAdvanced(t, e)
	e.submit("/model standard")
	if e.m.adv.tier != "auto" {
		t.Errorf("chose a tier outside allowed_tiers: %s", e.m.adv.tier)
	}
	e.expect("isn't a tier you can choose")
	e.m.notice = ""
	e.submit("/model nonsense")
	if e.m.adv.tier != "auto" {
		t.Error("accepted a nonsense tier")
	}
}

func TestInspector(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 50)
	enterAdvanced(t, d)
	d.expect("no turn yet", "shown after the next turn", "gateway", "ollama", "0.40.13")
	d.reply(g, "Question", "Answer.")
	for _, want := range []string{"auto → standard", "Gemma4:12b", "Code markers present", "152 tok", "388 tok", "6.4 tok/s", "2.2 s", "61 s", "1.2k/6.1k", "▓"} {
		d.expect(want)
	}
	d.refute("no turn yet")
}

func TestAdvancedShowsCodeBlocksAndReasoning(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	g.set(func() {
		g.script = replyScript(sse("thinking", map[string]any{"text": "Considering the handler."}),
			sse("delta", map[string]any{"text": "```go\nfunc handle() {}\n```"}), doneEvent("stop"))
	})
	d.submit("Refactor")
	d.finish()
	d.expect("[1] go", "func handle() {}", "Reasoning")
	d.refute("Considering the handler.")

	// Collapsed once finished, opened by clicking.
	d.clickText("▸ Reasoning")
	d.expect("Considering the handler.")

	// Clicking [1] copies the block.
	d.clickText("[1]")
	d.until("copy", func() bool { return d.clip.last() == "func handle() {}" })

	// Basic never renders it, and reopening has nothing to show.
	d.submit("/mode basic")
	d.refute("Considering the handler.", "Reasoning")
}

func TestCursorFollowsFocus(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	if d.m.View().Cursor == nil {
		t.Fatal("no cursor at home")
	}
	d.key("f1")
	if d.m.View().Cursor != nil {
		t.Error("the cursor stays in the composer behind a panel")
	}
	d.key("esc")
	cursor := d.m.View().Cursor
	if cursor == nil {
		t.Fatal("no cursor after the panel closed")
	}

	// The cursor sits inside the composer's box, past the + and the typed text.
	d.key("abc")
	cursor = d.m.View().Cursor
	line := strings.Split(d.text(), "\n")[cursor.Position.Y]
	if !strings.Contains(line, "+ abc") {
		t.Errorf("cursor row is %q", line)
	}
	x, _, _ := d.find("+ abc")
	if cursor.Position.X != x+len("+ abc") {
		t.Errorf("cursor at %d, text ends at %d", cursor.Position.X, x+len("+ abc"))
	}
	d.clearComposer()
}

func TestComposerGrowsThenScrolls(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "Answer.")
	base := d.m.geo.composer.H
	for i := range 12 {
		d.key("line")
		d.key("ctrl+j")
		want := min(i+2, 6)
		if got := d.m.geo.composer.H - base + 1; got != want {
			t.Fatalf("after %d lines the box has %d rows of text, want %d", i+1, got, want)
		}
	}
	if d.m.geo.composer.H != base+5 {
		t.Errorf("composer height %d", d.m.geo.composer.H)
	}
	if lines := strings.Count(d.m.composer.Value(), "\n"); lines != 12 {
		t.Errorf("the draft has %d lines: text was refused", lines)
	}
	d.clearComposer()
	if d.m.geo.composer.H != base {
		t.Errorf("composer did not shrink: %d", d.m.geo.composer.H)
	}
}

func TestComposerMovesToTheBottom(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 40)
	home := d.m.geo.composer.Y
	if home < 10 || home > 32 {
		t.Errorf("composer at row %d on the home screen", home)
	}
	d.reply(g, "Question", "Answer.")
	if docked := d.m.geo.composer.Y; docked <= home || docked < 34 {
		t.Errorf("composer at row %d in a conversation, %d at home", docked, home)
	}
}

func TestLongTitlesTruncate(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.expect("Budget memo draft for the…")
}

func TestHistoryGroups(t *testing.T) {
	g := newFakeGateway(t)
	now := time.Now().UTC()
	g.set(func() {
		g.chats = []map[string]any{
			{"id": "a", "title": "Today chat", "created_at": now.Format(time.RFC3339), "updated_at": now.Format(time.RFC3339), "message_count": 2},
			{"id": "b", "title": "Yesterday chat", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-24 * time.Hour).Format(time.RFC3339), "message_count": 2},
			{"id": "c", "title": "Last week chat", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-96 * time.Hour).Format(time.RFC3339), "message_count": 2},
			{"id": "d", "title": "Old chat", "created_at": now.Format(time.RFC3339), "updated_at": now.Add(-600 * time.Hour).Format(time.RFC3339), "message_count": 2},
		}
	})
	d := newDriver(t, g, 120, 40)
	d.expect("Today", "Yesterday", "Previous 7 days", "Older")
}

// The Settings choices change what they say they change.
func TestSettingsControls(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.key("f2")
	d.clickText("Light")
	if d.m.themeSetting != "light" || d.m.th.Name != "light" {
		t.Errorf("theme %s", d.m.themeSetting)
	}
	d.expect("● Light")
	d.clickText("Off")
	if d.m.mouse {
		t.Error("mouse stayed on")
	}
	if d.m.View().MouseMode != tea.MouseModeNone {
		t.Error("the frame still asks for the mouse")
	}
	d.do2("set-mouse", "on")
	if p := d.m.topPanel(); p == nil {
		t.Error("the choice closed the panel")
	}
	d.keys("right", "enter")
	d.closeAll()
}
