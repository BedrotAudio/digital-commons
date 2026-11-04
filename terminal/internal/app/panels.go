package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

const (
	panelHelp     = "help"
	panelPrivacy  = "privacy"
	panelSettings = "settings"
	panelStatus   = "status"
	panelModels   = "models"
	panelFiles    = "files"
	panelConfirm  = "confirm"
)

type confirmSpec struct {
	title, body, label string
	action             views.Action
}

type panel struct {
	kind    string
	cursor  int
	scroll  int
	loading bool
	message string // a load failure
	confirm *confirmSpec
}

func (m *Model) topPanel() *panel {
	if len(m.panels) == 0 {
		return nil
	}
	return m.panels[len(m.panels)-1]
}

// openPanel shows a panel. Opening the one already on top closes it.
func (m *Model) openPanel(p *panel) tea.Cmd {
	if top := m.topPanel(); top != nil && top.kind == p.kind && p.kind != panelConfirm {
		return m.closePanel()
	}
	m.palette.Close()
	m.sideOverlay = false
	m.panels = append(m.panels, p)
	m.composer.Blur()
	return nil
}

// closePanel closes the top panel and returns focus to the composer when it was
// the last. A file panel closing places any suggestion text.
func (m *Model) closePanel() tea.Cmd {
	top := m.topPanel()
	if top == nil {
		return nil
	}
	m.panels = m.panels[:len(m.panels)-1]
	if top.kind == panelFiles {
		chosen := m.picker != nil && len(m.picker.Chosen) > 0
		m.picker = nil
		if !chosen {
			m.pendingText = ""
		}
		if len(m.panels) == 0 {
			return m.placePending()
		}
	}
	if len(m.panels) == 0 {
		return m.setFocus(focusComposer)
	}
	return nil
}

func (m *Model) closePanels() tea.Cmd {
	var cmd tea.Cmd
	for len(m.panels) > 0 {
		cmd = m.closePanel()
	}
	return cmd
}

func (m *Model) openPrivacy() tea.Cmd {
	if cmd := m.openPanel(&panel{kind: panelPrivacy, loading: true, cursor: -1}); m.topPanel() == nil || m.topPanel().kind != panelPrivacy {
		return cmd
	}
	c := m.client
	return func() tea.Msg {
		p, err := c.Privacy(context.Background())
		return privacyMsg{privacy: p, err: err}
	}
}

func (m *Model) openModels() tea.Cmd {
	if cmd := m.openPanel(&panel{kind: panelModels, loading: true}); m.topPanel() == nil || m.topPanel().kind != panelModels {
		return cmd
	}
	return m.modelsCmd()
}

func (m *Model) modelsCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		models, err := c.Models(context.Background())
		return modelsMsg{models: models, err: err}
	}
}

// openPicker opens the file panel. text waits for the files to be chosen.
func (m *Model) openPicker(need int, text string) tea.Cmd {
	if top := m.topPanel(); top != nil && top.kind == panelFiles {
		return nil
	}
	limits := m.limits()
	dir := m.homeDir()
	m.picker = &components.FilePicker{
		Dir:        dir,
		Extensions: limits.AttachmentExtensions,
		Drives:     components.Drives(m.opts.Platform),
		Need:       need,
	}
	m.pendingText = text
	cmd := m.openPanel(&panel{kind: panelFiles})
	return tea.Batch(cmd, m.loadDir(dir))
}

func (m *Model) homeDir() string {
	if m.opts.HomeDir != "" {
		return m.opts.HomeDir
	}
	dir, _ := os.UserHomeDir()
	return dir
}

func (m *Model) loadDir(dir string) tea.Cmd {
	extensions := m.limits().AttachmentExtensions
	return func() tea.Msg {
		entries, err := components.ReadDir(dir, extensions)
		return dirMsg{dir: dir, entries: entries, err: err}
	}
}

func (m *Model) updatePanels(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case privacyMsg:
		m.applyLoad(panelPrivacy, msg.err, func() { m.privacy = msg.privacy })
		return nil, true
	case modelsMsg:
		m.applyLoad(panelModels, msg.err, func() { m.models = msg.models })
		return nil, true
	case dirMsg:
		if m.picker == nil {
			return nil, true
		}
		if msg.err != nil {
			m.picker.Message = views.FolderRefused(m.opts.Platform, errors.Is(msg.err, fs.ErrPermission))
			return nil, true
		}
		m.picker.Dir, m.picker.Entries, m.picker.Message = msg.dir, msg.entries, ""
		if p := m.topPanel(); p != nil && p.kind == panelFiles {
			p.cursor, p.scroll = m.picker.FirstEntry(), 0
		}
		return nil, true
	case attachedMsg:
		return m.handleAttached(msg), true
	case clipboardMsg:
		if msg.err != nil {
			// This asks the terminal, which gives no answer, so it may have done nothing.
			m.setNotice(views.CopyUnconfirmed)
			return tea.SetClipboard(msg.text), true
		}
		m.setNotice("Copied.")
		return nil, true
	case saveMsg:
		m.saving = false
		if m.unsaved != (config.State{}) {
			return m.saveAppearance(config.State{}), true
		}
		if msg.err != nil {
			m.setWarning("Your appearance choice could not be saved.")
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) applyLoad(kind string, err error, ok func()) {
	for _, p := range m.panels {
		if p.kind != kind {
			continue
		}
		p.loading = false
		if err != nil {
			p.message = m.errorText(err, "That information isn't available right now.")
			if !m.connected() || client.IsNetwork(err) {
				p.message = m.unreachableText()
			}
		}
	}
	if err == nil {
		ok()
	}
}

// panelKey routes keys while a panel is open. Esc always closes.
func (m *Model) panelKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.topPanel()
	built := m.buildPanel(p)
	controls := built.Controls()
	key := msg.String()
	scroll := func(d int) {
		p.scroll = max(p.scroll+d, 0)
		p.cursor = -1
	}
	step := func(d int) {
		if len(controls) == 0 {
			scroll(d)
			return
		}
		p.cursor = (p.cursor + d + len(controls)) % len(controls)
	}
	reading := p.kind == panelHelp || p.kind == panelStatus || p.kind == panelPrivacy
	switch key {
	case "esc":
		return m.closePanel()
	case "f1":
		if p.kind == panelHelp {
			return m.closePanel()
		}
		return m.openPanel(&panel{kind: panelHelp})
	case "f2":
		if p.kind == panelSettings {
			return m.closePanel()
		}
		return m.openPanel(&panel{kind: panelSettings})
	case "ctrl+t":
		return m.cycleTheme()
	case "up":
		if reading {
			scroll(-1)
		} else {
			step(-1)
		}
	case "down":
		if reading {
			scroll(1)
		} else {
			step(1)
		}
	case "shift+tab", "left":
		step(-1)
	case "tab", "right":
		step(1)
	case "pgup":
		scroll(-m.geo.panelPage)
	case "pgdown":
		scroll(m.geo.panelPage)
	case "backspace":
		if p.kind == panelFiles && m.picker != nil {
			return m.do(views.Action{Kind: "picker.up"})
		}
	case "enter", "space":
		if p.cursor >= 0 && p.cursor < len(controls) {
			return m.do(controls[p.cursor])
		}
	}
	return nil
}

const (
	widthText  = 72
	widthSmall = 56
)

// buildPanel turns panel state into rows. It runs on every frame, so status and
// privacy text are always current.
func (m *Model) buildPanel(p *panel) views.Panel {
	switch p.kind {
	case panelHelp:
		return m.helpPanel(p)
	case panelPrivacy:
		return m.privacyPanel(p)
	case panelSettings:
		return m.settingsPanel(p)
	case panelStatus:
		return m.statusPanel(p)
	case panelModels:
		return m.modelsPanel(p)
	case panelFiles:
		return m.filesPanel(p)
	}
	return m.confirmPanel(p)
}

func base(p *panel, title string, width int) views.Panel {
	return views.Panel{Title: title, Cursor: p.cursor, Scroll: p.scroll, Width: width}
}

func (m *Model) newlineKeys() string {
	if m.shiftEnter {
		return "Shift+Enter or Ctrl+J"
	}
	return "Ctrl+J"
}

func (m *Model) helpPanel(p *panel) views.Panel {
	out := base(p, "Help", widthText)
	st := m.th.Styles
	inner := views.PanelInner(widthText, m.width)
	add := func(rows ...views.Row) { out.Rows = append(out.Rows, rows...) }
	heading := func(text string) {
		add(views.Row{{Text: "", Style: st.Muted}}, views.Row{{Text: text, Style: st.Text.Bold(true)}})
	}

	add(views.TextRows("Digital Commons helps with writing, research and understanding documents. Type a question in plain language and press Enter.", inner, st.Text)...)

	heading("Keys")
	keys := [][2]string{
		{"Enter", "Send, or choose the highlighted item"},
		{m.newlineKeys(), "Start a new line"},
		{"Ctrl+V", "Paste"},
		{"Esc", "Close a list, then a panel, then stop the reply"},
		{"Ctrl+N", "New chat"},
		{"Ctrl+B", "Show or hide the chat list"},
		{"Ctrl+O", "Attach a file"},
		{"Ctrl+Y", "Copy the latest reply"},
		{"Ctrl+R", "Try again"},
		{"Delete", "Delete the highlighted chat, after asking"},
		{"Backspace", "In an empty message box, remove the last file"},
		{"Ctrl+T", "Change the appearance"},
		{"Tab", "Move between the message box, chats and conversation"},
		{"PgUp, PgDn", "Scroll the conversation"},
		{"F1, F2", "Help, Settings"},
		{"Ctrl+C", "Stop a reply. When idle, press twice to quit"},
	}
	if m.advanced() {
		keys = append(keys, [2]string{"F3", "Inspector"}, [2]string{"Ctrl+Y", "Advanced: copies the latest code block"})
	}
	keyWidth := min(24, inner/3+2)
	for _, pair := range keys {
		add(views.Pair(pair[0], pair[1], keyWidth, inner, st.Accent, st.Text)...)
	}

	heading("Attaching a file")
	exts := strings.Join(m.limits().AttachmentExtensions, " ")
	add(views.TextRows("Select the + beside the message box, press Ctrl+O, or type /attach. Accepted types: "+exts+".", inner, st.Text)...)

	if m.permissions().AdvancedMode {
		heading("Basic and Advanced modes")
		add(views.TextRows("Type /mode developer for more detail and controls, and /mode basic to come back.", inner, st.Text)...)
	}

	heading("Selecting text")
	add(views.TextRows(views.SelectText(m.opts.Platform, m.opts.TermProgram), inner, st.Text)...)

	if m.boot != nil && m.boot.Application.Notice != "" {
		heading("About this version")
		add(views.TextRows(m.boot.Application.Notice, inner, st.Warn)...)
	}
	add(views.Row{{Text: "", Style: st.Muted}})
	add(views.Row{{Text: "Digital Commons " + m.opts.Version, Style: st.Muted}})
	out.Cursor = -1
	return out
}

func statusMark(status string) string {
	switch status {
	case "ok":
		return "●"
	case "warn":
		return "!"
	}
	return "○"
}

func (m *Model) privacyPanel(p *panel) views.Panel {
	out := base(p, "Privacy", widthText)
	st := m.th.Styles
	inner := views.PanelInner(widthText, m.width)
	add := func(rows ...views.Row) { out.Rows = append(out.Rows, rows...) }

	switch {
	case p.message != "":
		add(views.TextRows(p.message, inner, st.Warn)...)
	case p.loading || m.privacy == nil:
		add(views.Row{{Text: "One moment…", Style: st.Muted}})
	default:
		_, label := m.locality()
		description := m.privacy.Locality.Description
		if !m.loopback() && m.privacy.Locality.Kind == "this_device" {
			description = "Your conversations are processed by your organization's service."
		}
		text := func(s string) string {
			if m.advanced() {
				return s
			}
			return views.PlainOr(s, "")
		}
		add(views.Row{{Text: label, Style: st.Text.Bold(true)}})
		add(views.TextRows(text(description), inner, st.Text)...)
		add(views.Row{{Text: "", Style: st.Muted}})
		for _, s := range m.privacy.Statements {
			style := st.Text
			if s.Status == "warn" {
				style = st.Warn
			}
			value, status := s.Value, s.Status
			if !m.loopback() {
				// Fixed text, as for the indicator: this computer is not the one serving.
				switch {
				case s.ID == "processing" && s.Value == "On this computer":
					value = "On your organization's service"
				case s.ID == "network_exposure" && s.Status == "ok":
					value, status = "Whoever your organization lets reach its service", "info"
				}
			}
			add(views.Row{{Text: statusMark(status) + " ", Style: style}, {Text: views.Truncate(views.OneLine(text(s.Label)), inner-2), Style: st.Text.Bold(true)}})
			for _, row := range views.TextRows(text(value), inner-2, style) {
				add(append(views.Row{{Text: "  ", Style: st.Muted}}, row...))
			}
			if m.advanced() && s.Detail != "" {
				for _, row := range views.TextRows(s.Detail, inner-2, st.Muted) {
					add(append(views.Row{{Text: "  ", Style: st.Muted}}, row...))
				}
			}
		}
		add(views.Row{{Text: "", Style: st.Muted}})
		add(views.TextRows(text(m.privacy.Notice), inner, st.Muted)...)
	}
	add(views.Row{{Text: "", Style: st.Muted}})
	add(views.Row{views.Control("Delete all conversations", st.Danger, views.Action{Kind: "delete-all"})})
	return out
}

func (m *Model) choice(label string, on bool, a views.Action) views.Span {
	mark := "○ "
	if on {
		mark = "● "
	}
	style := m.th.Styles.Muted
	if on {
		style = m.th.Styles.Text.Bold(true)
	}
	return views.Control(mark+label, style, a)
}

func (m *Model) settingsPanel(p *panel) views.Panel {
	out := base(p, "Settings", widthSmall)
	st := m.th.Styles
	gap := views.Span{Text: "   ", Style: st.Muted}
	label := func(text string) views.Span {
		return views.Span{Text: views.PadRight(text, 13), Style: st.Text}
	}
	add := func(rows ...views.Row) { out.Rows = append(out.Rows, rows...) }
	add(views.Row{
		label("Appearance"),
		m.choice("Auto", m.themeSetting == theme.NameAuto, views.Action{Kind: "set-theme", Arg: theme.NameAuto}), gap,
		m.choice("Dark", m.themeSetting == theme.NameDark, views.Action{Kind: "set-theme", Arg: theme.NameDark}), gap,
		m.choice("Light", m.themeSetting == theme.NameLight, views.Action{Kind: "set-theme", Arg: theme.NameLight}),
	})
	add(views.Row{
		label("Mouse"),
		m.choice("On", m.mouse, views.Action{Kind: "set-mouse", Arg: "on"}), gap,
		m.choice("Off", !m.mouse, views.Action{Kind: "set-mouse", Arg: "off"}),
	})
	modes := views.Row{label("Mode"), m.choice("Basic", !m.advanced(), views.Action{Kind: "set-mode", Arg: config.ModeBasic})}
	if m.permissions().AdvancedMode {
		modes = append(modes, gap, m.choice("Advanced", m.advanced(), views.Action{Kind: "set-mode", Arg: config.ModeDeveloper}))
	}
	add(modes)
	add(views.Row{{Text: "", Style: st.Muted}})
	add(views.Row{views.Control("Delete all conversations", st.Danger, views.Action{Kind: "delete-all"})})
	return out
}

func (m *Model) statusPanel(p *panel) views.Panel {
	out := base(p, "Status", widthText)
	st := m.th.Styles
	inner := views.PanelInner(widthText, m.width)
	out.Cursor = -1
	add := func(rows ...views.Row) { out.Rows = append(out.Rows, rows...) }
	pair := func(key, value string) {
		add(views.Pair(key, value, 15, inner, st.Muted, st.Text)...)
	}

	if !m.advanced() {
		_, label := m.locality()
		if !m.connected() {
			label = views.NotConnected
		}
		pair("Where it runs", label)
		state := views.Connecting
		switch m.conn {
		case connUp:
			state = "Connected"
		case connDown, connAuth:
			state = views.NotConnected
		}
		if m.status != nil && m.connected() {
			state = views.PlainOr(m.status.Summary, "Connected")
		}
		pair("Right now", state)
		if lines, _ := m.banner(); len(lines) > 0 {
			add(views.Row{{Text: "", Style: st.Muted}})
			for _, line := range lines {
				add(views.TextRows(line, inner, st.Warn)...)
			}
		}
		return out
	}

	pair("gateway", m.cfg.Gateway.BaseURL)
	pair("connection", m.connLabel())
	if lines, _ := m.banner(); len(lines) > 0 {
		for _, line := range lines {
			add(views.TextRows(line, inner, st.Warn)...)
		}
	}
	if m.boot != nil {
		pair("identity", m.boot.Identity.DisplayName+" ("+m.boot.Identity.AuthMode+")")
	}
	s := m.status
	if s == nil {
		return out
	}
	pair("state", strings.TrimSpace(s.State+" "+s.Reason))
	pair("summary", s.Summary)
	pair("locality", s.Locality.Kind)
	pair("version", s.Gateway.Version)
	pair("instance", s.Gateway.InstanceID)
	pair("uptime", (time.Duration(s.Gateway.UptimeS) * time.Second).String())
	pair("latency", fmt.Sprintf("%d ms", m.latency.Milliseconds()))
	warm := s.Warmup.State
	if s.Warmup.Message != "" {
		warm += " · " + s.Warmup.Message
	}
	pair("warm-up", warm)
	if s.Inference == nil {
		pair("provider", "not shown for this account")
		return out
	}
	inf := s.Inference
	add(views.Row{{Text: "", Style: st.Muted}})
	pair("provider", strings.TrimSpace(inf.Provider+" "+inf.Version))
	pair("reachable", fmt.Sprintf("%t", inf.Reachable))
	if inf.Error != "" {
		pair("error", inf.Error)
	}
	cloudOff := "not checked"
	if inf.CloudAccessDisabled != nil {
		cloudOff = fmt.Sprintf("%t", *inf.CloudAccessDisabled)
	}
	pair("cloud off", cloudOff)
	if inf.WarmupModel != "" {
		pair("warm-up model", inf.WarmupModel)
	}
	for _, loaded := range inf.LoadedModels {
		pair("loaded", fmt.Sprintf("%s · ctx %d · %d MB · gpu %d MB", loaded.Name, loaded.ContextTokens, loaded.SizeBytes>>20, loaded.GPUBytes>>20))
	}
	for _, missing := range inf.MissingModels {
		pair("missing", strings.TrimSpace(missing.Tier+" "+missing.Model+" "+missing.InstallHint))
	}
	return out
}

func (m *Model) connLabel() string {
	switch m.conn {
	case connUp:
		return "connected"
	case connConnecting:
		return "connecting"
	case connAuth:
		return "refused"
	}
	return "down"
}

func (m *Model) modelsPanel(p *panel) views.Panel {
	out := base(p, "Choose a tier", widthText)
	st := m.th.Styles
	inner := views.PanelInner(widthText, m.width)
	add := func(rows ...views.Row) { out.Rows = append(out.Rows, rows...) }
	marker := func(tier string) string {
		if m.adv.tier == tier {
			return "● "
		}
		return "○ "
	}
	add(views.Row{views.Control(views.PadRight(views.PadRight(marker("auto")+"auto", 16)+"Route each turn automatically", inner), st.Text, views.Action{Kind: "set-tier", Arg: "auto"})})

	switch {
	case p.message != "":
		add(views.TextRows(p.message, inner, st.Warn)...)
	case p.loading || m.models == nil:
		add(views.Row{{Text: "One moment…", Style: st.Muted}})
	default:
		for _, tier := range m.models.Tiers {
			detail := tier.Description
			if tier.Model != "" {
				detail = fmt.Sprintf("%s · ctx %d", tier.Model, tier.ContextTokens)
			}
			usable := tier.Enabled && tier.Available && m.tierAllowed(tier.Tier)
			row := views.PadRight(marker(tier.Tier)+tier.Tier, 16) + detail
			if !usable {
				reason := tier.UnavailableReason
				if reason == "" {
					reason = "unavailable"
				}
				add(views.Row{{Text: views.Truncate(row, inner), Style: st.Muted}})
				add(views.Row{{Text: views.Truncate("  "+reason, inner), Style: st.Muted}})
				continue
			}
			add(views.Row{views.Control(views.PadRight(views.Truncate(row, inner), inner), st.Text, views.Action{Kind: "set-tier", Arg: tier.Tier})})
			if tier.Model != "" {
				add(views.Row{{Text: views.Truncate(strings.Repeat(" ", 16)+tierSettings(tier), inner), Style: st.Muted}})
			}
		}
	}
	return out
}

// tierSettings names what can set one tier apart when every tier runs one model.
func tierSettings(tier client.Tier) string {
	reasoning := "off"
	if tier.Thinking {
		reasoning = "on"
	}
	return fmt.Sprintf("output %d · temperature %.1f · reasoning %s", tier.MaxOutputTokens, tier.Temperature, reasoning)
}

func (m *Model) filesPanel(p *panel) views.Panel {
	out := base(p, "Attach a file", widthText)
	if m.picker == nil {
		return out
	}
	inner := views.PanelInner(widthText, m.width)
	out.Rows = m.picker.Rows(m.th, inner)
	out.Pinned = m.picker.Hints(m.th, inner)
	return out
}

func (m *Model) confirmPanel(p *panel) views.Panel {
	c := p.confirm
	out := base(p, c.title, widthSmall)
	st := m.th.Styles
	inner := views.PanelInner(widthSmall, m.width)
	out.Rows = append(out.Rows, views.TextRows(c.body, inner, st.Text)...)
	out.Rows = append(out.Rows, views.Row{{Text: "", Style: st.Muted}})
	out.Rows = append(out.Rows, views.Row{
		views.Control("  Cancel  ", st.Raised, views.Action{Kind: "confirm.cancel"}),
		{Text: "   ", Style: st.Muted},
		views.Control("  "+c.label+"  ", st.Danger, c.action),
	})
	return out
}
