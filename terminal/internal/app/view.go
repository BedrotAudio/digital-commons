package app

import (
	"strings"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const productName = "Digital Commons"

// View draws one frame. Nothing is drawn until the theme is known, so a light
// terminal never flashes dark. BackgroundColor stays nil: every cell is painted.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.WindowTitle = productName
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if !m.themeReady || m.width <= 0 || m.height <= 0 {
		return v
	}
	v.Content, v.Cursor = m.draw()
	return v
}

// draw renders the frame and records what can be clicked in it.
func (m *Model) draw() (string, *tea.Cursor) {
	f := views.NewFrame(m.width, m.height, m.profile)
	c := m.th.Colors
	m.zones, m.modalZones, m.comp = nil, nil, nil

	if m.geo.small {
		lines := views.Wrap(views.Enlarge, m.width)
		top := max((m.height-len(lines))/2, 0)
		for i, line := range lines {
			f.Put(0, top+i, m.width, 1, m.th.Styles.Text.Render(views.Center(line, m.width)))
		}
		f.Paint(0, 0, m.width, m.height, c.Bg, c.Text)
		f.Strip()
		return f.String(), nil
	}

	var cursor *tea.Cursor
	if m.advanced() {
		cursor = m.drawAdvanced(f)
	} else {
		cursor = m.drawBasic(f)
	}
	f.Paint(0, 0, m.width, m.height, c.Bg, c.Text)
	m.zones = f.Zones()
	m.drawOverlays(f)
	f.Strip()
	return f.String(), cursor
}

func (m *Model) drawBasic(f *views.Frame) *tea.Cursor {
	g := m.geo
	st := m.th.Styles
	if g.side.W > 0 {
		m.drawSide(f, g.side, m.focus == focusList)
		if m.profile <= colorprofile.ASCII {
			f.Put(g.side.W-1, 0, 1, g.side.H, column(st.Border.Render("│"), g.side.H))
		}
	}
	m.drawHeader(f)
	if g.home {
		m.drawHome(f)
	} else {
		m.drawTranscript(f)
	}
	m.drawStatus(f, true)
	cursor := m.drawComposer(f)
	if g.disclaimer.H > 0 {
		for i, line := range views.DisclaimerLines(g.col.W) {
			f.Put(g.col.X, g.disclaimer.Y+i, g.col.W, 1, st.Muted.Render(views.Center(line, g.col.W)))
		}
	}
	if g.footer.H > 0 {
		m.drawBasicFooter(f)
	}
	return cursor
}

func column(cell string, h int) string {
	return strings.TrimSuffix(strings.Repeat(cell+"\n", h), "\n")
}

// drawSide draws the chat list on a surface fill, with highlighted rows raised.
func (m *Model) drawSide(f *views.Frame, r rect, focused bool) {
	c := m.th.Colors
	out := m.sidebar.Render(m.th, r.W, r.H, focused, m.now())
	f.Put(r.X, r.Y, r.W, r.H, out.Content)
	for _, row := range out.Rows {
		f.Paint(r.X, r.Y+row.Y, r.W, 1, c.SurfaceRaised, c.Text)
	}
	f.Paint(r.X, r.Y, r.W, r.H, c.Surface, c.Text)
	for _, z := range out.Zones {
		f.AddZones([]views.Zone{z.Offset(r.X, r.Y)})
	}
}

// indicator returns the locality text, which keeps its word when disconnected so
// that meaning survives without color.
func (m *Model) indicator() string {
	st := m.th.Styles
	switch m.conn {
	case connConnecting:
		return st.Muted.Render("○ " + views.Connecting)
	case connDown, connAuth:
		return st.Danger.Render("○ " + views.NotConnected)
	}
	kind, label := m.locality()
	if kind == "demo" {
		return st.Warn.Render("● " + label)
	}
	return st.Accent.Render("●") + " " + st.Text.Render(label)
}

func (m *Model) stageLabel() string {
	if m.boot != nil && m.boot.Application.Stage != "" {
		return titleCase(m.boot.Application.Stage)
	}
	return "Beta"
}

func (m *Model) drawHeader(f *views.Frame) {
	g := m.geo
	st := m.th.Styles
	x := g.header.X + 1
	if g.side.W == 0 {
		label := "▸ Chats"
		f.Put(x, 0, len("x Chats"), 1, st.Accent.Bold(true).Render(label))
		f.Click(x, 0, len("x Chats"), 1, views.Action{Kind: "toggle-side"})
		x += len("x Chats") + 2
	}
	indicator := m.indicator()
	width := ansi.StringWidth(indicator)
	f.Put(x, 0, width, 1, indicator)
	f.Click(x, 0, width, 1, views.Action{Kind: "privacy"})

	right := st.Muted.Render(m.stageLabel())
	if m.width >= coarseWidth && g.side.W == 0 { // The sidebar already names the product.
		right = st.Text.Bold(true).Render(productName) + "  " + right
	}
	rw := ansi.StringWidth(right)
	if x+width+2+rw <= g.header.X+g.header.W-1 {
		f.Put(g.header.X+g.header.W-rw-1, 0, rw, 1, right)
	}
}

func (m *Model) drawHome(f *views.Frame) {
	g := m.geo
	st := m.th.Styles
	f.Put(g.col.X, g.greeting, g.col.W, 1, st.Text.Bold(true).Render(views.Center(views.Greeting, g.col.W)))
	m.drawCards(f)
	if g.appNotice.H > 0 {
		text := views.PlainOr(m.boot.Application.Notice, "")
		for i, line := range views.Wrap(text, g.col.W) {
			f.Put(g.col.X, g.appNotice.Y+i, g.col.W, 1, st.Warn.Render(views.Center(line, g.col.W)))
		}
	}
}

// drawCards draws the plan for the home screen: framed cards, or one row each.
func (m *Model) drawCards(f *views.Frame) {
	g := m.geo
	plan := g.plan
	st := m.th.Styles
	c := m.th.Colors
	list := m.suggestions()
	active := func(i int) bool { return m.focus == focusConversation && m.cardCursor == i }

	if plan.framed {
		w := (g.cards.W - cardGap*(plan.cols-1)) / plan.cols
		for i, index := range plan.items {
			x := g.cards.X + (i%plan.cols)*(w+cardGap)
			y := g.cards.Y + (i/plan.cols)*cardRows
			f.Put(x, y, w, cardRows, m.card(list[index], w, active(i)))
			if active(i) {
				f.Paint(x+1, y+1, w-2, cardRows-2, c.SurfaceRaised, c.Text)
			}
			f.Click(x, y, w, cardRows, views.Action{Kind: "suggest", N: index})
		}
	} else {
		subtitles := true
		for _, index := range plan.items {
			s := list[index]
			need := ansi.StringWidth(m.plain(s.Title)) + 3 + ansi.StringWidth(m.plain(s.Subtitle))
			subtitles = subtitles && need <= g.cards.W-2
		}
		for i, index := range plan.items {
			s := list[index]
			marker, style := "  ", st.Text
			if active(i) {
				marker, style = "▸ ", st.Accent.Bold(true)
			}
			line := style.Render(marker + views.Truncate(m.plain(s.Title), g.cards.W-2))
			if subtitle := m.plain(s.Subtitle); subtitles && subtitle != "" {
				line += st.Muted.Render(" · " + subtitle)
			}
			f.Put(g.cards.X, g.cards.Y+i, g.cards.W, 1, line)
			f.Click(g.cards.X, g.cards.Y+i, g.cards.W, 1, views.Action{Kind: "suggest", N: index})
		}
	}
	if plan.more {
		label := "More suggestions"
		style := st.Info
		if active(len(plan.items)) {
			label, style = "▸ "+label, st.Accent.Bold(true)
		}
		y := g.cards.Y + g.cards.H - 1
		x := g.cards.X + max((g.cards.W-ansi.StringWidth(label))/2, 0)
		if !plan.framed {
			x = g.cards.X + 2
		}
		f.Put(x, y, ansi.StringWidth(label), 1, style.Render(label))
		f.Click(x, y, ansi.StringWidth(label), 1, views.Action{Kind: "more-cards"})
	}
}

// plain applies Basic mode wording to gateway text. Advanced shows it unchanged.
func (m *Model) plain(s string) string {
	if m.advanced() {
		return views.OneLine(s)
	}
	return views.OneLine(views.PlainOr(s, ""))
}

func (m *Model) card(s client.Suggestion, w int, focused bool) string {
	st := m.th.Styles
	border := st.Border
	titleStyle := st.Text.Bold(true)
	if focused {
		border = st.Accent
		titleStyle = st.Accent.Bold(true)
	}
	inner := w - 4
	bar := border.Render("│")
	title := views.PadRight(views.Truncate(m.plain(s.Title), inner), inner)
	subtitle := views.PadRight(views.Truncate(m.plain(s.Subtitle), inner), inner)
	return strings.Join([]string{
		border.Render("╭" + strings.Repeat("─", w-2) + "╮"),
		bar + " " + titleStyle.Render(title) + " " + bar,
		bar + " " + st.Muted.Render(subtitle) + " " + bar,
		border.Render("╰" + strings.Repeat("─", w-2) + "╯"),
	}, "\n")
}

func (m *Model) drawTranscript(f *views.Frame) {
	r := m.geo.transcript
	f.Put(r.X, r.Y, r.W, r.H, m.transcript.View())
	if m.transcript.AtBottom() || r.H < 3 {
		return
	}
	label := " ▾ Latest "
	w := ansi.StringWidth(label)
	x := r.X + (r.W-w)/2
	y := r.Y + r.H - 1
	f.Put(x, y, w, 1, m.th.Styles.Raised.Render(label))
	f.Click(x, y, w, 1, views.Action{Kind: "bottom"})
}

func (m *Model) drawStatus(f *views.Frame, centered bool) {
	r := m.geo.status
	for i, row := range m.geo.statusRows {
		text := row.text
		if centered {
			text = views.Center(text, r.W)
		}
		f.Put(r.X, r.Y+i, r.W, 1, row.style.Render(text))
	}
}

func (m *Model) composerFocused() bool {
	return m.focus == focusComposer && len(m.panels) == 0
}

func (m *Model) drawComposer(f *views.Frame) *tea.Cursor {
	r := m.geo.composer
	out := m.composer.Render(m.composerFocused())
	f.Put(r.X, r.Y, r.W, r.H, out.Content)
	for _, z := range out.Zones {
		f.AddZones([]views.Zone{z.Offset(r.X, r.Y)})
	}
	return m.composerCursor(out.Cursor, r.X+out.CursorX, r.Y+out.CursorY)
}

func (m *Model) composerCursor(show bool, x, y int) *tea.Cursor {
	if !show || !m.composerFocused() {
		return nil
	}
	cursor := tea.NewCursor(x, y)
	cursor.Shape = tea.CursorBar
	cursor.Blink = true
	return cursor
}

// newlineHint is the footer's word for a new line. It names Shift+Enter only after
// the terminal has confirmed that it works.
func (m *Model) newlineHint() string {
	if m.shiftEnter {
		return "Shift+Enter new line"
	}
	return "Ctrl+J new line"
}

type hint struct {
	text   string
	action views.Action
}

func (m *Model) drawBasicFooter(f *views.Frame) {
	r := m.geo.footer
	first := hint{"Enter to send", views.Action{}}
	if m.active != nil {
		first = hint{"Esc to stop", views.Action{Kind: "stop"}}
	}
	hints := []hint{first, {m.newlineHint(), views.Action{}}}
	if m.mac() {
		hints = append(hints, hint{"/help", views.Action{Kind: "help"}})
	} else {
		hints = append(hints, hint{"F1 Help", views.Action{Kind: "help"}}, hint{"F2 Settings", views.Action{Kind: "settings"}})
	}
	for hintsWidth(hints) > r.W && len(hints) > 1 {
		hints = hints[:len(hints)-1]
	}
	x := r.X + max((r.W-hintsWidth(hints))/2, 0)
	m.drawHints(f, hints, x, r.Y, r.W)
}

func hintsWidth(hints []hint) int {
	w := 0
	for i, h := range hints {
		if i > 0 {
			w += 3
		}
		w += ansi.StringWidth(h.text)
	}
	return w
}

func (m *Model) drawHints(f *views.Frame, hints []hint, x, y, room int) {
	st := m.th.Styles
	var line strings.Builder
	at := x
	for i, h := range hints {
		if i > 0 {
			line.WriteString(st.Muted.Render(" · "))
			at += 3
		}
		line.WriteString(st.Muted.Render(h.text))
		w := ansi.StringWidth(h.text)
		if h.action.Kind != "" {
			f.Click(at, y, w, 1, h.action)
		}
		at += w
	}
	f.Put(x, y, min(room, f.W-x), 1, line.String())
}

// box is a blank string of a size, for a hit-test layer.
func box(w, h int) string {
	return column(strings.Repeat(" ", w), h)
}

// drawOverlays draws the layers above the base: lists, then panels. Each one
// paints its own surface and replaces what was under it.
func (m *Model) drawOverlays(f *views.Frame) {
	layers := []*lipgloss.Layer{lipgloss.NewLayer(box(m.width, m.height)).ID(layerBase)}
	add := func(id string, x, y, w, h, z int, content string, zones []views.Zone) {
		f.Region(x, y, w, h, content, m.th.Colors.Surface, m.th.Colors.Text)
		for _, zone := range zones {
			m.modalZones = append(m.modalZones, zone.Offset(x, y))
		}
		layers = append(layers, lipgloss.NewLayer(box(w, h)).X(x).Y(y).Z(z).ID(id))
	}

	if m.sideOverlay {
		r := rect{0, 0, min(30, m.width-8), m.height}
		before := len(f.Zones())
		f.Put(r.X, r.Y, r.W, r.H, box(r.W, r.H))
		m.drawSide(f, rect{0, 0, r.W - 1, r.H}, m.focus == focusList)
		f.Put(r.W-1, 0, 1, r.H, column(m.th.Styles.Border.Render("│"), r.H))
		f.Paint(r.W-1, 0, 1, r.H, m.th.Colors.Surface, m.th.Colors.Text)
		m.modalZones = append(m.modalZones, f.Zones()[before:]...)
		layers = append(layers, lipgloss.NewLayer(box(r.W, r.H)).X(0).Y(0).Z(1).ID(layerSide))
	}
	if m.inspectorTop && m.advanced() {
		out := views.RenderPanel(m.th, m.inspectorPanel(), m.width, m.height)
		add(layerInspector, max(m.width-out.W-1, 0), 1, out.W, out.H, 1, out.Content, out.Zones)
	}
	if m.palette.Open && len(m.panels) == 0 {
		r := m.geo.composer
		x, width := r.X, r.W
		if m.advanced() {
			x, width = 2, min(m.width-4, 76)
		}
		room, below := r.Y, false
		if room < 3 {
			room, below = m.height-r.Y-r.H, true
		}
		out := m.palette.Render(m.th, width, min(8, room-2))
		y := r.Y - out.H
		if below {
			y = r.Y + r.H
		}
		add(layerPalette, x, y, width, out.H, 2, out.Content, out.Zones)
	}
	if p := m.topPanel(); p != nil {
		out := views.RenderPanel(m.th, m.buildPanel(p), m.width, m.height)
		p.scroll = out.Scroll
		m.geo.panelPage = max(out.Visible-1, 1)
		add(layerPanel, (m.width-out.W)/2, (m.height-out.H)/2, out.W, out.H, 3, out.Content, out.Zones)
	}
	m.comp = lipgloss.NewCompositor(layers...)
}
