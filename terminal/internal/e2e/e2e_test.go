package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	homeGreeting = "What can I help you with?"
	question     = "What is the remote work eligibility policy?"
)

// The Vocabulary section of docs/ui-spec.md, written here independently of the app.
var vocabulary = regexp.MustCompile(`(?i)(?:^|[^a-z])(models?|llms?|inference|gateways?|backends?|servers?|ollama|tokens?|contexts?|prompts?|tiers?|providers?|gpus?|apis?)(?:[^a-z]|$)`)

func (s *session) assertPlain(state string) {
	s.t.Helper()
	if m := vocabulary.FindStringSubmatch(s.text()); m != nil {
		s.t.Errorf("Basic screen %s shows %q:\n%s", state, m[1], s.text())
	}
}

// fixture starts a gateway and the program against it.
func fixture(t *testing.T, opts launchOptions) (*session, *gateway) {
	t.Helper()
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.start()
	opts.dataDir = dir
	opts.gatewayURL = gw.url()
	s := launch(t, opts)
	s.waitText(homeGreeting, "Demo mode")
	return s, gw
}

// waitPanes waits on the row of Advanced pane titles. The footer names some of the
// same words, and which ones fit depends on the platform's hints. The closing corner
// shows the row was redrawn at the new width, not just cut short by the resize.
func (s *session) waitPanes(ok func(titles string) bool) {
	s.t.Helper()
	s.waitFor("the pane titles", func(screen string) bool {
		lines := strings.Split(screen, "\n")
		return len(lines) > 1 && strings.HasSuffix(strings.TrimRight(lines[1], " "), "┐") && ok(lines[1])
	})
}

// canvas is the color behind most of the screen.
func (s *session) canvas() string {
	rows, _, _ := s.grab()
	return commonBackground(rows)
}

func (s *session) quit() {
	s.t.Helper()
	s.run("/exit")
	if code := s.waitExit(); code != 0 {
		s.t.Errorf("exit code %d", code)
	}
	s.assertRestored()
}

// sendAndFinish sends a message and waits for the finished reply.
func (s *session) sendAndFinish(text string) {
	s.t.Helper()
	s.run(text)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop")
}

func TestBasicConversation(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 120, height: 36})

	s.waitText("+ New chat", "Message Digital Commons", "Digital Commons can make mistakes", "Privacy", "Settings", "Help")
	s.assertPlain("home")
	s.shot("home, first launch")

	// The first status can arrive just before the composer has focus.
	s.typeText("Hello")
	s.waitText("Hello")
	s.shot("home, typing")
	textX, textY, _ := s.locate("Hello")
	s.waitFor("the cursor after the typed text", func(string) bool {
		x, y, visible := s.cursorAt()
		return visible && x == textX+len("Hello") && y == textY
	})
	s.send(strings.Repeat("\x7f", 5))
	s.waitText("Message Digital Commons")

	s.run(question)
	s.waitText(question[:20], "Stop")
	s.waitFor("the reply to begin", func(screen string) bool {
		return strings.Contains(screen, "demonstration")
	})
	s.assertPlain("streaming")
	s.shot("streaming")
	s.waitText("Try again", "Copy")
	s.waitGone("Stop")
	s.assertPlain("finished")
	s.shot("reply finished")

	// Enter does not send during a reply, and Esc stops it.
	s.run("Tell me more about the policy")
	s.waitText("Stop")
	s.typeText("draft kept while waiting")
	s.send(keyEnter)
	s.waitText("draft kept while waiting")
	s.shot("typing during a reply")
	s.send(keyEsc)
	s.waitText("Stopped")
	s.waitGone("Stop ")
	s.shot("stopped")
	s.send(strings.Repeat("\x7f", len("draft kept while waiting")))
	s.waitGone("draft kept while waiting")

	// Try again regenerates the last reply.
	s.send(keyCtrlR)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop")
	s.shot("after try again")

	// Sources come from the citations event.
	s.run("/search " + question)
	s.waitText("Stop")
	s.waitText("Sources", "[1] Remote Work Policy, Eligibility", "Try again")
	s.waitGone("Stop")
	s.assertPlain("sources")
	s.shot("reply with sources")

	s.send(keyCtrlN)
	s.waitText(homeGreeting, "Today")
	s.assertPlain("history")
	s.shot("new chat with history")

	// Reopen the chat from the list by keyboard.
	s.send(keyTab)
	time.Sleep(150 * time.Millisecond)
	s.shot("chat list focused")
	s.send(keyDown)
	time.Sleep(150 * time.Millisecond)
	s.shot("chat list row highlighted")
	s.send(keyEnter)
	s.waitGone(homeGreeting)
	s.waitText("Sources", "Copy", "Try again")
	s.assertPlain("reopened")
	s.shot("reopened from history")

	// Slash palette.
	s.send(keyTab)
	s.typeText("/")
	s.waitText("/help", "/new", "/privacy")
	s.assertPlain("palette")
	s.shot("slash palette")
	s.typeText("pri")
	s.waitGone("/new")
	s.shot("slash palette filtered")
	s.send(keyEsc)
	s.send(strings.Repeat("\x7f", 4))

	// Panels in Basic.
	s.send(keyF1)
	s.waitText("× Close")
	s.assertPlain("help")
	s.shot("help panel")
	s.send(keyEsc)
	s.send(keyF2)
	s.waitText("Delete all")
	s.assertPlain("settings")
	s.shot("settings panel")
	s.send(keyEsc)
	s.run("/privacy")
	s.waitText("× Close")
	s.assertPlain("privacy")
	s.shot("privacy panel")
	s.send(keyEsc)
	s.run("/status")
	s.waitText("× Close")
	s.assertPlain("status")
	s.shot("status panel")
	s.send(keyEsc)

	// An Advanced command explains itself.
	s.run("/model")
	s.waitText("Advanced")
	s.assertPlain("advanced command refused")
	s.shot("advanced command in basic")

	// Delete, with Cancel focused.
	s.run("/delete")
	s.waitText("Cancel")
	s.assertPlain("delete confirm")
	s.shot("delete confirmation")
	s.send(keyEnter)
	s.waitGone("Cancel")
	s.run("/delete")
	s.waitText("Cancel")
	s.send(keyEsc)
	s.waitGone("Cancel")

	s.quit()
}

func TestAdvancedWorkstation(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 140, height: 42})

	s.sendAndFinish(question)

	s.run("/mode developer")
	s.waitText("ADVANCED", "MODEL", "USAGE")
	s.shot("advanced, after switching")

	s.run("/model")
	s.waitText("standard", "fast", "advanced")
	s.shot("model picker")
	s.send(keyEsc)

	s.run("/model fast")
	s.waitFor("the tier in the header", func(screen string) bool {
		return strings.Contains(strings.Split(screen, "\n")[0], "fast")
	})

	s.run("/code")
	s.waitText("CODE")
	s.shot("code assistant on")

	s.run("Write a function that counts working days")
	s.waitText("Stop")
	s.shot("advanced streaming")
	s.waitText("[1]", "python")
	s.waitText("Try again")
	s.waitGone("Stop")
	s.shot("advanced code block")

	s.send(keyF3)
	time.Sleep(200 * time.Millisecond)
	s.shot("inspector toggled")
	s.send(keyF3)

	s.run("/privacy")
	s.waitText("× Close")
	s.shot("privacy panel, advanced")
	s.send(keyEsc)

	s.run("/status")
	s.waitText("× Close")
	s.shot("status panel, advanced")
	s.send(keyEsc)

	s.send(keyF1)
	s.waitText("× Close")
	s.shot("help panel, advanced")
	s.send(keyEsc)

	s.typeText("/")
	s.waitText("/model", "/code")
	s.shot("slash palette, advanced")
	s.send(keyEsc)
	s.send("\x7f")

	s.run("/mode basic")
	s.waitGone("ADVANCED")
	s.waitText("Message Digital Commons")
	s.assertPlain("back in basic")
	s.shot("back in basic")

	// Advanced choices come back.
	s.run("/mode developer")
	s.waitText("ADVANCED", "CODE")
	s.waitFor("the tier in the header", func(screen string) bool {
		return strings.Contains(strings.Split(screen, "\n")[0], "fast")
	})
	s.shot("advanced choices returned")

	s.resize(100, 30)
	s.waitPanes(func(titles string) bool { return !strings.Contains(titles, "inspector") })
	s.shot("advanced at 100x30")
	s.resize(70, 24)
	s.waitPanes(func(titles string) bool { return !strings.Contains(titles, "chats") })
	s.shot("advanced at 70x24")
	s.send(keyF3)
	s.waitText("Inspector")
	s.shot("inspector overlay")
	s.send(keyEsc)
	s.waitGone("Inspector")
	s.resize(140, 42)
	s.waitPanes(func(titles string) bool { return strings.Contains(titles, "inspector") })

	s.run("/mode basic")
	s.waitGone("ADVANCED")
	s.quit()
}

func TestResize(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 120, height: 36})
	s.sendAndFinish(question)

	s.resize(100, 30)
	time.Sleep(400 * time.Millisecond)
	s.waitText("Message Digital Commons")
	s.shot("conversation at 100x30")

	s.resize(80, 24)
	s.waitGone("+ New chat")
	s.waitText("Message Digital Commons")
	s.shot("conversation at 80x24")

	s.send(keyCtrlB)
	s.waitText("+ New chat")
	s.shot("sidebar overlay at 80x24")
	s.send(keyEsc)
	s.waitGone("+ New chat")

	s.resize(50, 16)
	time.Sleep(400 * time.Millisecond)
	s.waitText("Message Digital Commons")
	s.shot("conversation at 50x16")

	s.resize(30, 8)
	s.waitText("Please enlarge")
	s.shot("too small")

	s.resize(120, 36)
	s.waitGone("Please enlarge")
	time.Sleep(300 * time.Millisecond)
	s.shot("restored to 120x36")

	s.send(keyCtrlN)
	s.waitText(homeGreeting)
	for _, size := range [][2]int{{200, 60}, {100, 24}, {90, 16}, {80, 24}, {60, 20}, {60, 12}, {44, 16}, {40, 12}} {
		s.resize(size[0], size[1])
		time.Sleep(400 * time.Millisecond)
		s.waitText("Message Digital Commons")
		s.shot("home")
	}
	s.resize(120, 36)
	s.waitText(homeGreeting)
	s.quit()
}

func TestLightTheme(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 120, height: 36, theme: "light"})
	s.shot("light home")
	s.run("/search " + question)
	s.waitText("Sources")
	s.waitText("Try again")
	s.waitGone("Stop")
	s.shot("light reply with sources")
	s.send(keyF2)
	s.waitText("Delete all")
	s.shot("light settings")
	s.send(keyEsc)
	s.run("/code")
	s.waitText("Advanced")
	s.run("/mode developer")
	s.waitText("ADVANCED")
	s.run("/code")
	s.waitText("CODE")
	s.sendAndFinish("Write a function that counts working days")
	s.shot("light advanced code block")
	s.run("/mode basic")
	s.waitGone("ADVANCED")
	s.quit()
}

func TestAttach(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "policy.txt"), []byte("Remote work needs approval.\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "budget.csv"), []byte("item,cost\nchairs,10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "report.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	gw := newGateway(t, dir)
	gw.start()
	s := launch(t, launchOptions{width: 120, height: 36, dataDir: dir, gatewayURL: gw.url(), home: home})
	s.waitText(homeGreeting, "Demo mode")

	s.send(keyCtrlO)
	s.waitText("policy.txt", "budget.csv", "Save PDF or Word files as text first.")
	s.assertPlain("file picker")
	s.shot("file picker")
	s.send(keyEsc)
	s.waitGone("Save PDF or Word")

	s.run("/attach " + filepath.Join(home, "report.pdf"))
	s.waitText("Save PDF or Word files as text first.")
	s.shot("refused attachment")

	// A card that needs two files opens the picker for both.
	s.clickText("Compare two policies")
	s.waitText("Choose 2 files")
	s.shot("picker for two files")
	s.send(keyEnter)
	s.waitText("1 chosen")
	s.send(keyDown, keyEnter)
	s.waitGone("Choose 2 files")
	s.waitText("budget.csv ×", "policy.txt ×")
	s.shot("two chips")
	s.send(strings.Repeat(keyBackspace, 120))
	s.waitGone("policy.txt ×")
	s.waitGone("budget.csv ×")

	// The approved-documents mode is a chip too, removed by clicking it.
	s.run("/search")
	s.waitText("Approved documents ×")
	s.shot("approved documents chip")
	s.clickText("×")
	s.waitGone("Approved documents ×")

	s.run("/attach " + filepath.Join(home, "policy.txt"))
	s.waitText("policy.txt")
	s.shot("attachment chip")
	s.typeText("Summarize this")
	s.waitText("Summarize this")
	s.send(keyEnter)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop")
	s.assertPlain("attached reply")
	s.shot("reply with attachment")
	s.quit()
}

func TestMouse(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 120, height: 36})

	s.clickText("Explain a regulation")
	s.waitFor("the card to act", func(screen string) bool {
		return !strings.Contains(screen, homeGreeting) || strings.Contains(screen, "Explain")
	})
	s.shot("card clicked")
	if !strings.Contains(s.text(), "Stop") {
		s.send(keyEnter)
	}
	s.waitText("Try again")
	s.waitGone("Stop")

	s.clickText("Try again")
	s.waitText("Stop")
	s.clickText("Stop")
	s.waitText("Stopped")
	s.shot("stopped by mouse")

	s.clickText("+ New chat")
	s.waitText(homeGreeting)

	// Reopen by clicking the row, and delete with its ×.
	s.clickText("Explain this regulation")
	s.waitGone(homeGreeting)
	s.waitText("Stopped")
	s.clickAfter("regulation…", "×")
	s.waitText("Delete this chat?", "Cancel")
	s.clickText("Cancel")
	s.waitGone("Delete this chat?")
	s.waitText("Stopped")
	s.clickAfter("regulation…", "×")
	s.waitText("Delete this chat?")
	s.clickAfter("Cancel", "Delete")
	s.waitText(homeGreeting, "No chats yet")

	s.clickText("Settings")
	s.waitText("Delete all")
	s.send(keyEsc)
	s.waitGone("Delete all")
	s.clickText("Help")
	s.waitText("× Close")
	s.send(keyEsc)

	s.clickText("+ Message")
	s.waitText("Attach a file", "Save PDF or Word files as text first.")
	s.shot("picker from plus")
	s.send(keyEsc)
	s.waitGone("Attach a file")
	s.quit()
}

func TestWheel(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 80, height: 24})
	s.sendAndFinish(question)

	s.wheel(40, 10, false)
	s.wheel(40, 10, false)
	s.waitText("Latest")
	s.shot("scrolled up, with the way back")
	s.clickText("Latest")
	s.waitGone("Latest")
	s.waitText("Copy")
	s.quit()
}

func TestPaste(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30})
	s.paste("first pasted line\r\nsecond pasted line\r\nthird pasted line")
	s.waitText("first pasted line", "second pasted line", "third pasted line")
	s.shot("multi-line paste")
	if strings.Contains(s.text(), "Stop") {
		t.Error("the paste sent the message")
	}
	s.send(keyEnter)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop")
	s.quit()
}

func TestNewLineKeys(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30})

	s.typeText("first")
	s.send(keyCtrlJ)
	s.typeText("second")
	s.send(keyShiftEnter)
	s.typeText("third")
	s.waitText("first", "second", "third")
	s.shot("multi-line draft")
	if strings.Contains(s.text(), "Stop") {
		t.Error("a new line key sent the message")
	}
	s.send(strings.Repeat(keyBackspace, len("first second third")))
	s.waitText("Message Digital Commons")
	s.quit()
}

func TestCtrlCTwice(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30})
	s.send(keyCtrlC)
	s.waitText("again")
	s.shot("first ctrl+c")
	s.send(keyCtrlC)
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	s.assertRestored()
}

func TestCtrlCStopsThenQuits(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30})
	s.run(question)
	s.waitText("Stop")
	s.send(keyCtrlC)
	s.waitText("Stopped", "again")
	s.send(keyCtrlC)
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	s.assertRestored()
}

// A reply on a slow computer: a calm wait with honest elapsed time.
func TestSlowReply(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.slowDown("0.07")
	gw.start()
	s := launch(t, launchOptions{width: 120, height: 36, dataDir: dir, gatewayURL: gw.url()})
	s.waitText(homeGreeting, "Demo mode")

	s.run(question)
	s.waitText("Thinking", "Stop")
	s.assertPlain("waiting")
	s.shot("waiting")
	s.waitWithin(patience, "elapsed time", func(screen string) bool {
		return strings.Contains(screen, "Thinking… · ")
	})
	s.assertPlain("waiting with elapsed time")
	s.shot("waiting with elapsed time")
	s.send(keyEsc)
	s.waitText("Stopped")
	s.quit()
}

func TestReasoning(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.slowDown("12")
	gw.start()
	s := launch(t, launchOptions{width: 130, height: 40, dataDir: dir, gatewayURL: gw.url()})
	s.waitText(homeGreeting, "Demo mode")

	s.run("/mode developer")
	s.waitText("ADVANCED")
	s.run("/model advanced")
	s.waitFor("the tier in the header", func(screen string) bool {
		return strings.Contains(strings.Split(screen, "\n")[0], "advanced")
	})
	s.run(question)
	s.waitText("simulated reasoning")
	s.shot("reasoning while streaming")
	s.run("/mode basic")
	s.waitGone("ADVANCED")
	s.assertPlain("basic during reasoning")
	if strings.Contains(s.text(), "simulated reasoning") {
		t.Error("Basic mode shows reasoning")
	}
	s.shot("basic hides reasoning")
	s.send(keyEsc)
	s.waitText("Stopped")
	s.quit()
}

func TestNoColor(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30, noColor: true})
	s.run(question)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop")
	rows, _, _ := s.grab()
	for y, row := range rows {
		for x := range row {
			if st := row[x].Style; st.Fg != nil || st.Bg != nil {
				t.Fatalf("cell %d,%d has a color", x, y)
			}
		}
	}
	s.quit()
}

func TestAutoTheme(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30, theme: "auto"})
	s.shot("auto theme")
	s.quit()
}

func TestConnectionRecovery(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	// The program starts first, as it does when the service is not running yet.
	s := launch(t, launchOptions{width: 120, height: 36, dataDir: dir, gatewayURL: gw.url()})
	s.waitText("Not connected")
	s.waitText("isn't running")
	s.assertPlain("not running")
	s.shot("service not running")

	gw.start()
	s.waitWithin(2*patience, "recovery", func(screen string) bool {
		return strings.Contains(screen, "Demo mode") && !strings.Contains(screen, "isn't running")
	})
	s.waitText(homeGreeting)
	s.shot("recovered")

	// Losing the service mid-reply keeps the partial text.
	s.run(question)
	s.waitText(question[:20], "Stop")
	s.waitText("demonstration")
	gw.stop()
	s.waitText("The connection was lost")
	s.assertPlain("connection lost")
	s.shot("connection lost")
	s.waitText("Not connected")
	s.shot("disconnected")

	// A send while it is down fails inline and keeps the words.
	s.send(keyEnter)
	s.run("Are you still there?")
	s.waitText("Are you still there?", "Try again")
	s.shot("send while disconnected")

	gw.start()
	s.waitWithin(2*patience, "recovery", func(screen string) bool {
		return strings.Contains(screen, "Demo mode")
	})
	s.shot("reconnected")

	s.clickText("Try again")
	s.waitText("Stop")
	s.waitText("Are you still there?", "Try again")
	s.waitGone("Stop")
	s.shot("sent after reconnecting")
	s.quit()
}

// The theme choice is written to state.json and read back on the next launch.
func TestThemeChoicePersists(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.start()
	config := filepath.Join(dir, "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := launchOptions{width: 100, height: 30, dataDir: dir, gatewayURL: gw.url(), configDir: config}

	s := launch(t, opts)
	s.waitText(homeGreeting, "Demo mode")
	if bg := s.canvas(); bg != "#0f1720" {
		t.Errorf("dark canvas is %s", bg)
	}
	s.run("/theme light")
	s.waitFor("the light canvas", func(string) bool { return s.canvas() == "#f7f8f6" })
	s.quit()

	s = launch(t, opts)
	s.waitText(homeGreeting, "Demo mode")
	if bg := s.canvas(); bg != "#f7f8f6" {
		t.Errorf("after a restart the canvas is %s", bg)
	}
	s.quit()
}
