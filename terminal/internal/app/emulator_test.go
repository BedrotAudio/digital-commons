package app

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// lockedEmulator stands in for a terminal: it takes the program's output and sends
// its replies back as input. On macOS and Linux the e2e harness gives the emulator
// this same raw stream, with no console host in between, so this test lets any
// computer check that the renderer and the emulator agree.
type lockedEmulator struct {
	mu sync.Mutex
	em *vt.Emulator
}

func (l *lockedEmulator) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.em.Write(p)
}

func (l *lockedEmulator) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Split(l.em.String(), "\n")
}

func (l *lockedEmulator) unpainted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for y := range l.em.Height() {
		for x := range l.em.Width() {
			if c := l.em.CellAt(x, y); c == nil || c.Style.Bg == nil {
				count++
			}
		}
	}
	return count
}

func trimmed(lines []string) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func TestRawOutputThroughTheEmulator(t *testing.T) {
	g := newFakeGateway(t)
	d := rig{}.build(t, g.URL)
	in, keys := io.Pipe()
	screen := &lockedEmulator{em: vt.NewEmulator(100, 30)}
	go io.Copy(keys, screen.em)
	rec := &recorder{Model: d.m}
	program := tea.NewProgram(rec, tea.WithInput(in), tea.WithOutput(screen), tea.WithWindowSize(100, 30),
		tea.WithoutSignals(), tea.WithColorProfile(colorprofile.TrueColor), tea.WithFilter(Filter),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}))
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	matches := func(state string) {
		t.Helper()
		var got, want string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			got = trimmed(screen.lines())
			want = trimmed(strings.Split(ansi.Strip(rec.text()), "\n"))
			if want != "" && got == want {
				if n := screen.unpainted(); n > 0 {
					t.Errorf("%s: %d cells are unpainted", state, n)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: the emulator shows\n%s\n\nthe program drew\n%s", state, got, want)
	}
	send := func(text string) {
		t.Helper()
		if _, err := keys.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
	}

	matches("home")
	for _, r := range "Hello there" {
		send(string(r))
	}
	matches("typed text")
	g.set(func() {
		g.script = replyScript(sse("delta", map[string]any{"text": "A reply\n\n- with a list\n- and a tab\there\n\n" + strings.Repeat("Long line of words. ", 12)}), doneEvent("stop"))
	})
	send("\r")
	matches("reply")
	send("\x1bOP")
	matches("help panel")
	send("\x1b")
	matches("after the panel")

	resize := func(state string, w, h int) {
		t.Helper()
		screen.mu.Lock()
		screen.em.Resize(w, h)
		screen.mu.Unlock()
		program.Send(tea.WindowSizeMsg{Width: w, Height: h})
		deadline := time.Now().Add(10 * time.Second)
		for strings.Count(rec.text(), "\n")+1 != h {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the program never drew %d lines", state, h)
			}
			time.Sleep(10 * time.Millisecond)
		}
		matches(state)
	}
	resize("narrower", 80, 24)
	resize("wider", 120, 36)

	send("\x03")
	send("\x03")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not quit")
	}
	screen.em.Close()
}
