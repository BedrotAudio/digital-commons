package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveModel drives dc.py dev against the real Ollama and the configured model,
// with nothing faked. It needs DC_E2E_LIVE as well as DC_E2E, a running Ollama with
// cloud access off, and patience, because a cold load can take minutes on a CPU.
func TestLiveModel(t *testing.T) {
	requireE2E(t)
	if os.Getenv("DC_E2E_LIVE") == "" {
		t.Skip("DC_E2E_LIVE is not set")
	}
	python, err := exec.LookPath(pythonName)
	if err != nil {
		t.Skipf("%s is not on PATH", pythonName)
	}
	dir := workspace(t)
	s := launch(t, launchOptions{
		width: 120, height: 36, dataDir: dir,
		command: []string{python, filepath.Join(repoRoot(t), "dc.py"), "dev"},
		env:     []string{"DC_CONFIG=" + sessionConfig(t, dir, freePort(t), "")},
	})
	const reply = 8 * time.Minute
	finished := func(screen string) bool { return strings.Contains(screen, "Try again") }

	s.waitWithin(2*patience, "the home screen", func(screen string) bool {
		return strings.Contains(screen, homeGreeting) && strings.Contains(screen, "On this computer")
	})
	s.shot("live: home")
	s.assertPlain("live home")

	s.run("In one short sentence, what does a public library do?")
	s.waitWithin(reply, "a reply", func(screen string) bool {
		return strings.Contains(screen, "Stop") && !strings.Contains(screen, homeGreeting)
	})
	s.shot("live: waiting for the first reply")
	s.waitWithin(reply, "the finished reply", finished)
	s.shot("live: Basic reply")

	s.run("/search How many days a week can staff work remotely?")
	s.waitWithin(reply, "a reply with sources", func(screen string) bool {
		return finished(screen) && strings.Contains(screen, "Sources")
	})
	s.shot("live: reply with sources")

	s.run("/mode developer")
	s.waitText("ADVANCED")
	s.run("/new")
	s.run("/code")
	s.run("Write a Python function that reverses a string. Keep it short.")
	s.waitWithin(reply, "a code block", func(screen string) bool {
		return finished(screen) && strings.Contains(screen, "[1]")
	})
	s.shot("live: Advanced code reply")

	s.run("Write a very long essay about the history of public libraries.")
	s.waitWithin(reply, "text to start arriving", func(screen string) bool {
		return strings.Contains(screen, "librar") && strings.Contains(screen, "Stop")
	})
	time.Sleep(4 * time.Second)
	s.send(keyEsc)
	s.waitWithin(30*time.Second, "the stop", func(screen string) bool { return strings.Contains(screen, "Stopped") })
	s.shot("live: stopped")
	s.quit()
}
