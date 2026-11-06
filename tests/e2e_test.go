//go:build unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/rogpeppe/go-internal/testscript"
)

var (
	binDir    string
	masterKey string
)

// TestMain builds the real whoas binary and the fake CLI once per test run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "whoas-e2e-bin-")
	if err != nil {
		panic(err)
	}
	binDir = dir
	for _, b := range []struct{ out, pkg string }{
		{"whoas", "github.com/S-kkipie/whoas/cmd/whoas"},
		{"fakecli", "github.com/S-kkipie/whoas/tests/fakecli"},
	} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, b.out), b.pkg)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			panic(err)
		}
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	masterKey = id.String()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		Setup: func(env *testscript.Env) error {
			home := filepath.Join(env.WorkDir, "home")
			cfg := filepath.Join(env.WorkDir, "cfg")
			if err := os.MkdirAll(filepath.Join(cfg, "adapters"), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(cfg, "adapters", "fakecli.toml"), []byte(fakeAdapter), 0o600); err != nil {
				return err
			}
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("HOME", home)
			env.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			env.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			env.Setenv("WHOAS_CONFIG_DIR", cfg)
			env.Setenv("WHOAS_DATA_DIR", filepath.Join(env.WorkDir, "data"))
			// $WORK can be too deep for a unix socket path (macOS: 104 bytes),
			// so the runtime dir lives in a short temp dir; $WORK/run links to
			// it so scripts can keep using $WORK/run.
			rt, err := os.MkdirTemp("", "wr")
			if err != nil {
				return err
			}
			env.Defer(func() { os.RemoveAll(rt) })
			if err := os.Symlink(rt, filepath.Join(env.WorkDir, "run")); err != nil {
				return err
			}
			env.Setenv("WHOAS_RUNTIME_DIR", rt)
			env.Setenv("WHOAS_MASTER_KEY", masterKey)
			return os.MkdirAll(home, 0o700)
		},
		Cmds: customCmds,
	})
}
