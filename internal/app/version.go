package app

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() { register(newVersionCmd) }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the whoas version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "whoas", Version)
			return nil
		},
	}
}
