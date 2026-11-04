//go:build !windows

package main

import "os"

// resetWin32Input is never called here: the mode exists only on Windows.
func resetWin32Input(*os.File) {}
