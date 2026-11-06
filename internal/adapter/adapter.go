// Package adapter loads declarative TOML adapters that tell whoas where a
// CLI keeps its session and how to point the CLI at it (dir-overlay), or
// which env vars carry its API token (env-token).
// Adapters are data, not scripts, but they choose which binary runs with which arguments: only install adapters you trust, like any script in your config dir. Project files (.whoas.yaml) can never declare adapters.
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
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/S-kkipie/whoas/internal/ref"
)

// Kind is the isolation mechanism.
type Kind string

const (
	// KindDirOverlay points the CLI at an overlay of its config dir.
	KindDirOverlay Kind = "dir-overlay"
	// KindEnvToken injects API tokens as env vars; no overlay.
	KindEnvToken Kind = "env-token"
)

// Adapter describes one CLI.
type Adapter struct {
	Name   string  `toml:"name"`
	Bin    string  `toml:"bin"`
	Kind   Kind    `toml:"kind"`
	Dir    DirSpec `toml:"dir"`
	Login  Login   `toml:"login"`
	Whoami Whoami  `toml:"whoami"`
	// UnsetEnv lists env vars removed from the CLI's environment because they
	// would override the identity (e.g. GH_TOKEN for gh).
	UnsetEnv []string `toml:"unset_env"`
	// Experimental marks adapters whose isolation has known gaps.
	Experimental bool `toml:"experimental"`
	// Secrets are the env vars an env-token adapter injects.
	Secrets []Secret `toml:"secret"`
}

// Secret is one env var of an env-token adapter.
type Secret struct {
	Env      string `toml:"env"`
	Label    string `toml:"label"`
	Plain    bool   `toml:"plain"`    // not secret: prompt with echo (e.g. a host name)
	Optional bool   `toml:"optional"` // may be left empty
}

// DirSpec says where the session lives and how to redirect the CLI.
type DirSpec struct {
	Global    string   `toml:"global"`     // template, e.g. "{xdg_config}/gh"
	Inject    Inject   `toml:"inject"`     // exactly one of env or flag
	AuthFiles []string `toml:"auth_files"` // slash paths relative to the dir; may be dirs
	// HelperKeys are top-level JSON keys of an auth file that delegate
	// credentials to stores outside whoas (docker: credsStore, credHelpers).
	HelperKeys []string `toml:"helper_keys"`
	// Conflicts are paths (same placeholders as Global) that, when present,
	// make the CLI ignore the injected dir (wrangler: a legacy ~/.wrangler).
	Conflicts []string `toml:"conflicts"`
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

// envNameRe allows lowercase: some CLIs use host-specific names (TF_TOKEN_app_terraform_io).
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var binNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

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

// rootVars maps a root global to the env var that must point at the overlay.
var rootVars = map[string]string{"{home}": "HOME", "{xdg_config}": "XDG_CONFIG_HOME", "{xdg_data}": "XDG_DATA_HOME"}

// Validate checks every invariant the rest of whoas relies on.
func (a *Adapter) Validate() error {
	if err := ref.ValidProvider(a.Name); err != nil {
		return err
	}
	if !binNameRe.MatchString(a.Bin) {
		return fmt.Errorf("bin %q must be a bare command name", a.Bin)
	}
	if len(a.Whoami.Cmd) > 0 && a.Whoami.Cmd[0] != a.Bin {
		return fmt.Errorf("whoami.cmd must start with %q", a.Bin)
	}
	for _, k := range a.UnsetEnv {
		if !envNameRe.MatchString(k) {
			return fmt.Errorf("unset_env name %q invalid", k)
		}
	}
	switch a.Kind {
	case KindDirOverlay:
		return a.validateDirOverlay()
	case KindEnvToken:
		return a.validateEnvToken()
	}
	return fmt.Errorf("kind %q not supported (supported: %s, %s)", a.Kind, KindDirOverlay, KindEnvToken)
}

func (a *Adapter) validateDirOverlay() error {
	if len(a.Secrets) > 0 {
		return errors.New("[[secret]] is only valid for env-token adapters")
	}
	g := a.Dir.Global
	if g == "" {
		return errors.New("dir.global is required")
	}
	rootVar, isRoot := rootVars[g]
	if !isRoot && !strings.HasPrefix(g, "{home}/") && !strings.HasPrefix(g, "{xdg_config}/") && !strings.HasPrefix(g, "{xdg_data}/") {
		return errors.New("dir.global must start with {home}/, {xdg_config}/ or {xdg_data}/ (or be exactly {home}, {xdg_config} or {xdg_data})")
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
	if env {
		for k, v := range a.Dir.Inject.Env {
			if !envNameRe.MatchString(k) {
				return fmt.Errorf("inject env name %q invalid", k)
			}
			if !strings.Contains(v, "{dir}") {
				return fmt.Errorf("inject env %s must reference {dir}", k)
			}
		}
	} else {
		hasDir := false
		for _, f := range a.Dir.Inject.Flag {
			hasDir = hasDir || strings.Contains(f, "{dir}")
		}
		if !hasDir {
			return errors.New("dir.inject must reference {dir}")
		}
	}
	// A root overlay redirects exactly its own root variable; a root variable
	// may only be injected by the matching root overlay.
	if isRoot && a.Dir.Inject.Env[rootVar] != "{dir}" {
		return fmt.Errorf("dir.global %s requires inject env %s = \"{dir}\"", g, rootVar)
	}
	for root, v := range rootVars {
		if _, ok := a.Dir.Inject.Env[v]; ok && g != root {
			return fmt.Errorf("inject env %s is only allowed with dir.global = %s", v, root)
		}
	}
	for _, c := range a.Dir.Conflicts {
		if !strings.HasPrefix(c, "{home}/") && !strings.HasPrefix(c, "{xdg_config}/") && !strings.HasPrefix(c, "{xdg_data}/") {
			return fmt.Errorf("dir.conflicts %q must start with {home}/, {xdg_config}/ or {xdg_data}/", c)
		}
	}
	for _, k := range a.Dir.HelperKeys {
		if k == "" || strings.ContainsAny(k, ".*?|#@\\") {
			return fmt.Errorf("helper key %q must be a plain top-level JSON key", k)
		}
	}
	if len(a.Login.Cmd) == 0 || a.Login.Cmd[0] != a.Bin {
		return fmt.Errorf("login.cmd must start with %q", a.Bin)
	}
	return nil
}

func (a *Adapter) validateEnvToken() error {
	if a.Dir.Global != "" || len(a.Dir.AuthFiles) > 0 || len(a.Dir.Inject.Env) > 0 || len(a.Dir.Inject.Flag) > 0 || len(a.Dir.HelperKeys) > 0 || len(a.Dir.Conflicts) > 0 {
		return errors.New("[dir] is not valid for env-token adapters")
	}
	if len(a.Login.Cmd) > 0 {
		return errors.New("login.cmd is not valid for env-token adapters (use login.hint)")
	}
	if len(a.Secrets) == 0 {
		return errors.New("env-token adapters need at least one [[secret]]")
	}
	seen := map[string]bool{}
	for _, s := range a.Secrets {
		if !envNameRe.MatchString(s.Env) {
			return fmt.Errorf("secret env name %q invalid", s.Env)
		}
		if seen[s.Env] {
			return fmt.Errorf("secret env %s declared twice", s.Env)
		}
		seen[s.Env] = true
		if slices.Contains(a.UnsetEnv, s.Env) {
			return fmt.Errorf("secret env %s cannot also be in unset_env", s.Env)
		}
	}
	return nil
}

// Expand replaces {home}, {xdg_config}, {xdg_data} and {dir}.
func Expand(s string, v Vars, dir string) string {
	return strings.NewReplacer("{home}", v.Home, "{xdg_config}", v.XDGConfig, "{xdg_data}", v.XDGData, "{dir}", dir).Replace(s)
}

// CheckConflicts fails when one of the adapter's conflicting paths exists:
// the CLI would use it instead of the isolated directory.
func (a *Adapter) CheckConflicts(v Vars) error {
	for _, c := range a.Dir.Conflicts {
		p := filepath.Clean(Expand(c, v, ""))
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s: %s exists and %s uses it instead of the isolated directory; move it (see the adapter's hint) to use identities", a.Name, p, a.Bin)
		}
	}
	return nil
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

// ChildEnv is base without the adapter's UnsetEnv vars, plus InjectEnv(dir)
// (injected values win over inherited ones).
func (a *Adapter) ChildEnv(base []string, dir string) []string {
	out := make([]string, 0, len(base)+len(a.Dir.Inject.Env))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(a.UnsetEnv, k) {
			out = append(out, kv)
		}
	}
	return append(out, a.InjectEnv(dir)...)
}

// SecretEnv is base without the adapter's UnsetEnv vars and secret vars, plus
// each secret from values (ENV -> value) that is set.
func (a *Adapter) SecretEnv(base []string, values map[string]string) []string {
	drop := func(k string) bool {
		if slices.Contains(a.UnsetEnv, k) {
			return true
		}
		for _, s := range a.Secrets {
			if s.Env == k {
				return true
			}
		}
		return false
	}
	out := make([]string, 0, len(base)+len(a.Secrets))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop(k) {
			out = append(out, kv)
		}
	}
	for _, s := range a.Secrets {
		if v, ok := values[s.Env]; ok && v != "" {
			out = append(out, s.Env+"="+v)
		}
	}
	return out
}

// PromptLabel is the label shown when asking for s.
func (s Secret) PromptLabel() string {
	if s.Label == "" {
		return s.Env
	}
	return s.Label + " (" + s.Env + ")"
}

//go:embed builtin/*.toml
var builtinFS embed.FS

// Registry holds every known adapter by name.
type Registry struct {
	byName map[string]*Adapter
	source map[string]string // name -> "builtin" | "user"
}

// LoadRegistry loads built-in adapters, then userDir/*.toml (user wins on name clash).
func LoadRegistry(userDir string) (*Registry, error) {
	r := &Registry{byName: map[string]*Adapter{}, source: map[string]string{}}
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
		r.source[a.Name] = "builtin"
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
		r.source[a.Name] = "user"
	}
	// Check for duplicate bins
	binToName := make(map[string][]string)
	for name, adapter := range r.byName {
		binToName[adapter.Bin] = append(binToName[adapter.Bin], name)
	}
	for bin, names := range binToName {
		if len(names) > 1 {
			sort.Strings(names)
			return nil, fmt.Errorf("adapters %q and %q both use bin %q", names[0], names[1], bin)
		}
	}
	return r, nil
}

// Get returns the adapter named name.
func (r *Registry) Get(name string) (*Adapter, bool) {
	a, ok := r.byName[name]
	return a, ok
}

// Source says where the adapter named name came from: "builtin" or "user".
func (r *Registry) Source(name string) string { return r.source[name] }

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
