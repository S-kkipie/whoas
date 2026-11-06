package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/adapter"
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
			a, err := e.adapter(provider)
			if err != nil {
				return err
			}
			req := runRequest{provider: provider, alias: alias, profileFlag: profile, explicit: true}
			if a.Kind == adapter.KindMCP {
				// as `whoas mcp proxy` would resolve it here
				if req.contextDir, err = mcpContextDir(); err != nil {
					return err
				}
			}
			res, err := e.resolveIdentity(req)
			if errors.Is(err, resolve.ErrUnresolved) {
				if a.Kind == adapter.KindMCP {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: no identity (whoas mcp proxy exits 2)\n", provider)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: no identity (shims pass through to the global CLI)\n", provider)
				}
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
