//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
	"github.com/S-kkipie/whoas/internal/vault"
)

func TestRunEnvInjectsSecrets(t *testing.T) {
	root := t.TempDir()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipient := filepath.Join(root, "recipient")
	require.NoError(t, os.WriteFile(recipient, []byte(id.Recipient().String()), 0o644))
	v, err := vault.Open(recipient, filepath.Join(root, "vault"))
	require.NoError(t, err)
	dec, err := vault.NewIdentityDecrypter(id.String())
	require.NoError(t, err)

	a := &adapter.Adapter{
		Name: "tok", Bin: "sh", Kind: adapter.KindEnvToken,
		UnsetEnv: []string{"TOK_LEGACY"},
		Secrets:  []adapter.Secret{{Env: "TOK_KEY"}},
	}
	r := ref.Ref{Provider: "tok", Alias: "work"}
	require.NoError(t, v.Seal(r, envelope.FromSecrets("tok", map[string]string{"TOK_KEY": "k-work"})))

	var out bytes.Buffer
	res, err := RunEnv(context.Background(), EnvPlan{
		Ref: r, Adapter: a, Bin: "/bin/sh",
		Args:  []string{"-c", `echo "key=$TOK_KEY legacy=${TOK_LEGACY:-unset}"; exit 4`},
		Vault: v, Decrypter: dec,
		Env:    append(os.Environ(), "TOK_KEY=inherited", "TOK_LEGACY=old"),
		Stdout: &out,
	})
	require.NoError(t, err)
	require.Equal(t, 4, res.Code)
	require.Equal(t, "key=k-work legacy=unset\n", out.String())
}

func TestRunEnvMissingIdentity(t *testing.T) {
	root := t.TempDir()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipient := filepath.Join(root, "recipient")
	require.NoError(t, os.WriteFile(recipient, []byte(id.Recipient().String()), 0o644))
	v, err := vault.Open(recipient, filepath.Join(root, "vault"))
	require.NoError(t, err)
	dec, err := vault.NewIdentityDecrypter(id.String())
	require.NoError(t, err)

	_, err = RunEnv(context.Background(), EnvPlan{
		Ref: ref.Ref{Provider: "tok", Alias: "nope"}, Adapter: &adapter.Adapter{Kind: adapter.KindEnvToken},
		Bin: "/bin/true", Vault: v, Decrypter: dec,
	})
	require.ErrorIs(t, err, vault.ErrNotFound)
}

// An identity whose envelope lacks a required secret (e.g. sealed by an older
// dir-overlay adapter of the same name) must not run as the ambient account.
func TestRunEnvMissingRequiredSecret(t *testing.T) {
	root := t.TempDir()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipient := filepath.Join(root, "recipient")
	require.NoError(t, os.WriteFile(recipient, []byte(id.Recipient().String()), 0o644))
	v, err := vault.Open(recipient, filepath.Join(root, "vault"))
	require.NoError(t, err)
	dec, err := vault.NewIdentityDecrypter(id.String())
	require.NoError(t, err)

	a := &adapter.Adapter{
		Name: "tok", Bin: "sh", Kind: adapter.KindEnvToken,
		Secrets: []adapter.Secret{{Env: "TOK_KEY"}, {Env: "TOK_HOST", Optional: true}},
	}
	r := ref.Ref{Provider: "tok", Alias: "work"}
	require.NoError(t, v.Seal(r, envelope.New("tok", map[string]envelope.File{"auth.json": {Mode: 0o600, Data: []byte("{}")}})))

	var out bytes.Buffer
	marker := filepath.Join(root, "spawned")
	_, err = RunEnv(context.Background(), EnvPlan{
		Ref: r, Adapter: a, Bin: "/bin/sh",
		Args:  []string{"-c", `touch "$1"; echo "key=$TOK_KEY"`, "sh", marker},
		Vault: v, Decrypter: dec,
		Env:    append(os.Environ(), "TOK_KEY=ambient"),
		Stdout: &out,
	})
	require.EqualError(t, err, "tok@work: identity has no TOK_KEY; remove and re-add it: whoas rm tok@work && whoas add tok work")
	require.Empty(t, out.String())
	require.NoFileExists(t, marker, "the CLI must not be spawned")

	// a missing optional secret is fine
	require.NoError(t, v.Seal(r, envelope.FromSecrets("tok", map[string]string{"TOK_KEY": "k-work"})))
	res, err := RunEnv(context.Background(), EnvPlan{
		Ref: r, Adapter: a, Bin: "/bin/sh",
		Args:  []string{"-c", `echo "key=$TOK_KEY host=${TOK_HOST:-unset}"`},
		Vault: v, Decrypter: dec, Env: os.Environ(), Stdout: &out,
	})
	require.NoError(t, err)
	require.Zero(t, res.Code)
	require.Equal(t, "key=k-work host=unset\n", out.String())
}
