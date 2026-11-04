package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateFileName = "state.json"

// State is the set of choices persisted across runs. A nil or empty field means
// no choice was made, so the config file still applies.
type State struct {
	Theme string `json:"theme,omitempty"`
	Mouse *bool  `json:"mouse,omitempty"`
}

func (s State) apply(a *Appearance) {
	if validTheme(s.Theme) {
		a.Theme = s.Theme
	}
	if s.Mouse != nil {
		a.Mouse = *s.Mouse
	}
}

// loadState returns the saved state. A missing or damaged file gives an empty
// state, because losing a preference must never stop the terminal from starting.
func loadState(path string) State {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// Merge returns s with every choice made in change applied over it.
func (s State) Merge(change State) State {
	if change.Theme != "" {
		s.Theme = change.Theme
	}
	if change.Mouse != nil {
		s.Mouse = change.Mouse
	}
	return s
}

// SaveAppearance adds the choices in change to the state file beside the config
// file. A choice left empty keeps what is there, so one that was never made stays
// with the config file. It does nothing when no state folder is known.
func (c Config) SaveAppearance(change State) error {
	if c.StatePath == "" {
		return nil
	}
	if change.Theme != "" && !validTheme(change.Theme) {
		return fmt.Errorf("theme must be %s, %s or %s, not %q", ThemeAuto, ThemeDark, ThemeLight, change.Theme)
	}
	data, err := json.MarshalIndent(loadState(c.StatePath).Merge(change), "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	tmp, err := os.CreateTemp(dir, stateFileName+".*")
	if err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("saving settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.StatePath); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return nil
}
