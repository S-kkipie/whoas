package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/binpath"
	"github.com/S-kkipie/whoas/internal/config"
	"github.com/S-kkipie/whoas/internal/keysource"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/paths"
	"github.com/S-kkipie/whoas/internal/vault"
)

// env bundles what most commands load.
type env struct {
	dirs     paths.Dirs
	cfg      *config.Config
	registry *adapter.Registry
}

func loadEnv() (*env, error) {
	d := paths.Resolve()
	cfg, err := config.Load(d.ConfigFile())
	if err != nil {
		return nil, err
	}
	reg, err := adapter.LoadRegistry(d.AdaptersDir())
	if err != nil {
		return nil, err
	}
	return &env{dirs: d, cfg: cfg, registry: reg}, nil
}

func (e *env) vars() adapter.Vars {
	return adapter.Vars{Home: e.dirs.Home, XDGConfig: e.dirs.XDGConfig, XDGData: e.dirs.XDGData}
}

func (e *env) adapter(provider string) (*adapter.Adapter, error) {
	a, ok := e.registry.Get(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (see: whoas providers)", provider)
	}
	return a, nil
}

func (e *env) store() (keystore.Store, error) { return keystore.Select(e.dirs.Data, os.Getenv) }

func (e *env) openVault() (*vault.Vault, error) {
	return vault.Open(e.dirs.RecipientFile(), e.dirs.VaultDir())
}

func (e *env) initVault() (*vault.Vault, error) {
	st, err := e.store()
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(e.dirs.RecipientFile())
	created := errors.Is(statErr, fs.ErrNotExist)
	v, err := vault.Init(e.dirs.RecipientFile(), e.dirs.VaultDir(), st)
	if err != nil {
		return nil, err
	}
	// Only the store that created the vault is known to hold its master key;
	// an existing vault opened under a one-off override must not repoint it.
	if created {
		if err := keystore.Remember(e.dirs.Data, st); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// decrypter asks an unlocked agent first, so the hot path never selects (and
// possibly probes) a keystore while one is running.
func (e *env) decrypter() (vault.Decrypter, error) {
	c := agent.NewClient(e.dirs.AgentSocket())
	if locked, err := c.Status(); err == nil && !locked {
		return c, nil
	}
	st, err := e.store()
	if err != nil {
		return nil, err
	}
	return keysource.Decrypter(e.dirs.AgentSocket(), st, self())
}

// binIndex lists PATH once for resolving many CLIs (providers, shims).
func (e *env) binIndex() *binpath.Index {
	return binpath.NewIndex(os.Getenv("PATH"), e.dirs.ShimsDir(), self())
}

// realBin finds the real CLI, skipping shims and whoas itself.
func (e *env) realBin(name string) (string, error) {
	return binpath.Find(name, os.Getenv("PATH"), e.dirs.ShimsDir(), self())
}

func self() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
