package e2e

import (
	"fmt"
	"html"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// Page colors for cells the program left at the terminal default. A fully
// painted frame has none, and shot fails the test if one appears.
const (
	defaultFg = "#cccccc"
	defaultBg = "#000000"
)

var (
	captureMu sync.Mutex
	captured  []capture
)

type capture struct {
	test, name string
	w, h       int
	html, text string
	background string
	unpainted  int
}

func hex(c color.Color, fallback string) string {
	if c == nil {
		return fallback
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// spanStyle is the CSS for one cell.
func spanStyle(st uv.Style) string {
	fg, bg := hex(st.Fg, defaultFg), hex(st.Bg, defaultBg)
	if st.Attrs&uv.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	css := "color:" + fg + ";background:" + bg
	if st.Attrs&uv.AttrBold != 0 {
		css += ";font-weight:bold"
	}
	if st.Attrs&uv.AttrItalic != 0 {
		css += ";font-style:italic"
	}
	if st.Attrs&uv.AttrFaint != 0 {
		css += ";opacity:.7"
	}
	if st.Underline != uv.UnderlineStyleNone {
		css += ";text-decoration:underline"
	}
	if st.Attrs&uv.AttrStrikethrough != 0 {
		css += ";text-decoration:line-through"
	}
	return css
}

// commonBackground is the color behind most cells. The pre uses it, so that the
// gap between lines shows the canvas rather than the page.
func commonBackground(rows [][]uv.Cell) string {
	counts := map[string]int{}
	best := defaultBg
	for _, row := range rows {
		for i := range row {
			bg := hex(row[i].Style.Bg, defaultBg)
			counts[bg]++
			if counts[bg] > counts[best] {
				best = bg
			}
		}
	}
	return best
}

func renderRows(rows [][]uv.Cell) (markup, plain string, unpainted int) {
	var out, text strings.Builder
	for _, row := range rows {
		var run strings.Builder
		style := ""
		flush := func() {
			if run.Len() > 0 {
				fmt.Fprintf(&out, `<span style="%s">%s</span>`, style, html.EscapeString(run.String()))
				run.Reset()
			}
		}
		for i := range row {
			c := &row[i]
			if c.Width == 0 && c.Content == "" {
				// The second half of a wide character.
				continue
			}
			if c.Style.Bg == nil {
				unpainted++
			}
			css := spanStyle(c.Style)
			if css != style {
				flush()
				style = css
			}
			content := c.Content
			if content == "" {
				content = " "
			}
			run.WriteString(content)
			text.WriteString(content)
		}
		flush()
		out.WriteByte('\n')
		text.WriteByte('\n')
	}
	return out.String(), text.String(), unpainted
}

// settle waits for output to pause, because a repaint reaches the emulator in
// pieces and a clear can arrive a moment before the text that follows it.
func (s *session) settle() {
	deadline := time.Now().Add(2 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		time.Sleep(60 * time.Millisecond)
		now := s.text()
		if now != "" && now == last {
			return
		}
		last = now
	}
}

// shot records the current screen and rewrites the output files, so a failed run
// still leaves everything captured up to that point.
func (s *session) shot(name string) {
	s.t.Helper()
	s.settle()
	rows, w, h := s.grab()
	markup, plain, unpainted := renderRows(rows)
	if unpainted > 0 {
		s.t.Errorf("screen %s has %d unpainted cells", name, unpainted)
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	captured = append(captured, capture{
		test: s.t.Name(), name: name, w: w, h: h,
		html: markup, text: plain, background: commonBackground(rows), unpainted: unpainted,
	})
	writeScreens(s.t)
}

const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Digital Commons screens</title>
<style>
body { background: #20252b; color: #d8dde2; margin: 24px;
  font-family: "Cascadia Mono", Consolas, "DejaVu Sans Mono", Menlo, monospace; }
h1 { font-size: 16px; font-weight: normal; }
h2 { font-size: 13px; font-weight: normal; margin: 28px 0 6px; color: #9dabb8; }
pre { display: inline-block; margin: 0; padding: 0; font: inherit; font-size: 13px;
  line-height: 1.25; }
</style>
</head>
<body>
<h1>Digital Commons, captured from the real binary in a pseudo terminal</h1>
`

func writeScreens(t *testing.T) {
	t.Helper()
	dir := os.Getenv(envScreens)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var page, text strings.Builder
	page.WriteString(pageHead)
	for i, c := range captured {
		title := fmt.Sprintf("%02d %s / %s (%dx%d)", i+1, c.test, c.name, c.w, c.h)
		fmt.Fprintf(&page, "<h2>%s</h2>\n<pre style=\"background:%s\">%s</pre>\n",
			html.EscapeString(title), c.background, c.html)
		fmt.Fprintf(&text, "===== %s =====\n%s\n", title, c.text)
	}
	page.WriteString("</body>\n</html>\n")
	if err := os.WriteFile(filepath.Join(dir, "screens.html"), []byte(page.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "screens.txt"), []byte(text.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
