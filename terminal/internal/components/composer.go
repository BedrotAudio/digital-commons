// Package components holds the stateful parts of the screen: the composer, the
// transcript, the sidebar, the command palette and the file picker.
package components

import (
	"strings"

	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	composerMaxLines = 6
	chipMaxWidth     = 26
)

// Chip is a removable item above the text.
type Chip struct {
	Label  string
	Action views.Action
}

// Composer is the message box. It wraps textarea with the chips, the + button and
// the send and stop control.
type Composer struct {
	ta         textarea.Model
	th         theme.Theme
	width      int
	Chips      []Chip
	Busy       bool // a reply is running, so the control offers Stop
	Advanced   bool
	chipRowMax int
	fitted     int // the text width last given to the textarea
}

// NewComposer returns an empty composer. The textarea settings are the ones
// recorded in docs/implementation-notes.md.
func NewComposer(th theme.Theme) *Composer {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.Placeholder = views.Placeholder
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = composerMaxLines
	ta.MaxContentHeight = 10000
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j", "ctrl+enter")
	ta.KeyMap.Paste.SetEnabled(false)
	ta.SetVirtualCursor(false)
	c := &Composer{ta: ta, th: th, width: 40}
	c.SetTheme(th)
	c.SetWidth(c.width)
	return c
}

// SetTheme applies a theme. No style sets a background, because the frame paints
// every cell behind the composer.
func (c *Composer) SetTheme(th theme.Theme) {
	c.th = th
	state := textarea.StyleState{
		Base:        lipgloss.NewStyle(),
		Text:        th.Styles.Text,
		CursorLine:  th.Styles.Text,
		Placeholder: th.Styles.Muted,
		Prompt:      th.Styles.Muted,
		EndOfBuffer: lipgloss.NewStyle(),
		Selection:   th.Styles.Raised,
	}
	st := c.ta.Styles()
	st.Focused, st.Blurred = state, state
	st.Cursor.Color = nil
	st.Cursor.Blink = true
	st.Cursor.Shape = tea.CursorBar
	c.ta.SetStyles(st)
}

// SetLimits caps the text and chip rows, so that a short window keeps room for the
// conversation. The text still scrolls internally beyond the cap.
func (c *Composer) SetLimits(textRows, chipRows int) {
	c.chipRowMax = chipRows
	if c.ta.MaxHeight != textRows {
		c.ta.MaxHeight = textRows
		c.refit()
	}
}

// SetWidth sets the outer width, borders included. It runs after every message, so
// it touches the textarea, which costs time with a long draft, only on a change.
func (c *Composer) SetWidth(outer int) {
	c.width = outer
	if max(c.textWidth(), 1) != c.fitted {
		c.refit()
	}
}

// refit applies the size, then updates the textarea, which is the only way to make
// it scroll the cursor back into view.
func (c *Composer) refit() {
	c.fitted = max(c.textWidth(), 1)
	c.ta.SetWidth(c.fitted)
	c.ta, _ = c.ta.Update(nil)
}

func (c *Composer) prefix() string {
	if c.Advanced {
		return "+ > "
	}
	return "+ "
}

func (c *Composer) controlLabel() string {
	if c.Busy {
		if c.width >= 50 {
			return "■ Stop  Esc"
		}
		return "■ Stop"
	}
	return "Send"
}

// slotWidth reserves room for the longest control, so the text does not rewrap
// when a reply starts.
func (c *Composer) slotWidth() int {
	if c.width >= 50 {
		return 12
	}
	return 7
}

func (c *Composer) textWidth() int {
	return c.width - 4 - ansi.StringWidth(c.prefix()) - c.slotWidth()
}

// Update forwards a message to the textarea.
func (c *Composer) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	c.ta, cmd = c.ta.Update(msg)
	return cmd
}

// Focus gives the textarea the cursor.
func (c *Composer) Focus() tea.Cmd {
	cmd := c.ta.Focus()
	c.ta, _ = c.ta.Update(nil)
	return cmd
}

// Blur takes the cursor away.
func (c *Composer) Blur() { c.ta.Blur() }

// Focused reports whether the composer has the cursor.
func (c *Composer) Focused() bool { return c.ta.Focused() }

// Value returns the text.
func (c *Composer) Value() string { return c.ta.Value() }

// SetValue replaces the text and moves the cursor to its end.
func (c *Composer) SetValue(s string) {
	c.ta.SetValue(s)
	c.ta.MoveToEnd()
}

// Reset clears the text.
func (c *Composer) Reset() { c.ta.Reset() }

// Height returns the rows the composer needs with borders.
func (c *Composer) Height() int { return c.BareHeight() + 2 }

// BareHeight returns the rows the composer needs without borders.
func (c *Composer) BareHeight() int {
	return len(c.chipRows()) + max(c.ta.Height(), 1)
}

type chipPlace struct {
	chip Chip
	x    int
	w    int
}

// chipRows lays the chips into rows of the inner width.
func (c *Composer) chipRows() [][]chipPlace {
	inner := c.width - 4
	var rows [][]chipPlace
	var row []chipPlace
	x := 0
	for _, chip := range c.Chips {
		w := min(ansi.StringWidth(views.Truncate(chip.Label, chipMaxWidth))+4, inner)
		if x > 0 && x+w > inner {
			rows = append(rows, row)
			row, x = nil, 0
		}
		row = append(row, chipPlace{chip: chip, x: x, w: w})
		x += w + 1
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if c.chipRowMax > 0 && len(rows) > c.chipRowMax {
		rows = rows[len(rows)-c.chipRowMax:]
	}
	return rows
}

// Rendered is a drawn composer. Zones and the cursor are relative to its top-left
// corner.
type Rendered struct {
	Content string
	Zones   []views.Zone
	CursorX int
	CursorY int
	Cursor  bool
}

// Render draws the composer with its rounded border.
func (c *Composer) Render(focused bool) Rendered {
	border := c.th.Styles.Border
	if focused {
		border = c.th.Styles.Accent
	}
	bar := border.Render("│")
	out := c.body()
	inner := c.width - 2
	lines := []string{border.Render("╭" + strings.Repeat("─", inner) + "╮")}
	for _, line := range out.lines {
		lines = append(lines, bar+" "+line+" "+bar)
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", inner)+"╯"))

	r := Rendered{Content: strings.Join(lines, "\n")}
	for _, z := range out.zones {
		r.Zones = append(r.Zones, z.Offset(2, 1))
	}
	r.CursorX, r.CursorY, r.Cursor = out.cursorX+2, out.cursorY+1, out.cursor
	return r
}

// RenderBare draws the rows only, for the Advanced frame, at a width of Width-4.
func (c *Composer) RenderBare() Rendered {
	out := c.body()
	return Rendered{
		Content: strings.Join(out.lines, "\n"),
		Zones:   out.zones,
		CursorX: out.cursorX,
		CursorY: out.cursorY,
		Cursor:  out.cursor,
	}
}

type composerBody struct {
	lines            []string
	zones            []views.Zone
	cursorX, cursorY int
	cursor           bool
}

func (c *Composer) body() composerBody {
	var b composerBody
	inner := c.width - 4
	styles := c.th.Styles

	rows := c.chipRows()
	for y, row := range rows {
		var line strings.Builder
		x := 0
		for _, p := range row {
			line.WriteString(strings.Repeat(" ", p.x-x))
			label := views.Truncate(p.chip.Label, p.w-4)
			line.WriteString(styles.Raised.Render(" " + label + " ×" + " "))
			x = p.x + ansi.StringWidth(label) + 4
			b.zones = append(b.zones, views.Zone{X: p.x, Y: y, W: p.w, H: 1, Action: p.chip.Action})
		}
		b.lines = append(b.lines, views.PadRight(line.String(), inner))
	}

	prefix := c.prefix()
	prefixWidth := ansi.StringWidth(prefix)
	textWidth := c.textWidth()
	slot := c.slotWidth()
	text := strings.Split(strings.TrimRight(c.ta.View(), "\n"), "\n")
	top := len(b.lines)
	for i, line := range text {
		lead := strings.Repeat(" ", prefixWidth)
		if i == 0 {
			lead = styles.Accent.Bold(true).Render("+") + styles.Muted.Render(prefix[1:])
		}
		tail := strings.Repeat(" ", slot)
		if i == len(text)-1 {
			label := c.controlLabel()
			style := styles.Muted
			if c.Busy {
				style = styles.Text.Bold(true)
			} else if c.canSend() {
				style = styles.Accent.Bold(true)
			}
			tail = views.PadLeft(style.Render(label), slot)
			action := "send"
			if c.Busy {
				action = "stop"
			}
			w := ansi.StringWidth(label)
			b.zones = append(b.zones, views.Zone{X: prefixWidth + textWidth + slot - w, Y: top + i, W: w, H: 1, Action: views.Action{Kind: action}})
		}
		b.lines = append(b.lines, lead+views.PadRight(ansi.Truncate(line, textWidth, ""), textWidth)+tail)
	}
	b.zones = append(b.zones, views.Zone{X: 0, Y: top, W: 1, H: 1, Action: views.Action{Kind: "attach"}})

	if cur := c.ta.Cursor(); cur != nil {
		b.cursor = true
		b.cursorX = prefixWidth + cur.Position.X
		b.cursorY = top + cur.Position.Y
	}
	return b
}

func (c *Composer) canSend() bool {
	return strings.TrimSpace(c.ta.Value()) != "" || len(c.Chips) > 0
}
