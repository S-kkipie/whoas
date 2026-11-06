package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/binpath"
)

func init() { register(newRunCmd) }

func newRunCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "run [-p profile] -- <command> [args...]",
		Short: "Run a command whose nested CLI calls go through whoas shims",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return &exitError{code: exitWhoasError, err: err}
			}
			if profile != "" {
				if _, ok := e.cfg.Profile(profile); !ok {
					return &exitError{code: exitWhoasError, err: fmt.Errorf("unknown profile %q", profile)}
				}
			}
			pathList := e.dirs.ShimsDir() + string(os.PathListSeparator) + os.Getenv("PATH")
			bin := args[0]
			if !strings.ContainsRune(bin, '/') && !strings.ContainsRune(bin, os.PathSeparator) {
				if bin, err = binpath.Find(args[0], pathList, "", ""); err != nil {
					return &exitError{code: exitNotFound, err: err}
				}
			} else if _, err := os.Stat(bin); err != nil {
				return &exitError{code: exitNotFound, err: err}
			}
			env := setEnv(os.Environ(), append([]string{"PATH=" + pathList}, contextEnv()...)...)
			if profile != "" {
				env = setEnv(env, "WHOAS_PROFILE="+profile)
			}
			return &exitError{code: execReplace(bin, args, env)}
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "profile for every nested CLI")
	cmd.Flags().SetInterspersed(false)
	return cmd
}
