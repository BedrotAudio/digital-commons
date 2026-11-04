package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"digital-commons/terminal/internal/attach"
	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

const advancedOnly = "That is part of Advanced mode. Type /mode developer to switch."

// runLine runs a slash command. The text leaves the composer only when it ran, so
// a mistyped command can be corrected.
func (m *Model) runLine(line string) tea.Cmd {
	word, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	rest = strings.TrimSpace(rest)
	run, known := m.command(word, rest)
	if !known {
		m.setWarning("That command isn't recognized. Type /help to see what's available.")
		return nil
	}
	m.composer.Reset()
	return run()
}

func (m *Model) command(word, arg string) (func() tea.Cmd, bool) {
	if components.IsAdvancedCommand(word) && !m.advanced() {
		return m.note(advancedOnly), true
	}
	simple := func(fn func() tea.Cmd) (func() tea.Cmd, bool) { return fn, true }

	switch word {
	case "/help":
		return simple(func() tea.Cmd { return m.openPanel(&panel{kind: panelHelp}) })
	case "/new", "/clear":
		return simple(m.newChat)
	case "/attach":
		return simple(func() tea.Cmd { return m.attachCommand(arg) })
	case "/search":
		return simple(func() tea.Cmd { return m.searchCommand(arg) })
	case "/privacy":
		return simple(m.openPrivacy)
	case "/status":
		return simple(func() tea.Cmd { return m.openPanel(&panel{kind: panelStatus}) })
	case "/copy":
		return simple(func() tea.Cmd { return m.copyCommand(arg) })
	case "/theme":
		return simple(func() tea.Cmd { return m.themeCommand(arg) })
	case "/mode":
		return simple(func() tea.Cmd { return m.modeCommand(arg) })
	case "/delete":
		return simple(m.deleteCommand)
	case "/exit":
		return simple(m.quit)
	case "/model":
		return simple(func() tea.Cmd { return m.modelCommand(arg) })
	case "/code":
		return simple(func() tea.Cmd { return m.codeCommand() })
	}
	return nil, false
}

func (m *Model) note(text string) func() tea.Cmd {
	return func() tea.Cmd {
		m.setNotice(text)
		return nil
	}
}

// attachmentPath undoes what a shell would: the quotes a Windows terminal puts round
// a dragged path, the backslashes a Mac terminal puts before its spaces, and a
// leading ~. A backslash inside quotes, or on Windows, stays as it is.
func attachmentPath(path, platform, home string) string {
	trimmed := strings.Trim(path, `"'`)
	if platform != "windows" && trimmed == path {
		trimmed = shellEscape.ReplaceAllString(trimmed, "$1")
	}
	if rest, ok := strings.CutPrefix(trimmed, "~"); ok && home != "" && (rest == "" || strings.ContainsRune(`/\`, rune(rest[0]))) {
		return filepath.Join(home, rest)
	}
	return trimmed
}

var shellEscape = regexp.MustCompile(`\\(.)`)

func (m *Model) attachCommand(path string) tea.Cmd {
	path = attachmentPath(path, m.opts.Platform, m.homeDir())
	if path == "" {
		return m.openPicker(1, "")
	}
	return m.readAttachment(path)
}

func (m *Model) readAttachment(path string) tea.Cmd {
	limits := m.limits()
	if len(m.attachments) >= limits.MaxAttachments {
		text := fmt.Sprintf("You can attach up to %d files.", limits.MaxAttachments)
		if m.picker != nil {
			m.picker.Message = text
		} else {
			m.setWarning(text)
		}
		return nil
	}
	options := attach.Limits{Extensions: limits.AttachmentExtensions, MaxBytes: limits.MaxAttachmentBytes}
	return func() tea.Msg {
		file, err := attach.Read(path, options)
		return attachedMsg{file: file, err: err}
	}
}

func (m *Model) searchCommand(question string) tea.Cmd {
	if !m.permissions().SearchDocuments {
		m.refuse("Searching approved documents isn't available for your account.")
		return nil
	}
	if question == "" {
		m.useDocs = !m.useDocs
		m.syncChips()
		return nil
	}
	if m.active != nil {
		m.setNotice(waitForReply)
		text := "/search " + question
		m.composer.SetValue(text)
		m.palette.CloseOn(text)
		return nil
	}
	cmd := m.sendText(question, true)
	m.attachments = nil
	m.syncChips()
	return cmd
}

func (m *Model) copyCommand(arg string) tea.Cmd {
	word, rest, _ := strings.Cut(arg, " ")
	switch {
	case arg == "":
		return m.copyText(m.transcript.LatestReply())
	case word != "code":
		m.setNotice("Type /copy to copy the latest reply.")
		return nil
	case !m.advanced():
		m.setNotice(advancedOnly)
		return nil
	}
	n := 0
	if rest = strings.TrimSpace(rest); rest != "" {
		parsed, err := strconv.Atoi(rest)
		if err != nil || parsed < 1 {
			m.setNotice("Type /copy code and a block number, such as /copy code 1.")
			return nil
		}
		n = parsed
	}
	return m.copyCode(n)
}

func (m *Model) themeCommand(arg string) tea.Cmd {
	switch arg {
	case theme.NameAuto, theme.NameDark, theme.NameLight:
		return m.setTheme(arg)
	}
	m.setNotice("Type /theme auto, /theme dark or /theme light.")
	return nil
}

func (m *Model) modeCommand(arg string) tea.Cmd {
	switch arg {
	case config.ModeBasic:
		return m.switchMode(config.ModeBasic)
	case config.ModeDeveloper, "advanced":
		return m.switchMode(config.ModeDeveloper)
	}
	m.setNotice("Type /mode basic or /mode developer.")
	return nil
}

func (m *Model) deleteCommand() tea.Cmd {
	if m.currentID == "" {
		m.setNotice("There's nothing to delete. This chat hasn't started yet.")
		return nil
	}
	title := ""
	for _, chat := range m.sidebar.Chats {
		if chat.ID == m.currentID {
			title = chat.Title
		}
	}
	return m.confirmDelete(m.currentID, title)
}

func (m *Model) modelCommand(arg string) tea.Cmd {
	if !m.permissions().ManualModelSelection {
		m.setWarning("Your account can't choose a model. Routing stays automatic.")
		return nil
	}
	if arg == "" {
		return m.openModels()
	}
	return m.chooseTier(arg)
}

func (m *Model) chooseTier(tier string) tea.Cmd {
	if !m.tierAllowed(tier) {
		m.setWarning(fmt.Sprintf("%q isn't a tier you can choose. Type /model to see the list.", tier))
		return nil
	}
	m.adv.tier = tier
	if m.active != nil {
		m.setNotice("The next turn will use " + tier + ".")
	} else {
		m.setNotice("Tier: " + tier)
	}
	return nil
}

func (m *Model) codeCommand() tea.Cmd {
	if m.adv.assistant == "code" {
		m.adv.assistant = "general"
		m.setNotice("Coding assistant off.")
	} else {
		m.adv.assistant = "code"
		m.setNotice("Coding assistant on.")
	}
	return nil
}

// attachFile rebuilds a composer file from a request, for a failed send.
func attachFile(a client.Attachment) attach.File {
	return attach.File{Name: a.Name, MediaType: a.MediaType, Text: a.Text}
}

// syncChips rebuilds the composer's chips from the attachments and document mode.
func (m *Model) syncChips() {
	var chips []components.Chip
	for i, f := range m.attachments {
		chips = append(chips, components.Chip{Label: views.OneLine(f.Name), Action: views.Action{Kind: "remove-file", N: i}})
	}
	if m.useDocs {
		chips = append(chips, components.Chip{Label: "Approved documents", Action: views.Action{Kind: "remove-docs"}})
	}
	m.composer.Chips = chips
}

func (m *Model) handleAttached(msg attachedMsg) tea.Cmd {
	if msg.err != nil {
		text := "That file could not be read."
		if errors.Is(msg.err, attach.ErrRefused) {
			text = attach.Message(msg.err)
		}
		if m.picker != nil {
			m.picker.Message = text
		} else {
			m.setWarning(text)
		}
		return nil
	}
	m.attachments = append(m.attachments, msg.file)
	m.syncChips()
	m.notice = ""
	if m.picker == nil {
		return nil
	}
	m.picker.Chosen = append(m.picker.Chosen, msg.file.Name)
	m.picker.Message = ""
	if len(m.picker.Chosen) >= m.picker.Need || len(m.attachments) >= m.limits().MaxAttachments {
		return m.closePanel()
	}
	return nil
}

// placePending puts a suggestion's text in the composer for the user to complete.
func (m *Model) placePending() tea.Cmd {
	if m.pendingText != "" && m.composer.Value() == "" {
		m.composer.SetValue(m.pendingText)
	}
	m.pendingText = ""
	return m.setFocus(focusComposer)
}

func (m *Model) confirmDelete(id, title string) tea.Cmd {
	name := "This chat"
	if title != "" {
		name = fmt.Sprintf("%q", title)
	}
	return m.openPanel(&panel{kind: panelConfirm, confirm: &confirmSpec{
		title:  "Delete this chat?",
		body:   name + " and everything in it will be removed. This can't be undone.",
		label:  "Delete",
		action: views.Action{Kind: "delete-now", Arg: id},
	}})
}

func (m *Model) confirmDeleteAll() tea.Cmd {
	return m.openPanel(&panel{kind: panelConfirm, confirm: &confirmSpec{
		title:  "Delete all chats?",
		body:   "Every chat you have will be removed. This can't be undone.",
		label:  "Delete all",
		action: views.Action{Kind: "delete-all-now"},
	}})
}

func (m *Model) deleteNow(id string) tea.Cmd {
	c := m.client
	do := func() tea.Msg { return deletedMsg{err: c.DeleteConversation(context.Background(), id)} }
	if id == m.currentID {
		return m.stopThen(func() tea.Cmd { return do })
	}
	return do
}

func (m *Model) deleteAllNow() tea.Cmd {
	c := m.client
	return m.stopThen(func() tea.Cmd {
		return func() tea.Msg {
			n, err := c.DeleteAllConversations(context.Background())
			return deletedAllMsg{count: n, err: err}
		}
	})
}
