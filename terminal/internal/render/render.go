// Package render turns Markdown into terminal lines. Prose goes through glamour
// and top-level fenced code gets its own numbered frame, so the UI can locate and
// copy each block.
package render

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	"digital-commons/terminal/internal/security"
	"digital-commons/terminal/internal/theme"

	glamouransi "charm.land/glamour/v2/ansi"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const tabStop = "    "

// CodeBlock describes one top-level fenced block in rendered output.
type CodeBlock struct {
	Number    int    // the number shown in the label
	Language  string // from the fence, empty when none was given
	Text      string // the code to copy, with no trailing newline
	LabelLine int    // index into Output.Lines of the label row, for hit testing
}

// Output is one rendered document. Lines hold SGR styling and nothing else, and
// none is wider than the Renderer's width.
type Output struct {
	Lines      []string
	CodeBlocks []CodeBlock
}

// Renderer renders for one width and theme. It is cheap to build, so make a new
// one when either changes.
type Renderer struct {
	width  int
	theme  theme.Theme
	styles glamouransi.StyleConfig
	parser goldmark.Markdown
	code   codeStyles
}

// New returns a Renderer. A width below 1 is treated as 1.
func New(width int, t theme.Theme) *Renderer {
	width = max(width, 1)
	return &Renderer{
		width:  width,
		theme:  t,
		styles: styleConfig(t, width),
		parser: goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList)),
		code:   newCodeStyles(t),
	}
}

// Width returns the width this Renderer was built for.
func (r *Renderer) Width() int { return r.width }

// ThemeName returns the name of the theme this Renderer was built for.
func (r *Renderer) ThemeName() string { return r.theme.Name }

// Render numbers code blocks from 1. See RenderFrom.
func (r *Renderer) Render(markdown string) Output {
	return r.RenderFrom(markdown, 1)
}

// RenderFrom renders Markdown, numbering code blocks from first. The input may be
// a partial stream, including an unclosed fence. It is cleaned here as well, so a
// caller cannot forget, and that is safe to repeat.
func (r *Renderer) RenderFrom(markdown string, first int) Output {
	src := []byte(security.Clean(markdown))
	doc := r.parser.Parser().Parse(text.NewReader(src))
	src = rewrite(doc, src)

	var out Output
	number := first
	chunk := ast.NewDocument()

	flush := func() {
		if chunk.ChildCount() == 0 {
			return
		}
		out.append(r.prose(chunk, src, r.width))
		chunk = ast.NewDocument()
	}

	for n := doc.FirstChild(); n != nil; {
		next := n.NextSibling()
		doc.RemoveChild(doc, n)
		if fence, ok := n.(*ast.FencedCodeBlock); ok {
			flush()
			block := CodeBlock{
				Number:   number,
				Language: string(fence.Language(src)),
				Text:     fenceText(fence, src),
			}
			block.LabelLine = out.append(r.codeFrame(block))
			out.CodeBlocks = append(out.CodeBlocks, block)
			number++
		} else if _, ok := n.(*ast.List); ok && r.width >= hangMinWidth {
			flush()
			chunk.AppendChild(chunk, n)
			out.append(hang(r.prose(chunk, src, r.width-hangRoom)))
			chunk = ast.NewDocument()
		} else {
			chunk.AppendChild(chunk, n)
		}
		n = next
	}
	flush()
	return out
}

// append adds a block of lines, separated from the last by one blank line, and
// returns the index of the block's first line.
func (o *Output) append(lines []string) int {
	if len(lines) == 0 {
		return len(o.Lines)
	}
	if len(o.Lines) > 0 {
		o.Lines = append(o.Lines, "")
	}
	start := len(o.Lines)
	o.Lines = append(o.Lines, lines...)
	return start
}

func fenceText(n *ast.FencedCodeBlock, src []byte) string {
	var b strings.Builder
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\n")
}

// hangRoom is what a list gives up from its wrap width, so that hang can indent
// continuation lines. Below hangMinWidth that costs more than it gives.
const (
	hangRoom     = 4
	hangMinWidth = 24
)

var listMarker = regexp.MustCompile(`^ *(-|\d+\.) `)

// hang indents the lines of a wrapped list item to line up under its text.
func hang(lines []string) []string {
	indent := 0
	for i, line := range lines {
		plain := ansi.Strip(line)
		if strings.TrimSpace(plain) == "" {
			continue
		}
		if marker := listMarker.FindString(plain); marker != "" {
			indent = len(marker)
			continue
		}
		if pad := indent - (len(plain) - len(strings.TrimLeft(plain, " "))); pad > 0 {
			lines[i] = strings.Repeat(" ", pad) + line
		}
	}
	return lines
}

// prose renders one run of non-fence blocks through glamour, wrapped at wrap. If
// glamour fails or panics on odd input, the reply is still shown as plain wrapped
// text.
func (r *Renderer) prose(doc ast.Node, src []byte, wrap int) (lines []string) {
	plain := func() []string {
		return r.finish(strings.Split(html.UnescapeString(plainText(doc, src)), "\n"))
	}
	defer func() {
		if recover() != nil {
			lines = plain()
		}
	}()

	var buf bytes.Buffer
	glam := glamouransi.NewRenderer(glamouransi.Options{
		WordWrap:         wrap,
		Styles:           r.styles,
		ChromaFormatter:  "terminal16m",
		InlineTableLinks: true,
	})
	md := renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(glam, 1000)))
	if err := md.Render(&buf, src, doc); err != nil {
		return plain()
	}
	return r.finish(strings.Split(buf.String(), "\n"))
}

// finish scrubs each line, enforces the width, and removes outer and repeated
// blank lines.
func (r *Renderer) finish(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, row := range r.fit(scrub(line)) {
			if blank(row) && (len(out) == 0 || blank(out[len(out)-1])) {
				continue
			}
			out = append(out, row)
		}
	}
	for len(out) > 0 && blank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return out
}

func blank(line string) bool {
	return strings.TrimSpace(ansi.Strip(line)) == ""
}

// fit makes sure a line is no wider than the renderer. glamour already wraps, so
// this catches only what it leaves long, such as a fence inside a list.
func (r *Renderer) fit(line string) []string {
	line = trimRight(line)
	if ansi.StringWidth(line) <= r.width {
		return []string{line}
	}
	parts := strings.Split(ansi.Hardwrap(line, r.width, true), "\n")
	for i, p := range parts {
		parts[i] = ansi.Truncate(p, r.width, "")
	}
	return parts
}

// trimRight removes trailing padding, including padding wrapped in styling.
func trimRight(line string) string {
	for {
		trimmed := strings.TrimRight(line, " ")
		rest, ok := strings.CutSuffix(trimmed, ansi.ResetStyle)
		if !ok {
			return trimmed
		}
		if inner := strings.TrimRight(rest, " "); inner != rest {
			line = inner + ansi.ResetStyle
			continue
		}
		return trimmed
	}
}

// scrub is the last check on what reaches the terminal. It keeps text and SGR and
// drops every other sequence, whatever produced it, then expands tabs because a
// tab has no fixed width.
func scrub(s string) string {
	var b strings.Builder
	var state byte
	for len(s) > 0 {
		seq, _, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		s = s[n:]
		switch {
		case seq == "\t":
			b.WriteString(tabStop)
		case isSGR(seq):
			b.WriteString(seq)
		case seq == "" || seq[0] == ansi.ESC || seq[0] < 0x20 || seq[0] == 0x7f:
		default:
			b.WriteString(security.Clean(seq))
		}
	}
	return b.String()
}

// isSGR accepts only ESC [ parameters m, so that private forms such as ESC [ > 4 m
// are refused.
func isSGR(seq string) bool {
	body, ok := strings.CutPrefix(seq, "\x1b[")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, "m")
	if !ok {
		return false
	}
	for _, c := range []byte(body) {
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

// CloseOpen ends emphasis that a cut-off reply left open in its last paragraph, so
// that a partial bold phrase reads as bold instead of showing its markers. Text
// inside an unfinished code fence is left alone.
func CloseOpen(text string) string {
	if strings.Count("\n"+text, "\n```")%2 == 1 {
		return text
	}
	trimmed := strings.TrimRight(text, " \n")
	last := trimmed[strings.LastIndex(trimmed, "\n\n")+1:]
	if strings.Count(last, "`")%2 == 1 {
		return trimmed + "`"
	}
	if strings.Count(last, "**")%2 == 1 {
		return strings.TrimSuffix(trimmed, "*") + "**"
	}
	return text
}
