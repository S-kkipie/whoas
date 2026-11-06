package app

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newImportCmd) }

func newImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <provider> <alias>",
		Short: "Save the CLI's current session (or the API token in your environment) as provider@alias",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			r, err := ref.Parse(args[0] + "@" + args[1])
			if err != nil {
				return err
			}
			a, err := e.adapter(r.Provider)
			if err != nil {
				return err
			}
			v, err := e.initVault()
			if err != nil {
				return err
			}
			if v.Exists(r) {
				return fmt.Errorf("%s already exists (whoas rm %s first)", r, r)
			}
			if a.Kind == adapter.KindEnvToken {
				values, err := secretsFromEnv(a, os.Getenv)
				if err != nil {
					return err
				}
				account := ""
				if bin, err := e.realBin(a.Bin); err == nil {
					account = whoamiWithSecrets(a, bin, values)
				}
				return e.saveLocked(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, envelope.FromSecrets(r.Provider, values).Files, account)
			}
			global := a.GlobalDir(e.vars())
			files, err := overlay.Collect(global, a.Dir.AuthFiles)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("no session found in %s (expected %v)", global, a.Dir.AuthFiles)
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			dir := e.dirs.CaptureDir(r.Provider, r.Alias)
			defer overlay.Remove(dir)
			if err := overlay.Build(dir, global, a.Dir.AuthFiles, files); err != nil {
				return err
			}
			account := ""
			if bin, err := e.realBin(a.Bin); err == nil {
				account = whoami(a, bin, dir)
			}
			files, err = overlay.Collect(dir, a.Dir.AuthFiles)
			if err != nil {
				return err
			}
			if err := e.saveLocked(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, files, account); err != nil {
				return err
			}
			warnHelperKeys(cmd.ErrOrStderr(), r, a, files)
			return nil
		},
	}
}
