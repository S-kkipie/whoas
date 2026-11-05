package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() { register(newShimsCmd) }

func newShimsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "shims", Short: "Manage CLI shims (transparent identity for agents and scripts)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Create a shim for every adapter whose CLI is installed",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				target, err := filepath.EvalSymlinks(self())
				if err != nil {
					return err
				}
				dir := e.dirs.ShimsDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
				var installed []string
				for _, name := range e.registry.Names() {
					a, _ := e.registry.Get(name)
					if _, err := e.realBin(a.Bin); err != nil {
						continue // CLI not installed
					}
					link := filepath.Join(dir, a.Bin)
					if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
						if err := os.Remove(link); err != nil {
							return err
						}
					}
					if err := os.Symlink(target, link); err != nil {
						return err
					}
					installed = append(installed, a.Bin)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintln(out, "shims:", strings.Join(installed, ", "))
				if !inPath(dir) {
					fmt.Fprintf(out, "add to your shell rc:\n  export PATH=\"%s:$PATH\"\n", dir)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "uninstall",
			Short: "Remove all shims",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				links, err := shimLinks(e.dirs.ShimsDir())
				if err != nil {
					return err
				}
				for _, l := range links {
					if err := os.Remove(l); err != nil {
						return err
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed %d shims\n", len(links))
				return nil
			},
		},
		&cobra.Command{
			Use:   "ls",
			Short: "List installed shims",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				links, err := shimLinks(e.dirs.ShimsDir())
				if err != nil {
					return err
				}
				for _, l := range links {
					fmt.Fprintln(cmd.OutOrStdout(), filepath.Base(l))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dir %s (in PATH: %t)\n", e.dirs.ShimsDir(), inPath(e.dirs.ShimsDir()))
				return nil
			},
		},
	)
	return cmd
}

// shimLinks returns the symlinks in dir.
func shimLinks(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, en := range entries {
		if en.Type()&os.ModeSymlink != 0 {
			out = append(out, filepath.Join(dir, en.Name()))
		}
	}
	return out, nil
}

func inPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
