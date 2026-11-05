# whoas M1 (core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship whoas M1: capture OAuth sessions of gh, gcloud, codex and neonctl into an age vault and run each CLI as a chosen identity — explicitly, by profile, by trusted project file, or transparently through shims — without touching the CLI's global session.

**Architecture:** A Go CLI. Sessions are age-encrypted per identity (public key seals, private key from the OS keychain / WSL bridge / passphrase, cached by an optional agent). Each run joins or materializes a per-identity overlay of the CLI's config dir (symlinks to the real dir, auth files from the vault) under an flock-guarded holder list, spawns the real binary pointed at the overlay, re-seals auth files as they change, and removes the overlay when the last holder exits. Adapters are declarative TOML.

**Tech Stack:** Go 1.25+, cobra, filippo.io/age, zalando/go-keyring, gofrs/flock, fsnotify, adrg/xdg, awnumar/memguard, goccy/go-yaml, BurntSushi/toml, tidwall/gjson, golang.org/x/{sys,term}; tests: testify + rogpeppe/go-internal/testscript.

**Spec:** `docs/superpowers/specs/2026-11-05-whoas-design.md` (this plan implements milestone M1; M0 spikes, M2 and M3 get their own plans).

## Global Constraints

- Module path `github.com/S-kkipie/whoas`; `go 1.25.0` directive; CGO disabled for releases.
- Dependency versions are pinned exactly as in each task's `go get` (final `go.mod` at the end of this plan).
- Tests: unit tests are `*_test.go` next to the code; e2e tests live in `tests/` (testscript `.txtar`). Never use `__tests__/` (the Go tool ignores `_`-prefixed dirs).
- Unit tests that use symlinks, signals or unix sockets carry `//go:build unix`; native Windows only needs to **build** (it ships `whoas.exe keystore-helper` for WSL).
- Never print a secret: no token or `AGE-SECRET-KEY` in any whoas stdout/stderr; tokens are injected only via env vars/paths, never argv.
- Permissions: dirs 0700; config, trust, vault blobs, meta, state 0600; `recipient` 0644 (public key).
- Global CLI **auth files** are never written. New non-auth entries a CLI creates in an overlay are promoted to the global dir when absent there (shared state stays shared).
- Resolution: the first context source present decides, no fallthrough — `provider@alias` → `-p` → `WHOAS_PROFILE` → trusted `.whoas.yaml` (override beats its profile) → default profile.
- Signals: SIGTERM/SIGHUP always forwarded; SIGINT/SIGQUIT forwarded unless whoas is in a terminal's foreground process group.
- Exit codes: CLI's own code passes through (signal deaths are re-raised); whoas failures on the run path = 125, exec failure = 126, CLI not found = 127; subcommand errors = 1.
- Built-in adapters in M1: gh, gcloud, codex, neonctl (docker deferred to M2: Docker Desktop's `credsStore` keeps creds outside the config dir).
- Every commit message ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Prerequisites

- [ ] Go ≥ 1.25 on PATH (`go version`). If missing: download from https://go.dev/dl/ and add `<goroot>/bin` to PATH.
- [ ] Work in `/home/skkippie/work/AI-DO/whoas` (git repo already holds the spec). Run every command from the repo root.

## File Structure

```text
cmd/whoas/main.go                 entry point → app.Main
internal/fsutil/                  atomic file writes
internal/paths/                   config/data/runtime dirs (XDG + WHOAS_*_DIR overrides)
internal/ref/                     provider@alias parsing, reserved names
internal/config/                  config.yaml: profiles, default profile
internal/keystore/                master-key backends: env, OS keyring, WSL bridge, passphrase
internal/envelope/                plaintext identity format + stable hash
internal/vault/                   age seal/open per identity, metadata, listing
internal/adapter/                 TOML adapter schema, registry, built-in adapters
internal/overlay/                 build/collect/promote/remove per-identity overlays
internal/runlock/                 flock + holder state per identity
internal/agent/                   key agent (unix socket, peer check, memguard)
internal/keysource/               agent-or-direct decrypter selection
internal/supervisor/              join → spawn → seal-on-write → leave; crash recovery
internal/project/                 .whoas.yaml find + whitelist parse
internal/trust/                   trusted project files by hash
internal/resolve/                 precedence rules
internal/binpath/                 real-binary lookup skipping shims/self
internal/app/                     cobra commands, dispatch, shims
tests/                            e2e: harness, fakecli, testdata/*.txtar
```

---

### Task 1: Scaffold, version command, e2e harness

**Files:**
- Create: `cmd/whoas/main.go`
- Create: `internal/app/app.go`
- Create: `internal/app/version.go`
- Create: `.gitignore`
- Test: `tests/e2e_test.go`
- Test: `tests/cmds_test.go`
- Test: `tests/fakecli/main.go`
- Test: `tests/testdata/version.txtar`

**Interfaces:**
- Consumes: —
- Produces: `app.Main(args []string) int` (entry point; rewritten in Task 14); `register(func() *cobra.Command)` — every command file calls it from `init()`; `exitError{code int; err error}` — return from a RunE to set the exit code (nil err = already printed); `Version` string. E2E harness: every `tests/testdata/*.txtar` runs with the real `whoas` and `fakecli` binaries on PATH, `HOME=$WORK/home`, `WHOAS_CONFIG_DIR=$WORK/cfg`, `WHOAS_DATA_DIR=$WORK/data`, `WHOAS_RUNTIME_DIR=$WORK/run`, `WHOAS_MASTER_KEY` set, and a user adapter `$WORK/cfg/adapters/fakecli.toml`. Custom script commands: `sleep DURATION`, `expect-exit N cmd...`, `snapshot FILE KEY`, `unchanged FILE KEY`.

- [ ] **Step 1: Initialize the module and add dependencies**

```bash
go mod init github.com/S-kkipie/whoas
go mod edit -go=1.25.0
go get github.com/spf13/cobra@v1.10.2 github.com/rogpeppe/go-internal@v1.16.0 filippo.io/age@v1.3.2
```

- [ ] **Step 2: Write the failing tests**

**`tests/e2e_test.go`**

```go
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
			env.Setenv("WHOAS_RUNTIME_DIR", filepath.Join(env.WorkDir, "run"))
			env.Setenv("WHOAS_MASTER_KEY", masterKey)
			return os.MkdirAll(home, 0o700)
		},
		Cmds: customCmds,
	})
}
```

**`tests/cmds_test.go`**

```go
//go:build unix

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

// fakeAdapter is installed as a user adapter in every script.
const fakeAdapter = `name = "fakecli"
bin  = "fakecli"
kind = "dir-overlay"

[dir]
global     = "{home}/.fakecli"
inject     = { env = { FAKECLI_CONFIG_DIR = "{dir}" } }
auth_files = ["auth.json"]

[login]
cmd  = ["fakecli", "login"]
hint = "fake hint"

[whoami]
cmd = ["fakecli", "whoami"]
`

var (
	snapMu    sync.Mutex
	snapshots = map[string]string{}
)

func fileHash(ts *testscript.TestScript, path string) string {
	data, err := os.ReadFile(ts.MkAbs(path))
	ts.Check(err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var customCmds = map[string]func(ts *testscript.TestScript, neg bool, args []string){
	// sleep DURATION — pause the script (e.g. 500ms, 1s).
	"sleep": func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) != 1 {
			ts.Fatalf("usage: sleep DURATION")
		}
		d, err := time.ParseDuration(args[0])
		ts.Check(err)
		time.Sleep(d)
	},
	// expect-exit N cmd args... — run cmd and require exit code N.
	"expect-exit": func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) < 2 {
			ts.Fatalf("usage: expect-exit N cmd args...")
		}
		want, err := strconv.Atoi(args[0])
		ts.Check(err)
		got := 0
		if err := ts.Exec(args[1], args[2:]...); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				ts.Fatalf("%v", err)
			}
			got = ee.ExitCode()
		}
		if got != want {
			ts.Fatalf("exit code %d, want %d", got, want)
		}
	},
	// snapshot FILE KEY — remember the sha256 of FILE under KEY.
	"snapshot": func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) != 2 {
			ts.Fatalf("usage: snapshot FILE KEY")
		}
		snapMu.Lock()
		defer snapMu.Unlock()
		snapshots[ts.Getenv("WORK")+"/"+args[1]] = fileHash(ts, args[0])
	},
	// unchanged FILE KEY — require FILE to still match the snapshot KEY.
	"unchanged": func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) != 2 {
			ts.Fatalf("usage: unchanged FILE KEY")
		}
		snapMu.Lock()
		want, ok := snapshots[ts.Getenv("WORK")+"/"+args[1]]
		snapMu.Unlock()
		if !ok {
			ts.Fatalf("no snapshot %q", args[1])
		}
		if got := fileHash(ts, args[0]); got != want {
			ts.Fatalf("%s changed", args[0])
		}
	},
}
```

**`tests/fakecli/main.go`**

```go
// Command fakecli imitates a CLI that keeps an OAuth session in a config dir.
// It is used only by the e2e tests.
//
// Config dir: $FAKECLI_CONFIG_DIR, else $HOME/.fakecli.
// Arguments are a sequence of operations, run left to right:
//
//	login          write auth.json for account $FAKECLI_LOGIN_AS (default "default")
//	whoami         print "account=<name>"; exit 3 when not logged in
//	token          print "token=<token>"
//	refresh        rotate the token (gen+1) via write-to-temp + rename
//	dir            print "dir=<config dir>"
//	setting K V    write settings/K.txt = V (non-auth shared state)
//	sleep MS       sleep MS milliseconds
//	trap           wait for SIGTERM or SIGINT, print "trapped=TERM|INT", exit 42
//	exit N         exit with code N
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type auth struct {
	Account string `json:"account"`
	Gen     int    `json:"gen"`
	Token   string `json:"token"`
}

func configDir() string {
	if d := os.Getenv("FAKECLI_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".fakecli")
}

func authPath() string { return filepath.Join(configDir(), "auth.json") }

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakecli: "+format+"\n", args...)
	os.Exit(2)
}

func readAuth() (auth, bool) {
	var a auth
	data, err := os.ReadFile(authPath())
	if err != nil {
		return a, false
	}
	if err := json.Unmarshal(data, &a); err != nil {
		fail("corrupt auth.json: %v", err)
	}
	return a, true
}

func mustAuth() auth {
	a, ok := readAuth()
	if !ok {
		fmt.Fprintln(os.Stderr, "fakecli: not logged in")
		os.Exit(3)
	}
	return a
}

func writeAuth(a auth) {
	a.Token = fmt.Sprintf("tok-%s-%d", a.Account, a.Gen)
	data, err := json.Marshal(a)
	if err != nil {
		fail("%v", err)
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		fail("%v", err)
	}
	tmp := authPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		fail("%v", err)
	}
	if err := os.Rename(tmp, authPath()); err != nil {
		fail("%v", err)
	}
}

func intArg(args []string, op string) (int, []string) {
	if len(args) < 1 {
		fail("%s needs a number", op)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		fail("%s: %v", op, err)
	}
	return n, args[1:]
}

func main() {
	args := os.Args[1:]
	for len(args) > 0 {
		op := args[0]
		args = args[1:]
		switch op {
		case "login":
			name := os.Getenv("FAKECLI_LOGIN_AS")
			if name == "" {
				name = "default"
			}
			writeAuth(auth{Account: name})
		case "whoami":
			fmt.Printf("account=%s\n", mustAuth().Account)
		case "token":
			fmt.Printf("token=%s\n", mustAuth().Token)
		case "refresh":
			a := mustAuth()
			a.Gen++
			writeAuth(a)
		case "dir":
			fmt.Printf("dir=%s\n", configDir())
		case "setting":
			if len(args) < 2 {
				fail("setting needs K V")
			}
			p := filepath.Join(configDir(), "settings", args[0]+".txt")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				fail("%v", err)
			}
			if err := os.WriteFile(p, []byte(args[1]), 0o600); err != nil {
				fail("%v", err)
			}
			args = args[2:]
		case "sleep":
			var ms int
			ms, args = intArg(args, op)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		case "trap":
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
			if <-ch == syscall.SIGINT {
				fmt.Println("trapped=INT")
			} else {
				fmt.Println("trapped=TERM")
			}
			os.Exit(42)
		case "exit":
			var n int
			n, _ = intArg(args, op)
			os.Exit(n)
		default:
			fail("unknown op %q", op)
		}
	}
}
```

**`tests/testdata/version.txtar`**

```text
# whoas version prints the build version.
exec whoas version
stdout '^whoas dev$'
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./tests/
```

Expected: FAIL — TestMain cannot `go build ./cmd/whoas` (it does not exist yet) and panics before any script runs.

- [ ] **Step 4: Write the implementation**

**`cmd/whoas/main.go`**

```go
package main

import (
	"os"

	"github.com/S-kkipie/whoas/internal/app"
)

func main() { os.Exit(app.Main(os.Args)) }
```

**`internal/app/app.go`**

```go
// Package app wires whoas subcommands and process-level dispatch.
package app

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time:
// -ldflags "-X github.com/S-kkipie/whoas/internal/app.Version=v1.2.3".
var Version = "dev"

var factories []func() *cobra.Command

// register adds a subcommand constructor; each command file calls it from init.
func register(f func() *cobra.Command) { factories = append(factories, f) }

// exitError carries a specific exit code out of a cobra RunE.
// A nil err means the message was already printed.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "whoas",
		Short:         "Run CLIs as a chosen identity",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	for _, f := range factories {
		root.AddCommand(f())
	}
	return root
}

// runCobra executes subcommands and maps errors to exit codes.
func runCobra(args []string) int {
	root := newRoot()
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(os.Stderr, "whoas:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "whoas:", err)
	return 1
}

// Main is the process entry point and returns the exit code.
// Task 14 replaces this with provider/shim dispatch.
func Main(args []string) int {
	return runCobra(args[1:])
}
```

**`internal/app/version.go`**

```go
package app

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() { register(newVersionCmd) }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the whoas version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "whoas", Version)
			return nil
		},
	}
}
```

**`.gitignore`**

```text
/dist/
/whoas
/whoas.exe
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add .gitignore cmd/whoas/main.go go.mod go.sum internal/app/app.go internal/app/version.go tests/cmds_test.go tests/e2e_test.go tests/fakecli/main.go tests/testdata/version.txtar
git commit -m "chore: scaffold whoas CLI with e2e harness" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Filesystem helper and directory layout

**Files:**
- Create: `internal/fsutil/fsutil.go`
- Create: `internal/paths/paths.go`
- Test: `internal/fsutil/fsutil_test.go`
- Test: `internal/paths/paths_test.go`

**Interfaces:**
- Consumes: —
- Produces: `fsutil.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`; `paths.Resolve() paths.Dirs` with fields `Config, Data, Runtime, Home, XDGConfig, XDGData string` and methods `ConfigFile() AdaptersDir() VaultDir() RecipientFile() TrustFile() ShimsDir() AgentSocket() IDRoot() string`, `RunBase(provider, alias string) string`, `CaptureDir(provider, alias string) string`, `EnsureRuntime() error`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/adrg/xdg@v0.5.3 github.com/stretchr/testify@v1.12.1
```

- [ ] **Step 2: Write the failing tests**

**`internal/fsutil/fsutil_test.go`**

```go
//go:build unix

package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "f.json")
	require.NoError(t, WriteFileAtomic(p, []byte("one"), 0o600))
	require.NoError(t, WriteFileAtomic(p, []byte("two"), 0o600))

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "two", string(data))

	info, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	require.Len(t, entries, 1, "temp files must not be left behind")
}
```

**`internal/paths/paths_test.go`**

```go
//go:build unix

package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WHOAS_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("WHOAS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("WHOAS_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xc"))

	d := Resolve()
	require.Equal(t, filepath.Join(root, "cfg"), d.Config)
	require.Equal(t, filepath.Join(root, "data"), d.Data)
	require.Equal(t, filepath.Join(root, "run"), d.Runtime)
	require.Equal(t, filepath.Join(root, "xc"), d.XDGConfig)
	require.Equal(t, filepath.Join(root, "cfg", "config.yaml"), d.ConfigFile())
	require.Equal(t, filepath.Join(root, "data", "vault"), d.VaultDir())
	require.Equal(t, filepath.Join(root, "run", "id", "mcp", "posthog", "acme"), d.RunBase("mcp/posthog", "acme"))
}

func TestEnsureRuntime(t *testing.T) {
	d := Dirs{Runtime: filepath.Join(t.TempDir(), "run")}
	require.NoError(t, os.MkdirAll(d.Runtime, 0o755))
	require.NoError(t, d.EnsureRuntime())
	info, err := os.Stat(d.Runtime)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/fsutil/ ./internal/paths/
```

Expected: FAIL — `undefined: WriteFileAtomic`, `undefined: Resolve`.

- [ ] **Step 4: Write the implementation**

**`internal/fsutil/fsutil.go`**

```go
// Package fsutil holds small filesystem helpers shared across whoas.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to a temp file in the same directory, then
// renames it over path, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

**`internal/paths/paths.go`**

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/fsutil/ ./internal/paths/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/fsutil/fsutil.go internal/fsutil/fsutil_test.go internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat: add atomic writes and directory layout" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Identity references (provider@alias)

**Files:**
- Create: `internal/ref/ref.go`
- Test: `internal/ref/ref_test.go`

**Interfaces:**
- Consumes: —
- Produces: `ref.Ref{Provider, Alias string}` with `String() string`; `ref.Split(s string) (provider, alias string, err error)`; `ref.Parse(s string) (ref.Ref, error)` (requires `@alias`); `ref.ValidProvider(string) error`; `ref.ValidAlias(string) error` (also used for profile names); `ref.Reserved map[string]bool` (subcommand names).

- [ ] **Step 1: Write the failing tests**

**`internal/ref/ref_test.go`**

```go
package ref

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in, provider, alias string
		ok                  bool
	}{
		{"gh", "gh", "", true},
		{"vercel@work", "vercel", "work", true},
		{"mcp/posthog@acme", "mcp/posthog", "acme", true},
		{"gh@", "", "", false},
		{"GH@work", "", "", false},
		{"run", "", "", false},
		{"add@x", "", "", false},
		{"../etc@x", "", "", false},
		{"gh@../x", "", "", false},
		{"__complete", "", "", false},
	}
	for _, c := range cases {
		p, a, err := Split(c.in)
		if !c.ok {
			require.Error(t, err, c.in)
			continue
		}
		require.NoError(t, err, c.in)
		require.Equal(t, c.provider, p, c.in)
		require.Equal(t, c.alias, a, c.in)
	}
}

func TestParse(t *testing.T) {
	r, err := Parse("gcloud@acme")
	require.NoError(t, err)
	require.Equal(t, Ref{Provider: "gcloud", Alias: "acme"}, r)
	require.Equal(t, "gcloud@acme", r.String())

	_, err = Parse("gcloud")
	require.ErrorContains(t, err, "expected provider@alias")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/ref/
```

Expected: FAIL — `undefined: Split`, `undefined: Parse`.

- [ ] **Step 3: Write the implementation**

**`internal/ref/ref.go`**

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/ref/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/ref/ref.go internal/ref/ref_test.go
git commit -m "feat: parse and validate provider@alias references" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: User config (profiles)

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `ref.ValidAlias`, `ref.ValidProvider`, `ref.Ref` (Task 3); `fsutil.WriteFileAtomic` (Task 2).
- Produces: `config.Load(path string) (*config.Config, error)`; `Config{DefaultProfile string; Profiles map[string]map[string]string}`; methods `Save(path string) error`, `EnsureProfile(name string)`, `Bind(profile string, r ref.Ref)`, `Profile(name string) (map[string]string, bool)`, `ProfileNames() []string`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/goccy/go-yaml@v1.19.2
```

- [ ] **Step 2: Write the failing tests**

**`internal/config/config_test.go`**

```go
//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/ref"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	require.Empty(t, c.Profiles)
	require.Empty(t, c.DefaultProfile)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	c, err := Load(p)
	require.NoError(t, err)
	c.Bind("work", ref.Ref{Provider: "vercel", Alias: "work"})
	c.Bind("work", ref.Ref{Provider: "mcp/posthog", Alias: "acme"})
	c.EnsureProfile("empty")
	c.DefaultProfile = "work"
	require.NoError(t, c.Save(p))

	info, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "work", got.DefaultProfile)
	prof, ok := got.Profile("work")
	require.True(t, ok)
	require.Equal(t, map[string]string{"vercel": "work", "mcp/posthog": "acme"}, prof)
	require.Equal(t, []string{"empty", "work"}, got.ProfileNames())
}

func TestLoadRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown key":     "colour: blue\n",
		"bad provider":    "profiles:\n  work:\n    Vercel: x\n",
		"bad alias":       "profiles:\n  work:\n    vercel: ../x\n",
		"missing default": "default_profile: nope\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		_, err := Load(p)
		require.Error(t, err, name)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/config/
```

Expected: FAIL — `undefined: Load`.

- [ ] **Step 4: Write the implementation**

**`internal/config/config.go`**

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/config/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config/config.go internal/config/config_test.go
git commit -m "feat: load and save profiles config" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Keystore backends for the master key

**Files:**
- Create: `internal/keystore/keystore.go`
- Create: `internal/keystore/env.go`
- Create: `internal/keystore/os.go`
- Create: `internal/keystore/passphrase.go`
- Create: `internal/keystore/prompt.go`
- Create: `internal/keystore/wsl.go`
- Test: `internal/keystore/keystore_test.go`
- Test: `internal/keystore/integration_test.go`

**Interfaces:**
- Consumes: `fsutil.WriteFileAtomic` (Task 2).
- Produces: `keystore.Store` interface `{Name() string; Get() (string, error); Set(secret string) error; Slow() bool; TTL() time.Duration}`; `keystore.ErrNotFound`; `keystore.Select(dataDir string, getenv func(string) string) (keystore.Store, error)` (order: `WHOAS_MASTER_KEY` → `WHOAS_KEYSTORE=os|wsl|passphrase` → WSL bridge → OS keyring → passphrase); `keystore.OS() keystore.Store`. Env vars: `WHOAS_MASTER_KEY`, `WHOAS_KEYSTORE`, `WHOAS_PASSPHRASE`, `WHOAS_WIN_EXE`. `integration_test.go` only builds with `-tags keyring_integration` (CI).

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/zalando/go-keyring@v0.2.8 golang.org/x/term@v0.45.0 filippo.io/age@v1.3.2
```

- [ ] **Step 2: Write the failing tests**

**`internal/keystore/keystore_test.go`**

```go
//go:build unix

package keystore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSelect(t *testing.T) {
	dir := t.TempDir()

	s, err := Select(dir, getenvFrom(map[string]string{"WHOAS_MASTER_KEY": "AGE-SECRET-KEY-1X"}))
	require.NoError(t, err)
	require.Equal(t, "env (WHOAS_MASTER_KEY)", s.Name())

	s, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "passphrase"}))
	require.NoError(t, err)
	require.Contains(t, s.Name(), "passphrase")

	s, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "os"}))
	require.NoError(t, err)
	require.Equal(t, "os keyring", s.Name())

	_, err = Select(dir, getenvFrom(map[string]string{"WHOAS_KEYSTORE": "nope"}))
	require.ErrorContains(t, err, "unknown WHOAS_KEYSTORE")
}

func TestEnvStore(t *testing.T) {
	s := envStore{key: "k"}
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "k", got)
	require.Error(t, s.Set("x"))
}

func TestOSStoreWithMock(t *testing.T) {
	keyring.MockInit()
	s := OS()
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("secret"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "secret", got)
}

func TestPassphraseStore(t *testing.T) {
	scryptWorkFactor = 10
	env := map[string]string{"WHOAS_PASSPHRASE": "correct horse"}
	s := newPassphraseStore(t.TempDir(), getenvFrom(env))
	s.prompt = func(string) (string, error) { return "", errors.New("no tty in tests") }

	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1ABC"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1ABC", got)

	env["WHOAS_PASSPHRASE"] = "wrong"
	_, err = s.Get()
	require.ErrorContains(t, err, "wrong passphrase")
}

func TestWSLStore(t *testing.T) {
	stored := ""
	s := &wslStore{exe: filepath.Join("C:", "whoas.exe"), run: func(args []string, stdin string) (string, int, error) {
		switch args[1] {
		case "get":
			if stored == "" {
				return "", 2, nil
			}
			return stored + "\r\n", 0, nil
		case "set":
			stored = stdin
			return "", 0, nil
		}
		return "", 1, nil
	}}
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1W"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1W", got)
}
```

**`internal/keystore/integration_test.go`**

```go
//go:build keyring_integration

package keystore

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// TestOSKeyringRoundTrip hits the real OS keyring under a throwaway service
// name, so it never touches the user's whoas master key.
func TestOSKeyringRoundTrip(t *testing.T) {
	service = fmt.Sprintf("whoas-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = keyring.Delete(service, account)
		service = "whoas"
	})
	s := OS()
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1TEST"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1TEST", got)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/keystore/
```

Expected: FAIL — `undefined: Select`, `undefined: OS`, `undefined: newPassphraseStore`, `undefined: wslStore`.

- [ ] **Step 4: Write the implementation**

**`internal/keystore/keystore.go`**

```go
// Package keystore stores the vault master key (an age X25519 identity).
package keystore

import (
	"errors"
	"fmt"
	"time"
)

// service is a var so integration tests can use a throwaway keyring entry.
var service = "whoas"

const account = "master"

// ErrNotFound means the backend holds no master key yet.
var ErrNotFound = errors.New("master key not found")

// Store is one place the master key can live.
type Store interface {
	Name() string
	Get() (string, error) // AGE-SECRET-KEY-1...; ErrNotFound when absent
	Set(secret string) error
	Slow() bool         // true when Get is slow or interactive; an agent should cache the key
	TTL() time.Duration // agent idle TTL for this store; 0 = no expiry
}

// Select picks the backend. Order: WHOAS_MASTER_KEY, WHOAS_KEYSTORE override,
// WSL bridge to Windows Credential Manager, OS keyring, passphrase file.
func Select(dataDir string, getenv func(string) string) (Store, error) {
	if k := getenv("WHOAS_MASTER_KEY"); k != "" {
		return envStore{key: k}, nil
	}
	switch v := getenv("WHOAS_KEYSTORE"); v {
	case "":
	case "os":
		return osStore{}, nil
	case "passphrase":
		return newPassphraseStore(dataDir, getenv), nil
	case "wsl":
		exe, err := findWinExe(getenv)
		if err != nil {
			return nil, err
		}
		return newWSLStore(exe), nil
	default:
		return nil, fmt.Errorf("unknown WHOAS_KEYSTORE %q (os|wsl|passphrase)", v)
	}
	if isWSL(getenv) {
		if exe, err := findWinExe(getenv); err == nil {
			return newWSLStore(exe), nil
		}
	}
	if osAvailable() {
		return osStore{}, nil
	}
	return newPassphraseStore(dataDir, getenv), nil
}
```

**`internal/keystore/env.go`**

```go
package keystore

import (
	"errors"
	"time"
)

// envStore reads the key from WHOAS_MASTER_KEY (CI, containers, tests).
type envStore struct{ key string }

func (s envStore) Name() string         { return "env (WHOAS_MASTER_KEY)" }
func (s envStore) Get() (string, error) { return s.key, nil }
func (s envStore) Set(string) error     { return errors.New("WHOAS_MASTER_KEY is read-only") }
func (s envStore) Slow() bool           { return false }
func (s envStore) TTL() time.Duration   { return 0 }
```

**`internal/keystore/os.go`**

```go
package keystore

import (
	"errors"
	"time"

	"github.com/zalando/go-keyring"
)

// osStore uses macOS Keychain, Windows Credential Manager or Linux Secret Service.
type osStore struct{}

// OS returns the OS keyring backend (used by the Windows keystore-helper).
func OS() Store { return osStore{} }

func (osStore) Name() string { return "os keyring" }

func (osStore) Get() (string, error) {
	s, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return s, err
}

func (osStore) Set(secret string) error { return keyring.Set(service, account, secret) }
func (osStore) Slow() bool              { return false }
func (osStore) TTL() time.Duration      { return 0 }

// osAvailable reports whether the OS keyring answers at all.
func osAvailable() bool {
	_, err := keyring.Get(service, account)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
```

**`internal/keystore/passphrase.go`**

```go
package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// scryptWorkFactor is age's default; tests lower it.
var scryptWorkFactor = 18

// passphraseStore keeps the key in <data>/master.age, encrypted with a passphrase.
type passphraseStore struct {
	path   string
	getenv func(string) string
	prompt func(label string) (string, error)
}

func newPassphraseStore(dataDir string, getenv func(string) string) *passphraseStore {
	return &passphraseStore{path: filepath.Join(dataDir, "master.age"), getenv: getenv, prompt: ttyPrompt}
}

func (s *passphraseStore) Name() string       { return "passphrase (" + s.path + ")" }
func (s *passphraseStore) Slow() bool         { return true }
func (s *passphraseStore) TTL() time.Duration { return 8 * time.Hour }

func (s *passphraseStore) passphrase(label string) (string, error) {
	if v := s.getenv("WHOAS_PASSPHRASE"); v != "" {
		return v, nil
	}
	return s.prompt(label)
}

func (s *passphraseStore) Get() (string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	pass, err := s.passphrase("whoas passphrase: ")
	if err != nil {
		return "", err
	}
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		return "", errors.New("wrong passphrase or corrupt key file")
	}
	secret, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret)), nil
}

func (s *passphraseStore) Set(secret string) error {
	pass := s.getenv("WHOAS_PASSPHRASE")
	if pass == "" {
		p1, err := s.prompt("new whoas passphrase: ")
		if err != nil {
			return err
		}
		p2, err := s.prompt("repeat passphrase: ")
		if err != nil {
			return err
		}
		if p1 != p2 {
			return errors.New("passphrases do not match")
		}
		pass = p1
	}
	if pass == "" {
		return errors.New("empty passphrase")
	}
	rcpt, err := age.NewScryptRecipient(pass)
	if err != nil {
		return err
	}
	rcpt.SetWorkFactor(scryptWorkFactor)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, secret); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("encrypt master key: %w", err)
	}
	return fsutil.WriteFileAtomic(s.path, buf.Bytes(), 0o600)
}
```

**`internal/keystore/prompt.go`**

```go
package keystore

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// ttyPrompt reads a hidden line from the controlling terminal, so it works
// even when stdin is piped.
func ttyPrompt(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("passphrase needed but no terminal (set WHOAS_PASSPHRASE): %w", err)
	}
	defer tty.Close()
	fmt.Fprint(tty, label)
	b, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

**`internal/keystore/wsl.go`**

```go
package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runner executes whoas.exe; code is the exit code, err only for spawn failures.
type runner func(args []string, stdin string) (stdout string, code int, err error)

// wslStore keeps the key in Windows Credential Manager by calling the
// Windows build of whoas (whoas.exe keystore-helper) through WSL interop.
type wslStore struct {
	exe string
	run runner
}

func newWSLStore(exe string) *wslStore { return &wslStore{exe: exe, run: execRunner(exe)} }

func (s *wslStore) Name() string       { return "wsl (Windows Credential Manager via " + s.exe + ")" }
func (s *wslStore) Slow() bool         { return true }
func (s *wslStore) TTL() time.Duration { return 0 }

func (s *wslStore) Get() (string, error) {
	out, code, err := s.run([]string{"keystore-helper", "get"}, "")
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
		return strings.TrimSpace(out), nil
	case 2:
		return "", ErrNotFound
	default:
		return "", fmt.Errorf("%s keystore-helper get: exit %d", s.exe, code)
	}
}

func (s *wslStore) Set(secret string) error {
	_, code, err := s.run([]string{"keystore-helper", "set"}, secret)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s keystore-helper set: exit %d", s.exe, code)
	}
	return nil
}

func execRunner(exe string) runner {
	return func(args []string, stdin string) (string, int, error) {
		cmd := exec.Command(exe, args...)
		cmd.Stdin = strings.NewReader(stdin)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), ee.ExitCode(), nil
		}
		if err != nil {
			return "", -1, err
		}
		return out.String(), 0, nil
	}
}

func isWSL(getenv func(string) string) bool {
	if getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && bytes.Contains(bytes.ToLower(b), []byte("microsoft"))
}

// findWinExe locates whoas.exe: $WHOAS_WIN_EXE, else next to this binary.
func findWinExe(getenv func(string) string) (string, error) {
	if p := getenv("WHOAS_WIN_EXE"); p != "" {
		return p, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cand := filepath.Join(filepath.Dir(self), "whoas.exe")
	if _, err := os.Stat(cand); err != nil {
		return "", errors.New("whoas.exe not found next to whoas (set WHOAS_WIN_EXE)")
	}
	return cand, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/keystore/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/keystore/env.go internal/keystore/integration_test.go internal/keystore/keystore.go internal/keystore/keystore_test.go internal/keystore/os.go internal/keystore/passphrase.go internal/keystore/prompt.go internal/keystore/wsl.go
git commit -m "feat: add keystore backends (os keyring, WSL bridge, passphrase, env)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Envelope and age vault

**Files:**
- Create: `internal/envelope/envelope.go`
- Create: `internal/vault/vault.go`
- Test: `internal/envelope/envelope_test.go`
- Test: `internal/vault/vault_test.go`

**Interfaces:**
- Consumes: `keystore.Store`, `keystore.ErrNotFound` (Task 5); `ref.Ref` (Task 3); `fsutil.WriteFileAtomic` (Task 2).
- Produces: `envelope.File{Mode uint32; Data []byte}`, `envelope.Envelope{V int; Provider string; Files map[string]envelope.File}`, `envelope.New(provider string, files map[string]envelope.File) *envelope.Envelope`, `envelope.Hash(files map[string]envelope.File) string`. `vault.Decrypter` interface `{Decrypt(ciphertext []byte) ([]byte, error)}`; `vault.Init(recipientFile, vaultDir string, store keystore.Store) (*vault.Vault, error)`; `vault.Open(recipientFile, vaultDir string) (*vault.Vault, error)`; `vault.ErrNotInitialized`, `vault.ErrNotFound`; methods `Exists(ref.Ref) bool`, `Seal(ref.Ref, *envelope.Envelope) error` (public key only), `Load(ref.Ref, vault.Decrypter) (*envelope.Envelope, error)`, `WriteMeta(ref.Ref, vault.Meta) error`, `ReadMeta(ref.Ref) (vault.Meta, error)`, `Remove(ref.Ref) error`, `List() ([]vault.Entry, error)`; `vault.Meta{Account string; Created time.Time}`, `vault.Entry{Ref ref.Ref; Meta vault.Meta}`; `vault.NewIdentityDecrypter(secret string) (*vault.IdentityDecrypter, error)`.

- [ ] **Step 1: Write the failing tests**

**`internal/envelope/envelope_test.go`**

```go
package envelope

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHash(t *testing.T) {
	a := map[string]File{"a.json": {Mode: 0o600, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("2")}}
	b := map[string]File{"dir/b": {Mode: 0o600, Data: []byte("2")}, "a.json": {Mode: 0o600, Data: []byte("1")}}
	require.Equal(t, Hash(a), Hash(b), "order independent")

	c := map[string]File{"a.json": {Mode: 0o600, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("3")}}
	require.NotEqual(t, Hash(a), Hash(c), "content sensitive")

	d := map[string]File{"a.json": {Mode: 0o644, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("2")}}
	require.NotEqual(t, Hash(a), Hash(d), "mode sensitive")
}
```

**`internal/vault/vault_test.go`**

```go
//go:build unix

package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/ref"
)

// memStore is an in-memory keystore.Store.
type memStore struct{ secret string }

func (m *memStore) Name() string { return "mem" }
func (m *memStore) Get() (string, error) {
	if m.secret == "" {
		return "", keystore.ErrNotFound
	}
	return m.secret, nil
}
func (m *memStore) Set(s string) error { m.secret = s; return nil }
func (m *memStore) Slow() bool         { return false }
func (m *memStore) TTL() time.Duration { return 0 }

func newVault(t *testing.T) (*Vault, *memStore, string) {
	t.Helper()
	root := t.TempDir()
	st := &memStore{}
	v, err := Init(filepath.Join(root, "recipient"), filepath.Join(root, "vault"), st)
	require.NoError(t, err)
	return v, st, root
}

func TestInitGeneratesKeyOnce(t *testing.T) {
	v, st, root := newVault(t)
	require.NotNil(t, v)
	require.Contains(t, st.secret, "AGE-SECRET-KEY-1")
	first := st.secret

	_, err := Init(filepath.Join(root, "recipient"), filepath.Join(root, "vault"), st)
	require.NoError(t, err)
	require.Equal(t, first, st.secret, "second Init must not rotate the key")
}

func TestOpenNotInitialized(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "recipient"), t.TempDir())
	require.ErrorIs(t, err, ErrNotInitialized)
}

func TestSealLoadRoundTrip(t *testing.T) {
	v, st, _ := newVault(t)
	r := ref.Ref{Provider: "gh", Alias: "work"}
	env := envelope.New("gh", map[string]envelope.File{"hosts.yml": {Mode: 0o600, Data: []byte("token: x")}})
	require.NoError(t, v.Seal(r, env))
	require.True(t, v.Exists(r))

	info, err := os.Stat(v.base(r) + ".age")
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	d, err := NewIdentityDecrypter(st.secret)
	require.NoError(t, err)
	got, err := v.Load(r, d)
	require.NoError(t, err)
	require.Equal(t, env, got)

	_, err = v.Load(ref.Ref{Provider: "gh", Alias: "nope"}, d)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSealDoesNotNeedPrivateKey(t *testing.T) {
	v, st, root := newVault(t)
	st.secret = "" // private key unavailable
	reopened, err := Open(filepath.Join(root, "recipient"), filepath.Join(root, "vault"))
	require.NoError(t, err)
	require.NoError(t, reopened.Seal(ref.Ref{Provider: "gh", Alias: "x"}, envelope.New("gh", nil)))
	require.NotNil(t, v)
}

func TestListMetaRemove(t *testing.T) {
	v, _, _ := newVault(t)
	refs := []ref.Ref{{Provider: "vercel", Alias: "work"}, {Provider: "mcp/posthog", Alias: "acme"}, {Provider: "gh", Alias: "me"}}
	for _, r := range refs {
		require.NoError(t, v.Seal(r, envelope.New(r.Provider, nil)))
	}
	require.NoError(t, v.WriteMeta(refs[0], Meta{Account: "adrian@acme.com", Created: time.Unix(1, 0).UTC()}))

	list, err := v.List()
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, "gh@me", list[0].Ref.String())
	require.Equal(t, "mcp/posthog@acme", list[1].Ref.String())
	require.Equal(t, "vercel@work", list[2].Ref.String())
	require.Equal(t, "adrian@acme.com", list[2].Meta.Account)

	require.NoError(t, v.Remove(refs[0]))
	require.False(t, v.Exists(refs[0]))
	require.ErrorIs(t, v.Remove(refs[0]), ErrNotFound)
}

func TestListEmptyVault(t *testing.T) {
	v, _, _ := newVault(t)
	list, err := v.List()
	require.NoError(t, err)
	require.Empty(t, list)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/envelope/ ./internal/vault/
```

Expected: FAIL — `undefined: Hash`, `undefined: Init`.

- [ ] **Step 3: Write the implementation**

**`internal/envelope/envelope.go`**

```go
// Package envelope is the plaintext form of one sealed identity: the auth
// files a CLI wrote, keyed by slash-separated path relative to its config dir.
package envelope

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// Version of the envelope format.
const Version = 1

// File is one auth file. Data is base64 in JSON.
type File struct {
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

// Envelope is what gets age-encrypted into the vault.
type Envelope struct {
	V        int             `json:"v"`
	Provider string          `json:"provider"`
	Files    map[string]File `json:"files"`
}

// New builds a current-version envelope.
func New(provider string, files map[string]File) *Envelope {
	return &Envelope{V: Version, Provider: provider, Files: files}
}

// Hash is a stable sha256 over sorted paths, modes and contents.
func Hash(files map[string]File) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var n [8]byte
	for _, p := range paths {
		f := files[p]
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
		binary.BigEndian.PutUint64(n[:], uint64(f.Mode))
		h.Write(n[:])
		binary.BigEndian.PutUint64(n[:], uint64(len(f.Data)))
		h.Write(n[:])
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

**`internal/vault/vault.go`**

```go
// Package vault stores sealed identities: one age file per provider@alias.
// Sealing needs only the public key (the recipient file); opening needs a
// Decrypter that holds or reaches the private key.
package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/fsutil"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/ref"
)

var (
	ErrNotInitialized = errors.New("vault not initialized (run: whoas add <provider> <alias>)")
	ErrNotFound       = errors.New("identity not found")
)

// Decrypter turns an age ciphertext into plaintext.
type Decrypter interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

// Meta is non-secret metadata stored next to each blob.
type Meta struct {
	Account string    `json:"account,omitempty"`
	Created time.Time `json:"created"`
}

// Entry is one identity in a listing.
type Entry struct {
	Ref  ref.Ref
	Meta Meta
}

// Vault is the sealed-identity store.
type Vault struct {
	dir       string
	recipient age.Recipient
}

// Init makes sure a master key exists in store (generating one if needed)
// and that the recipient file matches it, then opens the vault.
func Init(recipientFile, vaultDir string, store keystore.Store) (*Vault, error) {
	v, err := Open(recipientFile, vaultDir)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrNotInitialized) {
		return nil, err
	}
	secret, err := store.Get()
	if errors.Is(err, keystore.ErrNotFound) {
		id, gerr := age.GenerateX25519Identity()
		if gerr != nil {
			return nil, gerr
		}
		secret = id.String()
		if err := store.Set(secret); err != nil {
			return nil, fmt.Errorf("store master key in %s: %w", store.Name(), err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read master key from %s: %w", store.Name(), err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(secret))
	if err != nil {
		return nil, fmt.Errorf("master key in %s is invalid: %w", store.Name(), err)
	}
	if err := fsutil.WriteFileAtomic(recipientFile, []byte(id.Recipient().String()+"\n"), 0o644); err != nil {
		return nil, err
	}
	return Open(recipientFile, vaultDir)
}

// Open loads the recipient (public key); it never touches the master key.
func Open(recipientFile, vaultDir string) (*Vault, error) {
	data, err := os.ReadFile(recipientFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	r, err := age.ParseX25519Recipient(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", recipientFile, err)
	}
	return &Vault{dir: vaultDir, recipient: r}, nil
}

func (v *Vault) base(r ref.Ref) string {
	return filepath.Join(v.dir, filepath.FromSlash(r.Provider), r.Alias)
}

// Exists reports whether r has a sealed blob.
func (v *Vault) Exists(r ref.Ref) bool {
	_, err := os.Stat(v.base(r) + ".age")
	return err == nil
}

// Seal encrypts env to the public key and writes it atomically (0600).
func (v *Vault) Seal(r ref.Ref, env *envelope.Envelope) error {
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.recipient)
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(v.base(r)+".age", buf.Bytes(), 0o600)
}

// Load decrypts r's envelope.
func (v *Vault) Load(r ref.Ref, d Decrypter) (*envelope.Envelope, error) {
	ct, err := os.ReadFile(v.base(r) + ".age")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", r, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	plain, err := d.Decrypt(ct)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", r, err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", r, err)
	}
	return &env, nil
}

// WriteMeta stores r's metadata (0600).
func (v *Vault) WriteMeta(r ref.Ref, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(v.base(r)+".meta.json", data, 0o600)
}

// ReadMeta returns r's metadata; missing metadata is the zero Meta.
func (v *Vault) ReadMeta(r ref.Ref) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(v.base(r) + ".meta.json")
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// Remove deletes r's blob and metadata.
func (v *Vault) Remove(r ref.Ref) error {
	if !v.Exists(r) {
		return fmt.Errorf("%s: %w", r, ErrNotFound)
	}
	if err := os.Remove(v.base(r) + ".age"); err != nil {
		return err
	}
	if err := os.Remove(v.base(r) + ".meta.json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every identity sorted by provider then alias.
func (v *Vault) List() ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(v.dir, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".age") {
			return nil
		}
		rel, err := filepath.Rel(v.dir, strings.TrimSuffix(path, ".age"))
		if err != nil {
			return err
		}
		r := ref.Ref{Provider: filepath.ToSlash(filepath.Dir(rel)), Alias: filepath.Base(rel)}
		m, err := v.ReadMeta(r)
		if err != nil {
			return err
		}
		out = append(out, Entry{Ref: r, Meta: m})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out, err
}

// IdentityDecrypter decrypts with an in-memory age identity.
type IdentityDecrypter struct{ id *age.X25519Identity }

// NewIdentityDecrypter parses an AGE-SECRET-KEY-1... string.
func NewIdentityDecrypter(secret string) (*IdentityDecrypter, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(secret))
	if err != nil {
		return nil, fmt.Errorf("invalid master key: %w", err)
	}
	return &IdentityDecrypter{id: id}, nil
}

func (d *IdentityDecrypter) Decrypt(ct []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), d.id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/envelope/ ./internal/vault/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/envelope/envelope.go internal/envelope/envelope_test.go internal/vault/vault.go internal/vault/vault_test.go
git commit -m "feat: seal identities into an age vault" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Declarative adapters + built-ins (gh, gcloud, codex, neonctl)

**Files:**
- Create: `internal/adapter/adapter.go`
- Create: `internal/adapter/builtin/gh.toml`
- Create: `internal/adapter/builtin/gcloud.toml`
- Create: `internal/adapter/builtin/codex.toml`
- Create: `internal/adapter/builtin/neonctl.toml`
- Test: `internal/adapter/adapter_test.go`

**Interfaces:**
- Consumes: `ref.ValidProvider` (Task 3).
- Produces: `adapter.Adapter{Name, Bin string; Kind adapter.Kind; Dir adapter.DirSpec; Login adapter.Login; Whoami adapter.Whoami}`; `adapter.DirSpec{Global string; Inject adapter.Inject; AuthFiles []string}`; `adapter.Inject{Env map[string]string; Flag []string}`; `adapter.Login{Cmd []string; Hint string}`; `adapter.Whoami{Cmd []string; JSON string}`; `adapter.KindDirOverlay`; `adapter.Vars{Home, XDGConfig, XDGData string}`; `adapter.Parse(data []byte, source string) (*adapter.Adapter, error)`; `adapter.Expand(s string, v adapter.Vars, dir string) string`; methods `GlobalDir(adapter.Vars) string`, `InjectArgs(dir string) []string`, `InjectEnv(dir string) []string`, `Validate() error`; `adapter.LoadRegistry(userDir string) (*adapter.Registry, error)` with `Get(name string) (*adapter.Adapter, bool)`, `ByBin(bin string) (*adapter.Adapter, bool)`, `Names() []string`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/BurntSushi/toml@v1.6.0
```

- [ ] **Step 2: Write the failing tests**

**`internal/adapter/adapter_test.go`**

```go
//go:build unix

package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const valid = `name = "fake"
bin  = "fake"
kind = "dir-overlay"
[dir]
global     = "{home}/.fake"
inject     = { env = { FAKE_DIR = "{dir}" } }
auth_files = ["auth.json", "nested/dir"]
[login]
cmd = ["fake", "login"]
[whoami]
cmd  = ["fake", "whoami"]
json = "user.email"
`

func TestBuiltinsLoad(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "none"))
	require.NoError(t, err)
	require.Equal(t, []string{"codex", "gcloud", "gh", "neonctl"}, r.Names())
	gh, ok := r.ByBin("gh")
	require.True(t, ok)
	require.Equal(t, []string{"GH_CONFIG_DIR=/o"}, gh.InjectEnv("/o"))
	neon, _ := r.Get("neonctl")
	require.Equal(t, []string{"--config-dir", "/o"}, neon.InjectArgs("/o"))
}

func TestUserAdapterOverridesAndExpands(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.toml"), []byte(valid), 0o600))
	r, err := LoadRegistry(dir)
	require.NoError(t, err)
	a, ok := r.Get("fake")
	require.True(t, ok)
	require.Equal(t, "/h/.fake", a.GlobalDir(Vars{Home: "/h"}))
	require.Equal(t, []string{"FAKE_DIR=/run/x"}, a.InjectEnv("/run/x"))
	require.Empty(t, a.InjectArgs("/run/x"))
	require.Equal(t, "user.email", a.Whoami.JSON)
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":   valid + "colour = \"blue\"\n",
		"bad kind":      replace(valid, `kind = "dir-overlay"`, `kind = "env-token"`),
		"abs auth":      replace(valid, `"auth.json", "nested/dir"`, `"/etc/passwd"`),
		"dotdot auth":   replace(valid, `"auth.json", "nested/dir"`, `"../x"`),
		"no dir ref":    replace(valid, `FAKE_DIR = "{dir}"`, `FAKE_DIR = "/tmp"`),
		"both injects":  replace(valid, `inject     = { env = { FAKE_DIR = "{dir}" } }`, `inject     = { env = { FAKE_DIR = "{dir}" }, flag = ["--d", "{dir}"] }`),
		"login bin":     replace(valid, `cmd = ["fake", "login"]`, `cmd = ["sh", "-c", "evil"]`),
		"bin with path": replace(valid, `bin  = "fake"`, `bin  = "/usr/bin/fake"`),
		"reserved name": replace(valid, `name = "fake"`, `name = "run"`),
	}
	for name, body := range cases {
		_, err := Parse([]byte(body), name)
		require.Error(t, err, name)
	}
}

func replace(s, old, new string) string { return strings.Replace(s, old, new, 1) }
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/adapter/
```

Expected: FAIL — `undefined: LoadRegistry`, `undefined: Parse`.

- [ ] **Step 4: Write the implementation**

**`internal/adapter/adapter.go`**

```go
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
```

**`internal/adapter/builtin/gh.toml`**

```toml
name = "gh"
bin  = "gh"
kind = "dir-overlay"

[dir]
global     = "{xdg_config}/gh"
inject     = { env = { GH_CONFIG_DIR = "{dir}" } }
auth_files = ["hosts.yml"]

[login]
# --insecure-storage keeps the token in hosts.yml, which whoas then seals.
cmd  = ["gh", "auth", "login", "--insecure-storage"]
hint = "If your browser is signed in to another GitHub account, switch accounts there first."

[whoami]
cmd  = ["gh", "api", "user"]
json = "login"
```

**`internal/adapter/builtin/gcloud.toml`**

```toml
name = "gcloud"
bin  = "gcloud"
kind = "dir-overlay"

[dir]
global = "{home}/.config/gcloud"
inject = { env = { CLOUDSDK_CONFIG = "{dir}" } }
auth_files = [
  "credentials.db",
  "access_tokens.db",
  "legacy_credentials",
  "configurations",
  "active_config",
  "application_default_credentials.json",
]

[login]
cmd  = ["gcloud", "auth", "login"]
hint = "Pick the Google account in the browser window that opens."

[whoami]
cmd = ["gcloud", "config", "get-value", "account"]
```

**`internal/adapter/builtin/codex.toml`**

```toml
name = "codex"
bin  = "codex"
kind = "dir-overlay"

[dir]
global     = "{home}/.codex"
inject     = { env = { CODEX_HOME = "{dir}" } }
auth_files = ["auth.json"]

[login]
cmd = ["codex", "login"]

[whoami]
cmd = ["codex", "login", "status"]
```

**`internal/adapter/builtin/neonctl.toml`**

```toml
name = "neonctl"
bin  = "neonctl"
kind = "dir-overlay"

[dir]
global     = "{xdg_config}/neonctl"
# neonctl has no env var for its config dir, only this global flag.
inject     = { flag = ["--config-dir", "{dir}"] }
auth_files = ["credentials.json"]

[login]
cmd = ["neonctl", "auth"]

[whoami]
cmd  = ["neonctl", "me", "--output", "json"]
json = "email"
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/adapter/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/adapter/adapter.go internal/adapter/adapter_test.go internal/adapter/builtin/codex.toml internal/adapter/builtin/gcloud.toml internal/adapter/builtin/gh.toml internal/adapter/builtin/neonctl.toml
git commit -m "feat: add declarative TOML adapters with built-ins" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Overlay builder

**Files:**
- Create: `internal/overlay/overlay.go`
- Test: `internal/overlay/overlay_test.go`

**Interfaces:**
- Consumes: `envelope.File` (Task 6).
- Produces: `overlay.Build(dir, global string, authPaths []string, files map[string]envelope.File) error`; `overlay.Collect(root string, authPaths []string) (map[string]envelope.File, error)`; `overlay.Promote(dir, global string, authPaths []string) error`; `overlay.Remove(dir string) error`.

- [ ] **Step 1: Write the failing tests**

**`internal/overlay/overlay_test.go`**

```go
//go:build unix

package overlay

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/envelope"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func isLink(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info.Mode()&os.ModeSymlink != 0
}

var auth = []string{"auth.json", ".w/config/default.toml"}

func globalFixture(t *testing.T) string {
	g := t.TempDir()
	write(t, filepath.Join(g, "a.txt"), "shared")
	write(t, filepath.Join(g, "sub", "b.txt"), "shared-b")
	write(t, filepath.Join(g, "auth.json"), "global-auth")
	write(t, filepath.Join(g, ".w", "other.txt"), "w-other")
	write(t, filepath.Join(g, ".w", "config", "default.toml"), "global-toml")
	return g
}

func TestBuildMirrorsAndMaterializes(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	files := map[string]envelope.File{
		"auth.json":              {Mode: 0o600, Data: []byte("id-auth")},
		".w/config/default.toml": {Mode: 0o600, Data: []byte("id-toml")},
	}
	require.NoError(t, Build(dir, g, auth, files))

	require.True(t, isLink(t, filepath.Join(dir, "a.txt")))
	require.True(t, isLink(t, filepath.Join(dir, "sub")))
	require.True(t, isLink(t, filepath.Join(dir, ".w", "other.txt")))
	require.False(t, isLink(t, filepath.Join(dir, ".w")))
	require.False(t, isLink(t, filepath.Join(dir, ".w", "config")))
	require.Equal(t, "id-auth", read(t, filepath.Join(dir, "auth.json")))
	require.Equal(t, "id-toml", read(t, filepath.Join(dir, ".w", "config", "default.toml")))
	require.Equal(t, "shared", read(t, filepath.Join(dir, "a.txt")))
	require.Equal(t, "global-auth", read(t, filepath.Join(g, "auth.json")), "global auth untouched")
}

func TestBuildWithoutGlobal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, filepath.Join(t.TempDir(), "missing"), auth, map[string]envelope.File{"auth.json": {Data: []byte("x")}}))
	require.Equal(t, "x", read(t, filepath.Join(dir, "auth.json")))
	info, err := os.Stat(filepath.Join(dir, "auth.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "zero mode defaults to 0600")
}

func TestCollect(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "auth.json"), "a")
	write(t, filepath.Join(root, "creds", "x", "adc.json"), "b")
	write(t, filepath.Join(root, "not-auth.txt"), "c")
	got, err := Collect(root, []string{"auth.json", "creds", "missing.json"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "a", string(got["auth.json"].Data))
	require.Equal(t, "b", string(got["creds/x/adc.json"].Data))
	require.Equal(t, uint32(0o600), got["auth.json"].Mode)
}

func TestPromote(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, map[string]envelope.File{"auth.json": {Data: []byte("id")}}))
	write(t, filepath.Join(dir, "new.txt"), "created-by-cli")
	write(t, filepath.Join(dir, "settings", "theme.txt"), "dark")
	write(t, filepath.Join(dir, ".w", "fresh.txt"), "in-ancestor")

	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "created-by-cli", read(t, filepath.Join(g, "new.txt")))
	require.Equal(t, "dark", read(t, filepath.Join(g, "settings", "theme.txt")))
	require.Equal(t, "in-ancestor", read(t, filepath.Join(g, ".w", "fresh.txt")))
	require.Equal(t, "global-auth", read(t, filepath.Join(g, "auth.json")), "auth never promoted")
}

func TestPromoteKeepsExistingGlobal(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	write(t, filepath.Join(dir, "a.txt"), "overlay-version")
	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}

func TestPromoteCrossDevice(t *testing.T) {
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = os.Rename })
	g := t.TempDir()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings", "k.txt"), "v")
	require.NoError(t, Promote(dir, g, auth))
	require.Equal(t, "v", read(t, filepath.Join(g, "settings", "k.txt")))
	_, err := os.Stat(filepath.Join(dir, "settings"))
	require.True(t, os.IsNotExist(err))
}

func TestRemoveDoesNotFollowLinks(t *testing.T) {
	g := globalFixture(t)
	dir := filepath.Join(t.TempDir(), "ov")
	require.NoError(t, Build(dir, g, auth, nil))
	require.NoError(t, Remove(dir))
	require.Equal(t, "shared", read(t, filepath.Join(g, "a.txt")))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/overlay/
```

Expected: FAIL — `undefined: Build`, `undefined: rename`.

- [ ] **Step 3: Write the implementation**

**`internal/overlay/overlay.go`**

```go
// Package overlay builds per-identity copies of a CLI config dir: every entry
// is a symlink to the real (global) dir except the auth paths, which hold the
// identity's own files. Shared state stays shared; only auth differs.
package overlay

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/S-kkipie/whoas/internal/envelope"
)

// split returns the cleaned auth paths and every ancestor dir of them.
func split(authPaths []string) (auth, ancestors map[string]bool) {
	auth, ancestors = map[string]bool{}, map[string]bool{}
	for _, p := range authPaths {
		p = filepath.Clean(filepath.FromSlash(p))
		auth[p] = true
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			ancestors[d] = true
		}
	}
	return auth, ancestors
}

// Build creates dir mirroring global, then writes files (slash paths).
func Build(dir, global string, authPaths []string, files map[string]envelope.File) error {
	global, err := filepath.Abs(global)
	if err != nil {
		return err
	}
	auth, anc := split(authPaths)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := mirror(dir, global, ".", auth, anc); err != nil {
		return err
	}
	for rel, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(f.Mode) & 0o777
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(p, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

func mirror(dir, global, rel string, auth, anc map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(global, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		switch {
		case auth[child]:
			// materialized from the vault, never linked
		case anc[child]:
			if err := os.MkdirAll(filepath.Join(dir, child), 0o700); err != nil {
				return err
			}
			if err := mirror(dir, global, child, auth, anc); err != nil {
				return err
			}
		default:
			if err := os.Symlink(filepath.Join(global, child), filepath.Join(dir, child)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Collect reads the auth paths under root (files or whole dirs).
// Missing paths are skipped. Keys are slash paths relative to root.
func Collect(root string, authPaths []string) (map[string]envelope.File, error) {
	out := map[string]envelope.File{}
	for _, p := range authPaths {
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			if err := add(out, root, full, info); err != nil {
				return nil, err
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return add(out, root, path, fi)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func add(out map[string]envelope.File, root, path string, info fs.FileInfo) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	out[filepath.ToSlash(rel)] = envelope.File{Mode: uint32(info.Mode().Perm()), Data: data}
	return nil
}

// rename is swapped in tests to simulate a cross-device move.
var rename = os.Rename

// Promote moves entries the CLI created inside dir (real files or dirs, not
// auth, not symlinks) into global when global lacks them, so non-auth state
// stays shared. Existing global entries always win.
func Promote(dir, global string, authPaths []string) error {
	auth, anc := split(authPaths)
	return promote(dir, global, ".", auth, anc)
}

func promote(dir, global, rel string, auth, anc map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(dir, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(rel, e.Name())
		if auth[child] || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if anc[child] {
			if err := promote(dir, global, child, auth, anc); err != nil {
				return err
			}
			continue
		}
		dst := filepath.Join(global, child)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := move(filepath.Join(dir, child), dst); err != nil {
			return err
		}
	}
	return nil
}

// move renames, falling back to copy+delete across filesystems (tmpfs → home).
func move(src, dst string) error {
	if err := rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Remove deletes dir; symlinks are removed, never followed.
func Remove(dir string) error { return os.RemoveAll(dir) }
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/overlay/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/overlay/overlay.go internal/overlay/overlay_test.go
git commit -m "feat: build per-identity overlays of CLI config dirs" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Run lock and holder state

**Files:**
- Create: `internal/runlock/runlock.go`
- Create: `internal/runlock/alive_unix.go`
- Create: `internal/runlock/alive_windows.go`
- Test: `internal/runlock/runlock_test.go`

**Interfaces:**
- Consumes: `fsutil.WriteFileAtomic` (Task 2).
- Produces: `runlock.Acquire(ctx context.Context, base string, timeout time.Duration) (*runlock.Lock, error)` (lock file `base+".lock"`); `Lock.Read() (runlock.State, error)`, `Lock.Write(runlock.State) error` (empty state deletes `base+".state.json"`), `Lock.Release() error`; `runlock.Peek(base string) (runlock.State, error)`; `runlock.State{Holders []int; Hash string}` with `Add(pid)`, `Remove(pid)`, `Prune()`, `Live() []int`; `runlock.Alive(pid int) bool`; `runlock.ErrBusy`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/gofrs/flock@v0.13.1
```

- [ ] **Step 2: Write the failing tests**

**`internal/runlock/runlock_test.go`**

```go
//go:build unix

package runlock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

func TestAcquireExclusive(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gh", "work")
	l, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)

	_, err = Acquire(context.Background(), base, 100*time.Millisecond)
	require.ErrorIs(t, err, ErrBusy)

	require.NoError(t, l.Release())
	l2, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)
	require.NoError(t, l2.Release())
}

func TestStateRoundTripAndPrune(t *testing.T) {
	base := filepath.Join(t.TempDir(), "work")
	l, err := Acquire(context.Background(), base, time.Second)
	require.NoError(t, err)
	defer l.Release()

	s, err := l.Read()
	require.NoError(t, err)
	require.Empty(t, s.Holders)

	dead := deadPID(t)
	s.Add(os.Getpid())
	s.Add(dead)
	s.Add(os.Getpid())
	s.Hash = "h1"
	require.NoError(t, l.Write(s))

	got, err := Peek(base)
	require.NoError(t, err)
	require.Equal(t, []int{os.Getpid(), dead}, got.Holders)
	require.Equal(t, []int{os.Getpid()}, got.Live())

	got.Prune()
	require.Equal(t, []int{os.Getpid()}, got.Holders)
	got.Remove(os.Getpid())
	got.Hash = ""
	require.NoError(t, l.Write(got))
	_, err = os.Stat(base + ".state.json")
	require.True(t, os.IsNotExist(err), "empty state deletes the file")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/runlock/
```

Expected: FAIL — `undefined: Acquire`, `undefined: Peek`.

- [ ] **Step 4: Write the implementation**

**`internal/runlock/runlock.go`**

```go
// Package runlock coordinates processes sharing one identity overlay: an
// exclusive flock on <base>.lock guards <base>.state.json, which records the
// PIDs using the overlay and the hash of the auth files last sealed.
package runlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// ErrBusy means the lock was not obtained in time.
var ErrBusy = errors.New("identity busy")

// State is the shared bookkeeping of one identity overlay.
type State struct {
	Holders []int  `json:"holders"`
	Hash    string `json:"hash,omitempty"`
}

// Lock is a held exclusive lock.
type Lock struct {
	fl    *flock.Flock
	state string
}

// Acquire takes the lock for base, waiting up to timeout.
func Acquire(ctx context.Context, base string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(base + ".lock")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 20*time.Millisecond)
	if !ok {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: lock held for more than %s (%s.lock)", ErrBusy, timeout, base)
	}
	return &Lock{fl: fl, state: base + ".state.json"}, nil
}

// Read returns the state; missing state is the zero State.
func (l *Lock) Read() (State, error) { return readState(l.state) }

// Write stores the state; an empty state deletes the file.
func (l *Lock) Write(s State) error {
	if len(s.Holders) == 0 && s.Hash == "" {
		err := os.Remove(l.state)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(l.state, data, 0o600)
}

// Release drops the lock.
func (l *Lock) Release() error { return l.fl.Unlock() }

// Peek reads base's state without locking (for listings).
func Peek(base string) (State, error) { return readState(base + ".state.json") }

func readState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// Add records pid as a holder.
func (s *State) Add(pid int) {
	if !slices.Contains(s.Holders, pid) {
		s.Holders = append(s.Holders, pid)
	}
}

// Remove drops pid from the holders.
func (s *State) Remove(pid int) {
	s.Holders = slices.DeleteFunc(s.Holders, func(p int) bool { return p == pid })
}

// Prune drops holders whose process no longer exists.
func (s *State) Prune() {
	s.Holders = slices.DeleteFunc(s.Holders, func(p int) bool { return !Alive(p) })
}

// Live returns the holders that are still running.
func (s State) Live() []int {
	return slices.DeleteFunc(slices.Clone(s.Holders), func(p int) bool { return !Alive(p) })
}
```

**`internal/runlock/alive_unix.go`**

```go
//go:build unix

package runlock

import (
	"errors"
	"syscall"
)

// Alive reports whether a process with pid exists.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
```

**`internal/runlock/alive_windows.go`**

```go
//go:build windows

package runlock

// Alive is conservative on Windows, where running CLIs is not supported yet.
func Alive(pid int) bool { return true }
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/runlock/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/runlock/alive_unix.go internal/runlock/alive_windows.go internal/runlock/runlock.go internal/runlock/runlock_test.go
git commit -m "feat: coordinate identity holders with flock state" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Key agent and decrypter selection

**Files:**
- Create: `internal/agent/protocol.go`
- Create: `internal/agent/server_unix.go`
- Create: `internal/agent/client_unix.go`
- Create: `internal/agent/peer_linux.go`
- Create: `internal/agent/peer_darwin.go`
- Create: `internal/agent/peer_other.go`
- Create: `internal/agent/agent_windows.go`
- Create: `internal/keysource/keysource.go`
- Test: `internal/agent/agent_test.go`
- Test: `internal/keysource/keysource_test.go`

**Interfaces:**
- Consumes: `vault.NewIdentityDecrypter`, `vault.Decrypter` (Task 6); `keystore.Store` (Task 5).
- Produces: `agent.NewServer() *agent.Server` with `Serve(ctx context.Context, socketPath string) error`; `agent.NewClient(socket string) *agent.Client` with `Status() (locked bool, err error)`, `Load(key string, ttl time.Duration) error`, `Lock() error`, `Decrypt([]byte) ([]byte, error)` (implements `vault.Decrypter`); `agent.Spawn(self, socket string) error`; `agent.ErrLocked`. `keysource.Decrypter(socket string, store keystore.Store, self string) (vault.Decrypter, error)`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/awnumar/memguard@v0.23.0 golang.org/x/sys@v0.47.0
```

- [ ] **Step 2: Write the failing tests**

**`internal/agent/agent_test.go`**

```go
//go:build unix

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
)

// shortSocket keeps unix socket paths under the ~104 byte limit.
func shortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("", "wa")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func startServer(t *testing.T) (*Server, *Client) {
	t.Helper()
	sock := shortSocket(t)
	srv := NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	c := NewClient(sock)
	require.Eventually(t, func() bool { _, err := c.Status(); return err == nil }, 2*time.Second, 10*time.Millisecond)
	return srv, c
}

func encrypt(t *testing.T, id *age.X25519Identity, msg string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	require.NoError(t, err)
	_, err = w.Write([]byte(msg))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestAgentLifecycle(t *testing.T) {
	_, c := startServer(t)
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ct := encrypt(t, id, "hello")

	locked, err := c.Status()
	require.NoError(t, err)
	require.True(t, locked)
	_, err = c.Decrypt(ct)
	require.ErrorIs(t, err, ErrLocked)

	require.Error(t, c.Load("not-a-key", 0))
	require.NoError(t, c.Load(id.String(), 0))
	plain, err := c.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, "hello", string(plain))

	require.NoError(t, c.Lock())
	_, err = c.Decrypt(ct)
	require.ErrorIs(t, err, ErrLocked)
}

func TestAgentIdleTTL(t *testing.T) {
	srv, c := startServer(t)
	now := time.Unix(1000, 0)
	advance := func(d time.Duration) { // guarded: the server reads now under mu
		srv.mu.Lock()
		now = now.Add(d)
		srv.mu.Unlock()
	}
	srv.mu.Lock()
	srv.now = func() time.Time { return now }
	srv.mu.Unlock()

	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	require.NoError(t, c.Load(id.String(), time.Minute))

	advance(30 * time.Second)
	locked, err := c.Status()
	require.NoError(t, err)
	require.False(t, locked)

	advance(2 * time.Minute)
	locked, err = c.Status()
	require.NoError(t, err)
	require.True(t, locked, "key expires after idle TTL")
}

func TestClientWithoutAgent(t *testing.T) {
	_, err := NewClient(shortSocket(t)).Status()
	require.Error(t, err)
}
```

**`internal/keysource/keysource_test.go`**

```go
//go:build unix

package keysource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/vault"
)

type fakeStore struct {
	secret string
	slow   bool
	gets   int
}

func (f *fakeStore) Name() string         { return "fake" }
func (f *fakeStore) Get() (string, error) { f.gets++; return f.secret, nil }
func (f *fakeStore) Set(string) error     { return nil }
func (f *fakeStore) Slow() bool           { return f.slow }
func (f *fakeStore) TTL() time.Duration   { return 0 }

func socket(t *testing.T) string {
	dir, err := os.MkdirTemp("", "wk")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func runAgent(t *testing.T, sock string) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = agent.NewServer().Serve(ctx, sock) }()
	t.Cleanup(cancel)
	require.Eventually(t, func() bool { _, err := agent.NewClient(sock).Status(); return err == nil }, 2*time.Second, 10*time.Millisecond)
}

func TestFastStoreNoAgent(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	st := &fakeStore{secret: id.String()}
	d, err := Decrypter(socket(t), st, "")
	require.NoError(t, err)
	require.IsType(t, &vault.IdentityDecrypter{}, d)
}

func TestSlowStoreLoadsRunningAgent(t *testing.T) {
	sock := socket(t)
	runAgent(t, sock)
	id, _ := age.GenerateX25519Identity()
	st := &fakeStore{secret: id.String(), slow: true}

	d, err := Decrypter(sock, st, "")
	require.NoError(t, err)
	require.IsType(t, &vault.IdentityDecrypter{}, d)
	require.Equal(t, 1, st.gets)

	d, err = Decrypter(sock, st, "")
	require.NoError(t, err)
	require.IsType(t, &agent.Client{}, d, "second call goes through the unlocked agent")
	require.Equal(t, 1, st.gets, "store not read again")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/agent/ ./internal/keysource/
```

Expected: FAIL — `undefined: NewServer`, `undefined: NewClient`, `undefined: Decrypter`.

- [ ] **Step 4: Write the implementation**

**`internal/agent/protocol.go`**

```go
// Package agent caches the vault master key in a background process
// (ssh-agent model): clients send ciphertext, the agent returns plaintext,
// and the key never leaves the agent.
package agent

import "errors"

// ErrLocked means the agent holds no key.
var ErrLocked = errors.New("agent is locked")

// errUnsupported is returned on platforms without the agent.
var errUnsupported = errors.New("whoas agent is not supported on this platform")

// request is one JSON line from client to agent.
type request struct {
	Op   string `json:"op"`             // "status" | "load" | "lock" | "decrypt"
	Data []byte `json:"data,omitempty"` // decrypt: ciphertext
	Key  string `json:"key,omitempty"`  // load: AGE-SECRET-KEY-1...
	TTL  int64  `json:"ttl,omitempty"`  // load: idle TTL in seconds, 0 = none
}

// response is the agent's single JSON reply.
type response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Data   []byte `json:"data,omitempty"`
	Locked bool   `json:"locked,omitempty"`
}
```

**`internal/agent/server_unix.go`**

```go
//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/awnumar/memguard"

	"github.com/S-kkipie/whoas/internal/vault"
)

// Server holds the key in locked memory and answers clients.
type Server struct {
	mu      sync.Mutex
	key     *memguard.Enclave
	ttl     time.Duration
	lastUse time.Time
	now     func() time.Time
}

// NewServer returns a locked server.
func NewServer() *Server { return &Server{now: time.Now} }

// Serve listens on socketPath (0600) until ctx is done.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(conn.(*net.UnixConn))
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := checkPeer(conn); err != nil {
		return
	}
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(s.do(req))
}

func (s *Server) do(req request) response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil && s.ttl > 0 && s.now().Sub(s.lastUse) > s.ttl {
		s.key = nil
	}
	switch req.Op {
	case "status":
		return response{OK: true, Locked: s.key == nil}
	case "lock":
		s.key = nil
		return response{OK: true, Locked: true}
	case "load":
		if _, err := age.ParseX25519Identity(req.Key); err != nil {
			return response{Error: "invalid key"}
		}
		s.key = memguard.NewEnclave([]byte(req.Key))
		s.ttl = time.Duration(req.TTL) * time.Second
		s.lastUse = s.now()
		return response{OK: true}
	case "decrypt":
		if s.key == nil {
			return response{Error: ErrLocked.Error(), Locked: true}
		}
		buf, err := s.key.Open()
		if err != nil {
			return response{Error: err.Error()}
		}
		d, err := vault.NewIdentityDecrypter(string(buf.Bytes()))
		buf.Destroy()
		if err != nil {
			return response{Error: err.Error()}
		}
		plain, err := d.Decrypt(req.Data)
		if err != nil {
			return response{Error: err.Error()}
		}
		s.lastUse = s.now()
		return response{OK: true, Data: plain}
	}
	return response{Error: "unknown op " + req.Op}
}
```

**`internal/agent/client_unix.go`**

```go
//go:build unix

package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"time"
)

// Client talks to the agent socket. It implements vault.Decrypter.
type Client struct {
	socket  string
	timeout time.Duration
}

// NewClient returns a client for socket.
func NewClient(socket string) *Client { return &Client{socket: socket, timeout: 5 * time.Second} }

func (c *Client) call(req request) (response, error) {
	conn, err := net.DialTimeout("unix", c.socket, c.timeout)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, err
	}
	if !resp.OK {
		if resp.Locked {
			return resp, ErrLocked
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Status reports whether the agent is locked; an error means it is not running.
func (c *Client) Status() (locked bool, err error) {
	resp, err := c.call(request{Op: "status"})
	return resp.Locked, err
}

// Load gives the agent the key with an idle TTL (0 = none).
func (c *Client) Load(key string, ttl time.Duration) error {
	_, err := c.call(request{Op: "load", Key: key, TTL: int64(ttl / time.Second)})
	return err
}

// Lock makes the agent forget the key.
func (c *Client) Lock() error {
	_, err := c.call(request{Op: "lock"})
	return err
}

// Decrypt asks the agent to decrypt an age ciphertext.
func (c *Client) Decrypt(ct []byte) ([]byte, error) {
	resp, err := c.call(request{Op: "decrypt", Data: ct})
	return resp.Data, err
}

// Spawn starts "<self> agent" detached and waits until it answers.
func Spawn(self, socket string) error {
	cmd := exec.Command(self, "agent")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	c := NewClient(socket)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := c.Status(); err == nil {
			return nil
		}
	}
	return errors.New("agent did not start")
}
```

**`internal/agent/peer_linux.go`**

```go
//go:build linux

package agent

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeer rejects connections from other users (SO_PEERCRED).
func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d rejected", cred.Uid)
	}
	return nil
}
```

**`internal/agent/peer_darwin.go`**

```go
//go:build darwin

package agent

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeer rejects connections from other users (LOCAL_PEERCRED).
func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d rejected", cred.Uid)
	}
	return nil
}
```

**`internal/agent/peer_other.go`**

```go
//go:build unix && !linux && !darwin

package agent

import "net"

// checkPeer relies on the socket's 0600 mode and 0700 parent dir here.
func checkPeer(*net.UnixConn) error { return nil }
```

**`internal/agent/agent_windows.go`**

```go
//go:build windows

package agent

import (
	"context"
	"time"
)

// Server is unavailable on Windows (whoas.exe only serves keystore-helper).
type Server struct{}

func NewServer() *Server                              { return &Server{} }
func (s *Server) Serve(context.Context, string) error { return errUnsupported }

// Client is unavailable on Windows.
type Client struct{}

func NewClient(string) *Client                     { return &Client{} }
func (c *Client) Status() (bool, error)            { return true, errUnsupported }
func (c *Client) Load(string, time.Duration) error { return errUnsupported }
func (c *Client) Lock() error                      { return errUnsupported }
func (c *Client) Decrypt([]byte) ([]byte, error)   { return nil, errUnsupported }
func Spawn(string, string) error                   { return errUnsupported }
```

**`internal/keysource/keysource.go`**

```go
// Package keysource picks how vault blobs get decrypted: through a running,
// unlocked agent, or directly with the master key from the keystore. For
// slow stores (WSL bridge, passphrase) it also starts and loads an agent so
// the next invocation is fast.
package keysource

import (
	"fmt"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/vault"
)

// Decrypter returns the decrypter to use. self is the whoas executable used
// to spawn an agent; "" disables spawning.
func Decrypter(socket string, store keystore.Store, self string) (vault.Decrypter, error) {
	c := agent.NewClient(socket)
	if locked, err := c.Status(); err == nil && !locked {
		return c, nil
	}
	secret, err := store.Get()
	if err != nil {
		return nil, fmt.Errorf("master key (%s): %w", store.Name(), err)
	}
	d, err := vault.NewIdentityDecrypter(secret)
	if err != nil {
		return nil, err
	}
	if store.Slow() {
		if _, err := c.Status(); err != nil && self != "" {
			_ = agent.Spawn(self, socket)
		}
		_ = c.Load(secret, store.TTL()) // best effort: caching only
	}
	return d, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/agent/ ./internal/keysource/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/agent/agent_test.go internal/agent/agent_windows.go internal/agent/client_unix.go internal/agent/peer_darwin.go internal/agent/peer_linux.go internal/agent/peer_other.go internal/agent/protocol.go internal/agent/server_unix.go internal/keysource/keysource.go internal/keysource/keysource_test.go
git commit -m "feat: add key agent and decrypter selection" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Supervisor: materialize, spawn, seal-on-write, recover

**Files:**
- Create: `internal/supervisor/supervisor.go`
- Create: `internal/supervisor/watch.go`
- Create: `internal/supervisor/spawn_unix.go`
- Create: `internal/supervisor/spawn_windows.go`
- Test: `internal/supervisor/supervisor_test.go`

**Interfaces:**
- Consumes: `adapter.Adapter` (Task 7); `envelope` (Task 6); `overlay` (Task 8); `runlock` (Task 9); `vault.Vault`, `vault.Decrypter` (Task 6); `ref.Ref` (Task 3).
- Produces: `supervisor.Plan{Ref ref.Ref; Adapter *adapter.Adapter; Bin string; Args []string; Global, RunBase string; Vault *vault.Vault; Decrypter vault.Decrypter; Env []string; Stdin io.Reader; Stdout, Stderr io.Writer; Debounce, LockTimeout time.Duration}`; `supervisor.Run(ctx context.Context, p supervisor.Plan) (supervisor.Result, error)`; `supervisor.Result{Code int; Signal os.Signal}`; `supervisor.Reraise(os.Signal)`; `supervisor.Recover(ctx context.Context, base, global string, a *adapter.Adapter, r ref.Ref, v *vault.Vault) (bool, error)`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/fsnotify/fsnotify@v1.10.1
```

- [ ] **Step 2: Write the failing tests**

**`internal/supervisor/supervisor_test.go`**

```go
//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

type fixture struct {
	plan   Plan
	global string
	dec    vault.Decrypter
}

func newFixture(t *testing.T, script string) *fixture {
	t.Helper()
	root := t.TempDir()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipient := filepath.Join(root, "recipient")
	require.NoError(t, os.WriteFile(recipient, []byte(id.Recipient().String()), 0o644))
	v, err := vault.Open(recipient, filepath.Join(root, "vault"))
	require.NoError(t, err)
	dec, err := vault.NewIdentityDecrypter(id.String())
	require.NoError(t, err)

	global := filepath.Join(root, "global")
	require.NoError(t, os.MkdirAll(global, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(global, "auth.json"), []byte("global"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(global, "shared.txt"), []byte("shared"), 0o600))

	a := &adapter.Adapter{
		Name: "fake", Bin: "sh", Kind: adapter.KindDirOverlay,
		Dir: adapter.DirSpec{
			Global:    global,
			Inject:    adapter.Inject{Env: map[string]string{"FAKE_DIR": "{dir}"}},
			AuthFiles: []string{"auth.json"},
		},
	}
	r := ref.Ref{Provider: "fake", Alias: "work"}
	require.NoError(t, v.Seal(r, envelope.New("fake", map[string]envelope.File{"auth.json": {Mode: 0o600, Data: []byte("old")}})))

	return &fixture{
		global: global,
		dec:    dec,
		plan: Plan{
			Ref: r, Adapter: a, Bin: "/bin/sh", Args: []string{"-c", script},
			Global: global, RunBase: filepath.Join(root, "run", "fake", "work"),
			Vault: v, Decrypter: dec, Env: os.Environ(),
			Debounce: 50 * time.Millisecond,
		},
	}
}

func (f *fixture) vaultAuth(t *testing.T) string {
	t.Helper()
	env, err := f.plan.Vault.Load(f.plan.Ref, f.dec)
	require.NoError(t, err)
	return string(env.Files["auth.json"].Data)
}

func run(t *testing.T, p Plan) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	p.Stdout = &out
	res, err := Run(context.Background(), p)
	require.NoError(t, err)
	return res, out.String()
}

func TestRunSealsAndCleansUp(t *testing.T) {
	f := newFixture(t, `cat "$FAKE_DIR/auth.json"; cat "$FAKE_DIR/shared.txt"; printf new > "$FAKE_DIR/auth.json"; exit 7`)
	res, out := run(t, f.plan)
	require.Equal(t, 7, res.Code)
	require.Equal(t, "oldshared", out)
	require.Equal(t, "new", f.vaultAuth(t))

	_, err := os.Stat(f.plan.RunBase)
	require.True(t, os.IsNotExist(err), "overlay removed")
	_, err = os.Stat(f.plan.RunBase + ".state.json")
	require.True(t, os.IsNotExist(err), "state removed")
	g, _ := os.ReadFile(filepath.Join(f.global, "auth.json"))
	require.Equal(t, "global", string(g), "global auth untouched")
}

func TestSealOnWriteDuringRun(t *testing.T) {
	f := newFixture(t, `printf v2 > "$FAKE_DIR/auth.json"; sleep 1`)
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), f.plan)
		done <- err
	}()
	require.Eventually(t, func() bool { return f.vaultAuth(t) == "v2" }, 900*time.Millisecond, 20*time.Millisecond,
		"sealed while the CLI is still running")
	require.NoError(t, <-done)
}

func TestRecoversCrashedOverlay(t *testing.T) {
	f := newFixture(t, `cat "$FAKE_DIR/auth.json"`)
	// Simulate a crash: overlay with a refreshed token and a dead holder.
	require.NoError(t, os.MkdirAll(f.plan.RunBase, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "auth.json"), []byte("crashed"), 0o600))
	l, err := runlock.Acquire(context.Background(), f.plan.RunBase, time.Second)
	require.NoError(t, err)
	require.NoError(t, l.Write(runlock.State{Holders: []int{999999}, Hash: "stale"}))
	require.NoError(t, l.Release())

	_, out := run(t, f.plan)
	require.Equal(t, "crashed", out, "refreshed token from the crashed run is kept")
}

func TestSignalResult(t *testing.T) {
	f := newFixture(t, `kill -TERM $$`)
	res, _ := run(t, f.plan)
	require.Equal(t, syscall.SIGTERM, res.Signal)
	require.Equal(t, 128+int(syscall.SIGTERM), res.Code)
}

func TestForwardsSIGTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	f := newFixture(t, `trap 'echo got-term; exit 5' TERM; touch "`+marker+`"; while :; do sleep 0.05; done`)
	var out bytes.Buffer
	f.plan.Stdout = &out
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(context.Background(), f.plan)
		done <- outcome{res, err}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	o := <-done
	require.NoError(t, o.err)
	require.Equal(t, 5, o.res.Code)
	require.Contains(t, out.String(), "got-term")
}

func TestPromotesNewSharedState(t *testing.T) {
	f := newFixture(t, `mkdir -p "$FAKE_DIR/settings" && printf dark > "$FAKE_DIR/settings/theme"`)
	run(t, f.plan)
	got, err := os.ReadFile(filepath.Join(f.global, "settings", "theme"))
	require.NoError(t, err)
	require.Equal(t, "dark", strings.TrimSpace(string(got)))
}

func TestRecoverHelper(t *testing.T) {
	f := newFixture(t, `true`)
	require.NoError(t, os.MkdirAll(f.plan.RunBase, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "auth.json"), []byte("orphan"), 0o600))
	found, err := Recover(context.Background(), f.plan.RunBase, f.global, f.plan.Adapter, f.plan.Ref, f.plan.Vault)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "orphan", f.vaultAuth(t))
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/supervisor/
```

Expected: FAIL — `undefined: Plan`, `undefined: Run`.

- [ ] **Step 4: Write the implementation**

**`internal/supervisor/supervisor.go`**

```go
// Package supervisor runs one CLI invocation as an identity: it joins (or
// materializes) the identity's overlay under a lock, spawns the real binary
// pointed at the overlay, seals refreshed auth files back into the vault as
// they change, and tears the overlay down when the last user exits.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

// Plan is everything one supervised run needs.
type Plan struct {
	Ref         ref.Ref
	Adapter     *adapter.Adapter
	Bin         string   // absolute path of the real CLI
	Args        []string // user args, passed verbatim after injected flags
	Global      string   // the CLI's real config dir
	RunBase     string   // overlay dir; .lock/.state.json sit next to it
	Vault       *vault.Vault
	Decrypter   vault.Decrypter
	Env         []string // base environment for the child
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Debounce    time.Duration // seal-on-write debounce; 0 = 200ms
	LockTimeout time.Duration // 0 = 10s
}

// Result is how the child ended.
type Result struct {
	Code   int
	Signal os.Signal // set when the child died from a signal
}

func (p *Plan) defaults() {
	if p.Debounce == 0 {
		p.Debounce = 200 * time.Millisecond
	}
	if p.LockTimeout == 0 {
		p.LockTimeout = 10 * time.Second
	}
	if p.Stderr == nil {
		p.Stderr = io.Discard
	}
}

// Run executes the plan and returns the child's result.
func Run(ctx context.Context, p Plan) (Result, error) {
	p.defaults()
	if err := p.join(ctx); err != nil {
		return Result{}, err
	}
	stop, err := p.watch(ctx)
	if err != nil {
		_ = p.leave(ctx)
		return Result{}, err
	}
	res, runErr := p.spawn()
	stop()
	if err := p.leave(ctx); err != nil && runErr == nil {
		runErr = err
	}
	return res, runErr
}

// join registers this process as a holder, materializing the overlay if it
// is the first one (recovering a crashed overlay first).
func (p *Plan) join(ctx context.Context) error {
	l, err := runlock.Acquire(ctx, p.RunBase, p.LockTimeout)
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return err
	}
	st.Prune()
	if len(st.Holders) == 0 {
		if _, err := recoverStale(p.RunBase, p.Global, p.Adapter, p.Ref, p.Vault, st.Hash); err != nil {
			return err
		}
		env, err := p.Vault.Load(p.Ref, p.Decrypter)
		if err != nil {
			return err
		}
		if err := overlay.Build(p.RunBase, p.Global, p.Adapter.Dir.AuthFiles, env.Files); err != nil {
			_ = overlay.Remove(p.RunBase)
			return err
		}
		st.Hash = envelope.Hash(env.Files)
	}
	st.Add(os.Getpid())
	return l.Write(st)
}

// leave seals final changes and removes the overlay if no holder remains.
func (p *Plan) leave(ctx context.Context) error {
	l, err := runlock.Acquire(ctx, p.RunBase, p.LockTimeout)
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return err
	}
	if st.Hash, err = sealIfChanged(p.RunBase, p.Adapter, p.Ref, p.Vault, st.Hash); err != nil {
		return err
	}
	st.Remove(os.Getpid())
	st.Prune()
	if len(st.Holders) == 0 {
		if err := overlay.Promote(p.RunBase, p.Global, p.Adapter.Dir.AuthFiles); err != nil {
			return err
		}
		if err := overlay.Remove(p.RunBase); err != nil {
			return err
		}
		st.Hash = ""
	}
	return l.Write(st)
}

// sealIfChanged seals the overlay's auth files when their hash differs from
// prev and returns the current hash. An empty overlay (logged out) is not sealed.
func sealIfChanged(dir string, a *adapter.Adapter, r ref.Ref, v *vault.Vault, prev string) (string, error) {
	files, err := overlay.Collect(dir, a.Dir.AuthFiles)
	if err != nil || len(files) == 0 {
		return prev, err
	}
	h := envelope.Hash(files)
	if h == prev {
		return prev, nil
	}
	if err := v.Seal(r, envelope.New(r.Provider, files)); err != nil {
		return prev, fmt.Errorf("seal %s: %w", r, err)
	}
	return h, nil
}

// recoverStale seals and removes an overlay left by crashed holders.
func recoverStale(dir, global string, a *adapter.Adapter, r ref.Ref, v *vault.Vault, hash string) (bool, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if _, err := sealIfChanged(dir, a, r, v, hash); err != nil {
		return false, err
	}
	if err := overlay.Promote(dir, global, a.Dir.AuthFiles); err != nil {
		return false, err
	}
	return true, overlay.Remove(dir)
}

// Recover cleans up r's overlay if every holder is dead. It reports whether
// a stale overlay was found.
func Recover(ctx context.Context, base, global string, a *adapter.Adapter, r ref.Ref, v *vault.Vault) (bool, error) {
	l, err := runlock.Acquire(ctx, base, 10*time.Second)
	if err != nil {
		return false, err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return false, err
	}
	st.Prune()
	if len(st.Holders) > 0 {
		return false, nil
	}
	found, err := recoverStale(base, global, a, r, v, st.Hash)
	if err != nil {
		return found, err
	}
	return found, l.Write(runlock.State{})
}
```

**`internal/supervisor/watch.go`**

```go
package supervisor

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/S-kkipie/whoas/internal/runlock"
)

// watch seals auth files shortly after the CLI rewrites them (seal-on-write),
// so refreshed tokens survive crashes. The returned stop is idempotent.
func (p *Plan) watch(ctx context.Context) (func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	for _, d := range p.watchDirs() {
		if err := w.Add(d); err != nil {
			w.Close()
			return nil, err
		}
	}
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	seal := func() {
		l, err := runlock.Acquire(ctx, p.RunBase, p.LockTimeout)
		if err != nil {
			fmt.Fprintln(p.Stderr, "whoas: seal skipped:", err)
			return
		}
		defer l.Release()
		st, err := l.Read()
		if err == nil {
			st.Hash, err = sealIfChanged(p.RunBase, p.Adapter, p.Ref, p.Vault, st.Hash)
		}
		if err == nil && len(st.Holders) > 0 {
			err = l.Write(st)
		}
		if err != nil {
			fmt.Fprintln(p.Stderr, "whoas: seal failed:", err)
		}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if p.isAuthPath(ev.Name) {
					mu.Lock()
					if timer != nil {
						timer.Stop()
					}
					timer = time.AfterFunc(p.Debounce, seal)
					mu.Unlock()
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
			w.Close()
			mu.Lock()
			if timer != nil {
				timer.Stop() // leave() does the final seal
			}
			mu.Unlock()
		})
	}, nil
}

// watchDirs are the overlay root, every ancestor of an auth path, and every
// existing directory inside directory-type auth paths.
func (p *Plan) watchDirs() []string {
	set := map[string]bool{p.RunBase: true}
	for _, a := range p.Adapter.Dir.AuthFiles {
		full := filepath.Join(p.RunBase, filepath.FromSlash(a))
		for d := filepath.Dir(full); strings.HasPrefix(d, p.RunBase); d = filepath.Dir(d) {
			set[d] = true
		}
		_ = filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				set[path] = true
			}
			return nil
		})
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	return out
}

// isAuthPath reports whether an event path is an auth file or inside an auth dir.
func (p *Plan) isAuthPath(name string) bool {
	rel, err := filepath.Rel(p.RunBase, name)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, a := range p.Adapter.Dir.AuthFiles {
		if rel == a || strings.HasPrefix(rel, a+"/") {
			return true
		}
	}
	return false
}
```

**`internal/supervisor/spawn_unix.go`**

```go
//go:build unix

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// spawn runs the real CLI and waits. SIGTERM/SIGHUP are always forwarded.
// SIGINT/SIGQUIT are forwarded only when whoas is not in a terminal's
// foreground process group; otherwise the terminal already delivered them
// to the child and forwarding would double them.
func (p *Plan) spawn() (Result, error) {
	forwardAll := !terminalDelivers()
	argv := append(p.Adapter.InjectArgs(p.RunBase), p.Args...)
	cmd := exec.Command(p.Bin, argv...)
	cmd.Env = append(append([]string{}, p.Env...), p.Adapter.InjectEnv(p.RunBase)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Stdin, p.Stdout, p.Stderr

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", p.Bin, err)
	}
	waited := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if forwardAll || s == syscall.SIGTERM || s == syscall.SIGHUP {
					_ = cmd.Process.Signal(s)
				}
			case <-waited:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(waited)
	return resultOf(err)
}

// terminalDelivers reports whether stdin is a terminal whose foreground
// process group is ours, so Ctrl-C already reaches the child.
func terminalDelivers() bool {
	pgrp, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	return err == nil && pgrp == syscall.Getpgrp()
}

func resultOf(err error) (Result, error) {
	if err == nil {
		return Result{}, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return Result{}, err
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return Result{Code: 128 + int(ws.Signal()), Signal: ws.Signal()}, nil
	}
	return Result{Code: ee.ExitCode()}, nil
}

// Reraise makes this process die from sig so its parent sees the same status.
// It returns only if the signal did not terminate the process.
func Reraise(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Reset(s)
	_ = syscall.Kill(os.Getpid(), s)
	time.Sleep(100 * time.Millisecond)
}
```

**`internal/supervisor/spawn_windows.go`**

```go
//go:build windows

package supervisor

import (
	"errors"
	"os"
)

func (p *Plan) spawn() (Result, error) {
	return Result{}, errors.New("running CLIs is not supported on native Windows yet; use WSL")
}

// Reraise is a no-op on Windows.
func Reraise(os.Signal) {}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/supervisor/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/supervisor/spawn_unix.go internal/supervisor/spawn_windows.go internal/supervisor/supervisor.go internal/supervisor/supervisor_test.go internal/supervisor/watch.go
git commit -m "feat: supervise CLI runs with overlays and seal-on-write" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Project file, trust store, resolver

**Files:**
- Create: `internal/project/project.go`
- Create: `internal/trust/trust.go`
- Create: `internal/resolve/resolve.go`
- Test: `internal/project/project_test.go`
- Test: `internal/trust/trust_test.go`
- Test: `internal/resolve/resolve_test.go`

**Interfaces:**
- Consumes: `ref` (Task 3); `config.Config` (Task 4); `fsutil.WriteFileAtomic` (Task 2).
- Produces: `project.FileName`; `project.Find(start string) (string, error)`; `project.Parse(path string, data []byte) (*project.File, error)`; `project.File{Path, Profile string; Overrides map[string]string}`. `trust.Load(path string) (*trust.Store, error)` with `IsTrusted(file string, data []byte) bool`, `Trust(file string, data []byte)`, `Untrust(file string) bool`, `Save() error`. `resolve.Input{Provider, Alias, ProfileFlag, EnvProfile string; Project *project.File; Config *config.Config}`; `resolve.Resolve(resolve.Input) (resolve.Result, error)`; `resolve.Result{Ref ref.Ref; Source string}`; `resolve.ErrUnresolved`.

- [ ] **Step 1: Write the failing tests**

**`internal/project/project_test.go`**

```go
//go:build unix

package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o700))
	want := filepath.Join(root, "a", FileName)
	require.NoError(t, os.WriteFile(want, []byte("profile: work\n"), 0o600))

	got, err := Find(deep)
	require.NoError(t, err)
	require.Equal(t, want, got)

	got, err = Find(root)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestParse(t *testing.T) {
	f, err := Parse("p", []byte("profile: work\nvercel: client-x\nmcp/posthog: acme\n"))
	require.NoError(t, err)
	require.Equal(t, "work", f.Profile)
	require.Equal(t, map[string]string{"vercel": "client-x", "mcp/posthog": "acme"}, f.Overrides)

	empty, err := Parse("p", []byte(""))
	require.NoError(t, err)
	require.Empty(t, empty.Profile)
}

func TestParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"list value":  "profile: [work]\n",
		"map value":   "vercel: {cmd: rm}\n",
		"bad key":     "Vercel: x\n",
		"bad alias":   "vercel: ../x\n",
		"reserved":    "run: x\n",
		"broken yaml": "profile: [\n",
	} {
		_, err := Parse("p", []byte(body))
		require.Error(t, err, name)
	}
}
```

**`internal/trust/trust_test.go`**

```go
//go:build unix

package trust

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustLifecycle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "trust.json")
	s, err := Load(db)
	require.NoError(t, err)

	file := "/p/.whoas.yaml"
	body := []byte("profile: work\n")
	require.False(t, s.IsTrusted(file, body))
	s.Trust(file, body)
	require.NoError(t, s.Save())

	s2, err := Load(db)
	require.NoError(t, err)
	require.True(t, s2.IsTrusted(file, body))
	require.False(t, s2.IsTrusted(file, []byte("profile: personal\n")), "edits revoke trust")
	require.True(t, s2.Untrust(file))
	require.False(t, s2.Untrust(file))
	require.False(t, s2.IsTrusted(file, body))
}
```

**`internal/resolve/resolve_test.go`**

```go
package resolve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/config"
	"github.com/S-kkipie/whoas/internal/project"
)

func cfg() *config.Config {
	return &config.Config{
		DefaultProfile: "personal",
		Profiles: map[string]map[string]string{
			"work":     {"vercel": "work", "gh": "corp"},
			"personal": {"vercel": "me"},
		},
	}
}

func TestPrecedence(t *testing.T) {
	proj := &project.File{Path: "/p/.whoas.yaml", Profile: "work", Overrides: map[string]string{"vercel": "client-x"}}
	cases := []struct {
		name   string
		in     Input
		alias  string
		source string
	}{
		{"explicit wins", Input{Alias: "x", ProfileFlag: "work", Project: proj}, "x", "explicit"},
		{"flag beats env and project", Input{ProfileFlag: "work", EnvProfile: "personal", Project: proj}, "work", "-p flag (profile work)"},
		{"env beats project", Input{EnvProfile: "personal", Project: proj}, "me", "WHOAS_PROFILE (profile personal)"},
		{"project override beats project profile", Input{Project: proj}, "client-x", "project /p/.whoas.yaml"},
		{"default profile last", Input{}, "me", "default profile (profile personal)"},
	}
	for _, c := range cases {
		c.in.Provider, c.in.Config = "vercel", cfg()
		res, err := Resolve(c.in)
		require.NoError(t, err, c.name)
		require.Equal(t, c.alias, res.Ref.Alias, c.name)
		require.Equal(t, c.source, res.Source, c.name)
	}
}

func TestNoFallthrough(t *testing.T) {
	// -p work has no supabase identity: unresolved, even though nothing else is set.
	_, err := Resolve(Input{Provider: "supabase", ProfileFlag: "work", Config: cfg()})
	require.ErrorIs(t, err, ErrUnresolved)

	proj := &project.File{Path: "/p/.whoas.yaml", Profile: "work"}
	res, err := Resolve(Input{Provider: "gh", Project: proj, Config: cfg()})
	require.NoError(t, err)
	require.Equal(t, "corp", res.Ref.Alias)
}

func TestUnknownProfileIsHardError(t *testing.T) {
	_, err := Resolve(Input{Provider: "vercel", EnvProfile: "typo", Config: cfg()})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnresolved)
}

func TestNothingConfigured(t *testing.T) {
	_, err := Resolve(Input{Provider: "vercel", Config: &config.Config{}})
	require.ErrorIs(t, err, ErrUnresolved)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/project/ ./internal/trust/ ./internal/resolve/
```

Expected: FAIL — `undefined: Find`, `undefined: Load`, `undefined: Resolve`.

- [ ] **Step 3: Write the implementation**

**`internal/project/project.go`**

```go
// Package project finds and parses .whoas.yaml. The schema is a whitelist:
// "profile" plus provider: alias pairs, all strings. It can never declare
// adapters, commands, paths or env, so a project file cannot run code.
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/S-kkipie/whoas/internal/ref"
)

// FileName is the project file name.
const FileName = ".whoas.yaml"

// File is a parsed project file.
type File struct {
	Path      string
	Profile   string
	Overrides map[string]string // provider → alias
}

// Find walks up from start and returns the nearest project file, or "".
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, FileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Parse validates data against the whitelist schema.
func Parse(path string, data []byte) (*File, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	f := &File{Path: path, Overrides: map[string]string{}}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: %q must be a string", path, k)
		}
		if err := ref.ValidAlias(s); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, k, err)
		}
		if k == "profile" {
			f.Profile = s
			continue
		}
		if err := ref.ValidProvider(k); err != nil {
			return nil, fmt.Errorf("%s: invalid key: %w", path, err)
		}
		f.Overrides[k] = s
	}
	return f, nil
}
```

**`internal/trust/trust.go`**

```go
// Package trust records which project files the user approved, by absolute
// path and content hash (direnv model). Any edit revokes trust.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/S-kkipie/whoas/internal/fsutil"
)

// Store is the trust database.
type Store struct {
	path    string
	entries map[string]string // absolute file path → sha256 hex
}

// Load reads path; a missing file is an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, &s.entries)
}

// Hash is the sha256 of data in hex.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// IsTrusted reports whether file was trusted with exactly this content.
func (s *Store) IsTrusted(file string, data []byte) bool { return s.entries[file] == Hash(data) }

// Trust approves file with its current content.
func (s *Store) Trust(file string, data []byte) { s.entries[file] = Hash(data) }

// Untrust forgets file; it reports whether it was trusted.
func (s *Store) Untrust(file string) bool {
	_, ok := s.entries[file]
	delete(s.entries, file)
	return ok
}

// Save writes the store (0600).
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600)
}
```

**`internal/resolve/resolve.go`**

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/project/ ./internal/trust/ ./internal/resolve/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/project/project.go internal/project/project_test.go internal/resolve/resolve.go internal/resolve/resolve_test.go internal/trust/trust.go internal/trust/trust_test.go
git commit -m "feat: resolve identities from flags, env, trusted projects, defaults" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Commands: add, import, ls, rm

**Files:**
- Create: `internal/binpath/binpath.go`
- Create: `internal/app/env.go`
- Create: `internal/app/capture.go`
- Create: `internal/app/add.go`
- Create: `internal/app/import.go`
- Create: `internal/app/ls.go`
- Test: `internal/binpath/binpath_test.go`
- Test: `internal/app/capture_test.go`
- Test: `tests/testdata/add.txtar`

**Interfaces:**
- Consumes: Everything from Tasks 2–10 (paths, config, adapter, keystore, vault, keysource, overlay, runlock).
- Produces: `binpath.Find(name, pathList, exclude, self string) (string, error)`. In package app: `type env struct{dirs paths.Dirs; cfg *config.Config; registry *adapter.Registry}`, `loadEnv() (*env, error)`, `(*env).vars() adapter.Vars`, `adapter(provider string) (*adapter.Adapter, error)`, `store() (keystore.Store, error)`, `openVault()`, `initVault() (*vault.Vault, error)`, `decrypter() (vault.Decrypter, error)`, `realBin(name string) (string, error)`; `self() string`; `runIn(a, bin, dir, args, stdin, stdout, stderr) error`; `whoami(a, bin, dir string) string`; `parseAccount(out []byte, jsonPath string) string`; `accountSuffix(account string) string`; `save(w, errw io.Writer, v *vault.Vault, r ref.Ref, files map[string]envelope.File, account string) error`. Commands: `whoas add|import <provider> <alias>`, `whoas ls [provider]`, `whoas rm <provider@alias>`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/tidwall/gjson@v1.20.0
```

- [ ] **Step 2: Write the failing tests**

**`internal/binpath/binpath_test.go`**

```go
//go:build unix

package binpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func exe(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
}

func TestFindSkipsShimsAndSelf(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(root, "bin", "whoas")
	exe(t, self)
	shims := filepath.Join(root, "shims")
	require.NoError(t, os.MkdirAll(shims, 0o700))
	require.NoError(t, os.Symlink(self, filepath.Join(shims, "gh")))
	other := filepath.Join(root, "other")
	require.NoError(t, os.MkdirAll(other, 0o700))
	require.NoError(t, os.Symlink(self, filepath.Join(other, "gh"))) // stray link to whoas
	real := filepath.Join(root, "usr", "gh")
	exe(t, real)
	require.NoError(t, os.WriteFile(filepath.Join(root, "noexec-gh"), []byte("x"), 0o644))

	path := strings.Join([]string{shims, other, filepath.Join(root, "usr")}, string(os.PathListSeparator))
	got, err := Find("gh", path, shims, self)
	require.NoError(t, err)
	require.Equal(t, real, got)

	got, err = Find("gh", path, "", "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(shims, "gh"), got, "no exclusions finds the shim")

	_, err = Find("nope", path, shims, self)
	require.ErrorContains(t, err, "not found in PATH")
}
```

**`internal/app/capture_test.go`**

```go
package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAccount(t *testing.T) {
	require.Equal(t, "octocat", parseAccount([]byte(`{"login":"octocat","id":1}`), "login"))
	require.Equal(t, "a@b.c", parseAccount([]byte("\n  a@b.c  \nmore\n"), ""))
	require.Equal(t, "", parseAccount([]byte(`{}`), "email"))
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	require.Len(t, parseAccount(long, ""), 80)
}
```

**`tests/testdata/add.txtar`**

```text
# AC-01: add captures logins into the vault; the global session is never touched.
env FAKECLI_LOGIN_AS=global
exec fakecli login
snapshot $HOME/.fakecli/auth.json g

exec whoas ls
stdout 'no identities yet'

env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work
stdout 'saved fakecli@work \(account=work\)'
stderr 'hint: fake hint'
env FAKECLI_LOGIN_AS=personal
exec whoas add fakecli personal

exec whoas ls
stdout 'fakecli@personal\s+account=personal'
stdout 'fakecli@work\s+account=work'
! stdout 'tok-'
unchanged $HOME/.fakecli/auth.json g

# a saved alias is never overwritten
! exec whoas add fakecli work
stderr 'already exists'

# unknown provider
! exec whoas add nope x
stderr 'unknown provider "nope"'

# import seals the current global session without re-login
exec whoas import fakecli main
stdout 'saved fakecli@main \(account=global\)'
unchanged $HOME/.fakecli/auth.json g

# same account captured twice -> warning
env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work2
stderr 'warning: fakecli@work2 has the same account as fakecli@work'

exec whoas rm fakecli@work2
stdout 'removed fakecli@work2'
! exec whoas rm fakecli@work2
stderr 'identity not found'
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/binpath/ ./internal/app/ ./tests/
```

Expected: FAIL — `undefined: Find`, `undefined: parseAccount`; add.txtar fails with `unknown command "add"`.

- [ ] **Step 4: Write the implementation**

**`internal/binpath/binpath.go`**

```go
// Package binpath finds the real CLI binary on PATH, skipping whoas shims.
package binpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Find looks name up in pathList. Directories equal to exclude are skipped,
// as are candidates that resolve to self (the whoas binary). Empty exclude or
// self disables that check.
func Find(name, pathList, exclude, self string) (string, error) {
	ex := canon(exclude)
	me := canon(self)
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		if ex != "" && canon(dir) == ex {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if me != "" && canon(p) == me {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

func canon(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}
```

**`internal/app/env.go`**

```go
package app

import (
	"fmt"
	"os"

	"github.com/S-kkipie/whoas/internal/adapter"
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
	return vault.Init(e.dirs.RecipientFile(), e.dirs.VaultDir(), st)
}

func (e *env) decrypter() (vault.Decrypter, error) {
	st, err := e.store()
	if err != nil {
		return nil, err
	}
	return keysource.Decrypter(e.dirs.AgentSocket(), st, self())
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
```

**`internal/app/capture.go`**

```go
package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/vault"
)

// runIn runs bin with the adapter's injection pointing at dir.
func runIn(a *adapter.Adapter, bin, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	c := exec.Command(bin, append(a.InjectArgs(dir), args...)...)
	c.Env = append(os.Environ(), a.InjectEnv(dir)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return c.Run()
}

// whoami returns the account label, or "" when unknown.
func whoami(a *adapter.Adapter, bin, dir string) string {
	if len(a.Whoami.Cmd) == 0 {
		return ""
	}
	var out bytes.Buffer
	if err := runIn(a, bin, dir, a.Whoami.Cmd[1:], nil, &out, io.Discard); err != nil {
		return ""
	}
	return parseAccount(out.Bytes(), a.Whoami.JSON)
}

func parseAccount(out []byte, jsonPath string) string {
	s := ""
	if jsonPath != "" {
		s = gjson.GetBytes(out, jsonPath).String()
	} else {
		for _, line := range strings.Split(string(out), "\n") {
			if s = strings.TrimSpace(line); s != "" {
				break
			}
		}
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func accountSuffix(account string) string {
	if account == "" {
		return ""
	}
	return " (" + account + ")"
}

// save seals files as r, records metadata and warns about duplicate accounts.
func save(w io.Writer, errw io.Writer, v *vault.Vault, r ref.Ref, files map[string]envelope.File, account string) error {
	if err := v.Seal(r, envelope.New(r.Provider, files)); err != nil {
		return err
	}
	if err := v.WriteMeta(r, vault.Meta{Account: account, Created: time.Now().UTC()}); err != nil {
		return err
	}
	if account != "" {
		entries, err := v.List()
		if err != nil {
			return err
		}
		for _, en := range entries {
			if en.Ref.Provider == r.Provider && en.Ref != r && en.Meta.Account == account {
				fmt.Fprintf(errw, "warning: %s has the same account as %s (did the browser reuse a signed-in session?)\n", r, en.Ref)
			}
		}
	}
	fmt.Fprintf(w, "saved %s%s\n", r, accountSuffix(account))
	return nil
}
```

**`internal/app/add.go`**

```go
package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newAddCmd) }

func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <provider> <alias>",
		Short: "Log in through the CLI and save the session as provider@alias",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			r, err := ref.Parse(args[0] + "@" + args[1])
			if err != nil {
				return err
			}
			a, err := e.adapter(r.Provider)
			if err != nil {
				return err
			}
			v, err := e.initVault()
			if err != nil {
				return err
			}
			if v.Exists(r) {
				return fmt.Errorf("%s already exists (whoas rm %s first)", r, r)
			}
			bin, err := e.realBin(a.Bin)
			if err != nil {
				return err
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			dir := e.dirs.CaptureDir(r.Provider, r.Alias)
			defer overlay.Remove(dir)
			if err := overlay.Build(dir, a.GlobalDir(e.vars()), a.Dir.AuthFiles, nil); err != nil {
				return err
			}
			if a.Login.Hint != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "hint:", a.Login.Hint)
			}
			if err := runIn(a, bin, dir, a.Login.Cmd[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			files, err := overlay.Collect(dir, a.Dir.AuthFiles)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("login produced none of %v; nothing saved", a.Dir.AuthFiles)
			}
			return save(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, files, whoami(a, bin, dir))
		},
	}
}
```

**`internal/app/import.go`**

```go
package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newImportCmd) }

func newImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <provider> <alias>",
		Short: "Save the CLI's current global session as provider@alias (no re-login)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			r, err := ref.Parse(args[0] + "@" + args[1])
			if err != nil {
				return err
			}
			a, err := e.adapter(r.Provider)
			if err != nil {
				return err
			}
			v, err := e.initVault()
			if err != nil {
				return err
			}
			if v.Exists(r) {
				return fmt.Errorf("%s already exists (whoas rm %s first)", r, r)
			}
			global := a.GlobalDir(e.vars())
			files, err := overlay.Collect(global, a.Dir.AuthFiles)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("no session found in %s (expected %v)", global, a.Dir.AuthFiles)
			}
			account := ""
			if bin, err := e.realBin(a.Bin); err == nil {
				account = whoami(a, bin, global)
			}
			return save(cmd.OutOrStdout(), cmd.ErrOrStderr(), v, r, files, account)
		},
	}
}
```

**`internal/app/ls.go`**

```go
package app

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

func init() {
	register(newLsCmd)
	register(newRmCmd)
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls [provider]",
		Short: "List saved identities (never prints secrets)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			v, err := e.openVault()
			if errors.Is(err, vault.ErrNotInitialized) {
				fmt.Fprintln(cmd.OutOrStdout(), "no identities yet (whoas add <provider> <alias>)")
				return nil
			}
			if err != nil {
				return err
			}
			entries, err := v.List()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "IDENTITY\tACCOUNT\tACTIVE")
			for _, en := range entries {
				if len(args) == 1 && en.Ref.Provider != args[0] {
					continue
				}
				st, _ := runlock.Peek(e.dirs.RunBase(en.Ref.Provider, en.Ref.Alias))
				active := "-"
				if n := len(st.Live()); n > 0 {
					active = fmt.Sprintf("%d process(es)", n)
				}
				account := en.Meta.Account
				if account == "" {
					account = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", en.Ref, account, active)
			}
			return tw.Flush()
		},
	}
}

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <provider@alias>",
		Short: "Delete a saved identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			r, err := ref.Parse(args[0])
			if err != nil {
				return err
			}
			v, err := e.openVault()
			if err != nil {
				return err
			}
			st, _ := runlock.Peek(e.dirs.RunBase(r.Provider, r.Alias))
			if live := st.Live(); len(live) > 0 {
				return fmt.Errorf("%s is in use by pid %v", r, live)
			}
			if err := v.Remove(r); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", r)
			for _, name := range e.cfg.ProfileNames() {
				if b, _ := e.cfg.Profile(name); b[r.Provider] == r.Alias {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: profile %s still maps %s\n", name, r)
				}
			}
			return nil
		},
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/binpath/ ./internal/app/ ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/app/add.go internal/app/capture.go internal/app/capture_test.go internal/app/env.go internal/app/import.go internal/app/ls.go internal/binpath/binpath.go internal/binpath/binpath_test.go tests/testdata/add.txtar
git commit -m "feat: add, import, list and remove identities" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: Run as an identity: dispatch, shims, exit codes

**Files:**
- Modify (full replacement): `internal/app/app.go`
- Create: `internal/app/exec.go`
- Create: `internal/app/shim.go`
- Create: `internal/app/execreplace_unix.go`
- Create: `internal/app/execreplace_windows.go`
- Test: `internal/app/app_test.go`
- Test: `tests/testdata/exec.txtar`

**Interfaces:**
- Consumes: `supervisor.Run`, `supervisor.Reraise` (Task 11); `resolve`, `project`, `trust` (Task 12); app `env` helpers (Task 13).
- Produces: `Main` now dispatches: argv[0] ≠ whoas → `shimMain(bin string, args []string) int`; `whoas [-p P] provider[@alias] args…` → `execMain(profileFlag, token string, args []string) int`; else cobra. `leadingProfile(args []string) (string, []string, error)`; `type runRequest struct{provider, alias, profileFlag string; args []string; explicit bool}`; `runProvider(e *env, req runRequest) (int, error)`; `(*env).resolveIdentity(runRequest) (resolve.Result, error)`; `(*env).trustedProject(warnOnly bool) (*project.File, error)`; `errPassthrough`; `banner(r ref.Ref, account string)`; `execReplace(path string, argv, env []string) int`; constants `exitWhoasError=125`, `exitExecFailed=126`, `exitNotFound=127`.

- [ ] **Step 1: Write the failing tests**

**`internal/app/app_test.go`**

```go
package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLeadingProfile(t *testing.T) {
	cases := []struct {
		in      []string
		profile string
		rest    []string
		err     bool
	}{
		{[]string{"gh", "repo", "list"}, "", []string{"gh", "repo", "list"}, false},
		{[]string{"-p", "work", "gh", "-p", "x"}, "work", []string{"gh", "-p", "x"}, false},
		{[]string{"--profile", "work", "gh"}, "work", []string{"gh"}, false},
		{[]string{"--profile=work", "gh"}, "work", []string{"gh"}, false},
		{[]string{"-p"}, "", nil, true},
		{nil, "", nil, false},
	}
	for _, c := range cases {
		p, rest, err := leadingProfile(c.in)
		if c.err {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, c.profile, p)
		require.Equal(t, c.rest, rest)
	}
}
```

**`tests/testdata/exec.txtar`**

```text
# Running CLIs as an identity: AC-02, AC-03, AC-05, AC-07, seal-on-write,
# signals, crash recovery, and the global session staying untouched.
env FAKECLI_LOGIN_AS=global
exec fakecli login
snapshot $HOME/.fakecli/auth.json g
env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work
env FAKECLI_LOGIN_AS=personal
exec whoas add fakecli personal

# AC-02: the CLI receives the chosen identity
exec whoas fakecli@work whoami
stdout '^account=work$'
exec whoas fakecli@personal whoami
stdout '^account=personal$'

# banner (stderr is not a TTY here, so force it)
env WHOAS_BANNER=always
exec whoas fakecli@work whoami
stderr '^whoas: fakecli@work \(account=work\)$'
env WHOAS_BANNER=

# AC-05: exit codes pass through
expect-exit 7 whoas fakecli@work exit 7

# AC-07: unknown identity fails before running the CLI
! exec whoas fakecli@nobody whoami
stderr 'identity fakecli@nobody not found'
! stdout .
expect-exit 125 whoas fakecli@nobody whoami

# no context at all: explicit run without alias is an error
! exec whoas fakecli whoami
stderr 'no identity resolved'

# refreshed tokens are sealed back into the vault
exec whoas fakecli@work refresh token
stdout '^token=tok-work-1$'
exec whoas fakecli@work token
stdout '^token=tok-work-1$'

# AC-03: two identities at the same time
exec whoas fakecli@work sleep 300 whoami &w&
exec whoas fakecli@personal sleep 300 whoami &p&
wait
stdout 'account=work'
stdout 'account=personal'

# same identity twice at once shares one overlay, removed afterwards
exec whoas fakecli@work dir sleep 300 &a&
exec whoas fakecli@work dir sleep 300 &b&
wait
! exists $WORK/run/id/fakecli/work
! exists $WORK/run/id/fakecli/work.state.json

# non-auth state the CLI writes lands in the shared global dir
exec whoas fakecli@work setting theme dark
exists $HOME/.fakecli/settings/theme.txt

# without a terminal (agents, CI) SIGINT sent to whoas is forwarded to the CLI
! exec whoas fakecli@work trap &t&
sleep 1s
kill -INT t
wait
stdout 'trapped=INT'

# kill -9 of whoas mid-run: the next run recovers with the refreshed token
! exec whoas fakecli@work refresh sleep 1500 &k&
sleep 500ms
kill -KILL k
wait
exec whoas fakecli@work token
stdout '^token=tok-work-2$'

# the global session was never modified
unchanged $HOME/.fakecli/auth.json g
exec fakecli whoami
stdout '^account=global$'
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/app/ ./tests/
```

Expected: FAIL — `undefined: leadingProfile`; exec.txtar fails: `whoas fakecli@work whoami` → `unknown command "fakecli@work"`.

- [ ] **Step 3: Write the implementation**

**`internal/app/app.go`**

```go
// Package app wires whoas subcommands and process-level dispatch.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
)

// Version is set at build time:
// -ldflags "-X github.com/S-kkipie/whoas/internal/app.Version=v1.2.3".
var Version = "dev"

var factories []func() *cobra.Command

// register adds a subcommand constructor; each command file calls it from init.
func register(f func() *cobra.Command) { factories = append(factories, f) }

// exitError carries a specific exit code out of a cobra RunE.
// A nil err means the message was already printed.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "whoas",
		Short:         "Run CLIs as a chosen identity",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	for _, f := range factories {
		root.AddCommand(f())
	}
	return root
}

// runCobra executes subcommands and maps errors to exit codes.
func runCobra(args []string) int {
	root := newRoot()
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(os.Stderr, "whoas:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "whoas:", err)
	return 1
}

// Main is the process entry point and returns the exit code.
//
//	argv[0] != whoas            → shim for that CLI
//	whoas [-p P] provider[@a] … → run the CLI as an identity
//	whoas <subcommand> …        → cobra
func Main(args []string) int {
	name := strings.TrimSuffix(filepath.Base(args[0]), ".exe")
	if name != "whoas" {
		return shimMain(name, args[1:])
	}
	profile, rest, err := leadingProfile(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return 2
	}
	if len(rest) > 0 {
		if _, _, err := ref.Split(rest[0]); err == nil {
			return execMain(profile, rest[0], rest[1:])
		}
	}
	if profile != "" {
		fmt.Fprintln(os.Stderr, "whoas: -p must be followed by a provider (or use: whoas run -p NAME -- cmd)")
		return 2
	}
	return runCobra(args[1:])
}

// leadingProfile strips "-p NAME", "--profile NAME" or "--profile=NAME"
// placed before the provider.
func leadingProfile(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", args, nil
	}
	switch a := args[0]; {
	case a == "-p" || a == "--profile":
		if len(args) < 2 {
			return "", nil, fmt.Errorf("%s needs a profile name", a)
		}
		return args[1], args[2:], nil
	case strings.HasPrefix(a, "--profile="):
		return strings.TrimPrefix(a, "--profile="), args[1:], nil
	}
	return "", args, nil
}
```

**`internal/app/exec.go`**

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/term"

	"github.com/S-kkipie/whoas/internal/project"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/resolve"
	"github.com/S-kkipie/whoas/internal/supervisor"
	"github.com/S-kkipie/whoas/internal/trust"
	"github.com/S-kkipie/whoas/internal/vault"
)

// Exit codes for failures of whoas itself on the run path (env/docker style).
const (
	exitWhoasError = 125 // whoas failed before or around the CLI
	exitExecFailed = 126 // exec of the real binary failed
	exitNotFound   = 127 // real binary not on PATH
)

const maxDepth = 8

// errPassthrough means no identity applies; a shim should exec the global CLI.
var errPassthrough = errors.New("passthrough")

type runRequest struct {
	provider    string
	alias       string
	profileFlag string
	args        []string
	explicit    bool // false when invoked through a shim
}

// execMain handles `whoas [-p P] provider[@alias] args...`.
func execMain(profileFlag, token string, args []string) int {
	provider, alias, _ := ref.Split(token)
	e, err := loadEnv()
	if err == nil {
		var code int
		code, err = runProvider(e, runRequest{provider: provider, alias: alias, profileFlag: profileFlag, args: args, explicit: true})
		if err == nil {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "whoas:", err)
	return exitWhoasError
}

// runProvider resolves the identity and runs the CLI under the supervisor.
func runProvider(e *env, req runRequest) (int, error) {
	a, err := e.adapter(req.provider)
	if err != nil {
		return 0, err
	}
	res, err := e.resolveIdentity(req)
	if err != nil {
		return 0, err
	}
	depth, _ := strconv.Atoi(os.Getenv("WHOAS_DEPTH"))
	if depth >= maxDepth {
		return 0, errors.New("WHOAS_DEPTH limit reached (shim loop?)")
	}
	bin, err := e.realBin(a.Bin)
	if err != nil {
		return 0, err
	}
	v, err := e.openVault()
	if errors.Is(err, vault.ErrNotInitialized) {
		return 0, fmt.Errorf("identity %s not found (see: whoas ls)", res.Ref)
	}
	if err != nil {
		return 0, err
	}
	if !v.Exists(res.Ref) {
		return 0, fmt.Errorf("identity %s not found (see: whoas ls)", res.Ref)
	}
	d, err := e.decrypter()
	if err != nil {
		return 0, err
	}
	if err := e.dirs.EnsureRuntime(); err != nil {
		return 0, err
	}
	meta, _ := v.ReadMeta(res.Ref)
	banner(res.Ref, meta.Account)
	result, err := supervisor.Run(context.Background(), supervisor.Plan{
		Ref:       res.Ref,
		Adapter:   a,
		Bin:       bin,
		Args:      req.args,
		Global:    a.GlobalDir(e.vars()),
		RunBase:   e.dirs.RunBase(res.Ref.Provider, res.Ref.Alias),
		Vault:     v,
		Decrypter: d,
		Env:       append(os.Environ(), "WHOAS_DEPTH="+strconv.Itoa(depth+1)),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
	if err != nil {
		return 0, err
	}
	if result.Signal != nil {
		supervisor.Reraise(result.Signal)
	}
	return result.Code, nil
}

// resolveIdentity applies the resolution rules, reading .whoas.yaml only
// when no explicit context is given.
func (e *env) resolveIdentity(req runRequest) (resolve.Result, error) {
	in := resolve.Input{
		Provider:    req.provider,
		Alias:       req.alias,
		ProfileFlag: req.profileFlag,
		EnvProfile:  os.Getenv("WHOAS_PROFILE"),
		Config:      e.cfg,
	}
	if in.Alias == "" && in.ProfileFlag == "" && in.EnvProfile == "" {
		pf, err := e.trustedProject(!req.explicit)
		if err != nil {
			return resolve.Result{}, err
		}
		in.Project = pf
	}
	res, err := resolve.Resolve(in)
	if err != nil && !req.explicit && errors.Is(err, resolve.ErrUnresolved) {
		return res, errPassthrough
	}
	return res, err
}

// trustedProject returns the nearest trusted .whoas.yaml. An untrusted file
// is an error, or a warning (and ignored) when warnOnly is set (shims).
func (e *env) trustedProject(warnOnly bool) (*project.File, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	path, err := project.Find(wd)
	if err != nil || path == "" {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ts, err := trust.Load(e.dirs.TrustFile())
	if err != nil {
		return nil, err
	}
	if !ts.IsTrusted(path, data) {
		msg := fmt.Errorf("%s is not trusted (review it, then run: whoas trust)", path)
		if warnOnly {
			fmt.Fprintln(os.Stderr, "whoas: warning:", msg)
			return nil, nil
		}
		return nil, msg
	}
	return project.Parse(path, data)
}

// banner prints the identity on stderr when it is a terminal, or always /
// never per WHOAS_BANNER.
func banner(r ref.Ref, account string) {
	mode := os.Getenv("WHOAS_BANNER")
	if mode == "never" || (mode != "always" && !term.IsTerminal(int(os.Stderr.Fd()))) {
		return
	}
	fmt.Fprintf(os.Stderr, "whoas: %s%s\n", r, accountSuffix(account))
}
```

**`internal/app/shim.go`**

```go
package app

import (
	"errors"
	"fmt"
	"os"
)

// shimMain runs when whoas is invoked through a shim named after a CLI.
// With an identity it supervises; without one it becomes the real CLI.
func shimMain(bin string, args []string) int {
	e, err := loadEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return exitWhoasError
	}
	if a, ok := e.registry.ByBin(bin); ok {
		code, err := runProvider(e, runRequest{provider: a.Name, args: args})
		if err == nil {
			return code
		}
		if !errors.Is(err, errPassthrough) {
			fmt.Fprintln(os.Stderr, "whoas:", err)
			return exitWhoasError
		}
	}
	real, err := e.realBin(bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return exitNotFound
	}
	return execReplace(real, append([]string{bin}, args...), os.Environ())
}
```

**`internal/app/execreplace_unix.go`**

```go
//go:build unix

package app

import (
	"fmt"
	"os"
	"syscall"
)

// execReplace replaces this process with path (zero overhead passthrough).
// It returns only on failure.
func execReplace(path string, argv, env []string) int {
	err := syscall.Exec(path, argv, env)
	fmt.Fprintln(os.Stderr, "whoas: exec", path+":", err)
	return exitExecFailed
}
```

**`internal/app/execreplace_windows.go`**

```go
//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execReplace runs path as a child and returns its exit code (Windows has no exec).
func execReplace(path string, argv, env []string) int {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas: exec", path+":", err)
		return exitExecFailed
	}
	return 0
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./internal/app/ ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/app/app.go internal/app/app_test.go internal/app/exec.go internal/app/execreplace_unix.go internal/app/execreplace_windows.go internal/app/shim.go tests/testdata/exec.txtar
git commit -m "feat: run CLIs as an identity via dispatch and shims" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: Commands: profile, which, trust/untrust

**Files:**
- Create: `internal/app/profile.go`
- Create: `internal/app/which.go`
- Create: `internal/app/trust.go`
- Test: `tests/testdata/project.txtar`

**Interfaces:**
- Consumes: `config` (Task 4); `trust`, `project` (Task 12); `resolveIdentity`, `runRequest`, `exitError` (Tasks 1, 14).
- Produces: Commands: `whoas profile set <name> [provider@alias...]`, `profile ls`, `profile show <name>`, `profile default <name>`, `whoas which <provider> [-p profile]` (exit 1 when unresolved), `whoas trust [file]`, `whoas untrust [file]`; helper `projectPath(args []string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

**`tests/testdata/project.txtar`**

```text
# Profiles, default profile, trusted .whoas.yaml (AC-04) and precedence.
env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work
env FAKECLI_LOGIN_AS=personal
exec whoas add fakecli personal

exec whoas profile set work fakecli@work
exec whoas profile set personal fakecli@personal
! exec whoas profile set work fakecli@nobody
stderr 'identity fakecli@nobody not found'
exec whoas profile default personal
exec whoas profile ls
stdout '^\* personal$'
stdout '^  work$'
exec whoas profile show work
stdout '^fakecli\s+work$'

# the default profile applies when nothing else does
exec whoas fakecli whoami
stdout '^account=personal$'
exec whoas which fakecli
stdout '^fakecli@personal  via default profile \(profile personal\)$'

# AC-04: an untrusted project file blocks explicit runs until trusted
cd proj
! exec whoas fakecli whoami
stderr 'not trusted'
exec whoas trust
stdout 'trusted .*\.whoas\.yaml'
exec whoas fakecli whoami
stdout '^account=work$'
exec whoas which fakecli
stdout 'fakecli@work  via project .*\.whoas\.yaml \(profile work\)'

# -p and WHOAS_PROFILE beat the project file
exec whoas -p personal fakecli whoami
stdout '^account=personal$'
env WHOAS_PROFILE=personal
exec whoas fakecli whoami
stdout '^account=personal$'
env WHOAS_PROFILE=typo
! exec whoas fakecli whoami
stderr 'unknown profile "typo"'
env WHOAS_PROFILE=

# editing the file revokes trust
cp other.yaml .whoas.yaml
! exec whoas fakecli whoami
stderr 'not trusted'
exec whoas untrust
stdout '^untrusted '
exec whoas untrust
stdout '^not trusted: '

# schema whitelist is enforced at trust time
cp bad.yaml .whoas.yaml
! exec whoas trust
stderr 'must be a string'

-- proj/.whoas.yaml --
profile: work
-- proj/other.yaml --
profile: personal
-- proj/bad.yaml --
profile: [work]
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./tests/
```

Expected: FAIL — project.txtar: `unknown command "profile"`.

- [ ] **Step 3: Write the implementation**

**`internal/app/profile.go`**

```go
package app

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
)

func init() { register(newProfileCmd) }

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Manage profiles (provider → alias maps)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "set <name> [provider@alias...]",
			Short: "Create a profile or bind identities in it",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				if err := ref.ValidAlias(args[0]); err != nil {
					return err
				}
				e.cfg.EnsureProfile(args[0])
				if len(args) > 1 {
					v, err := e.openVault()
					if err != nil {
						return err
					}
					for _, s := range args[1:] {
						r, err := ref.Parse(s)
						if err != nil {
							return err
						}
						if !v.Exists(r) {
							return fmt.Errorf("identity %s not found (see: whoas ls)", r)
						}
						e.cfg.Bind(args[0], r)
					}
				}
				if err := e.cfg.Save(e.dirs.ConfigFile()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "saved profile", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "ls",
			Short: "List profiles (* = default)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				for _, n := range e.cfg.ProfileNames() {
					mark := " "
					if n == e.cfg.DefaultProfile {
						mark = "*"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", mark, n)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "show <name>",
			Short: "Show a profile's bindings",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				b, ok := e.cfg.Profile(args[0])
				if !ok {
					return fmt.Errorf("unknown profile %q", args[0])
				}
				providers := make([]string, 0, len(b))
				for p := range b {
					providers = append(providers, p)
				}
				sort.Strings(providers)
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				for _, p := range providers {
					fmt.Fprintf(tw, "%s\t%s\n", p, b[p])
				}
				return tw.Flush()
			},
		},
		&cobra.Command{
			Use:   "default <name>",
			Short: "Set the profile used when nothing else applies",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				if _, ok := e.cfg.Profile(args[0]); !ok {
					return fmt.Errorf("unknown profile %q", args[0])
				}
				e.cfg.DefaultProfile = args[0]
				if err := e.cfg.Save(e.dirs.ConfigFile()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "default profile:", args[0])
				return nil
			},
		},
	)
	return cmd
}
```

**`internal/app/which.go`**

```go
package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/resolve"
)

func init() { register(newWhichCmd) }

func newWhichCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "which <provider>",
		Short: "Show which identity a provider would run as, and why",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			provider, alias, err := ref.Split(args[0])
			if err != nil {
				return err
			}
			if _, err := e.adapter(provider); err != nil {
				return err
			}
			res, err := e.resolveIdentity(runRequest{provider: provider, alias: alias, profileFlag: profile, explicit: true})
			if errors.Is(err, resolve.ErrUnresolved) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: no identity (shims pass through to the global CLI)\n", provider)
				return &exitError{code: 1}
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  via %s\n", res.Ref, res.Source)
			return nil
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "resolve as if -p were given")
	return cmd
}
```

**`internal/app/trust.go`**

```go
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/project"
	"github.com/S-kkipie/whoas/internal/trust"
)

func init() {
	register(newTrustCmd)
	register(newUntrustCmd)
}

// projectPath returns the given file (absolute) or the nearest .whoas.yaml.
func projectPath(args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	p, err := project.Find(wd)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("no " + project.FileName + " found here or in any parent directory")
	}
	return p, nil
}

func newTrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trust [file]",
		Short: "Approve a .whoas.yaml after reviewing it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			path, err := projectPath(args)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if _, err := project.Parse(path, data); err != nil {
				return err
			}
			ts, err := trust.Load(e.dirs.TrustFile())
			if err != nil {
				return err
			}
			ts.Trust(path, data)
			if err := ts.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "trusted", path)
			return nil
		},
	}
}

func newUntrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrust [file]",
		Short: "Revoke approval of a .whoas.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			path, err := projectPath(args)
			if err != nil {
				return err
			}
			ts, err := trust.Load(e.dirs.TrustFile())
			if err != nil {
				return err
			}
			if !ts.Untrust(path) {
				fmt.Fprintln(cmd.OutOrStdout(), "not trusted:", path)
				return nil
			}
			if err := ts.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "untrusted", path)
			return nil
		},
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/app/profile.go internal/app/trust.go internal/app/which.go tests/testdata/project.txtar
git commit -m "feat: manage profiles, explain resolution, trust project files" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 16: Commands: run and shims

**Files:**
- Create: `internal/app/run.go`
- Create: `internal/app/shims.go`
- Test: `tests/testdata/shims.txtar`

**Interfaces:**
- Consumes: `binpath.Find` (Task 13); `execReplace`, `exitNotFound` (Task 14).
- Produces: Commands: `whoas run [-p profile] -- <cmd...>` (prepends shims dir to PATH, sets `WHOAS_PROFILE`, then exec), `whoas shims install|uninstall|ls`; helpers `shimLinks(dir string) ([]string, error)`, `inPath(dir string) bool`.

- [ ] **Step 1: Write the failing tests**

**`tests/testdata/shims.txtar`**

```text
# Shims give nested and agent-launched CLI calls the right identity.
env FAKECLI_LOGIN_AS=global
exec fakecli login
env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work
exec whoas profile set work fakecli@work

exec whoas shims install
stdout '^shims: .*fakecli'
stdout 'export PATH='
exists $WORK/data/shims/fakecli

# a shim with nothing resolved is the real CLI on the global session
env PATH=$WORK/data/shims:$PATH
exec fakecli whoami
stdout '^account=global$'

# WHOAS_PROFILE makes the shim supervise as the profile's identity
env WHOAS_PROFILE=work
exec fakecli whoami
stdout '^account=work$'
env WHOAS_PROFILE=

# an untrusted project file only warns through a shim
cd proj
exec fakecli whoami
stdout '^account=global$'
stderr 'warning: .*not trusted'
cd $WORK

# whoas run: every nested call goes through the shims with the profile
exec whoas run -p work -- sh -c 'fakecli whoami'
stdout '^account=work$'
! exec whoas run -p nope -- true
stderr 'unknown profile "nope"'
expect-exit 3 whoas run -- sh -c 'exit 3'

exec whoas shims ls
stdout '^fakecli$'
exec whoas shims uninstall
stdout 'removed [0-9]+ shims'
! exists $WORK/data/shims/fakecli

-- proj/.whoas.yaml --
profile: work
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./tests/
```

Expected: FAIL — shims.txtar: `unknown command "shims"`.

- [ ] **Step 3: Write the implementation**

**`internal/app/run.go`**

```go
package app

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/binpath"
)

func init() { register(newRunCmd) }

func newRunCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "run [-p profile] -- <command> [args...]",
		Short: "Run a command whose nested CLI calls go through whoas shims",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if profile != "" {
				if _, ok := e.cfg.Profile(profile); !ok {
					return fmt.Errorf("unknown profile %q", profile)
				}
			}
			pathList := e.dirs.ShimsDir() + string(os.PathListSeparator) + os.Getenv("PATH")
			bin, err := binpath.Find(args[0], pathList, "", "")
			if err != nil {
				return &exitError{code: exitNotFound, err: err}
			}
			env := append(os.Environ(), "PATH="+pathList)
			if profile != "" {
				env = append(env, "WHOAS_PROFILE="+profile)
			}
			return &exitError{code: execReplace(bin, args, env)}
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "profile for every nested CLI")
	cmd.Flags().SetInterspersed(false)
	return cmd
}
```

**`internal/app/shims.go`**

```go
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() { register(newShimsCmd) }

func newShimsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "shims", Short: "Manage CLI shims (transparent identity for agents and scripts)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Create a shim for every adapter whose CLI is installed",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				target, err := filepath.EvalSymlinks(self())
				if err != nil {
					return err
				}
				dir := e.dirs.ShimsDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
				var installed []string
				for _, name := range e.registry.Names() {
					a, _ := e.registry.Get(name)
					if _, err := e.realBin(a.Bin); err != nil {
						continue // CLI not installed
					}
					link := filepath.Join(dir, a.Bin)
					if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
						if err := os.Remove(link); err != nil {
							return err
						}
					}
					if err := os.Symlink(target, link); err != nil {
						return err
					}
					installed = append(installed, a.Bin)
				}
				out := cmd.OutOrStdout()
				fmt.Fprintln(out, "shims:", strings.Join(installed, ", "))
				if !inPath(dir) {
					fmt.Fprintf(out, "add to your shell rc:\n  export PATH=\"%s:$PATH\"\n", dir)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "uninstall",
			Short: "Remove all shims",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				links, err := shimLinks(e.dirs.ShimsDir())
				if err != nil {
					return err
				}
				for _, l := range links {
					if err := os.Remove(l); err != nil {
						return err
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed %d shims\n", len(links))
				return nil
			},
		},
		&cobra.Command{
			Use:   "ls",
			Short: "List installed shims",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				e, err := loadEnv()
				if err != nil {
					return err
				}
				links, err := shimLinks(e.dirs.ShimsDir())
				if err != nil {
					return err
				}
				for _, l := range links {
					fmt.Fprintln(cmd.OutOrStdout(), filepath.Base(l))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dir %s (in PATH: %t)\n", e.dirs.ShimsDir(), inPath(e.dirs.ShimsDir()))
				return nil
			},
		},
	)
	return cmd
}

// shimLinks returns the symlinks in dir.
func shimLinks(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, en := range entries {
		if en.Type()&os.ModeSymlink != 0 {
			out = append(out, filepath.Join(dir, en.Name()))
		}
	}
	return out, nil
}

func inPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/app/run.go internal/app/shims.go tests/testdata/shims.txtar
git commit -m "feat: add run command and CLI shims for agents" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 17: Commands: agent, unlock, lock, keystore-helper, doctor, providers

**Files:**
- Create: `internal/app/agent.go`
- Create: `internal/app/doctor.go`
- Test: `tests/testdata/doctor.txtar`

**Interfaces:**
- Consumes: `agent` (Task 10); `keystore.OS` (Task 5); `supervisor.Recover` (Task 11); `inPath` (Task 16).
- Produces: Commands: `whoas agent`, `whoas unlock`, `whoas lock`, hidden `whoas keystore-helper get|set` (exit 2 = not found; used by WSL), `whoas doctor` (also recovers stale overlays via `recoverAll(e *env, v *vault.Vault) (int, error)`), `whoas providers`.

- [ ] **Step 1: Write the failing tests**

**`tests/testdata/doctor.txtar`**

```text
# providers, doctor, agent lifecycle; AC-06: no secret ever printed.
exec whoas providers
stdout '^gh\s+gh\s+dir-overlay'
stdout '^fakecli\s+fakecli\s+dir-overlay\s+\S*fakecli$'

exec whoas doctor
stdout '^keystore\s+env \(WHOAS_MASTER_KEY\)$'
stdout '^vault\s+not initialized$'
stdout '^agent\s+not running$'

env FAKECLI_LOGIN_AS=work
exec whoas add fakecli work
exec whoas doctor
stdout '^vault\s+initialized$'
stdout '^recovered\s+0 stale overlay'

# agent: start, unlock, use, lock
exec whoas agent &ag&
sleep 300ms
exec whoas unlock
stdout '^unlocked \(env'
exec whoas doctor
stdout '^agent\s+running \(unlocked\)$'
exec whoas fakecli@work whoami
stdout '^account=work$'
exec whoas lock
stdout '^locked$'
exec whoas doctor
stdout '^agent\s+running \(locked\)$'
kill -INT ag
wait

# AC-06: nothing whoas prints contains a token or the master key,
# and sealed blobs are not plaintext.
exec whoas ls
! stdout 'tok-|AGE-SECRET-KEY'
exec whoas doctor
! stdout 'tok-|AGE-SECRET-KEY'
exec whoas which fakecli@work
! stdout 'tok-|AGE-SECRET-KEY'
! grep 'tok-' $WORK/data/vault/fakecli/work.age
! grep 'tok-' $WORK/data/vault/fakecli/work.meta.json
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./tests/
```

Expected: FAIL — doctor.txtar: `unknown command "providers"`.

- [ ] **Step 3: Write the implementation**

**`internal/app/agent.go`**

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/keystore"
)

func init() {
	register(newAgentCmd)
	register(newUnlockCmd)
	register(newLockCmd)
	register(newKeystoreHelperCmd)
}

func newAgentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent",
		Short: "Run the key agent in the foreground (started automatically when needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return agent.NewServer().Serve(ctx, e.dirs.AgentSocket())
		},
	}
}

func newUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Load the master key into the agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			st, err := e.store()
			if err != nil {
				return err
			}
			secret, err := st.Get()
			if err != nil {
				return fmt.Errorf("master key (%s): %w", st.Name(), err)
			}
			if err := e.dirs.EnsureRuntime(); err != nil {
				return err
			}
			c := agent.NewClient(e.dirs.AgentSocket())
			if _, err := c.Status(); err != nil {
				if err := agent.Spawn(self(), e.dirs.AgentSocket()); err != nil {
					return err
				}
			}
			if err := c.Load(secret, st.TTL()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unlocked (%s)\n", st.Name())
			return nil
		},
	}
}

func newLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Make the agent forget the master key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			if err := agent.NewClient(e.dirs.AgentSocket()).Lock(); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "agent not running")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "locked")
			return nil
		},
	}
}

// newKeystoreHelperCmd is what WSL calls on whoas.exe to reach the Windows
// Credential Manager. get: exit 2 when absent. set: secret on stdin.
func newKeystoreHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "keystore-helper <get|set>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := keystore.OS()
			switch args[0] {
			case "get":
				s, err := st.Get()
				if errors.Is(err, keystore.ErrNotFound) {
					return &exitError{code: 2}
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), s)
				return nil
			case "set":
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				return st.Set(strings.TrimSpace(string(b)))
			}
			return fmt.Errorf("unknown action %q (get|set)", args[0])
		},
	}
}
```

**`internal/app/doctor.go`**

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/supervisor"
	"github.com/S-kkipie/whoas/internal/vault"
)

func init() {
	register(newDoctorCmd)
	register(newProvidersCmd)
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
		if !ok {
			return nil
		}
		found, err := supervisor.Recover(context.Background(), base, a.GlobalDir(e.vars()), a, r, v)
		if found {
			n++
		}
		return err
	})
	return n, err
}

func newProvidersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List adapters and whether their CLI is installed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "PROVIDER\tBIN\tKIND\tINSTALLED")
			for _, n := range e.registry.Names() {
				a, _ := e.registry.Get(n)
				installed := "-"
				if p, err := e.realBin(a.Bin); err == nil {
					installed = p
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.Bin, a.Kind, installed)
			}
			return tw.Flush()
		},
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./tests/
```

Expected: no gofmt output, vet silent, every listed package `ok`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/app/agent.go internal/app/doctor.go tests/testdata/doctor.txtar
git commit -m "feat: add agent controls, doctor and providers" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 18: Release config, CI, README

**Files:**
- Create: `.goreleaser.yaml`
- Create: `.github/workflows/ci.yml`
- Create: `README.md`
- Create: `LICENSE`

**Interfaces:**
- Consumes: All previous tasks.
- Produces: Release artifacts for linux/darwin/windows × amd64/arm64; CI running unit + e2e (`-race`) on ubuntu/macos and real-keyring round trips on ubuntu/macos/windows.

- [ ] **Step 1: Write the implementation**

**`.goreleaser.yaml`**

```yaml
version: 2
project_name: whoas

builds:
  - id: whoas
    main: ./cmd/whoas
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w -X github.com/S-kkipie/whoas/internal/app.Version={{.Version}}

archives:
  - id: default
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files: [README.md, LICENSE]

checksum:
  name_template: checksums.txt
```

**`.github/workflows/ci.yml`**

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: gofmt
        run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go test -race ./...

  keyring:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: build + vet (all platforms)
        run: |
          go build ./...
          go vet ./...
      - name: Secret Service (Linux)
        if: runner.os == 'Linux'
        run: |
          sudo apt-get update
          sudo apt-get install -y gnome-keyring dbus-x11
          dbus-run-session -- sh -c 'echo ci | gnome-keyring-daemon --unlock --components=secrets && go test -tags keyring_integration -run TestOSKeyring ./internal/keystore/'
      - name: OS keyring (macOS / Windows)
        if: runner.os != 'Linux'
        run: go test -tags keyring_integration -run TestOSKeyring ./internal/keystore/
```

**`README.md`**

````markdown
# whoas

Run any CLI as a chosen identity. Capture several OAuth logins per CLI once,
then switch per command, per project, or per AI agent — without logging out,
without touching the CLI's global session.

```
whoas add gh work            # log in once through gh's own flow
whoas add gh personal
whoas gh@work repo list      # runs the real gh as "work"
whoas gh@personal repo list  # at the same time, as "personal"
```

- **Open source and local.** Sessions are sealed with [age](https://age-encryption.org)
  in `~/.local/share/whoas/vault`; the master key lives in your OS keychain.
- **OAuth sessions, not just tokens.** whoas snapshots what the CLI's own login
  writes, so browser logins work.
- **Built for agents.** With shims, `gh`, `gcloud`, … pick the identity from the
  project or session, so an agent that runs `gh pr create` uses the right
  account without knowing whoas exists.

> Status: early. Linux, WSL and macOS. Native Windows only ships the
> `whoas.exe` keystore helper used by WSL.

## Install

```
go install github.com/S-kkipie/whoas/cmd/whoas@latest
```

**WSL:** also put the Windows build next to the Linux one so the master key
lives in Windows Credential Manager:

```
GOOS=windows go build -o "$(dirname "$(command -v whoas)")/whoas.exe" github.com/S-kkipie/whoas/cmd/whoas
```

## Use

```
whoas providers                     # supported CLIs and whether they are installed
whoas add <provider> <alias>        # log in and save the session
whoas import <provider> <alias>     # save the CLI's current global session
whoas ls                            # identities (never prints secrets)
whoas <provider>@<alias> [args...]  # run as that identity; args pass through verbatim
whoas which <provider>              # what would resolve, and why
```

### Profiles and projects

```
whoas profile set work gh@work gcloud@acme
whoas profile default personal
echo 'profile: work' > ~/code/client/.whoas.yaml
cd ~/code/client && whoas trust     # review first: project files need approval
whoas gh repo list                  # → gh@work
```

Resolution — the first source present decides, with no fallthrough:
`provider@alias` → `-p profile` → `WHOAS_PROFILE` → trusted `.whoas.yaml`
(a `provider: alias` line beats its `profile`) → default profile.

### Shims and agents

```
whoas shims install                 # prints the PATH line to add
whoas run -p work -- claude         # every CLI the agent runs resolves to "work"
```

A shim with no identity resolved is a zero-overhead `exec` of the real CLI.

### Key agent

On WSL and with the passphrase fallback, reading the master key is slow, so
whoas starts `whoas agent` to cache it (the key never leaves the agent; clients
send ciphertext). `whoas unlock` / `whoas lock` control it.

### Exit codes

The CLI's own exit code (or signal) is passed through. whoas's own failures on
the run path exit `125`; `126` = exec failed; `127` = CLI not found.

## How it works

For `whoas gh@work`, whoas builds an overlay of `~/.config/gh` in
`$XDG_RUNTIME_DIR/whoas`: every entry is a symlink to the real one except the
auth files (`hosts.yml`), which are decrypted from the vault. gh is pointed at
the overlay (`GH_CONFIG_DIR`). Processes using the same identity share the
overlay; when gh refreshes a token it is re-sealed within ~200ms; when the last
one exits the overlay is removed. Your global `~/.config/gh/hosts.yml` is never
written.

## Adapters

Built in: `gh`, `gcloud`, `codex`, `neonctl`. Add your own in
`~/.config/whoas/adapters/<name>.toml` — data only, never code:

```toml
name = "mycli"
bin  = "mycli"
kind = "dir-overlay"

[dir]
global     = "{xdg_config}/mycli"            # {home} {xdg_config} {xdg_data}
inject     = { env = { MYCLI_CONFIG_DIR = "{dir}" } }   # or flag = ["--config-dir", "{dir}"]
auth_files = ["credentials.json"]

[login]
cmd = ["mycli", "login"]

[whoami]
cmd  = ["mycli", "whoami", "--json"]
json = "email"                               # optional gjson path
```

## Threat model

**Protects against**

- Running against the wrong account (explicit resolution, banner, `which`, trust gate).
- Sessions leaking at rest through backups, dotfile repos, cloud sync or disk
  theft: the vault is encrypted; plaintext exists only on tmpfs while in use.
- whoas leaking secrets in output: `ls`, `doctor` and errors never print them;
  injection uses env vars and paths, never tokens in argv.
- Malicious repositories: `.whoas.yaml` needs `whoas trust` (by content hash)
  and only accepts `profile` and `provider: alias` strings.
- Other local users: 0700/0600 permissions and a peer-uid check on the agent socket.

**Does not protect against**

- Malware running as your user (it can read live overlays,
  `/proc/<pid>/environ`, or ask the agent to decrypt). Same boundary as
  aws-vault and 1Password CLI.
- The CLI's own child processes (they inherit the identity by design).
- A compromised CLI binary.

## Credits

Ideas from [direnv](https://direnv.net) (trust), [mise](https://mise.jdx.dev)
(shims), [aws-vault](https://github.com/99designs/aws-vault) (exec + keyring)
and [1Password Shell Plugins](https://github.com/1Password/shell-plugins).

## License

MIT
````

**`LICENSE`**

```text
MIT License

Copyright (c) 2026 Adrian Issac

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 2: Verify the whole tree and cross-builds**

```bash
go mod tidy
test -z "$(gofmt -l .)"
go vet ./...
go test -race -count=1 ./...
for os in darwin windows; do GOOS=$os CGO_ENABLED=0 go build ./... && GOOS=$os go vet ./...; done
```

Expected: every package `ok`, no gofmt output, both cross-builds silent. Optional: `go run github.com/goreleaser/goreleaser/v2@latest check` → `config is valid`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml .goreleaser.yaml LICENSE README.md
git commit -m "docs: add README, release config and CI" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Final `go.mod` (reference)

```text
module github.com/S-kkipie/whoas

go 1.25.0

require (
	filippo.io/age v1.3.2
	github.com/BurntSushi/toml v1.6.0
	github.com/adrg/xdg v0.5.3
	github.com/awnumar/memguard v0.23.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/goccy/go-yaml v1.19.2
	github.com/gofrs/flock v0.13.1
	github.com/rogpeppe/go-internal v1.16.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.12.1
	github.com/tidwall/gjson v1.20.0
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
)

require (
	filippo.io/hpke v0.4.0 // indirect
	github.com/awnumar/memcall v0.4.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)
```

## Spec coverage (M1)

- Concepts, CLI surface, reserved names → Tasks 3, 13–17
- Resolution + `.whoas.yaml` whitelist + trust + visibility (`banner`, `which`) → Tasks 12, 14, 15
- Vault (age, public-key sealing), master-key sources, agent (key never leaves, TTL, peer check) → Tasks 5, 6, 10, 17
- Adapters (declarative, dir-overlay env/flag), overlay mirror, capture (`add`/`import`, duplicate warning) → Tasks 7, 8, 13
- Execution lifecycle (lock/holders, shared overlay, spawn, signals, seal-on-write, exit codes, crash recovery) → Tasks 9, 11, 14, 17
- Shims + `run` (agent story) → Tasks 14, 16
- Security invariants (no secrets in output, global auth untouched, no shell) → e2e in Tasks 13, 14, 17
- Testing strategy (unit + testscript + fakecli + keyring CI matrix) → every task + Task 18
- Out of M1 by design: env-token/json-slice kinds, vercel/wrangler/railway/docker, Claude/MCP, macOS Claude keychain, native Windows run
