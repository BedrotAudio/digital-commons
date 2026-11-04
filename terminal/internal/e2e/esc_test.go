package e2e

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// latePty hands each write to the reader no sooner than its ready time, the way a
// program that is slow to read its pty sees it.
type latePty struct{ ch chan lateChunk }

type lateChunk struct {
	data  []byte
	ready time.Time
}

func (p *latePty) Read(b []byte) (int, error) {
	c, ok := <-p.ch
	if !ok {
		return 0, io.EOF
	}
	time.Sleep(time.Until(c.ready))
	return copy(b, c.data), nil
}

// decode writes first, waits gap, writes second, and returns the keys Bubble Tea's
// own reader makes of them when the program reads the first write late.
func decode(first, second string, gap, late time.Duration) []string {
	p := &latePty{ch: make(chan lateChunk, 2)}
	events := make(chan uv.Event, 16)
	done := make(chan struct{})
	go func() {
		uv.NewTerminalReader(p, "xterm-256color").StreamEvents(context.Background(), events)
		close(done)
	}()
	var keys []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for e := range events {
			if k, ok := e.(uv.KeyPressEvent); ok {
				keys = append(keys, k.String())
			} else {
				keys = append(keys, fmt.Sprintf("%T", e))
			}
		}
	}()
	start := time.Now()
	p.ch <- lateChunk{[]byte(first), start.Add(late)}
	time.Sleep(gap)
	p.ch <- lateChunk{[]byte(second), time.Now()}
	time.Sleep(250 * time.Millisecond)
	close(p.ch)
	<-done
	close(events)
	wg.Wait()
	return keys
}

// On Unix a bare Esc is told from the start of a longer key only by 50 ms of
// silence, counted from when the program reads it. The rig's pause after Esc must
// leave room for a program that reads it late, or Esc and the next key arrive as
// one Alt key and the test then waits for a panel that never closes. A Unix Esc is
// sent here on every platform, because Windows sends win32-input records.
func TestEscIsNotJoinedToTheNextKey(t *testing.T) {
	const late = 60 * time.Millisecond
	for next, name := range map[string]string{"\x1bOQ": "f2", "/": "/", "\x7f": "backspace"} {
		if got := decode("\x1b", next, keyGap(keyEsc), late); len(got) != 2 || got[0] != "esc" || got[1] != name {
			t.Errorf("Esc then %s arrived as %q", name, got)
		}
	}
}
