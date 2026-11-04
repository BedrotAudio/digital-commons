package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Advanced for an account without view_diagnostics: the gateway sends no provider
// details, and every place that reads them must cope.
func TestAdvancedWithoutDiagnostics(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.boot["permissions"].(map[string]any)["view_diagnostics"] = false
		g.status["inference"] = nil
		g.script = []string{sse("start", map[string]any{"request_id": "r1", "conversation_id": "new1", "message_id": "m9", "title": "T",
			"route":   map[string]any{"requested_tier": "auto", "tier": "standard", "locality": "this_device"},
			"context": map[string]any{"estimated_tokens": 10, "budget_tokens": 100, "omitted_messages": 2}, "notices": []any{}}),
			sse("delta", map[string]any{"text": "Answer."}), doneEvent("stop")}
	})
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)
	d.submit("Question")
	d.finish()
	d.expect("2 messages omitted")
	d.submit("/status")
	d.expect("not shown for this account")
	d.refute("ollama")
	assertFits(t, d, "no diagnostics", 130, 40)
}

// A gateway that answers but fails is not one that is not running.
func TestBootstrapServerErrorIsNotCalledNotRunning(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.code = 500 })
	d := rig{}.build(t, g.URL)
	d.until("failure", func() bool { return d.m.conn == connDown })
	d.send(struct{}{})
	d.expect(views.Unreachable)
	d.refute(views.NotRunning)
}

func TestConnectingShowsTheAddressWhenAdvancedIsConfigured(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.drop = true })
	d := rig{config: func(c *config.Config) { c.Application.DefaultMode = config.ModeDeveloper }}.build(t, g.URL)
	d.expect("Connecting to 127.0.0.1")
	d.until("failure", func() bool { return d.m.conn == connDown })
}

// A new local token is picked up by the retry, with no restart.
func TestTokenChangeRecovers(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.code = 401 })
	dialed := 0
	d := rig{dial: func() *client.Client {
		dialed++
		return client.New(client.Options{BaseURL: g.URL, Token: "new"})
	}}.build(t, g.URL)
	d.until("refusal", func() bool { return d.m.conn == connAuth })
	g.set(func() { g.code = 0 })
	d.send(retryMsg{})
	d.until("recovery", func() bool { return d.m.conn == connUp })
	if dialed < 2 {
		t.Errorf("dialed %d times", dialed)
	}
}

// A status route that fails beside a bootstrap that works must not spin.
func TestFailingStatusDoesNotHammerTheGateway(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.statusCode = 500
		g.hits["/v1/bootstrap"] = 0
		g.hits["/v1/status"] = 0
	})
	d.send(pollMsg{d.m.pollGen})
	d.settle(300 * time.Millisecond)
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.hits["/v1/status"]; n > 3 {
		t.Errorf("%d status calls in 300 ms", n)
	}
	if !d.m.retrying {
		t.Error("no retry is waiting")
	}
}

// Leaving before a slow chat arrives keeps the person where they went.
func TestLateChatDoesNotPullYouBack(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
	d := newDriver(t, g, 120, 40)
	d.do2("open", "c3")
	d.key("ctrl+n")
	d.settle(20 * time.Millisecond)
	if d.m.currentID != "" || len(d.m.transcript.Messages) != 0 {
		t.Errorf("pulled back to %q", d.m.currentID)
	}
	d.expect(views.Greeting)

	// Of two quick choices, the last one wins.
	g.set(func() { g.convs["c4"] = storedChat("c4", "stop") })
	d.do2("open", "c3")
	d.do2("open", "c4")
	d.until("open", func() bool { return d.m.currentID != "" })
	d.settle(20 * time.Millisecond)
	if d.m.currentID != "c4" {
		t.Errorf("ended on %q", d.m.currentID)
	}
}

func TestRefusalsBeforeTheFirstConnection(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.drop = true })
	d := rig{}.build(t, g.URL)
	d.submit("/mode developer")
	d.expect("Still connecting.")
	d.refute("your account")
	d.m.notice = ""
	d.submit("/search")
	d.expect("Still connecting.")
}

func TestPanelFailureIsPlain(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.drop = true })
	d.submit("/privacy")
	d.until("failure", func() bool { return !d.m.topPanel().loading })
	d.expect(views.NotRunning)
	d.refute("EOF")
	d.closeAll()
}

func TestHelpOffersOnlyModesTheAccountHas(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 60})
	d.key("f1")
	d.expect("/mode developer")
	d.closeAll()

	g.set(func() { g.boot["permissions"].(map[string]any)["advanced_mode"] = false })
	e := g.open(t, rig{w: 120, h: 60})
	e.key("f1")
	e.refute("/mode developer")
}

func TestStatusPanelWhenDown(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.drop = true })
	d.send(pollMsg{d.m.pollGen})
	d.until("down", func() bool { return d.m.conn == connDown })
	d.submit("/status")
	d.expect("Not connected", views.NotRunning)
	d.refute(views.Connecting)
}

func TestChoicesPersistButModeDoesNot(t *testing.T) {
	g := newFakeGateway(t)
	path := filepath.Join(t.TempDir(), "state.json")
	d := g.open(t, rig{config: func(c *config.Config) { c.StatePath = path }})
	enterAdvanced(t, d)
	d.submit("/theme light")
	d.do2("set-mouse", "off")
	d.until("save", func() bool { return !d.m.saving })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state["mode"]; ok || strings.Contains(string(data), "developer") {
		t.Errorf("the mode was saved: %s", data)
	}
	if !strings.Contains(string(data), "light") || !strings.Contains(string(data), "false") {
		t.Errorf("the last choices were not saved: %s", data)
	}
}

func TestNoticeExpiresAndDoesNotMoveTheHomeScreen(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock, w: 120, h: 40})
	y := d.m.geo.composer.Y
	d.m.setNotice("A short notice.")
	d.send(struct{}{})
	d.expect("A short notice.")
	if got := d.m.geo.composer.Y; got != y {
		t.Errorf("a notice moved the composer from %d to %d", y, got)
	}
	clock.Advance(noticeFor + time.Second)
	d.send(clockMsg{})
	d.refute("A short notice.")
}

func TestWaitDotAnimates(t *testing.T) {
	g := newFakeGateway(t)
	clock := newClock()
	d := g.open(t, rig{clock: clock})
	g.set(func() { g.script = []string{"HOLD"} })
	d.submit("Question")
	d.settle(130 * time.Millisecond)
	seen := map[string]bool{}
	for range 8 {
		clock.Advance(320 * time.Millisecond)
		d.send(tickMsg{d.m.active.id})
		line := ""
		for _, row := range strings.Split(d.text(), "\n") {
			if strings.Contains(row, "Thinking…") {
				line = row
			}
		}
		seen[line] = true
	}
	if len(seen) < 3 {
		t.Errorf("the wait has %d frames", len(seen))
	}
	d.key("esc")
	d.finish()
}

func TestEmptyReplyIsNotBlank(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() { g.script = []string{startOnly("new1"), doneEvent("stop")} })
	d.submit("Question")
	d.finish()
	d.expect("◆ "+views.NoReply, "Try again")
}

func TestKeyboardReachesReplyActions(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.reply(g, "Question", "First answer.")
	d.keys("tab", "tab")
	d.expect("▸Copy")
	d.key("enter")
	d.until("copy", func() bool { return d.clip.last() == "First answer." })
	d.key("right")
	d.expect("▸Try again")
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Second."}), doneEvent("stop")) })
	d.key("enter")
	d.finish()
	d.expect("Second.")
	if !g.lastRequest(t).Regenerate {
		t.Error("Enter on Try again did not regenerate")
	}
}

func TestKeyboardReachesTheChatList(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
	d := newDriver(t, g, 120, 40)
	d.key("tab")
	for range 3 {
		d.key("down")
	}
	if chat, _ := d.m.sidebar.CursorChat(); chat.ID != "c3" {
		t.Fatalf("cursor on %+v", chat)
	}
	d.key("enter")
	d.until("open", func() bool { return d.m.currentID == "c3" })
	if d.m.focus != focusComposer {
		t.Error("opening a chat did not return to the composer")
	}

	d.key("tab")
	for range 10 {
		d.key("down")
	}
	d.key("enter") // Help, the last row.
	if p := d.m.topPanel(); p == nil || p.kind != panelHelp {
		t.Fatalf("the last row did not open Help: %+v", p)
	}
	d.closeAll()

	d.key("tab")
	for range 10 {
		d.key("up")
	}
	d.key("enter") // New chat, the first row.
	d.settle(5 * time.Millisecond)
	d.expect(views.Greeting)
}

func TestSidebarWheelAfterTheCursorMoved(t *testing.T) {
	g := newFakeGateway(t)
	now := time.Now().UTC().Format(time.RFC3339)
	g.set(func() {
		g.chats = nil
		for i := range 60 {
			g.chats = append(g.chats, map[string]any{"id": fmt.Sprintf("x%d", i), "title": fmt.Sprintf("Chat number %d", i), "created_at": now, "updated_at": now, "message_count": 2})
		}
	})
	d := newDriver(t, g, 100, 30)
	d.expect("Chat number 3")
	d.keys("tab", "down", "down", "esc")
	wheel := func(n int) {
		for range n {
			d.send(tea.MouseWheelMsg(tea.Mouse{X: 3, Y: 10, Button: tea.MouseWheelDown}))
		}
	}
	wheel(4)
	d.refute("Chat number 3 ")
	d.expect("Chat number 14")

	// A row clicked after scrolling is the row that was drawn.
	x, y, ok := d.find("Chat number 14")
	if !ok {
		t.Fatal("no row")
	}
	g.set(func() { g.convs["x14"] = storedChat("x14", "stop") })
	d.click(x, y)
	d.until("open", func() bool { return d.m.currentID == "x14" })

	wheel(40)
	d.expect("Chat number 59")
	d.refute("New chat number")
}

// The list keeps its place in view with the keyboard too.
func TestSidebarCursorScrollsIntoView(t *testing.T) {
	g := newFakeGateway(t)
	now := time.Now().UTC().Format(time.RFC3339)
	g.set(func() {
		g.chats = nil
		for i := range 60 {
			g.chats = append(g.chats, map[string]any{"id": fmt.Sprintf("x%d", i), "title": fmt.Sprintf("Chat number %d", i), "created_at": now, "updated_at": now, "message_count": 2})
		}
	})
	d := newDriver(t, g, 100, 30)
	d.key("tab")
	for range 50 {
		d.key("down")
	}
	d.expect("▸ Chat number 49")
}

func TestPaletteClosesWhenFocusLeaves(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.key("/")
	d.key("shift+tab")
	if d.m.palette.Open {
		t.Error("the list stayed open with focus elsewhere")
	}
	d.clearComposer()
}

func TestKeysDoNothingBelowTheMinimumSize(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.send(tea.WindowSizeMsg{Width: 30, Height: 8})
	d.key("x")
	d.send(tea.PasteMsg{Content: "pasted"})
	d.key("enter")
	d.settle(5 * time.Millisecond)
	if g.requestCount() != 0 || d.m.composer.Value() != "" {
		t.Errorf("typed blind: %q", d.m.composer.Value())
	}
	d.keys("ctrl+c", "ctrl+c")
	d.until("quit", func() bool { return d.quit })
}

func TestOverlaysCloseWhenTheWindowHasRoom(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 70, 24)
	d.key("ctrl+b")
	d.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	if d.m.sideOverlay {
		t.Error("the overlay stayed on top of the sidebar")
	}
	d.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	d.closeAll()
	enterAdvanced(t, d)
	d.key("f3")
	d.send(tea.WindowSizeMsg{Width: 130, Height: 40})
	if d.m.inspectorTop {
		t.Error("the overlay stayed on top of the inspector")
	}
}

func TestListFocusLeavesWhenTheListDisappears(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	d.key("tab")
	if d.m.focus != focusList {
		t.Fatal("no list focus")
	}
	d.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	d.rewrap()
	if d.m.focus != focusComposer {
		t.Errorf("focus %d is on a list that is not there", d.m.focus)
	}
	if d.m.View().Cursor == nil {
		t.Error("no cursor after focus returned")
	}
}

func TestCursorStaysInTheComposer(t *testing.T) {
	g := newFakeGateway(t)
	for _, advanced := range []bool{false, true} {
		d := newDriver(t, g, 100, 30)
		if advanced {
			enterAdvanced(t, d)
		}
		d.reply(g, "Question", "Answer.")
		for i := range 14 {
			d.key(fmt.Sprintf("row%d", i))
			view := d.m.View()
			if view.Cursor == nil {
				t.Fatal("no cursor")
			}
			if line := strings.Split(ansi.Strip(view.Content), "\n")[view.Cursor.Position.Y]; !strings.Contains(line, fmt.Sprintf("row%d", i)) {
				t.Fatalf("%s: cursor row %d reads %q", modeName(advanced), view.Cursor.Position.Y, line)
			}
			x := view.Cursor.Position.X
			line := strings.Split(ansi.Strip(view.Content), "\n")[view.Cursor.Position.Y]
			at := strings.Index(line, fmt.Sprintf("row%d", i))
			if want := ansi.StringWidth(line[:at]) + len(fmt.Sprintf("row%d", i)); x != want {
				t.Fatalf("%s: cursor at %d, text ends at %d", modeName(advanced), x, want)
			}
			d.key("ctrl+j")
		}
	}
}

func TestLongLineWrapsInTheComposerAndSurvivesResize(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 120, 40)
	text := strings.TrimSpace(strings.Repeat("word ", 90))
	d.send(tea.PasteMsg{Content: text})
	if got := d.m.composer.Value(); got != text {
		t.Fatalf("the composer changed the text")
	}
	d.expect("word word")
	assertFits(t, d, "long line", 120, 40)
	if d.m.geo.composer.H < 5 {
		t.Errorf("composer did not grow: %d", d.m.geo.composer.H)
	}
	for _, s := range [][2]int{{40, 12}, {60, 20}, {200, 60}} {
		d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
		d.rewrap()
		assertFits(t, d, "long line", s[0], s[1])
	}
}

func TestAttachmentLimit(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	path := filepath.Join(home, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := g.open(t, rig{home: home})
	for range 4 {
		d.submit("/attach " + path)
		d.settle(5 * time.Millisecond)
	}
	d.until("four", func() bool { return len(d.m.attachments) == 4 })
	d.submit("/attach " + path)
	d.expect("You can attach up to 4 files.")

	d.key("ctrl+o")
	d.until("listing", func() bool { return d.m.picker != nil && len(d.m.picker.Entries) > 0 })
	d.clickText("a.txt")
	d.expect("You can attach up to 4 files.")
	d.closeAll()
}

func TestRefusedSendKeepsTheFiles(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.m.attachments = append(d.m.attachments, attachFile(client.Attachment{Name: "a.txt", Text: "Body"}))
	d.m.syncChips()
	g.set(func() {
		g.chatCode = 415
		g.chatError = map[string]any{"code": "unsupported_attachment", "message": "That type of file can't be attached.", "retryable": false}
	})
	d.submit("Keep me")
	d.finish()
	d.expect("a.txt ×", "That type of file can't be attached.")
	if got := d.m.composer.Value(); got != "Keep me" {
		t.Errorf("composer holds %q", got)
	}
}

func TestSearchDuringAReplyWaits(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	holdReply(t, d, g, "Partial")
	d.submit("/search How long?")
	d.settle(10 * time.Millisecond)
	if n := g.requestCount(); n != 1 {
		t.Errorf("%d requests", n)
	}
	if got := d.m.composer.Value(); got != "/search How long?" {
		t.Errorf("composer holds %q", got)
	}
	d.expect("Wait for this reply to finish")
	release(d, g)
}

func TestSettingsByKeyboard(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.key("f2")
	d.keys("right", "right", "enter")
	if d.m.themeSetting != "light" {
		t.Errorf("theme is %s", d.m.themeSetting)
	}
	d.keys("tab", "enter") // Mouse On, then
	d.keys("tab", "enter") // Mouse Off.
	if d.m.mouse {
		t.Error("the mouse stayed on")
	}
	d.do2("set-mouse", "on")
	d.closeAll()
}

func TestPrivacyDeleteAllByKeyboard(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.submit("/privacy")
	d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
	d.keys("tab", "enter")
	if p := d.m.topPanel(); p == nil || p.kind != panelConfirm {
		t.Fatalf("no confirmation: %+v", p)
	}
	d.closeAll()
}

// Detail is for Advanced, and status is carried by a symbol as well as color.
func TestPrivacyDetailOnlyInAdvanced(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 50})
	d.submit("/privacy")
	d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
	d.expect("Where your messages go", "On this computer", "● ")
	d.refute("retention_days", "provider ollama")
	d.closeAll()
	enterAdvanced(t, d)
	d.submit("/privacy")
	d.until("privacy", func() bool { return !d.m.topPanel().loading })
	d.expect("retention_days=90", "provider ollama")
}

func TestWin32ModeIsRequestedOnlyWhenAsked(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.script = replyScript(doneEvent("stop")) })
	output := runProgram(t, g, colorprofile.TrueColor, rig{win32: true}, func(wait func(string), keys interface{ Write([]byte) (int, error) }) {
		wait("What can I help you with?")
		keys.Write([]byte("\x1b[?9001;1$y"))
		wait("Shift+Enter new line")
		keys.Write([]byte{3, 3})
	})
	if !strings.Contains(output, ansi.SetModeWin32Input) || !strings.Contains(output, ansi.RequestModeWin32Input) {
		t.Errorf("win32-input mode was not requested:\n%q", output[:min(len(output), 200)])
	}
	output = runProgram(t, g, colorprofile.TrueColor, rig{}, func(wait func(string), keys interface{ Write([]byte) (int, error) }) {
		wait("What can I help you with?")
		keys.Write([]byte{3, 3})
	})
	if strings.Contains(output, ansi.SetModeWin32Input) {
		t.Error("win32-input mode was requested where it should not be")
	}
}

func TestFolderRefusalNamesTheSetting(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{platform: "darwin"})
	d.key("ctrl+o")
	d.send(dirMsg{dir: "/Users/a/Documents", err: os.ErrPermission})
	d.expect("System", "Settings, Privacy & Security, Files and Folders.")
	d.expect("/Volumes")
	d.closeAll()

	w := g.open(t, rig{platform: "windows"})
	w.key("ctrl+o")
	w.send(dirMsg{dir: `C:\x`, err: os.ErrPermission})
	w.expect("That folder can't be opened.")
	w.refute("System Settings", "/Volumes")
	w.closeAll()
}

func TestAdvancedMouseTargets(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.convs["c3"] = storedChat("c3", "stop") })
	d := newDriver(t, g, 130, 40)
	enterAdvanced(t, d)

	d.clickText("On this computer")
	if p := d.m.topPanel(); p == nil || p.kind != panelPrivacy {
		t.Fatalf("the indicator did not open Privacy:\n%s", d.text())
	}
	d.closeAll()

	d.clickText("Vendor letter")
	d.until("open", func() bool { return d.m.currentID == "c3" })

	d.clickText("+ new")
	d.settle(5 * time.Millisecond)
	if d.m.currentID != "" {
		t.Error("+ new did not start a chat")
	}

	x, y, ok := d.find("+ > ")
	if !ok {
		t.Fatalf("no composer:\n%s", d.text())
	}
	d.click(x, y)
	if p := d.m.topPanel(); p == nil || p.kind != panelFiles {
		t.Fatalf("the + did not open the picker:\n%s", d.text())
	}
	d.closeAll()

	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "Partial"}), "HOLD", doneEvent("stop"))
	})
	d.key("Question")
	d.clickText("Send")
	d.until("text", func() bool { return d.m.active != nil && d.m.active.reply.Text != "" })
	d.clickText("■ Stop")
	d.finish()
	d.expect(views.Stopped)
}

func TestComposerControlKeys(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.keys("one")
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	d.key("two")
	if got := d.m.composer.Value(); got != "one\ntwo" {
		t.Errorf("Ctrl+Enter: %q", got)
	}
	d.clearComposer()

	clip := &fakeClipboard{content: "from the clipboard"}
	mac := g.open(t, rig{clip: clip, platform: "darwin"})
	mac.send(tea.KeyPressMsg{Code: 'v', Mod: tea.ModSuper})
	mac.until("paste", func() bool { return mac.m.composer.Value() != "" })
}

func TestPastedPathIsSentAsAMessage(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "/Users/alex/notes.txt", "Short answer.")
	if !strings.Contains(d.text(), "Short answer.") {
		t.Fatalf("the path was not sent:\n%s", d.text())
	}
}
