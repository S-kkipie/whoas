package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/project"
	"github.com/S-kkipie/whoas/internal/trust"
)

func init() {
	register(newTrustCmd)
	register(newUntrustCmd)
}

// projectPath returns the given file (absolute) or the nearest .whoas.yaml.
func projectPath(args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	p, err := project.Find(wd)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("no " + project.FileName + " found here or in any parent directory")
	}
	return p, nil
}

func newTrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trust [file]",
		Short: "Approve a .whoas.yaml after reviewing it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			path, err := projectPath(args)
			if err != nil {
				return err
			}
			data, err := project.Read(path)
			if err != nil {
				return err
			}
			if _, err := project.Parse(path, data); err != nil {
				return err
			}
			ts, err := trust.Load(e.dirs.TrustFile())
			if err != nil {
				return err
			}
			ts.Trust(path, data)
			if err := ts.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "trusted", path)
			return nil
		},
	}
}

func newUntrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrust [file]",
		Short: "Revoke approval of a .whoas.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			path, err := projectPath(args)
			if err != nil {
				return err
			}
			ts, err := trust.Load(e.dirs.TrustFile())
			if err != nil {
				return err
			}
			if !ts.Untrust(path) {
				fmt.Fprintln(cmd.OutOrStdout(), "not trusted:", path)
				return nil
			}
			if err := ts.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "untrusted", path)
			return nil
		},
	}
}
