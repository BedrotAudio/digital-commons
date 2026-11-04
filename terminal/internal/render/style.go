package render

import (
	"strings"

	"digital-commons/terminal/internal/theme"

	glamouransi "charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

func str(s string) *string { return &s }
func yes() *bool           { b := true; return &b }
func num(n uint) *uint     { return &n }

// styleConfig builds glamour's style from theme tokens. Headings stay modest,
// list markers use plain ASCII, and the document color stays unset so that Paint
// supplies it.
func styleConfig(t theme.Theme, width int) glamouransi.StyleConfig {
	tk := t.Tokens
	heading := func(color string) glamouransi.StyleBlock {
		return glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
			Color: str(color), Bold: yes(), BlockSuffix: "\n",
		}}
	}
	return glamouransi.StyleConfig{
		Document:  glamouransi.StyleBlock{Margin: num(0)},
		Paragraph: glamouransi.StyleBlock{Margin: num(0)},
		BlockQuote: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{Color: str(tk.TextMuted)},
			Indent:         num(1),
			IndentToken:    str("│ "),
		},
		List:    glamouransi.StyleList{LevelIndent: 2},
		Heading: glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{BlockSuffix: "\n"}},
		H1:      heading(tk.Accent),
		H2:      heading(tk.Text),
		H3:      heading(tk.Text),
		H4:      heading(tk.TextMuted),
		H5:      heading(tk.TextMuted),
		H6:      heading(tk.TextMuted),

		Strikethrough:  glamouransi.StylePrimitive{CrossedOut: yes()},
		Emph:           glamouransi.StylePrimitive{Italic: yes()},
		Strong:         glamouransi.StylePrimitive{Bold: yes()},
		HorizontalRule: glamouransi.StylePrimitive{Color: str(tk.Border), Format: "\n" + strings.Repeat("─", min(width, 40)) + "\n"},

		Item:        glamouransi.StylePrimitive{BlockPrefix: "- "},
		Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Task:        glamouransi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},

		Link:     glamouransi.StylePrimitive{Color: str(tk.Info)},
		LinkText: glamouransi.StylePrimitive{Color: str(tk.Info)},

		Code: glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
			Color: str(tk.Text), BackgroundColor: str(tk.SurfaceRaised),
		}},
		CodeBlock: glamouransi.StyleCodeBlock{
			StyleBlock: glamouransi.StyleBlock{Margin: num(2)},
			Theme:      chromaStyleName(t),
		},
		Table: glamouransi.StyleTable{
			StyleBlock:      glamouransi.StyleBlock{},
			CenterSeparator: str("─"),
			ColumnSeparator: str("│"),
			RowSeparator:    str("─"),
		},
		DefinitionDescription: glamouransi.StylePrimitive{BlockPrefix: "\n  "},
	}
}

func chromaStyleName(t theme.Theme) string { return "digital-commons-" + t.Name }

// Chroma styles are registered once, by name, for the fences that stay inside
// glamour (those nested in lists or quotes). glamour's own StyleCodeBlock.Chroma
// path registers a single global style, which cannot hold two themes.
var chromaStyles = map[string]*chroma.Style{}

func init() {
	for _, name := range []string{theme.NameDark, theme.NameLight} {
		t := theme.New(name)
		s := chroma.MustNewStyle(chromaStyleName(t), chromaEntries(t.Tokens))
		styles.Register(s)
		chromaStyles[name] = s
	}
}

func chromaEntries(tk theme.Tokens) chroma.StyleEntries {
	return chroma.StyleEntries{
		chroma.Text:              tk.Text,
		chroma.Error:             tk.Text,
		chroma.Comment:           "italic " + tk.TextMuted,
		chroma.CommentPreproc:    tk.Accent,
		chroma.Keyword:           tk.Accent,
		chroma.KeywordType:       tk.Info,
		chroma.Name:              tk.Text,
		chroma.NameBuiltin:       tk.Info,
		chroma.NameClass:         tk.Info,
		chroma.NameConstant:      tk.Warn,
		chroma.NameDecorator:     tk.Warn,
		chroma.NameException:     tk.Danger,
		chroma.NameFunction:      tk.Info,
		chroma.NameTag:           tk.Accent,
		chroma.NameAttribute:     tk.Info,
		chroma.Literal:           tk.Warn,
		chroma.LiteralString:     tk.Warn,
		chroma.LiteralNumber:     tk.Warn,
		chroma.Operator:          tk.TextMuted,
		chroma.Punctuation:       tk.TextMuted,
		chroma.GenericDeleted:    tk.Danger,
		chroma.GenericInserted:   tk.Accent,
		chroma.GenericEmph:       "italic",
		chroma.GenericStrong:     "bold",
		chroma.GenericHeading:    "bold " + tk.Text,
		chroma.GenericSubheading: "bold " + tk.TextMuted,
	}
}
