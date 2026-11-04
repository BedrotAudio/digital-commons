// Package client speaks the gateway API in docs/api.md. Every string it returns
// has passed through security.Clean.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"digital-commons/terminal/internal/security"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultIdleTimeout    = 30 * time.Second
	maxResponseBytes      = 64 << 20
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Options configures a Client. Only BaseURL is required.
type Options struct {
	BaseURL string
	Token   string

	// Transport replaces the default transport, which keeps Go's proxy handling
	// except for a loopback gateway.
	Transport http.RoundTripper
	// RequestTimeout bounds each non-streaming call. Default 15 seconds.
	RequestTimeout time.Duration
	// IdleTimeout is how long a stream may stay silent. Default 30 seconds.
	IdleTimeout time.Duration
}

// Client calls the gateway. It is safe for concurrent use.
type Client struct {
	base           string
	token          string
	http           *http.Client
	requestTimeout time.Duration
	idleTimeout    time.Duration
}

// New returns a Client. There is one http.Client, with no overall timeout because
// a chat stream may run for minutes. Redirects are never followed, because Go
// would re-send the prompt to the redirect target.
func New(opts Options) *Client {
	transport := opts.Transport
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = proxyFor
		transport = base
	}
	c := &Client{
		base:           strings.TrimRight(opts.BaseURL, "/"),
		token:          opts.Token,
		requestTimeout: opts.RequestTimeout,
		idleTimeout:    opts.IdleTimeout,
		http: &http.Client{
			Transport: transport,
			Timeout:   0,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if c.requestTimeout <= 0 {
		c.requestTimeout = defaultRequestTimeout
	}
	if c.idleTimeout <= 0 {
		c.idleTimeout = defaultIdleTimeout
	}
	return c
}

// proxyFor is Go's rule, except that a loopback gateway is never proxied. Go skips
// the proxy only for the exact lowercase word localhost, so Localhost would send the
// local token and prompts through HTTP_PROXY.
func proxyFor(r *http.Request) (*url.URL, error) {
	if security.IsLoopbackHost(r.URL.Hostname()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(r)
}

// Health calls GET /healthz, which needs no token.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var out Health
	if err := c.call(ctx, http.MethodGet, "/healthz", false, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Bootstrap calls GET /v1/bootstrap.
func (c *Client) Bootstrap(ctx context.Context) (*Bootstrap, error) {
	var out Bootstrap
	if err := c.call(ctx, http.MethodGet, "/v1/bootstrap", true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status calls GET /v1/status.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var out Status
	if err := c.call(ctx, http.MethodGet, "/v1/status", true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Models calls GET /v1/models, which requires manual_model_selection.
func (c *Client) Models(ctx context.Context) (*Models, error) {
	var out Models
	if err := c.call(ctx, http.MethodGet, "/v1/models", true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Privacy calls GET /v1/privacy.
func (c *Client) Privacy(ctx context.Context) (*Privacy, error) {
	var out Privacy
	if err := c.call(ctx, http.MethodGet, "/v1/privacy", true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Conversations calls GET /v1/conversations, most recently updated first. A limit
// of zero or less uses the gateway's default.
func (c *Client) Conversations(ctx context.Context, limit int) ([]ConversationSummary, error) {
	path := "/v1/conversations"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out conversationList
	if err := c.call(ctx, http.MethodGet, path, true, &out); err != nil {
		return nil, err
	}
	return out.Conversations, nil
}

// Conversation calls GET /v1/conversations/{id}.
func (c *Client) Conversation(ctx context.Context, id string) (*Conversation, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var out Conversation
	if err := c.call(ctx, http.MethodGet, "/v1/conversations/"+id, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteConversation calls DELETE /v1/conversations/{id}.
func (c *Client) DeleteConversation(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/v1/conversations/"+id, true, nil)
}

// DeleteAllConversations calls DELETE /v1/conversations and returns how many were
// deleted.
func (c *Client) DeleteAllConversations(ctx context.Context) (int, error) {
	var out deletedCount
	if err := c.call(ctx, http.MethodDelete, "/v1/conversations", true, &out); err != nil {
		return 0, err
	}
	return out.Deleted, nil
}

func checkID(id string) error {
	if identifier.MatchString(id) {
		return nil
	}
	return &Error{Kind: KindAPI, Code: "validation_error", Message: "That conversation can't be opened."}
}

// call runs one JSON request under the request timeout and decodes into out.
func (c *Client) call(ctx context.Context, method, path string, auth bool, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	req, err := c.newRequest(ctx, method, path, nil, auth)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return networkError(0, err)
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return networkError(resp.StatusCode, err)
	}
	if err := decode(data, out); err != nil {
		return networkError(resp.StatusCode, err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte, auth bool) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, networkError(0, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// checkResponse turns any non-2xx response into an *Error. Any 3xx is a
// connection failure, so a redirect can never carry a prompt elsewhere.
func checkResponse(resp *http.Response) error {
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		return nil
	}
	if code >= 300 && code < 400 {
		return networkError(code, fmt.Errorf("refused a redirect (HTTP %d)", code))
	}
	e := apiError(code, readErrorBody(resp.Body))
	e.RequestID = security.Clean(resp.Header.Get("X-Request-ID"))
	if code == http.StatusUnauthorized {
		e.Kind = KindAuth
	}
	return e
}

type errorEnvelope struct {
	Error *errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Detail    string `json:"detail"`
	Retryable bool   `json:"retryable"`
}

func readErrorBody(r io.Reader) *errorBody {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil
	}
	var env errorEnvelope
	if decode(data, &env) != nil {
		return nil
	}
	return env.Error
}

// apiError builds the error, tolerating a non-2xx from something that is not the
// gateway, such as a proxy page.
func apiError(status int, body *errorBody) *Error {
	if body == nil || body.Code == "" || body.Message == "" {
		return &Error{Kind: KindAPI, Status: status, Code: "internal", Message: messageGeneric, Retryable: status >= 500}
	}
	return &Error{
		Kind:      KindAPI,
		Status:    status,
		Code:      body.Code,
		Message:   body.Message,
		Detail:    body.Detail,
		Retryable: body.Retryable,
	}
}

// decode parses JSON and cleans every string in the result, so that a new field
// cannot miss the sanitizer.
func decode(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	cleanValue(reflect.ValueOf(out))
	return nil
}

func cleanValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			cleanValue(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			cleanValue(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			cleanValue(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(security.Clean(v.String()))
		}
	}
}

// ParseTime reads a gateway timestamp.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
