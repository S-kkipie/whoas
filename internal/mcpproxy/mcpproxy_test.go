package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// seen is what the test server recorded about one POST.
type seen struct{ method, agent, version string }

type recorder struct {
	mu   sync.Mutex
	list []seen
}

func (r *recorder) all() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.list...)
}

// testServer is a Streamable HTTP MCP server that only accepts
// "Authorization: Bearer <key>"; its whoami tool returns the key it got and
// its wait tool blocks until gate is closed.
func testServer(t *testing.T, key string, opts *mcp.StreamableHTTPOptions, gate chan struct{}) (*httptest.Server, *recorder) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "the key this server received"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			got := strings.TrimPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "key=" + got}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "wait", Description: "blocks until the test releases it"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			select {
			case <-gate:
			case <-ctx.Done():
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "released"}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, opts)
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var m struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &m)
			rec.mu.Lock()
			rec.list = append(rec.list, seen{m.Method, r.Header.Get("User-Agent"), r.Header.Get("Mcp-Protocol-Version")})
			rec.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

// agent starts Run on pipes and connects a go-sdk client to it, as an agent
// does over stdio. done receives Run's result.
func agent(t *testing.T, ctx context.Context, o Options, opts *mcp.ClientSessionOptions) (cs *mcp.ClientSession, done chan error, err error) {
	t.Helper()
	toProxyR, toProxyW := io.Pipe()
	fromProxyR, fromProxyW := io.Pipe()
	o.Stdin, o.Stdout = toProxyR, fromProxyW
	done = make(chan error, 1)
	go func() {
		done <- Run(ctx, o)
		fromProxyW.Close() // the agent sees the server exit
	}()
	c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	cs, err = c.Connect(ctx, &mcp.IOTransport{Reader: fromProxyR, Writer: toProxyW}, opts)
	return cs, done, err
}

func wait(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not stop")
		return nil
	}
}

func TestRunInjectsHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		opts      *mcp.ClientSessionOptions
		server    *mcp.StreamableHTTPOptions
		wantAfter string // the Mcp-Protocol-Version of the tools/call
	}{
		"initialize":                      {&mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"}, nil, "2025-11-25"},
		"latest version, stateful":        {nil, nil, "2025-11-25"}, // the stateful server rejects 2026-07-28, so the client initializes
		"latest version, server/discover": {nil, &mcp.StreamableHTTPOptions{Stateless: true}, "2026-07-28"},
	} {
		opts := tc.opts
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ts, rec := testServer(t, "k1-secret-value", tc.server, nil)
			cs, done, err := agent(t, ctx, Options{URL: ts.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, opts)
			require.NoError(t, err)
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
			require.NoError(t, err)
			require.Equal(t, "key=k1-secret-value", res.Content[0].(*mcp.TextContent).Text)
			require.NoError(t, cs.Close())
			require.NoError(t, wait(t, done), "the agent closing stdin ends the proxy cleanly")

			calls := 0
			for _, s := range rec.all() {
				require.Equal(t, "whoas/v9", s.agent, s.method)
				if s.method == "tools/call" {
					calls++
					require.Equal(t, tc.wantAfter, s.version, "the negotiated protocol version is sent")
				}
			}
			require.Equal(t, 1, calls)
		})
	}
}

// Calls are sent concurrently whatever the protocol version: a server that
// holds each POST open until its result is ready (plain JSON, or an SSE
// stream) must not let a slow call hold back the others. The stateless cases
// leave the client's version unpinned, so it uses server/discover and never
// sends initialize.
func TestRunConcurrentCalls(t *testing.T) {
	for name, tc := range map[string]struct {
		opts   *mcp.ClientSessionOptions
		server *mcp.StreamableHTTPOptions
	}{
		"2025-11-25 stateful, JSON": {&mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"}, &mcp.StreamableHTTPOptions{JSONResponse: true}},
		"latest stateless, JSON":    {nil, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}},
		"latest stateless, SSE":     {nil, &mcp.StreamableHTTPOptions{Stateless: true}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			gate := make(chan struct{})
			var release sync.Once
			ts, _ := testServer(t, "k1-secret-value", tc.server, gate)
			t.Cleanup(func() { release.Do(func() { close(gate) }) }) // runs before ts.Close, even when the test fails
			cs, done, err := agent(t, ctx, Options{URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, tc.opts)
			require.NoError(t, err)
			slow := make(chan string, 1)
			go func() {
				res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait"})
				if err != nil {
					slow <- err.Error()
					return
				}
				slow <- res.Content[0].(*mcp.TextContent).Text
			}()
			time.Sleep(100 * time.Millisecond) // let the slow call reach the server first
			quick, qcancel := context.WithTimeout(ctx, 3*time.Second)
			defer qcancel()
			res, err := cs.CallTool(quick, &mcp.CallToolParams{Name: "whoami"})
			require.NoError(t, err, "a call is not held back by a pending one")
			require.Equal(t, "key=k1-secret-value", res.Content[0].(*mcp.TextContent).Text)
			release.Do(func() { close(gate) })
			require.Equal(t, "released", <-slow)
			require.NoError(t, cs.Close())
			require.NoError(t, wait(t, done))
		})
	}
}

// Under protocol 2026-07-28 the SDK sets the version and Mcp-Method headers
// itself; a notifications/cancelled that the proxy stamped with a version
// header (but no Mcp-Method) is a 400, which would end the session.
func TestRunSurvivesCancelledCallWhenStateless(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gate := make(chan struct{})
	var release sync.Once
	ts, _ := testServer(t, "k1-secret-value", &mcp.StreamableHTTPOptions{Stateless: true}, gate)
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	cs, done, err := agent(t, ctx, Options{URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, nil)
	require.NoError(t, err)

	slowCtx, slowCancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(slowCtx, &mcp.CallToolParams{Name: "wait"})
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	slowCancel() // the client sends notifications/cancelled through the proxy
	require.Error(t, <-errc)
	time.Sleep(300 * time.Millisecond) // let the notification reach the server

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err, "the session survives the cancellation")
	require.Equal(t, "key=k1-secret-value", res.Content[0].(*mcp.TextContent).Text)
	select {
	case err := <-done:
		t.Fatalf("the proxy ended: %v", err)
	default:
	}
	require.NoError(t, cs.Close())
	require.NoError(t, wait(t, done))
}

// failingFront forwards to ts except for tools/call, which it answers with
// status (and a body that echoes the key, which must never reach the agent).
func failingFront(t *testing.T, ts *httptest.Server, status int) *httptest.Server {
	t.Helper()
	target, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if strings.Contains(string(body), `"tools/call"`) {
				http.Error(w, "upstream said "+r.Header.Get("Authorization"), status)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	return front
}

// A transient HTTP failure reaches the agent with its cause, not as the
// SDK's bare "rejected by transport", and without the key.
func TestRunTransientFailureKeepsCause(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusBadGateway, "Bad Gateway"}, {http.StatusTooManyRequests, "Too Many Requests"}} {
		t.Run(tc.want, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ts, _ := testServer(t, "k1-secret-value", &mcp.StreamableHTTPOptions{Stateless: true}, nil)
			front := failingFront(t, ts, tc.status)
			cs, done, err := agent(t, ctx, Options{URL: front.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, nil)
			require.NoError(t, err)
			_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "k1-secret-value")
			// the session survives a transient failure
			_, err = cs.ListTools(ctx, nil)
			require.NoError(t, err)
			require.NoError(t, cs.Close())
			require.NoError(t, wait(t, done))
		})
	}
}

// A redirect to another origin is refused: the other origin sees no request
// (so no headers) and the agent's call fails.
func TestRunRefusesCrossOriginRedirect(t *testing.T) {
	var hits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, done, err := agent(t, ctx, Options{URL: origin.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.Error(t, err)
	perr := wait(t, done)
	require.ErrorContains(t, perr, "another origin")
	require.NotContains(t, perr.Error(), "k1-secret-value")
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, hits, "the other origin never got a request")
}

func TestRunRemoteRejectsKey(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(io.Discard)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts, _ := testServer(t, "right-key-value", nil, nil)
	_, done, err := agent(t, ctx, Options{URL: ts.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer wrong-key-value"}, Version: "v9"}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.Error(t, err, "the agent gets an error for its initialize call")
	require.NotContains(t, err.Error(), "wrong-key-value")
	perr := wait(t, done)
	require.ErrorContains(t, perr, "initialize")
	require.ErrorContains(t, perr, "Unauthorized")
	require.NotContains(t, perr.Error(), "wrong-key-value")
	require.NotContains(t, logs.String(), "wrong-key-value", "nothing logs the key")
}

func TestHeaderTransport(t *testing.T) {
	var got []*http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r)
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})
	s := &session{}
	rt := &headerTransport{base: base, origin: "https://mcp.example.com", headers: map[string]string{"Authorization": "Bearer k"}, userAgent: "whoas/v1", session: s}

	req, _ := http.NewRequest(http.MethodPost, "https://mcp.example.com/mcp", nil)
	_, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, "Bearer k", got[0].Header.Get("Authorization"))
	require.Equal(t, "whoas/v1", got[0].Header.Get("User-Agent"))
	require.Empty(t, got[0].Header.Get("Mcp-Protocol-Version"), "unknown before initialize")
	require.Empty(t, req.Header.Get("Authorization"), "the caller's request is not modified")

	s.version = "2025-06-18"
	req, _ = http.NewRequest(http.MethodPost, "https://mcp.example.com/mcp", nil)
	_, _ = rt.RoundTrip(req)
	require.Equal(t, "2025-06-18", got[1].Header.Get("Mcp-Protocol-Version"))
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	_, _ = rt.RoundTrip(req)
	require.Equal(t, "2026-07-28", got[2].Header.Get("Mcp-Protocol-Version"), "a version set by the SDK wins")

	req, _ = http.NewRequest(http.MethodGet, "https://elsewhere.example.com/x", nil)
	_, _ = rt.RoundTrip(req)
	require.Empty(t, got[3].Header.Get("Authorization"), "headers never go to another origin")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeConn is an in-memory mcp.Connection.
type fakeConn struct {
	in       chan jsonrpc.Message
	readErr  error // returned once in is closed
	writeErr func(jsonrpc.Message) error

	mu      sync.Mutex
	written []jsonrpc.Message
	closed  chan struct{}
	once    sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan jsonrpc.Message, 8), readErr: io.EOF, closed: make(chan struct{})}
}

func (c *fakeConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case m, ok := <-c.in:
		if !ok {
			return nil, c.readErr
		}
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *fakeConn) Write(_ context.Context, m jsonrpc.Message) error {
	if c.writeErr != nil {
		if err := c.writeErr(m); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, m)
	return nil
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeConn) SessionID() string { return "" }

func (c *fakeConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *fakeConn) messages() []jsonrpc.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]jsonrpc.Message(nil), c.written...)
}

func call(id int64, method string) *jsonrpc.Request {
	jid, _ := jsonrpc.MakeID(float64(id))
	return &jsonrpc.Request{ID: jid, Method: method, Params: json.RawMessage(`{}`)}
}

func TestBridgeClosesBothSides(t *testing.T) {
	// the agent closes stdin: a clean end
	local, remote := newFakeConn(), newFakeConn()
	local.in <- call(1, "tools/list")
	close(local.in)
	require.NoError(t, bridge(context.Background(), local, remote, &session{}, nil))
	require.True(t, local.isClosed())
	require.True(t, remote.isClosed())
	require.Len(t, remote.messages(), 1, "messages read before the end are forwarded")

	// the remote fails: an error
	local, remote = newFakeConn(), newFakeConn()
	remote.readErr = errors.New("session terminated by the server")
	close(remote.in)
	require.EqualError(t, bridge(context.Background(), local, remote, &session{}, nil), "session terminated by the server")
	require.True(t, local.isClosed())
	require.True(t, remote.isClosed())
}

// A call the remote cannot take gets an error response (secrets redacted);
// a failed initialize also ends the session.
func TestBridgeFailedCalls(t *testing.T) {
	secrets := secretList([]string{"secret-token-1"})
	fail := func(m jsonrpc.Message) error {
		if r, ok := m.(*jsonrpc.Request); ok {
			return errors.New(`sending "` + r.Method + `": Bad Gateway for Bearer secret-token-1`)
		}
		return nil
	}
	check := func(t *testing.T, msg jsonrpc.Message, id int64) {
		t.Helper()
		resp := msg.(*jsonrpc.Response)
		want, _ := jsonrpc.MakeID(float64(id))
		require.Equal(t, want, resp.ID)
		require.ErrorContains(t, resp.Error, "Bad Gateway for Bearer [redacted]")
		require.NotContains(t, resp.Error.Error(), "secret-token-1")
	}

	t.Run("call", func(t *testing.T) {
		local, remote := newFakeConn(), newFakeConn()
		remote.writeErr = fail
		local.in <- call(7, "tools/list") // sent concurrently, so the agent stays connected until it is answered
		done := make(chan error, 1)
		go func() { done <- bridge(context.Background(), local, remote, &session{}, secrets) }()
		require.Eventually(t, func() bool { return len(local.messages()) == 1 }, 5*time.Second, 5*time.Millisecond)
		close(local.in)
		require.NoError(t, <-done, "an undelivered call does not end the session")
		check(t, local.messages()[0], 7)
	})

	t.Run("initialize", func(t *testing.T) {
		local, remote := newFakeConn(), newFakeConn()
		remote.writeErr = fail
		local.in <- call(8, "initialize")
		err := bridge(context.Background(), local, remote, &session{}, secrets)
		require.EqualError(t, err, `initialize: sending "initialize": Bad Gateway for Bearer secret-token-1`, "bridge returns the raw error; Run redacts it")
		require.Len(t, local.messages(), 1)
		check(t, local.messages()[0], 8)
	})

	// The SDK's "rejected by transport" sentinel wraps transient statuses and
	// network failures; the agent gets the whole text, not the sentinel.
	t.Run("rejected by transport", func(t *testing.T) {
		local, remote := newFakeConn(), newFakeConn()
		remote.writeErr = func(m jsonrpc.Message) error {
			return fmt.Errorf(`sending "tools/call": %w: Bad Gateway for Bearer secret-token-1`, &jsonrpc.Error{Code: -32005, Message: "rejected by transport"})
		}
		local.in <- call(9, "tools/call")
		done := make(chan error, 1)
		go func() { done <- bridge(context.Background(), local, remote, &session{}, secrets) }()
		require.Eventually(t, func() bool { return len(local.messages()) == 1 }, 5*time.Second, 5*time.Millisecond)
		close(local.in)
		require.NoError(t, <-done)
		check(t, local.messages()[0], 9)
	})

	// A JSON-RPC error from the remote (an HTTP error whose body is a
	// JSON-RPC response) keeps its code; its message and data are redacted,
	// whatever JSON the data is.
	for name, tc := range map[string]struct{ data, want string }{
		"string data":  {`"key secret-token-1 rejected"`, `"key [redacted] rejected"`},
		"object data":  {`{"got":"Bearer secret-token-1","client":"whoas","n":12345678901234567890}`, `{"client":"whoas","got":"Bearer [redacted]","n":12345678901234567890}`},
		"escaped data": {`{"got":"secret\u002dtoken-1"}`, `{"got":"[redacted]"}`},
		"number data":  {`12345678901234567890`, `12345678901234567890`},
		"no data":      {``, ``},
	} {
		t.Run("remote error, "+name, func(t *testing.T) {
			local, remote := newFakeConn(), newFakeConn()
			remote.writeErr = func(m jsonrpc.Message) error {
				rerr := &jsonrpc.Error{Code: -32001, Message: "key secret-token-1 is not valid for client whoas", Data: json.RawMessage(tc.data)}
				return fmt.Errorf(`sending "tools/call": %w: %w: Unauthorized`, rerr, &jsonrpc.Error{Code: -32005, Message: "rejected by transport"})
			}
			local.in <- call(10, "tools/call")
			done := make(chan error, 1)
			go func() { done <- bridge(context.Background(), local, remote, &session{}, secrets) }()
			require.Eventually(t, func() bool { return len(local.messages()) == 1 }, 5*time.Second, 5*time.Millisecond)
			close(local.in)
			require.NoError(t, <-done)
			resp := local.messages()[0].(*jsonrpc.Response)
			require.Equal(t, int64(-32001), resp.Error.(*jsonrpc.Error).Code)
			werr := resp.Error.(*jsonrpc.Error)
			require.Equal(t, "key [redacted] is not valid for client whoas", werr.Message, "only the secret value is redacted")
			require.Equal(t, tc.want, string(werr.Data))
			wire, err := jsonrpc.EncodeMessage(resp)
			require.NoError(t, err, "the redacted response still encodes")
			require.NotContains(t, string(wire), "secret-token-1")
		})
	}
}

func TestRedact(t *testing.T) {
	s := secretList([]string{"phx_abc", "", "phx_abcdef123456", "42", "phx_abcdef123456"})
	require.Equal(t, []string{"phx_abcdef123456", "phx_abc", "42"}, s, "each non-empty value once, longest first")
	require.Equal(t, "got Bearer [redacted] and [redacted]=[redacted] and [redacted]", redact("got Bearer phx_abcdef123456 and phx_abc=phx_abcdef123456 and 42", s))
	require.Equal(t, "X-Client: whoas", redact("X-Client: whoas", s), "literal header text is not a secret")
	require.NoError(t, redactErr(nil, nil))
	require.EqualError(t, redactErr(errors.New("bad Bearer phx_abcdef123456"), s), "bad Bearer [redacted]")

	// error data: escaped secrets are found, and the result is always valid JSON
	q := secretList([]string{`k"&<1`, "4242"})
	for in, want := range map[string]string{
		`{"a":"k\"&<1","b":"\u0026"}`: `{"a":"[redacted]","b":"&"}`,
		`{"n":4242,"m":1}`:            `"{\"m\":1,\"n\":[redacted]}"`,
		`[true,null,"x"]`:             `[true,null,"x"]`,
	} {
		got := redactData(json.RawMessage(in), q)
		require.Equal(t, want, string(got), in)
		require.True(t, json.Valid(got), in)
	}
	require.Equal(t, `{"a":"k"}`, string(redactData(json.RawMessage(`{"a":"k"}`), nil)), "no secrets: unchanged")
}

// A remote that echoes the request's headers in a JSON-RPC error (status
// 401, the message and the data) never gets the key to the agent, and the
// literal header text around it survives.
func TestRunRedactsRemoteErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts, _ := testServer(t, "k1-secret-value", &mcp.StreamableHTTPOptions{Stateless: true}, nil)
	target, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &m)
			if m.Method == "tools/call" {
				echo := "X-Client=" + r.Header.Get("X-Client") + " Authorization=" + r.Header.Get("Authorization")
				data, _ := json.Marshal(map[string]string{"headers": echo})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32001,"message":%q,"data":%s}}`, m.ID, "rejected "+echo, data)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	o := Options{URL: front.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value", "X-Client": "whoas"}, Secrets: []string{"k1-secret-value"}, Version: "v9"}
	cs, done, err := agent(t, ctx, o, nil)
	require.NoError(t, err)
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	require.Error(t, err)
	var werr *jsonrpc.Error
	require.ErrorAs(t, err, &werr)
	require.Equal(t, int64(-32001), werr.Code)
	require.Equal(t, "rejected X-Client=whoas Authorization=Bearer [redacted]", werr.Message)
	require.JSONEq(t, `{"headers":"X-Client=whoas Authorization=Bearer [redacted]"}`, string(werr.Data))
	require.NotContains(t, err.Error(), "k1-secret-value")
	require.NoError(t, cs.Close())
	require.NoError(t, wait(t, done))
}
