package resolve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/S-kkipie/whoas/internal/config"
	"github.com/S-kkipie/whoas/internal/project"
)

func cfg() *config.Config {
	return &config.Config{
		DefaultProfile: "personal",
		Profiles: map[string]map[string]string{
			"work":     {"vercel": "work", "gh": "corp"},
			"personal": {"vercel": "me"},
		},
	}
}

func TestPrecedence(t *testing.T) {
	proj := &project.File{Path: "/p/.whoas.yaml", Profile: "work", Overrides: map[string]string{"vercel": "client-x"}}
	cases := []struct {
		name   string
		in     Input
		alias  string
		source string
	}{
		{"explicit wins", Input{Alias: "x", ProfileFlag: "work", Project: proj}, "x", "explicit"},
		{"flag beats env and project", Input{ProfileFlag: "work", EnvProfile: "personal", Project: proj}, "work", "-p flag (profile work)"},
		{"env beats project", Input{EnvProfile: "personal", Project: proj}, "me", "WHOAS_PROFILE (profile personal)"},
		{"project override beats project profile", Input{Project: proj}, "client-x", "project /p/.whoas.yaml"},
		{"default profile last", Input{}, "me", "default profile (profile personal)"},
	}
	for _, c := range cases {
		c.in.Provider, c.in.Config = "vercel", cfg()
		res, err := Resolve(c.in)
		require.NoError(t, err, c.name)
		require.Equal(t, c.alias, res.Ref.Alias, c.name)
		require.Equal(t, c.source, res.Source, c.name)
	}
}

func TestNoFallthrough(t *testing.T) {
	// -p work has no supabase identity: unresolved, even though nothing else is set.
	_, err := Resolve(Input{Provider: "supabase", ProfileFlag: "work", Config: cfg()})
	require.ErrorIs(t, err, ErrUnresolved)

	proj := &project.File{Path: "/p/.whoas.yaml", Profile: "work"}
	res, err := Resolve(Input{Provider: "gh", Project: proj, Config: cfg()})
	require.NoError(t, err)
	require.Equal(t, "corp", res.Ref.Alias)
}

func TestUnknownProfileIsHardError(t *testing.T) {
	_, err := Resolve(Input{Provider: "vercel", EnvProfile: "typo", Config: cfg()})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnresolved)
}

func TestNothingConfigured(t *testing.T) {
	_, err := Resolve(Input{Provider: "vercel", Config: &config.Config{}})
	require.ErrorIs(t, err, ErrUnresolved)
}
