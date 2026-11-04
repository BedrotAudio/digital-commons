package app

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// testClock is a clock the test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.Local)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeClipboard records writes and answers reads.
type fakeClipboard struct {
	mu       sync.Mutex
	written  []string
	content  string
	writeErr error
	readErr  error
}

func (c *fakeClipboard) Write(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	c.written = append(c.written, s)
	return nil
}

func (c *fakeClipboard) Read() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content, c.readErr
}

func (c *fakeClipboard) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.written) == 0 {
		return ""
	}
	return c.written[len(c.written)-1]
}

// rig describes one headless application. Zero values give a dark 100x30 window
// with the gateway reached over loopback.
type rig struct {
	w, h     int
	theme    string
	profile  colorprofile.Profile
	baseURL  string // config gateway URL, when it should differ from the client's
	token    string
	config   func(*config.Config)
	clock    *testClock
	clip     *fakeClipboard
	home     string
	platform string
	term     string
	win32    bool
	dial     func() *client.Client
}

func (r rig) build(t *testing.T, url string) *driver {
	t.Helper()
	cfg := config.Default()
	cfg.Gateway.BaseURL = url
	if r.baseURL != "" {
		cfg.Gateway.BaseURL = r.baseURL
	}
	cfg.StatePath = ""
	cfg.Appearance.Theme = r.theme
	if r.theme == "" {
		cfg.Appearance.Theme = "dark"
	}
	if r.config != nil {
		r.config(&cfg)
	}
	if r.clip == nil {
		r.clip = &fakeClipboard{}
	}
	if r.home == "" {
		r.home = t.TempDir()
	}
	if r.token == "" {
		r.token = "test"
	}
	opts := Options{
		Config: cfg, Version: "0.1.0-test", Profile: r.profile, HomeDir: r.home, Win32Input: r.win32,
		Platform: r.platform, TermProgram: r.term, Dial: r.dial,
		Client:    client.New(client.Options{BaseURL: url, Token: r.token}),
		Clipboard: Clipboard{Write: r.clip.Write, Read: r.clip.Read},
	}
	if r.clock != nil {
		opts.Now = r.clock.Now
	}
	m := New(opts)
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 256), clip: r.clip}
	d.start(m.Init())
	w, h := r.w, r.h
	if w == 0 {
		w, h = 100, 30
	}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

// open starts an application against the gateway and waits until it is connected
// with its chat list loaded.
func (g *fakeGateway) open(t *testing.T, r rig) *driver {
	t.Helper()
	d := r.build(t, g.URL)
	d.until("ready", func() bool {
		return d.m.themeReady && d.m.boot != nil && d.m.status != nil && len(d.m.sidebar.Chats) == g.chatCount()
	})
	return d
}

func newDriver(t *testing.T, g *fakeGateway, w, h int) *driver {
	t.Helper()
	return g.open(t, rig{w: w, h: h})
}

func newDriverAt(t *testing.T, url, token string, w, h int) *driver {
	t.Helper()
	d := rig{w: w, h: h, token: token}.build(t, url)
	d.until("ready", func() bool { return d.m.boot != nil && d.m.status != nil })
	return d
}

func (g *fakeGateway) chatCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.chats)
}

// set applies a change to the gateway's state under its lock.
func (g *fakeGateway) set(change func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change()
}

func (g *fakeGateway) lastRequest(t *testing.T) client.ChatRequest {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.requests) == 0 {
		t.Fatal("no chat request was made")
	}
	return g.requests[len(g.requests)-1]
}

func (g *fakeGateway) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func namedKey(s string) (tea.KeyPressMsg, bool) {
	names := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
		"left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp,
		"pgdown": tea.KeyPgDown, "delete": tea.KeyDelete, "backspace": tea.KeyBackspace,
		"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3,
	}
	if code, ok := names[s]; ok {
		return tea.KeyPressMsg{Code: code}, true
	}
	if rest, ok := strings.CutPrefix(s, "shift+"); ok {
		if code, ok := names[rest]; ok {
			return tea.KeyPressMsg{Code: code, Mod: tea.ModShift}, true
		}
	}
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok && len(rest) == 1 {
		return tea.KeyPressMsg{Code: rune(rest[0]), Mod: tea.ModCtrl}, true
	}
	return tea.KeyPressMsg{}, false
}

// keys presses each named key, or types each string, in order.
func (d *driver) keys(names ...string) {
	for _, name := range names {
		d.key(name)
	}
}

// submit types a line and presses Enter.
func (d *driver) submit(text string) {
	d.key(text)
	d.key("enter")
}

// expect fails unless the frame contains every string.
func (d *driver) expect(what ...string) {
	d.t.Helper()
	frame := d.text()
	for _, s := range what {
		if !strings.Contains(frame, s) {
			d.t.Errorf("frame lacks %q:\n%s", s, frame)
		}
	}
}

// refute fails if the frame contains any of the strings.
func (d *driver) refute(what ...string) {
	d.t.Helper()
	frame := d.text()
	for _, s := range what {
		if strings.Contains(frame, s) {
			d.t.Errorf("frame contains %q:\n%s", s, frame)
		}
	}
}

// clearComposer empties the composer and lets the palette notice.
func (d *driver) clearComposer() {
	d.m.composer.Reset()
	d.send(struct{}{})
}

// finish waits until no reply is running.
func (d *driver) finish() {
	d.t.Helper()
	d.until("the reply to end", func() bool { return d.m.active == nil })
}

// rewrap lets the transcript catch up with the window.
func (d *driver) rewrap() {
	d.m.resizing = false
	d.m.relayout()
	d.m.transcript.Rebuild()
}

// reply answers the next message with text and waits for it to finish.
func (d *driver) reply(g *fakeGateway, question, text string) {
	d.t.Helper()
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": text}), doneEvent("stop")) })
	d.submit(question)
	d.finish()
}

func doneEvent(reason string) string {
	return sse("done", map[string]any{"message_id": "m9", "finish_reason": reason, "usage": map[string]any{
		"prompt_tokens": 152, "completion_tokens": 388, "total_tokens": 540, "first_token_ms": 2210, "total_ms": 61000,
		"load_ms": 78, "tokens_per_second": 6.4, "estimated": false,
	}})
}

func errorEvent(code, message string, retryable bool) string {
	return sse("error", map[string]any{"code": code, "message": message, "detail": "connect to 127.0.0.1:11434 refused", "retryable": retryable})
}

// startOnly opens a stream with a start event and no status.
func startOnly(conversation string, notices ...map[string]any) string {
	if notices == nil {
		notices = []map[string]any{}
	}
	return sse("start", map[string]any{"request_id": "r1", "conversation_id": conversation, "message_id": "m9", "title": "Remote work eligibility",
		"route":   map[string]any{"requested_tier": "auto", "tier": "standard", "locality": "this_device", "model": "Gemma4:12b", "provider": "ollama", "reason": "Code markers present"},
		"context": map[string]any{"estimated_tokens": 1240, "budget_tokens": 6144, "omitted_messages": 0}, "notices": notices})
}

// click sends a left click.
func (d *driver) click(x, y int) {
	d.send(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
}

// clickText clicks the first place the text appears in the frame. It fails when
// the text is not there.
func (d *driver) clickText(text string) {
	d.t.Helper()
	x, y, ok := d.find(text)
	if !ok {
		d.t.Fatalf("nothing to click: %q is not on screen:\n%s", text, d.text())
	}
	d.click(x+ansi.StringWidth(text)/2, y)
}

// find locates text in the frame, in cells.
func (d *driver) find(text string) (x, y int, ok bool) {
	for y, line := range strings.Split(d.text(), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y, true
		}
	}
	return 0, 0, false
}

var errFake = errors.New("fake failure")
