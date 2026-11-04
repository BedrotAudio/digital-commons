// Package config loads terminal settings, the persisted UI state, the data
// folder and the gateway token.
package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"digital-commons/terminal/internal/security"

	"gopkg.in/yaml.v3"
)

// Accepted values for application.default_mode and appearance.theme.
const (
	ModeBasic     = "basic"
	ModeDeveloper = "developer"

	ThemeAuto  = "auto"
	ThemeDark  = "dark"
	ThemeLight = "light"
)

// DefaultGatewayURL is the local gateway address used when nothing else is set.
const DefaultGatewayURL = "http://127.0.0.1:8000"

// EnvConfigPath names the environment variable that selects a config file.
const EnvConfigPath = "DC_TERMINAL_CONFIG"

// Application holds application-level settings.
type Application struct {
	DefaultMode string `yaml:"default_mode"`
}

// Gateway holds the gateway address.
type Gateway struct {
	BaseURL           string `yaml:"base_url"`
	AllowInsecureHTTP bool   `yaml:"allow_insecure_http"`
}

// IsLoopback reports whether the gateway URL parses to a loopback host. The UI
// must not say "On this computer" unless this is true.
func (g Gateway) IsLoopback() bool {
	u, err := url.Parse(g.BaseURL)
	return err == nil && security.IsLoopbackHost(u.Hostname())
}

// Appearance holds the visual preferences.
type Appearance struct {
	Theme string `yaml:"theme"`
	Mouse bool   `yaml:"mouse"`
}

// Config is the resolved terminal configuration.
type Config struct {
	Application Application `yaml:"application"`
	Gateway     Gateway     `yaml:"gateway"`
	Appearance  Appearance  `yaml:"appearance"`

	// Path is the file that was loaded, empty when built-in defaults applied.
	Path string `yaml:"-"`
	// StatePath is where state.json lives, empty when no folder could be found.
	StatePath string `yaml:"-"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Application: Application{DefaultMode: ModeBasic},
		Gateway:     Gateway{BaseURL: DefaultGatewayURL},
		Appearance:  Appearance{Theme: ThemeAuto, Mouse: true},
	}
}

// Options carries the command-line overrides.
type Options struct {
	// ConfigPath is the --config flag.
	ConfigPath string
	// GatewayURL is the --gateway flag. It replaces gateway.base_url.
	GatewayURL string
}

// environment is what Load reads from the process, replaced in tests.
type environment struct {
	getenv        func(string) string
	userConfigDir func() (string, error)
}

// Load resolves the configuration. The file comes from --config, then
// DC_TERMINAL_CONFIG, then os.UserConfigDir()/DigitalCommons/terminal.yaml, then
// built-in defaults. A file named by a flag or variable must exist. Theme and
// mouse choices from state.json override the file.
func Load(opts Options) (Config, error) {
	return load(opts, environment{os.Getenv, os.UserConfigDir})
}

func load(opts Options, env environment) (Config, error) {
	cfg := Default()

	path, required, err := locate(opts, env)
	if err != nil {
		return Config{}, err
	}
	if path != "" {
		found, err := readFile(path, &cfg)
		if err != nil {
			return Config{}, err
		}
		if !found && required {
			return Config{}, fmt.Errorf("config file %s does not exist", path)
		}
		if found {
			cfg.Path = path
		}
		cfg.StatePath = filepath.Join(filepath.Dir(path), stateFileName)
	}

	if opts.GatewayURL != "" {
		cfg.Gateway.BaseURL = opts.GatewayURL
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	if cfg.StatePath != "" {
		loadState(cfg.StatePath).apply(&cfg.Appearance)
	}
	return cfg, nil
}

// locate returns the config path to try and whether it has to exist.
func locate(opts Options, env environment) (string, bool, error) {
	if opts.ConfigPath != "" {
		return opts.ConfigPath, true, nil
	}
	if p := env.getenv(EnvConfigPath); p != "" {
		return p, true, nil
	}
	dir, err := env.userConfigDir()
	if err != nil {
		// Without a config folder the built-in defaults still work.
		return "", false, nil
	}
	return filepath.Join(dir, "DigitalCommons", "terminal.yaml"), false, nil
}

// readFile decodes path over cfg and reports whether the file existed. Unknown
// keys are errors, so a misspelled base_url cannot silently fall back to the
// default gateway.
func readFile(path string, cfg *Config) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading config file: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("config file %s: %w", path, err)
	}
	return true, nil
}

func (c *Config) validate() error {
	switch c.Application.DefaultMode {
	case ModeBasic, ModeDeveloper:
	default:
		return fmt.Errorf("application.default_mode must be %s or %s, not %q",
			ModeBasic, ModeDeveloper, c.Application.DefaultMode)
	}
	if !validTheme(c.Appearance.Theme) {
		return fmt.Errorf("appearance.theme must be %s, %s or %s, not %q",
			ThemeAuto, ThemeDark, ThemeLight, c.Appearance.Theme)
	}
	normalized, err := ValidateGatewayURL(c.Gateway.BaseURL, c.Gateway.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	c.Gateway.BaseURL = normalized
	return nil
}

func validTheme(s string) bool {
	return s == ThemeAuto || s == ThemeDark || s == ThemeLight
}

// ValidateGatewayURL checks a gateway URL and returns it without a trailing
// slash. http is accepted only for a loopback host, https otherwise, unless
// allowInsecure is set. Credentials, queries and fragments are refused.
func ValidateGatewayURL(raw string, allowInsecure bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("gateway URL %q is not valid: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("gateway URL %q must start with http:// or https://", raw)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("gateway URL %q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("gateway URL %q must not contain a user name or password", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("gateway URL %q must not contain a query or fragment", raw)
	}
	if u.Scheme == "http" && !security.IsLoopbackHost(u.Hostname()) && !allowInsecure {
		return "", fmt.Errorf("gateway URL %q must use https because it is not on this computer", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}
