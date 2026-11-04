package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvGatewayToken and EnvDataDir name the environment variables read here.
const (
	EnvGatewayToken = "DC_GATEWAY_TOKEN"
	EnvDataDir      = "DC_DATA_DIR"
)

const localTokenFile = "local-token"

// TokenSource says where a gateway token came from.
type TokenSource string

// Token sources.
const (
	TokenNone        TokenSource = ""
	TokenEnvironment TokenSource = "environment"
	TokenLocalFile   TokenSource = "local-file"
)

// Token is the bearer token for the gateway.
type Token struct {
	Value  string
	Source TokenSource
}

// DataDir returns the gateway's runtime folder. It follows the same rules as
// resolve_data_dir in backend/app/security/paths.py and dc.py.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return resolveDataDir(os.Getenv, runtime.GOOS, home)
}

func resolveDataDir(getenv func(string) string, goos, home string) (string, error) {
	if explicit := getenv(EnvDataDir); explicit != "" {
		if !isAbs(goos, explicit) {
			return "", fmt.Errorf("the data folder must be an absolute path, not %q", explicit)
		}
		return explicit, nil
	}
	if goos == "windows" {
		if local := getenv("LOCALAPPDATA"); local != "" {
			return join(goos, local, "DigitalCommons"), nil
		}
		if home == "" {
			return "", errors.New("the data folder could not be found")
		}
		return join(goos, home, "AppData", "Local", "DigitalCommons"), nil
	}
	if goos == "darwin" {
		if home == "" {
			return "", errors.New("the data folder could not be found")
		}
		return join(goos, home, "Library", "Application Support", "DigitalCommons"), nil
	}
	if state := getenv("XDG_STATE_HOME"); state != "" && isAbs(goos, state) {
		return join(goos, state, "digital-commons"), nil
	}
	if home == "" {
		return "", errors.New("the data folder could not be found")
	}
	return join(goos, home, ".local", "state", "digital-commons"), nil
}

// isAbs and join take goos so that each platform's rules can be tested anywhere.
func isAbs(goos, p string) bool {
	if goos != "windows" {
		return strings.HasPrefix(p, "/")
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return true
	}
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

func join(goos, base string, parts ...string) string {
	if goos == "windows" {
		return strings.TrimRight(base, `\/`) + `\` + strings.Join(parts, `\`)
	}
	return path.Join(append([]string{base}, parts...)...)
}

// LoadToken finds the gateway token: DC_GATEWAY_TOKEN, then <data_dir>/local-token
// only when the gateway is loopback. The local token is never returned for any
// other gateway. No token is not an error; the gateway then answers 401.
func LoadToken(g Gateway) (Token, error) {
	return loadToken(g, os.Getenv)
}

func loadToken(g Gateway, getenv func(string) string) (Token, error) {
	if v := strings.TrimSpace(getenv(EnvGatewayToken)); v != "" {
		return Token{Value: v, Source: TokenEnvironment}, nil
	}
	if !g.IsLoopback() {
		return Token{}, nil
	}
	dir, err := DataDir()
	if err != nil {
		return Token{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, localTokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return Token{}, nil
	}
	if err != nil {
		return Token{}, fmt.Errorf("reading the local token: %w", err)
	}
	if v := strings.TrimSpace(string(data)); v != "" {
		return Token{Value: v, Source: TokenLocalFile}, nil
	}
	return Token{}, nil
}
