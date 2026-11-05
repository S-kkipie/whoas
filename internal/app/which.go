package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/resolve"
)

func init() { register(newWhichCmd) }

func newWhichCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "which <provider>",
		Short: "Show which identity a provider would run as, and why",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			provider, alias, err := ref.Split(args[0])
			if err != nil {
				return err
			}
			if _, err := e.adapter(provider); err != nil {
				return err
			}
			res, err := e.resolveIdentity(runRequest{provider: provider, alias: alias, profileFlag: profile, explicit: true})
			if errors.Is(err, resolve.ErrUnresolved) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: no identity (shims pass through to the global CLI)\n", provider)
				return &exitError{code: 1}
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  via %s\n", res.Ref, res.Source)
			return nil
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "resolve as if -p were given")
	return cmd
}
