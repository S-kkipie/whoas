package app

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newProfileCmd) }

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Manage profiles (provider → alias maps)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "set <name> [provider@alias...]",
			Short: "Create a profile or bind identities in it",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				if err := ref.ValidAlias(args[0]); err != nil {
					return err
				}
				e.cfg.EnsureProfile(args[0])
				if len(args) > 1 {
					v, err := e.openVault()
					if err != nil {
						return err
					}
					for _, s := range args[1:] {
						r, err := ref.Parse(s)
						if err != nil {
							return err
						}
						if !v.Exists(r) {
							return fmt.Errorf("identity %s not found (see: whoas ls)", r)
						}
						e.cfg.Bind(args[0], r)
					}
				}
				if err := e.cfg.Save(e.dirs.ConfigFile()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "saved profile", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "ls",
			Short: "List profiles (* = default)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				for _, n := range e.cfg.ProfileNames() {
					mark := " "
					if n == e.cfg.DefaultProfile {
						mark = "*"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", mark, n)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "show <name>",
			Short: "Show a profile's bindings",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				b, ok := e.cfg.Profile(args[0])
				if !ok {
					return fmt.Errorf("unknown profile %q", args[0])
				}
				providers := make([]string, 0, len(b))
				for p := range b {
					providers = append(providers, p)
				}
				sort.Strings(providers)
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				for _, p := range providers {
					fmt.Fprintf(tw, "%s\t%s\n", p, b[p])
				}
				return tw.Flush()
			},
		},
		&cobra.Command{
			Use:   "default <name>",
			Short: "Set the profile used when nothing else applies",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				if _, ok := e.cfg.Profile(args[0]); !ok {
					return fmt.Errorf("unknown profile %q", args[0])
				}
				e.cfg.DefaultProfile = args[0]
				if err := e.cfg.Save(e.dirs.ConfigFile()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "default profile:", args[0])
				return nil
			},
		},
	)
	return cmd
}
