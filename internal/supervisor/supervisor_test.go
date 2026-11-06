//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
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

func (f *fixture) vaultAuthOrEmpty(t *testing.T) string {
	t.Helper()
	env, err := f.plan.Vault.Load(f.plan.Ref, f.dec)
	if err != nil {
		return ""
	}
	return string(env.Files["auth.json"].Data)
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	err := cmd.Run()
	require.NoError(t, err)
	return cmd.ProcessState.Pid()
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
	require.Eventually(t, func() bool { return f.vaultAuthOrEmpty(t) == "v2" }, 900*time.Millisecond, 20*time.Millisecond,
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
	require.NoError(t, l.Write(runlock.State{Holders: []int{deadPID(t)}, Hash: "stale"}))
	require.NoError(t, l.Release())

	_, out := run(t, f.plan)
	require.Equal(t, "crashed", out, "refreshed token from the crashed run is kept")

	_, err = os.Stat(f.plan.RunBase)
	require.True(t, os.IsNotExist(err), "overlay removed after recovery")
	_, err = os.Stat(f.plan.RunBase + ".state.json")
	require.True(t, os.IsNotExist(err), "state removed after recovery")
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

// writeState records holders and hash for the fixture's overlay.
func (f *fixture) writeState(t *testing.T, s runlock.State) {
	t.Helper()
	l, err := runlock.Acquire(context.Background(), f.plan.RunBase, time.Second)
	require.NoError(t, err)
	require.NoError(t, l.Write(s))
	require.NoError(t, l.Release())
}

func TestRecoverHelper(t *testing.T) {
	f := newFixture(t, `true`)
	require.NoError(t, os.MkdirAll(f.plan.RunBase, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "auth.json"), []byte("orphan"), 0o600))
	f.writeState(t, runlock.State{Holders: []int{deadPID(t)}, Hash: "stale"})
	found, err := Recover(context.Background(), f.plan.RunBase, f.global, f.plan.Adapter, f.plan.Ref, f.plan.Vault)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "orphan", f.vaultAuth(t))
	_, err = os.Stat(f.plan.RunBase)
	require.True(t, os.IsNotExist(err), "overlay removed")
}

// An overlay without a recorded hash was never completed by join (crash
// mid-build): it must be dropped, never sealed over the vault.
func TestRecoverDoesNotSealIncompleteOverlay(t *testing.T) {
	for name, recov := range map[string]func(t *testing.T, f *fixture){
		"run": func(t *testing.T, f *fixture) { run(t, f.plan) },
		"Recover": func(t *testing.T, f *fixture) {
			found, err := Recover(context.Background(), f.plan.RunBase, f.global, f.plan.Adapter, f.plan.Ref, f.plan.Vault)
			require.NoError(t, err)
			require.True(t, found)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, `true`)
			require.NoError(t, os.MkdirAll(f.plan.RunBase, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "auth.json"), nil, 0o600))
			recov(t, f)
			require.Equal(t, "old", f.vaultAuth(t), "half-built overlay not sealed")
			_, err := os.Stat(f.plan.RunBase)
			require.True(t, os.IsNotExist(err), "overlay removed")
		})
	}
}

// A crashed overlay of an identity that was removed from the vault must not
// resurrect it.
func TestRecoverDoesNotResurrectRemovedIdentity(t *testing.T) {
	f := newFixture(t, `true`)
	require.NoError(t, os.MkdirAll(f.plan.RunBase, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "auth.json"), []byte("crashed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.RunBase, "new.txt"), []byte("shared"), 0o600))
	f.writeState(t, runlock.State{Holders: []int{deadPID(t)}, Hash: "stale"})
	require.NoError(t, f.plan.Vault.Remove(f.plan.Ref))

	found, err := Recover(context.Background(), f.plan.RunBase, f.global, f.plan.Adapter, f.plan.Ref, f.plan.Vault)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, f.plan.Vault.Exists(f.plan.Ref), "removed identity stays removed")
	_, err = os.Stat(f.plan.RunBase)
	require.True(t, os.IsNotExist(err), "overlay removed")
	got, err := os.ReadFile(filepath.Join(f.global, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "shared", string(got), "non-auth entries still promoted")
}

func TestIgnoredSignalStaysIgnored(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })

	f := newFixture(t, `kill -HUP $$; echo survived; exit 3`)
	res, out := run(t, f.plan)
	require.Equal(t, 3, res.Code)
	require.Nil(t, res.Signal)
	require.Contains(t, out, "survived")
}

func TestReraiseReturnsForQuit(t *testing.T) {
	start := time.Now()
	Reraise(syscall.SIGQUIT)
	Reraise(syscall.SIGPIPE)
	elapsed := time.Since(start)
	require.Less(t, elapsed, 50*time.Millisecond, "both Reraise calls completed quickly")
}

func TestMissingNestedAuthDirDoesNotFail(t *testing.T) {
	f := newFixture(t, `true`)
	f.plan.Adapter.Dir.AuthFiles = []string{"auth.json", "nested/dir/token.json"}
	res, err := Run(context.Background(), f.plan)
	require.NoError(t, err)
	require.Equal(t, 0, res.Code)
}

func TestPromoteFailureIsAWarning(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	f := newFixture(t, `printf new > "$FAKE_DIR/auth.json"; printf x > "$FAKE_DIR/newfile"`)
	require.NoError(t, os.Chmod(f.global, 0o500))
	t.Cleanup(func() { os.Chmod(f.global, 0o700) })

	var stderr bytes.Buffer
	f.plan.Stderr = &stderr
	res, err := Run(context.Background(), f.plan)
	require.NoError(t, err)
	require.Equal(t, 0, res.Code)
	require.Equal(t, "new", f.vaultAuth(t))
	_, err = os.Stat(f.plan.RunBase)
	require.True(t, os.IsNotExist(err), "overlay removed despite promote failure")
	require.Contains(t, stderr.String(), "could not share new non-auth files")
}

func TestUnsetEnvRemovedFromChild(t *testing.T) {
	f := newFixture(t, `echo "token=${FAKE_TOKEN:-unset}"`)
	f.plan.Adapter.UnsetEnv = []string{"FAKE_TOKEN"}
	f.plan.Env = append(os.Environ(), "FAKE_TOKEN=leak")
	_, out := run(t, f.plan)
	require.Equal(t, "token=unset\n", out)
}

// A mirrored entry replaced by rename is copied back when the global file did
// not change; whoas warns (and keeps the global copy) only when both changed.
func TestRunWarnsAboutReplacedEntries(t *testing.T) {
	f := newFixture(t, `printf edited > "$FAKE_DIR/shared.txt.lock" && mv "$FAKE_DIR/shared.txt.lock" "$FAKE_DIR/shared.txt"`)
	var stderr bytes.Buffer
	f.plan.Stderr = &stderr
	res, _ := run(t, f.plan)
	require.Zero(t, res.Code)
	require.Empty(t, stderr.String())
	g, _ := os.ReadFile(filepath.Join(f.global, "shared.txt"))
	require.Equal(t, "edited", string(g), "copied back")

	f = newFixture(t, `printf edited > "$FAKE_DIR/shared.txt.lock" && mv "$FAKE_DIR/shared.txt.lock" "$FAKE_DIR/shared.txt" && printf other-change > "$FAKE_GLOBAL/shared.txt"`)
	f.plan.Env = append(os.Environ(), "FAKE_GLOBAL="+f.global)
	stderr.Reset()
	f.plan.Stderr = &stderr
	res, _ = run(t, f.plan)
	require.Zero(t, res.Code)
	require.Equal(t, "whoas: warning: fake@work: shared.txt was replaced during the run, but "+filepath.Join(f.global, "shared.txt")+" changed too (or is not a plain file); kept the global copy\n", stderr.String())
	g, _ = os.ReadFile(filepath.Join(f.global, "shared.txt"))
	require.Equal(t, "other-change", string(g))

	// writing through the mirrored symlink changes the global file: no warning
	f = newFixture(t, `printf edited > "$FAKE_DIR/shared.txt"`)
	stderr.Reset()
	f.plan.Stderr = &stderr
	run(t, f.plan)
	require.Empty(t, stderr.String())
	g, _ = os.ReadFile(filepath.Join(f.global, "shared.txt"))
	require.Equal(t, "edited", string(g))
}
