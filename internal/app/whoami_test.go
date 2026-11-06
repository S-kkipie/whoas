//go:build unix

package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
)

// The account label is stored in plaintext metadata and printed by ls and the
// banner, so a whoami that echoes a secret must not become the label.
func TestWhoamiWithSecretsDropsSecretLabels(t *testing.T) {
	values := map[string]string{"TOK_KEY": "k-secret-123", "TOK_HOST": "h.example.com"}
	label := func(script string) string {
		a := tokAdapter()
		a.Bin = "sh"
		a.Whoami = adapter.Whoami{Cmd: []string{"sh", "-c", script}}
		return whoamiWithSecrets(a, "/bin/sh", values)
	}
	require.Equal(t, "account=work host=h.example.com", label(`echo "account=work host=$TOK_HOST"`), "plain secrets may show")
	require.Equal(t, "", label(`echo "key: $TOK_KEY"`))
	// the secret starts inside the 80-char label but is cut off by it
	require.Equal(t, "", label(`echo "`+strings.Repeat("x", 75)+`$TOK_KEY"`))
}
