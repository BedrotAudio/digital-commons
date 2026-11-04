package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// Every action in the Mouse section, by clicking what is drawn.

func TestClickSidebar(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "Answer.")

	d.clickText("Vendor letter")
	d.until("open", func() bool { return d.m.currentID == "c3" })
	d.expect("Employees may work remotely after probation.", "×")

	d.clickText("+ New chat")
	d.settle(5 * time.Millisecond)
	d.expect(views.Greeting)

	d.clickText("Privacy")
	if p := d.m.topPanel(); p == nil || p.kind != panelPrivacy {
		t.Fatal("no privacy panel")
	}
	d.clickText("× Close")
	d.settle(5 * time.Millisecond)
	if d.m.topPanel() != nil {
		t.Fatal("Close did not close")
	}
	d.clickText("Settings")
	if p := d.m.topPanel(); p == nil || p.kind != panelSettings {
		t.Fatal("no settings panel")
	}
	d.closeAll()
	d.clickText("Help")
	if p := d.m.topPanel(); p == nil || p.kind != panelHelp {
		t.Fatal("no help panel")
	}
	d.closeAll()
}

func TestClickDeleteOnTheSelectedRow(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
	d := newDriver(t, g, 120, 40)
	d.clickText("Vendor letter")
	d.until("open", func() bool { return d.m.currentID == "c3" })

	x, y, ok := d.find("×")
	if !ok {
		t.Fatalf("no delete mark:\n%s", d.text())
	}
	if _, row, _ := d.find("Vendor letter"); row != y {
		t.Fatalf("the × is on row %d, the chat on %d", y, row)
	}
	d.click(x, y)
	if p := d.m.topPanel(); p == nil || p.kind != panelConfirm {
		t.Fatalf("no confirmation:\n%s", d.text())
	}
	d.expect("Vendor letter")
	d.clickText("  Delete  ")
	d.until("deleted", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.deleted) == 1 })
	if g.deleted[0] != "c3" {
		t.Errorf("deleted %v", g.deleted)
	}
	d.until("home", func() bool { return len(d.m.transcript.Messages) == 0 })
}

func TestClickComposerControls(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("Body"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := g.open(t, rig{home: home})

	x, y, ok := d.find("+ Message")
	if !ok {
		t.Fatal("no composer")
	}
	d.click(x, y)
	if p := d.m.topPanel(); p == nil || p.kind != panelFiles {
		t.Fatalf("the + did not open the picker:\n%s", d.text())
	}
	d.closeAll()

	// Clicking Send sends, and clicking the box returns focus to it.
	d.key("tab")
	if d.m.focus == focusComposer {
		t.Fatal("focus did not move")
	}
	x, y, _ = d.find("Message Digital")
	d.click(x+3, y)
	if d.m.focus != focusComposer {
		t.Error("clicking the box did not focus it")
	}
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Partial"}), "HOLD", doneEvent("stop"))
	})
	d.key("Question")
	d.clickText("Send")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	if got := g.lastRequest(t).Message; got != "Question" {
		t.Fatalf("sent %q", got)
	}

	d.clickText("■ Stop")
	d.finish()
	d.expect(views.Stopped)
}

func TestClickChips(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.m.attachments = append(d.m.attachments, attachFile(client.Attachment{Name: "a.txt"}), attachFile(client.Attachment{Name: "b.txt"}))
	d.m.useDocs = true
	d.m.syncChips()
	d.send(struct{}{})

	x, y, ok := d.find("a.txt ×")
	if !ok {
		t.Fatalf("no chip:\n%s", d.text())
	}
	d.click(x+len("a.txt "), y)
	d.refute("a.txt")
	d.expect("b.txt ×", "Approved documents ×")
	if len(d.m.attachments) != 1 || d.m.attachments[0].Name != "b.txt" {
		t.Errorf("attachments: %+v", d.m.attachments)
	}
}

func TestClickReplyActions(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "First answer.")
	d.clickText("Copy")
	d.until("copy", func() bool { return d.clip.last() == "First answer." })

	// Scrolled, the targets move with the text.
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": strings.Repeat("Long paragraph.\n\n", 40)}), doneEvent("stop"))
	})
	d.key("ctrl+r")
	d.finish()
	d.key("pgup")
	d.send(struct{}{})
	d.refute("Copy · Try again")
	before := g.requestCount()
	y := d.m.geo.transcript.Y + d.m.geo.transcript.H - 3
	d.click(d.m.geo.transcript.X+3, y)
	d.settle(5 * time.Millisecond)
	if g.requestCount() != before {
		t.Error("a click where the buttons were not drew a retry")
	}
	d.m.transcript.GotoBottom()
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Third."}), doneEvent("stop")) })
	d.clickText("Try again")
	d.finish()
	d.expect("Third.")
}

func TestClickHeader(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 70, 24)
	d.clickText("▸ Chats")
	if !d.m.sideOverlay {
		t.Fatal("the header control did not open the list")
	}
	d.clickText("Vendor letter")
	d.until("open", func() bool { return !d.m.sideOverlay })

	d.key("ctrl+n")
	d.settle(5 * time.Millisecond)
	d.clickText("On this computer")
	if p := d.m.topPanel(); p == nil || p.kind != panelPrivacy {
		t.Fatal("the indicator did not open Privacy")
	}
}

func TestClickOutsideOverlays(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 70, 24)
	d.key("ctrl+b")
	d.click(60, 12)
	if d.m.sideOverlay {
		t.Error("clicking outside left the list open")
	}
	d.key("f1")
	d.click(0, 0)
	if d.m.topPanel() != nil {
		t.Error("clicking outside left the panel open")
	}
	d.submit("/delete")
	d.reply(g, "Question", "Answer.")
	d.submit("/delete")
	d.click(0, 0)
	if d.m.topPanel() == nil {
		t.Error("clicking outside dismissed a confirmation")
	}
	d.closeAll()
}

func TestClickPaletteRow(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("/")
	d.clickText("/privacy")
	if p := d.m.topPanel(); p == nil || p.kind != panelPrivacy {
		t.Fatalf("clicking a row did not run it:\n%s", d.text())
	}
	d.closeAll()
	if d.m.composer.Value() != "" {
		t.Errorf("composer holds %q", d.m.composer.Value())
	}

	// A row that needs an argument completes instead.
	d.key("/")
	d.key("t")
	d.clickText("/theme")
	if got := d.m.composer.Value(); got != "/theme " {
		t.Errorf("composer holds %q", got)
	}
	d.clearComposer()
}

func TestSuggestionCardsByKeyboard(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	// Tab to the list, Tab to the cards, then move and press Enter.
	d.keys("tab", "tab", "right", "right")
	d.key("enter")
	if got := d.m.composer.Value(); !strings.HasPrefix(got, "Help me draft") {
		t.Errorf("composer holds %q", got)
	}
}

func TestMoreSuggestionsPages(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 80, 24)
	if !d.m.geo.plan.more {
		t.Fatalf("all cards fit at this size:\n%s", d.text())
	}
	first := d.m.geo.plan.items[0]
	d.clickText("More suggestions")
	if d.m.geo.plan.items[0] == first {
		t.Error("paging did not move")
	}
}

func TestInspectorOverlayClick(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	enterAdvanced(t, d)
	d.clickText("inspector")
	if !d.m.inspectorTop {
		t.Fatalf("the footer control did not open it:\n%s", d.text())
	}
	d.clickText("× Close")
	if d.m.inspectorTop {
		t.Error("Close did not close the overlay")
	}
	d.clickText("/model")
	d.until("picker", func() bool { return d.m.topPanel() != nil && d.m.topPanel().kind == panelModels })
	d.closeAll()
}

func TestMouseIgnoredBelowMinimumSize(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.send(tea.WindowSizeMsg{Width: 30, Height: 8})
	d.click(5, 2)
	d.send(tea.MouseWheelMsg(tea.Mouse{X: 5, Y: 2, Button: tea.MouseWheelDown}))
	d.settle(5 * time.Millisecond)
	if d.m.topPanel() != nil {
		t.Error("a click went through the enlarge message")
	}
}

func TestRightClickDoesNothing(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	x, y, _ := d.find("+ New chat")
	d.send(tea.MouseClickMsg(tea.Mouse{X: x + 1, Y: y, Button: tea.MouseRight}))
	x, y, _ = d.find("Privacy")
	d.send(tea.MouseClickMsg(tea.Mouse{X: x + 1, Y: y, Button: tea.MouseRight}))
	if d.m.topPanel() != nil {
		t.Error("a right click acted")
	}
}
