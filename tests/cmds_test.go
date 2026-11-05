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
	"syscall"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

// fakeAdapter is installed as a user adapter in every script.
const fakeAdapter = `name = "fakecli"
bin  = "fakecli"
kind = "dir-overlay"
unset_env = ["FAKECLI_TOKEN"]

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
	// sigterm-bg — send SIGTERM to every background command of the script.
	"sigterm-bg": func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) != 0 {
			ts.Fatalf("usage: sigterm-bg")
		}
		for _, c := range ts.BackgroundCmds() {
			if c.Process != nil {
				ts.Check(c.Process.Signal(syscall.SIGTERM))
			}
		}
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
