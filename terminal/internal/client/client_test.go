package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"digital-commons/terminal/internal/config"
)

const hostile = "a\x1b[2Jb\x1b]52;c;aGVsbG8=\x07c\u009b2Jd\u202ee\x08f\rg"
const hostileClean = "ab" + "c" + "2Jd" + "e" + "f" + "g"

func newServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, New(Options{BaseURL: srv.URL, Token: "secret"})
}

func jsonHandler(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestEndpointsUseContractPaths(t *testing.T) {
	type call struct {
		name, method, path string
		auth               bool
		body               string
		run                func(*Client) error
	}
	ctx := context.Background()
	cases := []call{
		{"health", "GET", "/healthz", false, `{"status":"ok"}`, func(c *Client) error {
			v, err := c.Health(ctx)
			if err == nil && v.Status != "ok" {
				t.Errorf("status %q", v.Status)
			}
			return err
		}},
		{"bootstrap", "GET", "/v1/bootstrap", true, `{"identity":{"display_name":"Ada"}}`, func(c *Client) error {
			v, err := c.Bootstrap(ctx)
			if err == nil && v.Identity.DisplayName != "Ada" {
				t.Errorf("name %q", v.Identity.DisplayName)
			}
			return err
		}},
		{"status", "GET", "/v1/status", true, `{"state":"ready"}`, func(c *Client) error {
			v, err := c.Status(ctx)
			if err == nil && (v.State != "ready" || v.Inference != nil) {
				t.Errorf("status %+v", v)
			}
			return err
		}},
		{"models", "GET", "/v1/models", true, `{"provider":"ollama"}`, func(c *Client) error {
			_, err := c.Models(ctx)
			return err
		}},
		{"privacy", "GET", "/v1/privacy", true, `{"notice":"n"}`, func(c *Client) error {
			_, err := c.Privacy(ctx)
			return err
		}},
		{"conversations", "GET", "/v1/conversations", true, `{"conversations":[{"id":"a"}]}`, func(c *Client) error {
			v, err := c.Conversations(ctx, 0)
			if err == nil && len(v) != 1 {
				t.Errorf("rows %d", len(v))
			}
			return err
		}},
		{"conversations limit", "GET", "/v1/conversations?limit=5", true, `{"conversations":[]}`, func(c *Client) error {
			_, err := c.Conversations(ctx, 5)
			return err
		}},
		{"conversation", "GET", "/v1/conversations/abc-1", true, `{"id":"abc-1","title":"T","messages":[{"id":"m1"}]}`, func(c *Client) error {
			v, err := c.Conversation(ctx, "abc-1")
			if err == nil && (v.Title != "T" || len(v.Messages) != 1) {
				t.Errorf("conversation %+v", v)
			}
			return err
		}},
		{"delete", "DELETE", "/v1/conversations/abc-1", true, "", func(c *Client) error {
			return c.DeleteConversation(ctx, "abc-1")
		}},
		{"delete all", "DELETE", "/v1/conversations", true, `{"deleted":12}`, func(c *Client) error {
			n, err := c.DeleteAllConversations(ctx)
			if err == nil && n != 12 {
				t.Errorf("deleted %d", n)
			}
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got *http.Request
			_, cl := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				got = r
				if c.body == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				io.WriteString(w, c.body)
			})
			if err := c.run(cl); err != nil {
				t.Fatal(err)
			}
			if got.Method != c.method || got.URL.RequestURI() != c.path {
				t.Errorf("sent %s %s", got.Method, got.URL.RequestURI())
			}
			wantAuth := ""
			if c.auth {
				wantAuth = "Bearer secret"
			}
			if h := got.Header.Get("Authorization"); h != wantAuth {
				t.Errorf("Authorization = %q", h)
			}
			if got.Header.Get("Origin") != "" || got.Header.Get("Sec-Fetch-Site") != "" {
				t.Error("browser headers sent")
			}
		})
	}
}

func TestBadConversationIDNeverSent(t *testing.T) {
	var hits atomic.Int32
	_, c := newServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	for _, id := range []string{"", "..", "a/b", "a b", strings.Repeat("x", 65), "a?b=1"} {
		if _, err := c.Conversation(context.Background(), id); !IsAPI(err) {
			t.Errorf("Conversation(%q) err = %v", id, err)
		}
		if err := c.DeleteConversation(context.Background(), id); !IsAPI(err) {
			t.Errorf("DeleteConversation(%q) err = %v", id, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests reached the gateway", hits.Load())
	}
}

func TestDecodesContractSamples(t *testing.T) {
	statusBody := `{"state":"ready","reason":null,"summary":"Ready",
	"locality":{"kind":"this_device","label":"On this computer","description":"d"},
	"warmup":{"state":"ready","message":null},
	"gateway":{"version":"0.1.0","instance_id":"0f3c9a51","uptime_s":124,"address":"127.0.0.1:8000"},
	"inference":{"provider":"ollama","kind":"ollama","reachable":true,"version":"0.40.13","error":null,
	"cloud_access_disabled":true,"missing_models":[{"tier":"fast","model":"m","install_hint":"h"}],"warmup_model":"Gemma4:12b",
	"loaded_models":[{"name":"Gemma4:12b","size_bytes":6401126562,"gpu_bytes":0,"context_tokens":8192,"expires_at":"2026-11-01T21:15:13Z"}]},
	"future_field":{"nested":[1,2,3]}}`
	_, c := newServer(t, jsonHandler(t, 200, statusBody))
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Gateway.InstanceID != "0f3c9a51" || s.Gateway.UptimeS != 124 {
		t.Errorf("gateway %+v", s.Gateway)
	}
	if s.Inference == nil || s.Inference.LoadedModels[0].SizeBytes != 6401126562 {
		t.Fatalf("inference %+v", s.Inference)
	}
	if cloud := s.Inference.CloudAccessDisabled; s.Inference.MissingModels[0].InstallHint != "h" || cloud == nil || !*cloud {
		t.Errorf("inference %+v", s.Inference)
	}

	boot := `{"application":{"name":"Digital Commons","version":"0.1.0","stage":"beta","notice":"n"},
	"identity":{"user_id":"u","display_name":"Local Developer","groups":["all-staff"],"auth_mode":"local"},
	"permissions":{"advanced_mode":true,"manual_model_selection":true,"view_diagnostics":true,"search_documents":true,"allowed_tiers":["fast"]},
	"locality":{"kind":"this_device","label":"l","description":"d"},
	"suggestions":[{"id":"s","title":"t","subtitle":"st","action":"attach","prompt":"p"}],
	"limits":{"max_message_chars":15000,"max_attachments":4,"max_attachment_bytes":262144,"attachment_extensions":[".txt",".md"]}}`
	_, c = newServer(t, jsonHandler(t, 200, boot))
	b, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.Limits.MaxAttachmentBytes != 262144 || b.Limits.AttachmentExtensions[1] != ".md" {
		t.Errorf("limits %+v", b.Limits)
	}
	if !b.Permissions.ViewDiagnostics || b.Permissions.AllowedTiers[0] != "fast" {
		t.Errorf("permissions %+v", b.Permissions)
	}
	if b.Suggestions[0].Action != "attach" {
		t.Errorf("suggestions %+v", b.Suggestions)
	}

	conv := `{"id":"c","title":"T","assistant":"general","locality":"this_device","created_at":"2026-11-01T20:39:12Z",
	"updated_at":"2026-11-01T20:41:02Z","message_count":1,"messages":[{"id":"m1","role":"assistant","content":"x",
	"created_at":"2026-11-01T20:39:12Z","attachments":[{"name":"p.txt","media_type":"text/plain","chars":4120}],
	"citations":[{"number":1,"title":"Doc","page":7},{"number":2,"withheld":true}],"route":null,"usage":null,"finish_reason":"stop"}]}`
	_, c = newServer(t, jsonHandler(t, 200, conv))
	v, err := c.Conversation(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	m := v.Messages[0]
	if m.Route != nil || m.Usage != nil || m.Attachments[0].Chars != 4120 {
		t.Errorf("message %+v", m)
	}
	if m.Citations[0].Page == nil || *m.Citations[0].Page != 7 || !m.Citations[1].Withheld {
		t.Errorf("citations %+v", m.Citations)
	}
	if v.CreatedAt != "2026-11-01T20:39:12Z" {
		t.Errorf("summary not embedded: %+v", v.ConversationSummary)
	}
	if at, err := ParseTime(v.CreatedAt); err != nil || at.Year() != 2026 {
		t.Errorf("ParseTime = %v, %v", at, err)
	}
}

// fill sets every string reachable from v, so the test covers fields added later.
func fill(v reflect.Value, s string) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), s)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Field(i).CanSet() {
				fill(v.Field(i), s)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), s)
	case reflect.String:
		v.SetString(s)
	}
}

func checkFilled(t *testing.T, v reflect.Value, path string) int {
	t.Helper()
	count := 0
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			count += checkFilled(t, v.Elem(), path)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			count += checkFilled(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Slice:
		for i := range v.Len() {
			count += checkFilled(t, v.Index(i), path)
		}
	case reflect.String:
		count++
		if v.String() != hostileClean {
			t.Errorf("%s = %q", path, v.String())
		}
	}
	return count
}

func TestEveryStringIsCleaned(t *testing.T) {
	types := []any{
		&Bootstrap{}, &Status{}, &Models{}, &Privacy{}, &Conversation{}, &Health{},
		&StartEvent{}, &CitationsEvent{}, &NoticesEvent{}, &StatusEvent{}, &ThinkingEvent{}, &DeltaEvent{}, &DoneEvent{},
	}
	for _, target := range types {
		rv := reflect.ValueOf(target)
		fill(rv.Elem(), hostile)
		data, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		// JSON escapes must not smuggle anything past the decoder either.
		if !strings.Contains(string(data), `\u001b`) {
			t.Fatalf("expected escaped control characters in %s", data)
		}
		decoded := reflect.New(rv.Type().Elem())
		if err := decode(data, decoded.Interface()); err != nil {
			t.Fatal(err)
		}
		if n := checkFilled(t, decoded, rv.Type().Elem().Name()); n == 0 {
			t.Errorf("%T: no strings checked", target)
		}
	}
}

func TestErrorKinds(t *testing.T) {
	apiBody := `{"error":{"code":"busy","message":"Please wait.","detail":"queue full","retryable":true}}`
	cases := []struct {
		name        string
		status      int
		body        string
		network     bool
		auth        bool
		api         bool
		code, msg   string
		detail      string
		retryable   bool
		wantRequest string
	}{
		{"api", 429, apiBody, false, false, true, "busy", "Please wait.", "queue full", true, "req-1"},
		{"auth", 401, `{"error":{"code":"auth_required","message":"No access.","detail":null,"retryable":false}}`, false, true, false, "auth_required", "No access.", "", false, "req-1"},
		{"auth with no body", 401, "", false, true, false, "internal", messageGeneric, "", false, "req-1"},
		{"proxy page", 502, "<html>bad gateway</html>", false, false, true, "internal", messageGeneric, "", true, "req-1"},
		{"redirect", 302, "", true, false, false, "connection_failed", messageNetwork, "", true, ""},
		{"permanent redirect", 308, "", true, false, false, "connection_failed", messageNetwork, "", true, ""},
		{"not json on 200", 200, "<html>captive portal</html>", true, false, false, "connection_failed", messageNetwork, "", true, ""},
		{"hostile message", 500, `{"error":{"code":"internal","message":"bad\u001b[2J\u001b]0;x\u0007 thing","detail":"\u202edetail","retryable":false}}`,
			false, false, true, "internal", "bad thing", "detail", false, "req-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cl := newServer(t, jsonHandler(t, c.status, c.body))
			_, err := cl.Status(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if IsNetwork(err) != c.network || IsAuth(err) != c.auth || IsAPI(err) != c.api {
				t.Fatalf("network=%v auth=%v api=%v for %v", IsNetwork(err), IsAuth(err), IsAPI(err), err)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("%T", err)
			}
			if e.Code != c.code || e.Message != c.msg || e.Detail != c.detail || e.Retryable != c.retryable {
				t.Fatalf("%+v", e)
			}
			if e.RequestID != c.wantRequest {
				t.Errorf("request id %q", e.RequestID)
			}
		})
	}
}

func TestConnectionRefusedIsNetwork(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := New(Options{BaseURL: url}).Status(context.Background())
	if !IsNetwork(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestTimeoutEndsAHungCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := New(Options{BaseURL: srv.URL, RequestTimeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := c.Status(context.Background()); !IsNetwork(err) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not apply")
	}
}

func TestRedirectIsNeverFollowed(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()

	for _, code := range []int{301, 302, 303, 307, 308} {
		_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/steal", code)
		})
		if _, err := c.Status(context.Background()); !IsNetwork(err) {
			t.Errorf("%d: GET err = %v", code, err)
		}
		events := collect(t, c.StreamChat(context.Background(), ChatRequest{Message: "a private prompt"}))
		if _, ok := events[len(events)-1].(LostEvent); !ok || len(events) != 1 {
			t.Errorf("%d: stream events %#v", code, events)
		}
	}
	if followed.Load() != 0 {
		t.Fatalf("redirect target received %d requests", followed.Load())
	}
}

// dialTo sends every connection to addr, so a test can use a non-loopback name.
func dialTo(addr string) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return transport
}

// The whole path from config to the wire: a gateway that is not loopback never
// receives the local token, even when local-token exists.
func TestLocalTokenNeverReachesRemoteGateway(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local-token"), []byte("local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvDataDir, dir)

	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		io.WriteString(w, `{"state":"ready"}`)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, env string
		gateway   config.Gateway
		want      string
	}{
		{"loopback gets local", "", config.Gateway{BaseURL: "http://127.0.0.1:" + port}, "Bearer local-secret"},
		{"remote gets nothing", "", config.Gateway{BaseURL: "http://gateway.city.test:" + port, AllowInsecureHTTP: true}, ""},
		{"spoof gets nothing", "", config.Gateway{BaseURL: "http://127.0.0.1.evil.test:" + port, AllowInsecureHTTP: true}, ""},
		{"remote gets env token", "org-secret", config.Gateway{BaseURL: "http://gateway.city.test:" + port, AllowInsecureHTTP: true}, "Bearer org-secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.EnvGatewayToken, c.env)
			tok, err := config.LoadToken(c.gateway)
			if err != nil {
				t.Fatal(err)
			}
			cl := New(Options{BaseURL: c.gateway.BaseURL, Token: tok.Value, Transport: dialTo(addr)})
			if _, err := cl.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := seen.Load(); got != c.want {
				t.Fatalf("Authorization = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClientKeepsProxyAndNoTimeout(t *testing.T) {
	c := New(Options{BaseURL: "http://127.0.0.1:1"})
	if c.http.Timeout != 0 {
		t.Errorf("Timeout = %v", c.http.Timeout)
	}
	if c.idleTimeout != 30*time.Second {
		t.Errorf("idle timeout = %v, the contract says 30 seconds", c.idleTimeout)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Error("default proxy behaviour was removed")
	}
	if err := c.http.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect = %v", err)
	}
}

// Go's proxy bypass matches only the lowercase word localhost, so any other spelling
// of a loopback name has to be dialled directly here, or the token and the prompt go
// to HTTP_PROXY. Go reads the proxy variables once per process, so this needs its own.
func TestMixedCaseLocalhostSkipsTheProxy(t *testing.T) {
	if base := os.Getenv("DC_TEST_GATEWAY"); base != "" {
		New(Options{BaseURL: base, Token: "secret"}).Health(context.Background())
		return
	}
	var proxied, direct atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		direct.Add(1)
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer gateway.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"Localhost", "LOCALHOST"} {
		child := exec.Command(os.Args[0], "-test.run=^TestMixedCaseLocalhostSkipsTheProxy$")
		child.Env = append(os.Environ(), "HTTP_PROXY="+proxy.URL, "http_proxy="+proxy.URL, "NO_PROXY=", "no_proxy=", "DC_TEST_GATEWAY=http://"+host+":"+port)
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if proxied.Load() != 0 || direct.Load() != 2 {
		t.Errorf("%d requests went through the proxy and %d went direct", proxied.Load(), direct.Load())
	}
}

// An organization gateway is reached the way any other site is, including any proxy.
func TestRemoteGatewayKeepsTheProxyRule(t *testing.T) {
	transport := New(Options{BaseURL: "https://ai.example.gov"}).http.Transport.(*http.Transport)
	if transport.Proxy == nil {
		t.Error("an organization gateway lost the system proxy")
	}
}
