package render

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"digital-commons/terminal/internal/theme"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
)

const (
	frameMinWidth = 8 // below this a frame leaves no room for code
	frameSides    = 4 // cells used by the two borders and their spaces
)

// codeStyles holds the frame styles and the highlight colors for one theme.
type codeStyles struct {
	border   lipgloss.Style
	number   lipgloss.Style
	language lipgloss.Style
	chroma   *chroma.Style
}

func newCodeStyles(t theme.Theme) codeStyles {
	return codeStyles{
		border:   t.Styles.Border,
		number:   t.Styles.Accent.Bold(true),
		language: t.Styles.Muted,
		chroma:   chromaStyles[t.Name],
	}
}

// segment is a run of code with one style.
type segment struct {
	text  string
	style lipgloss.Style
}

// codeFrame draws a block. Its first line is always the label row.
func (r *Renderer) codeFrame(b CodeBlock) []string {
	if r.width < frameMinWidth {
		return r.plainCode(b)
	}
	inner := r.width - frameSides
	bar := r.code.border.Render("│")

	lines := []string{r.frameTop(b)}
	for _, source := range highlight(b.Text, b.Language, r.code) {
		for _, row := range wrapSegments(source, inner) {
			lines = append(lines, bar+" "+row+strings.Repeat(" ", max(inner-ansi.StringWidth(row), 0))+" "+bar)
		}
	}
	bottom := "╰" + strings.Repeat("─", r.width-2) + "╯"
	return append(lines, r.code.border.Render(bottom))
}

// plainCode is the frameless form for very narrow columns.
func (r *Renderer) plainCode(b CodeBlock) []string {
	label := "[" + strconv.Itoa(b.Number) + "]"
	lines := []string{r.code.number.Render(ansi.Truncate(label, r.width, ""))}
	for _, source := range highlight(b.Text, b.Language, r.code) {
		lines = append(lines, wrapSegments(source, r.width)...)
	}
	return lines
}

// frameTop draws the label row: corner, bar and space, the number, an optional
// language, a space, at least one bar, and the closing corner.
func (r *Renderer) frameTop(b CodeBlock) string {
	const lead, tail = 3, 3
	number := "[" + strconv.Itoa(b.Number) + "]"
	used := lead + ansi.StringWidth(number) + tail

	label := r.code.border.Render("╭─ ") + r.code.number.Render(number)
	if room := r.width - used - 1; room > 0 && b.Language != "" {
		language := ansi.Truncate(b.Language, room, "")
		label += " " + r.code.language.Render(language)
		used += 1 + ansi.StringWidth(language)
	}
	bars := strings.Repeat("─", max(r.width-used+1, 0))
	return ansi.Truncate(label+" "+r.code.border.Render(bars+"╮"), r.width, "")
}

// hangingLexers never return on ordinary text. Jungle's rules push and pop a state
// without reading anything, which loops outside the regexps that Chroma's timeout
// covers, and a fence label comes from model text. TestEveryLexerFinishes looks for
// more of these.
var hangingLexers = map[string]bool{"Jungle": true}

// canHighlight reports whether a fence label may reach Chroma. It looks at the lexer
// the label resolves to, because a label can also name a file, as in x.jungle.
func canHighlight(language string) bool {
	lexer := lexers.Get(language)
	return lexer == nil || !hangingLexers[lexer.Config().Name]
}

// highlight splits code into source lines of styled segments. Chroma never fails
// on partial code: anything it cannot lex comes back as plain tokens.
func highlight(code, language string, styles codeStyles) [][]segment {
	lexer := lexers.Get(language)
	if lexer == nil || !canHighlight(language) {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	code = strings.ReplaceAll(code, "\t", tabStop)
	lines := [][]segment{nil}
	emit := func(value string, style lipgloss.Style) {
		for i, part := range strings.Split(value, "\n") {
			if i > 0 {
				lines = append(lines, nil)
			}
			if part != "" {
				last := len(lines) - 1
				lines[last] = append(lines[last], segment{part, style})
			}
		}
	}

	tokens, err := lexer.Tokenise(nil, code)
	if err != nil {
		emit(code, tokenStyle(styles.chroma, chroma.Text))
		return lines
	}
	for t := tokens(); t != chroma.EOF; t = tokens() {
		emit(t.Value, tokenStyle(styles.chroma, t.Type))
	}
	// Lexers add a final newline to their input, which is not part of the code.
	if len(lines) > 1 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func tokenStyle(s *chroma.Style, t chroma.TokenType) lipgloss.Style {
	entry := s.Get(t)
	style := lipgloss.NewStyle()
	if entry.Colour.IsSet() {
		style = style.Foreground(lipgloss.Color(entry.Colour.String()))
	}
	if entry.Bold == chroma.Yes {
		style = style.Bold(true)
	}
	if entry.Italic == chroma.Yes {
		style = style.Italic(true)
	}
	return style
}

// wrapSegments lays one source line into rows no wider than width, styling each
// row independently so that no style spans a line break.
func wrapSegments(source []segment, width int) []string {
	var rows []string
	var row strings.Builder
	used := 0
	endRow := func() {
		rows = append(rows, row.String())
		row.Reset()
		used = 0
	}
	for _, seg := range source {
		rest := seg.text
		for rest != "" {
			head := ansi.Truncate(rest, width-used, "")
			if head == "" {
				if used > 0 {
					endRow()
					continue
				}
				// One character wider than the row. Skip it rather than overflow.
				_, size := utf8.DecodeRuneInString(rest)
				rest = rest[size:]
				continue
			}
			row.WriteString(seg.style.Render(head))
			used += ansi.StringWidth(head)
			rest = rest[len(head):]
			if rest != "" {
				endRow()
			}
		}
	}
	endRow()
	return rows
}
