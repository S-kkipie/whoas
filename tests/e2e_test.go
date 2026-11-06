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

// TestMain builds the real whoas binary and the test helpers (fake CLI, fake
// agents, fake MCP server, MCP client) once per test run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "whoas-e2e-bin-")
	if err != nil {
		panic(err)
	}
	binDir = dir
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o700); err != nil {
		panic(err)
	}
	for _, b := range []struct{ out, pkg string }{
		{"whoas", "github.com/S-kkipie/whoas/cmd/whoas"},
		{"fakecli", "github.com/S-kkipie/whoas/tests/fakecli"},
		// the same fake CLI under other names, one per fake adapter (bins are unique)
		{"fakehome", "github.com/S-kkipie/whoas/tests/fakecli"},
		{"faketok", "github.com/S-kkipie/whoas/tests/fakecli"},
		{"fakedock", "github.com/S-kkipie/whoas/tests/fakecli"},
		{"fakeclaude", "github.com/S-kkipie/whoas/tests/fakecli"},
		// fake claude and codex agents, only on PATH where a script adds $FAKEAGENTS
		{filepath.Join("agents", "claude"), "github.com/S-kkipie/whoas/tests/fakecli"},
		{filepath.Join("agents", "codex"), "github.com/S-kkipie/whoas/tests/fakecli"},
		// a Streamable HTTP MCP server and an MCP client
		{"fakemcp", "github.com/S-kkipie/whoas/tests/fakemcp"},
		{"mcpcall", "github.com/S-kkipie/whoas/tests/mcpcall"},
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
			for name, body := range fakeAdapters {
				if err := os.WriteFile(filepath.Join(cfg, "adapters", name), []byte(body), 0o600); err != nil {
					return err
				}
			}
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("FAKEAGENTS", filepath.Join(binDir, "agents"))
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
