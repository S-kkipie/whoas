// Package config loads and saves ~/.config/whoas/config.yaml (no secrets).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/goccy/go-yaml"

	"github.com/S-kkipie/whoas/internal/fsutil"
	"github.com/S-kkipie/whoas/internal/ref"
)

// Config is the user's profile setup.
type Config struct {
	DefaultProfile string                       `yaml:"default_profile,omitempty"`
	Profiles       map[string]map[string]string `yaml:"profiles,omitempty"` // profile → provider → alias
}

// Load reads path; a missing file yields an empty config.
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.Profiles = map[string]map[string]string{}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.UnmarshalWithOptions(data, c, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]map[string]string{}
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	for name, bindings := range c.Profiles {
		if err := ref.ValidAlias(name); err != nil {
			return fmt.Errorf("profile: %w", err)
		}
		for provider, alias := range bindings {
			if err := ref.ValidProvider(provider); err != nil {
				return fmt.Errorf("profile %s: %w", name, err)
			}
			if err := ref.ValidAlias(alias); err != nil {
				return fmt.Errorf("profile %s: %w", name, err)
			}
		}
	}
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("default_profile %q does not exist", c.DefaultProfile)
		}
	}
	return nil
}

// Save writes the config atomically with mode 0600.
func (c *Config) Save(path string) error {
	if err := c.validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// EnsureProfile creates an empty profile if it does not exist.
func (c *Config) EnsureProfile(name string) {
	if c.Profiles[name] == nil {
		c.Profiles[name] = map[string]string{}
	}
}

// Bind maps r.Provider to r.Alias inside profile, creating it if needed.
func (c *Config) Bind(profile string, r ref.Ref) {
	c.EnsureProfile(profile)
	c.Profiles[profile][r.Provider] = r.Alias
}

// Profile returns the bindings of name.
func (c *Config) Profile(name string) (map[string]string, bool) {
	p, ok := c.Profiles[name]
	return p, ok
}

// ProfileNames returns profile names sorted.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
