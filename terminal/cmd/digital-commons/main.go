// Command digital-commons is the terminal for the Digital Commons gateway.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"digital-commons/terminal/internal/app"
	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	if err := newRootCommand(launch).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "digital-commons:", err)
		os.Exit(1)
	}
}

func newRootCommand(start func(config.Config) error) *cobra.Command {
	var opts config.Options
	cmd := &cobra.Command{
		Use:           "digital-commons",
		Short:         "Digital Commons, a private AI workspace",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load(opts)
			if err != nil {
				return err
			}
			if _, err := config.LoadToken(cfg.Gateway); err != nil {
				return err
			}
			return start(cfg)
		},
	}
	cmd.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	cmd.Flags().StringVar(&opts.ConfigPath, "config", "", "path to terminal.yaml")
	cmd.Flags().StringVar(&opts.GatewayURL, "gateway", "", "gateway URL, replacing gateway.base_url")
	return cmd
}

// dialer returns a function that builds a client with the current token, so that
// a gateway restart with a new local token recovers without restarting this app.
func dialer(cfg config.Config) func() *client.Client {
	return func() *client.Client {
		token, _ := config.LoadToken(cfg.Gateway)
		return client.New(client.Options{BaseURL: cfg.Gateway.BaseURL, Token: token.Value})
	}
}

// noColor reports the NO_COLOR rule: any non-empty value, even 0 or false.
func noColor(getenv func(string) string) bool { return getenv("NO_COLOR") != "" }

// win32InputUsable reports whether the terminal around the console host will send
// keys in win32-input format. The console host answers the mode query itself, so
// the answer proves nothing about the terminal. A terminal that ignores the mode
// sends plain arrow keys, and the host then delivers them as stray characters.
// Windows Terminal and a bare console window are known to work. Any other terminal
// names itself through these variables and keeps plain keys and the Ctrl+J hint.
func win32InputUsable(getenv func(string) string) bool {
	if getenv("WT_SESSION") != "" {
		return true
	}
	return getenv("TERM") == "" && getenv("TERM_PROGRAM") == "" && getenv("SSH_CONNECTION") == ""
}

// launch runs the interface until the user quits.
func launch(cfg config.Config) error {
	// Without this, a piped run waits forever on a console nobody can type into.
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return errors.New("this program needs an interactive console window")
	}

	// Either variable would log the conversation to a file.
	os.Unsetenv("TEA_TRACE")
	os.Unsetenv("TEA_DEBUG")

	win32 := runtime.GOOS == "windows" && win32InputUsable(os.Getenv)
	dial := dialer(cfg)
	options := app.Options{
		Config:     cfg,
		Version:    version,
		Client:     dial(),
		Dial:       dial,
		Win32Input: win32,
	}
	programOptions := []tea.ProgramOption{tea.WithFilter(app.Filter)}
	if noColor(os.Getenv) {
		options.Profile = colorprofile.ASCII
		programOptions = append(programOptions, tea.WithColorProfile(colorprofile.ASCII))
	}

	_, err := tea.NewProgram(app.New(options), programOptions...).Run()
	if win32 {
		resetWin32Input(os.Stdout)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}
