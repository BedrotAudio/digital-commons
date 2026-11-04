// Package theme holds the color tokens from docs/ui-spec.md. It is the only place
// in the terminal that names a color.
package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme names, as used by appearance.theme.
const (
	NameAuto  = "auto"
	NameDark  = "dark"
	NameLight = "light"
)

// Tokens are the palette as hex strings, so that lipgloss, glamour and chroma can
// all use them.
type Tokens struct {
	Bg            string
	Surface       string
	SurfaceRaised string
	Border        string
	Text          string
	TextMuted     string
	Accent        string
	AccentText    string
	Info          string
	Warn          string
	Danger        string
}

var darkTokens = Tokens{
	Bg:            "#0F1720",
	Surface:       "#151F29",
	SurfaceRaised: "#1D2935",
	Border:        "#2E3C4A",
	Text:          "#E6EBF0",
	TextMuted:     "#9DABB8",
	Accent:        "#74B892",
	AccentText:    "#0F1720",
	Info:          "#8FB3D9",
	Warn:          "#D9B36C",
	Danger:        "#E58F8F",
}

var lightTokens = Tokens{
	Bg:            "#F7F8F6",
	Surface:       "#EEF1EE",
	SurfaceRaised: "#E3E8E4",
	Border:        "#C5CEC8",
	Text:          "#1B2730",
	TextMuted:     "#4F5E6A",
	Accent:        "#2B6A4C",
	AccentText:    "#FFFFFF",
	Info:          "#2A5C8A",
	Warn:          "#7D5410",
	Danger:        "#A23535",
}

// Colors are the tokens as color values.
type Colors struct {
	Bg, Surface, SurfaceRaised, Border     color.Color
	Text, TextMuted                        color.Color
	Accent, AccentText, Info, Warn, Danger color.Color
}

// Styles are the shared lipgloss styles. Backgrounds other than fills are left to
// cell-level painting, see render.Paint.
type Styles struct {
	Text    lipgloss.Style // primary text
	Muted   lipgloss.Style // secondary text
	Accent  lipgloss.Style // brand, focus
	Info    lipgloss.Style // links, citations
	Warn    lipgloss.Style // warnings, demo mode
	Danger  lipgloss.Style // errors, destructive actions
	Border  lipgloss.Style // separators, inactive borders
	Button  lipgloss.Style // text on an accent fill
	Raised  lipgloss.Style // user bubble, cards, selected rows
	Panel   lipgloss.Style // sidebar, panels
	Heading lipgloss.Style // section titles inside panels
}

// Theme is a resolved color theme.
type Theme struct {
	Name   string // NameDark or NameLight
	Dark   bool
	Tokens Tokens
	Colors Colors
	Styles Styles
}

// New returns the named theme. Anything other than NameLight gives dark, which is
// the fallback the spec requires.
func New(name string) Theme {
	if name == NameLight {
		return build(NameLight, false, lightTokens)
	}
	return build(NameDark, true, darkTokens)
}

// Resolve applies appearance.theme. For auto, terminalIsDark is what the terminal
// reported, or true when it never answered.
func Resolve(setting string, terminalIsDark bool) Theme {
	if setting == NameAuto {
		if terminalIsDark {
			return New(NameDark)
		}
		return New(NameLight)
	}
	return New(setting)
}

func build(name string, dark bool, t Tokens) Theme {
	c := Colors{
		Bg:            lipgloss.Color(t.Bg),
		Surface:       lipgloss.Color(t.Surface),
		SurfaceRaised: lipgloss.Color(t.SurfaceRaised),
		Border:        lipgloss.Color(t.Border),
		Text:          lipgloss.Color(t.Text),
		TextMuted:     lipgloss.Color(t.TextMuted),
		Accent:        lipgloss.Color(t.Accent),
		AccentText:    lipgloss.Color(t.AccentText),
		Info:          lipgloss.Color(t.Info),
		Warn:          lipgloss.Color(t.Warn),
		Danger:        lipgloss.Color(t.Danger),
	}
	base := lipgloss.NewStyle()
	return Theme{
		Name:   name,
		Dark:   dark,
		Tokens: t,
		Colors: c,
		Styles: Styles{
			Text:    base.Foreground(c.Text),
			Muted:   base.Foreground(c.TextMuted),
			Accent:  base.Foreground(c.Accent),
			Info:    base.Foreground(c.Info),
			Warn:    base.Foreground(c.Warn),
			Danger:  base.Foreground(c.Danger),
			Border:  base.Foreground(c.Border),
			Button:  base.Foreground(c.AccentText).Background(c.Accent).Bold(true),
			Raised:  base.Foreground(c.Text).Background(c.SurfaceRaised),
			Panel:   base.Foreground(c.Text).Background(c.Surface),
			Heading: base.Foreground(c.TextMuted).Bold(true),
		},
	}
}
