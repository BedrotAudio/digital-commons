package app

import (
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/security"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// handlePaste normalizes line endings and cleans the text at the root, because
// textarea double-spaces CRLF.
func (m *Model) handlePaste(msg tea.PasteMsg) tea.Cmd {
	if len(m.panels) > 0 || m.geo.small {
		return nil
	}
	cmd := m.setFocus(focusComposer)
	return tea.Batch(cmd, m.composer.Update(tea.PasteMsg{Content: security.CleanPaste(msg.Content)}))
}

func (m *Model) pasteCmd() tea.Cmd {
	read := m.opts.Clipboard.Read
	return func() tea.Msg {
		text, err := read()
		if err != nil {
			return nil
		}
		return tea.PasteMsg{Content: text}
	}
}

// handleKey consumes app shortcuts before anything reaches the textarea, because
// its defaults collide with ctrl+n, ctrl+b and ctrl+t.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return m.interrupt()
	}
	if m.geo.small {
		return nil // Nothing can be seen, so nothing should happen.
	}
	if len(m.panels) > 0 {
		return m.panelKey(msg)
	}
	switch key {
	case "esc":
		return m.escape()
	case "ctrl+n":
		return m.newChat()
	case "ctrl+b":
		return m.toggleSidebar()
	case "ctrl+o":
		return m.openPicker(1, "")
	case "ctrl+y":
		return m.copyLatest()
	case "ctrl+r":
		return m.retry()
	case "ctrl+t":
		return m.cycleTheme()
	case "ctrl+v", "super+v":
		return m.pasteCmd()
	case "f1":
		return m.openPanel(&panel{kind: panelHelp})
	case "f2":
		return m.openPanel(&panel{kind: panelSettings})
	case "f3":
		m.toggleInspector()
		return nil
	case "pgup":
		m.transcript.PageUp()
		return nil
	case "pgdown":
		m.transcript.PageDown()
		return nil
	case "tab", "shift+tab":
		if key == "tab" && m.palette.Open {
			m.completeCommand()
			return nil
		}
		return m.cycleFocus(key == "tab")
	}

	switch m.focus {
	case focusList:
		return m.listKey(msg)
	case focusConversation:
		return m.conversationKey(msg)
	}
	return m.composerKey(msg)
}

// interrupt is Ctrl+C. It stops a reply. When idle, two presses inside the window
// quit. A time window is used because a bare modifier can arrive as a key on inbox
// conhost, so "the very next key" is not a reliable rule.
func (m *Model) interrupt() tea.Cmd {
	now := m.now()
	armed := now.Before(m.quitArmed)
	m.quitArmed = now.Add(quitWindow)
	if armed {
		return m.quit()
	}
	m.setNotice("Press Ctrl+C again to quit.")
	m.stop()
	return nil
}

func (m *Model) quit() tea.Cmd {
	m.quitting = true
	return m.stopThen(func() tea.Cmd { return tea.Quit })
}

// escape closes a list, then a panel, then stops the reply.
func (m *Model) escape() tea.Cmd {
	switch {
	case m.palette.Open:
		m.palette.Close()
	case m.sideOverlay:
		m.sideOverlay = false
		return m.setFocus(focusComposer)
	case m.inspectorTop:
		m.inspectorTop = false
	case m.active != nil:
		m.stop()
	default:
		return m.setFocus(focusComposer)
	}
	return nil
}

func (m *Model) composerKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		if m.palette.Open {
			m.palette.Move(-1)
			return nil
		}
	case "down":
		if m.palette.Open {
			m.palette.Move(1)
			return nil
		}
	case "enter":
		return m.enter()
	case "backspace":
		if m.composer.Value() == "" {
			m.removeLastChip()
			return nil
		}
	}
	return m.composer.Update(msg)
}

// removeLastChip is Backspace in an empty composer, the keyboard's way to remove
// what the x on a chip removes.
func (m *Model) removeLastChip() {
	switch {
	case m.useDocs:
		m.useDocs = false
	case len(m.attachments) > 0:
		m.attachments = m.attachments[:len(m.attachments)-1]
	}
	m.syncChips()
}

// enter runs the highlighted command, a typed command, or sends.
func (m *Model) enter() tea.Cmd {
	text := m.composer.Value()
	if m.palette.Open {
		if c, ok := m.palette.Selected(); ok {
			if c.NeedsArg {
				m.completeCommand()
				return nil
			}
			if len(c.Name) >= len(text) {
				text = c.Name
			}
		}
		return m.runLine(text)
	}
	if components.IsCommandLine(text) {
		return m.runLine(text)
	}
	return m.sendComposer()
}

func (m *Model) completeCommand() {
	c, ok := m.palette.Selected()
	if !ok {
		return
	}
	text := c.Name
	if c.NeedsArg || c.Hint != "" {
		text += " "
	}
	m.composer.SetValue(text)
}

func (m *Model) listKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		m.sidebar.Move(-1)
		return nil
	case "down":
		m.sidebar.Move(1)
		return nil
	case "enter":
		return m.do(m.sidebar.CursorAction())
	case "delete":
		if chat, ok := m.sidebar.CursorChat(); ok {
			return m.confirmDelete(chat.ID, chat.Title)
		}
		return nil
	}
	return m.typeInto(msg)
}

func (m *Model) conversationKey(msg tea.KeyPressMsg) tea.Cmd {
	home := len(m.transcript.Messages) == 0
	key := msg.String()
	if home {
		count := m.cardCount()
		switch key {
		case "up", "left":
			if count > 0 {
				m.cardCursor = (m.cardCursor - 1 + count) % count
			}
			return nil
		case "down", "right":
			if count > 0 {
				m.cardCursor = (m.cardCursor + 1) % count
			}
			return nil
		case "enter":
			return m.activateCard()
		}
		return m.typeInto(msg)
	}

	targets := m.transcript.KeyTargets()
	switch key {
	case "up":
		m.transcript.ScrollUp(1)
		return nil
	case "down":
		m.transcript.ScrollDown(1)
		return nil
	case "home":
		m.transcript.GotoTop()
		return nil
	case "end":
		m.transcript.GotoBottom()
		return nil
	case "left", "right":
		if len(targets) == 0 {
			return nil
		}
		step := 1
		if key == "left" {
			step = -1
		}
		m.transcript.Focus = (m.transcript.Focus + step + len(targets)) % len(targets)
		m.transcript.Rebuild()
		m.transcript.GotoBottom()
		return nil
	case "enter":
		if i := m.transcript.Focus; i >= 0 && i < len(targets) {
			return m.do(targets[i].Action)
		}
		return nil
	}
	return m.typeInto(msg)
}

// typeInto moves printable keys to the composer, so typing always works.
func (m *Model) typeInto(msg tea.KeyPressMsg) tea.Cmd {
	if msg.Text == "" {
		return nil
	}
	cmd := m.setFocus(focusComposer)
	return tea.Batch(cmd, m.composer.Update(msg))
}

func (m *Model) cycleFocus(forward bool) tea.Cmd {
	order := []focusArea{focusComposer}
	if m.listVisible() {
		order = append(order, focusList)
	}
	order = append(order, focusConversation)
	at := 0
	for i, area := range order {
		if area == m.focus {
			at = i
		}
	}
	step := 1
	if !forward {
		step = -1
	}
	next := order[(at+step+len(order))%len(order)]
	m.palette.Close()
	if next == focusList {
		m.sidebar.CursorToCurrent()
	}
	cmd := m.setFocus(next)
	if next == focusConversation {
		m.cardCursor = 0
		if len(m.transcript.KeyTargets()) > 0 {
			m.transcript.Focus = 0
		}
		m.transcript.Rebuild()
	}
	return cmd
}

func (m *Model) listVisible() bool { return m.geo.side.W > 0 || m.sideOverlay }

func (m *Model) toggleSidebar() tea.Cmd {
	if m.width >= m.sideBreakpoint() {
		m.sideHidden = !m.sideHidden
		if m.sideHidden && m.focus == focusList {
			return m.setFocus(focusComposer)
		}
		return nil
	}
	m.sideOverlay = !m.sideOverlay
	if m.sideOverlay {
		m.sidebar.CursorToCurrent()
		return m.setFocus(focusList)
	}
	return m.setFocus(focusComposer)
}

func (m *Model) sideBreakpoint() int { return 90 }

func (m *Model) toggleInspector() {
	if !m.advanced() {
		return
	}
	if m.width >= 120 {
		m.inspectorOff = !m.inspectorOff
		return
	}
	m.inspectorTop = !m.inspectorTop
}

func (m *Model) cycleTheme() tea.Cmd {
	next := map[string]string{
		theme.NameAuto:  theme.NameDark,
		theme.NameDark:  theme.NameLight,
		theme.NameLight: theme.NameAuto,
	}[m.themeSetting]
	return m.setTheme(next)
}

func (m *Model) setTheme(name string) tea.Cmd {
	m.themeSetting = name
	m.applyTheme()
	m.setNotice("Appearance: " + name)
	return m.saveAppearance(config.State{Theme: name})
}

// saveAppearance writes one save at a time. Two at once could finish in the wrong
// order, and on Windows the second rename can fail. Only what the person changed is
// written, so the config file keeps the rest.
func (m *Model) saveAppearance(change config.State) tea.Cmd {
	m.unsaved = m.unsaved.Merge(change)
	if m.saving {
		return nil
	}
	m.saving = true
	cfg, pending := m.cfg, m.unsaved
	m.unsaved = config.State{}
	return func() tea.Msg { return saveMsg{cfg.SaveAppearance(pending)} }
}

// handleClick resolves a left click: overlays first, then the frame's zones, then
// the transcript, whose targets move with its scroll offset.
func (m *Model) handleClick(msg tea.MouseClickMsg) tea.Cmd {
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	x, y := mouse.X, mouse.Y
	m.refreshTargets()

	if m.comp != nil {
		hit := m.comp.Hit(x, y)
		if !hit.Empty() && hit.ID() != layerBase {
			return m.clickZones(m.modalZones, x, y)
		}
		switch {
		case len(m.panels) > 0:
			if m.panels[len(m.panels)-1].kind != panelConfirm {
				return m.closePanel()
			}
			return nil
		case m.sideOverlay:
			m.sideOverlay = false
			return m.setFocus(focusComposer)
		case m.inspectorTop:
			m.inspectorTop = false
			return nil
		}
		m.palette.Close()
	}

	if cmd, ok := m.tryZones(m.zones, x, y); ok {
		return cmd
	}
	if r := m.geo.transcript; r.contains(x, y) {
		if a, ok := m.transcript.Hit(x-r.X, y-r.Y); ok {
			return m.do(a)
		}
	}
	if m.geo.composer.contains(x, y) && m.focus != focusComposer {
		return m.setFocus(focusComposer)
	}
	return nil
}

// refreshTargets redraws, so that hit testing sees the state after the last
// message rather than the last frame sent to the terminal.
func (m *Model) refreshTargets() {
	if m.themeReady && m.width > 0 && m.height > 0 {
		m.draw()
	}
}

func (m *Model) clickZones(zones []views.Zone, x, y int) tea.Cmd {
	cmd, _ := m.tryZones(zones, x, y)
	return cmd
}

func (m *Model) tryZones(zones []views.Zone, x, y int) (tea.Cmd, bool) {
	for i := len(zones) - 1; i >= 0; i-- {
		if zones[i].Contains(x, y) {
			return m.do(zones[i].Action), true
		}
	}
	return nil, false
}

// handleWheel scrolls whatever is under the pointer.
func (m *Model) handleWheel(msg tea.MouseWheelMsg) {
	mouse := msg.Mouse()
	x, y := mouse.X, mouse.Y
	up := mouse.Button == tea.MouseWheelUp
	if !up && mouse.Button != tea.MouseWheelDown {
		return
	}
	delta := 3
	if up {
		delta = -3
	}
	m.refreshTargets()
	if m.comp != nil {
		hit := m.comp.Hit(x, y)
		switch hit.ID() {
		case layerPanel:
			if p := m.topPanel(); p != nil {
				p.scroll = max(p.scroll+delta, 0)
				p.cursor = -1
			}
			return
		case layerPalette:
			m.palette.Move(delta / 3)
			return
		case layerSide:
			m.sidebar.Scroll(delta)
			return
		case layerInspector:
			return
		}
		if len(m.panels) > 0 {
			return
		}
	}
	switch {
	case m.geo.side.contains(x, y):
		m.sidebar.Scroll(delta)
	case m.geo.transcript.contains(x, y):
		if up {
			m.transcript.ScrollUp(3)
		} else {
			m.transcript.ScrollDown(3)
		}
	}
}
