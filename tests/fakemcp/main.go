// Command fakemcp is a Streamable HTTP MCP server for the e2e tests. It
// listens on a loopback port, writes a whoas user adapter for itself
// (mcp-fake, server "fake") to the path given as its only argument, and
// requires "Authorization: Bearer <key>": keys containing "revoked" get 401.
// Its whoami tool returns the key it received. SIGINT/SIGTERM stop it.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const adapter = `name = "mcp-fake"
kind = "mcp"

[mcp]
server  = "fake"
url     = "%s"
headers = { Authorization = "Bearer {FAKE_MCP_KEY}" }

[[secret]]
env   = "FAKE_MCP_KEY"
label = "Fake MCP key"

[login]
hint = "fake mcp hint"
`

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fakemcp ADAPTER-PATH")
		os.Exit(2)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fakemcp", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "the API key this server received"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			key := strings.TrimPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "key=" + key}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" || strings.Contains(key, "revoked") {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
	body := fmt.Sprintf(adapter, "http://"+ln.Addr().String()+"/mcp")
	tmp := os.Args[1] + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
	hs := &http.Server{Handler: auth}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		hs.Close()
	}()
	if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
}
