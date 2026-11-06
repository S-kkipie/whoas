// Command fakecli imitates a CLI that keeps an OAuth session in a config dir.
// It is used only by the e2e tests.
//
// Config dir: $FAKECLI_CONFIG_DIR, else $HOME/.fakecli.
// Arguments are a sequence of operations, run left to right:
//
//	login          write auth.json for account $FAKECLI_LOGIN_AS (default "default")
//	whoami         print "account=<name>"; exit 3 when not logged in
//	               ($FAKECLI_TOKEN set → "account=envtoken", like GH_TOKEN overriding gh)
//	token          print "token=<token>"
//	refresh        rotate the token (gen+1) via write-to-temp + rename
//	dir            print "dir=<config dir>"
//	setting K V    write settings/K.txt = V (non-auth shared state)
//	sleep MS       sleep MS milliseconds
//	trap           wait for SIGTERM or SIGINT, print "trapped=TERM|INT", exit 42
//	exit N         exit with code N
//	envkey NAME    print "NAME=set" or "NAME=unset" (never the value)
//	tokwho         print "account=<part of $FAKETOK_KEY before '-'>"; exit 3 without it
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
			if os.Getenv("FAKECLI_TOKEN") != "" {
				fmt.Println("account=envtoken")
				continue
			}
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
		case "envkey":
			if len(args) < 1 {
				fail("envkey needs NAME")
			}
			state := "unset"
			if os.Getenv(args[0]) != "" {
				state = "set"
			}
			fmt.Printf("%s=%s\n", args[0], state)
			args = args[1:]
		case "tokwho":
			key := os.Getenv("FAKETOK_KEY")
			if key == "" {
				fmt.Fprintln(os.Stderr, "fakecli: no key")
				os.Exit(3)
			}
			account, _, _ := strings.Cut(key, "-")
			fmt.Printf("account=%s\n", account)
		case "exit":
			var n int
			n, _ = intArg(args, op)
			os.Exit(n)
		default:
			fail("unknown op %q", op)
		}
	}
}
