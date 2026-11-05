package app

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

func init() {
	register(newLsCmd)
	register(newRmCmd)
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls [provider]",
		Short: "List saved identities (never prints secrets)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			v, err := e.openVault()
			if errors.Is(err, vault.ErrNotInitialized) {
				fmt.Fprintln(cmd.OutOrStdout(), "no identities yet (whoas add <provider> <alias>)")
				return nil
			}
			if err != nil {
				return err
			}
			entries, err := v.List()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "IDENTITY\tACCOUNT\tACTIVE")
			for _, en := range entries {
				if len(args) == 1 && en.Ref.Provider != args[0] {
					continue
				}
				st, _ := runlock.Peek(e.dirs.RunBase(en.Ref.Provider, en.Ref.Alias))
				active := "-"
				if n := len(st.Live()); n > 0 {
					active = fmt.Sprintf("%d process(es)", n)
				}
				account := en.Meta.Account
				if account == "" {
					account = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", en.Ref, account, active)
			}
			return tw.Flush()
		},
	}
}

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <provider@alias>",
		Short: "Delete a saved identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			r, err := ref.Parse(args[0])
			if err != nil {
				return err
			}
			v, err := e.openVault()
			if err != nil {
				return err
			}
			st, _ := runlock.Peek(e.dirs.RunBase(r.Provider, r.Alias))
			if live := st.Live(); len(live) > 0 {
				return fmt.Errorf("%s is in use by pid %v", r, live)
			}
			if err := v.Remove(r); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", r)
			for _, name := range e.cfg.ProfileNames() {
				if b, _ := e.cfg.Profile(name); b[r.Provider] == r.Alias {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: profile %s still maps %s\n", name, r)
				}
			}
			return nil
		},
	}
}
