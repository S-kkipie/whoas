package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveVersion(t *testing.T) {
	for _, c := range []struct{ ldflags, module, want string }{
		{"v0.3.0", "(devel)", "v0.3.0"}, // release build: -ldflags -X
		{"v0.3.0", "v0.2.0", "v0.3.0"},  // -ldflags win over the module version
		{"dev", "v0.3.0", "v0.3.0"},     // go install …/cmd/whoas@v0.3.0
		{"dev", "v0.0.0-20261106120000-b98abd9c0e1f+dirty", "v0.0.0-20261106120000-b98abd9c0e1f+dirty"}, // go build in a checkout
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"}, // no build info
	} {
		require.Equal(t, c.want, resolveVersion(c.ldflags, c.module), "%+v", c)
	}
}
