// Package views holds the drawing primitives and the fixed wording shared by the
// components and the application model. It has no state of its own.
package views

import (
	"image"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Action is what a click or Enter on an item does. Kind names the handler in the
// application model. Arg and N carry its argument.
type Action struct {
	Kind string
	Arg  string
	N    int
}

// Zone is a clickable rectangle in window cells.
type Zone struct {
	X, Y, W, H int
	Action     Action
}

// Contains reports whether the cell is inside the zone.
func (z Zone) Contains(x, y int) bool {
	return x >= z.X && x < z.X+z.W && y >= z.Y && y < z.Y+z.H
}

// Offset returns the zone moved by dx and dy.
func (z Zone) Offset(dx, dy int) Zone {
	z.X += dx
	z.Y += dy
	return z
}

// Frame is one window's worth of cells. Regions are drawn into it and painted at
// cell level, because no style can reach every cell.
type Frame struct {
	W, H    int
	Profile colorprofile.Profile
	canvas  *lipgloss.Canvas
	zones   []Zone
}

// NewFrame returns an empty frame.
func NewFrame(w, h int, profile colorprofile.Profile) *Frame {
	return &Frame{W: w, H: h, Profile: profile, canvas: lipgloss.NewCanvas(w, h)}
}

// Put draws content into a rectangle, replacing what was there and clipping
// anything outside it.
func (f *Frame) Put(x, y, w, h int, content string) {
	if w <= 0 || h <= 0 {
		return
	}
	lipgloss.NewLayer(content).Draw(f.canvas, image.Rect(x, y, x+w, y+h))
}

// Paint fills every cell of a rectangle that has no color of its own. It does
// nothing at ASCII, so that no color is emitted.
func (f *Frame) Paint(x, y, w, h int, bg, fg color.Color) {
	if f.Profile <= colorprofile.ASCII {
		return
	}
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			cell := f.canvas.CellAt(col, row)
			if cell == nil {
				continue
			}
			if cell.Style.Bg == nil {
				cell.Style.Bg = bg
			}
			if cell.Style.Fg == nil {
				cell.Style.Fg = fg
			}
		}
	}
}

// Strip removes every color from the frame. It is the last step at ASCII, so that
// NO_COLOR holds for the frame itself and not only at the terminal renderer.
func (f *Frame) Strip() {
	if f.Profile > colorprofile.ASCII {
		return
	}
	for row := range f.H {
		for col := range f.W {
			if cell := f.canvas.CellAt(col, row); cell != nil {
				cell.Style.Fg, cell.Style.Bg, cell.Style.UnderlineColor = nil, nil, nil
			}
		}
	}
}

// Region is Put followed by Paint.
func (f *Frame) Region(x, y, w, h int, content string, bg, fg color.Color) {
	f.Put(x, y, w, h, content)
	f.Paint(x, y, w, h, bg, fg)
}

// Click records a clickable rectangle. Later zones win.
func (f *Frame) Click(x, y, w, h int, a Action) {
	f.zones = append(f.zones, Zone{X: x, Y: y, W: w, H: h, Action: a})
}

// AddZones records zones already placed in window cells.
func (f *Frame) AddZones(zones []Zone) { f.zones = append(f.zones, zones...) }

// Zones returns the clickable rectangles, lowest first.
func (f *Frame) Zones() []Zone { return f.zones }

// String renders the frame to exactly the window size.
func (f *Frame) String() string { return Fit(f.canvas.Render(), f.W, f.H) }

// Fit forces content to exactly h lines, none wider than w.
func Fit(content string, w, h int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > w {
			lines[i] = ansi.Truncate(line, w, "")
		}
	}
	return strings.Join(lines, "\n")
}

// Truncate shortens a plain or styled string to width cells, ending in an
// ellipsis when anything was cut.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// TruncateStart shortens plain text from the left, for a path, whose end matters
// most.
func TruncateStart(s string, width int) string {
	if width <= 0 {
		return ""
	}
	extra := ansi.StringWidth(s) - width
	if extra <= 0 {
		return s
	}
	return ansi.TruncateLeft(s, extra+1, "…")
}

// OneLine folds all whitespace to single spaces, for gateway text bound for one
// row. A newline there would push every row below it away from its click target.
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// PadRight pads a plain or styled string with spaces to width cells.
func PadRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-ansi.StringWidth(s), 0))
}

// PadLeft pads a plain or styled string on the left to width cells.
func PadLeft(s string, width int) string {
	return strings.Repeat(" ", max(width-ansi.StringWidth(s), 0)) + s
}

// Center pads a plain or styled string on the left so that it sits in the middle
// of width cells.
func Center(s string, width int) string {
	return strings.Repeat(" ", max((width-ansi.StringWidth(s))/2, 0)) + s
}

// Wrap breaks plain text into lines no wider than width, at spaces only, because
// ansi.Wrap also breaks at every hyphen and splits dates. A word longer than the
// line is cut, so that a long URL or path still fits. A tab has no fixed width, so
// it becomes spaces.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n") {
		line, used := "", 0
		for i, word := range strings.Split(paragraph, " ") {
			w := ansi.StringWidth(word)
			if i > 0 && used+1+w <= width {
				line += " " + word
				used += 1 + w
				continue
			}
			if i > 0 {
				out = append(out, line)
			}
			for w > width {
				head := ansi.Truncate(word, width, "")
				if head == "" {
					head = string([]rune(word)[:1])
				}
				out = append(out, head)
				word = word[len(head):]
				w = ansi.StringWidth(word)
			}
			line, used = word, w
		}
		out = append(out, line)
	}
	return out
}
