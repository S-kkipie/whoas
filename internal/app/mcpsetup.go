package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
)

// stdioEntry is an agent's config entry for a stdio MCP server.
type stdioEntry struct {
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// mcpSetup is one registration of `whoas mcp proxy` in an agent.
type mcpSetup struct {
	agent   string   // claude | codex
	scope   string   // claude only
	server  string   // name the agent knows the server by
	command string   // absolute path of whoas
	args    []string // mcp proxy <server | provider@alias>
}

func (s mcpSetup) getCmd() []string    { return []string{s.agent, "mcp", "get", s.server} }
func (s mcpSetup) removeCmd() []string { return []string{s.agent, "mcp", "remove", s.server} }

func (s mcpSetup) addCmd() []string {
	if s.agent == "codex" {
		return append([]string{"codex", "mcp", "add", s.server, "--", s.command}, s.args...)
	}
	return []string{"claude", "mcp", "add-json", "--scope", s.scope, s.server, jsonText(stdioEntry{Type: "stdio", Command: s.command, Args: s.args})}
}

// jsonText marshals v without HTML escaping.
func jsonText(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// printEntry is the generic stdio entry for agents set up by hand.
func printEntry(w io.Writer, server, command string, args []string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]map[string]stdioEntry{"mcpServers": {server: {Command: command, Args: args}}}); err != nil {
		return err
	}
	_, err := w.Write(b.Bytes())
	return err
}

var shellSafeRe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// shellQuote renders argv as a POSIX shell command line.
func shellQuote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if shellSafeRe.MatchString(a) {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// whoasPath is the command agents should start: the PATH entry the user ran
// when it is this executable (stable across package upgrades), else the
// executable itself.
func whoasPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := exec.LookPath(os.Args[0]); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			a, errA := os.Stat(abs)
			b, errB := os.Stat(exe)
			if errA == nil && errB == nil && os.SameFile(a, b) {
				return abs, nil
			}
		}
	}
	return exe, nil
}

func newMCPSetupCmd() *cobra.Command {
	var agentName, scope, alias string
	var replace, printOnly bool
	cmd := &cobra.Command{
		Use:   "setup <server> (--agent claude|codex [--scope user|local|project] [--replace] | --print) [--alias <alias>]",
		Short: "Register whoas mcp proxy for a server in an agent (or print the entry for other agents)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.Contains(args[0], "@") {
				return errors.New("pass the alias with --alias, not server@alias")
			}
			switch {
			case printOnly && (agentName != "" || replace || cmd.Flags().Changed("scope")):
				return errors.New("--print cannot be combined with --agent, --scope or --replace")
			case !printOnly && agentName == "":
				return errors.New("--agent claude|codex (or --print) is required")
			case !printOnly && agentName != "claude" && agentName != "codex":
				return fmt.Errorf("unknown agent %q (claude|codex; --print for others)", agentName)
			case agentName == "codex" && cmd.Flags().Changed("scope"):
				return errors.New("codex has no scopes; --scope is only for --agent claude")
			case scope != "user" && scope != "local" && scope != "project":
				return fmt.Errorf("unknown scope %q (user|local|project)", scope)
			}
			if alias != "" {
				if err := ref.ValidAlias(alias); err != nil {
					return err
				}
			}
			e, err := loadEnv()
			if err != nil {
				return err
			}
			a, _, err := e.mcpAdapter(args[0])
			if err != nil {
				return err
			}
			command, err := whoasPath()
			if err != nil {
				return err
			}
			proxyArgs := []string{"mcp", "proxy", a.MCP.Server}
			if alias != "" {
				proxyArgs[2] = a.Name + "@" + alias
			}
			if printOnly {
				return printEntry(cmd.OutOrStdout(), a.MCP.Server, command, proxyArgs)
			}
			s := mcpSetup{agent: agentName, scope: scope, server: a.MCP.Server, command: command, args: proxyArgs}
			bin, err := e.realBin(agentName)
			if err != nil {
				return err
			}
			run := func(argv []string) error {
				fmt.Fprintln(cmd.OutOrStdout(), shellQuote(argv))
				c := exec.Command(bin, argv[1:]...)
				c.Stdin, c.Stdout, c.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
				if err := c.Run(); err != nil {
					return fmt.Errorf("%s mcp %s failed: %w", agentName, argv[2], err)
				}
				return nil
			}
			get := s.getCmd()
			exists := exec.Command(bin, get[1:]...).Run() == nil
			if exists && !replace {
				return fmt.Errorf("%s already has an MCP server named %q (see: %s); use --replace to replace it", agentName, s.server, shellQuote(get))
			}
			removed := false
			if exists {
				if err := run(s.removeCmd()); err != nil {
					return err
				}
				removed = true
			}
			if err := run(s.addCmd()); err != nil {
				if removed {
					return fmt.Errorf("%w; the previous %s entry was already removed — fix the error above and re-run whoas mcp setup %s --agent %s", err, s.server, s.server, agentName)
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent to register the server in: claude or codex")
	cmd.Flags().StringVar(&scope, "scope", "user", "claude config scope: user, local or project")
	cmd.Flags().BoolVar(&replace, "replace", false, "replace an existing server with the same name")
	cmd.Flags().StringVar(&alias, "alias", "", "pin an identity: the proxy runs as <provider>@<alias> everywhere")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the stdio entry for other agents instead")
	return cmd
}
