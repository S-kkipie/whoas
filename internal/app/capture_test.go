package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAccount(t *testing.T) {
	require.Equal(t, "octocat", parseAccount([]byte(`{"login":"octocat","id":1}`), "login"))
	require.Equal(t, "a@b.c", parseAccount([]byte("\n  a@b.c  \nmore\n"), ""))
	require.Equal(t, "", parseAccount([]byte(`{}`), "email"))
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	require.Len(t, parseAccount(long, ""), 80)
}
