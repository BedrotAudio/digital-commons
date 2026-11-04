package theme

import (
	"fmt"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// legible256 is the floor after reduction to the 256 color palette. The palette has
// no exact match for most tokens, so WCAG AA, which the true colors meet, can fall a
// little short here. Large text's 3:1 is the line under which words get hard to read.
const legible256 = 3.0

func reduced(hex string) string {
	r, g, b, _ := colorprofile.ANSI256.Convert(lipgloss.Color(hex)).RGBA()
	return fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, b>>8)
}

// Terminal.app before macOS 26 has 256 colors, so every text color must stay legible
// on every surface after reduction.
func TestContrastAt256Colors(t *testing.T) {
	for _, name := range []string{NameDark, NameLight} {
		tk := New(name).Tokens
		for _, fg := range []struct{ label, hex string }{
			{"text", tk.Text}, {"muted", tk.TextMuted}, {"accent", tk.Accent}, {"info", tk.Info}, {"warn", tk.Warn}, {"danger", tk.Danger},
		} {
			for _, bg := range []struct{ label, hex string }{{"bg", tk.Bg}, {"surface", tk.Surface}, {"raised", tk.SurfaceRaised}} {
				got := contrast(reduced(fg.hex), reduced(bg.hex))
				t.Logf("%s: %s on %s = %.2f", name, fg.label, bg.label, got)
				if got < legible256 {
					t.Errorf("%s: %s on %s = %.2f at 256 colors", name, fg.label, bg.label, got)
				}
			}
		}
		if got := contrast(reduced(tk.AccentText), reduced(tk.Accent)); got < legible256 {
			t.Errorf("%s: accentText on accent = %.2f at 256 colors", name, got)
		}
	}
}
