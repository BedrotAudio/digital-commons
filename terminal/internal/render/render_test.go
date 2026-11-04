package render

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/theme"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

var dark = theme.New(theme.NameDark)
var light = theme.New(theme.NameLight)

func plain(out Output) string {
	return ansi.Strip(strings.Join(out.Lines, "\n"))
}

var sgrOnly = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// assertSafe fails if anything but SGR styling reached the output.
func assertSafe(t *testing.T, label string, out Output) {
	t.Helper()
	joined := strings.Join(out.Lines, "\n")
	rest := sgrOnly.ReplaceAllString(joined, "")
	for _, r := range rest {
		switch {
		case r == '\n':
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			t.Fatalf("%s: control %#x in %q", label, r, rest)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			t.Fatalf("%s: bidi control %#x in %q", label, r, rest)
		case r == 0x200b || r == 0x2060 || r == 0xfeff:
			t.Fatalf("%s: invisible %#x in %q", label, r, rest)
		}
	}
}

func assertWidth(t *testing.T, label string, out Output, width int) {
	t.Helper()
	for i, line := range out.Lines {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("%s: line %d is %d wide, limit %d: %q", label, i, w, width, ansi.Strip(line))
		}
	}
}

var hostileInputs = map[string]string{
	"clear":        "\x1b[2J",
	"osc52":        "\x1b]52;c;aGVsbG8=\x07",
	"osc52 st":     "\x1b]52;c;aGVsbG8=\x1b\\",
	"osc8":         "\x1b]8;;http://evil.gov\x1b\\hidden\x1b]8;;\x1b\\",
	"osc0":         "\x1b]0;owned\x07",
	"c1 csi":       "\u009b2J",
	"c1 osc":       "\u009d0;x\u009c",
	"rlo":          "\u202e",
	"cr":           "\r",
	"bs":           "\x08\x08",
	"bel":          "\x07",
	"sgr":          "\x1b[31m",
	"unterminated": "\x1b]52;c;",
}

// Each hostile value is dropped into every construct that glamour treats
// differently.
var contexts = map[string]string{
	"prose":      "before %s after",
	"bold":       "**before %s after**",
	"span":       "use `before %s after` here",
	"fence":      "```go\nbefore %s after\n```",
	"open fence": "```go\nbefore %s after",
	"nested":     "- item\n\n  ```\n  before %s after\n  ```\n",
	"indented":   "    before %s after\n",
	"heading":    "# before %s after",
	"quote":      "> before %s after",
	"list":       "- before %s after",
	"table":      "| a |\n|---|\n| before %s after |",
	"link text":  "[before %s after](http://x.gov)",
	"link url":   "[text](http://x.gov/%s)",
	"image":      "![before %s after](http://x.gov/i.png)",
	"strike":     "~~before %s after~~",
	"html":       "<div>before %s after</div>",
	"language":   "```go%s\ncode\n```",
}

func TestHostileStringsNeverLeak(t *testing.T) {
	r := New(60, dark)
	for hn, h := range hostileInputs {
		for cn, c := range contexts {
			md := strings.ReplaceAll(c, "%s", h)
			assertSafe(t, cn+"/"+hn, r.Render(md))
		}
	}
}

// Glamour decodes character references after Clean has run, so these are the
// strings that would become real escape sequences if the renderer trusted it.
func TestCharacterReferencesNeverBecomeEscapes(t *testing.T) {
	refs := []string{
		"&#27;[2J", "&#x1b;[2J", "&#X1B;[2J", "&#27[2J", "&#27;]52;c;aGVsbG8=&#7;",
		"&#27;]8;;http://evil.gov&#27;\\", "&#27;[31m", "&#27;[>4;2m", "&#155;2J", "&#8238;",
		"&#13;", "&#8;", "&#7;", "&#x202E;", "&#x2066;", "&#xFEFF;", "&#8203;", "&ZeroWidthSpace;",
	}
	r := New(60, dark)
	for _, ref := range refs {
		for cn, c := range contexts {
			md := strings.ReplaceAll(c, "%s", ref)
			out := r.Render(md)
			assertSafe(t, cn+"/"+ref, out)
			if joined := strings.Join(out.Lines, "\n"); strings.Contains(joined, "\x1b[31m") || strings.Contains(joined, "\x1b[>") {
				t.Fatalf("%s/%s: reference became a sequence: %q", cn, ref, joined)
			}
		}
	}
}

func TestReferencesAreLiteralInCode(t *testing.T) {
	cases := []struct{ name, md, want string }{
		{"span", "see `&#27;[2J` and `a &amp; b`", "&#27;[2J"},
		{"span entity", "see `a &amp; b`", "a &amp; b"},
		{"fence", "```\n&#27;[2J\n```", "&#27;[2J"},
	}
	r := New(60, dark)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := plain(r.Render(c.md)); !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEntitiesInProse(t *testing.T) {
	got := plain(New(60, dark).Render("AT&amp;T &copy; 2026 \\*literal\\* C:\\temp"))
	for _, want := range []string{"AT&T", "\u00a9 2026", "*literal*", `C:\temp`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestStreamedSplitSequenceIsClean(t *testing.T) {
	// The UI cleans accumulated text, so a split survives only as harmless text.
	first, second := "hello \x1b", "[2Jworld"
	out := New(40, dark).Render(first + second)
	assertSafe(t, "split", out)
	if got := plain(out); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestLinks(t *testing.T) {
	r := New(100, dark)
	cases := []struct{ name, md, want string }{
		{"inline", "[policy](https://city.gov/a?x=1&y=2)", "policy (https://city.gov/a?x=1&y=2)"},
		{"amp entity", "[policy](https://city.gov/a?x=1&amp;y=2)", "policy (https://city.gov/a?x=1&y=2)"},
		{"not an entity", "[policy](https://city.gov/a?x=1&copy=2)", "policy (https://city.gov/a?x=1&copy=2)"},
		{"reference", "[policy][r]\n\n[r]: https://city.gov/r", "policy (https://city.gov/r)"},
		{"autolink", "<https://city.gov/auto>", "https://city.gov/auto"},
		{"bare", "see https://city.gov/bare now", "see https://city.gov/bare now"},
		{"www", "see www.city.gov now", "see http://www.city.gov now"},
		{"email", "<clerk@city.gov>", "clerk@city.gov"},
		{"text is url", "[https://city.gov/](https://city.gov/)", "https://city.gov/"},
		{"empty text", "[](https://city.gov/x)", "https://city.gov/x"},
		{"no target", "[text]()", "text"},
		{"fragment only", "[text](#section)", "text (#section)"},
		{"unparsable url", "[text](http://a%zz/)", "text (http://a%zz/)"},
		{"mailto", "[mail](mailto:a@b.gov)", "mail (mailto:a@b.gov)"},
		{"bold text", "[**bold**](https://city.gov)", "bold (https://city.gov)"},
		{"in table", "| a |\n|---|\n| [x](https://city.gov/t) |", "x (https://city.gov/t)"},
		{"image", "![a chart](https://city.gov/c.png)", "Image: a chart (https://city.gov/c.png)"},
		{"image no alt", "![](https://city.gov/c.png)", "Image (https://city.gov/c.png)"},
		{"image link", "[![alt](https://city.gov/i.png)](https://city.gov/page)", "Image: alt (https://city.gov/i.png) (https://city.gov/page)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := r.Render(c.md)
			if got := plain(out); !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			joined := strings.Join(out.Lines, "\n")
			if strings.Contains(joined, "\x1b]") {
				t.Fatalf("OSC in %q", joined)
			}
		})
	}
}

func TestLongURLIsWrappedNeverShortened(t *testing.T) {
	url := "https://records.city.gov/" + strings.Repeat("segment-", 12) + "end?token=abcdefghijklmnopqrstuvwxyz"
	for _, width := range []int{20, 40, 78} {
		out := New(width, dark).Render("Read [this](" + url + ") now.")
		assertWidth(t, "url", out, width)
		var joined strings.Builder
		for _, line := range out.Lines {
			joined.WriteString(strings.TrimSpace(ansi.Strip(line)))
		}
		if !strings.Contains(joined.String(), url) {
			t.Fatalf("width %d: url not intact in %q", width, joined.String())
		}
	}
}

var widthCorpus = map[string]string{
	"paragraph":  strings.Repeat("The quick brown fox jumps over the lazy dog. ", 10),
	"long word":  strings.Repeat("x", 400),
	"long url":   "[link](https://city.gov/" + strings.Repeat("abc/", 60) + ")",
	"bare url":   "https://city.gov/" + strings.Repeat("a", 300),
	"heading":    "# " + strings.Repeat("heading ", 40),
	"long head":  "## " + strings.Repeat("h", 300),
	"quote":      "> " + strings.Repeat("quoted text ", 40),
	"deep quote": "> > > " + strings.Repeat("quoted text ", 40),
	"list":       "- " + strings.Repeat("item text ", 40) + "\n  - " + strings.Repeat("nested ", 40) + "\n    - " + strings.Repeat("y", 200),
	"numbered":   "1. " + strings.Repeat("item text ", 40),
	"wide table": "| " + strings.Repeat("column header | ", 12) + "\n|" + strings.Repeat("---|", 12) + "\n| " + strings.Repeat("some cell value | ", 12),
	"long cell":  "| a | b |\n|---|---|\n| " + strings.Repeat("w", 200) + " | [l](https://city.gov/" + strings.Repeat("u", 200) + ") |",
	"code line":  "```python\n" + strings.Repeat("value = compute(argument) + ", 12) + "\n```",
	"code word":  "```\n" + strings.Repeat("z", 400) + "\n```",
	"code wide":  "```\n" + strings.Repeat("\u65e5\u672c\u8a9e", 60) + "\n```",
	"code tabs":  "```go\n\t\t\t\t\t\t\t\t\t\t\t\tx := 1\n```",
	"long lang":  "```" + strings.Repeat("l", 200) + "\ncode\n```",
	"nested":     "- item\n\n  ```go\n  " + strings.Repeat("nested(code); ", 20) + "\n  ```\n",
	"indented":   "    " + strings.Repeat("indented code ", 20),
	"cjk":        strings.Repeat("\u65e5\u672c\u8a9e\u306e\u6587\u7ae0\u3067\u3059\u3002", 30),
	"emoji":      strings.Repeat("\U0001F600 ", 100),
	"tabs":       "a\tb\tc\t" + strings.Repeat("d\t", 60),
	"span":       "`" + strings.Repeat("c", 300) + "`",
	"strike":     "~~" + strings.Repeat("gone ", 60) + "~~",
	"definition": "Term\n: " + strings.Repeat("definition ", 40),
	"task":       "- [x] " + strings.Repeat("task ", 50),
	"rule":       "---",
	"image":      "![" + strings.Repeat("alt ", 40) + "](https://city.gov/" + strings.Repeat("i", 200) + ".png)",
	"html":       "<div>" + strings.Repeat("html ", 80) + "</div>",
}

func TestWidthLimits(t *testing.T) {
	for _, width := range []int{20, 40, 78, 120} {
		for _, th := range []theme.Theme{dark, light} {
			r := New(width, th)
			for name, md := range widthCorpus {
				t.Run(fmt.Sprintf("%d/%s/%s", width, th.Name, name), func(t *testing.T) {
					out := r.Render(md)
					if len(out.Lines) == 0 {
						t.Fatal("no output")
					}
					assertWidth(t, name, out, width)
					assertSafe(t, name, out)
				})
			}
		}
	}
}

func TestVeryNarrowWidths(t *testing.T) {
	for _, width := range []int{1, 2, 3, 5, 7, 8, 9, 10} {
		r := New(width, dark)
		for name, md := range widthCorpus {
			assertWidth(t, fmt.Sprintf("%d/%s", width, name), r.Render(md), width)
		}
		out := r.RenderFrom("```go\ncode\n```", 123456)
		assertWidth(t, "number", out, width)
		if len(out.CodeBlocks) != 1 {
			t.Fatalf("width %d: %d blocks", width, len(out.CodeBlocks))
		}
	}
}

func TestCodeBlocks(t *testing.T) {
	md := "Intro text.\n\n```go\nfunc a() {}\n\n// tail\n```\n\nMiddle.\n\n```\nplain\n```\n\n~~~python title\nprint(1)\n~~~\n\nEnd."
	out := New(40, dark).Render(md)
	if len(out.CodeBlocks) != 3 {
		t.Fatalf("%+v", out.CodeBlocks)
	}
	want := []CodeBlock{
		{Number: 1, Language: "go", Text: "func a() {}\n\n// tail"},
		{Number: 2, Language: "", Text: "plain"},
		{Number: 3, Language: "python", Text: "print(1)"},
	}
	for i, w := range want {
		got := out.CodeBlocks[i]
		if got.Number != w.Number || got.Language != w.Language || got.Text != w.Text {
			t.Errorf("block %d: %+v", i, got)
		}
		label := ansi.Strip(out.Lines[got.LabelLine])
		mark := fmt.Sprintf("[%d]", w.Number)
		if !strings.HasPrefix(label, "\u256d\u2500 "+mark) || !strings.HasSuffix(label, "\u256e") {
			t.Errorf("block %d: label %q", i, label)
		}
		if w.Language != "" && !strings.Contains(label, w.Language) {
			t.Errorf("block %d: no language in %q", i, label)
		}
		if lipgloss.Width(out.Lines[got.LabelLine]) != 40 {
			t.Errorf("block %d: label is %d wide", i, lipgloss.Width(out.Lines[got.LabelLine]))
		}
	}
	got := plain(out)
	for _, want := range []string{"Intro text.", "Middle.", "End.", "\u2502 func a() {}", "\u2570"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Index(got, "Intro") > strings.Index(got, "func a") || strings.Index(got, "func a") > strings.Index(got, "Middle") {
		t.Errorf("order lost:\n%s", got)
	}
}

func TestEveryFrameRowIsFullWidth(t *testing.T) {
	out := New(30, dark).Render("```go\nshort\n\n" + strings.Repeat("long ", 20) + "\n\u65e5\u672c\u8a9e\n```")
	for i, line := range out.Lines {
		if w := lipgloss.Width(line); w != 30 {
			t.Errorf("row %d is %d wide: %q", i, w, ansi.Strip(line))
		}
	}
}

func TestRenderFromNumbersOnward(t *testing.T) {
	out := New(40, dark).RenderFrom("```\na\n```\n\n```\nb\n```", 7)
	if out.CodeBlocks[0].Number != 7 || out.CodeBlocks[1].Number != 8 {
		t.Fatalf("%+v", out.CodeBlocks)
	}
	if !strings.Contains(plain(out), "[8]") {
		t.Fatal(plain(out))
	}
}

func TestNestedFenceStaysInProse(t *testing.T) {
	out := New(40, dark).Render("- item\n\n  ```go\n  x := 1\n  ```\n\n> ```\n> quoted\n> ```\n")
	if len(out.CodeBlocks) != 0 {
		t.Fatalf("%+v", out.CodeBlocks)
	}
	got := plain(out)
	if !strings.Contains(got, "x := 1") || !strings.Contains(got, "quoted") {
		t.Fatal(got)
	}
}

func TestHighlightUsesThemeColors(t *testing.T) {
	md := "```go\nfunc main() { return \"s\" } // c\n```"
	sgr := func(hex string) string {
		var r, g, b int
		fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}
	for _, th := range []theme.Theme{dark, light} {
		joined := strings.Join(New(60, th).Render(md).Lines, "\n")
		for name, hex := range map[string]string{"keyword": th.Tokens.Accent, "string": th.Tokens.Warn, "comment": th.Tokens.TextMuted, "frame": th.Tokens.Border} {
			if !strings.Contains(joined, sgr(hex)) {
				t.Errorf("%s: %s color %s missing from %q", th.Name, name, hex, joined)
			}
		}
	}
}

// glamour's Chroma option registers one global style, so nested fences must use
// their own per-theme style or the second theme would show the first's colors.
func TestNestedFenceColorsFollowTheTheme(t *testing.T) {
	md := "- item\n\n  ```go\n  func main() {}\n  ```\n"
	for _, th := range []theme.Theme{dark, light, dark} {
		joined := strings.Join(New(60, th).Render(md).Lines, "\n")
		var r, g, b int
		fmt.Sscanf(th.Tokens.Accent, "#%02x%02x%02x", &r, &g, &b)
		if !strings.Contains(joined, fmt.Sprintf("38;2;%d;%d;%d", r, g, b)) {
			t.Errorf("%s: keyword color missing from %q", th.Name, joined)
		}
	}
}

func TestPartialMarkdownEveryPrefix(t *testing.T) {
	doc := "# Heading\n\nSome **bold *mixed* text** and `code` and [a link](https://city.gov/page).\n\n" +
		"- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
		"> quote\n\n![img](https://city.gov/i.png)\n\n~~~\nsecond\n"
	r := New(40, dark)
	for i := range len(doc) + 1 {
		out := r.Render(doc[:i])
		assertWidth(t, fmt.Sprintf("prefix %d", i), out, 40)
		assertSafe(t, fmt.Sprintf("prefix %d", i), out)
	}
}

func TestUnclosedFence(t *testing.T) {
	r := New(40, dark)
	cases := []struct {
		name, md string
		blocks   int
		text     string
	}{
		{"open with code", "Intro\n\n```go\nfunc main() {", 1, "func main() {"},
		{"open with nothing", "```go\n", 1, ""},
		{"fence only", "```", 1, ""},
		{"language being typed", "```pyth", 1, ""},
		{"two backticks", "``", 0, ""},
		{"closing being typed", "```\ncode\n``", 1, "code\n``"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := r.Render(c.md)
			if len(out.CodeBlocks) != c.blocks {
				t.Fatalf("%+v", out)
			}
			if c.blocks == 1 && out.CodeBlocks[0].Text != c.text {
				t.Fatalf("text %q", out.CodeBlocks[0].Text)
			}
		})
	}
}

func TestEmptyInput(t *testing.T) {
	out := New(40, dark).Render("")
	if len(out.Lines) != 0 || len(out.CodeBlocks) != 0 {
		t.Fatalf("%+v", out)
	}
	if out := New(40, dark).Render("   \n\n  \n"); len(out.Lines) != 0 {
		t.Fatalf("%q", out.Lines)
	}
}

func TestNoRepeatedBlankLines(t *testing.T) {
	out := New(40, dark).Render("- a\n- b\n\n1. c\n\nText\n\n```\ncode\n```\n\n\n\nEnd")
	for i := 1; i < len(out.Lines); i++ {
		if blank(out.Lines[i]) && blank(out.Lines[i-1]) {
			t.Fatalf("two blank lines at %d: %q", i, out.Lines)
		}
	}
	if blank(out.Lines[0]) || blank(out.Lines[len(out.Lines)-1]) {
		t.Fatalf("outer blank lines: %q", out.Lines)
	}
}

func TestRenderIsRepeatable(t *testing.T) {
	r := New(40, dark)
	md := "# T\n\n[a](http://b.gov) `c`\n\n```go\nx\n```"
	first := strings.Join(r.Render(md).Lines, "\n")
	for range 3 {
		if got := strings.Join(r.Render(md).Lines, "\n"); got != first {
			t.Fatalf("changed: %q vs %q", got, first)
		}
	}
}

func TestGlyphsStayWithinTheSpec(t *testing.T) {
	allowed := "\u25cf\u25cb\u25c6\u25b8\u25be\u2502\u2500\u256d\u256e\u2570\u256f\u25a0+\u00d7\u00b7"
	md := "# H\n\n- a\n- [x] b\n\n1. c\n\n> q\n\n---\n\n| a |\n|---|\n| b |\n\nTerm\n: def\n\n```\nx\n```"
	for _, r := range plain(New(40, dark).Render(md)) {
		if r > 0x7e && !strings.ContainsRune(allowed, r) {
			t.Errorf("glyph %q is not in the spec's list", r)
		}
	}
}

func TestAccessors(t *testing.T) {
	r := New(0, light)
	if r.Width() != 1 || r.ThemeName() != theme.NameLight {
		t.Fatalf("%d %s", r.Width(), r.ThemeName())
	}
}

func TestScrubKeepsOnlySGR(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"sgr", "\x1b[38;2;1;2;3mx\x1b[m", "\x1b[38;2;1;2;3mx\x1b[m"},
		{"colon sgr", "\x1b[4:3mx", "\x1b[4:3mx"},
		{"private m", "\x1b[>4;2mx", "x"},
		{"erase", "\x1b[2Jx", "x"},
		{"cursor", "\x1b[5;5Hx", "x"},
		{"osc8", "\x1b]8;;u\x1b\\x\x1b]8;;\x1b\\", "x"},
		{"osc52", "\x1b]52;c;QQ==\x07x", "x"},
		{"bel", "a\x07b", "ab"},
		{"cr", "a\rb", "ab"},
		{"tab", "a\tb", "a    b"},
		{"c1", "a\u009bb", "ab"},
		{"bidi", "a\u202eb", "ab"},
		{"keeps wide", "\u65e5\u672c", "\u65e5\u672c"},
		{"keeps zwj emoji", "\U0001F468\u200d\U0001F469", "\U0001F468\u200d\U0001F469"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scrub(c.in); got != c.want {
				t.Fatalf("scrub(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPaintFillsEveryCell(t *testing.T) {
	r := New(30, dark)
	out := r.Render("# Title\n\nSome **bold** text with `code`.\n\n```go\nfunc x() {}\n```")
	content := strings.Join(out.Lines, "\n")
	height := len(out.Lines) + 2

	painted := Paint(content, 30, height, dark.Colors.Bg, dark.Colors.Text, colorprofile.TrueColor)

	canvas := lipgloss.NewCanvas(30, height)
	canvas.Compose(lipgloss.NewLayer(painted))
	for y := range height {
		for x := range 30 {
			cell := canvas.CellAt(x, y)
			if cell == nil || cell.IsZero() {
				continue // the second half of a wide character
			}
			if cell.Style.Bg == nil {
				t.Fatalf("cell %d,%d has no background", x, y)
			}
			if cell.Style.Fg == nil {
				t.Fatalf("cell %d,%d has no foreground", x, y)
			}
		}
	}
	if !strings.Contains(ansi.Strip(painted), "Title") {
		t.Fatalf("text lost: %q", ansi.Strip(painted))
	}
}

func TestPaintKeepsExplicitColors(t *testing.T) {
	content := lipgloss.NewStyle().Foreground(lipgloss.Color("#112233")).Background(lipgloss.Color("#445566")).Render("x")
	painted := Paint(content+" ", 4, 1, dark.Colors.Bg, dark.Colors.Text, colorprofile.TrueColor)

	canvas := lipgloss.NewCanvas(4, 1)
	canvas.Compose(lipgloss.NewLayer(painted))
	same := func(a, b color.Color) bool {
		ar, ag, ab, _ := a.RGBA()
		br, bg, bb, _ := b.RGBA()
		return ar == br && ag == bg && ab == bb
	}
	if c := canvas.CellAt(0, 0); !same(c.Style.Bg, lipgloss.Color("#445566")) || !same(c.Style.Fg, lipgloss.Color("#112233")) {
		t.Errorf("explicit colors replaced: %+v", c.Style)
	}
	if c := canvas.CellAt(3, 0); !same(c.Style.Bg, dark.Colors.Bg) {
		t.Errorf("blank cell not painted: %+v", c.Style)
	}
}

func TestPaintSkippedWithoutColor(t *testing.T) {
	for _, p := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.NoTTY, colorprofile.Unknown} {
		if got := Paint("text", 10, 2, dark.Colors.Bg, dark.Colors.Text, p); got != "text" {
			t.Errorf("%v: %q", p, got)
		}
	}
	for _, p := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor} {
		if got := Paint("text", 10, 2, dark.Colors.Bg, dark.Colors.Text, p); got == "text" {
			t.Errorf("%v: not painted", p)
		}
	}
	if got := Paint("text", 0, 0, dark.Colors.Bg, dark.Colors.Text, colorprofile.TrueColor); got != "text" {
		t.Errorf("zero region: %q", got)
	}
}

func TestCloseOpen(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"**This is", "**This is**"},
		{"**This is ", "**This is**"},
		{"**This is*", "**This is**"},
		{"**done** and more", "**done** and more"},
		{"run `go te", "run `go te`"},
		{"**first**\n\nsecond **par", "**first**\n\nsecond **par**"},
		{"```go\nx := `a", "```go\nx := `a"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CloseOpen(c.in); got != c.want {
			t.Errorf("CloseOpen(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestListLinesHang(t *testing.T) {
	r := New(40, theme.New(theme.NameDark))
	text := "- " + strings.Repeat("word ", 20) + "\n\n1. " + strings.Repeat("word ", 20)
	var rows []string
	for _, line := range r.Render(text).Lines {
		rows = append(rows, ansi.Strip(line))
	}
	for i, row := range rows {
		if width := ansi.StringWidth(row); width > 40 {
			t.Errorf("row %d is %d wide: %q", i, width, row)
		}
		if row != "" && !strings.HasPrefix(row, "- ") && !strings.HasPrefix(row, "1. ") && !strings.HasPrefix(row, "  ") {
			t.Errorf("row %d does not hang: %q", i, row)
		}
	}
	if len(rows) < 6 {
		t.Fatalf("expected wrapped lists, got %q", rows)
	}
}

// finishes runs a render with a time limit, because the failures this guards
// against never return. The goroutine of a failed run is left behind on purpose.
func finishes(t *testing.T, label string, render func() Output) Output {
	t.Helper()
	done := make(chan Output, 1)
	go func() { done <- render() }()
	select {
	case out := <-done:
		return out
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish", label)
		return Output{}
	}
}

// Chroma's Jungle lexer never returns on most text. Its rules push and pop a state
// without reading anything, so Chroma's own regexp timeout does not apply, and the
// event loop would stop with no way to quit.
func TestLexerThatNeverReturnsIsNotUsed(t *testing.T) {
	for _, md := range []string{
		"```jungle\n\"\n```",
		"```jungle\n- item\n```",
		"```jungle\n\"",
		"```Jungle\nlib foo : bar ;\n```",
		"```JUNGLE\n-\n```",
		"```monkey.jungle\n\"\n```",
		"```jungle extra words\n\"\n```",
		"- a list\n\n  ```jungle\n  \"\n  ```\n",
		"> a quote\n>\n> ```jungle\n> \"\n> ```\n",
		"1. one\n   > quoted\n   >\n   > ```jungle\n   > - item\n   > ```\n",
	} {
		finishes(t, md, func() Output { return New(60, dark).Render(md) })
	}
	// The block keeps its label and its text, and a nested fence of any other
	// language still reaches the highlighter.
	out := New(60, dark).Render("```jungle\n\"\n```")
	if len(out.CodeBlocks) != 1 || out.CodeBlocks[0].Language != "jungle" || out.CodeBlocks[0].Text != `"` {
		t.Errorf("code blocks: %+v", out.CodeBlocks)
	}
	if text := plain(New(60, dark).Render("- a\n\n  ```jungle\n  \"\n  ```\n")); !strings.Contains(text, `"`) {
		t.Errorf("a nested fence lost its code: %q", text)
	}
	if text := plain(New(60, dark).Render("- a\n\n  ```go\n  x := 1\n  ```\n")); !strings.Contains(text, "x := 1") {
		t.Errorf("a nested fence lost its code: %q", text)
	}
}

// An upgrade to Chroma could add another lexer like Jungle. Every other lexer must
// finish on short text, which is what a hostile reply would use.
func TestEveryLexerFinishes(t *testing.T) {
	bodies := []string{
		"", "x", "\"", "'", "-", "- item", "#", "//", "/*", "<", ">", "(", ")", "{", "}", "[", "]", "$", "%", "\\",
		"=", ":", ";", ",", ".", "!", "?", "@", "&", "*", "+", "|", "~", "^", "`", " ", "\n", "\t", "é", "x\ny\n",
		"lib foo : bar ;", "a = b\n", "<a b=\"c\">", "if x then y", "\"\"\"\n", "\\n",
	}
	for _, lexer := range lexers.GlobalLexerRegistry.Lexers {
		name := lexer.Config().Name
		if hangingLexers[name] {
			continue
		}
		wrapped := chroma.Coalesce(lexer)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, body := range bodies {
				tokens, err := wrapped.Tokenise(nil, body)
				if err != nil {
					continue
				}
				for token := tokens(); token != chroma.EOF; token = tokens() {
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("lexer %q did not finish. Add it to hangingLexers.", name)
		}
	}
}

// Past the nesting limit a block loses its indentation and keeps its words. Glamour
// repeats its work at every level, so without the limit a line of 80 > characters
// took longer than anyone would wait, and memory grew past a gigabyte.
func TestDeepNestingFinishesAndKeepsTheText(t *testing.T) {
	for name, md := range map[string]string{
		"quote":        strings.Repeat(">", 120) + " KEPT",
		"quote spaced": strings.Repeat("> ", 500) + "KEPT",
		"long quote":   strings.Repeat(">", 2000) + " KEPT",
		"list":         strings.Repeat("- ", 1000) + "KEPT",
		"numbered":     strings.Repeat("1. ", 700) + "KEPT",
		"mixed":        strings.Repeat("> - ", 200) + "KEPT",
		"code":         strings.Repeat("> ", 100) + "```\n" + strings.Repeat("> ", 100) + "KEPT\n",
		"link":         strings.Repeat("> ", 100) + "<https://example.gov/KEPT>",
		"after":        strings.Repeat(">", 120) + " first\n\nKEPT",
		"twice":        strings.Repeat(">", 120) + " one\n\n" + strings.Repeat(">", 120) + " KEPT\n",
	} {
		for _, width := range []int{8, 24, 78, 118} {
			label := fmt.Sprintf("%s at width %d", name, width)
			out := finishes(t, label, func() Output { return New(width, dark).Render(md) })
			// At width 8 the indentation leaves no room for a word in one piece.
			if width > 8 && !strings.Contains(plain(out), "KEPT") {
				t.Errorf("%s lost its text: %q", label, plain(out))
			}
			assertWidth(t, label, out, width)
			assertSafe(t, label, out)
		}
	}
}

// What is flattened has been through the same cleaning as any other text.
func TestFlattenedTextIsClean(t *testing.T) {
	for label, hostile := range hostileInputs {
		out := finishes(t, label, func() Output {
			return New(60, dark).Render(strings.Repeat("> ", 40) + "a" + hostile + "b &#27;[2J")
		})
		assertSafe(t, label, out)
	}
}

// Nesting a real reply could use is left alone.
func TestOrdinaryNestingKeepsItsShape(t *testing.T) {
	var md strings.Builder
	for level := range maxNesting {
		md.WriteString(strings.Repeat("  ", level) + "- level\n")
	}
	lines := New(100, dark).Render(md.String()).Lines
	if len(lines) != maxNesting {
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
	last := ansi.Strip(lines[len(lines)-1])
	if indent := len(last) - len(strings.TrimLeft(last, " ")); indent == 0 {
		t.Errorf("the deepest level lost its indentation: %q", last)
	}
}
