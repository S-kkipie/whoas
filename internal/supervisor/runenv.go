package supervisor

import (
	"context"
	"io"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/vault"
)

// EnvPlan is one env-token run: no overlay, no lock, no seal-on-write; the
// identity's secrets exist only in the child's environment.
type EnvPlan struct {
	Ref       ref.Ref
	Adapter   *adapter.Adapter
	Bin       string   // absolute path of the real CLI
	Args      []string // user args, passed verbatim
	Vault     *vault.Vault
	Decrypter vault.Decrypter
	Env       []string // base environment for the child
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// RunEnv decrypts the identity's secrets and runs the CLI with them injected.
func RunEnv(ctx context.Context, p EnvPlan) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	env, err := p.Vault.Load(p.Ref, p.Decrypter)
	if err != nil {
		return Result{}, err
	}
	return spawnCmd(p.Bin, p.Args, p.Adapter.SecretEnv(p.Env, env.Secrets()), p.Stdin, p.Stdout, p.Stderr)
}
