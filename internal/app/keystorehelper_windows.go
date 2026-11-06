//go:build windows

package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/keystore"
)

// keystore-helper prints the master key, so only the Windows build has it.
func init() { register(newKeystoreHelperCmd) }

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
