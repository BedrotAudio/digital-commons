package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstalledCopy runs an installed Digital Commons the way its shortcut or app does,
// `dc.py start` with the installer's own Python, in demo mode so that no model is
// needed. DC_E2E_INSTALL_DIR names the installed folder: on a Mac, the app bundle's
// Contents/Resources/app.
func TestInstalledCopy(t *testing.T) {
	requireE2E(t)
	dir := os.Getenv("DC_E2E_INSTALL_DIR")
	if dir == "" {
		t.Skip("DC_E2E_INSTALL_DIR is not set")
	}
	python := filepath.Join(dir, "runtime", "bin", "python3")
	if runtime.GOOS == "windows" {
		python = filepath.Join(dir, "runtime", "python.exe")
	}
	data := workspace(t)
	port := freePort(t)
	s := launch(t, launchOptions{
		width: 110, height: 32, dataDir: data,
		command: []string{python, filepath.Join(dir, "dc.py"), "start"},
		env:     []string{"DC_PROVIDER=mock", "DC_CONFIG=" + sessionConfig(t, data, port, "")},
	})
	s.waitWithin(2*patience, "the home screen", func(screen string) bool {
		return strings.Contains(screen, homeGreeting) && strings.Contains(screen, "Demo mode")
	})
	s.assertPlain("installed home")
	s.shot("installed copy: home")
	s.run("Hello from the installed copy")
	s.waitText("demonstration", "Try again")
	s.shot("installed copy: reply")
	s.quit()

	if _, err := os.Stat(filepath.Join(data, "terminal.yaml")); err != nil {
		t.Errorf("the terminal's settings were not kept with the person's data: %v", err)
	}
	if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port)); err == nil {
		resp.Body.Close()
		t.Error("the gateway is still answering after the installed copy closed")
	}
}
