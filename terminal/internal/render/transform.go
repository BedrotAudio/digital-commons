package render

import (
	"html"
	"strings"

	"digital-commons/terminal/internal/security"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// noTarget is a link destination glamour treats as having no URL, so it emits no
// OSC 8 sequence and no href, and only applies the link style to the text.
const noTarget = "#"

// rewriter edits the parsed document before glamour sees it. glamour would emit
// OSC 8 for every link and image, hide some URLs, and decode character references
// such as &#27; into real escape characters, even inside code spans, after Clean
// has run. New text is appended to the source so that existing offsets stay valid.
type rewriter struct {
	src   []byte
	depth int // quotes and lists around the current node
}

// maxNesting is how many quotes and lists can stand inside each other. glamour
// rewraps the output of every level, so past about 40 levels the time and memory
// run away, on the event loop. Nothing a person reads nests this deep.
const maxNesting = 8

// rewrite returns the source, extended with any text the new nodes point at.
func rewrite(doc ast.Node, src []byte) []byte {
	w := &rewriter{src: src}
	w.visit(doc)
	return w.src
}

func (w *rewriter) visit(parent ast.Node) {
	for n := parent.FirstChild(); n != nil; {
		next := n.NextSibling()
		switch v := n.(type) {
		case *ast.Blockquote, *ast.List, *east.DefinitionList:
			w.depth++
			w.visit(n)
			w.depth--
			if w.depth == maxNesting {
				w.flatten(parent, n)
			}
		case *ast.FencedCodeBlock:
			// A top-level fence goes to codeFrame. Any other reaches Chroma through
			// glamour, which takes the language from Info. Language would cache it.
			if parent.Kind() != ast.KindDocument && v.Info != nil {
				label, _, _ := strings.Cut(string(v.Info.Segment.Value(w.src)), " ")
				if !canHighlight(label) {
					v.Info = nil
				}
			}
		case *ast.CodeBlock:
			// glamour prints these without decoding anything.
		case *ast.CodeSpan:
			w.literalChildren(v)
		case *ast.Text:
			w.cleanText(parent, v)
		case *ast.Image:
			w.image(parent, v)
		case *ast.AutoLink:
			w.autoLink(parent, v)
		case *ast.Link:
			w.visit(v)
			w.link(parent, v)
		case *ast.RawHTML:
			if hostile(string(v.Segments.Value(w.src))) {
				parent.RemoveChild(parent, v)
			}
		case *ast.HTMLBlock:
			if hostile(blockText(v, w.src)) {
				parent.RemoveChild(parent, v)
			}
		default:
			w.visit(n)
		}
		n = next
	}
}

// hostile reports whether text would gain forbidden characters when its character
// references are decoded.
func hostile(raw string) bool {
	if !strings.Contains(raw, "&") {
		return false
	}
	decoded := html.UnescapeString(raw)
	return security.Clean(decoded) != decoded
}

func blockText(n *ast.HTMLBlock, src []byte) string {
	text := blockLines(n, src)
	if n.HasClosure() {
		text += string(n.ClosureLine.Value(src))
	}
	return text
}

func blockLines(n ast.Node, src []byte) string {
	var b strings.Builder
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return b.String()
}

// newText appends s to the source as a text node that displays exactly s, because
// glamour decodes every text node once.
func (w *rewriter) newText(s string) *ast.Text {
	start := len(w.src)
	w.src = append(w.src, strings.ReplaceAll(s, "&", "&amp;")...)
	return ast.NewTextSegment(text.NewSegment(start, len(w.src)))
}

func (w *rewriter) replaceText(parent ast.Node, old *ast.Text, s string) {
	t := w.newText(s)
	t.SetSoftLineBreak(old.SoftLineBreak())
	t.SetHardLineBreak(old.HardLineBreak())
	parent.ReplaceChild(parent, old, t)
}

// cleanText removes what character references would decode to and drops the
// backslash of a Markdown escape, which glamour would otherwise show.
func (w *rewriter) cleanText(parent ast.Node, t *ast.Text) {
	raw := string(t.Segment.Value(w.src))
	if !hostile(raw) && !strings.Contains(raw, `\`) {
		return
	}
	w.replaceText(parent, t, shownText(raw))
}

// shownText is raw Markdown text as a reader should see it.
func shownText(raw string) string {
	return security.Clean(html.UnescapeString(string(util.UnescapePunctuations([]byte(raw)))))
}

// flatten replaces a block with a paragraph of its words, one row per line.
func (w *rewriter) flatten(parent, n ast.Node) {
	p := ast.NewParagraph()
	for line := range strings.Lines(shownText(plainText(n, w.src))) {
		if line = strings.TrimSpace(line); line != "" {
			t := w.newText(line)
			t.SetHardLineBreak(true)
			p.AppendChild(p, t)
		}
	}
	parent.ReplaceChild(parent, n, p)
}

// literalChildren makes a code span show its text verbatim.
func (w *rewriter) literalChildren(span ast.Node) {
	for n := span.FirstChild(); n != nil; {
		next := n.NextSibling()
		if t, ok := n.(*ast.Text); ok {
			if value := string(t.Segment.Value(w.src)); strings.Contains(value, "&") {
				w.replaceText(span, t, value)
			}
		}
		n = next
	}
}

// styled returns a link node with no target, which glamour draws in the link
// style.
func (w *rewriter) styled(s string) *ast.Link {
	l := ast.NewLink()
	l.Destination = []byte(noTarget)
	l.AppendChild(l, w.newText(s))
	return l
}

// link shows the target in full after the text, or alone when the text is empty
// or repeats it.
func (w *rewriter) link(parent ast.Node, n *ast.Link) {
	target := destination(n.Destination)
	label := security.Clean(html.UnescapeString(plainText(n, w.src)))
	n.Destination = []byte(noTarget)
	n.Title = nil
	switch {
	case target == "":
	case label == "" || label == target:
		parent.ReplaceChild(parent, n, w.styled(target))
	default:
		w.insertAfter(parent, n, w.newText(" ("), w.styled(target), w.newText(")"))
	}
}

func (w *rewriter) autoLink(parent ast.Node, n *ast.AutoLink) {
	shown := n.URL(w.src)
	if n.AutoLinkType == ast.AutoLinkEmail {
		shown = n.Label(w.src)
	}
	parent.ReplaceChild(parent, n, w.styled(destination(shown)))
}

func (w *rewriter) image(parent ast.Node, n *ast.Image) {
	alt := security.Clean(html.UnescapeString(plainText(n, w.src)))
	target := destination(n.Destination)
	lead := "Image"
	if alt != "" {
		lead = "Image: " + alt
	}
	if target == "" {
		parent.ReplaceChild(parent, n, w.newText(lead))
		return
	}
	first := w.newText(lead + " (")
	parent.ReplaceChild(parent, n, first)
	w.insertAfter(parent, first, w.styled(target), w.newText(")"))
}

func (w *rewriter) insertAfter(parent, after ast.Node, nodes ...ast.Node) {
	for _, n := range nodes {
		parent.InsertAfter(parent, after, n)
		after = n
	}
}

// destination decodes a link target by the CommonMark rules, under which a
// reference needs its semicolon, so that ?a=1&copy=2 survives intact.
func destination(raw []byte) string {
	decoded := util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(raw)))
	return security.Clean(string(decoded))
}

// plainText returns the text under n with no styling, one line per block.
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(src))
				if t.SoftLineBreak() || t.HardLineBreak() {
					b.WriteByte(10)
				}
				continue
			}
			walk(c)
			if c.Type() == ast.TypeBlock {
				// A code or HTML block keeps its text in Lines, with no children.
				if c.FirstChild() == nil {
					b.WriteString(blockLines(c, src))
				}
				b.WriteByte(10)
			}
		}
	}
	walk(n)
	return b.String()
}
