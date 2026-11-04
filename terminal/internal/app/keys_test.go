package app

import (
	"image/color"
	"io"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// holdReply starts a reply that waits for the test to release it.
func holdReply(t *testing.T, d *driver, g *fakeGateway, text string) {
	t.Helper()
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": text}), "HOLD", doneEvent("stop")) })
	d.submit("A question")
	d.until("the reply to start", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
}

func release(d *driver, g *fakeGateway) {
	d.t.Helper()
	g.release <- struct{}{}
	d.finish()
}

func TestEnterSendsAndNewLineKeysDoNot(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.script = replyScript(doneEvent("stop")) })

	d.keys("one", "ctrl+j", "two", "shift+enter", "three")
	if got := d.m.composer.Value(); got != "one\ntwo\nthree" {
		t.Fatalf("composer holds %q", got)
	}
	if g.requestCount() != 0 {
		t.Fatal("a new-line key sent the message")
	}
	d.key("enter")
	d.finish()
	if got := g.lastRequest(t).Message; got != "one\ntwo\nthree" {
		t.Fatalf("sent %q", got)
	}
	if got := d.m.composer.Value(); got != "" {
		t.Fatalf("composer kept %q", got)
	}
}

func TestEnterWithNothingToSend(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("enter")
	d.keys("   ", "ctrl+j", "enter")
	d.settle(5 * time.Millisecond)
	if g.requestCount() != 0 {
		t.Fatal("sent an empty message")
	}
}

func TestEnterDoesNotSendDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	holdReply(t, d, g, "Partial")

	d.key("next question")
	d.key("enter")
	d.settle(20 * time.Millisecond)
	if n := g.requestCount(); n != 1 {
		t.Fatalf("%d requests during a reply", n)
	}
	if got := d.m.composer.Value(); got != "next question" {
		t.Fatalf("composer holds %q", got)
	}
	d.expect("Wait for this reply to finish")

	release(d, g)
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("enter")
	d.finish()
	if got := g.lastRequest(t).Message; got != "next question" {
		t.Fatalf("sent %q", got)
	}
}

func TestPasteStaysInTheComposer(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.send(tea.PasteMsg{Content: "one\r\ntwo\rthree\n"})
	if got := d.m.composer.Value(); got != "one\ntwo\nthree\n" {
		t.Fatalf("composer holds %q", got)
	}
	if g.requestCount() != 0 {
		t.Fatal("paste sent the message")
	}
	d.clearComposer()

	d.key("ctrl+o")
	d.send(tea.PasteMsg{Content: "ignored"})
	if d.m.composer.Value() != "" {
		t.Error("paste went behind a panel")
	}
	d.closeAll()
}

func TestCtrlVPastesFromTheClipboard(t *testing.T) {
	g := newFakeGateway(t)
	clip := &fakeClipboard{content: "line one\r\nline two"}
	d := g.open(t, rig{clip: clip})
	d.key("ctrl+v")
	d.until("paste", func() bool { return d.m.composer.Value() != "" })
	if got := d.m.composer.Value(); got != "line one\nline two" {
		t.Fatalf("composer holds %q", got)
	}
	d.clearComposer()
	clip.readErr = errFake
	d.key("ctrl+v")
	d.settle(10 * time.Millisecond)
	if d.m.composer.Value() != "" {
		t.Error("a failed read changed the composer")
	}
}

// Esc closes a list, then a panel, then stops the reply, and each press does only
// the first of those.
func TestEscPrecedence(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 70, 24)
	holdReply(t, d, g, "Partial")

	d.key("/")
	if !d.m.palette.Open {
		t.Fatal("no palette")
	}
	d.key("esc")
	if d.m.palette.Open {
		t.Error("Esc left the palette open")
	}
	if d.m.active == nil || d.m.active.stopping {
		t.Error("Esc on a list stopped the reply")
	}
	d.clearComposer()

	d.key("f1")
	if d.m.topPanel() == nil {
		t.Fatal("no panel")
	}
	d.key("esc")
	if d.m.topPanel() != nil {
		t.Error("Esc left the panel open")
	}
	if d.m.active.stopping {
		t.Error("Esc on a panel stopped the reply")
	}

	d.key("ctrl+b")
	if !d.m.sideOverlay {
		t.Fatal("no overlay")
	}
	d.key("esc")
	if d.m.sideOverlay {
		t.Error("Esc left the chat list open")
	}
	if d.m.active.stopping {
		t.Error("Esc on the chat list stopped the reply")
	}

	d.key("esc")
	d.finish()
	if got := d.m.transcript.Last().Note; got != views.Stopped {
		t.Errorf("note is %q", got)
	}

	// With nothing to close or stop, Esc does no harm.
	d.key("draft")
	d.key("esc")
	if got := d.m.composer.Value(); got != "draft" {
		t.Errorf("composer holds %q", got)
	}
}

func TestCtrlCDoublePress(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock})

	d.key("ctrl+c")
	d.expect("Press Ctrl+C again to quit.")
	d.settle(5 * time.Millisecond)
	if d.quit {
		t.Fatal("one press quit")
	}
	clock.Advance(3 * time.Second)
	d.key("ctrl+c")
	d.settle(5 * time.Millisecond)
	if d.quit {
		t.Fatal("two presses far apart quit")
	}
	clock.Advance(time.Second)
	d.key("ctrl+c")
	d.until("quit", func() bool { return d.quit })
}

func TestCtrlCStopsThenQuits(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock})
	holdReply(t, d, g, "Partial")

	d.key("ctrl+c")
	d.finish()
	if d.quit {
		t.Fatal("the first press quit")
	}
	if got := d.m.transcript.Last().Note; got != views.Stopped {
		t.Errorf("note is %q", got)
	}
	d.expect("Press Ctrl+C again to quit.")
	d.key("ctrl+c")
	d.until("quit", func() bool { return d.quit })
}

// Two presses in a row during a reply: the stop is still running when the second
// arrives.
func TestCtrlCTwiceDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	holdReply(t, d, g, "Partial")
	d.keys("ctrl+c", "ctrl+c")
	d.until("quit", func() bool { return d.quit })
	if d.m.active != nil {
		t.Error("quit with a reply still running")
	}
}

func TestExitDuringAReplyStopsFirst(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	holdReply(t, d, g, "Partial")
	d.submit("/exit")
	d.until("quit", func() bool { return d.quit })
	if d.m.active != nil {
		t.Error("quit with a reply still running")
	}
}

func TestCtrlNNewChat(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "Answer.")
	d.key("draft")
	d.key("ctrl+n")
	d.settle(5 * time.Millisecond)
	if len(d.m.transcript.Messages) != 0 || d.m.currentID != "" {
		t.Fatal("still in the conversation")
	}
	d.expect(views.Greeting)
	if got := d.m.composer.Value(); got != "draft" {
		t.Errorf("Ctrl+N changed the draft to %q", got)
	}
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("enter")
	d.finish()
	if g.lastRequest(t).ConversationID != nil {
		t.Error("a new chat sent the old conversation id")
	}
}

func TestCtrlBTogglesTheSidebar(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.expect("+ New chat")
	d.key("ctrl+b")
	d.refute("+ New chat")
	d.expect("▸ Chats")
	d.key("ctrl+b")
	d.expect("+ New chat")

	d.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	d.rewrap()
	d.refute("New chat")
	d.key("ctrl+b")
	d.expect("New chat")
	if d.m.focus != focusList {
		t.Error("the overlay did not take focus")
	}
	d.key("ctrl+b")
	d.refute("New chat")
	if d.m.focus != focusComposer {
		t.Error("focus did not come back")
	}
}

func TestCtrlOOpensThePicker(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("ctrl+o")
	if p := d.m.topPanel(); p == nil || p.kind != panelFiles {
		t.Fatal("no file picker")
	}
	d.key("ctrl+o")
	if len(d.m.panels) != 1 {
		t.Error("Ctrl+O stacked a second picker")
	}
}

func TestCtrlYCopies(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("ctrl+y")
	d.expect("There's nothing to copy yet.")

	d.reply(g, "Question", "Prose before.\n\n```go\nfunc one() {}\n```\n\nThen.\n\n```python\nprint(2)\n```\n")
	d.key("ctrl+y")
	d.until("copy", func() bool { return d.clip.last() != "" })
	if got := d.clip.last(); !strings.Contains(got, "Prose before.") {
		t.Errorf("Basic copied %q", got)
	}
	// The write happens before its result reaches the model, which then says so.
	d.until("the confirmation", func() bool { return d.m.notice == "Copied." })
	d.expect("Copied.")

	enterAdvanced(t, d)
	d.key("ctrl+y")
	d.until("copy", func() bool { return !strings.Contains(d.clip.last(), "Prose") })
	if got := d.clip.last(); got != "print(2)" {
		t.Errorf("Advanced copied %q", got)
	}
	d.submit("/copy code 1")
	d.until("copy", func() bool { return d.clip.last() == "func one() {}" })
	d.submit("/copy code 3")
	d.expect("only 2 code blocks")
	d.submit("/copy")
	d.until("copy", func() bool { return strings.Contains(d.clip.last(), "Prose before.") })
}

// The clipboard falls back to OSC 52 when the native write fails.
func TestCopyFallsBackToOSC52(t *testing.T) {
	g := newFakeGateway(t)
	clip := &fakeClipboard{writeErr: errFake}
	d := g.open(t, rig{clip: clip})
	d.reply(g, "Question", "Answer.")
	d.key("ctrl+y")
	d.until("fallback", func() bool {
		for _, seen := range d.seen {
			if strings.Contains(seen, "setClipboardMsg") {
				return true
			}
		}
		return false
	})
}

func TestCtrlRTriesAgainAndIsIgnoredDuringAReply(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("ctrl+r")
	d.settle(5 * time.Millisecond)
	if g.requestCount() != 0 {
		t.Fatal("Ctrl+R with nothing to retry sent a request")
	}

	d.reply(g, "Question", "First answer.")
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Second"}), "HOLD", doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.until("regenerate", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	if !g.lastRequest(t).Regenerate {
		t.Fatal("Ctrl+R did not regenerate")
	}
	d.key("ctrl+r")
	d.settle(20 * time.Millisecond)
	if n := g.requestCount(); n != 2 {
		t.Errorf("%d requests: Ctrl+R during a reply was not ignored", n)
	}
	release(d, g)
}

func TestCtrlTCyclesTheTheme(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{theme: "auto"})
	want := []string{"dark", "light", "auto", "dark"}
	for _, name := range want {
		d.key("ctrl+t")
		if d.m.themeSetting != name {
			t.Fatalf("theme is %s, want %s", d.m.themeSetting, name)
		}
	}
	d.key("ctrl+t")
	if d.m.th.Name != "light" {
		t.Errorf("drawing with %s", d.m.th.Name)
	}
	d.send(tea.BackgroundColorMsg{Color: ansiWhite})
	d.keys("ctrl+t")
	if d.m.themeSetting != "auto" || d.m.th.Name != "light" {
		t.Errorf("auto did not reuse the terminal's answer: %s %s", d.m.themeSetting, d.m.th.Name)
	}
}

// The textarea's own bindings for these keys must never run.
func TestTextareaDefaultsDoNotLeak(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("abc")
	for _, key := range []string{"ctrl+t", "ctrl+b", "ctrl+n"} {
		d.key(key)
		if got := d.m.composer.Value(); got != "abc" {
			t.Fatalf("%s changed the draft to %q", key, got)
		}
		if key == "ctrl+b" {
			d.key("ctrl+b")
		}
	}
	d.key("d")
	if got := d.m.composer.Value(); got != "abcd" {
		t.Fatalf("%q: a key moved the cursor", got)
	}
}

func TestDeleteKeyInTheList(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.keys("tab", "down", "down")
	chat, ok := d.m.sidebar.CursorChat()
	if !ok {
		t.Fatalf("cursor at %d", d.m.sidebar.Cursor)
	}
	d.key("delete")
	if p := d.m.topPanel(); p == nil || p.kind != panelConfirm {
		t.Fatal("no confirmation")
	}
	d.expect(chat.Title[:12])
	d.key("enter") // Cancel has focus.
	d.settle(5 * time.Millisecond)
	if len(g.deleted) != 0 {
		t.Fatal("Enter deleted without choosing Delete")
	}
	d.keys("tab", "down", "down", "delete", "tab", "enter")
	d.until("delete", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.deleted) == 1 })
	if g.deleted[0] != chat.ID {
		t.Errorf("deleted %s, want %s", g.deleted[0], chat.ID)
	}
}

func TestBackspaceRemovesTheLastChip(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.m.attachments = append(d.m.attachments, attachFile(client.Attachment{Name: "a.txt"}), attachFile(client.Attachment{Name: "b.txt"}))
	d.m.useDocs = true
	d.m.syncChips()
	d.send(struct{}{})
	d.expect("a.txt ×", "b.txt ×", "Approved documents ×")

	d.key("backspace")
	d.refute("Approved documents")
	d.key("backspace")
	d.refute("b.txt")
	d.key("x")
	d.key("backspace")
	d.expect("a.txt ×")
	d.key("backspace")
	d.refute("a.txt")
	d.key("backspace")
}

func TestTabMovesFocus(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")

	order := []focusArea{focusList, focusConversation, focusComposer}
	for _, want := range order {
		d.key("tab")
		if d.m.focus != want {
			t.Fatalf("Tab reached %d, want %d", d.m.focus, want)
		}
	}
	for _, want := range []focusArea{focusConversation, focusList, focusComposer} {
		d.key("shift+tab")
		if d.m.focus != want {
			t.Fatalf("Shift+Tab reached %d, want %d", d.m.focus, want)
		}
	}

	// A list that is not on screen is skipped.
	d.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	d.rewrap()
	d.key("tab")
	if d.m.focus != focusConversation {
		t.Errorf("Tab reached %d with the list hidden", d.m.focus)
	}
	d.key("tab")
}

func TestTypingFromAnyFocusReachesTheComposer(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")
	for _, keys := range [][]string{{"tab"}, {"tab", "tab"}} {
		d.keys(keys...)
		d.key("x")
		if d.m.focus != focusComposer || !strings.HasSuffix(d.m.composer.Value(), "x") {
			t.Errorf("after %v: focus %d, draft %q", keys, d.m.focus, d.m.composer.Value())
		}
	}
}

func TestScrollKeys(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", strings.Repeat("A paragraph of the reply.\n\n", 60))
	tr := d.m.transcript
	if !tr.AtBottom() {
		t.Fatal("a finished reply should rest at the bottom")
	}
	d.key("pgup")
	if tr.AtBottom() {
		t.Fatal("PgUp did not scroll")
	}
	d.expect("▾ Latest")
	d.key("pgdown")
	if !tr.AtBottom() {
		t.Fatal("PgDn did not return")
	}
	d.refute("▾ Latest")

	// With focus on the conversation, these scroll on every platform.
	d.keys("tab", "tab")
	if d.m.focus != focusConversation {
		t.Fatal("focus is not on the conversation")
	}
	d.key("up")
	if tr.AtBottom() {
		t.Fatal("Up did not scroll")
	}
	d.key("down")
	if !tr.AtBottom() {
		t.Fatal("Down did not scroll")
	}
	d.key("home")
	d.expect("Question")
	if tr.AtBottom() {
		t.Fatal("Home did not scroll")
	}
	d.key("end")
	if !tr.AtBottom() {
		t.Fatal("End did not scroll")
	}
}

func TestWheelFollowsThePointer(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", strings.Repeat("A paragraph of the reply.\n\n", 60))
	wheel := func(x, y int, button tea.MouseButton) {
		d.send(tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: button}))
	}
	tr := d.m.transcript
	r := d.m.geo.transcript

	// Over the sidebar the conversation stays put.
	wheel(2, 10, tea.MouseWheelUp)
	if !tr.AtBottom() {
		t.Error("the wheel over the sidebar scrolled the conversation")
	}
	wheel(r.X+3, r.Y+3, tea.MouseWheelUp)
	if tr.AtBottom() {
		t.Error("the wheel did not scroll the conversation")
	}
	wheel(r.X+3, r.Y+3, tea.MouseWheelDown)
	if !tr.AtBottom() {
		t.Error("the wheel did not scroll back")
	}

	// Over a panel it scrolls the panel, and nothing behind it.
	d.key("f1")
	wheel(60, 20, tea.MouseWheelDown)
	if d.m.topPanel().scroll == 0 {
		t.Error("the wheel did not scroll the panel")
	}
	if !tr.AtBottom() {
		t.Error("the wheel reached behind a panel")
	}
}

func TestFunctionKeys(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("f1")
	d.expect("Help", "× Close")
	d.key("f1")
	if d.m.topPanel() != nil {
		t.Error("F1 did not close Help")
	}
	d.key("f2")
	d.expect("Settings", "Appearance")
	d.key("f2")
	if d.m.topPanel() != nil {
		t.Error("F2 did not close Settings")
	}

	d.key("f3")
	if d.m.inspectorTop {
		t.Error("F3 opened the inspector in Basic")
	}
	d.refute("Inspector")

	enterAdvanced(t, d)
	d.key("f3")
	if !d.m.inspectorTop {
		t.Error("F3 did not open the inspector overlay")
	}
	d.expect("Inspector", "MODEL", "USAGE", "CONTEXT", "BACKEND")
	d.key("f3")
	if d.m.inspectorTop {
		t.Error("F3 did not close the overlay")
	}

	d.send(tea.WindowSizeMsg{Width: 130, Height: 40})
	d.rewrap()
	d.expect("MODEL")
	d.key("f3")
	d.refute("MODEL")
	d.key("f3")
	d.expect("MODEL")
}

func TestPaletteKeys(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("/")
	if !d.m.palette.Open || len(d.m.palette.Items) < 5 {
		t.Fatal("no palette on a leading slash")
	}
	for _, item := range d.m.palette.Items {
		if item.Advanced {
			t.Errorf("%s is listed in Basic", item.Name)
		}
	}
	d.keys("down", "down")
	if d.m.palette.Cursor != 2 {
		t.Errorf("cursor at %d", d.m.palette.Cursor)
	}
	d.key("up")
	if d.m.palette.Cursor != 1 {
		t.Errorf("cursor at %d", d.m.palette.Cursor)
	}
	d.key("tab")
	if got := d.m.composer.Value(); got != d.m.palette.Items[0].Name && !strings.HasPrefix(got, "/new") {
		t.Errorf("Tab completed to %q", got)
	}
	d.clearComposer()

	d.key("/pri")
	if len(d.m.palette.Items) != 1 {
		t.Fatalf("filter left %d rows", len(d.m.palette.Items))
	}
	d.key("enter")
	if p := d.m.topPanel(); p == nil || p.kind != panelPrivacy {
		t.Fatal("Enter did not run the command")
	}
	d.key("esc")
	if d.m.composer.Value() != "" {
		t.Errorf("the command stayed in the composer: %q", d.m.composer.Value())
	}

	// A slash anywhere else is ordinary text, and so is a multi-line message.
	d.key("a/b")
	if d.m.palette.Open {
		t.Error("the palette opened for a slash mid-text")
	}
	d.clearComposer()
	d.keys("/help", "ctrl+j", "more")
	if d.m.palette.Open {
		t.Error("the palette stayed open on a multi-line message")
	}
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	d.key("enter")
	d.finish()
	if got := g.lastRequest(t).Message; got != "/help\nmore" {
		t.Errorf("sent %q", got)
	}

	enterAdvanced(t, d)
	d.key("/mod")
	found := map[string]bool{}
	for _, item := range d.m.palette.Items {
		found[item.Name] = true
	}
	if !found["/model"] || !found["/mode"] {
		t.Errorf("Advanced list: %v", found)
	}
	d.clearComposer()
}

// Up and Down reach the rows that scroll out of a short list.
func TestPaletteScrollsToEveryRow(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 80, 24)
	d.key("/")
	for i := range d.m.palette.Items {
		d.m.palette.Cursor = i
		d.expect(d.m.palette.Items[i].Name)
	}
}

func TestShiftEnterHint(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.expect("Ctrl+J new line")
	d.refute("Shift+Enter")

	d.send(tea.ModeReportMsg{Mode: ansi.ModeWin32Input, Value: ansi.ModeReset})
	d.refute("Shift+Enter")
	d.send(tea.ModeReportMsg{Mode: ansi.ModeWin32Input, Value: ansi.ModeNotRecognized})
	d.refute("Shift+Enter")
	d.send(tea.ModeReportMsg{Mode: ansi.ModeWin32Input, Value: ansi.ModeSet})
	d.expect("Shift+Enter new line")
	d.key("f1")
	d.expect("Shift+Enter or Ctrl+J")
	d.closeAll()

	k := g.open(t, rig{})
	k.send(tea.KeyboardEnhancementsMsg{Flags: 1})
	k.expect("Shift+Enter new line")
}

func TestFilterDropsWin32Noise(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyReleaseMsg{Code: 'a'},
		tea.KeyPressMsg{Code: tea.KeyLeftShift},
		tea.KeyPressMsg{Code: tea.KeyRightCtrl},
		tea.KeyPressMsg{Code: tea.KeyLeftAlt},
		tea.KeyPressMsg{Code: tea.KeyIsoLevel5Shift},
	} {
		if Filter(nil, msg) != nil {
			t.Errorf("%#v was not dropped", msg)
		}
	}
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'a', Text: "a"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.PasteMsg{Content: "x"}} {
		if Filter(nil, msg) == nil {
			t.Errorf("%#v was dropped", msg)
		}
	}
}

// Real bytes through the real decoder, for the keys that depend on it.
func TestKeysThroughTheDecoder(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Decoded reply."}), doneEvent("stop"))
	})
	runProgram(t, g, colorprofile.TrueColor, rig{}, func(wait func(string), keys io.Writer) {
		wait("What can I help you with?")
		send := func(s string) {
			if _, err := keys.Write([]byte(s)); err != nil {
				t.Fatal(err)
			}
		}
		send("first")
		wait("first")
		send("\n") // Ctrl+J
		send("second")
		wait("second")
		send("\x1b[13;2u") // Shift+Enter, as the kitty protocol reports it
		send("third")
		wait("third")
		send("\x1b[13;28;13;1;16;1_") // Shift+Enter under win32-input mode
		send("\x1b[13;28;13;0;16;1_") // and its release
		send("fourth")
		wait("fourth")
		send("\x1b[200~pasted\r\nline\x1b[201~")
		wait("pasted")
		send("\r")
		wait("Decoded reply.")
		send("\x03")
		wait("Press Ctrl+C again to quit.")
		send("\x03")
	})
	if got, want := g.lastRequest(t).Message, "first\nsecond\nthird\nfourthpasted\nline"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
}

// The window title, alt screen and mouse mode are set on every frame, including the
// empty ones before the theme resolves.
func TestViewDeclaresTheTerminalState(t *testing.T) {
	g := newFakeGateway(t)
	d := rig{theme: "auto"}.build(t, g.URL)
	first := d.m.View()
	if !first.AltScreen || first.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("first frame: %+v", first)
	}
	if first.Content != "" {
		t.Fatal("drew before the theme resolved")
	}
	d.until("the theme", func() bool { return d.m.themeReady })
	if d.m.th.Name != "dark" {
		t.Errorf("fallback theme is %s", d.m.th.Name)
	}

	off := g.open(t, rig{})
	off.do2("set-mouse", "off")
	if off.m.View().MouseMode != tea.MouseModeNone {
		t.Error("mouse off still captures the mouse")
	}
}

func TestLightTerminalAnswerAvoidsDark(t *testing.T) {
	g := newFakeGateway(t)
	d := rig{theme: "auto"}.build(t, g.URL)
	if d.m.themeReady {
		t.Fatal("ready before the terminal answered")
	}
	d.send(tea.BackgroundColorMsg{Color: ansiWhite})
	if !d.m.themeReady || d.m.th.Name != "light" {
		t.Errorf("ready %v, theme %s", d.m.themeReady, d.m.th.Name)
	}
}

func TestHelpListsEveryKey(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 60})
	d.key("f1")
	for _, key := range []string{"Enter", "Ctrl+J", "Ctrl+V", "Esc", "Ctrl+N", "Ctrl+B", "Ctrl+O", "Ctrl+Y", "Ctrl+R", "Delete", "Backspace", "Ctrl+T", "Tab", "PgUp", "F1", "Ctrl+C"} {
		d.expect(key)
	}
}

// On a Mac the hints name /help, and selecting text uses the right modifier.
func TestMacWording(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{platform: "darwin", term: "iTerm.app", w: 120, h: 50})
	d.refute("F1 Help")
	d.expect("/help")
	d.key("f1")
	for range 8 {
		d.key("pgdown")
	}
	d.expect("Option")

	apple := g.open(t, rig{platform: "darwin", term: "Apple_Terminal", w: 120, h: 50})
	apple.key("f1")
	for range 8 {
		apple.key("pgdown")
	}
	apple.expect("Fn")

	win := g.open(t, rig{platform: "windows", w: 120, h: 50})
	win.expect("F1 Help")
	win.key("f1")
	for range 8 {
		win.key("pgdown")
	}
	win.expect("Shift")
}

func TestCommandNamesFollowThePlatform(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.drop = true })
	mac := rig{platform: "darwin"}.build(t, g.URL)
	mac.until("down", func() bool { return mac.m.conn == connDown })
	mac.expect("python3 dc.py")

	win := rig{platform: "windows"}.build(t, g.URL)
	win.until("down", func() bool { return win.m.conn == connDown })
	win.expect("python dc.py")
	win.refute("python3")
}

var ansiWhite = color.White
