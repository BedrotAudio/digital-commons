package views

import (
	"strings"

	"digital-commons/terminal/internal/theme"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Span is a run of text in a panel row. A span with an action is a control: it
// can be clicked and reached with the keyboard.
type Span struct {
	Text   string
	Style  lipgloss.Style
	Action *Action
}

// Row is one line of a panel.
type Row []Span

// Panel is a modal box. Its rows are built by the caller for the inner width.
type Panel struct {
	Title  string
	Rows   []Row
	Pinned []Row // shown under Rows, which scroll, and never scrolled away
	Cursor int   // index among the controls, -1 for none
	Scroll int   // first visible row
	Width  int   // preferred outer width
}

// PanelOut is a laid out panel. Zones are relative to its top-left corner.
type PanelOut struct {
	Content string
	W, H    int
	Zones   []Zone
	Scroll  int // the scroll offset after keeping the cursor in view
	Visible int // rows shown at once
}

// PanelInner returns the text width of a panel of the preferred width in a window
// of the given width. Narrow windows use all of it.
func PanelInner(preferred, window int) int {
	return panelWidth(preferred, window) - 4
}

func panelWidth(preferred, window int) int {
	if window < 60 {
		return window
	}
	return min(preferred, window-4)
}

// Controls returns the actions of the rows in order.
func (p Panel) Controls() []Action {
	var out []Action
	for _, row := range p.Rows {
		for _, span := range row {
			if span.Action != nil {
				out = append(out, *span.Action)
			}
		}
	}
	return out
}

// RenderPanel lays out the panel for a window.
func RenderPanel(th theme.Theme, p Panel, windowW, windowH int) PanelOut {
	w := panelWidth(p.Width, windowW)
	inner := w - 4
	pinned := p.Pinned
	if panelRows(windowH)-len(pinned) < 3 {
		pinned = nil
	}
	visible := min(len(p.Rows), max(panelRows(windowH)-len(pinned), 1))

	scroll := min(p.Scroll, max(len(p.Rows)-visible, 0))
	if r := cursorRow(p); r >= 0 {
		if r < scroll {
			scroll = r
		}
		if r >= scroll+visible {
			scroll = r - visible + 1
		}
	}

	b := th.Styles.Border
	bar := b.Render("│")
	var lines []string
	var zones []Zone
	lines = append(lines, b.Render("╭"+strings.Repeat("─", w-2)+"╮"))

	closeLabel := "× Close"
	title := th.Styles.Text.Bold(true).Render(Truncate(p.Title, inner-ansi.StringWidth(closeLabel)-1))
	lines = append(lines, bar+" "+PadRight(title, inner-ansi.StringWidth(closeLabel))+th.Styles.Muted.Render(closeLabel)+" "+bar)
	zones = append(zones, Zone{X: 2 + inner - ansi.StringWidth(closeLabel), Y: 1, W: ansi.StringWidth(closeLabel), H: 1, Action: Action{Kind: "panel.close"}})
	lines = append(lines, bar+strings.Repeat(" ", w-2)+bar)

	control := 0
	for i, row := range p.Rows {
		var text strings.Builder
		x := 0
		for _, span := range row {
			label := span.Text
			style := span.Style
			if span.Action != nil {
				if control == p.Cursor {
					style = th.Styles.Button
				}
				if i >= scroll && i < scroll+visible {
					zones = append(zones, Zone{X: 2 + x, Y: 3 + i - scroll, W: ansi.StringWidth(label), H: 1, Action: *span.Action})
				}
				control++
			}
			text.WriteString(style.Render(label))
			x += ansi.StringWidth(label)
		}
		if i >= scroll && i < scroll+visible {
			lines = append(lines, bar+" "+PadRight(ansi.Truncate(text.String(), inner, "…"), inner)+" "+bar)
		}
	}

	for _, row := range pinned {
		var text strings.Builder
		for _, span := range row {
			text.WriteString(span.Style.Render(span.Text))
		}
		lines = append(lines, bar+" "+PadRight(ansi.Truncate(text.String(), inner, "…"), inner)+" "+bar)
	}

	more := "  "
	if scroll+visible < len(p.Rows) {
		more = " ▾ more "
	} else if scroll > 0 {
		more = " top "
	}
	bottom := "╰" + strings.Repeat("─", max(w-2-ansi.StringWidth(more)-1, 0)) + more + "─╯"
	if more == "  " {
		bottom = "╰" + strings.Repeat("─", w-2) + "╯"
	}
	lines = append(lines, b.Render(bottom))
	return PanelOut{
		Content: strings.Join(lines, "\n"),
		W:       w,
		H:       len(lines),
		Zones:   zones,
		Scroll:  scroll,
		Visible: visible,
	}
}

// panelRows is the room for rows. A tall window keeps the header and footer in
// sight around the panel, and a short one gives the panel everything.
func panelRows(windowH int) int {
	if windowH >= 24 {
		return windowH - 8
	}
	return windowH - 5
}

func cursorRow(p Panel) int {
	control := 0
	for i, row := range p.Rows {
		for _, span := range row {
			if span.Action != nil {
				if control == p.Cursor {
					return i
				}
				control++
			}
		}
	}
	return -1
}

// TextRows wraps plain text into panel rows of one style.
func TextRows(text string, width int, style lipgloss.Style) []Row {
	var rows []Row
	for _, line := range Wrap(text, width) {
		rows = append(rows, Row{{Text: line, Style: style}})
	}
	return rows
}

// Pair is a two-column row for a key and its value, wrapping the value.
func Pair(key, value string, keyWidth, width int, keyStyle, valueStyle lipgloss.Style) []Row {
	var rows []Row
	for i, line := range Wrap(value, max(width-keyWidth, 1)) {
		left := ""
		if i == 0 {
			left = Truncate(key, keyWidth-1)
		}
		rows = append(rows, Row{
			{Text: PadRight(left, keyWidth), Style: keyStyle},
			{Text: line, Style: valueStyle},
		})
	}
	return rows
}

// Control is a span that can be clicked and reached by keyboard.
func Control(label string, style lipgloss.Style, a Action) Span {
	return Span{Text: label, Style: style, Action: &a}
}

// ListRow is the row shape shared by the sidebar and the file picker: a marker
// column, then a truncated label.
func ListRow(marker, label string, width int) string {
	return PadRight(marker+" "+Truncate(OneLine(label), width-2), width)
}
