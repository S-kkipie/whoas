package app

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newAddCmd) }

func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <provider> <alias>",
		Short: "Log in through the CLI and save the session as provider@alias",
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
			return e.saveLocked(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, files, whoami(a, bin, dir))
		},
	}
}
