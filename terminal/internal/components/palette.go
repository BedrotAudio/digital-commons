package components

import (
	"strings"

	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	"github.com/charmbracelet/x/ansi"
)

// Command is one row of the slash palette.
type Command struct {
	Name     string // what is run, including any fixed argument
	Hint     string // argument hint shown after the name
	Desc     string // plain description
	Advanced bool   // Advanced mode only
	NeedsArg bool   // picking it completes the text instead of running
}

// Commands is the table from docs/ui-spec.md. Rows with a space in Name appear
// once the text has reached that space.
var Commands = []Command{
	{Name: "/help", Desc: "Show help"},
	{Name: "/new", Desc: "Start a new chat"},
	{Name: "/clear", Desc: "New chat. The old one stays in your history"},
	{Name: "/attach", Hint: "[path]", Desc: "Attach a file"},
	{Name: "/search", Hint: "[question]", Desc: "Ask approved documents, or turn that on and off"},
	{Name: "/privacy", Desc: "See how your information is handled"},
	{Name: "/status", Desc: "See how things are running"},
	{Name: "/copy", Desc: "Copy the latest reply"},
	{Name: "/copy code", Hint: "[n]", Desc: "Copy a code block", Advanced: true},
	{Name: "/theme", Hint: "auto|dark|light", Desc: "Change the appearance", NeedsArg: true},
	{Name: "/theme auto", Desc: "Follow this window"},
	{Name: "/theme dark", Desc: "Dark appearance"},
	{Name: "/theme light", Desc: "Light appearance"},
	{Name: "/mode", Hint: "basic|developer", Desc: "Switch between Basic and Advanced", NeedsArg: true},
	{Name: "/mode basic", Desc: "Switch to Basic"},
	{Name: "/mode developer", Desc: "Switch to Advanced"},
	{Name: "/model", Hint: "[tier]", Desc: "Choose a model tier", Advanced: true},
	{Name: "/model auto", Desc: "Route each turn automatically", Advanced: true},
	{Name: "/model fast", Desc: "Fast tier", Advanced: true},
	{Name: "/model standard", Desc: "Standard tier", Advanced: true},
	{Name: "/model advanced", Desc: "Advanced tier", Advanced: true},
	{Name: "/code", Desc: "Turn the coding assistant on or off", Advanced: true},
	{Name: "/delete", Desc: "Delete this chat"},
	{Name: "/exit", Desc: "Quit"},
}

// IsAdvancedCommand reports whether the first word is an Advanced-only command.
func IsAdvancedCommand(word string) bool {
	return word == "/model" || word == "/code"
}

// Palette is the filtered list above the composer.
type Palette struct {
	Open     bool
	Items    []Command
	Cursor   int
	Offset   int
	Advanced bool
	input    string
	closed   string // the text Esc was pressed on
}

// IsCommandLine reports whether composer text is a slash command. A path such as
// /Users/a/notes.txt is not, so a pasted path goes to the assistant as a message.
func IsCommandLine(text string) bool {
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return false
	}
	word, _, _ := strings.Cut(text, " ")
	return !strings.Contains(word[1:], "/")
}

// Sync refreshes the palette from the composer text. It opens for a command line,
// and stays shut after Esc until the text changes.
func (p *Palette) Sync(text string) {
	if text == p.input {
		return
	}
	p.input = text
	if !IsCommandLine(text) {
		p.Open = false
		p.closed = ""
		return
	}
	if text == p.closed {
		return
	}
	p.closed = ""
	p.refilter()
}

// SetAdvanced changes which commands are listed.
func (p *Palette) SetAdvanced(advanced bool) {
	p.Advanced = advanced
	if p.Open {
		p.refilter()
	}
}

func (p *Palette) refilter() {
	p.Items = Filter(p.input, p.Advanced)
	p.Cursor = 0
	p.Offset = 0
	p.Open = len(p.Items) > 0
}

// Filter returns the rows matching the text. A bare command word shows the
// commands. Once the text has a space it shows that command's arguments.
func Filter(text string, advanced bool) []Command {
	var out []Command
	spaced := strings.Contains(text, " ")
	for _, c := range Commands {
		if c.Advanced && !advanced {
			continue
		}
		fixed := strings.Contains(c.Name, " ")
		switch {
		case !spaced && !fixed && strings.HasPrefix(c.Name, text):
			out = append(out, c)
		case spaced && fixed && strings.HasPrefix(c.Name, text):
			out = append(out, c)
		case spaced && !fixed && strings.HasPrefix(text, c.Name+" ") && !c.NeedsArg:
			out = append(out, c)
		}
	}
	return out
}

// Close shuts the list until the text changes.
func (p *Palette) Close() {
	p.Open = false
	p.closed = p.input
}

// CloseOn shuts the list for text the composer is about to hold.
func (p *Palette) CloseOn(text string) {
	p.Open = false
	p.input, p.closed = text, text
}

// Move shifts the highlight, wrapping at the ends.
func (p *Palette) Move(delta int) {
	if len(p.Items) > 0 {
		p.Cursor = (p.Cursor + delta + len(p.Items)) % len(p.Items)
	}
}

// Selected returns the highlighted row.
func (p *Palette) Selected() (Command, bool) {
	if !p.Open || p.Cursor >= len(p.Items) {
		return Command{}, false
	}
	return p.Items[p.Cursor], true
}

// PaletteOut is a drawn palette. Zones are relative to its top-left corner.
type PaletteOut struct {
	Content string
	H       int
	Zones   []views.Zone
}

// Render draws up to maxRows rows in a bordered box of the given width.
func (p *Palette) Render(th theme.Theme, width, maxRows int) PaletteOut {
	rows := min(len(p.Items), max(maxRows, 1))
	if p.Cursor < p.Offset {
		p.Offset = p.Cursor
	}
	if p.Cursor >= p.Offset+rows {
		p.Offset = p.Cursor - rows + 1
	}
	inner := width - 4
	nameWidth := 0
	for _, c := range p.Items {
		nameWidth = max(nameWidth, ansi.StringWidth(label(c)))
	}
	nameWidth = min(nameWidth+2, inner/2+6)

	st := th.Styles
	bar := st.Border.Render("│")
	lines := []string{st.Border.Render("╭" + strings.Repeat("─", width-2) + "╮")}
	var zones []views.Zone
	for i := p.Offset; i < p.Offset+rows; i++ {
		c := p.Items[i]
		name := views.PadRight(views.Truncate(label(c), nameWidth-1), nameWidth)
		desc := views.Truncate(c.Desc, max(inner-nameWidth-2, 0))
		marker := "  "
		nameStyle, descStyle := st.Text, st.Muted
		if i == p.Cursor {
			marker = "▸ "
			nameStyle = st.Accent.Bold(true)
			descStyle = st.Text
		}
		row := views.PadRight(marker+nameStyle.Render(name)+descStyle.Render(desc), inner)
		y := len(lines)
		lines = append(lines, bar+" "+row+" "+bar)
		zones = append(zones, views.Zone{X: 1, Y: y, W: width - 2, H: 1, Action: views.Action{Kind: "palette", N: i}})
	}
	bottom := "╰" + strings.Repeat("─", width-2) + "╯"
	if p.Offset+rows < len(p.Items) {
		const more = " ▾ more ─╯"
		bottom = "╰" + strings.Repeat("─", max(width-1-ansi.StringWidth(more), 0)) + more
	}
	lines = append(lines, st.Border.Render(bottom))
	return PaletteOut{Content: strings.Join(lines, "\n"), H: len(lines), Zones: zones}
}

func label(c Command) string {
	if c.Hint != "" {
		return c.Name + " " + c.Hint
	}
	return c.Name
}
