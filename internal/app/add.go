package app

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newAddCmd) }

func newAddCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "add <provider> <alias>",
		Short: "Log in through the CLI (or enter its API token) and save it as provider@alias",
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
			if err := a.CheckConflicts(e.vars()); err != nil {
				return err
			}
			v, err := e.initVault()
			if err != nil {
				return err
			}
			if v.Exists(r) {
				return fmt.Errorf("%s already exists (whoas rm %s first)", r, r)
			}
			if a.Kind == adapter.KindEnvToken || a.Kind == adapter.KindMCP {
				if a.Login.Hint != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "hint:", a.Login.Hint)
				}
				loginBin := ""
				if len(a.Login.Cmd) > 0 && !fromStdin {
					if loginBin, err = e.realBin(a.Bin); err != nil {
						return err
					}
				}
				values, err := obtainSecrets(a, r, loginBin, fromStdin, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ttyAsk)
				if err != nil {
					return err
				}
				account := ""
				if a.Kind == adapter.KindEnvToken {
					if bin, err := e.realBin(a.Bin); err == nil {
						account = whoamiWithSecrets(a, bin, values)
					}
				}
				return e.saveLocked(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, envelope.FromSecrets(r.Provider, values).Files, account)
			}
			if fromStdin {
				return errors.New("--stdin is only for env-token and mcp adapters")
			}
			bin, err := e.realBin(a.Bin)
			if err != nil {
				return err
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			dir := e.dirs.CaptureDir(r.Provider, r.Alias)
			defer overlay.Remove(dir)
			if err := overlay.Build(dir, a.GlobalDir(e.vars()), a.Dir.AuthFiles, nil); err != nil {
				return err
			}
			if a.Login.Hint != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "hint:", a.Login.Hint)
			}
			// whoas stays alive while the CLI's interactive login runs so the capture dir is
			// always removed; the terminal still delivers Ctrl-C to the CLI itself.
			sigs := make(chan os.Signal, 4)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
			err = runIn(a, bin, dir, a.Login.Cmd[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			signal.Stop(sigs)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			files, err := overlay.Collect(dir, a.Dir.AuthFiles)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("login produced none of %v; nothing saved", a.Dir.AuthFiles)
			}
			if err := e.saveLocked(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, files, whoami(a, bin, dir)); err != nil {
				return err
			}
			warnHelperKeys(cmd.ErrOrStderr(), r, a, files)
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "env-token and mcp adapters: read one secret per line from stdin (skips login.cmd)")
	return cmd
}
