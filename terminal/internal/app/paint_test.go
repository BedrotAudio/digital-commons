package app

import (
	"fmt"
	"image/color"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// tokenSet is every color the theme names. Nothing else may reach a frame.
func tokenSet(name string) map[[3]uint32]string {
	tk := theme.New(name).Tokens
	out := map[[3]uint32]string{}
	for label, hex := range map[string]string{
		"bg": tk.Bg, "surface": tk.Surface, "raised": tk.SurfaceRaised, "border": tk.Border, "text": tk.Text,
		"muted": tk.TextMuted, "accent": tk.Accent, "accentText": tk.AccentText, "info": tk.Info, "warn": tk.Warn, "danger": tk.Danger,
	} {
		out[rgb(lipgloss.Color(hex))] = label
	}
	return out
}

func rgb(c color.Color) [3]uint32 {
	r, g, b, _ := c.RGBA()
	return [3]uint32{r >> 8, g >> 8, b >> 8}
}

// assertPainted parses the frame back into cells and checks that each one has a
// background from the theme, and that any drawn text has a theme foreground.
func assertPainted(t *testing.T, content, themeName, label string, w, h int) {
	t.Helper()
	tokens := tokenSet(themeName)
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewLayer(content))
	bare, foreign := 0, map[string]bool{}
	for y := range h {
		for x := range w {
			cell := canvas.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			if cell.Style.Bg == nil {
				bare++
				continue
			}
			if _, ok := tokens[rgb(cell.Style.Bg)]; !ok {
				foreign[fmt.Sprintf("bg %v at %d,%d", rgb(cell.Style.Bg), x, y)] = true
			}
			if cell.Link.URL != "" {
				foreign["a hyperlink"] = true
			}
			if strings.TrimSpace(cell.Content) == "" {
				continue
			}
			if cell.Style.Fg == nil {
				foreign[fmt.Sprintf("text %q with no foreground at %d,%d", cell.Content, x, y)] = true
			} else if _, ok := tokens[rgb(cell.Style.Fg)]; !ok {
				foreign[fmt.Sprintf("fg %v on %q at %d,%d", rgb(cell.Style.Fg), cell.Content, x, y)] = true
			}
		}
	}
	if bare > 0 {
		t.Errorf("%s %s: %d unpainted cells", themeName, label, bare)
	}
	for problem := range foreign {
		t.Errorf("%s %s: %s", themeName, label, problem)
		break
	}
}

func TestEveryCellPainted(t *testing.T) {
	sizes := [][2]int{{60, 20}, {80, 24}, {120, 40}, {200, 60}}
	for _, name := range []string{"dark", "light"} {
		for _, advanced := range []bool{false, true} {
			t.Run(name+"/"+modeName(advanced), func(t *testing.T) {
				t.Parallel()
				g := newFakeGateway(t)
				d := g.open(t, rig{theme: name, profile: colorprofile.TrueColor})
				if advanced {
					enterAdvanced(t, d)
				}
				walk(d, g, func(state string) {
					t.Helper()
					for _, s := range sizes {
						d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
						d.rewrap()
						view := d.m.View()
						if view.BackgroundColor != nil || view.ForegroundColor != nil {
							t.Errorf("%s: the view sets a terminal color", state)
						}
						if view.Cursor != nil && view.Cursor.Color != nil {
							t.Errorf("%s: the cursor sets a terminal color", state)
						}
						assertPainted(t, view.Content, name, fmt.Sprintf("%s %s %dx%d", modeName(advanced), state, s[0], s[1]), s[0], s[1])
					}
					d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
					d.rewrap()
				})
			})
		}
	}
}

var sgrPattern = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// colorSGR returns the first SGR sequence that sets a color.
func colorSGR(content string) string {
	for _, match := range sgrPattern.FindAllStringSubmatch(content, -1) {
		for _, part := range strings.FieldsFunc(match[1], func(r rune) bool { return r == ';' || r == ':' }) {
			n, _ := strconv.Atoi(part)
			switch {
			case n >= 30 && n <= 38, n >= 40 && n <= 48, n == 58, n >= 90 && n <= 97, n >= 100 && n <= 107:
				return fmt.Sprintf("%q", match[0])
			}
		}
	}
	return ""
}

func TestNoColorFrames(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		for _, advanced := range []bool{false, true} {
			t.Run(name+"/"+modeName(advanced), func(t *testing.T) {
				t.Parallel()
				g := newFakeGateway(t)
				d := g.open(t, rig{theme: name, profile: colorprofile.ASCII})
				if advanced {
					enterAdvanced(t, d)
				}
				walk(d, g, func(state string) {
					t.Helper()
					for _, s := range [][2]int{{30, 8}, {80, 24}, {120, 40}} {
						d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
						d.rewrap()
						view := d.m.View()
						if found := colorSGR(view.Content); found != "" {
							t.Errorf("%s %dx%d: color in the frame: %s", state, s[0], s[1], found)
						}
						if view.BackgroundColor != nil || view.ForegroundColor != nil {
							t.Errorf("%s: the view sets a terminal color", state)
						}
					}
					d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
					d.rewrap()
				})
			})
		}
	}
}

// Under NO_COLOR, state has to survive in symbols and decoration.
func TestNoColorKeepsMeaning(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{profile: colorprofile.ASCII})
	d.do("delete-all")
	frame := d.m.View().Content
	reversed := false
	for _, match := range sgrPattern.FindAllStringSubmatch(frame, -1) {
		for _, part := range strings.Split(match[1], ";") {
			reversed = reversed || part == "7"
		}
	}
	if !reversed {
		t.Errorf("the focused control cannot be told apart without color:\n%q", frame)
	}
	d.closeAll()

	d.key("tab")
	d.expect("▸")
	d.key("esc")
	d.m.setDown(errFake)
	d.send(struct{}{})
	d.expect("○ Not connected")
}

// recorder keeps the latest frame as plain text. The program's output is a stream of
// diffs, which can split any word across writes, so tests read this instead.
type recorder struct {
	*Model
	mu    sync.Mutex
	frame string
}

func (r *recorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := r.Model.Update(msg)
	frame := ansi.Strip(r.Model.View().Content)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frame = frame
	return r, cmd
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frame
}

// runProgram runs the real Bubble Tea loop with bytes through the real decoder.
func runProgram[W io.Writer](t *testing.T, g *fakeGateway, profile colorprofile.Profile, r rig, script func(wait func(string), keys W)) string {
	t.Helper()
	cfg := config.Default()
	cfg.Gateway.BaseURL = g.URL
	cfg.Appearance.Theme = "dark"
	r.config = func(c *config.Config) { *c = cfg }
	d := r.build(t, g.URL)
	in, pipe := io.Pipe()
	keys := any(pipe).(W)
	var output syncBuffer
	rec := &recorder{Model: d.m}
	program := tea.NewProgram(rec, tea.WithInput(in), tea.WithOutput(&output), tea.WithWindowSize(100, 30),
		tea.WithoutSignals(), tea.WithColorProfile(profile), tea.WithFilter(Filter))
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	wait := func(what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(rec.text(), what) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q in:\n%s", what, rec.text())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	script(wait, keys)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not quit")
	}
	return output.String()
}

func TestNoColorThroughTheRenderer(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": richReply}), doneEvent("stop")) })
	output := runProgram(t, g, colorprofile.ASCII, rig{profile: colorprofile.ASCII}, func(wait func(string), keys io.Writer) {
		wait("What can I help you with?")
		keys.Write([]byte("Hello\r"))
		wait("That is all")
		keys.Write([]byte{3, 3})
	})
	if found := colorSGR(output); found != "" {
		t.Errorf("color reached the terminal: %s", found)
	}
}

// At 256 colors the frame stays painted, and no true color sequence goes out.
func TestAnsi256ThroughTheRenderer(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": richReply}), doneEvent("stop")) })
	output := runProgram(t, g, colorprofile.ANSI256, rig{profile: colorprofile.ANSI256}, func(wait func(string), keys io.Writer) {
		wait("What can I help you with?")
		keys.Write([]byte("Hello\r"))
		wait("That is all")
		keys.Write([]byte{3, 3})
	})
	if strings.Contains(output, "38;2;") || strings.Contains(output, "48;2;") {
		t.Error("true color sequences at 256 colors")
	}
	if !strings.Contains(output, "48;5;") {
		t.Error("no painted background at 256 colors")
	}
}
