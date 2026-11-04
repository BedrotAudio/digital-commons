package theme

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func channel(hex string, offset int) float64 {
	v, err := strconv.ParseUint(hex[offset:offset+2], 16, 8)
	if err != nil {
		panic(err)
	}
	c := float64(v) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminance(hex string) float64 {
	return 0.2126*channel(hex, 1) + 0.7152*channel(hex, 3) + 0.0722*channel(hex, 5)
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrastFormula(t *testing.T) {
	if got := contrast("#000000", "#FFFFFF"); math.Abs(got-21) > 0.001 {
		t.Fatalf("black on white = %v", got)
	}
	if got := contrast("#777777", "#777777"); got != 1 {
		t.Fatalf("same color = %v", got)
	}
}

func TestContrastPairs(t *testing.T) {
	const aa = 4.5
	for _, name := range []string{NameDark, NameLight} {
		tk := New(name).Tokens
		foregrounds := map[string]string{
			"text": tk.Text, "textMuted": tk.TextMuted, "accent": tk.Accent,
			"info": tk.Info, "warn": tk.Warn, "danger": tk.Danger,
		}
		backgrounds := map[string]string{
			"bg": tk.Bg, "surface": tk.Surface, "surfaceRaised": tk.SurfaceRaised,
		}
		for fn, fg := range foregrounds {
			for bn, bg := range backgrounds {
				if got := contrast(fg, bg); got < aa {
					t.Errorf("%s: %s on %s = %.2f, below %.1f", name, fn, bn, got, aa)
				}
			}
		}
		if got := contrast(tk.AccentText, tk.Accent); got < aa {
			t.Errorf("%s: accentText on accent = %.2f, below %.1f", name, got, aa)
		}
	}
}

// Every value in docs/ui-spec.md, so a drift between spec and code fails here.
func TestTokensMatchSpec(t *testing.T) {
	want := map[string][2]string{
		"Bg":            {"#0F1720", "#F7F8F6"},
		"Surface":       {"#151F29", "#EEF1EE"},
		"SurfaceRaised": {"#1D2935", "#E3E8E4"},
		"Border":        {"#2E3C4A", "#C5CEC8"},
		"Text":          {"#E6EBF0", "#1B2730"},
		"TextMuted":     {"#9DABB8", "#4F5E6A"},
		"Accent":        {"#74B892", "#2B6A4C"},
		"AccentText":    {"#0F1720", "#FFFFFF"},
		"Info":          {"#8FB3D9", "#2A5C8A"},
		"Warn":          {"#D9B36C", "#7D5410"},
		"Danger":        {"#E58F8F", "#A23535"},
	}
	get := func(tk Tokens, k string) string {
		return map[string]string{
			"Bg": tk.Bg, "Surface": tk.Surface, "SurfaceRaised": tk.SurfaceRaised, "Border": tk.Border,
			"Text": tk.Text, "TextMuted": tk.TextMuted, "Accent": tk.Accent, "AccentText": tk.AccentText,
			"Info": tk.Info, "Warn": tk.Warn, "Danger": tk.Danger,
		}[k]
	}
	dark, light := New(NameDark).Tokens, New(NameLight).Tokens
	for k, v := range want {
		if got := get(dark, k); got != v[0] {
			t.Errorf("dark %s = %s, want %s", k, got, v[0])
		}
		if got := get(light, k); got != v[1] {
			t.Errorf("light %s = %s, want %s", k, got, v[1])
		}
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		setting  string
		termDark bool
		want     string
	}{
		{NameAuto, true, NameDark},
		{NameAuto, false, NameLight},
		{NameDark, false, NameDark},
		{NameLight, true, NameLight},
		{"unknown", false, NameDark},
	}
	for _, c := range cases {
		if got := Resolve(c.setting, c.termDark); got.Name != c.want {
			t.Errorf("Resolve(%q, %v) = %s, want %s", c.setting, c.termDark, got.Name, c.want)
		}
	}
}

func TestStylesCarryColors(t *testing.T) {
	th := New(NameDark)
	out := th.Styles.Button.Render("Go")
	if !strings.Contains(out, "Go") {
		t.Fatalf("rendered %q", out)
	}
	if th.Colors.Bg == nil || th.Colors.Danger == nil {
		t.Fatal("colors not set")
	}
	if New(NameLight).Dark {
		t.Fatal("light reports dark")
	}
}
