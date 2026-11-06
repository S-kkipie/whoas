package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/adapter"
	"github.com/S-kkipie/whoas/internal/envelope"
	"github.com/S-kkipie/whoas/internal/ref"
)

func tokAdapter() *adapter.Adapter {
	return &adapter.Adapter{Name: "tok", Bin: "tok", Kind: adapter.KindEnvToken, Secrets: []adapter.Secret{
		{Env: "TOK_KEY", Label: "Tok API Key"},
		{Env: "TOK_HOST", Label: "Tok Host", Plain: true, Optional: true},
	}}
}

var tokRef = ref.Ref{Provider: "tok", Alias: "work"}

func TestReadSecretsFromStdin(t *testing.T) {
	got, err := readSecrets(tokAdapter(), tokRef, true, strings.NewReader("k1\n\n"), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"TOK_KEY": "k1", "TOK_HOST": ""}, got)

	_, err = readSecrets(tokAdapter(), tokRef, true, strings.NewReader(""), nil)
	require.ErrorContains(t, err, "TOK_KEY is required")
}

func TestReadSecretsAsks(t *testing.T) {
	var asked []string
	ask := func(label string, hidden bool) (string, error) {
		asked = append(asked, label+map[bool]string{true: "[hidden]", false: "[echo]"}[hidden])
		if strings.Contains(label, "TOK_KEY") {
			return " k2 ", nil
		}
		return "h.example.com", nil
	}
	got, err := readSecrets(tokAdapter(), tokRef, false, nil, ask)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"TOK_KEY": "k2", "TOK_HOST": "h.example.com"}, got)
	require.Equal(t, []string{"Tok API Key (TOK_KEY): [hidden]", "Tok Host (TOK_HOST): [echo]"}, asked)

	_, err = readSecrets(tokAdapter(), tokRef, false, nil, func(string, bool) (string, error) { return "", errors.New("no tty") })
	require.ErrorContains(t, err, "no tty")
}

func TestSecretsFromEnv(t *testing.T) {
	env := map[string]string{"TOK_KEY": "k3"}
	got, err := secretsFromEnv(tokAdapter(), tokRef, func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, map[string]string{"TOK_KEY": "k3", "TOK_HOST": ""}, got)

	_, err = secretsFromEnv(tokAdapter(), tokRef, func(string) string { return "" })
	require.ErrorContains(t, err, "not set in the environment: $TOK_KEY")
}

func TestWarnHelperKeys(t *testing.T) {
	a := &adapter.Adapter{Dir: adapter.DirSpec{HelperKeys: []string{"credsStore", "credHelpers"}}}
	r := ref.Ref{Provider: "docker", Alias: "work"}
	var out bytes.Buffer
	warnHelperKeys(&out, r, a, map[string]envelope.File{
		"config.json": {Data: []byte(`{"auths":{"ghcr.io":{}},"credHelpers":{"registry.example.com":"gcloud"},"credsStore":"desktop"}`)},
	})
	require.Equal(t, "warning: docker@work: config.json delegates credentials to helpers (credsStore: desktop; credHelpers: registry.example.com); those stay in the helper's own store and are not isolated by whoas\n", out.String())

	out.Reset()
	warnHelperKeys(&out, r, a, map[string]envelope.File{"config.json": {Data: []byte(`{"auths":{"ghcr.io":{"auth":"x"}}}`)}})
	require.Empty(t, out.String())
}

func patternAdapter() *adapter.Adapter {
	a := tokAdapter()
	a.Secrets[0].Pattern = "^tk_[a-z]+$"
	a.Secrets[1].Pattern = "^[a-z.]+$"
	return a
}

func TestReadSecretsPattern(t *testing.T) {
	_, err := readSecrets(patternAdapter(), tokRef, true, strings.NewReader("tk_fragment9\n"), nil)
	require.EqualError(t, err, "tok@work: Tok API Key (TOK_KEY) does not look right (expected format: ^tk_[a-z]+$); paste the whole value on one line")
	require.NotContains(t, err.Error(), "fragment9")

	got, err := readSecrets(patternAdapter(), tokRef, true, strings.NewReader("tk_ok\n\n"), nil)
	require.NoError(t, err, "valid value and an empty optional value pass")
	require.Equal(t, map[string]string{"TOK_KEY": "tk_ok", "TOK_HOST": ""}, got)

	ask := func(label string, _ bool) (string, error) {
		if strings.Contains(label, "TOK_KEY") {
			return "tk_good", nil
		}
		return "BAD HOST", nil
	}
	_, err = readSecrets(patternAdapter(), tokRef, false, nil, ask)
	require.ErrorContains(t, err, "Tok Host (TOK_HOST) does not look right")
	require.NotContains(t, err.Error(), "BAD HOST")
}

func TestSecretsFromEnvPattern(t *testing.T) {
	_, err := secretsFromEnv(patternAdapter(), tokRef, func(string) string { return "tk_Secret1" })
	require.EqualError(t, err, "tok@work: Tok API Key (TOK_KEY) does not look right (expected format: ^tk_[a-z]+$); paste the whole value on one line")
	require.NotContains(t, err.Error(), "Secret1")

	got, err := secretsFromEnv(patternAdapter(), tokRef, func(k string) string { return map[string]string{"TOK_KEY": "tk_fine"}[k] })
	require.NoError(t, err)
	require.Equal(t, "tk_fine", got["TOK_KEY"])
}
