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
//	renamewrite P V  write $HOME/P.tmp = V, then rename it over $HOME/P
//	sleep MS       sleep MS milliseconds
//	trap           wait for SIGTERM or SIGINT, print "trapped=TERM|INT", exit 42
//	exit N         exit with code N
//	envkey NAME    print "NAME=set" or "NAME=unset" (never the value)
//	env NAME       print "NAME=<value>" (only for non-secret variables)
//	prefix NAME    print "NAME=<value up to its first '-'>" or "NAME=unset"
//	setup-token    create $HOME/setup-token-ran, print "setup-token ran", exit
//	               $FAKECLI_SETUP_EXIT (default 0), like claude setup-token
//	tokwho         print "account=<part of $FAKETOK_KEY before '-'>"; exit 3 without it
//
// Named claude or codex, it is a fake agent instead: it appends its argv to
// $HOME/agent-argv.log and keeps MCP servers as files in $HOME/.fake-<name>
// ("mcp get|remove NAME", claude "mcp add-json [--scope S] NAME JSON",
// codex "mcp add NAME -- CMD...").
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

// agentMain imitates the MCP commands of claude and codex.
func agentMain(name string, args []string) {
	home := os.Getenv("HOME")
	f, err := os.OpenFile(filepath.Join(home, "agent-argv.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fail("%v", err)
	}
	fmt.Fprintln(f, name+" "+strings.Join(args, " "))
	f.Close()
	if len(args) < 3 || args[0] != "mcp" {
		fail("%s: unsupported arguments %q", name, args)
	}
	dir := filepath.Join(home, ".fake-"+name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("%v", err)
	}
	var server, entry string
	switch args[1] {
	case "get", "remove":
		server = args[len(args)-1]
		if _, err := os.Stat(filepath.Join(dir, server)); err != nil {
			fmt.Fprintf(os.Stderr, "No MCP server named %q\n", server)
			os.Exit(1)
		}
		if args[1] == "remove" {
			if err := os.Remove(filepath.Join(dir, server)); err != nil {
				fail("%v", err)
			}
		}
		return
	case "add-json":
		server, entry = args[len(args)-2], args[len(args)-1]
	case "add":
		server = args[2]
		if len(args) < 5 || args[3] != "--" {
			fail("codex mcp add NAME -- CMD...")
		}
		entry = strings.Join(args[4:], " ")
	default:
		fail("%s: unsupported mcp command %q", name, args[1])
	}
	p := filepath.Join(dir, server)
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(os.Stderr, "MCP server %s already exists\n", server)
		os.Exit(1)
	}
	if err := os.WriteFile(p, []byte(entry+"\n"), 0o600); err != nil {
		fail("%v", err)
	}
}

func main() {
	if name := filepath.Base(os.Args[0]); name == "claude" || name == "codex" {
		agentMain(name, os.Args[1:])
		return
	}
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
		case "renamewrite":
			if len(args) < 2 {
				fail("renamewrite needs PATH VALUE")
			}
			p := filepath.Join(os.Getenv("HOME"), filepath.FromSlash(args[0]))
			if err := os.WriteFile(p+".tmp", []byte(args[1]), 0o600); err != nil {
				fail("%v", err)
			}
			if err := os.Rename(p+".tmp", p); err != nil {
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
		case "env":
			if len(args) < 1 {
				fail("env needs NAME")
			}
			fmt.Printf("%s=%s\n", args[0], os.Getenv(args[0]))
			args = args[1:]
		case "prefix":
			if len(args) < 1 {
				fail("prefix needs NAME")
			}
			v, _, _ := strings.Cut(os.Getenv(args[0]), "-")
			if v == "" {
				v = "unset"
			}
			fmt.Printf("%s=%s\n", args[0], v)
			args = args[1:]
		case "setup-token":
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "setup-token-ran"), nil, 0o600); err != nil {
				fail("%v", err)
			}
			fmt.Println("setup-token ran")
			code, _ := strconv.Atoi(os.Getenv("FAKECLI_SETUP_EXIT"))
			os.Exit(code)
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
