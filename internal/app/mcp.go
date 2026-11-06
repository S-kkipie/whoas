package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/keysource"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/mcpproxy"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/vault"
)

// Exit codes of `whoas mcp proxy` (agents log its stderr).
const (
	exitProxyRemote     = 1 // the remote failed before or during the session
	exitProxyNoIdentity = 2 // no identity resolves (or a usage error)
	exitProxyLocked     = 3 // the vault cannot be read without a terminal
)

func init() { register(newMCPCmd) }

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Use remote MCP servers as a chosen identity, from any AI agent"}
	cmd.AddCommand(&cobra.Command{
		Use:   "proxy <server | provider | provider@alias>",
		Short: "Run a stdio MCP server that forwards to the remote server with the identity's API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if code := mcpProxy(ctx, args[0], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	})
	return cmd
}

// mcpProxy runs `whoas mcp proxy arg` and returns its exit code. It
// resolves the identity from the agent's project (WHOAS_CONTEXT_DIR, else
// the working directory), decrypts it without ever prompting, renders the
// headers and bridges stdin/stdout to the remote server. Errors go to
// stderr, never with a secret.
func mcpProxy(ctx context.Context, arg string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int {
		fmt.Fprintf(stderr, "whoas: mcp proxy %s: %v\n", arg, err)
		return code
	}
	e, err := loadEnv()
	if err != nil {
		return fail(exitProxyRemote, err)
	}
	a, alias, err := e.mcpAdapter(arg)
	if err != nil {
		return fail(exitProxyNoIdentity, err)
	}
	dir, err := mcpContextDir()
	if err != nil {
		return fail(exitProxyRemote, err)
	}
	res, err := e.resolveIdentity(runRequest{provider: a.Name, alias: alias, contextDir: dir})
	if errors.Is(err, errPassthrough) {
		return fail(exitProxyNoIdentity, fmt.Errorf("no identity for %s here (%s); add one with whoas add %s <alias>", a.Name, e.contextLabel(dir), a.Name))
	}
	if err != nil {
		return fail(exitProxyNoIdentity, err)
	}
	v, err := e.openVault()
	if errors.Is(err, vault.ErrNotInitialized) || (err == nil && !v.Exists(res.Ref)) {
		return fail(exitProxyNoIdentity, fmt.Errorf("identity %s (%s) not found; add it with whoas add %s %s", res.Ref, res.Source, a.Name, res.Ref.Alias))
	}
	if err != nil {
		return fail(exitProxyRemote, err)
	}
	d, err := e.mcpDecrypter()
	if err != nil {
		return fail(lockedOr(err), lockedErr(err))
	}
	env, err := v.Load(res.Ref, d)
	if err != nil {
		return fail(lockedOr(err), lockedErr(err))
	}
	headers, err := a.RenderHeaders(env.Secrets())
	if err != nil {
		return fail(exitProxyNoIdentity, fmt.Errorf("%w; remove and re-add it: whoas rm %s && whoas add %s %s", err, res.Ref, a.Name, res.Ref.Alias))
	}
	err = mcpproxy.Run(ctx, mcpproxy.Options{URL: a.MCP.URL, Headers: headers, Stdin: stdin, Stdout: stdout, Version: Version})
	if err != nil {
		return fail(exitProxyRemote, fmt.Errorf("%s: %w", a.MCP.URL, err))
	}
	return 0
}

func lockedOr(err error) int {
	if errors.Is(err, keystore.ErrNeedsTerminal) {
		return exitProxyLocked
	}
	return exitProxyRemote
}

func lockedErr(err error) error {
	if errors.Is(err, keystore.ErrNeedsTerminal) {
		return errors.New("the vault is locked and its key needs a terminal; run whoas unlock")
	}
	return err
}

// mcpAdapter maps the proxy argument to an mcp adapter: the word before any
// "@alias" names an mcp adapter, else an mcp.server. Other adapters never
// match.
func (e *env) mcpAdapter(arg string) (*adapter.Adapter, string, error) {
	name, alias, err := ref.Split(arg)
	if err != nil {
		return nil, "", err
	}
	if a, ok := e.registry.Get(name); ok && a.Kind == adapter.KindMCP {
		return a, alias, nil
	}
	if a, ok := e.registry.ByServer(name); ok {
		return a, alias, nil
	}
	return nil, "", fmt.Errorf("%q is not an MCP server or mcp adapter (see: whoas providers)", name)
}

// contextLabel names what resolution looked at from dir, for "no identity"
// errors: the profile in effect, else the project file, else none.
func (e *env) contextLabel(dir string) string {
	if p := os.Getenv("WHOAS_PROFILE"); p != "" {
		return "profile " + p
	}
	if pf, err := e.findTrustedProject(dir); err == nil && pf != nil {
		if pf.Profile != "" {
			return "profile " + pf.Profile
		}
		return "project " + pf.Path
	}
	if p := e.cfg.DefaultProfile; p != "" {
		return "profile " + p
	}
	return "no profile"
}

// mcpDecrypter is decrypter for the proxy: an unlocked agent first, else the
// keystore, which must not prompt (the terminal belongs to the agent).
func (e *env) mcpDecrypter() (vault.Decrypter, error) {
	c := agent.NewClient(e.dirs.AgentSocket())
	if locked, err := c.Status(); err == nil && !locked {
		return c, nil
	}
	st, err := e.store()
	if err != nil {
		return nil, err
	}
	return keysource.Decrypter(e.dirs.AgentSocket(), keystore.NoPrompt(st), self())
}
