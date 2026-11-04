package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
)

const (
	notSetUp      = "The assistant isn't set up yet. Please contact your IT team."
	notResponding = "The AI service isn't responding."
	cloudIsOn     = "Ollama cloud access is on."
	serviceToken  = "an-e2e-service-token-of-sufficient-length"
)

// fakeOllama answers the routes the gateway uses, on a real port, with the wire
// shapes in docs/implementation-notes.md. No model is involved.
type fakeOllama struct {
	url          string
	disabled     atomic.Bool
	failMidReply atomic.Bool
	chats        atomic.Int32
}

func startFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	const model = "Gemma4:12b"
	fake := &fakeOllama{}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"cloud": map[string]any{"disabled": fake.disabled.Load(), "source": "none"}})
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"version": "0.40.13"})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"models": []any{map[string]any{"name": model, "model": model}}})
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"details": map[string]any{"family": "gemma"}})
	})
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"models": []any{map[string]any{
			"name": model, "model": model, "size": 6401126562, "size_vram": 0,
			"context_length": 8192, "expires_at": "2026-11-01T17:15:13.1234567-04:00",
		}}})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		fake.chats.Add(1)
		var body struct {
			Messages []any `json:"messages"`
			Stream   bool  `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			reply(w, map[string]any{"model": model, "done": true, "done_reason": "load"})
			return
		}
		flusher := w.(http.Flusher)
		line := func(chunk map[string]any) {
			json.NewEncoder(w).Encode(chunk)
			flusher.Flush()
		}
		line(map[string]any{"message": map[string]any{"role": "assistant", "thinking": "Considering. "}, "done": false})
		line(map[string]any{"message": map[string]any{"role": "assistant", "content": "Hello from the fake. "}, "done": false})
		if fake.failMidReply.Load() {
			line(map[string]any{"error": "the fake failed"})
			return
		}
		line(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "Finished."}, "done": true,
			"done_reason": "stop", "prompt_eval_count": 40, "eval_count": 12,
			"eval_duration": 2000000000, "load_duration": 78000000,
		})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	fake.url = "http://" + listener.Addr().String()
	return fake
}

func (g *gateway) useOllamaAt(url string) {
	g.backend = "ollama"
	g.override("inference:\n  warm_on_start: false\nproviders:\n  ollama:\n    base_url: " + url + "\n")
}

func (g *gateway) api() *api {
	g.t.Helper()
	token, err := os.ReadFile(filepath.Join(g.dataDir, "local-token"))
	if err != nil {
		g.t.Fatal(err)
	}
	return &api{t: g.t, base: g.url(), token: strings.TrimSpace(string(token))}
}

func (a *api) status() client.Status {
	a.t.Helper()
	var status client.Status
	data := a.expect(200, reflect.TypeFor[client.Status](), "GET", "/v1/status", nil)
	if err := json.Unmarshal(data, &status); err != nil {
		a.t.Fatal(err)
	}
	return status
}

// assertNoExtraWords is assertPlain for a screen that may name the organization
// server, which is the one fixed phrase the vocabulary rule allows.
func (s *session) assertNoExtraWords(state string) {
	s.t.Helper()
	text := strings.ReplaceAll(s.text(), "Organization server", "")
	if m := vocabulary.FindStringSubmatch(text); m != nil {
		s.t.Errorf("Basic screen %s shows %q:\n%s", state, m[1], s.text())
	}
}

// sessionConfig copies the shipped configuration with another port and, when given,
// another Ollama address, in the plain lines dc.py itself also reads.
func sessionConfig(t *testing.T, dir string, port int, ollama string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "development.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "port: 8000", fmt.Sprintf("port: %d", port), 1)
	if ollama != "" {
		text = strings.Replace(text, "base_url: http://127.0.0.1:11434", "base_url: "+ollama, 1)
	}
	config := filepath.Join(dir, "development.yaml")
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}

// The default path: dc.py dev with Ollama, warm-up included, against the fake.
func TestDevCommand(t *testing.T) {
	requireE2E(t)
	python, err := exec.LookPath(pythonName)
	if err != nil {
		t.Skipf("%s is not on PATH", pythonName)
	}
	dir := workspace(t)
	fake := startFakeOllama(t)
	fake.disabled.Store(true)
	port := freePort(t)
	s := launch(t, launchOptions{
		width: 110, height: 32, dataDir: dir,
		command: []string{python, filepath.Join(repoRoot(t), "dc.py"), "dev"},
		env:     []string{"DC_CONFIG=" + sessionConfig(t, dir, port, fake.url)},
	})
	s.waitWithin(2*patience, "the home screen", func(screen string) bool {
		return strings.Contains(screen, homeGreeting) && strings.Contains(screen, "On this computer")
	})
	s.assertPlain("dev home")
	s.waitFor("the warm-up", func(string) bool { return fake.chats.Load() == 1 })

	s.run("Hello")
	s.waitText("Hello from the fake.", "Finished.")
	s.assertPlain("dev reply")
	s.shot("dc.py dev")
	s.quit()
	if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port)); err == nil {
		resp.Body.Close()
		t.Error("the gateway is still answering after dc.py dev closed")
	}
}

// The real command, in a real pseudo console, on a spare port so nothing else
// matters. It must start the gateway, open the terminal, and stop what it started.
func TestDemoCommand(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	root := repoRoot(t)
	python, err := exec.LookPath(pythonName)
	if err != nil {
		t.Skipf("%s is not on PATH", pythonName)
	}
	port := freePort(t)
	config := sessionConfig(t, dir, port, "")

	s := launch(t, launchOptions{
		width: 110, height: 32, dataDir: dir,
		command: []string{python, filepath.Join(root, "dc.py"), "demo"},
		env:     []string{"DC_CONFIG=" + config},
	})
	s.waitWithin(2*patience, "the home screen", func(screen string) bool {
		return strings.Contains(screen, homeGreeting) && strings.Contains(screen, "Demo mode")
	})
	s.shot("dc.py demo")
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("the gateway is not up: %v", err)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(dir, "demo", "local-token")); err != nil {
		t.Errorf("the demo did not keep its own data: %v", err)
	}

	s.readmeWalkthrough()
	s.quit()

	if resp, err := http.Get(url); err == nil {
		resp.Body.Close()
		t.Error("the gateway is still answering after the demo closed")
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.pid")); !os.IsNotExist(err) {
		t.Errorf("the pid file was left behind: %v", err)
	}
}

// readmeWalkthrough follows "Try Basic mode" and "Try Advanced mode" in the README,
// in the working folder dc.py gives the terminal. The demonstration reply stands in
// for a real one, so this proves the steps and not the content.
func (s *session) readmeWalkthrough() {
	s.t.Helper()
	finish := func() {
		s.t.Helper()
		s.waitText("Stop")
		s.waitText("Try again")
		s.waitGone("Stop ")
	}

	s.clickText("Draft correspondence")
	s.waitText("Help me draft a professional message to")
	s.typeText("the budget office")
	s.send(keyEnter)
	finish()

	s.send(keyCtrlN)
	s.waitText(homeGreeting)
	s.run("/attach data/sample_documents/remote-work-policy.md")
	s.waitText("remote-work-policy.md")
	s.run("Summarize this document")
	finish()

	s.run("/search What are the sealed bid thresholds?")
	s.waitText("Stop")
	s.waitText("Try again", "Sources", "[1] Procurement Thresholds")
	s.waitGone("Stop ")

	s.run("/search How does an investigator interview a complainant?")
	finish()
	if strings.Contains(s.text(), "Personnel Investigations") {
		s.t.Errorf("a restricted document reached the screen:\n%s", s.text())
	}

	s.send(keyCtrlN)
	s.waitText(homeGreeting, "Today")
	s.send(keyCtrlB)
	s.waitGone("+ New chat")
	s.send(keyCtrlB)
	s.waitText("+ New chat")

	s.run("/privacy")
	s.waitText("Where the AI runs", "× Close")
	s.send(keyEsc)
	s.waitGone("× Close")

	// The theme step is left out: dc.py passes config/terminal.yaml, and a choice made
	// here would be saved beside it. TestThemeChoicePersists covers the command.
	s.run("/mode advanced")
	s.waitText("ADVANCED")
	s.run("Hello")
	s.waitText("Stop")
	s.waitFor("the usage line", func(screen string) bool {
		return strings.Contains(screen, "tok/s") && !strings.Contains(screen, "Stop ")
	})
	s.send(keyF3)
	s.waitText("USAGE", "× Close")
	s.send(keyEsc)
	s.waitGone("× Close")
	s.run("/model fast")
	s.waitFor("the tier in the header", func(screen string) bool {
		return strings.Contains(strings.Split(screen, "\n")[0], "fast")
	})
	s.run("/model auto")
	s.run("/code")
	s.waitText("CODE")
	s.run("Write a short function")
	s.waitText("Stop")
	s.waitText("working_days_until", "Try again")
	s.waitGone("Stop ")
	s.run("/code")
	s.run("/status")
	s.waitText("× Close")
	s.send(keyEsc)
	s.waitGone("× Close")
	s.run("/mode basic")
	s.waitGone("ADVANCED")
}

// The two commands that run the pieces separately. The terminal configuration names
// port 8000 and the frontend command takes no override, so this needs that port.
func TestBackendAndFrontendCommands(t *testing.T) {
	requireE2E(t)
	if l, err := net.Listen("tcp", "127.0.0.1:8000"); err != nil {
		t.Skip("port 8000 is in use")
	} else {
		l.Close()
	}
	python, err := exec.LookPath(pythonName)
	if err != nil {
		t.Skipf("%s is not on PATH", pythonName)
	}
	dir := workspace(t)
	root := repoRoot(t)
	script := filepath.Join(root, "dc.py")
	env := append(cleanEnviron(), "DC_DATA_DIR="+dir, "DC_PROVIDER=mock")
	dc := func(args ...string) *exec.Cmd {
		cmd := exec.Command(python, append([]string{script}, args...)...)
		cmd.Env, cmd.Dir = env, root
		return cmd
	}

	backend := dc("backend")
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- backend.Wait() }()
	t.Cleanup(func() { dc("stop").Run() })
	deadline := time.Now().Add(patience)
	for {
		resp, err := http.Get("http://127.0.0.1:8000/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dc.py backend did not start: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	s := launch(t, launchOptions{
		width: 100, height: 30, dataDir: dir,
		command: []string{python, script, "frontend"},
	})
	s.waitText(homeGreeting, "Demo mode")
	s.sendAndFinish(question)
	s.quit()

	report, _ := dc("doctor").CombinedOutput()
	if !strings.Contains(string(report), "started by dc.py backend") {
		t.Errorf("doctor does not report the gateway:\n%s", report)
	}
	if out, err := dc("stop").CombinedOutput(); err != nil {
		t.Errorf("dc.py stop: %v\n%s", err, out)
	}
	select {
	case <-finished:
	case <-time.After(patience):
		t.Error("dc.py backend did not end after dc.py stop")
	}
	if resp, err := http.Get("http://127.0.0.1:8000/healthz"); err == nil {
		resp.Body.Close()
		t.Error("the gateway is still answering after dc.py stop")
	}
}

// With the gateway down, Advanced mode shows the address, the error and a countdown.
func TestRetryCountdown(t *testing.T) {
	requireE2E(t)
	s, gw := fixture(t, launchOptions{width: 120, height: 36})
	s.run("/mode developer")
	s.waitText("ADVANCED")
	gw.stop()
	s.waitText("Not connected")
	s.waitText(fmt.Sprintf("127.0.0.1:%d", gw.port), "Retrying in")
	s.shot("advanced, gateway down")
	if text := s.text(); strings.Contains(text, "..") || !strings.Contains(text, "state   unknown") {
		t.Errorf("the disconnected screen is wrong:\n%s", text)
	}
	gw.start()
	s.waitWithin(2*patience, "recovery", func(screen string) bool {
		return strings.Contains(screen, "Demo mode") && !strings.Contains(screen, "Retrying in")
	})
	s.shot("advanced, gateway back")
	s.quit()
}

// One gateway stands in for the organization's model server, and a second relays
// to it. Conversations must exist only on the second.
func TestOrganizationSwitch(t *testing.T) {
	requireE2E(t)
	hostDir, localDir := workspace(t), workspace(t)
	host := newGateway(t, hostDir)
	host.override("inference_service:\n  enabled: true\nproviders:\n  mock:\n    tokens_per_second: 12\n")
	host.env = []string{"DC_SERVICE_TOKEN=" + serviceToken}
	host.start()

	gw := newGateway(t, localDir)
	gw.backend = "org_server"
	gw.override("providers:\n  org_server:\n    base_url: " + host.url() + "\n")
	gw.env = []string{"DC_ORG_SERVER_TOKEN=" + serviceToken}
	gw.start()

	s := launch(t, launchOptions{width: 120, height: 36, dataDir: localDir, gatewayURL: gw.url()})
	s.waitText(homeGreeting, "● Organization server")
	s.assertNoExtraWords("organization home")
	s.shot("organization server")

	const secret = "Organization switch marker 7731"
	s.run(secret)
	s.waitText("Stop", "demonstration")
	s.assertNoExtraWords("organization streaming")
	s.send(keyEsc)
	s.waitText("Stopped")
	s.waitGone("Stop ")
	s.shot("organization stopped")

	s.send(keyCtrlR)
	s.waitText("Stop")
	s.waitText("Try again")
	s.waitGone("Stop ")
	s.shot("organization regenerated")

	s.run("/privacy")
	s.waitText("× Close")
	s.assertNoExtraWords("organization privacy")
	s.shot("organization privacy")
	s.send(keyEsc)

	s.run("/mode developer")
	s.waitText("ADVANCED", "● Organization server")
	s.shot("organization advanced")
	s.quit()

	local := gw.api()
	data := local.expect(200, reflect.TypeFor[conversationList](), "GET", "/v1/conversations", nil)
	var list conversationList
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Conversations) != 1 || list.Conversations[0].Locality != "organization" {
		t.Fatalf("the local gateway holds %+v", list.Conversations)
	}
	var stored client.Conversation
	data = local.expect(200, reflect.TypeFor[client.Conversation](), "GET", "/v1/conversations/"+list.Conversations[0].ID, nil)
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 2 || stored.Messages[1].FinishReason != "stop" {
		t.Errorf("after regenerate the conversation holds %+v", stored.Messages)
	}
	if last := stored.Messages[1]; last.Route == nil || last.Route.Provider != "org_server" || last.Route.Model == "" {
		t.Errorf("the route is %+v", last.Route)
	}

	// The model server's stand-in stored nothing, by its API or on disk.
	held := host.api()
	data = held.expect(200, reflect.TypeFor[conversationList](), "GET", "/v1/conversations", nil)
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Conversations) != 0 {
		t.Errorf("the model server stored %+v", list.Conversations)
	}
	host.stop()
	gw.stop()
	if contains(t, hostDir, secret) {
		t.Error("the message reached a file on the model server")
	}
	if !contains(t, localDir, secret) {
		t.Error("the marker was not found on the local gateway, so this check proves nothing")
	}
}

func contains(t *testing.T, dir, text string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found = found || bytes.Contains(data, []byte(text))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// Ollama that does not say cloud access is off is never sent a message. The fake
// then turns it off, and the same session recovers with nothing restarted.
func TestFailClosed(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	fake := startFakeOllama(t)
	gw := newGateway(t, dir)
	gw.useOllamaAt(fake.url)
	gw.start()
	s := launch(t, launchOptions{width: 120, height: 36, dataDir: dir, gatewayURL: gw.url()})
	s.waitText(homeGreeting, notSetUp)
	s.waitText("Not confirmed")
	s.assertPlain("cloud access on")
	s.shot("fail closed, basic")

	// A refused send is told apart from the banner by appearing as well as it.
	s.run("Is this private?")
	s.waitFor("the inline refusal", func(screen string) bool {
		return strings.Count(screen, notSetUp) == 2
	})
	s.assertPlain("cloud access on, after a send")
	s.shot("fail closed, send refused")
	s.send(strings.Repeat("", len("Is this private?")))
	s.waitText("Message Digital Commons")

	s.run("/mode developer")
	s.waitText(cloudIsOn, "dc.py setup")
	s.shot("fail closed, advanced")
	if got := fake.chats.Load(); got != 0 {
		t.Errorf("%d chat requests reached Ollama with its cloud access on", got)
	}

	a := gw.api()
	status := a.status()
	if status.State != "unavailable" || status.Reason != "cloud_access_enabled" {
		t.Errorf("status is %s, %s", status.State, status.Reason)
	}
	if status.Locality.Kind == "this_device" {
		t.Error("the gateway claims this device with the gate closed")
	}

	fake.disabled.Store(true)
	s.waitWithin(2*patience, "the gate to open", func(screen string) bool {
		return strings.Contains(screen, "On this computer") && !strings.Contains(screen, cloudIsOn)
	})
	s.shot("gate open, advanced")
	s.quit()

	if got := a.status(); got.Inference == nil || len(got.Inference.LoadedModels) != 1 {
		t.Fatalf("status is %+v", got)
	}
	if at := a.status().Inference.LoadedModels[0].ExpiresAt; !isoTime.MatchString(at) {
		t.Errorf("expires_at is %q", at)
	}

	// The Ollama path of the contract, including thinking and a failed stream.
	turn := a.stream(client.ChatRequest{Message: "Hello"}, false)
	assertOrder(t, turn)
	if !hasEvent(turn, "done") || !hasEvent(turn, "thinking") {
		t.Errorf("events were %v", eventNames(turn))
	}
	fake.failMidReply.Store(true)
	failed := a.stream(client.ChatRequest{Message: "Fail, please"}, false)
	assertOrder(t, failed)
	last := failed[len(failed)-1]
	if last.name != "error" || !bytes.Contains(last.data, []byte(`"code":"provider_error"`)) {
		t.Errorf("the stream ended with %s %s", last.name, last.data)
	}
}

// The configured model service is on a closed port.
func TestOllamaUnreachable(t *testing.T) {
	requireE2E(t)
	dir := workspace(t)
	gw := newGateway(t, dir)
	gw.useOllamaAt(fmt.Sprintf("http://127.0.0.1:%d", freePort(t)))
	gw.start()
	s := launch(t, launchOptions{width: 120, height: 36, dataDir: dir, gatewayURL: gw.url()})
	s.waitText(homeGreeting, notResponding)
	s.assertPlain("provider unreachable")
	s.shot("service unreachable, basic")

	s.run("Are you there?")
	s.waitText("Are you there?", "Try again")
	s.assertPlain("provider unreachable, after a send")
	s.shot("service unreachable, send refused")

	s.run("/mode developer")
	s.waitText("ollama", notResponding)
	s.shot("service unreachable, advanced")
	s.quit()

	status := gw.api().status()
	if status.State != "unavailable" || status.Reason != "provider_unreachable" {
		t.Errorf("status is %s, %s", status.State, status.Reason)
	}
}

// Connections made by the terminal, sampled through a whole conversation. Nothing
// but the configured gateway may ever appear.
func TestConnectsOnlyToTheGateway(t *testing.T) {
	requireE2E(t)
	// Go ignores proxy settings for a loopback address. If it ever stopped doing so,
	// the home screen would not appear.
	proxy := "http://127.0.0.1:9"
	s, gw := fixture(t, launchOptions{width: 120, height: 36,
		env: []string{"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}})
	want := fmt.Sprintf("127.0.0.1:%d", gw.port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan map[string]int, 1)
	go func() {
		peers := map[string]int{}
		for ctx.Err() == nil {
			for _, peer := range peersOf(s.pty.pid()) {
				peers[peer]++
			}
			time.Sleep(50 * time.Millisecond)
		}
		seen <- peers
	}()

	s.sendAndFinish(question)
	s.run("/privacy")
	s.waitText("× Close")
	s.send(keyEsc)
	s.run("/search " + question)
	s.waitText("Try again", "Sources")
	s.waitGone("Stop ")
	cancel()
	peers := <-seen
	s.quit()

	if peers[want] == 0 {
		t.Errorf("no connection to %s was seen, so this check proves nothing: %v", want, peers)
	}
	for peer := range peers {
		if peer != want {
			t.Errorf("the terminal connected to %s", peer)
		}
	}
	t.Logf("peers: %v", peers)
}
