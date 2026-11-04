package app

import (
	"path/filepath"
	"strings"

	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// do runs an action from a click, or from Enter on a focused item.
func (m *Model) do(a views.Action) tea.Cmd {
	switch a.Kind {
	case "new":
		return m.newChat()
	case "open":
		return m.open(a.Arg)
	case "delete-chat":
		title := ""
		for _, chat := range m.sidebar.Chats {
			if chat.ID == a.Arg {
				title = chat.Title
			}
		}
		return m.confirmDelete(a.Arg, title)
	case "privacy":
		return m.openPrivacy()
	case "settings":
		return m.openPanel(&panel{kind: panelSettings})
	case "help":
		return m.openPanel(&panel{kind: panelHelp})
	case "status":
		return m.openPanel(&panel{kind: panelStatus})
	case "attach":
		return m.openPicker(1, "")
	case "send":
		return m.enter()
	case "stop":
		m.stop()
		return nil
	case "copy":
		return m.copyText(m.transcript.LatestReply())
	case "retry":
		return m.retry()
	case "copy-code":
		return m.copyCode(a.N)
	case "reasoning":
		return m.toggleReasoning(a.N)
	case "suggest":
		return m.suggest(a.N)
	case "bottom":
		m.transcript.GotoBottom()
		return nil
	case "more-cards":
		m.cardPage++
		m.cardCursor = 0
		return nil
	case "toggle-side":
		return m.toggleSidebar()
	case "toggle-inspector":
		m.toggleInspector()
		return nil
	case "remove-file":
		if a.N < len(m.attachments) {
			m.attachments = append(m.attachments[:a.N:a.N], m.attachments[a.N+1:]...)
			m.syncChips()
		}
		return nil
	case "remove-docs":
		m.useDocs = false
		m.syncChips()
		return nil
	case "palette":
		return m.pickCommand(a.N)
	case "run":
		return m.runLine(a.Arg)
	case "panel.close":
		if len(m.panels) == 0 {
			m.inspectorTop = false
			return nil
		}
		return m.closePanel()
	case "set-theme":
		return m.setTheme(a.Arg)
	case "set-mouse":
		m.mouse = a.Arg == "on"
		mouse := m.mouse
		return m.saveAppearance(config.State{Mouse: &mouse})
	case "set-mode":
		return m.switchMode(a.Arg)
	case "delete-all":
		return m.confirmDeleteAll()
	case "confirm.cancel":
		return m.closePanel()
	case "delete-now":
		cmd := m.closePanels()
		return tea.Batch(cmd, m.deleteNow(a.Arg))
	case "delete-all-now":
		cmd := m.closePanels()
		return tea.Batch(cmd, m.deleteAllNow())
	case "set-tier":
		m.chooseTier(a.Arg)
		return m.closePanel()
	case "picker.entry":
		return m.pickEntry(a.N)
	case "picker.up":
		return m.loadDir(filepath.Dir(m.picker.Dir))
	case "picker.drive":
		return m.loadDir(a.Arg)
	}
	return nil
}

func (m *Model) toggleReasoning(uid int) tea.Cmd {
	for _, msg := range m.transcript.Messages {
		if msg.UID == uid {
			msg.ThinkingOpen = !msg.ThinkingOpen
			msg.Touch()
		}
	}
	m.transcript.Rebuild()
	return nil
}

func (m *Model) pickCommand(i int) tea.Cmd {
	if i >= len(m.palette.Items) {
		return nil
	}
	m.palette.Cursor = i
	c := m.palette.Items[i]
	if c.NeedsArg {
		m.completeCommand()
		return m.setFocus(focusComposer)
	}
	return m.runLine(c.Name)
}

// suggest applies a suggestion card.
func (m *Model) suggest(i int) tea.Cmd {
	if m.boot == nil || i >= len(m.boot.Suggestions) {
		return nil
	}
	s := m.boot.Suggestions[i]
	switch s.Action {
	case "attach":
		return m.openPicker(1, s.Prompt)
	case "attach_two":
		return m.openPicker(2, s.Prompt)
	case "documents":
		if !m.permissions().SearchDocuments {
			m.refuse("Searching approved documents isn't available for your account.")
			return nil
		}
		m.useDocs = true
		m.syncChips()
	}
	m.pendingText = s.Prompt
	return m.placePending()
}

func (m *Model) pickEntry(i int) tea.Cmd {
	p := m.picker
	if p == nil || i >= len(p.Entries) {
		return nil
	}
	entry := p.Entries[i]
	path := filepath.Join(p.Dir, entry.Name)
	if entry.Dir {
		return m.loadDir(path)
	}
	return m.readAttachment(path)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
