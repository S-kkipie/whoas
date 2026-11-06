// Package resolve decides which identity a provider runs as.
//
// The first context source that is present decides, with no fallthrough:
// explicit alias, then -p, then WHOAS_PROFILE, then a trusted .whoas.yaml
// (a per-provider override beats its profile), then the default profile.
// A source that is present but maps no identity for the provider yields
// ErrUnresolved; shims then pass through to the global CLI.
package resolve

import (
	"errors"
	"fmt"

	"github.com/S-kkipie/whoas/internal/config"
	"github.com/S-kkipie/whoas/internal/project"
	"github.com/S-kkipie/whoas/internal/ref"
)

// ErrUnresolved means no identity applies.
var ErrUnresolved = errors.New("no identity resolved")

// Input is the context for one resolution.
type Input struct {
	Provider    string
	Alias       string        // from provider@alias; "" when absent
	ProfileFlag string        // -p
	EnvProfile  string        // WHOAS_PROFILE
	Project     *project.File // trusted project file, or nil
	Config      *config.Config
}

// Result is the chosen identity and the rule that chose it.
type Result struct {
	Ref    ref.Ref
	Source string
}

// Resolve applies the precedence rules.
func Resolve(in Input) (Result, error) {
	pick := func(alias, source string) (Result, error) {
		return Result{Ref: ref.Ref{Provider: in.Provider, Alias: alias}, Source: source}, nil
	}
	fromProfile := func(name, source string) (Result, error) {
		bindings, ok := in.Config.Profile(name)
		if !ok {
			return Result{}, fmt.Errorf("unknown profile %q (from %s)", name, source)
		}
		alias, ok := bindings[in.Provider]
		if !ok {
			return Result{}, fmt.Errorf("%w: profile %q has no %s identity (from %s)", ErrUnresolved, name, in.Provider, source)
		}
		return pick(alias, fmt.Sprintf("%s (profile %s)", source, name))
	}
	switch {
	case in.Alias != "":
		return pick(in.Alias, "explicit")
	case in.ProfileFlag != "":
		return fromProfile(in.ProfileFlag, "-p flag")
	case in.EnvProfile != "":
		return fromProfile(in.EnvProfile, "WHOAS_PROFILE")
	case in.Project != nil:
		if alias, ok := in.Project.Overrides[in.Provider]; ok {
			return pick(alias, "project "+in.Project.Path)
		}
		if in.Project.Profile != "" {
			return fromProfile(in.Project.Profile, "project "+in.Project.Path)
		}
		return Result{}, fmt.Errorf("%w: %s maps no %s identity", ErrUnresolved, in.Project.Path, in.Provider)
	case in.Config.DefaultProfile != "":
		return fromProfile(in.Config.DefaultProfile, "default profile")
	}
	return Result{}, fmt.Errorf("%w for %s (use %s@alias, -p, .whoas.yaml or whoas profile default)", ErrUnresolved, in.Provider, in.Provider)
}
