package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLeadingProfile(t *testing.T) {
	cases := []struct {
		in      []string
		profile string
		rest    []string
		err     bool
	}{
		{[]string{"gh", "repo", "list"}, "", []string{"gh", "repo", "list"}, false},
		{[]string{"-p", "work", "gh", "-p", "x"}, "work", []string{"gh", "-p", "x"}, false},
		{[]string{"--profile", "work", "gh"}, "work", []string{"gh"}, false},
		{[]string{"--profile=work", "gh"}, "work", []string{"gh"}, false},
		{[]string{"-p"}, "", nil, true},
		{nil, "", nil, false},
	}
	for _, c := range cases {
		p, rest, err := leadingProfile(c.in)
		if c.err {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, c.profile, p)
		require.Equal(t, c.rest, rest)
	}
}
