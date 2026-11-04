package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
)

// vocabulary is the Vocabulary section of docs/ui-spec.md and its third principle,
// written apart from the app's own list so that the test checks that list too. Any
// non-letter is a boundary, so an identifier such as model_missing is caught.
var vocabulary = regexp.MustCompile(`(?i)(?:^|[^a-z])(models?|llms?|inference|gateways?|backends?|servers?|ollama|tokens?|contexts?|prompts?|tiers?|providers?|gpus?|apis?)(?:[^a-z]|$)`)

// bannedIn returns any technical word in the frame. "Organization server" is the
// one fixed phrase the spec itself puts in the indicator.
func bannedIn(frame string) string {
	match := vocabulary.FindStringSubmatch(strings.ReplaceAll(frame, views.OrgServer, ""))
	if match == nil {
		return ""
	}
	return match[1]
}

func scanBasic(t *testing.T, d *driver, state string) {
	t.Helper()
	if d.m.advanced() {
		t.Fatal("the scan must run in Basic mode")
	}
	for _, s := range [][2]int{{100, 30}, {60, 20}, {160, 50}} {
		d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
		d.rewrap()
		frame := d.text()
		if word := bannedIn(frame); word != "" {
			t.Errorf("%s at %dx%d shows %q:\n%s", state, s[0], s[1], word, frame)
		}
		// A row can be cut in the panel, so read the scrolled parts too.
		if p := d.m.topPanel(); p != nil {
			for range 40 {
				d.key("pgdown")
				frame = d.text()
				if word := bannedIn(frame); word != "" {
					t.Errorf("%s scrolled at %dx%d shows %q:\n%s", state, s[0], s[1], word, frame)
				}
			}
			p.scroll = 0
		}
	}
	d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.rewrap()
}

// realGateway answers with the exact wording of the backend, including the wording
// that is only safe once Basic mode has translated it.
func realGateway(t *testing.T) *fakeGateway {
	g := newFakeGateway(t)
	g.set(func() {
		g.privacyBody = map[string]any{
			"locality": map[string]any{"kind": "organization", "label": "Organization server", "description": "Your conversations are processed by a server your organization runs."},
			"statements": []map[string]any{
				{"id": "processing", "label": "Where the AI runs", "value": "On your organization's service", "status": "info", "detail": "Inference is handled by Org at 10.0.0.1."},
				{"id": "external_ai", "label": "Outside AI services", "value": "Could not be confirmed as off", "status": "warn", "detail": "Ollama did not answer GET /api/status. Chat is refused."},
				{"id": "cloud_fallback", "label": "Backup AI services", "value": "None. If the AI service is down, requests stop here", "status": "ok", "detail": "One provider is active per gateway, and no code path falls back to another."},
				{"id": "telemetry", "label": "Usage data", "value": "Digital Commons sends none. Your AI software may check for its own updates", "status": "info", "detail": "No prompt content is involved."},
				{"id": "conversation_storage", "label": "Where conversations are kept", "value": "In a private folder on the computer running Digital Commons, for 90 days", "status": "ok", "detail": "SQLite under C:/data."},
				{"id": "logging", "label": "What is recorded", "value": "Request times and outcomes, never what you write", "status": "ok", "detail": "The gateway logs method."},
				{"id": "network_exposure", "label": "Who can connect", "value": "Your AI software may be open to other computers", "status": "warn", "detail": "The gateway listens on 127.0.0.1:8000."},
				{"id": "encryption_at_rest", "label": "Encryption of saved conversations", "value": "Not encrypted in this beta", "status": "warn", "detail": "SQLite files."},
				{"id": "authentication", "label": "How this service knows it's you", "value": "A private key kept on this computer", "status": "info", "detail": "Local mode: a 256-bit bearer token."},
			},
			"notice": "Local processing alone does not make a system secure or compliant. This beta has not been approved for production use.",
		}
	})
	return g
}

func TestVocabularyAcrossEveryState(t *testing.T) {
	g := realGateway(t)
	d := g.open(t, rig{})
	walk(d, g, func(state string) {
		t.Helper()
		scanBasic(t, d, state)
	})
}

// A gateway that ignores the rule, or a future message nobody reviewed, still
// cannot put a technical word on a Basic screen.
func TestVocabularyHoldsAgainstTechnicalGatewayText(t *testing.T) {
	const raw = "Ollama model Gemma4:12b failed: model_missing, GPU context token limit via API gateway backend server, tier fast, provider x, inference prompt LLM"
	g := newFakeGateway(t)
	g.set(func() {
		g.boot["application"].(map[string]any)["notice"] = raw
		g.boot["locality"].(map[string]any)["description"] = raw
		g.boot["suggestions"] = []map[string]any{{"id": "x", "title": raw, "subtitle": raw, "action": "prompt", "prompt": "Help"}}
		g.status["summary"] = raw
		g.privacyBody = map[string]any{
			"locality":   g.boot["locality"],
			"statements": []map[string]any{{"id": "x", "label": raw, "value": raw, "status": "warn", "detail": raw}},
			"notice":     raw,
		}
		g.convs["c3"] = map[string]any{"id": "c3", "title": "Saved", "assistant": "general", "messages": []map[string]any{}}
	})
	d := g.open(t, rig{})
	scanBasic(t, d, "home")
	d.submit("/privacy")
	d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
	scanBasic(t, d, "privacy")
	d.key("esc")
	d.submit("/status")
	scanBasic(t, d, "status")
	d.key("esc")

	g.set(func() {
		g.script = []string{startOnly("new1", map[string]any{"code": "tier_fallback", "message": raw}),
			sse("status", map[string]any{"phase": "loading_model", "message": raw}), sse("thinking", map[string]any{"text": raw}), "HOLD",
			sse("delta", map[string]any{"text": "A plain answer."}), doneEvent("stop")}
	})
	d.submit("Question")
	d.until("status", func() bool { return d.m.active != nil && d.m.active.reply.Status != "" })
	d.settle(120e6)
	scanBasic(t, d, "waiting")
	g.release <- struct{}{}
	d.finish()
	scanBasic(t, d, "finished")
	d.refute("Reasoning", "standard")
}

// catalog is backend/app/models/errors.py, word for word.
var catalog = []struct {
	code, message string
	status        int
	retryable     bool
}{
	{"validation_error", "That request could not be processed. Please check it and try again.", 422, false},
	{"auth_required", "Sign-in is required to use this service.", 401, false},
	{"permission_denied", "Your account doesn't have access to that.", 403, false},
	{"not_found", "That item could not be found.", 404, false},
	{"conversation_locality_changed", "This conversation began in a different place. Please start a new chat.", 409, false},
	{"request_too_large", "That is too large to send. Try a shorter message or a smaller file.", 413, false},
	{"unsupported_attachment", "That type of file can't be attached.", 415, false},
	{"busy", "The assistant is busy right now. Please try again in a moment.", 429, true},
	{"provider_unavailable", "The AI service isn't responding. Please try again in a moment.", 503, true},
	{"model_not_installed", "No AI model is available for this request yet.", 503, false},
	{"model_load_failed", "The AI model could not be loaded. Please try again.", 503, true},
	{"model_not_permitted", "This AI service was blocked to protect your data, because outside AI access may be on.", 503, false},
	{"context_too_large", "That message is too long for the assistant. Please shorten it and try again.", 422, false},
	{"timeout", "The AI service stopped responding. Please try again.", 504, true},
	{"provider_error", "The AI service ran into a problem. Please try again.", 502, true},
	{"internal", "Something went wrong. Please try again.", 500, true},
}

func TestVocabularyOnEveryErrorCode(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{})
	for _, e := range catalog {
		for _, route := range []string{"http", "stream"} {
			name := e.code + " by " + route
			g.set(func() {
				g.chatCode, g.chatError, g.script = 0, nil, nil
				if route == "http" {
					g.chatCode = e.status
					g.chatError = map[string]any{"code": e.code, "message": e.message, "detail": "ollama said no", "retryable": e.retryable}
				} else {
					g.script = []string{errorEvent(e.code, e.message, e.retryable)}
				}
			})
			d.submit("Question for " + e.code[:3])
			d.finish()
			if fail := d.m.transcript.Last().Fail; fail == nil {
				t.Fatalf("%s: no failure shown", name)
			}
			scanBasic(t, d, name)
			d.refute("ollama said no")
			if !strings.Contains(e.message, e.code) && strings.Contains(d.text(), e.code) {
				t.Errorf("%s: the error code is on screen", name)
			}
			d.keys("ctrl+n")
			d.settle(2e6)
			d.clearComposer()
			g.set(func() { g.code = 0 })
			d.m.conn, d.m.connErr = connUp, nil
			d.send(struct{}{})
		}
	}
}

func TestVocabularyInCommandReplies(t *testing.T) {
	g := newFakeGateway(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "big.txt"), []byte(strings.Repeat("x", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "report.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := g.open(t, rig{home: home})
	d.reply(g, "Question", "Answer.")

	for _, line := range []string{
		"/model", "/model fast", "/code", "/copy code", "/copy code 1", "/copy nonsense", "/theme purple",
		"/mode nonsense", "/unknown", "/attach " + filepath.Join(home, "report.pdf"), "/attach " + filepath.Join(home, "big.txt"),
		"/attach " + filepath.Join(home, "missing.txt"), "/attach " + home,
	} {
		d.submit(line)
		d.settle(5e6)
		if d.m.notice == "" {
			t.Errorf("%s: silent", line)
		}
		scanBasic(t, d, line)
		d.m.notice = ""
		d.clearComposer()
	}

	g.set(func() {
		perms := g.boot["permissions"].(map[string]any)
		perms["advanced_mode"], perms["search_documents"], perms["manual_model_selection"] = false, false, false
	})
	d.m.boot.Permissions = client.Permissions{}
	for _, line := range []string{"/mode developer", "/mode advanced", "/search", "/search policy"} {
		d.submit(line)
		if d.m.notice == "" {
			t.Errorf("%s: silent", line)
		}
		scanBasic(t, d, line)
		d.m.notice = ""
		d.clearComposer()
	}
	d.key("ctrl+n")
	d.settle(2e6)
	d.submit("/delete")
	scanBasic(t, d, "delete with nothing")
}

// Basic mode does not show routing, usage or timing in any form.
func TestBasicHidesTheMachinery(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 160, h: 50})
	d.reply(g, "Question", "Answer.")
	d.refute("Gemma4", "standard", "tok/s", "6144", "ollama", "127.0.0.1", "388")
	d.submit("/status")
	d.refute("Gemma4", "127.0.0.1", "ollama", "instance")
	if fmt.Sprint(d.m.topPanel().kind) != panelStatus {
		t.Fatal("no status panel")
	}
}

// The scanners themselves, because a scanner that finds nothing also passes.
func TestScannersCatchWhatTheyClaim(t *testing.T) {
	for _, text := range []string{"The model failed", "model_missing", "API key", "see Ollama.", "GPU", "tokens", "(server)", "context window"} {
		if bannedIn(text) == "" {
			t.Errorf("%q was not caught", text)
		}
		if len(views.BannedWords(text)) == 0 {
			t.Errorf("the app's list missed %q", text)
		}
	}
	for _, text := range []string{"Organization server", "/mode developer", "Remodeling", "Apiary", "contextual", "Basic mode"} {
		if word := bannedIn(text); word != "" {
			t.Errorf("%q flagged as %q", text, word)
		}
	}
	for _, sgr := range []string{"\x1b[31m", "\x1b[1;38;2;1;2;3m", "\x1b[48;5;234m", "\x1b[97m", "\x1b[105m"} {
		if colorSGR(sgr) == "" {
			t.Errorf("%q was not seen as color", sgr)
		}
	}
	for _, sgr := range []string{"\x1b[1m", "\x1b[7m", "\x1b[0m", "\x1b[3;4m", "plain"} {
		if found := colorSGR(sgr); found != "" {
			t.Errorf("%q seen as color", sgr)
		}
	}
}

func TestHowToStartNamesEachPlatformsWindow(t *testing.T) {
	cases := map[string]string{
		"windows": "open a command window in the Digital Commons folder and run: python dc.py dev",
		"darwin":  "open Terminal in the Digital Commons folder and run: python3 dc.py dev",
	}
	for goos, want := range cases {
		if got := views.HowToStart(goos); !strings.HasSuffix(got, want) {
			t.Errorf("%s: %q", goos, got)
		}
	}
}
