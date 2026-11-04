//go:build !windows

package e2e

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/xpty"
)

const (
	binaryName = "digital-commons"
	pythonName = "python3"
)

// A Unix terminal sends plain VT sequences. Shift+Enter is the form terminals with
// enhanced keys send, such as iTerm2 and kitty. Terminal.app has no such form.
const (
	keyEnter      = "\r"
	keyEsc        = "\x1b"
	keyTab        = "\t"
	keyBackspace  = "\x7f"
	keyUp         = "\x1b[A"
	keyDown       = "\x1b[B"
	keyPgUp       = "\x1b[5~"
	keyPgDn       = "\x1b[6~"
	keyDel        = "\x1b[3~"
	keyF1         = "\x1bOP"
	keyF2         = "\x1bOQ"
	keyF3         = "\x1bOR"
	keyCtrlB      = "\x02"
	keyCtrlC      = "\x03"
	keyCtrlJ      = "\n"
	keyCtrlN      = "\x0e"
	keyCtrlO      = "\x0f"
	keyCtrlR      = "\x12"
	keyShiftEnter = "\x1b[13;2u"
)

func character(r rune) string { return string(r) }

func gatewayPython(root string) string {
	return filepath.Join(root, "backend", ".venv", "bin", "python")
}

func killGateway(cmd *exec.Cmd) { cmd.Process.Kill() }

// terminalEnv describes a true-color terminal. COLORTERM is what every true-color
// terminal sets, and TERM must be present or the program assumes no terminal at all.
func terminalEnv(opts launchOptions) []string {
	return []string{"TERM=xterm-256color", "COLORTERM=truecolor", "HOME=" + opts.home}
}

// unixConsole holds the pty's child side open in this process, so output written
// just before the program exits stays readable on macOS as well.
type unixConsole struct {
	*xpty.UnixPty
	cmd *exec.Cmd
}

func newConsole(width, height int) (console, error) {
	pty, err := xpty.NewUnixPty(width, height)
	if err != nil {
		return nil, err
	}
	return &unixConsole{UnixPty: pty}, nil
}

// start makes the pty the program's controlling terminal, because resizes arrive
// as SIGWINCH on that terminal. xpty leaves this to the caller.
func (c *unixConsole) start(exe string, args, env []string, dir string) error {
	c.cmd = exec.Command(exe, args...)
	c.cmd.Env, c.cmd.Dir = env, dir
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	return c.Start(c.cmd)
}

func (c *unixConsole) wait() int {
	c.cmd.Wait()
	return c.cmd.ProcessState.ExitCode()
}

func (c *unixConsole) kill() { c.cmd.Process.Kill() }

func (c *unixConsole) pid() int { return c.cmd.Process.Pid }

// peersOf lists the remote addresses of a process's TCP connections.
func peersOf(pid int) []string {
	out, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-Fn").Output()
	if err != nil {
		return nil
	}
	var peers []string
	for line := range strings.Lines(string(out)) {
		if _, peer, ok := strings.Cut(strings.TrimSpace(line), "->"); ok {
			peers = append(peers, peer)
		}
	}
	return peers
}
