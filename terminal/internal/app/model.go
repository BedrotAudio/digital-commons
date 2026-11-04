// Package app is the Bubble Tea model. It owns all state and turns events into
// gateway calls. Drawing lives in view.go and advanced.go, on top of the
// components and views packages.
package app

import (
	"context"
	"os"
	"runtime"
	"time"

	"digital-commons/terminal/internal/attach"
	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/components"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const (
	pollEvery      = 5 * time.Second
	retryEvery     = 3 * time.Second
	themeWait      = 250 * time.Millisecond
	renderEvery    = 100 * time.Millisecond
	resizeWait     = 100 * time.Millisecond
	noticeFor      = 6 * time.Second
	quitWindow     = 2 * time.Second
	listLimit      = 200
	defaultCharMax = 15000
)

type focusArea int

const (
	focusComposer focusArea = iota
	focusList
	focusConversation
)

type connState int

const (
	connConnecting connState = iota
	connUp
	connDown
	connAuth
)

// Clipboard is the system clipboard, replaced in tests.
type Clipboard struct {
	Write func(string) error
	Read  func() (string, error)
}

// Options are the application's dependencies.
type Options struct {
	Config     config.Config
	Version    string
	Client     *client.Client
	Dial       func() *client.Client // rebuilds the client, re-reading the token
	Profile    colorprofile.Profile
	Win32Input bool
	Now        func() time.Time
	Clipboard  Clipboard
	HomeDir    string

	// Platform is runtime.GOOS and TermProgram is TERM_PROGRAM, unless a test sets
	// them. They decide the wording that differs on a Mac.
	Platform    string
	TermProgram string
}

// advancedChoices are kept in memory across mode switches.
type advancedChoices struct {
	tier      string
	assistant string
}

// turn is one reply in progress, or the last one.
type turn struct {
	id       int
	cancel   context.CancelFunc
	stream   <-chan client.StreamEvent
	req      client.ChatRequest
	started  bool
	stopping bool
	user     *components.Message
	reply    *components.Message
	replaced *components.Message
	then     []func() tea.Cmd
}

// Model is the application state.
type Model struct {
	opts   Options
	cfg    config.Config
	client *client.Client
	now    func() time.Time

	width, height int
	resizeGen     int
	resizing      bool
	geo           layout

	profile      colorprofile.Profile
	themeSetting string
	termDark     bool
	termAnswered bool
	themeReady   bool
	th           theme.Theme
	shiftEnter   bool
	mouse        bool
	saving       bool
	unsaved      config.State // choices waiting for the save in progress

	conn        connState
	connErr     error
	boot        *client.Bootstrap
	status      *client.Status
	latency     time.Duration
	retryAt     time.Time
	retrying    bool
	instanceID  string
	pollGen     int
	modeApplied bool

	statusFailed bool // the last status poll failed

	// answers counts replies that produced text, and statusAnswers is that count when
	// the status in hand was asked for. A reply proves the assistant is ready, and a
	// status can be a whole poll older than the reply.
	answers       int
	statusAnswers int

	mode string
	adv  advancedChoices

	focus      focusArea
	composer   *components.Composer
	transcript *components.Transcript
	sidebar    *components.Sidebar
	palette    *components.Palette

	currentID   string
	currentTime time.Time
	opening     string // the chat being fetched
	turnSeq     int
	active      *turn
	last        *turn
	dirty       bool
	lastContext *client.ContextInfo

	attachments []attach.File
	useDocs     bool
	pendingText string // a suggestion's text, placed once its files are chosen

	sideHidden   bool
	sideOverlay  bool
	inspectorOff bool
	inspectorTop bool

	cardCursor int
	cardPage   int

	panels  []*panel
	privacy *client.Privacy
	models  *client.Models
	picker  *components.FilePicker

	notice      string
	noticeWarn  bool
	noticeUntil time.Time
	quitArmed   time.Time
	quitting    bool

	zones      []views.Zone
	modalZones []views.Zone
	comp       *lipgloss.Compositor
}

type (
	themeTimeoutMsg struct{}
	clockMsg        struct{}
	pollMsg         struct{ gen int }
	retryMsg        struct{}
	resizeMsg       struct{ gen int }
	tickMsg         struct{ turn int }

	bootstrapMsg struct {
		boot   *client.Bootstrap
		client *client.Client
		err    error
	}
	statusMsg struct {
		status  *client.Status
		latency time.Duration
		answers int // Model.answers when the status was asked for
		err     error
	}
	conversationsMsg struct {
		list []client.ConversationSummary
		err  error
	}
	conversationMsg struct {
		id   string
		conv *client.Conversation
		err  error
	}
	streamMsg struct {
		turn int
		ev   client.StreamEvent
	}
	streamEndMsg struct{ turn int }
	deletedMsg   struct {
		err error
	}
	deletedAllMsg struct {
		count int
		err   error
	}
	privacyMsg struct {
		privacy *client.Privacy
		err     error
	}
	modelsMsg struct {
		models *client.Models
		err    error
	}
	clipboardMsg struct {
		text string
		err  error
	}
	attachedMsg struct {
		file attach.File
		err  error
	}
	dirMsg struct {
		dir     string
		entries []components.FileEntry
		err     error
	}
	saveMsg struct{ err error }
)

// New builds the model. Nothing is drawn until the theme resolves.
func New(opts Options) *Model {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Clipboard.Write == nil {
		opts.Clipboard.Write = clipboard.WriteAll
	}
	if opts.Clipboard.Read == nil {
		opts.Clipboard.Read = clipboard.ReadAll
	}
	if opts.Profile == 0 {
		opts.Profile = colorprofile.TrueColor
	}
	if opts.Platform == "" {
		opts.Platform = runtime.GOOS
		opts.TermProgram = os.Getenv("TERM_PROGRAM")
	}
	cfg := opts.Config
	setting := cfg.Appearance.Theme
	if setting == "" {
		setting = theme.NameAuto
	}
	th := theme.Resolve(setting, true)
	m := &Model{
		opts:         opts,
		cfg:          cfg,
		client:       opts.Client,
		now:          opts.Now,
		profile:      opts.Profile,
		themeSetting: setting,
		termDark:     true,
		themeReady:   setting != theme.NameAuto,
		th:           th,
		mouse:        cfg.Appearance.Mouse,
		mode:         config.ModeBasic,
		adv:          advancedChoices{tier: "auto", assistant: "general"},
		composer:     components.NewComposer(th),
		sidebar:      &components.Sidebar{},
		palette:      &components.Palette{},
	}
	m.transcript = components.NewTranscript(th, opts.Now)
	m.applyTheme()
	return m
}

// Init starts the theme query, the first connection and, on Windows, win32-input
// mode, without which Shift+Enter arrives as Enter and sends the message.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.RequestBackgroundColor,
		tea.Tick(themeWait, func(time.Time) tea.Msg { return themeTimeoutMsg{} }),
		m.composer.Focus(),
		m.bootstrapCmd(),
		clockCmd(),
	}
	if m.opts.Win32Input {
		cmds = append(cmds, tea.Sequence(tea.Raw(ansi.SetModeWin32Input), tea.Raw(ansi.RequestModeWin32Input)))
	}
	return tea.Batch(cmds...)
}

func clockCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return clockMsg{} })
}

// Filter drops what win32-input mode adds: key releases and bare modifier presses.
func Filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch key := msg.(type) {
	case tea.KeyReleaseMsg:
		return nil
	case tea.KeyPressMsg:
		if key.Code >= tea.KeyLeftShift && key.Code <= tea.KeyIsoLevel5Shift {
			return nil
		}
	}
	return msg
}

// Update applies one message. Geometry is refreshed afterwards, because almost any
// message can change the composer's height.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.palette.Sync(m.composer.Value())
	m.relayout()
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		first := m.width == 0
		m.width, m.height = msg.Width, msg.Height
		if m.width >= sideWidth {
			m.sideOverlay = false
		}
		if m.width >= wideWidth {
			m.inspectorTop = false
		}
		if first {
			return nil
		}
		m.resizeGen++
		m.resizing = true
		gen := m.resizeGen
		return tea.Tick(resizeWait, func(time.Time) tea.Msg { return resizeMsg{gen} })
	case resizeMsg:
		if msg.gen == m.resizeGen {
			m.resizing = false
		}
	case tea.ColorProfileMsg:
		m.profile = msg.Profile
		m.applyTheme()
	case tea.BackgroundColorMsg:
		m.termDark = msg.IsDark()
		m.termAnswered = true
		m.resolveTheme()
	case themeTimeoutMsg:
		m.resolveTheme()
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeWin32Input && msg.Value == ansi.ModeSet {
			m.shiftEnter = true
		}
	case tea.KeyboardEnhancementsMsg:
		if msg.SupportsKeyDisambiguation() {
			m.shiftEnter = true
		}
	case tea.PasteMsg:
		return m.handlePaste(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleClick(msg)
	case tea.MouseWheelMsg:
		m.handleWheel(msg)
	case clockMsg:
		return m.handleClock()
	case tickMsg:
		return m.handleTick(msg)
	default:
		if cmd, ok := m.updateConnection(msg); ok {
			return cmd
		}
		if cmd, ok := m.updateChat(msg); ok {
			return cmd
		}
		if cmd, ok := m.updatePanels(msg); ok {
			return cmd
		}
		return m.composer.Update(msg)
	}
	return nil
}

func (m *Model) resolveTheme() {
	m.themeReady = true
	m.applyTheme()
}

func (m *Model) applyTheme() {
	m.th = theme.Resolve(m.themeSetting, m.termDark)
	if m.profile <= colorprofile.ASCII {
		// With no fill to show focus, reverse video carries it.
		m.th.Styles.Button = m.th.Styles.Button.Reverse(true)
	}
	m.composer.SetTheme(m.th)
	m.transcript.SetTheme(m.th)
	m.transcript.SetOutlined(m.profile <= colorprofile.ASCII)
	m.transcript.Rebuild()
}

func (m *Model) handleClock() tea.Cmd {
	now := m.now()
	if m.notice != "" && now.After(m.noticeUntil) {
		m.notice = ""
	}
	return clockCmd()
}

func (m *Model) setNotice(text string) {
	m.notice = text
	m.noticeWarn = false
	m.noticeUntil = m.now().Add(noticeFor)
}

func (m *Model) setWarning(text string) {
	m.setNotice(text)
	m.noticeWarn = true
}

// refuse explains a missing permission. Before the first bootstrap nothing is known
// about the account yet, so that is what it says.
func (m *Model) refuse(text string) {
	if m.boot == nil {
		text = "Still connecting. Please try again in a moment."
	}
	m.setWarning(text)
}

func (m *Model) advanced() bool { return m.mode == config.ModeDeveloper }

func (m *Model) mac() bool { return m.opts.Platform == "darwin" }

func (m *Model) limits() client.Limits {
	if m.boot != nil && len(m.boot.Limits.AttachmentExtensions) > 0 {
		return m.boot.Limits
	}
	return client.Limits{
		MaxMessageChars:      defaultCharMax,
		MaxAttachments:       4,
		MaxAttachmentBytes:   262144,
		AttachmentExtensions: []string{".txt", ".md", ".csv", ".tsv"},
	}
}

func (m *Model) permissions() client.Permissions {
	if m.boot == nil {
		return client.Permissions{}
	}
	return m.boot.Permissions
}

func (m *Model) setFocus(f focusArea) tea.Cmd {
	m.focus = f
	if m.transcript.Focus >= 0 {
		m.transcript.Focus = -1
		m.transcript.Rebuild()
	}
	if f != focusList {
		m.sideOverlay = false
	}
	if f == focusComposer {
		return m.composer.Focus()
	}
	m.composer.Blur()
	return nil
}
