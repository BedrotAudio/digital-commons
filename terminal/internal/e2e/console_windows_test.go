package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

const (
	binaryName = "digital-commons.exe"
	pythonName = "python"
)

// Windows Terminal honours the console host's request for win32-input mode, so
// every key reaches the host as press and release records in that format. Bytes
// for a terminal that ignores the request are in plainKeys.
var (
	keyEnter      = named(0x0D, 13, 0)
	keyEsc        = named(0x1B, 27, 0)
	keyTab        = named(0x09, 9, 0)
	keyBackspace  = named(0x08, 8, 0)
	keyUp         = named(0x26, 0, enhanced)
	keyDown       = named(0x28, 0, enhanced)
	keyPgUp       = named(0x21, 0, enhanced)
	keyPgDn       = named(0x22, 0, enhanced)
	keyDel        = named(0x2E, 0, enhanced)
	keyF1         = named(0x70, 0, 0)
	keyF2         = named(0x71, 0, 0)
	keyF3         = named(0x72, 0, 0)
	keyCtrlB      = ctrl('B')
	keyCtrlC      = ctrl('C')
	keyCtrlJ      = ctrl('J')
	keyCtrlN      = ctrl('N')
	keyCtrlO      = ctrl('O')
	keyCtrlR      = ctrl('R')
	keyShiftEnter = withShift(named(0x0D, 13, shiftPressed))
)

const (
	shiftPressed = 0x10
	leftCtrl     = 0x08
	enhanced     = 0x100

	vkShift   = 0x10
	vkControl = 0x11
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	vkKeyScan     = user32.NewProc("VkKeyScanW")
	mapVirtualKey = user32.NewProc("MapVirtualKeyW")
)

func scanCode(vk int) int {
	code, _, _ := mapVirtualKey.Call(uintptr(vk), 0)
	return int(code)
}

func record(vk, sc, char, down, state int) string {
	return fmt.Sprintf("[%d;%d;%d;%d;%d;1_", vk, sc, char, down, state)
}

func tap(vk, char, state int) string {
	sc := scanCode(vk)
	return record(vk, sc, char, 1, state) + record(vk, sc, char, 0, state)
}

func named(vk, char, state int) string { return tap(vk, char, state) }

func ctrl(letter rune) string {
	cs := scanCode(vkControl)
	return record(vkControl, cs, 0, 1, leftCtrl) +
		tap(int(letter), int(letter-'A'+1), leftCtrl) +
		record(vkControl, cs, 0, 0, 0)
}

func withShift(keys string) string {
	ss := scanCode(vkShift)
	return record(vkShift, ss, 0, 1, shiftPressed) + keys + record(vkShift, ss, 0, 0, 0)
}

// character is what a real keyboard produces for one typed rune.
func character(r rune) string {
	res, _, _ := vkKeyScan.Call(uintptr(r))
	if int16(res) == -1 || r > 0x7f {
		return tap(0, int(r), 0)
	}
	vk, shift := int(res&0xff), res&0x100 != 0
	if shift {
		return withShift(tap(vk, int(r), shiftPressed))
	}
	return tap(vk, int(r), 0)
}

func gatewayPython(root string) string {
	return filepath.Join(root, "backend", ".venv", "Scripts", "python.exe")
}

// killGateway ends the whole process tree, because the venv's python.exe is a
// launcher with the real interpreter as its child.
func killGateway(cmd *exec.Cmd) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func terminalEnv(opts launchOptions) []string {
	env := []string{"COLORTERM=truecolor", "USERPROFILE=" + opts.home}
	if opts.plainKeys {
		return append(env, "TERM_PROGRAM=e2e")
	}
	return append(env, "WT_SESSION=e2e")
}

type windowsConsole struct {
	*conpty.ConPty
	proc *os.Process
}

func newConsole(width, height int) (console, error) {
	pty, err := conpty.New(width, height, 0)
	if err != nil {
		return nil, err
	}
	return &windowsConsole{ConPty: pty}, nil
}

func (c *windowsConsole) start(exe string, args, env []string, dir string) error {
	pid, _, err := c.Spawn(exe, append([]string{exe}, args...), &syscall.ProcAttr{Env: env, Dir: dir})
	if err != nil {
		return err
	}
	c.proc, err = os.FindProcess(pid)
	return err
}

func (c *windowsConsole) wait() int {
	state, err := c.proc.Wait()
	if err != nil {
		return -1
	}
	return state.ExitCode()
}

func (c *windowsConsole) kill() { c.proc.Kill() }

func (c *windowsConsole) pid() int { return c.proc.Pid }

// peersOf lists the remote addresses of a process's TCP connections. A socket bound
// for a connect that has not begun shows port 0, which is no peer at all.
func peersOf(pid int) []string {
	out, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
	if err != nil {
		return nil
	}
	var peers []string
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 5 && fields[4] == strconv.Itoa(pid) && !strings.HasSuffix(fields[2], ":0") {
			peers = append(peers, fields[2])
		}
	}
	return peers
}
