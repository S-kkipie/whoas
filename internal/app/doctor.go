package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/supervisor"
	"github.com/S-kkipie/whoas/internal/vault"
)

func init() {
	register(newDoctorCmd)
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check setup and clean up overlays left by crashed runs (never prints secrets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(tw, "config\t%s\n", e.dirs.Config)
			fmt.Fprintf(tw, "data\t%s\n", e.dirs.Data)
			fmt.Fprintf(tw, "runtime\t%s\n", e.dirs.Runtime)
			if st, err := e.store(); err != nil {
				fmt.Fprintf(tw, "keystore\terror: %v\n", err)
			} else {
				fmt.Fprintf(tw, "keystore\t%s\n", st.Name())
			}
			v, verr := e.openVault()
			switch {
			case errors.Is(verr, vault.ErrNotInitialized):
				fmt.Fprintln(tw, "vault\tnot initialized")
			case verr != nil:
				fmt.Fprintf(tw, "vault\terror: %v\n", verr)
			default:
				fmt.Fprintln(tw, "vault\tinitialized")
			}
			if locked, err := agent.NewClient(e.dirs.AgentSocket()).Status(); err != nil {
				fmt.Fprintln(tw, "agent\tnot running")
			} else if locked {
				fmt.Fprintln(tw, "agent\trunning (locked)")
			} else {
				fmt.Fprintln(tw, "agent\trunning (unlocked)")
			}
			fmt.Fprintf(tw, "shims\t%s (in PATH: %t)\n", e.dirs.ShimsDir(), inPath(e.dirs.ShimsDir()))
			printClaudeWarnings(tw, e.dirs.Home, os.Getenv("CLAUDE_CONFIG_DIR"))
			if n, err := sweepCaptures(filepath.Join(e.dirs.Runtime, "capture")); err != nil {
				fmt.Fprintf(tw, "captures\terror: %v\n", err)
			} else {
				fmt.Fprintf(tw, "captures\tremoved %d stale capture dir(s)\n", n)
			}
			if verr == nil {
				n, err := recoverAll(e, v)
				if err != nil {
					fmt.Fprintf(tw, "recovery\terror: %v\n", err)
				} else {
					fmt.Fprintf(tw, "recovered\t%d stale overlay(s)\n", n)
				}
			}
			return tw.Flush()
		},
	}
}

// printClaudeWarnings reports what Claude Code's settings files set that
// outranks or overrides a claude identity's environment: an apiKeyHelper,
// and env entries for the variables whoas removes or injects. It prints
// names only, never a helper's command or an env value.
func printClaudeWarnings(w io.Writer, home, configDir string) {
	for _, p := range claudeKeyHelpers(home, configDir) {
		fmt.Fprintf(w, "claude\twarning: %s sets apiKeyHelper, which outranks the token of every claude identity and cannot be unset from the environment\n", p)
	}
	for _, o := range claudeEnvOverrides(home, configDir) {
		fmt.Fprintf(w, "claude\twarning: %s sets env %s; Claude Code applies it over the identity's environment\n", o.Path, strings.Join(o.Keys, ", "))
	}
}

// claudeSettingsFile is a Claude Code settings file that holds valid JSON.
type claudeSettingsFile struct {
	path string
	data []byte
}

// claudeSettings reads settings.json in configDir (the value of
// $CLAUDE_CONFIG_DIR) and in ~/.claude, each once. Missing or unreadable
// files and files that are not valid JSON are skipped.
func claudeSettings(home, configDir string) []claudeSettingsFile {
	dirs := []string{filepath.Join(home, ".claude")}
	if configDir != "" {
		dirs = []string{configDir, dirs[0]}
	}
	var out []claudeSettingsFile
	seen := map[string]bool{}
	for _, d := range dirs {
		p := filepath.Join(d, "settings.json")
		if seen[p] {
			continue
		}
		seen[p] = true
		data, err := os.ReadFile(p)
		if err != nil || !gjson.ValidBytes(data) {
			continue
		}
		out = append(out, claudeSettingsFile{path: p, data: data})
	}
	return out
}

// claudeKeyHelpers returns the Claude Code settings files that set
// apiKeyHelper. The helper's command is never printed.
func claudeKeyHelpers(home, configDir string) []string {
	var out []string
	for _, f := range claudeSettings(home, configDir) {
		if v := gjson.GetBytes(f.data, "apiKeyHelper"); v.Exists() && v.String() != "" {
			out = append(out, f.path)
		}
	}
	return out
}

// claudeEnvOverride is a settings file whose env block sets variables that a
// claude identity removes or injects. Claude Code writes that block into its
// own environment over the inherited values, so it brings them back.
type claudeEnvOverride struct {
	Path string
	Keys []string // names only, in file order
}

// claudeEnvOverrides returns the Claude Code settings files whose env object
// sets ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, CLAUDE_CODE_OAUTH_TOKEN or a
// CLAUDE_CODE_USE_* provider switch.
func claudeEnvOverrides(home, configDir string) []claudeEnvOverride {
	var out []claudeEnvOverride
	for _, f := range claudeSettings(home, configDir) {
		env := gjson.GetBytes(f.data, "env")
		if !env.IsObject() {
			continue
		}
		var keys []string
		env.ForEach(func(k, _ gjson.Result) bool {
			if name := k.String(); overridesClaudeIdentity(name) && !slices.Contains(keys, name) {
				keys = append(keys, name)
			}
			return true
		})
		if len(keys) > 0 {
			out = append(out, claudeEnvOverride{Path: f.path, Keys: keys})
		}
	}
	return out
}

func overridesClaudeIdentity(name string) bool {
	switch name {
	case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN":
		return true
	}
	return strings.HasPrefix(name, "CLAUDE_CODE_USE_")
}

// recoverAll seals and removes overlays whose holders all died.
func recoverAll(e *env, v *vault.Vault) (int, error) {
	root := e.dirs.IDRoot()
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".state.json") {
			return err
		}
		base := strings.TrimSuffix(path, ".state.json")
		rel, err := filepath.Rel(root, base)
		if err != nil {
			return err
		}
		r := ref.Ref{Provider: filepath.ToSlash(filepath.Dir(rel)), Alias: filepath.Base(rel)}
		a, ok := e.registry.Get(r.Provider)
		if !ok || a.Kind != adapter.KindDirOverlay {
			return nil // env-token runs have no overlay to recover
		}
		found, err := supervisor.Recover(context.Background(), base, a.GlobalDir(e.vars()), a, r, v)
		if found {
			n++
		}
		return err
	})
	return n, err
}

var captureNameRe = regexp.MustCompile(`^(.+)-([0-9]+)$`)

// sweepCaptures removes capture overlays (<root>/<provider>/<alias>-<pid>)
// left behind by add/import runs whose process died.
func sweepCaptures(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.SkipAll
		}
		if err != nil || !d.IsDir() || path == root {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		m := captureNameRe.FindStringSubmatch(d.Name())
		if m == nil || ref.ValidProvider(filepath.ToSlash(rel)) != nil || ref.ValidAlias(m[1]) != nil {
			return nil
		}
		pid, err := strconv.Atoi(m[2])
		if err != nil || runlock.Alive(pid) {
			return fs.SkipDir
		}
		if err := overlay.Remove(path); err != nil {
			return err
		}
		n++
		return fs.SkipDir
	})
	return n, err
}
