// Package adapter loads declarative TOML adapters that tell whoas where a
// CLI keeps its session and how to point the CLI at another directory.
// Adapters are data only: they never contain code to execute.
package adapter

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/S-kkipie/whoas/internal/ref"
)

// Kind is the isolation mechanism.
type Kind string

// KindDirOverlay points the CLI at an overlay of its config dir.
const KindDirOverlay Kind = "dir-overlay"

// Adapter describes one CLI.
type Adapter struct {
	Name   string  `toml:"name"`
	Bin    string  `toml:"bin"`
	Kind   Kind    `toml:"kind"`
	Dir    DirSpec `toml:"dir"`
	Login  Login   `toml:"login"`
	Whoami Whoami  `toml:"whoami"`
}

// DirSpec says where the session lives and how to redirect the CLI.
type DirSpec struct {
	Global    string   `toml:"global"`     // template, e.g. "{xdg_config}/gh"
	Inject    Inject   `toml:"inject"`     // exactly one of env or flag
	AuthFiles []string `toml:"auth_files"` // slash paths relative to the dir; may be dirs
}

// Inject points the CLI at {dir} through env vars or leading flags.
type Inject struct {
	Env  map[string]string `toml:"env"`
	Flag []string          `toml:"flag"`
}

// Login is the command that runs the CLI's own login flow.
type Login struct {
	Cmd  []string `toml:"cmd"`
	Hint string   `toml:"hint"`
}

// Whoami prints the account; JSON is an optional gjson path into stdout.
type Whoami struct {
	Cmd  []string `toml:"cmd"`
	JSON string   `toml:"json"`
}

// Vars are the values templates expand.
type Vars struct {
	Home      string
	XDGConfig string
	XDGData   string
}

var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Parse decodes one adapter strictly (unknown keys are errors) and validates it.
func Parse(data []byte, source string) (*Adapter, error) {
	var a Adapter
	md, err := toml.Decode(string(data), &a)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", source, strings.Join(keys, ", "))
	}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &a, nil
}

// Validate checks every invariant the rest of whoas relies on.
func (a *Adapter) Validate() error {
	if err := ref.ValidProvider(a.Name); err != nil {
		return err
	}
	if a.Bin == "" || strings.ContainsAny(a.Bin, `/\`) {
		return fmt.Errorf("bin %q must be a bare command name", a.Bin)
	}
	if a.Kind != KindDirOverlay {
		return fmt.Errorf("kind %q not supported (supported: %s)", a.Kind, KindDirOverlay)
	}
	if a.Dir.Global == "" {
		return errors.New("dir.global is required")
	}
	if len(a.Dir.AuthFiles) == 0 {
		return errors.New("dir.auth_files is required")
	}
	for _, p := range a.Dir.AuthFiles {
		c := path.Clean(p)
		if c != p || path.IsAbs(p) || c == "." || strings.HasPrefix(c, "../") || c == ".." || strings.Contains(p, `\`) {
			return fmt.Errorf("auth file %q must be a clean relative slash path", p)
		}
	}
	env, flag := len(a.Dir.Inject.Env) > 0, len(a.Dir.Inject.Flag) > 0
	if env == flag {
		return errors.New("dir.inject needs exactly one of env or flag")
	}
	hasDir := false
	for k, v := range a.Dir.Inject.Env {
		if !envNameRe.MatchString(k) {
			return fmt.Errorf("inject env name %q invalid", k)
		}
		hasDir = hasDir || strings.Contains(v, "{dir}")
	}
	for _, f := range a.Dir.Inject.Flag {
		hasDir = hasDir || strings.Contains(f, "{dir}")
	}
	if !hasDir {
		return errors.New("dir.inject must reference {dir}")
	}
	if len(a.Login.Cmd) == 0 || a.Login.Cmd[0] != a.Bin {
		return fmt.Errorf("login.cmd must start with %q", a.Bin)
	}
	if len(a.Whoami.Cmd) > 0 && a.Whoami.Cmd[0] != a.Bin {
		return fmt.Errorf("whoami.cmd must start with %q", a.Bin)
	}
	return nil
}

// Expand replaces {home}, {xdg_config}, {xdg_data} and {dir}.
func Expand(s string, v Vars, dir string) string {
	return strings.NewReplacer("{home}", v.Home, "{xdg_config}", v.XDGConfig, "{xdg_data}", v.XDGData, "{dir}", dir).Replace(s)
}

// GlobalDir is the CLI's real config dir.
func (a *Adapter) GlobalDir(v Vars) string { return filepath.Clean(Expand(a.Dir.Global, v, "")) }

// InjectArgs are the flags to place right after the binary name.
func (a *Adapter) InjectArgs(dir string) []string {
	out := make([]string, len(a.Dir.Inject.Flag))
	for i, f := range a.Dir.Inject.Flag {
		out[i] = Expand(f, Vars{}, dir)
	}
	return out
}

// InjectEnv returns KEY=VALUE pairs, sorted by key.
func (a *Adapter) InjectEnv(dir string) []string {
	keys := make([]string, 0, len(a.Dir.Inject.Env))
	for k := range a.Dir.Inject.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + Expand(a.Dir.Inject.Env[k], Vars{}, dir)
	}
	return out
}

//go:embed builtin/*.toml
var builtinFS embed.FS

// Registry holds every known adapter by name.
type Registry struct{ byName map[string]*Adapter }

// LoadRegistry loads built-in adapters, then userDir/*.toml (user wins on name clash).
func LoadRegistry(userDir string) (*Registry, error) {
	r := &Registry{byName: map[string]*Adapter{}}
	builtins, err := fs.Glob(builtinFS, "builtin/*.toml")
	if err != nil {
		return nil, err
	}
	for _, p := range builtins {
		data, err := builtinFS.ReadFile(p)
		if err != nil {
			return nil, err
		}
		a, err := Parse(data, "builtin "+path.Base(p))
		if err != nil {
			return nil, err
		}
		r.byName[a.Name] = a
	}
	user, err := filepath.Glob(filepath.Join(userDir, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, p := range user {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		a, err := Parse(data, p)
		if err != nil {
			return nil, err
		}
		r.byName[a.Name] = a
	}
	return r, nil
}

// Get returns the adapter named name.
func (r *Registry) Get(name string) (*Adapter, bool) {
	a, ok := r.byName[name]
	return a, ok
}

// ByBin returns the adapter whose binary is bin.
func (r *Registry) ByBin(bin string) (*Adapter, bool) {
	for _, n := range r.Names() {
		if a := r.byName[n]; a.Bin == bin {
			return a, true
		}
	}
	return nil, false
}

// Names returns adapter names sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
