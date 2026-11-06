package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/overlay"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/runlock"
	"github.com/S-kkipie/whoas/internal/vault"
)

// runIn runs bin with the adapter's injection pointing at dir.
func runIn(a *adapter.Adapter, bin, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	c := exec.Command(bin, append(a.InjectArgs(dir), args...)...)
	c.Env = withContext(a.ChildEnv(os.Environ(), dir))
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

func parseAccount(out []byte, jsonPath string) string { return shortLabel(accountField(out, jsonPath)) }

// accountField is the whoami JSON field, or the first non-empty line.
func accountField(out []byte, jsonPath string) string {
	if jsonPath != "" {
		return gjson.GetBytes(out, jsonPath).String()
	}
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

func shortLabel(s string) string {
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

// saveLocked saves r under its run lock: it refuses while a process holds r,
// and drops any overlay a crashed run left so that it can never be sealed
// over (or run instead of) the new session. The lock is not held during login.
func (e *env) saveLocked(w io.Writer, errw io.Writer, v *vault.Vault, r ref.Ref, files map[string]envelope.File, account string) error {
	base := e.dirs.RunBase(r.Provider, r.Alias)
	l, err := runlock.Acquire(context.Background(), base, 10*time.Second)
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := l.Read()
	if err != nil {
		return fmt.Errorf("check whether %s is in use: %w", r, err)
	}
	if live := st.Live(); len(live) > 0 {
		return fmt.Errorf("%s is in use by pid %v", r, live)
	}
	if err := overlay.Remove(base); err != nil {
		return err
	}
	if err := l.Write(runlock.State{}); err != nil {
		return err
	}
	return save(w, errw, v, r, files, account)
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
