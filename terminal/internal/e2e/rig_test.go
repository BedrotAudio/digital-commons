package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const (
	envEnabled = "DC_E2E"
	envScreens = "DC_E2E_SCREENS"

	patience = 30 * time.Second
)

// console is a pseudo terminal with one program on it. Each platform provides
// newConsole, the key sequences and the rest of what differs, in console_*_test.go.
type console interface {
	io.ReadWriter
	Resize(width, height int) error
	Close() error

	start(exe string, args, env []string, dir string) error
	wait() (exitCode int)
	kill()
	pid() int
}

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv(envEnabled) == "" {
		t.Skipf("%s is not set", envEnabled)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildPath != "" {
		os.RemoveAll(filepath.Dir(buildPath))
	}
	os.Exit(code)
}

// binary builds the real command once per test run.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp(os.Getenv("DC_DATA_DIR"), "dc-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, binaryName)
		cmd := exec.Command("go", "build", "-o", buildPath, "./cmd/digital-commons")
		cmd.Dir = filepath.Join(repoRoot(t), "terminal")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

// workspace creates a folder for one test. The base comes from DC_DATA_DIR when
// set, and only the new subfolder is ever removed.
func workspace(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DC_DATA_DIR")
	if base == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			os.RemoveAll(dir)
		}
	})
	return dir
}

// gateway is a real gateway process using the demonstration provider.
type gateway struct {
	t       *testing.T
	root    string
	dataDir string
	config  string // optional replacement for config/development.yaml
	port    int
	backend string   // DC_PROVIDER, or the demonstration provider when empty
	env     []string // added to the gateway's environment
	cmd     *exec.Cmd
	log     *os.File
	exited  chan struct{}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newGateway(t *testing.T, dataDir string) *gateway {
	t.Helper()
	return &gateway{t: t, root: repoRoot(t), dataDir: dataDir, port: freePort(t)}
}

// slowDown makes the demonstration provider produce tokens at this rate.
func (g *gateway) slowDown(tokensPerSecond string) {
	g.t.Helper()
	data, err := os.ReadFile(filepath.Join(g.root, "config", "development.yaml"))
	if err != nil {
		g.t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	const was = "tokens_per_second: 40"
	if !strings.Contains(text, was) {
		g.t.Fatalf("config/development.yaml no longer contains %q", was)
	}
	g.config = filepath.Join(g.dataDir, "development.yaml")
	text = strings.Replace(text, was, "tokens_per_second: "+tokensPerSecond, 1)
	if err := os.WriteFile(g.config, []byte(text), 0o600); err != nil {
		g.t.Fatal(err)
	}
}

// override writes a copy of the configuration with this development.local.yaml
// beside it, as a user's local override would be.
func (g *gateway) override(yaml string) {
	g.t.Helper()
	data, err := os.ReadFile(filepath.Join(g.root, "config", "development.yaml"))
	if err != nil {
		g.t.Fatal(err)
	}
	folder := filepath.Join(g.dataDir, "config")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		g.t.Fatal(err)
	}
	g.config = filepath.Join(folder, "development.yaml")
	if err := os.WriteFile(g.config, data, 0o600); err != nil {
		g.t.Fatal(err)
	}
	local := filepath.Join(folder, "development.local.yaml")
	if err := os.WriteFile(local, []byte(yaml), 0o600); err != nil {
		g.t.Fatal(err)
	}
}

func (g *gateway) url() string { return "http://127.0.0.1:" + strconv.Itoa(g.port) }

func (g *gateway) start() {
	g.t.Helper()
	python := gatewayPython(g.root)
	if _, err := os.Stat(python); err != nil {
		g.t.Fatalf("gateway Python is missing: %v", err)
	}
	log, err := os.OpenFile(filepath.Join(g.dataDir, "gateway-output.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		g.t.Fatal(err)
	}
	cmd := exec.Command(python, "-m", "app", "--port", strconv.Itoa(g.port))
	cmd.Dir = filepath.Join(g.root, "backend")
	backend := g.backend
	if backend == "" {
		backend = "mock"
	}
	cmd.Env = append(cleanEnviron(), "DC_PROVIDER="+backend, "DC_DATA_DIR="+g.dataDir)
	if g.config != "" {
		cmd.Env = append(cmd.Env, "DC_CONFIG="+g.config)
	}
	cmd.Env = append(cmd.Env, g.env...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		g.t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	g.cmd, g.log, g.exited = cmd, log, exited
	g.t.Cleanup(g.stop)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.url()+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-exited:
			g.t.Fatalf("the gateway stopped during startup:\n%s", tail(log.Name()))
		case <-time.After(100 * time.Millisecond):
		}
	}
	g.t.Fatalf("the gateway did not start:\n%s", tail(log.Name()))
}

// tail is the end of a log, so a failure on a remote machine explains itself.
func tail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(data[max(len(data)-4096, 0):])
}

func (g *gateway) stop() {
	if g.cmd == nil {
		return
	}
	killGateway(g.cmd)
	<-g.exited
	g.log.Close()
	g.cmd, g.log = nil, nil
}

// cleanEnviron is this process's environment without anything that changes how
// either program detects the terminal or finds its data.
func cleanEnviron() []string {
	return without(os.Environ(),
		"NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "TTY_FORCE", "TMUX", "GOOGLE_CLOUD_SHELL",
		"TERM", "COLORTERM", "WT_SESSION", "TERM_PROGRAM", "TERM_PROGRAM_VERSION",
		"TEA_TRACE", "TEA_DEBUG", "DC_DATA_DIR", "DC_PROVIDER", "DC_CONFIG",
		"DC_MODELS_CONFIG", "DC_GATEWAY_TOKEN", "DC_TERMINAL_CONFIG",
		"DC_ORG_SERVER_TOKEN", "DC_SERVICE_TOKEN")
}

func without(environ []string, names ...string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
			env = append(env, kv)
		}
	}
	return env
}

// launchOptions choose how the real binary starts.
type launchOptions struct {
	width, height int
	theme         string
	gatewayURL    string
	dataDir       string
	home          string
	configDir     string // reuse a folder, and so its state.json, across launches
	plainKeys     bool   // Windows only: a terminal that ignores win32-input mode
	noColor       bool
	command       []string // run this instead of the binary, for dc.py
	env           []string // added to the program's environment
}

// session is one run of the real binary in a pseudo terminal.
type session struct {
	t   *testing.T
	pty console

	mu      sync.Mutex
	em      *vt.Emulator
	raw     bytes.Buffer
	alt     bool
	cursor  bool
	decMode map[int]bool

	exited   chan struct{}
	exitCode int
}

func launch(t *testing.T, opts launchOptions) *session {
	t.Helper()
	var exe string
	args := []string{"--gateway", opts.gatewayURL}
	if len(opts.command) > 0 {
		exe, args = opts.command[0], opts.command[1:]
	} else {
		exe = binary(t)
	}
	if opts.width == 0 {
		opts.width, opts.height = 120, 36
	}
	if opts.theme == "" {
		opts.theme = "dark"
	}

	configDir := opts.configDir
	if configDir == "" {
		var err error
		if configDir, err = os.MkdirTemp(opts.dataDir, "terminal-"); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(configDir, "terminal.yaml")
	body := "appearance:\n  theme: " + opts.theme + "\n"
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// The file picker starts in the home folder, so keep it away from the real one.
	if opts.home == "" {
		opts.home = opts.dataDir
	}
	env := append(without(cleanEnviron(), "HOME", "USERPROFILE"),
		"DC_DATA_DIR="+opts.dataDir,
		"DC_TERMINAL_CONFIG="+config)
	env = append(env, terminalEnv(opts)...)
	if opts.noColor {
		env = append(env, "NO_COLOR=1")
	}
	env = append(env, opts.env...)

	pty, err := newConsole(opts.width, opts.height)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{
		t:       t,
		pty:     pty,
		em:      vt.NewEmulator(opts.width, opts.height),
		cursor:  true,
		decMode: map[int]bool{},
		exited:  make(chan struct{}),
	}
	s.em.SetCallbacks(vt.Callbacks{
		AltScreen:        func(on bool) { s.alt = on },
		CursorVisibility: func(visible bool) { s.cursor = visible },
		EnableMode:       func(m ansi.Mode) { s.noteMode(m, true) },
		DisableMode:      func(m ansi.Mode) { s.noteMode(m, false) },
	})

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.raw.Write(buf[:n])
				s.em.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go io.Copy(pty, s.em)

	if err := pty.start(exe, args, env, opts.dataDir); err != nil {
		pty.Close()
		t.Fatal(err)
	}
	go func() {
		s.exitCode = pty.wait()
		close(s.exited)
	}()

	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			pty.kill()
			<-s.exited
		}
		pty.Close()
		s.em.Close()
	})
	return s
}

func (s *session) noteMode(m ansi.Mode, on bool) {
	if _, dec := m.(ansi.DECMode); dec {
		s.decMode[m.Mode()] = on
	}
}

func (s *session) send(keys ...string) {
	s.t.Helper()
	for _, k := range keys {
		if _, err := s.pty.Write([]byte(k)); err != nil {
			s.t.Fatalf("writing keys: %v", err)
		}
		time.Sleep(keyGap(k))
	}
}

// keyGap is the pause after a key. On Unix a bare Esc is told from the start of an
// escape sequence only by 50 ms of silence, measured from when the program reads it,
// so a busy machine needs far more than that or Esc and the next key become Alt+key.
func keyGap(key string) time.Duration {
	if key == keyEsc {
		return 250 * time.Millisecond
	}
	return 60 * time.Millisecond
}

// typeText sends characters one at a time, as a fast typist would.
func (s *session) typeText(text string) {
	s.t.Helper()
	for _, r := range text {
		if _, err := s.pty.Write([]byte(character(r))); err != nil {
			s.t.Fatalf("typing: %v", err)
		}
		time.Sleep(4 * time.Millisecond)
	}
}

// run types a line into the composer and presses Enter.
func (s *session) run(line string) {
	s.t.Helper()
	s.typeText(line)
	time.Sleep(120 * time.Millisecond)
	s.send(keyEnter)
}

// click presses and releases the left button at a screen cell, using the SGR
// mouse encoding a terminal sends.
func (s *session) click(x, y int) {
	s.t.Helper()
	s.send(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1))
}

// wheel sends a wheel notch at a screen cell.
func (s *session) wheel(x, y int, down bool) {
	s.t.Helper()
	button := 64
	if down {
		button = 65
	}
	s.send(fmt.Sprintf("\x1b[<%d;%d;%dM", button, x+1, y+1))
}

// paste sends text the way a terminal does with bracketed paste on.
func (s *session) paste(text string) {
	s.t.Helper()
	s.send("\x1b[200~" + text + "\x1b[201~")
}

func (s *session) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.em.String()
}

func (s *session) lines() []string {
	return strings.Split(s.text(), "\n")
}

// locate finds the first cell of text on screen.
func (s *session) locate(text string) (x, y int, ok bool) {
	for row, line := range s.lines() {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), row, true
		}
	}
	return 0, 0, false
}

// clickText clicks the first character of text.
func (s *session) clickText(text string) {
	s.t.Helper()
	x, y, ok := s.locate(text)
	if !ok {
		s.t.Fatalf("%q is not on screen:\n%s", text, s.text())
	}
	s.click(x, y)
}

// clickAfter clicks target, the first time it follows anchor on anchor's line.
func (s *session) clickAfter(anchor, target string) {
	s.t.Helper()
	x, y, ok := s.locate(anchor)
	if !ok {
		s.t.Fatalf("%q is not on screen:\n%s", anchor, s.text())
	}
	line := s.lines()[y]
	start := strings.Index(line, anchor) + len(anchor)
	i := strings.Index(line[start:], target)
	if i < 0 {
		s.t.Fatalf("%q does not follow %q in %q", target, anchor, line)
	}
	s.click(x+ansi.StringWidth(anchor)+ansi.StringWidth(line[start:start+i]), y)
}

func (s *session) waitFor(what string, ok func(screen string) bool) {
	s.t.Helper()
	s.waitWithin(patience, what, ok)
}

func (s *session) waitWithin(limit time.Duration, what string, ok func(screen string) bool) {
	s.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		screen := s.text()
		if ok(screen) {
			return
		}
		select {
		case <-s.exited:
			s.t.Fatalf("the program exited (code %d) waiting for %s\n%s", s.exitCode, what, screen)
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s\n%s", what, screen)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *session) waitText(texts ...string) {
	s.t.Helper()
	for _, text := range texts {
		s.waitFor(fmt.Sprintf("%q", text), func(screen string) bool {
			return strings.Contains(screen, text)
		})
	}
}

func (s *session) waitGone(text string) {
	s.t.Helper()
	s.waitFor(fmt.Sprintf("%q to go", text), func(screen string) bool {
		return !strings.Contains(screen, text)
	})
}

// resize changes the emulated window first, as a terminal does, so the program's
// repaint can never reach a window of the old size.
func (s *session) resize(w, h int) {
	s.t.Helper()
	s.mu.Lock()
	s.em.Resize(w, h)
	s.mu.Unlock()
	if err := s.pty.Resize(w, h); err != nil {
		s.t.Fatal(err)
	}
}

// waitExit waits for the program to end and returns its exit code.
func (s *session) waitExit() int {
	s.t.Helper()
	select {
	case <-s.exited:
	case <-time.After(patience):
		s.t.Fatalf("the program did not exit\n%s", s.text())
	}
	// Output written just before exit can still be in the pipe.
	time.Sleep(300 * time.Millisecond)
	return s.exitCode
}

// assertRestored checks that the emulated terminal is usable after the program
// left: main screen, visible cursor, and no mouse or paste mode left on. Modes
// 1004 and 9001 are excluded because the console host itself sets them on its
// own terminal before the program starts.
func (s *session) assertRestored() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alt {
		s.t.Error("the terminal is still on the alternate screen")
	}
	if !s.cursor {
		s.t.Error("the cursor is still hidden")
	}
	for _, mode := range []int{1000, 1002, 1003, 1006, 2004} {
		if s.decMode[mode] {
			s.t.Errorf("DEC mode %d is still set", mode)
		}
	}
}

// cursorAt returns where the terminal's real cursor is, and whether it is showing.
func (s *session) cursorAt() (x, y int, visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := s.em.CursorPosition()
	return pos.X, pos.Y, s.cursor
}

// grab copies the screen, cell by cell.
func (s *session) grab() ([][]uv.Cell, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.em.Width(), s.em.Height()
	rows := make([][]uv.Cell, h)
	for y := range rows {
		rows[y] = make([]uv.Cell, w)
		for x := range rows[y] {
			if c := s.em.CellAt(x, y); c != nil {
				rows[y][x] = *c
			} else {
				rows[y][x] = uv.EmptyCell
			}
		}
	}
	return rows, w, h
}
