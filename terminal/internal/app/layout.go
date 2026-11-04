package app

import (
	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Layer ids for hit testing with the compositor.
const (
	layerBase      = "base"
	layerPanel     = "panel"
	layerPalette   = "palette"
	layerSide      = "side"
	layerInspector = "inspector"
)

// Sizes from the responsive table in docs/ui-spec.md.
const (
	minWidth      = 40
	minHeight     = 10
	wideWidth     = 120
	sideWidth     = 90
	coarseWidth   = 60
	footerHeight  = 20
	cardsHeight   = 14
	columnMax     = 78
	cardRows      = 4
	cardsTwoWidth = 56
	advChatsWidth = 22
	advInspWidth  = 30
)

type rect struct{ X, Y, W, H int }

func (r rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// layout is where everything goes in the current window. It is recomputed after
// every message, because the composer's height can change with any of them.
type layout struct {
	small bool
	home  bool

	header     rect
	side       rect
	col        rect // the reading column, or the Advanced conversation pane
	transcript rect
	composer   rect
	footer     rect
	inspector  rect

	greeting   int // row of the greeting, Basic home only
	cards      rect
	plan       cardPlan
	status     rect
	statusRows []statusRow
	disclaimer rect
	appNotice  rect
	body       rect // Advanced: the row span of the three panes

	panelPage int
}

// statusRow is one line of connection state or notice above the composer.
type statusRow struct {
	text  string
	style lipgloss.Style
}

// cardGap is the space between two columns of cards.
const cardGap = 2

// cardPlan is which suggestions fit on the home screen.
type cardPlan struct {
	framed bool
	cols   int
	rows   int   // card rows, or text rows when not framed
	items  []int // indexes into the suggestions
	more   bool  // a row to page through the rest
}

// height returns the rows the plan occupies.
func (p cardPlan) height() int {
	h := p.rows
	if p.framed {
		h *= cardRows
	}
	if p.more {
		h++
	}
	return h
}

func (m *Model) suggestions() []client.Suggestion {
	if m.boot == nil {
		return nil
	}
	return m.boot.Suggestions
}

// planCards decides how to show the suggestions in avail rows of a column. It
// prefers framed cards, then single-line rows, then none, and offers paging when
// some do not fit.
func (m *Model) planCards(avail, width int) cardPlan {
	total := len(m.suggestions())
	if total == 0 || avail < 1 {
		return cardPlan{}
	}
	if !m.advanced() && m.width >= coarseWidth && m.height >= cardsHeight && avail >= cardRows {
		cols := 1
		if width >= cardsTwoWidth && m.cardTextFits((width-cardGap)/2) {
			cols = 2
		}
		rows := min(avail/cardRows, (total+cols-1)/cols)
		if rows*cols < total && avail-rows*cardRows < 1 {
			rows--
		}
		if rows > 0 && (rows*cols > 1 || total == 1) {
			shown := min(rows*cols, total)
			return cardPlan{framed: true, cols: cols, rows: rows, items: m.pageItems(shown, total), more: shown < total}
		}
	}
	shown := min(avail, total)
	if shown < total {
		shown = avail - 1
	}
	if shown <= 0 {
		return cardPlan{}
	}
	return cardPlan{cols: 1, rows: shown, items: m.pageItems(shown, total), more: shown < total}
}

// cardTextFits reports whether every suggestion fits a card of width w whole.
func (m *Model) cardTextFits(w int) bool {
	inner := w - 4
	for _, s := range m.suggestions() {
		if ansi.StringWidth(m.plain(s.Title)) > inner || ansi.StringWidth(m.plain(s.Subtitle)) > inner {
			return false
		}
	}
	return true
}

// pageItems lists the suggestions on the current page, wrapping at the end, so
// that paging reaches all of them.
func (m *Model) pageItems(shown, total int) []int {
	items := make([]int, shown)
	start := m.cardPage * shown % total
	for i := range items {
		items[i] = (start + i) % total
	}
	return items
}

// cardCount returns how many items the keyboard can reach on the home screen.
func (m *Model) cardCount() int {
	n := len(m.geo.plan.items)
	if m.geo.plan.more {
		n++
	}
	return n
}

func (m *Model) activateCard() tea.Cmd {
	plan := m.geo.plan
	if m.cardCursor < len(plan.items) {
		return m.suggest(plan.items[m.cardCursor])
	}
	if plan.more {
		return m.do(views.Action{Kind: "more-cards"})
	}
	return nil
}

// statusLines builds the rows shown above the composer: the connection state,
// then any notice. When there is too little room the notice wins.
func (m *Model) statusLines(width, limit int) []statusRow {
	st := m.th.Styles
	var out []statusRow
	lines, warn := m.banner()
	style := st.Muted
	if warn {
		style = st.Warn
	}
	for _, line := range lines {
		for _, row := range views.Wrap(line, width) {
			out = append(out, statusRow{row, style})
		}
	}
	if m.notice != "" {
		style = st.Muted
		if m.noticeWarn {
			style = st.Warn
		}
		for _, row := range views.Wrap(m.notice, width) {
			out = append(out, statusRow{row, style})
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// relayout recomputes the geometry and applies it to the components. Rewrapping
// the transcript waits for the resize debounce, and until then the viewport clips.
func (m *Model) relayout() {
	var g layout
	if m.width < minWidth || m.height < minHeight {
		g.small = true
		m.geo = g
		return
	}
	g.home = len(m.transcript.Messages) == 0
	if m.advanced() {
		m.layoutAdvanced(&g)
	} else {
		m.layoutBasic(&g)
	}
	g.panelPage = max(m.height-8, 1)
	m.geo = g
	if m.focus == focusList && !m.listVisible() {
		m.setFocus(focusComposer)
	}
	m.transcript.SetSize(g.transcript.W, g.transcript.H)
	if !m.resizing && m.transcript.WrapWidth() != g.transcript.W {
		m.transcript.Wrap(g.transcript.W)
		m.transcript.Rebuild()
	}
}

func (m *Model) layoutBasic(g *layout) {
	w, h := m.width, m.height
	sideW := 0
	if w >= sideWidth && !m.sideHidden {
		sideW = min(max(w/4, 24), 30)
	}
	g.side = rect{0, 0, sideW, h}
	mainW := w - sideW
	g.header = rect{sideW, 0, mainW, 1}

	colW := min(columnMax, mainW-4)
	if mainW < coarseWidth {
		colW = mainW - 2
	}
	colX := sideW + (mainW-colW)/2
	g.col = rect{colX, 0, colW, h}

	m.composer.SetLimits(max(min(h-7, 6), 1), max((h-8)/3, 1))
	m.composer.SetWidth(colW)
	composerH := m.composer.Height()

	tall := h >= footerHeight
	footer := 0
	if tall {
		footer = 1
		g.footer = rect{colX, h - 1, colW, 1}
	}
	notes := 0
	if tall {
		notes = len(views.DisclaimerLines(colW))
	}

	statusLimit := 3
	if h < 14 {
		statusLimit = 1
	}
	g.statusRows = m.statusLines(colW, statusLimit)
	statusH := len(g.statusRows)

	if !g.home {
		bottom := h - footer - notes
		g.composer = rect{colX, bottom - composerH, colW, composerH}
		g.status = rect{colX, g.composer.Y - statusH, colW, statusH}
		g.transcript = rect{colX, 1, colW, max(g.status.Y-1, 1)}
		g.disclaimer = rect{colX, bottom, colW, notes}
		return
	}

	var noticeLines []string
	if tall && m.boot != nil && m.boot.Application.Notice != "" {
		noticeLines = views.Wrap(views.PlainOr(m.boot.Application.Notice, ""), colW)
	}
	// One row above the composer is always held for status, so that a passing
	// notice does not move the screen.
	reserve := max(statusH, 1)
	fixed := 1 + footer + composerH + notes + len(noticeLines) + reserve + 2 // header, greeting, gap
	margin := 0
	if tall {
		margin = 2
	}
	g.plan = m.planCards(h-fixed-margin, colW)
	cardsH := g.plan.height()
	block := fixed - 1 - footer + cardsH
	top := 1 + max(h-1-footer-block, 0)/2

	y := top
	g.greeting = y
	y += 2
	if cardsH > 0 {
		g.cards = rect{colX, y, colW, cardsH}
		y += cardsH
	}
	g.status = rect{colX, y + reserve - statusH, colW, statusH}
	y += reserve
	g.composer = rect{colX, y, colW, composerH}
	y += composerH
	g.disclaimer = rect{colX, y, colW, notes}
	y += notes
	g.appNotice = rect{colX, y, colW, len(noticeLines)}
	g.transcript = rect{colX, 1, colW, 1}
}

func (m *Model) layoutAdvanced(g *layout) {
	w, h := m.width, m.height
	chatsW := 0
	if w >= sideWidth && !m.sideHidden {
		chatsW = advChatsWidth
	}
	inspW := 0
	if w >= wideWidth && !m.inspectorOff {
		inspW = advInspWidth
	}
	footer := 0
	if h >= footerHeight {
		footer = 1
		g.footer = rect{0, h - 1, w, 1}
	}
	g.header = rect{0, 0, w, 1}

	m.composer.SetLimits(max(min(h-8, 6), 1), max((h-8)/3, 1))
	m.composer.SetWidth(w)
	rows := m.composer.BareHeight()

	statusLimit := 3
	if h < 14 {
		statusLimit = 1
	}
	inner := w - 2
	if chatsW > 0 {
		inner -= chatsW + 1
	}
	if inspW > 0 {
		inner -= inspW + 1
	}
	g.statusRows = m.statusLines(inner-2, statusLimit)
	statusH := len(g.statusRows)

	bottom := h - footer - 1
	composerY := bottom - rows
	g.composer = rect{0, composerY, w, rows}

	top := 2
	bodyH := max(composerY-1-top, 1)
	g.body = rect{0, top, w, bodyH}

	x := 1
	if chatsW > 0 {
		g.side = rect{1, top, chatsW, bodyH}
		x += chatsW + 1
	}
	g.col = rect{x, top, inner, bodyH}
	if inspW > 0 {
		g.inspector = rect{x + inner + 1, top, inspW, bodyH}
	}
	g.status = rect{x + 1, top + bodyH - statusH, inner - 2, statusH}
	g.transcript = rect{x + 1, top, inner - 2, max(bodyH-statusH, 1)}
	if g.home {
		g.plan = m.planCards(g.transcript.H-2, g.transcript.W)
		g.cards = rect{g.transcript.X, top + 2, g.transcript.W, g.plan.height()}
	}
}
