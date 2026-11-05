package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/keystore"
)

func init() {
	register(newAgentCmd)
	register(newUnlockCmd)
	register(newLockCmd)
	register(newKeystoreHelperCmd)
}

func newAgentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent",
		Short: "Run the key agent in the foreground (started automatically when needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return agent.NewServer().Serve(ctx, e.dirs.AgentSocket())
		},
	}
}

func newUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Load the master key into the agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			st, err := e.store()
			if err != nil {
				return err
			}
			secret, err := st.Get()
			if err != nil {
				return fmt.Errorf("master key (%s): %w", st.Name(), err)
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			c := agent.NewClient(e.dirs.AgentSocket())
			if _, err := c.Status(); err != nil {
				if err := agent.Spawn(self(), e.dirs.AgentSocket()); err != nil {
					return err
				}
			}
			if err := c.Load(secret, st.TTL()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unlocked (%s)\n", st.Name())
			return nil
		},
	}
}

func newLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Make the agent forget the master key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if err := agent.NewClient(e.dirs.AgentSocket()).Lock(); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "agent not running")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "locked")
			return nil
		},
	}
}

// newKeystoreHelperCmd is what WSL calls on whoas.exe to reach the Windows
// Credential Manager. get: exit 2 when absent. set: secret on stdin.
func newKeystoreHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "keystore-helper <get|set>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := keystore.OS()
			switch args[0] {
			case "get":
				s, err := st.Get()
				if errors.Is(err, keystore.ErrNotFound) {
					return &exitError{code: 2}
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), s)
				return nil
			case "set":
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				return st.Set(strings.TrimSpace(string(b)))
			}
			return fmt.Errorf("unknown action %q (get|set)", args[0])
		},
	}
}
