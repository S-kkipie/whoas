// Command mcpcall is an MCP client for the e2e tests: it starts its
// arguments as a stdio MCP server (as an agent does), calls the whoami tool
// and prints its text. The server's stderr passes through to mcpcall's.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcpcall COMMAND [ARGS...]")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stderr = os.Stderr
	c := mcp.NewClient(&mcp.Implementation{Name: "mcpcall", Version: "1"}, nil)
	// the protocol version today's agents negotiate through initialize
	cs, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpcall: connect:", err)
		os.Exit(1)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpcall: whoami:", err)
		os.Exit(1)
	}
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			fmt.Println(t.Text)
		}
	}
}
