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
