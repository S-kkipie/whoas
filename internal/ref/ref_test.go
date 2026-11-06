package ref

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in, provider, alias string
		ok                  bool
	}{
		{"gh", "gh", "", true},
		{"vercel@work", "vercel", "work", true},
		{"mcp/posthog@acme", "mcp/posthog", "acme", true},
		{"gh@", "", "", false},
		{"GH@work", "", "", false},
		{"run", "", "", false},
		{"add@x", "", "", false},
		{"../etc@x", "", "", false},
		{"gh@../x", "", "", false},
		{"__complete", "", "", false},
		{"mcp", "", "", false},
		{"mcp-posthog@acme", "mcp-posthog", "acme", true},
	}
	for _, c := range cases {
		p, a, err := Split(c.in)
		if !c.ok {
			require.Error(t, err, c.in)
			continue
		}
		require.NoError(t, err, c.in)
		require.Equal(t, c.provider, p, c.in)
		require.Equal(t, c.alias, a, c.in)
	}
}

func TestParse(t *testing.T) {
	r, err := Parse("gcloud@acme")
	require.NoError(t, err)
	require.Equal(t, Ref{Provider: "gcloud", Alias: "acme"}, r)
	require.Equal(t, "gcloud@acme", r.String())

	_, err = Parse("gcloud")
	require.ErrorContains(t, err, "expected provider@alias")
}
