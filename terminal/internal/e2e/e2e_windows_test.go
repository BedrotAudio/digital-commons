package e2e

import (
	"strings"
	"testing"
	"time"
)

// A terminal that ignores win32-input mode sends plain sequences. It keeps working
// arrow keys and the Ctrl+J hint.
func TestTerminalWithoutWin32Input(t *testing.T) {
	requireE2E(t)
	s, _ := fixture(t, launchOptions{width: 100, height: 30, plainKeys: true})
	s.waitText("Ctrl+J new line")
	s.shot("plain terminal")

	s.pty.Write([]byte("/"))
	s.waitText("/help", "/new")
	s.pty.Write([]byte("\x1b[B"))
	time.Sleep(150 * time.Millisecond)
	s.pty.Write([]byte("\x1b[B"))
	time.Sleep(150 * time.Millisecond)
	s.shot("plain arrows in the palette")
	if strings.Contains(s.text(), "_[B") {
		t.Fatalf("an arrow key reached the composer as text:\n%s", s.text())
	}
	s.pty.Write([]byte("\x1b"))
	s.waitGone("/help ")
	s.pty.Write([]byte("\x7f"))
	s.waitText("Message Digital Commons")
	s.pty.Write([]byte("/exit"))
	time.Sleep(150 * time.Millisecond)
	s.pty.Write([]byte("\r"))
	if code := s.waitExit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	s.assertRestored()
}
