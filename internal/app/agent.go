package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/agent"
)

func init() {
	register(newAgentCmd)
	register(newUnlockCmd)
	register(newLockCmd)
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
