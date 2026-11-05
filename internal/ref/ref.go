// Package ref parses and validates identity references (provider@alias).
package ref

import (
	"fmt"
	"regexp"
	"strings"
)

// Ref names one captured identity.
type Ref struct {
	Provider string
	Alias    string
}

func (r Ref) String() string { return r.Provider + "@" + r.Alias }

var (
	providerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9._-]*)?$`)
	aliasRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// Reserved lists subcommand names; no provider may use them.
var Reserved = map[string]bool{
	"add": true, "import": true, "ls": true, "rm": true, "run": true,
	"profile": true, "which": true, "trust": true, "untrust": true,
	"shims": true, "agent": true, "unlock": true, "lock": true,
	"doctor": true, "providers": true, "version": true, "help": true,
	"completion": true, "keystore-helper": true,
}

// ValidProvider checks a provider name ("gh", "mcp/posthog").
func ValidProvider(p string) error {
	if !providerRe.MatchString(p) {
		return fmt.Errorf("invalid provider name %q", p)
	}
	if Reserved[p] {
		return fmt.Errorf("provider name %q is reserved", p)
	}
	return nil
}

// ValidAlias checks an alias or profile name.
func ValidAlias(a string) error {
	if !aliasRe.MatchString(a) {
		return fmt.Errorf("invalid name %q (use a-z 0-9 . _ -, max 64)", a)
	}
	return nil
}

// Split parses "provider" or "provider@alias"; alias is "" when absent.
func Split(s string) (provider, alias string, err error) {
	provider, alias, found := strings.Cut(s, "@")
	if err := ValidProvider(provider); err != nil {
		return "", "", err
	}
	if found {
		if err := ValidAlias(alias); err != nil {
			return "", "", err
		}
	}
	return provider, alias, nil
}

// Parse requires the full "provider@alias" form.
func Parse(s string) (Ref, error) {
	p, a, err := Split(s)
	if err != nil {
		return Ref{}, err
	}
	if a == "" {
		return Ref{}, fmt.Errorf("%q: expected provider@alias", s)
	}
	return Ref{Provider: p, Alias: a}, nil
}
