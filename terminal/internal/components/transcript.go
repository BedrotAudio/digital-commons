package components

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/render"
	"digital-commons/terminal/internal/security"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	"charm.land/bubbles/v2/viewport"
	"github.com/charmbracelet/x/ansi"
)

// Message is one entry of the transcript. Text is never trusted: it is cleaned
// again whenever it is rendered or copied.
type Message struct {
	UID         int
	ID          string
	Role        string // user or assistant
	Text        string
	Time        time.Time
	Attachments []string
	Citations   []client.Citation
	Notices     []client.Notice
	Route       *client.Route
	Usage       *client.Usage
	Context     *client.ContextInfo

	Thinking     string // Advanced mode only, held in memory and never stored
	ThinkingOpen bool

	Streaming bool
	Status    string // status.message while waiting
	Waiting   time.Time

	Note     string // a finish note such as Stopped
	Fail     *Failure
	CanRetry bool

	version int
}

// Failure is an inline error. Basic mode shows only the plain text.
type Failure struct {
	Code    string
	Message string
	Detail  string
	NewChat bool
}

// Touch marks the message as changed, so its cached lines are dropped.
func (m *Message) Touch() { m.version++ }

// Target is a clickable part of the transcript in content coordinates.
type Target struct {
	Line, X, W int
	Action     views.Action
	Key        bool // reachable with the keyboard from the conversation
}

type blockKey struct {
	uid, width, first, version int
	theme                      string
	advanced                   bool
}

type block struct {
	lines   []string
	targets []Target
	code    []render.CodeBlock
}

// Transcript draws the messages into a viewport.
type Transcript struct {
	Messages []*Message
	Advanced bool
	Focus    int // highlighted key target, -1 for none

	th       theme.Theme
	outlined bool
	vp       viewport.Model
	width    int
	height   int
	now      func() time.Time
	renderer *render.Renderer
	cache    map[blockKey]*block
	targets  []Target
	code     []render.CodeBlock
	nextUID  int
}

// NewTranscript returns an empty transcript.
func NewTranscript(th theme.Theme, now func() time.Time) *Transcript {
	vp := viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	vp.KeyMap = viewport.KeyMap{}
	vp.MouseWheelEnabled = false
	return &Transcript{th: th, vp: vp, width: 40, height: 10, now: now, cache: map[blockKey]*block{}, Focus: -1}
}

// NewUID returns a stable local id for a new message.
func (t *Transcript) NewUID() int {
	t.nextUID++
	return t.nextUID
}

// Add appends a message.
func (t *Transcript) Add(m *Message) *Message {
	if m.UID == 0 {
		m.UID = t.NewUID()
	}
	t.Messages = append(t.Messages, m)
	return m
}

// Clear empties the transcript and returns to the top.
func (t *Transcript) Clear() {
	t.Messages = nil
	t.Focus = -1
	t.cache = map[blockKey]*block{}
	t.vp.GotoTop()
	t.Rebuild()
}

// SetTheme applies a theme to messages drawn from now on.
func (t *Transcript) SetTheme(th theme.Theme) {
	t.th = th
	t.renderer = nil
}

// SetOutlined draws user bubbles with a border, for a terminal with no color to
// fill them.
func (t *Transcript) SetOutlined(outlined bool) {
	if t.outlined != outlined {
		t.outlined = outlined
		t.cache = map[blockKey]*block{}
	}
}

// SetSize sets the drawn area. Rewrapping is separate, because a resize is
// debounced. Until then lines are clipped by the viewport.
func (t *Transcript) SetSize(width, height int) {
	follow := t.vp.AtBottom()
	t.vp.SetWidth(width)
	t.vp.SetHeight(height)
	t.height = height
	if follow {
		t.vp.GotoBottom()
	}
}

// Wrap sets the width that text is wrapped to.
func (t *Transcript) Wrap(width int) {
	t.width = width
	t.renderer = nil
}

// WrapWidth returns the width text is wrapped to.
func (t *Transcript) WrapWidth() int { return t.width }

// View returns the visible lines.
func (t *Transcript) View() string { return t.vp.View() }

// PageUp and the methods below scroll the viewport.
func (t *Transcript) PageUp()          { t.vp.PageUp() }
func (t *Transcript) PageDown()        { t.vp.PageDown() }
func (t *Transcript) ScrollUp(n int)   { t.vp.ScrollUp(n) }
func (t *Transcript) ScrollDown(n int) { t.vp.ScrollDown(n) }
func (t *Transcript) GotoTop()         { t.vp.GotoTop() }
func (t *Transcript) GotoBottom()      { t.vp.GotoBottom() }
func (t *Transcript) AtBottom() bool   { return t.vp.AtBottom() }

// Hit resolves a click at a position relative to the top-left of the viewport.
func (t *Transcript) Hit(x, y int) (views.Action, bool) {
	line := y + t.vp.YOffset()
	for _, target := range t.targets {
		if target.Line == line && x >= target.X && x < target.X+target.W {
			return target.Action, true
		}
	}
	return views.Action{}, false
}

// KeyTargets returns the actions reachable with the keyboard.
func (t *Transcript) KeyTargets() []Target {
	var out []Target
	for _, target := range t.targets {
		if target.Key {
			out = append(out, target)
		}
	}
	return out
}

// EnsureVisible scrolls to a content line.
func (t *Transcript) EnsureVisible(line int) { t.vp.EnsureVisible(line, 0, 0) }

// CodeBlocks returns every top-level block in the conversation, numbered from 1.
func (t *Transcript) CodeBlocks() []render.CodeBlock { return t.code }

// LatestReply returns the text of the newest assistant message that has any.
func (t *Transcript) LatestReply() string {
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if m := t.Messages[i]; m.Role == "assistant" && strings.TrimSpace(m.Text) != "" {
			return security.Clean(m.Text)
		}
	}
	return ""
}

// Last returns the newest message.
func (t *Transcript) Last() *Message {
	if len(t.Messages) == 0 {
		return nil
	}
	return t.Messages[len(t.Messages)-1]
}

// Rebuild lays out every message. Finished messages come from the cache, so only
// the streaming one costs anything.
func (t *Transcript) Rebuild() {
	follow := t.vp.AtBottom()
	if t.renderer == nil || t.renderer.Width() != t.bodyWidth() {
		t.renderer = render.New(t.bodyWidth(), t.th)
	}
	lines := []string{""}
	t.targets = t.targets[:0]
	t.code = t.code[:0]
	first := 1
	for i, m := range t.Messages {
		b := t.block(m, first)
		base := len(lines)
		lines = append(lines, b.lines...)
		for _, target := range b.targets {
			target.Line += base
			t.targets = append(t.targets, target)
		}
		for _, c := range b.code {
			c.LabelLine += base
			t.code = append(t.code, c)
		}
		first += len(b.code)
		if i == len(t.Messages)-1 {
			lines = t.actionRow(m, lines)
		}
		lines = append(lines, "")
		if !t.Advanced && i < len(t.Messages)-1 && m.Role == "assistant" {
			lines = append(lines, "")
		}
	}
	t.vp.SetContentLines(lines)
	if follow {
		t.vp.GotoBottom()
	}
}

func (t *Transcript) indent() int {
	if t.Advanced {
		return 0
	}
	return 2
}

func (t *Transcript) bodyWidth() int { return max(t.width-t.indent(), 8) }

func (t *Transcript) block(m *Message, first int) *block {
	key := blockKey{uid: m.UID, width: t.width, first: first, version: m.version, theme: t.th.Name, advanced: t.Advanced}
	if !m.Streaming {
		if b, ok := t.cache[key]; ok {
			return b
		}
	}
	var b *block
	if m.Role == "user" {
		b = t.userBlock(m)
	} else {
		b = t.assistantBlock(m, first)
	}
	if !m.Streaming {
		t.cache[key] = b
	}
	return b
}

func (t *Transcript) userBlock(m *Message) *block {
	st := t.th.Styles
	text := security.Clean(m.Text)
	b := &block{}
	if t.Advanced {
		head := "you"
		if !m.Time.IsZero() {
			head += " · " + m.Time.Local().Format("15:04")
		}
		b.lines = append(b.lines, st.Muted.Render(views.Truncate(head, t.width)))
		if len(m.Attachments) > 0 {
			for _, line := range views.Wrap("attached: "+strings.Join(m.Attachments, ", "), t.width) {
				b.lines = append(b.lines, st.Info.Render(line))
			}
		}
		if text != "" {
			for _, line := range views.Wrap(text, t.width) {
				b.lines = append(b.lines, st.Text.Render(line))
			}
		}
		return b
	}

	limit := max(t.width*7/10, 12)
	inner := limit - 4
	if len(m.Attachments) > 0 {
		for _, line := range views.Wrap(strings.Join(m.Attachments, " · "), limit) {
			b.lines = append(b.lines, views.PadLeft(st.Info.Render(line), t.width))
		}
	}
	if strings.TrimSpace(text) == "" {
		return b
	}
	rows := views.Wrap(text, inner)
	widest := 0
	for _, row := range rows {
		widest = max(widest, ansi.StringWidth(row))
	}
	pad := strings.Repeat(" ", max(t.width-widest-4, 0))
	if !t.outlined {
		for _, row := range rows {
			b.lines = append(b.lines, pad+st.Raised.Render("  "+views.PadRight(row, widest)+"  "))
		}
		return b
	}
	bar := st.Border.Render("│")
	b.lines = append(b.lines, pad+st.Border.Render("╭"+strings.Repeat("─", widest+2)+"╮"))
	for _, row := range rows {
		b.lines = append(b.lines, pad+bar+st.Raised.Render(" "+views.PadRight(row, widest)+" ")+bar)
	}
	b.lines = append(b.lines, pad+st.Border.Render("╰"+strings.Repeat("─", widest+2)+"╯"))
	return b
}

func (t *Transcript) assistantBlock(m *Message, first int) *block {
	st := t.th.Styles
	b := &block{}
	indent := strings.Repeat(" ", t.indent())
	width := t.bodyWidth()
	add := func(line string) { b.lines = append(b.lines, indent+line) }

	if t.Advanced {
		b.lines = append(b.lines, st.Muted.Render(views.Truncate(t.advancedHeading(m), t.width)))
	}

	for _, notice := range m.Notices {
		text := notice.Message
		if !t.Advanced && !namesAFile(notice) {
			text = views.PlainOr(text, "")
		}
		if text == "" {
			continue
		}
		for _, line := range views.Wrap(text, width) {
			add(st.Muted.Italic(true).Render(line))
		}
		add("")
	}

	if t.Advanced && m.Thinking != "" {
		marker := "▸"
		if m.ThinkingOpen {
			marker = "▾"
		}
		label := marker + " Reasoning"
		b.targets = append(b.targets, Target{Line: len(b.lines), X: 0, W: ansi.StringWidth(label), Action: views.Action{Kind: "reasoning", N: m.UID}})
		add(st.Muted.Render(label))
		if m.ThinkingOpen {
			for _, line := range views.Wrap(security.Clean(m.Thinking), width-2) {
				add("  " + st.Muted.Faint(true).Render(line))
			}
		}
		add("")
	}

	text := security.Clean(m.Text)
	hasText := strings.TrimSpace(text) != ""
	mark := func(line string) string {
		if t.Advanced {
			return line
		}
		return st.Accent.Render("◆") + " " + line
	}

	switch {
	case hasText:
		if m.Streaming || m.Note != "" || m.Fail != nil {
			text = render.CloseOpen(text)
		}
		out := t.renderer.RenderFrom(text, first)
		base := len(b.lines)
		for i, line := range out.Lines {
			if i == 0 {
				b.lines = append(b.lines, mark(line))
			} else {
				add(line)
			}
		}
		if m.Streaming {
			caret := st.Accent.Render("│")
			last := len(b.lines) - 1
			if len(out.Lines) > 0 && ansi.StringWidth(out.Lines[len(out.Lines)-1])+1 < width {
				b.lines[last] += caret
			} else {
				add(caret)
			}
		}
		for _, c := range out.CodeBlocks {
			c.LabelLine += base
			b.code = append(b.code, c)
			b.targets = append(b.targets, Target{Line: c.LabelLine, X: t.indent() + 3, W: len(fmt.Sprint(c.Number)) + 2, Action: views.Action{Kind: "copy-code", N: c.Number}})
		}
	case m.Streaming:
		// The animated dot stands where the mark will be.
		b.lines = append(b.lines, t.waitLine(m, width-1))
	}

	if m.Streaming {
		return b
	}

	// A note or failure with no text above it takes the text's place, mark and all.
	marked := hasText
	lead := func(line string) {
		if marked {
			add(line)
			return
		}
		b.lines = append(b.lines, mark(line))
		marked = true
	}
	if m.Note != "" {
		if hasText {
			add("")
		}
		for _, line := range views.Wrap(m.Note, width) {
			lead(st.Muted.Render(line))
		}
	}
	if m.Fail != nil {
		if hasText {
			add("")
		}
		message := m.Fail.Message
		if !t.Advanced {
			message = views.PlainError(m.Fail.Code, message)
		}
		for _, line := range views.Wrap(message, width) {
			lead(st.Warn.Render(line))
		}
		if t.Advanced && m.Fail.Detail != "" {
			for _, line := range views.Wrap(m.Fail.Detail, width) {
				add(st.Muted.Render(line))
			}
		}
	}
	if len(m.Citations) > 0 {
		add("")
		add(st.Heading.Render("Sources"))
		for _, line := range citationLines(citedSources(m.Citations, security.Clean(m.Text)), width) {
			add(st.Info.Render(line))
		}
	}
	return b
}

// namesAFile reports a notice that must reach Basic mode as it stands. Its wording
// is plain and the only other word in it is the person's own file name, which would
// otherwise be reworded, or cost them the only sign that a file was not read.
func namesAFile(n client.Notice) bool {
	return n.Code == "attachment_truncated" || n.Code == "attachment_left_out"
}

func (t *Transcript) advancedHeading(m *Message) string {
	parts := []string{"assistant"}
	if m.Route != nil && m.Route.Tier != "" {
		parts = append(parts, m.Route.Tier)
	}
	if m.Usage != nil {
		if m.Usage.TokensPerSecond > 0 {
			parts = append(parts, fmt.Sprintf("%.1f tok/s", m.Usage.TokensPerSecond))
		}
		if m.Usage.CompletionTokens > 0 {
			parts = append(parts, fmt.Sprintf("%d tok", m.Usage.CompletionTokens))
		}
	}
	return views.OneLine(strings.Join(parts, " · "))
}

// waitLine is the honest wait: an animated dot, the status verbatim, and the
// elapsed time once past 10 seconds.
func (t *Transcript) waitLine(m *Message, width int) string {
	st := t.th.Styles
	now := t.now()
	frames := []string{"·", "○", "●", "○"}
	dot := st.Accent.Render(frames[int(now.UnixMilli()/320)%len(frames)])
	status := m.Status
	if !t.Advanced {
		status = views.PlainOr(status, "")
	}
	if status == "" {
		status = views.Thinking
	}
	line := status
	if !m.Waiting.IsZero() {
		if seconds := int(now.Sub(m.Waiting).Seconds()); seconds > 10 {
			line += " · " + views.Elapsed(seconds)
		}
	}
	return dot + " " + st.Muted.Render(views.Truncate(views.OneLine(line), width-2))
}

// citationRef is how a reply cites a source: [1], or several at once as [1, 2].
var citationRef = regexp.MustCompile(`\[(\d{1,6}(?:\s*,\s*\d{1,6})*)\]`)

// citedSources returns the sources a finished reply cites, in the order and with
// the numbers they were sent. Retrieval can return a passage the reply never used.
// The list still comes only from the citations event: the reply's text can narrow
// it and never add to it. A reply that cites none of them keeps them all, so a
// source is never lost.
func citedSources(citations []client.Citation, text string) []client.Citation {
	cited := map[int]bool{}
	for _, group := range citationRef.FindAllStringSubmatch(text, -1) {
		for _, number := range strings.Split(group[1], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(number)); err == nil {
				cited[n] = true
			}
		}
	}
	var out []client.Citation
	for _, c := range citations {
		if cited[c.Number] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return citations
	}
	return out
}

func citationLines(citations []client.Citation, width int) []string {
	var out []string
	for _, c := range citations {
		text := citationText(c)
		rows := views.Wrap(text, max(width-4, 8))
		for i, row := range rows {
			if i == 0 {
				out = append(out, fmt.Sprintf("[%d] %s", c.Number, row))
			} else {
				out = append(out, "    "+row)
			}
		}
	}
	return out
}

func citationText(c client.Citation) string {
	if c.Withheld {
		return "This source is no longer available to you."
	}
	title := c.Title
	if c.Section != "" {
		title += ", " + c.Section
	}
	parts := []string{title}
	if c.Department != "" {
		parts = append(parts, c.Department)
	}
	var edition []string
	if c.Version != "" {
		edition = append(edition, "v"+c.Version)
	}
	if c.DocumentDate != "" {
		edition = append(edition, c.DocumentDate)
	}
	if c.Page != nil {
		edition = append(edition, fmt.Sprintf("page %d", *c.Page))
	}
	if len(edition) > 0 {
		parts = append(parts, strings.Join(edition, ", "))
	}
	if c.Sample {
		parts = append(parts, "invented sample, not a real policy")
	}
	return strings.Join(parts, " · ")
}

// actionRow draws the controls under the latest message.
func (t *Transcript) actionRow(m *Message, lines []string) []string {
	if m.Role != "assistant" || m.Streaming {
		return lines
	}
	type control struct {
		label string
		kind  string
		n     int
	}
	var controls []control
	if strings.TrimSpace(m.Text) != "" {
		controls = append(controls, control{label: "Copy", kind: "copy"})
	}
	if m.CanRetry {
		controls = append(controls, control{label: "Try again", kind: "retry"})
	}
	if m.Fail != nil && m.Fail.NewChat {
		controls = append(controls, control{label: "New chat", kind: "new"})
	}
	if t.Advanced && m.Thinking != "" {
		controls = append(controls, control{label: "Reasoning", kind: "reasoning", n: m.UID})
	}
	if len(controls) == 0 {
		return lines
	}
	st := t.th.Styles
	lines = append(lines, "")
	y := len(lines)
	x := t.indent()
	row := strings.Repeat(" ", x)
	for i, c := range controls {
		if i > 0 {
			row += st.Muted.Render(" · ")
			x += 3
		}
		style := st.Info
		label := c.label
		if t.Focus == i {
			style = st.Button
			label = "▸" + label
		}
		row += style.Render(label)
		w := ansi.StringWidth(label)
		t.targets = append(t.targets, Target{Line: y, X: x, W: w, Action: views.Action{Kind: c.kind, N: c.n}, Key: true})
		x += w
	}
	return append(lines, row)
}
