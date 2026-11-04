package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) bootstrapCmd() tea.Cmd {
	dial := m.opts.Dial
	current := m.client
	return func() tea.Msg {
		c := current
		if dial != nil {
			c = dial()
		}
		boot, err := c.Bootstrap(context.Background())
		return bootstrapMsg{boot: boot, client: c, err: err}
	}
}

func (m *Model) statusCmd() tea.Cmd {
	c := m.client
	now := m.now
	answers := m.answers
	return func() tea.Msg {
		start := now()
		status, err := c.Status(context.Background())
		return statusMsg{status: status, latency: now().Sub(start), answers: answers, err: err}
	}
}

func (m *Model) conversationsCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		list, err := c.Conversations(context.Background(), listLimit)
		return conversationsMsg{list: list, err: err}
	}
}

func (m *Model) schedulePoll() tea.Cmd {
	m.pollGen++
	gen := m.pollGen
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{gen} })
}

func (m *Model) updateConnection(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case bootstrapMsg:
		return m.handleBootstrap(msg), true
	case retryMsg:
		m.retrying = false
		if m.conn == connUp {
			return nil, true
		}
		return m.bootstrapCmd(), true
	case pollMsg:
		if msg.gen != m.pollGen || m.conn != connUp {
			return nil, true
		}
		return m.statusCmd(), true
	case statusMsg:
		return m.handleStatus(msg), true
	case conversationsMsg:
		if msg.err == nil {
			m.sidebar.Chats = msg.list
			if m.sidebar.Cursor >= m.sidebar.ItemCount() {
				m.sidebar.Cursor = m.sidebar.ItemCount() - 1
			}
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) handleBootstrap(msg bootstrapMsg) tea.Cmd {
	if msg.err != nil {
		m.setDown(msg.err)
		return m.retryLater()
	}
	wasUp := m.conn == connUp
	m.client = msg.client
	m.boot = msg.boot
	m.conn = connUp
	m.connErr = nil
	m.applyDefaultMode()
	if m.mode == config.ModeDeveloper && !msg.boot.Permissions.AdvancedMode {
		m.setMode(config.ModeBasic)
		m.setNotice("Advanced mode is no longer available for your account.")
	}
	cmds := []tea.Cmd{m.conversationsCmd()}
	if m.advanced() {
		cmds = append(cmds, m.refreshModels())
	}
	m.cardCursor = 0
	if !wasUp {
		cmds = append(cmds, m.statusCmd())
	}
	return tea.Batch(cmds...)
}

// retryLater schedules the next bootstrap, unless one is already waiting.
func (m *Model) retryLater() tea.Cmd {
	if m.retrying {
		return nil
	}
	m.retrying = true
	m.retryAt = m.now().Add(retryEvery)
	return tea.Tick(retryEvery, func(time.Time) tea.Msg { return retryMsg{} })
}

// refreshModels fetches the tier list for the inspector, where the caller may
// read it.
func (m *Model) refreshModels() tea.Cmd {
	if !m.permissions().ManualModelSelection {
		return nil
	}
	return m.modelsCmd()
}

// applyDefaultMode runs once, on the first bootstrap, because permissions are
// unknown before it.
func (m *Model) applyDefaultMode() {
	if m.modeApplied {
		return
	}
	m.modeApplied = true
	if m.cfg.Application.DefaultMode != config.ModeDeveloper {
		return
	}
	if m.boot.Permissions.AdvancedMode {
		m.setMode(config.ModeDeveloper)
		return
	}
	m.setNotice("Advanced mode isn't available for your account, so this is Basic mode.")
}

func (m *Model) handleStatus(msg statusMsg) tea.Cmd {
	if msg.err != nil {
		m.setDown(msg.err)
		if m.statusFailed {
			return m.retryLater()
		}
		m.statusFailed = true
		return m.bootstrapCmd()
	}
	m.statusFailed = false
	m.status = msg.status
	m.statusAnswers = msg.answers
	m.latency = msg.latency
	changed := m.instanceID != "" && msg.status.Gateway.InstanceID != m.instanceID
	m.instanceID = msg.status.Gateway.InstanceID
	next := m.schedulePoll()
	if changed {
		return tea.Batch(next, m.bootstrapCmd())
	}
	return next
}

func (m *Model) setDown(err error) {
	if client.IsAuth(err) {
		m.conn = connAuth
	} else {
		m.conn = connDown
	}
	m.connErr = err
}

func (m *Model) loopback() bool { return m.cfg.Gateway.IsLoopback() }

func (m *Model) gatewayAddress() string {
	if u, err := url.Parse(m.cfg.Gateway.BaseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return m.cfg.Gateway.BaseURL
}

// locality returns the kind and label for the indicator. A non-loopback gateway
// can never claim to be on this computer.
func (m *Model) locality() (kind, label string) {
	var loc client.Locality
	switch {
	case m.status != nil:
		loc = m.status.Locality
	case m.boot != nil:
		loc = m.boot.Locality
	}
	kind, label = loc.Kind, views.OneLine(loc.Label)
	if kind == "this_device" && !m.loopback() {
		return "organization", views.OrgServer
	}
	if kind == "demo" {
		return kind, views.DemoMode
	}
	if label == "" {
		switch kind {
		case "this_device":
			label = "On this computer"
		case "organization":
			label = views.OrgServer
		}
	}
	return kind, label
}

// connected reports whether the gateway is answering.
func (m *Model) connected() bool { return m.conn == connUp }

// banner returns the connection and readiness text for the current state, and
// whether it is a problem rather than a wait.
func (m *Model) banner() (lines []string, warn bool) {
	adv := m.advanced()
	address := m.gatewayAddress()
	detail := ""
	if m.connErr != nil {
		// Windows ends its network errors with a period, and the banner adds its own.
		detail = strings.TrimRight(connDetail(m.connErr), ".")
	}
	retry := ""
	if wait := m.retryAt.Sub(m.now()); wait > 0 {
		retry = fmt.Sprintf(" Retrying in %d s.", (wait+time.Second-1)/time.Second)
	}

	switch m.conn {
	case connConnecting:
		if adv || m.cfg.Application.DefaultMode == config.ModeDeveloper {
			return []string{"Connecting to " + address + "…"}, false
		}
		return []string{views.Connecting}, false
	case connAuth:
		if adv {
			return []string{views.NoAccess, address + ": " + detail + "." + retry}, true
		}
		return []string{views.NoAccess}, true
	case connDown:
		// Only a loopback gateway that does not answer is one that is not running.
		local := m.loopback() && (m.connErr == nil || client.IsNetwork(m.connErr))
		text := views.Unreachable
		if local {
			text = views.NotRunning
		}
		if adv {
			return []string{text, address + ": " + detail + "." + retry}, true
		}
		if local {
			return []string{text, views.HowToStart(m.opts.Platform)}, true
		}
		return []string{text}, true
	}

	if m.status == nil {
		return nil, false
	}
	s := m.status
	var inf client.Inference
	if s.Inference != nil {
		inf = *s.Inference
	}
	switch s.State {
	case "starting":
		if m.answeredSinceStatus() {
			return nil, false
		}
		if adv {
			return []string{fmt.Sprintf("Warm-up %s: %s", s.Warmup.State, inf.WarmupModel)}, false
		}
		return []string{views.GettingReady}, false
	case "unavailable":
		return m.unavailable(s, inf), true
	}
	return nil, false
}

// answeredSinceStatus reports that the assistant has written something since the
// status in hand was asked for. A reply streaming proves the assistant is ready, so
// the warm-up banner would be out of date.
func (m *Model) answeredSinceStatus() bool {
	if t := m.active; t != nil && answered(t.reply) {
		return true
	}
	return m.answers > m.statusAnswers
}

func answered(reply *components.Message) bool {
	return reply.Text != "" || reply.Thinking != ""
}

func (m *Model) unavailable(s *client.Status, inf client.Inference) []string {
	adv := m.advanced()
	switch s.Reason {
	case "provider_unreachable":
		if adv {
			return []string{views.NotResponding, strings.TrimSpace(inf.Provider + " " + inf.Error)}
		}
		return []string{views.NotResponding}
	case "cloud_access_enabled":
		if adv {
			return []string{views.CloudAccessOn(m.opts.Platform)}
		}
		return []string{views.NotSetUp}
	}
	if adv {
		lines := []string{views.NotSetUp}
		for _, missing := range inf.MissingModels {
			lines = append(lines, strings.TrimSpace(missing.Model+" "+missing.InstallHint))
		}
		if len(lines) == 1 && inf.Error != "" {
			lines = append(lines, inf.Error)
		}
		return lines
	}
	return []string{views.NotSetUp}
}

func connDetail(err error) string {
	var e *client.Error
	if errors.As(err, &e) {
		if e.Err != nil {
			return shortError(e.Err)
		}
		if e.Detail != "" {
			return e.Detail
		}
		if e.Code != "" {
			return e.Code
		}
	}
	return shortError(err)
}

// shortError drops Go's request prefix, which repeats the address.
func shortError(err error) string {
	text := err.Error()
	if i := strings.LastIndex(text, ": "); i >= 0 && i+2 < len(text) {
		return text[i+2:]
	}
	return text
}

// tierAllowed reports whether a tier can be chosen by this caller.
func (m *Model) tierAllowed(tier string) bool {
	return tier == "auto" || slices.Contains(m.permissions().AllowedTiers, tier)
}
