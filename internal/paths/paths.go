// Package paths resolves every directory whoas reads or writes.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/adrg/xdg"
)

// Dirs holds whoas's directories plus the XDG values adapters expand.
type Dirs struct {
	Config    string // ~/.config/whoas
	Data      string // ~/.local/share/whoas
	Runtime   string // $XDG_RUNTIME_DIR/whoas (tmpfs on Linux)
	Home      string
	XDGConfig string
	XDGData   string
}

// Resolve computes Dirs from the environment. WHOAS_CONFIG_DIR,
// WHOAS_DATA_DIR and WHOAS_RUNTIME_DIR override the defaults.
func Resolve() Dirs {
	xdg.Reload()
	d := Dirs{
		Config:    envOr("WHOAS_CONFIG_DIR", filepath.Join(xdg.ConfigHome, "whoas")),
		Data:      envOr("WHOAS_DATA_DIR", filepath.Join(xdg.DataHome, "whoas")),
		Runtime:   os.Getenv("WHOAS_RUNTIME_DIR"),
		Home:      xdg.Home,
		XDGConfig: xdg.ConfigHome,
		XDGData:   xdg.DataHome,
	}
	if d.Runtime == "" {
		d.Runtime = defaultRuntime()
	}
	return d
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func defaultRuntime() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(os.TempDir(), "whoas") // per-user $TMPDIR
	}
	if xdg.RuntimeDir != "" && writable(xdg.RuntimeDir) {
		return filepath.Join(xdg.RuntimeDir, "whoas")
	}
	return filepath.Join(xdg.CacheHome, "whoas", "run")
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".whoas-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func (d Dirs) ConfigFile() string    { return filepath.Join(d.Config, "config.yaml") }
func (d Dirs) AdaptersDir() string   { return filepath.Join(d.Config, "adapters") }
func (d Dirs) VaultDir() string      { return filepath.Join(d.Data, "vault") }
func (d Dirs) RecipientFile() string { return filepath.Join(d.Data, "recipient") }
func (d Dirs) TrustFile() string     { return filepath.Join(d.Data, "trust.json") }
func (d Dirs) ShimsDir() string      { return filepath.Join(d.Data, "shims") }
func (d Dirs) AgentSocket() string   { return filepath.Join(d.Runtime, "agent.sock") }

// IDRoot holds one overlay (plus .lock and .state.json) per active identity.
func (d Dirs) IDRoot() string { return filepath.Join(d.Runtime, "id") }

// RunBase is the overlay dir of provider@alias; ".lock" and ".state.json"
// sit next to it.
func (d Dirs) RunBase(provider, alias string) string {
	return filepath.Join(d.IDRoot(), filepath.FromSlash(provider), alias)
}

// CaptureDir is a throwaway overlay used while `whoas add` runs a login.
func (d Dirs) CaptureDir(provider, alias string) string {
	return filepath.Join(d.Runtime, "capture", filepath.FromSlash(provider), fmt.Sprintf("%s-%d", alias, os.Getpid()))
}

// EnsureRuntime creates the runtime dir and forces it to 0700.
func (d Dirs) EnsureRuntime() error {
	if err := os.MkdirAll(d.Runtime, 0o700); err != nil {
		return err
	}
	return os.Chmod(d.Runtime, 0o700)
}
