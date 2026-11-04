package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"digital-commons/terminal/internal/config"
)

// go test gives a program no console, which is the case this guards.
func TestRefusesToRunWithoutAConsole(t *testing.T) {
	err := launch(config.Config{})
	if err == nil || !strings.Contains(err.Error(), "interactive console") {
		t.Fatalf("launch returned %v", err)
	}
}

func TestFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
	}{
		{"version", []string{"--version"}, "", "digital-commons " + version},
		{"good gateway", []string{"--gateway", "http://127.0.0.1:9000"}, "", ""},
		{"refused gateway", []string{"--gateway", "http://gateway.city.gov"}, "https", ""},
		{"missing config", []string{"--config", "does-not-exist.yaml"}, "does not exist", ""},
		{"unknown flag", []string{"--nope"}, "unknown flag", ""},
		{"stray argument", []string{"chat"}, "unknown command", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			empty := filepath.Join(t.TempDir(), "terminal.yaml")
			if err := os.WriteFile(empty, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DC_TERMINAL_CONFIG", empty)
			var out bytes.Buffer
			cmd := newRootCommand(func(config.Config) error { return nil })
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(c.args)
			err := cmd.Execute()
			if c.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}

func TestNoColor(t *testing.T) {
	for value, want := range map[string]bool{"": false, "1": true, "0": true, "false": true, " ": true} {
		got := noColor(func(name string) string {
			if name == "NO_COLOR" {
				return value
			}
			return ""
		})
		if got != want {
			t.Errorf("NO_COLOR=%q: %v", value, got)
		}
	}
}

func TestWin32InputOnlyWhereTheTerminalSpeaksIt(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"console window", nil, true},
		{"windows terminal", map[string]string{"WT_SESSION": "id"}, true},
		{"git bash in windows terminal", map[string]string{"WT_SESSION": "id", "TERM": "xterm-256color"}, true},
		{"vs code", map[string]string{"TERM_PROGRAM": "vscode"}, false},
		{"alacritty", map[string]string{"TERM": "alacritty"}, false},
		{"mintty", map[string]string{"TERM": "xterm"}, false},
		{"ssh", map[string]string{"SSH_CONNECTION": "10.0.0.2 1 10.0.0.1 22"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := win32InputUsable(func(k string) string { return c.env[k] })
			if got != c.want {
				t.Errorf("got %v", got)
			}
		})
	}
}
