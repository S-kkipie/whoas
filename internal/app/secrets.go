package app

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"golang.org/x/term"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
)

// askFunc asks for one value; hidden disables echo.
type askFunc func(label string, hidden bool) (string, error)

// readSecrets collects a value for each of a's secrets: one line per secret,
// in declaration order, from in when fromStdin is set; otherwise by asking.
func readSecrets(a *adapter.Adapter, fromStdin bool, in io.Reader, ask askFunc) (map[string]string, error) {
	values := map[string]string{}
	var sc *bufio.Scanner
	if fromStdin {
		sc = bufio.NewScanner(in)
	}
	for _, s := range a.Secrets {
		var v string
		if fromStdin {
			if sc.Scan() {
				v = sc.Text()
			} else if err := sc.Err(); err != nil {
				return nil, err
			}
		} else {
			var err error
			if v, err = ask(s.PromptLabel()+": ", !s.Plain); err != nil {
				return nil, err
			}
		}
		v = strings.TrimSpace(v)
		if v == "" && !s.Optional {
			return nil, fmt.Errorf("%s is required", s.Env)
		}
		values[s.Env] = v
	}
	return values, nil
}

// secretsFromEnv reads a's secrets from the environment (whoas import).
func secretsFromEnv(a *adapter.Adapter, getenv func(string) string) (map[string]string, error) {
	values := map[string]string{}
	var missing []string
	for _, s := range a.Secrets {
		v := getenv(s.Env)
		if v == "" && !s.Optional {
			missing = append(missing, "$"+s.Env)
		}
		values[s.Env] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not set in the environment: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

// ttyAsk prompts on the controlling terminal, so it works with stdin piped.
func ttyAsk(label string, hidden bool) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to ask for secrets (use --stdin): %w", err)
	}
	defer tty.Close()
	fmt.Fprint(tty, label)
	if hidden {
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return string(b), err
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// whoamiWithSecrets returns the account label of an env-token identity, or
// "" when the adapter has no whoami, it fails, or its output would put a
// (non-plain) secret in the label.
func whoamiWithSecrets(a *adapter.Adapter, bin string, values map[string]string) string {
	if len(a.Whoami.Cmd) == 0 {
		return ""
	}
	var out bytes.Buffer
	c := exec.Command(bin, a.Whoami.Cmd[1:]...)
	c.Env = a.SecretEnv(os.Environ(), values)
	c.Stdout, c.Stderr = &out, io.Discard
	if err := c.Run(); err != nil {
		return ""
	}
	field := accountField(out.Bytes(), a.Whoami.JSON)
	for _, s := range a.Secrets {
		if v := values[s.Env]; !s.Plain && v != "" && strings.Contains(field, v) {
			return ""
		}
	}
	return shortLabel(field)
}

// warnHelperKeys reports credentials that a captured auth file delegates to
// stores outside whoas (docker credential helpers).
func warnHelperKeys(w io.Writer, r ref.Ref, a *adapter.Adapter, files map[string]envelope.File) {
	if len(a.Dir.HelperKeys) == 0 {
		return
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data := files[p].Data
		if !gjson.ValidBytes(data) {
			continue
		}
		var found []string
		for _, k := range a.Dir.HelperKeys {
			res := gjson.GetBytes(data, k)
			if res.IsObject() {
				var names []string
				res.ForEach(func(key, _ gjson.Result) bool {
					names = append(names, key.String())
					return true
				})
				if len(names) > 0 {
					found = append(found, k+": "+strings.Join(names, ", "))
				}
			} else if res.Exists() && res.String() != "" {
				found = append(found, k+": "+res.String())
			}
		}
		if len(found) > 0 {
			fmt.Fprintf(w, "warning: %s: %s delegates credentials to helpers (%s); those stay in the helper's own store and are not isolated by whoas\n", r, p, strings.Join(found, "; "))
		}
	}
}
