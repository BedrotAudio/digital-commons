// Package e2e drives the real digital-commons binary and dc.py in a pseudo terminal
// against the real gateway, and asserts on an emulated screen and on raw responses.
// It uses a pseudo console on Windows and a Unix pty elsewhere, and runs only when
// DC_E2E is set:
//
//	DC_E2E=1 DC_E2E_SCREENS=<folder> go test ./internal/e2e -v
//
// With DC_E2E_SCREENS set, every captured screen is written with its true cell
// colors to screens.html, and as plain text to screens.txt, in that folder.
//
// The gateway runs from backend/.venv. Its provider is the demonstration one, another
// gateway standing in for the organization server, or a fake Ollama in this process,
// so nothing here reaches a real model. Scenarios run on every platform, except those
// about win32-input mode, which are in e2e_windows_test.go.
package e2e
