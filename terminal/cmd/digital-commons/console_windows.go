package main

import (
	"os"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/windows"
)

// resetWin32Input turns win32-input mode off. Bubble Tea has already put the console
// back as it found it, and a classic console window starts with escape sequences
// off. There the sequence would print as text and leave the mode on, so later
// programs in the window would read key records. They go on for this one write.
func resetWin32Input(out *os.File) {
	console := windows.Handle(out.Fd())
	var mode uint32
	if windows.GetConsoleMode(console, &mode) != nil {
		return
	}
	if windows.SetConsoleMode(console, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		return
	}
	out.WriteString(ansi.ResetModeWin32Input)
	windows.SetConsoleMode(console, mode)
}
