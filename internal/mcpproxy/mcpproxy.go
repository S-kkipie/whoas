// Package mcpproxy is the stdio MCP server behind `whoas mcp proxy`: it
// bridges JSON-RPC messages between an agent (newline-delimited JSON on
// stdin/stdout) and a remote Streamable HTTP MCP server, adding the
// identity's headers to every request it sends to that server. The headers
// hold secrets: they live only in this process's memory and never reach a
// log, an error message or another process. Errors are redacted by the
// identity's secret values, so the literal text of a header is kept.
package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures Run.
type Options struct {
	URL     string            // the remote Streamable HTTP endpoint
	Headers map[string]string // rendered headers (secrets); added to every request to URL's origin
	Secrets []string          // the identity's secret values, redacted from every error
	Stdin   io.Reader         // messages from the agent
	Stdout  io.Writer         // messages to the agent
	Version string            // whoas version, sent as "User-Agent: whoas/<version>"
}

// Run proxies until the agent closes stdin or ctx ends (nil), or until the
// remote fails, including a failed initialize (an error, with every value of
// o.Secrets redacted from its text).
func Run(ctx context.Context, o Options) error {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid MCP URL %q", o.URL)
	}
	s := &session{}
	rt := &headerTransport{base: http.DefaultTransport, origin: origin(u), headers: o.Headers, userAgent: "whoas/" + o.Version, session: s}
	client := &http.Client{
		Transport: rt,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if origin(req.URL) != rt.origin {
				return fmt.Errorf("refusing to follow a redirect to another origin (%s)", origin(req.URL))
			}
			return nil
		},
	}
	// The standalone SSE stream needs the SDK's own client session; the
	// bridge forwards raw messages, so the server reaches the agent only
	// through the responses to its requests.
	remote, err := (&mcp.StreamableClientTransport{Endpoint: o.URL, HTTPClient: client, DisableStandaloneSSE: true}).Connect(ctx)
	secrets := secretList(o.Secrets)
	if err != nil {
		return redactErr(err, secrets)
	}
	local, err := (&mcp.IOTransport{Reader: io.NopCloser(o.Stdin), Writer: nopWriteCloser{o.Stdout}}).Connect(ctx)
	if err != nil {
		remote.Close()
		return err
	}
	return redactErr(bridge(ctx, local, remote, s, secrets), secrets)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// headerTransport adds the identity's headers and whoas's User-Agent to
// requests for the remote's origin (never to another host), plus the
// protocol version negotiated during initialize when the request lacks it.
// It never logs anything.
type headerTransport struct {
	base      http.RoundTripper
	origin    string
	headers   map[string]string
	userAgent string
	session   *session
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if origin(req.URL) != t.origin {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("User-Agent", t.userAgent)
	if v := t.session.protocolVersion(); v != "" && r.Header.Get("Mcp-Protocol-Version") == "" {
		r.Header.Set("Mcp-Protocol-Version", v)
	}
	return t.base.RoundTrip(r)
}

// session is what the bridge learns from the messages it forwards.
type session struct {
	mu      sync.Mutex
	initID  *jsonrpc.ID // the agent's initialize call
	version string      // protocol version from the initialize result (legacy handshake only)
}

func (s *session) protocolVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// bridge forwards messages between local (the agent) and remote (the
// server) until either side ends, then closes both. It returns nil when the
// agent ended the session, and an error when the remote failed (or the
// initialize call could not be delivered).
func bridge(ctx context.Context, local, remote mcp.Connection, s *session, secrets []string) error {
	ctx, cancel := context.WithCancel(ctx)
	b := &bridger{local: local, remote: remote, s: s, secrets: secrets}
	up, down := make(chan error, 1), make(chan error, 1)
	go func() { up <- b.up(ctx) }()
	go func() { down <- b.down(ctx) }()
	var err error
	select {
	case err = <-up:
		cancel()
		<-down
	case err = <-down:
		cancel()
		<-up
	}
	b.calls.Wait()
	local.Close()
	remote.Close()
	return err
}

type bridger struct {
	local, remote mcp.Connection
	s             *session
	secrets       []string
	calls         sync.WaitGroup // calls being sent concurrently
}

// up forwards the agent's messages to the remote. Only initialize is sent
// in order (the server's session, and with it Mcp-Session-Id, starts there);
// every other call is sent concurrently, so a slow call does not hold back
// the others or a notifications/cancelled meant for it. Under protocol
// 2026-07-28 there is no initialize at all (server/discover is stateless).
func (b *bridger) up(ctx context.Context) error {
	for {
		msg, err := b.local.Read(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading from the agent: %w", err)
		}
		req, isReq := msg.(*jsonrpc.Request)
		isCall := isReq && req.IsCall()
		if isCall {
			b.s.noteCall(req)
		}
		if isCall && req.Method != "initialize" {
			b.calls.Add(1)
			go func() {
				defer b.calls.Done()
				b.send(ctx, req)
			}()
			continue
		}
		if err := b.send(ctx, msg); err != nil {
			return err
		}
	}
}

// send writes msg to the remote. When a call cannot be delivered, the agent
// gets an error response for it; a failed initialize ends the session.
func (b *bridger) send(ctx context.Context, msg jsonrpc.Message) error {
	err := b.remote.Write(ctx, msg)
	if err == nil || ctx.Err() != nil {
		return nil
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok || !req.IsCall() {
		return nil
	}
	werr := &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "whoas: " + redact(err.Error(), b.secrets)}
	var remoteErr *jsonrpc.Error
	// The SDK wraps its "rejected by transport" sentinel (-32005) around
	// transient HTTP statuses (429, 5xx) and every network failure, so that
	// error says nothing; the full text names the cause.
	if errors.As(err, &remoteErr) && remoteErr.Code != errRejectedCode {
		werr = &jsonrpc.Error{Code: remoteErr.Code, Message: redact(remoteErr.Message, b.secrets), Data: redactData(remoteErr.Data, b.secrets)}
	}
	_ = b.local.Write(context.WithoutCancel(ctx), &jsonrpc.Response{ID: req.ID, Error: werr})
	if req.Method == "initialize" {
		return fmt.Errorf("initialize: %w", err)
	}
	return nil
}

// errRejectedCode is the code of the SDK's jsonrpc2.ErrRejected sentinel.
const errRejectedCode = -32005

// noteCall records the agent's initialize call, whose result carries the
// protocol version. Sessions at 2026-07-28 or later (server/discover) are
// left alone on purpose: the SDK sets Mcp-Protocol-Version and Mcp-Method
// itself on those requests, and a version header without Mcp-Method on a
// notification is a 400 that ends the session.
func (s *session) noteCall(req *jsonrpc.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Method {
	case "initialize":
		id := req.ID
		s.initID = &id
	}
}

// noteResponse takes the protocol version from the result of the initialize
// call that resp answers.
func (s *session) noteResponse(resp *jsonrpc.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initID != nil && *s.initID == resp.ID {
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(resp.Result, &r) == nil {
			s.version = r.ProtocolVersion
		}
		return
	}
}

// down forwards the remote's messages to the agent and records the protocol
// version from the initialize result.
func (b *bridger) down(ctx context.Context) error {
	for {
		msg, err := b.remote.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.Error == nil {
			b.s.noteResponse(resp)
		}
		if err := b.local.Write(ctx, msg); err != nil {
			return nil // the agent is gone
		}
	}
}

// secretList lists the strings to redact: each non-empty secret value, once,
// longest first (so a secret that contains another is redacted whole).
func secretList(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func redact(s string, secrets []string) string {
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

func redactErr(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	return errors.New(redact(err.Error(), secrets))
}

// redactData redacts the data of a JSON-RPC error. A JSON string is redacted
// as text. Any other value is re-encoded first (which undoes escapes such as
// \u002d inside its strings) and its JSON text is redacted, secrets in their
// JSON-escaped form too; if that text is no longer valid JSON (a secret was
// part of a number or a key's syntax), the agent gets it as a string.
func redactData(data json.RawMessage, secrets []string) json.RawMessage {
	if len(data) == 0 || len(secrets) == 0 {
		return data
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return jsonString(redact(string(data), secrets))
	}
	if s, ok := v.(string); ok {
		return jsonString(redact(s, secrets))
	}
	text := redact(encodeJSON(v), jsonSecrets(secrets))
	if !json.Valid([]byte(text)) {
		return jsonString(text)
	}
	return json.RawMessage(text)
}

// jsonSecrets adds the JSON-escaped form of each secret that has one.
func jsonSecrets(secrets []string) []string {
	out := slices.Clone(secrets)
	for _, v := range secrets {
		if e := encodeJSON(v); e[1:len(e)-1] != v {
			out = append(out, e[1:len(e)-1])
		}
	}
	return secretList(out)
}

// encodeJSON encodes v without HTML escaping (so "&" stays "&").
func encodeJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // decoded JSON (or a string) always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

func jsonString(s string) json.RawMessage { return json.RawMessage(encodeJSON(s)) }
