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
				ix := e.binIndex()
				for _, name := range e.registry.Names() {
					a, _ := e.registry.Get(name)
					if _, err := ix.Find(a.Bin); err != nil {
						continue // CLI not installed
					}
					link := filepath.Join(dir, a.Bin)
					if _, err := os.Lstat(link); err == nil {
						if isShim(link) {
							if err := os.Remove(link); err != nil {
								return err
							}
						} else {
							fmt.Fprintf(cmd.ErrOrStderr(), "whoas: skipping %s: %s exists and is not a whoas shim\n", a.Bin, link)
							continue
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

// isShim returns true if path is a symlink to whoas or whoas.exe.
func isShim(path string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	base := filepath.Base(target)
	return base == "whoas" || base == "whoas.exe"
}

// shimLinks returns the symlinks in dir that point to whoas.
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
			fullPath := filepath.Join(dir, en.Name())
			if isShim(fullPath) {
				out = append(out, fullPath)
			}
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
