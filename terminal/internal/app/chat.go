package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/security"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// waitStream reads one event in a tea.Cmd, where a panic is recovered and the
// terminal restored. A bare goroutine would leave the terminal in a bad state.
func waitStream(ch <-chan client.StreamEvent, id int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamEndMsg{id}
		}
		return streamMsg{turn: id, ev: ev}
	}
}

const waitForReply = "Wait for this reply to finish, or stop it first."

func tickCmd(id int) tea.Cmd {
	return tea.Tick(renderEvery, func(time.Time) tea.Msg { return tickMsg{id} })
}

// begin opens the stream for a turn. StreamChat returns at once, and the reads
// happen in a chain of tea.Cmd values, one per event.
func (m *Model) begin(t *turn) tea.Cmd {
	m.notice = ""
	m.turnSeq++
	t.id = m.turnSeq
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	m.active = t
	m.last = t
	m.opening = "" // the turn belongs to the chat on screen, so a late fetch is dropped
	m.composer.Busy = true
	m.transcript.Focus = -1
	m.transcript.GotoBottom()
	m.transcript.Rebuild()

	t.stream = m.client.StreamChat(ctx, t.req)
	return tea.Batch(waitStream(t.stream, t.id), tickCmd(t.id))
}

// tierAndAssistant returns what this mode sends. Basic is always auto and general.
func (m *Model) tierAndAssistant() (string, string) {
	if m.advanced() {
		return m.adv.tier, m.adv.assistant
	}
	return "auto", "general"
}

func (m *Model) conversationRef() *string {
	if m.currentID == "" {
		return nil
	}
	id := m.currentID
	return &id
}

// sendComposer sends what is in the composer.
func (m *Model) sendComposer() tea.Cmd {
	text := strings.TrimRight(m.composer.Value(), " \n")
	if strings.TrimSpace(text) == "" && len(m.attachments) == 0 {
		return nil
	}
	if m.active != nil {
		m.setNotice(waitForReply)
		return nil
	}
	limit := m.limits().MaxMessageChars
	if count := utf8.RuneCountInString(text); limit > 0 && count > limit {
		m.setWarning(fmt.Sprintf("That message is too long: %d characters, and the limit is %d. Please shorten it.", count, limit))
		return nil
	}
	cmd := m.sendText(text, m.useDocs)
	m.composer.Reset()
	m.attachments = nil
	m.syncChips()
	return tea.Batch(cmd, m.setFocus(focusComposer))
}

// sendText starts a normal turn with the attachments in the composer.
func (m *Model) sendText(text string, docs bool) tea.Cmd {
	m.dropUnsent()
	tier, assistant := m.tierAndAssistant()
	req := client.ChatRequest{
		ConversationID: m.conversationRef(),
		Message:        text,
		Tier:           tier,
		Assistant:      assistant,
		UseDocuments:   docs,
	}
	var names []string
	for _, f := range m.attachments {
		req.Attachments = append(req.Attachments, client.Attachment{Name: f.Name, MediaType: f.MediaType, Text: f.Text})
		names = append(names, f.Name)
	}
	user := m.transcript.Add(&components.Message{Role: "user", Text: text, Attachments: names, Time: m.now()})
	reply := m.newReply()
	return m.begin(&turn{req: req, user: user, reply: reply})
}

func (m *Model) newReply() *components.Message {
	return m.transcript.Add(&components.Message{Role: "assistant", Streaming: true, Waiting: m.now(), Time: m.now(), ThinkingOpen: true})
}

// dropUnsent removes a turn that failed before it was stored, so resending from
// the composer does not show the same message twice.
func (m *Model) dropUnsent() {
	t := m.last
	if t == nil || t.started || t.user == nil || m.active != nil {
		return
	}
	messages := m.transcript.Messages
	if len(messages) >= 2 && messages[len(messages)-1] == t.reply && messages[len(messages)-2] == t.user {
		if t.reply.Fail != nil && !t.reply.CanRetry {
			m.transcript.Messages = messages[:len(messages)-2]
		}
	}
}

// retry is Try again. Before start nothing was stored, so the same request goes
// again. That holds after a Stop too, because the gateway writes nothing for a
// caller that has gone before start. After start it means regenerate.
func (m *Model) retry() tea.Cmd {
	if m.active != nil {
		return nil
	}
	last := m.transcript.Last()
	if last == nil || last.Role != "assistant" || !last.CanRetry {
		return nil
	}
	sourced := len(last.Citations) > 0 && m.permissions().SearchDocuments
	replaced := m.takeReplies()

	if m.last != nil && !m.last.started && !m.last.req.Regenerate {
		req := m.last.req
		req.Tier, req.Assistant = m.tierAndAssistant()
		reply := m.newReply()
		return m.begin(&turn{req: req, user: m.last.user, reply: reply})
	}

	tier, assistant := m.tierAndAssistant()
	// A chat opened from history has no request to copy. A reply with sources came
	// from the approved documents, and regenerate must not silently replace it with
	// one that did not.
	docs := m.useDocs || sourced
	if m.last != nil {
		docs = m.last.req.UseDocuments
	}
	req := client.ChatRequest{
		ConversationID: m.conversationRef(),
		Tier:           tier,
		Assistant:      assistant,
		UseDocuments:   docs,
		Regenerate:     true,
	}
	reply := m.newReply()
	return m.begin(&turn{req: req, reply: reply, replaced: replaced})
}

// takeReplies removes everything after the last user message and returns the newest
// reply with text. A failed regenerate can leave the old reply and the failure side
// by side, and the next attempt replaces both.
func (m *Model) takeReplies() *components.Message {
	messages := m.transcript.Messages
	cut := len(messages)
	for cut > 0 && messages[cut-1].Role == "assistant" {
		cut--
	}
	var kept *components.Message
	for _, msg := range messages[cut:] {
		if strings.TrimSpace(msg.Text) != "" {
			kept = msg
		}
	}
	m.transcript.Messages = messages[:cut]
	return kept
}

// stopThen stops any reply, then runs next once the stream has closed. With no
// reply it runs next at once.
func (m *Model) stopThen(next func() tea.Cmd) tea.Cmd {
	t := m.active
	if t == nil {
		return next()
	}
	t.then = append(t.then, next)
	if !t.stopping {
		t.stopping = true
		t.cancel()
	}
	return nil
}

func (m *Model) stop() {
	if t := m.active; t != nil && !t.stopping {
		t.stopping = true
		t.cancel()
	}
}

func (m *Model) handleTick(msg tickMsg) tea.Cmd {
	t := m.active
	if t == nil || t.id != msg.turn {
		return nil
	}
	if m.dirty || strings.TrimSpace(t.reply.Text) == "" {
		m.dirty = false
		m.transcript.Rebuild()
	}
	return tickCmd(t.id)
}

func (m *Model) updateChat(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case streamMsg:
		t := m.active
		if t == nil || t.id != msg.turn {
			return nil, true
		}
		return m.handleEvent(t, msg.ev), true
	case streamEndMsg:
		t := m.active
		if t == nil || t.id != msg.turn {
			return nil, true
		}
		return m.finishStopped(t), true
	case conversationMsg:
		return m.handleConversation(msg), true
	case deletedMsg:
		if msg.err != nil {
			m.setWarning(m.errorText(msg.err, "That chat could not be deleted."))
			return m.conversationsCmd(), true
		}
		m.setNotice("Chat deleted.")
		return tea.Batch(m.conversationsCmd(), m.leaveForHome()), true
	case deletedAllMsg:
		if msg.err != nil {
			m.setWarning(m.errorText(msg.err, "Your chats could not be deleted."))
			return nil, true
		}
		m.setNotice(fmt.Sprintf("Deleted %d chats.", msg.count))
		return tea.Batch(m.conversationsCmd(), m.leaveForHome()), true
	}
	return nil, false
}

func (m *Model) errorText(err error, fallback string) string {
	if e, ok := err.(*client.Error); ok && e.Message != "" && !m.advanced() {
		return views.PlainError(e.Code, e.Message)
	}
	if e, ok := err.(*client.Error); ok && e.Message != "" {
		return e.Message
	}
	return fallback
}

func (m *Model) handleEvent(t *turn, ev client.StreamEvent) tea.Cmd {
	reply := t.reply
	switch ev := ev.(type) {
	case client.StartEvent:
		t.started = true
		reply.ID = ev.MessageID
		route := ev.Route
		reply.Route = &route
		contextInfo := ev.Context
		reply.Context = &contextInfo
		m.lastContext = &contextInfo
		reply.Notices = ev.Notices
		m.currentID = ev.ConversationID
		m.sidebar.Current = ev.ConversationID
		m.upsertChat(ev)
		m.dirty = true
	case client.CitationsEvent:
		reply.Citations = ev.Citations
	case client.NoticesEvent:
		reply.Notices = ev.Notices
		m.dirty = true
	case client.StatusEvent:
		reply.Status = ev.Message
		m.dirty = true
	case client.ThinkingEvent:
		reply.Thinking += ev.Text
		m.dirty = true
	case client.DeltaEvent:
		reply.Text += ev.Text
		m.dirty = true
	case client.DoneEvent:
		return m.finishDone(t, ev)
	case client.ErrorEvent:
		return m.finishError(t, ev.Err)
	case client.LostEvent:
		return m.finishLost(t, ev.Err)
	}
	return m.nextEvent(t)
}

func (m *Model) nextEvent(t *turn) tea.Cmd { return waitStream(t.stream, t.id) }

func (m *Model) upsertChat(ev client.StartEvent) {
	now := m.now().UTC().Format(time.RFC3339)
	for i, chat := range m.sidebar.Chats {
		if chat.ID == ev.ConversationID {
			chat.UpdatedAt = now
			if ev.Title != "" {
				chat.Title = ev.Title
			}
			copy(m.sidebar.Chats[1:i+1], m.sidebar.Chats[:i])
			m.sidebar.Chats[0] = chat
			return
		}
	}
	m.sidebar.Chats = append([]client.ConversationSummary{{ID: ev.ConversationID, Title: ev.Title, CreatedAt: now, UpdatedAt: now}}, m.sidebar.Chats...)
}

func (m *Model) closeTurn(t *turn) tea.Cmd {
	t.reply.Streaming = false
	t.reply.ThinkingOpen = false
	t.reply.Touch()
	t.cancel()
	m.active = nil
	m.dirty = false
	m.composer.Busy = false
	if m.notice == waitForReply {
		m.notice = ""
	}
	m.transcript.Rebuild()
	if answered(t.reply) {
		m.answers++
	}
	cmds := []tea.Cmd{m.conversationsCmd()}
	if m.conn == connUp {
		// The status in hand can be a poll old, and the reply may have just proved
		// the assistant ready, or the connection may have just failed.
		cmds = append(cmds, m.statusCmd())
	}
	for _, then := range t.then {
		cmds = append(cmds, then())
	}
	return tea.Batch(cmds...)
}

func (m *Model) finishDone(t *turn, ev client.DoneEvent) tea.Cmd {
	reply := t.reply
	reply.ID = ev.MessageID
	usage := ev.Usage
	reply.Usage = &usage
	reply.CanRetry = true
	blank := strings.TrimSpace(reply.Text) == ""
	switch {
	case ev.FinishReason == "length" && blank:
		reply.Note = views.NoRoom
	case ev.FinishReason == "length":
		reply.Note = views.CutShort
	case blank:
		reply.Note = views.NoReply
	}
	m.restoreReplaced(t)
	return m.closeTurn(t)
}

// finishError shows the failure inline. Before start nothing was stored, so a
// failure that cannot be retried returns the text and files to the composer.
func (m *Model) finishError(t *turn, err *client.Error) tea.Cmd {
	reply := t.reply
	reply.Fail = &components.Failure{
		Code:    err.Code,
		Message: err.Message,
		Detail:  err.Detail,
		NewChat: err.Code == "conversation_locality_changed",
	}
	reply.CanRetry = err.Retryable
	auth := client.IsAuth(err)
	if auth {
		reply.Fail.Message = views.NoAccess
		m.setDown(err)
	}
	m.restoreReplaced(t)
	if !t.started && !err.Retryable {
		m.restoreDraft(t)
	}
	if auth {
		return tea.Batch(m.closeTurn(t), m.bootstrapCmd())
	}
	return m.closeTurn(t)
}

func (m *Model) finishLost(t *turn, err *client.Error) tea.Cmd {
	reply := t.reply
	reply.CanRetry = true
	m.restoreReplaced(t)
	if t.started {
		reply.Note = views.Lost
	} else {
		reply.Fail = &components.Failure{Code: "connection_failed", Message: m.unreachableText(), Detail: connDetail(err)}
	}
	return m.closeTurn(t) // which asks for the status, to learn whether the gateway is down
}

func (m *Model) unreachableText() string {
	if m.loopback() {
		return views.NotRunning
	}
	return views.Unreachable
}

// finishStopped handles the stream closing with no final event, which happens
// only after the app cancelled it.
func (m *Model) finishStopped(t *turn) tea.Cmd {
	t.reply.Note = views.Stopped
	t.reply.CanRetry = true
	m.restoreReplaced(t)
	return m.closeTurn(t)
}

// restoreReplaced puts the previous reply back when regenerate produced nothing,
// because the gateway keeps it in that case.
func (m *Model) restoreReplaced(t *turn) {
	if t.replaced == nil || strings.TrimSpace(t.reply.Text) != "" {
		return
	}
	messages := m.transcript.Messages
	last := len(messages) - 1
	if last >= 0 && messages[last] == t.reply {
		m.transcript.Messages = append(messages[:last:last], t.replaced, t.reply)
	}
	t.replaced.CanRetry = false
	t.replaced = nil
}

// restoreDraft returns a refused message to the composer, ahead of anything typed
// since, because dropUnsent takes it off the screen at the next send.
func (m *Model) restoreDraft(t *turn) {
	switch typed := m.composer.Value(); {
	case t.req.Message == "":
	case typed == "":
		m.composer.SetValue(t.req.Message)
	default:
		m.composer.SetValue(t.req.Message + "\n" + typed)
	}
	for _, a := range t.req.Attachments {
		m.attachments = append(m.attachments, attachFile(a))
	}
	m.syncChips()
}

// goHome shows the empty home screen. A reply must already be stopped.
func (m *Model) goHome() {
	m.transcript.Clear()
	m.currentID = ""
	m.opening = ""
	m.sidebar.Current = ""
	m.last = nil
	m.lastContext = nil
	m.useDocs = false
	m.syncChips()
	m.cardCursor = 0
	m.sideOverlay = false
}

func (m *Model) newChat() tea.Cmd { return m.leaveForHome() }

// leaveForHome stops any reply, keeping its partial text, then shows home. It is
// the one route out of a chat, so a reply is never left running with no view.
func (m *Model) leaveForHome() tea.Cmd {
	return m.stopThen(func() tea.Cmd {
		m.goHome()
		return m.setFocus(focusComposer)
	})
}

func (m *Model) open(id string) tea.Cmd {
	if id == m.currentID && len(m.transcript.Messages) > 0 {
		m.sideOverlay = false
		return m.setFocus(focusComposer)
	}
	m.opening = id
	return m.stopThen(func() tea.Cmd {
		c := m.client
		return func() tea.Msg {
			conv, err := c.Conversation(context.Background(), id)
			return conversationMsg{id: id, conv: conv, err: err}
		}
	})
}

// handleConversation restores a chat from history.
func (m *Model) handleConversation(msg conversationMsg) tea.Cmd {
	if msg.id != m.opening {
		return nil
	}
	m.opening = ""
	if msg.err != nil {
		m.setWarning(m.errorText(msg.err, "That chat could not be opened."))
		return m.conversationsCmd()
	}
	conv := msg.conv
	m.transcript.Clear()
	m.last = nil
	m.lastContext = nil
	m.currentID = conv.ID
	m.sidebar.Current = conv.ID
	m.sideOverlay = false
	if m.advanced() && conv.Assistant != "" {
		m.adv.assistant = conv.Assistant
	}
	for _, stored := range conv.Messages {
		m.transcript.Add(restored(stored))
	}
	if last := m.transcript.Last(); last != nil {
		if last.Role == "user" {
			m.transcript.Add(&components.Message{Role: "assistant", Note: views.NoReplySaved, CanRetry: true})
		} else {
			last.CanRetry = true
		}
	}
	m.transcript.GotoBottom()
	m.transcript.Rebuild()
	m.transcript.GotoBottom()
	return m.setFocus(focusComposer)
}

func restored(stored client.Message) *components.Message {
	msg := &components.Message{
		ID:        stored.ID,
		Role:      stored.Role,
		Text:      stored.Content,
		Citations: stored.Citations,
		Notices:   stored.Notices,
		Route:     stored.Route,
		Usage:     stored.Usage,
	}
	if t, err := client.ParseTime(stored.CreatedAt); err == nil {
		msg.Time = t
	}
	for _, a := range stored.Attachments {
		msg.Attachments = append(msg.Attachments, a.Name)
	}
	if stored.Role == "assistant" {
		switch stored.FinishReason {
		case "cancelled":
			msg.Note = views.Stopped
		case "length":
			msg.Note = views.CutShort
		case "error":
			msg.Note = views.Interrupted
		}
	}
	return msg
}

func (m *Model) copyText(text string) tea.Cmd {
	if text == "" {
		m.setNotice("There's nothing to copy yet.")
		return nil
	}
	write := m.opts.Clipboard.Write
	text = security.Clean(text)
	return func() tea.Msg {
		return clipboardMsg{text: text, err: write(text)}
	}
}

// copyLatest copies the latest reply, or in Advanced the latest code block.
func (m *Model) copyLatest() tea.Cmd {
	if m.advanced() {
		if blocks := m.transcript.CodeBlocks(); len(blocks) > 0 {
			return m.copyText(blocks[len(blocks)-1].Text)
		}
	}
	return m.copyText(m.transcript.LatestReply())
}

func (m *Model) copyCode(n int) tea.Cmd {
	blocks := m.transcript.CodeBlocks()
	if len(blocks) == 0 {
		m.setNotice("There are no code blocks in this chat.")
		return nil
	}
	if n <= 0 {
		n = len(blocks)
	}
	if n > len(blocks) {
		m.setNotice(fmt.Sprintf("There are only %d code blocks in this chat.", len(blocks)))
		return nil
	}
	return m.copyText(blocks[n-1].Text)
}

func (m *Model) setMode(mode string) {
	m.mode = mode
	adv := m.advanced()
	m.composer.Advanced = adv
	m.transcript.Advanced = adv
	m.transcript.Focus = -1
	m.palette.SetAdvanced(adv)
	m.sidebar.Dense = adv
	m.sidebar.Offset = 0
	m.cardCursor = 0
	m.sideOverlay = false
	m.inspectorTop = false
	m.relayout()
	m.transcript.Rebuild()
}

// switchMode applies /mode, refusing Advanced without the permission.
func (m *Model) switchMode(target string) tea.Cmd {
	if target == config.ModeDeveloper && !m.permissions().AdvancedMode {
		m.refuse("Advanced mode isn't available for your account.")
		return nil
	}
	if target == m.mode {
		return nil
	}
	m.setMode(target)
	if m.advanced() {
		m.setNotice("Advanced mode. Type /mode basic to go back.")
		return m.refreshModels()
	}
	m.setNotice("Basic mode.")
	return nil
}
