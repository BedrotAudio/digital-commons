package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(vars map[string]string, configDir string) environment {
	return environment{
		getenv: func(k string) string { return vars[k] },
		userConfigDir: func() (string, error) {
			if configDir == "" {
				return "", errors.New("no config dir")
			}
			return configDir, nil
		},
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gatewayYAML(url string) string {
	return "gateway:\n  base_url: " + url + "\n"
}

func TestLookupOrder(t *testing.T) {
	dir := t.TempDir()
	flag := writeFile(t, filepath.Join(dir, "flag", "t.yaml"), gatewayYAML("http://127.0.0.1:1001"))
	envFile := writeFile(t, filepath.Join(dir, "env", "t.yaml"), gatewayYAML("http://127.0.0.1:1002"))
	userDir := filepath.Join(dir, "user")
	userFile := writeFile(t, filepath.Join(userDir, "DigitalCommons", "terminal.yaml"), gatewayYAML("http://127.0.0.1:1003"))
	emptyDir := filepath.Join(dir, "nothing")

	cases := []struct {
		name      string
		opts      Options
		vars      map[string]string
		configDir string
		wantURL   string
		wantPath  string
		wantState string
	}{
		{"flag beats all", Options{ConfigPath: flag}, map[string]string{EnvConfigPath: envFile}, userDir,
			"http://127.0.0.1:1001", flag, filepath.Join(dir, "flag", "state.json")},
		{"env beats user dir", Options{}, map[string]string{EnvConfigPath: envFile}, userDir,
			"http://127.0.0.1:1002", envFile, filepath.Join(dir, "env", "state.json")},
		{"user dir", Options{}, nil, userDir,
			"http://127.0.0.1:1003", userFile, filepath.Join(userDir, "DigitalCommons", "state.json")},
		{"defaults when no file", Options{}, nil, emptyDir,
			DefaultGatewayURL, "", filepath.Join(emptyDir, "DigitalCommons", "state.json")},
		{"defaults when no config dir", Options{}, nil, "", DefaultGatewayURL, "", ""},
		{"gateway flag wins", Options{ConfigPath: flag, GatewayURL: "http://localhost:9/"}, nil, userDir,
			"http://localhost:9", flag, filepath.Join(dir, "flag", "state.json")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := load(c.opts, envOf(c.vars, c.configDir))
			if err != nil {
				t.Fatal(err)
			}
			if got.Gateway.BaseURL != c.wantURL {
				t.Errorf("url = %q, want %q", got.Gateway.BaseURL, c.wantURL)
			}
			if got.Path != c.wantPath {
				t.Errorf("path = %q, want %q", got.Path, c.wantPath)
			}
			if got.StatePath != c.wantState {
				t.Errorf("state = %q, want %q", got.StatePath, c.wantState)
			}
		})
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := load(Options{ConfigPath: missing}, envOf(nil, "")); err == nil {
		t.Error("--config with a missing file should fail")
	}
	if _, err := load(Options{}, envOf(map[string]string{EnvConfigPath: missing}, "")); err == nil {
		t.Error("DC_TERMINAL_CONFIG with a missing file should fail")
	}
}

func TestDefaultsAndPartialFile(t *testing.T) {
	path := writeFile(t, filepath.Join(t.TempDir(), "t.yaml"), "appearance:\n  theme: light\n")
	got, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Appearance.Theme != ThemeLight {
		t.Errorf("theme = %q", got.Appearance.Theme)
	}
	if !got.Appearance.Mouse || got.Application.DefaultMode != ModeBasic || got.Gateway.BaseURL != DefaultGatewayURL {
		t.Errorf("defaults lost: %+v", got)
	}
}

func TestEmptyFileIsDefaults(t *testing.T) {
	path := writeFile(t, filepath.Join(t.TempDir(), "t.yaml"), "")
	got, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Gateway.BaseURL != DefaultGatewayURL {
		t.Errorf("url = %q", got.Gateway.BaseURL)
	}
}

func TestShippedConfigLoads(t *testing.T) {
	got, err := load(Options{ConfigPath: filepath.Join("..", "..", "..", "config", "terminal.yaml")}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if got.Application != want.Application || got.Gateway != want.Gateway || got.Appearance != want.Appearance {
		t.Errorf("shipped config differs from built-in defaults: %+v", got)
	}
}

func TestInvalidFiles(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bad yaml", "gateway: [", "config file"},
		{"unknown key", "gateway:\n  base_ur: http://127.0.0.1:1\n", "base_ur"},
		{"bad mode", "application:\n  default_mode: expert\n", "default_mode"},
		{"bad theme", "appearance:\n  theme: blue\n", "theme"},
		{"bad gateway", gatewayYAML("http://gateway.city.gov"), "https"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeFile(t, filepath.Join(t.TempDir(), "t.yaml"), c.body)
			_, err := load(Options{ConfigPath: path}, envOf(nil, ""))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestValidateGatewayURL(t *testing.T) {
	cases := []struct {
		raw      string
		insecure bool
		want     string
		ok       bool
	}{
		{"http://127.0.0.1:8000", false, "http://127.0.0.1:8000", true},
		{"http://127.0.0.1:8000/", false, "http://127.0.0.1:8000", true},
		{"http://localhost:8000", false, "http://localhost:8000", true},
		{"http://LOCALHOST:8000", false, "http://LOCALHOST:8000", true},
		{"http://[::1]:8000", false, "http://[::1]:8000", true},
		{"https://gateway.city.gov", false, "https://gateway.city.gov", true},
		{"https://gateway.city.gov/dc/", false, "https://gateway.city.gov/dc", true},
		{"HTTP://127.0.0.1:8000", false, "http://127.0.0.1:8000", true},
		{"http://gateway.city.gov", true, "http://gateway.city.gov", true},
		{"http://gateway.city.gov", false, "", false},
		{"http://10.0.0.5:8000", false, "", false},
		{"http://127.0.0.1.evil.gov", false, "", false},
		{"http://localhost.evil.gov", false, "", false},
		{"http://127.0.0.1@evil.gov", false, "", false},
		{"http://0.0.0.0:8000", false, "", false},
		{"http://127.1:8000", false, "", false},
		{"http://2130706433:8000", false, "", false},
		{"https://user:pw@gateway.city.gov", false, "", false},
		{"https://gateway.city.gov?x=1", false, "", false},
		{"https://gateway.city.gov#x", false, "", false},
		{"ftp://127.0.0.1", false, "", false},
		{"127.0.0.1:8000", false, "", false},
		{"", false, "", false},
		{"http://", false, "", false},
		{"http://127.0.0.1:port", false, "", false},
	}
	for _, c := range cases {
		got, err := ValidateGatewayURL(c.raw, c.insecure)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("%q: got %q, %v; want %q", c.raw, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: accepted as %q", c.raw, got)
		}
	}
}

func TestGatewayIsLoopback(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:8000", true},
		{"http://[::1]:8000", true},
		{"https://localhost", true},
		{"https://gateway.city.gov", false},
		{"http://127.0.0.1.evil.gov", false},
		{"http://127.0.0.1@evil.gov", false},
	}
	for _, c := range cases {
		if got := (Gateway{BaseURL: c.url}).IsLoopback(); got != c.want {
			t.Errorf("%q = %v", c.url, got)
		}
	}
}

func TestStatePersistence(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "t.yaml"), "appearance:\n  theme: dark\n  mouse: true\n")

	cfg, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if err := cfg.SaveAppearance(State{Theme: ThemeLight, Mouse: &off}); err != nil {
		t.Fatal(err)
	}
	again, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if again.Appearance.Theme != ThemeLight || again.Appearance.Mouse {
		t.Errorf("state not applied: %+v", again.Appearance)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("temporary file left behind: %v", entries)
	}
}

func TestStateCreatesFolderWhenUsingDefaults(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "cfg")
	cfg, err := load(Options{}, envOf(nil, configDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveAppearance(State{Theme: ThemeDark}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "DigitalCommons", "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDamagedStateIsIgnored(t *testing.T) {
	cases := []struct{ name, state string }{
		{"not json", "{broken"},
		{"wrong type", `{"mouse":"yes"}`},
		{"unknown theme", `{"theme":"blue"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeFile(t, filepath.Join(dir, "t.yaml"), "appearance:\n  theme: light\n")
			writeFile(t, filepath.Join(dir, "state.json"), c.state)
			got, err := load(Options{ConfigPath: path}, envOf(nil, ""))
			if err != nil {
				t.Fatal(err)
			}
			if got.Appearance.Theme != ThemeLight || !got.Appearance.Mouse {
				t.Errorf("appearance = %+v", got.Appearance)
			}
		})
	}
}

func TestSaveWithoutStatePathDoesNothing(t *testing.T) {
	if err := (Config{}).SaveAppearance(State{Theme: ThemeDark}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRefusesUnknownTheme(t *testing.T) {
	cfg := Config{StatePath: filepath.Join(t.TempDir(), "state.json")}
	if err := cfg.SaveAppearance(State{Theme: "blue"}); err == nil {
		t.Fatal("expected an error")
	}
}

// A choice that was not made leaves the config file in charge, and a later save of
// one choice keeps the other.
func TestSavingOneChoiceLeavesTheOther(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "t.yaml"), "appearance:\n  theme: auto\n  mouse: true\n")
	cfg, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveAppearance(State{Theme: ThemeLight}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "appearance:\n  theme: dark\n  mouse: false\n")
	again, err := load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if again.Appearance.Mouse || again.Appearance.Theme != ThemeLight {
		t.Errorf("after a theme save: %+v", again.Appearance)
	}

	on := true
	if err := cfg.SaveAppearance(State{Mouse: &on}); err != nil {
		t.Fatal(err)
	}
	again, err = load(Options{ConfigPath: path}, envOf(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Appearance.Mouse || again.Appearance.Theme != ThemeLight {
		t.Errorf("after a mouse save: %+v", again.Appearance)
	}
}

func TestResolveDataDir(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		env     map[string]string
		home    string
		want    string
		wantErr bool
	}{
		{"override windows", "windows", map[string]string{"DC_DATA_DIR": `D:\dc`, "LOCALAPPDATA": `C:\L`}, `C:\Users\A`, `D:\dc`, false},
		{"override posix", "linux", map[string]string{"DC_DATA_DIR": "/data/dc"}, "/home/a", "/data/dc", false},
		{"relative refused windows", "windows", map[string]string{"DC_DATA_DIR": `rel\dc`}, `C:\Users\A`, "", true},
		{"drive relative refused", "windows", map[string]string{"DC_DATA_DIR": `C:rel`}, `C:\Users\A`, "", true},
		{"relative refused posix", "linux", map[string]string{"DC_DATA_DIR": "rel/dc"}, "/home/a", "", true},
		{"unc accepted", "windows", map[string]string{"DC_DATA_DIR": `\\srv\share`}, `C:\Users\A`, `\\srv\share`, false},
		{"localappdata", "windows", map[string]string{"LOCALAPPDATA": `C:\L`}, `C:\Users\A`, `C:\L\DigitalCommons`, false},
		{"windows no localappdata", "windows", nil, `C:\Users\A`, `C:\Users\A\AppData\Local\DigitalCommons`, false},
		{"xdg", "linux", map[string]string{"XDG_STATE_HOME": "/s"}, "/home/a", "/s/digital-commons", false},
		{"xdg relative ignored", "linux", map[string]string{"XDG_STATE_HOME": "rel"}, "/home/a", "/home/a/.local/state/digital-commons", false},
		{"linux default", "linux", nil, "/home/a", "/home/a/.local/state/digital-commons", false},
		{"darwin default", "darwin", nil, "/Users/a", "/Users/a/Library/Application Support/DigitalCommons", false},
		{"darwin ignores xdg", "darwin", map[string]string{"XDG_STATE_HOME": "/s"}, "/Users/a", "/Users/a/Library/Application Support/DigitalCommons", false},
		{"darwin no home", "darwin", nil, "", "", true},
		{"no home", "linux", nil, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveDataDir(func(k string) string { return c.env[k] }, c.goos, c.home)
			if c.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLoadToken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "local-token"), "file-secret\r\n")
	t.Setenv(EnvDataDir, dir)
	t.Setenv(EnvGatewayToken, "")

	loopback := Gateway{BaseURL: "http://127.0.0.1:8000"}
	remote := Gateway{BaseURL: "https://gateway.city.gov"}
	spoof := Gateway{BaseURL: "http://127.0.0.1.evil.gov:8000", AllowInsecureHTTP: true}
	userinfo := Gateway{BaseURL: "http://127.0.0.1@evil.gov:8000", AllowInsecureHTTP: true}

	cases := []struct {
		name string
		env  string
		gw   Gateway
		want Token
	}{
		{"file on loopback", "", loopback, Token{"file-secret", TokenLocalFile}},
		{"env beats file", " env-secret\n", loopback, Token{"env-secret", TokenEnvironment}},
		{"env for remote", "env-secret", remote, Token{"env-secret", TokenEnvironment}},
		{"never file to remote", "", remote, Token{}},
		{"never file to spoof", "", spoof, Token{}},
		{"never file to userinfo", "", userinfo, Token{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvGatewayToken, c.env)
			got, err := LoadToken(c.gw)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLoadTokenMissingAndEmptyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	t.Setenv(EnvGatewayToken, "")
	gw := Gateway{BaseURL: "http://127.0.0.1:8000"}

	if got, err := LoadToken(gw); err != nil || got != (Token{}) {
		t.Fatalf("missing: %+v, %v", got, err)
	}
	writeFile(t, filepath.Join(dir, "local-token"), "  \n")
	if got, err := LoadToken(gw); err != nil || got != (Token{}) {
		t.Fatalf("empty: %+v, %v", got, err)
	}
}
