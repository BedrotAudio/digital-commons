package app

import (
	"fmt"
	"strings"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// drawAdvanced draws the workstation: a header, up to three panes, the composer
// joined to them, and a status footer.
func (m *Model) drawAdvanced(f *views.Frame) *tea.Cursor {
	g := m.geo
	st := m.th.Styles
	c := m.th.Colors
	m.drawAdvancedHeader(f)

	type pane struct {
		title   string
		width   int
		focused bool
	}
	var panes []pane
	if g.side.W > 0 {
		panes = append(panes, pane{"chats", g.side.W, m.focus == focusList})
	}
	panes = append(panes, pane{"conversation", g.col.W, m.focus == focusConversation})
	if g.inspector.W > 0 {
		panes = append(panes, pane{"inspector", g.inspector.W, false})
	}

	var top strings.Builder
	top.WriteString(st.Border.Render("┌"))
	var joints []int
	x := 0
	for i, p := range panes {
		style := st.Muted
		if p.focused {
			style = st.Accent.Bold(true)
		}
		label := " " + views.Truncate(p.title, p.width-3) + " "
		top.WriteString(style.Render(label))
		top.WriteString(st.Border.Render(strings.Repeat("─", max(p.width-ansi.StringWidth(label), 0))))
		x += p.width + 1
		if i < len(panes)-1 {
			top.WriteString(st.Border.Render("┬"))
			joints = append(joints, x)
		}
	}
	top.WriteString(st.Border.Render("┐"))
	f.Put(0, 1, m.width, 1, top.String())

	bar := st.Border.Render("│")
	f.Put(0, g.body.Y, 1, g.body.H, column(bar, g.body.H))
	f.Put(m.width-1, g.body.Y, 1, g.body.H, column(bar, g.body.H))
	for _, joint := range joints {
		f.Put(joint, g.body.Y, 1, g.body.H, column(bar, g.body.H))
	}

	if g.side.W > 0 {
		m.drawSide(f, g.side, m.focus == focusList)
	}
	if g.inspector.W > 0 {
		lines := m.inspectorLines(g.inspector.W - 2)
		lines = lines[:min(len(lines), g.inspector.H)]
		f.Put(g.inspector.X+1, g.inspector.Y, g.inspector.W-2, g.inspector.H, strings.Join(lines, "\n"))
		f.Paint(g.inspector.X, g.inspector.Y, g.inspector.W, g.inspector.H, c.Surface, c.Text)
	}

	if g.home {
		m.drawAdvancedHome(f)
	} else {
		m.drawTranscript(f)
	}
	m.drawStatus(f, false)

	focused := m.composerFocused()
	edge := st.Border
	if focused {
		edge = st.Accent
	}
	line := []rune(strings.Repeat("─", m.width))
	line[0], line[m.width-1] = '├', '┤'
	for _, joint := range joints {
		line[joint] = '┴'
	}
	sep := g.composer.Y - 1
	f.Put(0, sep, m.width, 1, edge.Render(string(line)))

	out := m.composer.RenderBare()
	f.Put(2, g.composer.Y, m.width-4, g.composer.H, out.Content)
	for _, z := range out.Zones {
		f.AddZones([]views.Zone{z.Offset(2, g.composer.Y)})
	}
	sides := edge.Render("│")
	f.Put(0, g.composer.Y, 1, g.composer.H, column(sides, g.composer.H))
	f.Put(m.width-1, g.composer.Y, 1, g.composer.H, column(sides, g.composer.H))
	f.Put(0, g.composer.Y+g.composer.H, m.width, 1, edge.Render("└"+strings.Repeat("─", m.width-2)+"┘"))

	if g.footer.H > 0 {
		m.drawAdvancedFooter(f)
	}
	return m.composerCursor(out.Cursor, 2+out.CursorX, g.composer.Y+out.CursorY)
}

// routeLabel is the header's tier: what was asked for and, once a turn has run
// with that choice, where it went.
func (m *Model) routeLabel() string {
	route, _ := m.latest()
	if route != nil && route.RequestedTier == m.adv.tier && route.Tier != "" && route.Tier != m.adv.tier {
		return m.adv.tier + " → " + route.Tier
	}
	return m.adv.tier
}

// latest returns the newest route and usage in the transcript.
func (m *Model) latest() (route *client.Route, usage *client.Usage) {
	messages := m.transcript.Messages
	for i := len(messages) - 1; i >= 0 && (route == nil || usage == nil); i-- {
		if route == nil {
			route = messages[i].Route
		}
		if usage == nil {
			usage = messages[i].Usage
		}
	}
	return route, usage
}

func (m *Model) drawAdvancedHeader(f *views.Frame) {
	st := m.th.Styles
	const gap = "  "
	brand := st.Accent.Render("◆") + " " + st.Text.Bold(true).Render(productName)
	mode := st.Accent.Bold(true).Render("ADVANCED")
	gateway := st.Muted.Render("gateway ") + m.liveDot(m.connected())
	route, _ := m.latest()
	model := ""
	if route != nil {
		model = route.Model
	}

	parts := []string{brand, mode, m.indicator(), gateway, st.Text.Render(m.routeLabel())}
	if model != "" {
		parts = append(parts, st.Muted.Render(views.OneLine(model)))
	}
	if m.adv.assistant == "code" {
		parts = append(parts, st.Warn.Bold(true).Render("CODE"))
	}
	join := func() string { return " " + strings.Join(parts, gap) }
	drop := func(i int) { parts = append(parts[:i:i], parts[i+1:]...) }
	if ansi.StringWidth(join()) > m.width && model != "" {
		drop(5)
	}
	if ansi.StringWidth(join()) > m.width {
		drop(3)
	}
	if ansi.StringWidth(join()) > m.width {
		parts[0] = st.Accent.Render("◆")
	}
	f.Put(0, 0, m.width, 1, views.Truncate(join(), m.width))
	if w := ansi.StringWidth(brand); ansi.StringWidth(parts[0]) == w {
		x := 1 + w + len(gap) + len("ADVANCED") + len(gap)
		width := ansi.StringWidth(m.indicator())
		if x+width <= m.width {
			f.Click(x, 0, width, 1, views.Action{Kind: "privacy"})
		}
	}
}

func (m *Model) liveDot(up bool) string {
	if up {
		return m.th.Styles.Accent.Render("●")
	}
	return m.th.Styles.Danger.Render("○")
}

func (m *Model) drawAdvancedHome(f *views.Frame) {
	g := m.geo
	st := m.th.Styles
	f.Put(g.transcript.X, g.transcript.Y, g.transcript.W, 1, st.Muted.Render(views.Truncate("New chat. Type a message, or / for commands.", g.transcript.W)))
	m.drawCards(f)
}

func (m *Model) drawAdvancedFooter(f *views.Frame) {
	hints := []hint{
		{"/help", views.Action{Kind: "run", Arg: "/help"}},
		{"/model", views.Action{Kind: "run", Arg: "/model"}},
		{"/code", views.Action{Kind: "run", Arg: "/code"}},
		{"/status", views.Action{Kind: "run", Arg: "/status"}},
		{m.newlineHint(), views.Action{}},
	}
	label := "F3 inspector"
	if m.mac() {
		label = "inspector"
	}
	hints = append(hints, hint{label, views.Action{Kind: "toggle-inspector"}})
	if m.geo.side.W == 0 {
		hints = append(hints, hint{"Ctrl+B chats", views.Action{Kind: "toggle-side"}})
	}
	right := m.footerState()
	rw := ansi.StringWidth(right)
	room := m.width - 2
	if rw+2 < room {
		room -= rw + 2
		f.Put(m.width-rw-1, m.geo.footer.Y, rw, 1, right)
	}
	for hintsWidth(hints) > room && len(hints) > 1 {
		hints = hints[:len(hints)-1]
	}
	m.drawHints(f, hints, 1, m.geo.footer.Y, room)
}

// footerState is the provider, what it is doing and the last speed.
func (m *Model) footerState() string {
	st := m.th.Styles
	name := "service"
	reachable := m.connected()
	if m.status != nil && m.status.Inference != nil {
		name = m.status.Inference.Provider
		reachable = reachable && m.status.Inference.Reachable
	}
	state := "idle"
	if m.active != nil {
		state = "working"
	}
	out := st.Muted.Render(name+" ") + m.liveDot(reachable) + st.Muted.Render(" "+state)
	if _, usage := m.latest(); usage != nil && usage.TokensPerSecond > 0 {
		out += st.Muted.Render(fmt.Sprintf(" · %.1f tok/s", usage.TokensPerSecond))
	}
	return out
}

// tierContext finds a tier's context size in what the gateway has reported.
func (m *Model) tierContext(route *client.Route) int {
	if m.models != nil && route != nil {
		for _, tier := range m.models.Tiers {
			if tier.Tier == route.Tier {
				return tier.ContextTokens
			}
		}
	}
	if m.status != nil && m.status.Inference != nil && route != nil {
		for _, loaded := range m.status.Inference.LoadedModels {
			if loaded.Name == route.Model {
				return loaded.ContextTokens
			}
		}
	}
	return 0
}

func compact(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func seconds(ms int64) string {
	if ms < 10000 {
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%d s", ms/1000)
}

// inspectorLines builds the inspector for a text width.
func (m *Model) inspectorLines(width int) []string {
	st := m.th.Styles
	var lines []string
	head := func(title string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, st.Heading.Render(title))
	}
	const keyWidth = 8
	pair := func(key, value string) {
		for i, row := range views.Wrap(value, max(width-keyWidth, 4)) {
			label := ""
			if i == 0 {
				label = key
			}
			lines = append(lines, st.Muted.Render(views.PadRight(label, keyWidth))+st.Text.Render(row))
		}
	}

	route, usage := m.latest()
	head("MODEL")
	pair("route", m.routeLabel())
	if route != nil && route.FallbackFrom != "" {
		pair("from", route.FallbackFrom)
	}
	if route != nil && route.Model != "" {
		pair("model", route.Model)
	}
	if ctx := m.tierContext(route); ctx > 0 {
		pair("ctx", fmt.Sprintf("%d", ctx))
	}
	if route != nil && route.Reason != "" {
		pair("reason", route.Reason)
	}
	if route == nil {
		lines = append(lines, st.Muted.Render("no turn yet"))
	}

	head("USAGE")
	if usage == nil {
		lines = append(lines, st.Muted.Render("no turn yet"))
	} else {
		mark := ""
		if usage.Estimated {
			mark = "~"
		}
		pair("prompt", fmt.Sprintf("%s%d tok", mark, usage.PromptTokens))
		pair("output", fmt.Sprintf("%s%d tok", mark, usage.CompletionTokens))
		pair("speed", fmt.Sprintf("%.1f tok/s", usage.TokensPerSecond))
		pair("first", seconds(usage.FirstTokenMS))
		pair("total", seconds(usage.TotalMS))
		if usage.LoadMS > 0 {
			pair("load", seconds(usage.LoadMS))
		}
	}

	head("CONTEXT")
	if ctx := m.lastContext; ctx != nil && ctx.BudgetTokens > 0 {
		text := compact(ctx.EstimatedTokens) + "/" + compact(ctx.BudgetTokens)
		cells := max(width-ansi.StringWidth(text)-1, 4)
		filled := min(ctx.EstimatedTokens*cells/ctx.BudgetTokens, cells)
		lines = append(lines, st.Accent.Render(strings.Repeat("▓", filled))+st.Muted.Render(strings.Repeat("░", cells-filled))+" "+st.Text.Render(text))
		if ctx.OmittedMessages > 0 {
			lines = append(lines, st.Warn.Render(views.Truncate(fmt.Sprintf("%d messages omitted", ctx.OmittedMessages), width)))
		}
	} else {
		lines = append(lines, st.Muted.Render("shown after the next turn"))
	}

	head("BACKEND")
	gateway := m.liveDot(m.connected())
	if m.connected() {
		gateway += st.Text.Render(fmt.Sprintf(" %d ms", m.latency.Milliseconds()))
	}
	lines = append(lines, st.Muted.Render(views.PadRight("gateway", keyWidth))+gateway)
	if s := m.status; s != nil {
		if inf := s.Inference; inf != nil {
			name := views.Truncate(inf.Provider, keyWidth-1)
			lines = append(lines, st.Muted.Render(views.PadRight(name, keyWidth))+m.liveDot(m.connected() && inf.Reachable)+st.Text.Render(" "+views.Truncate(inf.Version, width-keyWidth-2)))
		}
		if m.connected() {
			pair("state", s.State)
		} else {
			pair("state", "unknown")
		}
		if s.State == "degraded" {
			for _, row := range views.Wrap(s.Summary, width) {
				lines = append(lines, st.Warn.Render(row))
			}
		}
		if s.Warmup.State != "" && s.Warmup.State != "idle" && s.Warmup.State != "ready" {
			pair("warm-up", s.Warmup.State)
		}
	}
	return lines
}

// inspectorPanel is the overlay form, for windows too narrow for the pane.
func (m *Model) inspectorPanel() views.Panel {
	width := advInspWidth + 4
	out := views.Panel{Title: "Inspector", Cursor: -1, Width: width}
	for _, line := range m.inspectorLines(views.PanelInner(width, m.width)) {
		out.Rows = append(out.Rows, views.Row{{Text: line, Style: lipgloss.NewStyle()}})
	}
	return out
}
