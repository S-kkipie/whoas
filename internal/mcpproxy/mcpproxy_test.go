package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	for name, opts := range map[string]*mcp.ClientSessionOptions{
		"initialize":     {ProtocolVersion: "2025-11-25"},
		"latest version": nil,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ts, rec := testServer(t, "k1-secret-value", nil, nil)
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
					require.NotEmpty(t, s.version, "the negotiated protocol version is sent")
				}
			}
			require.Equal(t, 1, calls)
		})
	}
}

// After initialization, calls are sent concurrently: a server that answers
// with plain JSON holds each POST open until its result is ready, and a slow
// call must not hold back the others.
func TestRunConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gate := make(chan struct{})
	ts, _ := testServer(t, "k1-secret-value", &mcp.StreamableHTTPOptions{JSONResponse: true}, gate)
	cs, done, err := agent(t, ctx, Options{URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer k1-secret-value"}, Version: "v9"}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err)
	require.Equal(t, "key=k1-secret-value", res.Content[0].(*mcp.TextContent).Text)
	close(gate)
	require.Equal(t, "released", <-slow)
	require.NoError(t, cs.Close())
	require.NoError(t, wait(t, done))
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
	local, remote := newFakeConn(), newFakeConn()
	local.in <- call(7, "tools/list")
	local.in <- call(8, "initialize")
	remote.writeErr = func(m jsonrpc.Message) error {
		if r, ok := m.(*jsonrpc.Request); ok {
			return errors.New(`sending "` + r.Method + `": Bad Gateway for Bearer secret-token-1`)
		}
		return nil
	}
	err := bridge(context.Background(), local, remote, &session{}, secretsOf(map[string]string{"Authorization": "Bearer secret-token-1"}))
	require.EqualError(t, err, `initialize: sending "initialize": Bad Gateway for Bearer secret-token-1`, "bridge returns the raw error; Run redacts it")
	msgs := local.messages()
	require.Len(t, msgs, 2, "each undelivered call gets an error response")
	for i, id := range []int64{7, 8} {
		resp := msgs[i].(*jsonrpc.Response)
		want, _ := jsonrpc.MakeID(float64(id))
		require.Equal(t, want, resp.ID)
		require.ErrorContains(t, resp.Error, "Bad Gateway for [redacted]")
		require.NotContains(t, resp.Error.Error(), "secret-token-1")
	}
}

func TestRedact(t *testing.T) {
	s := secretsOf(map[string]string{"Authorization": "Bearer phx_abcdef123456", "X-Org": "42"})
	require.Equal(t, "got [redacted] and [redacted] and [redacted]", redact("got Bearer phx_abcdef123456 and phx_abcdef123456 and 42", s))
	require.NoError(t, redactErr(nil, nil))
	require.EqualError(t, redactErr(errors.New("bad Bearer phx_abcdef123456"), map[string]string{"Authorization": "Bearer phx_abcdef123456"}), "bad [redacted]")
}
