package envelope

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHash(t *testing.T) {
	a := map[string]File{"a.json": {Mode: 0o600, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("2")}}
	b := map[string]File{"dir/b": {Mode: 0o600, Data: []byte("2")}, "a.json": {Mode: 0o600, Data: []byte("1")}}
	require.Equal(t, Hash(a), Hash(b), "order independent")

	c := map[string]File{"a.json": {Mode: 0o600, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("3")}}
	require.NotEqual(t, Hash(a), Hash(c), "content sensitive")

	d := map[string]File{"a.json": {Mode: 0o644, Data: []byte("1")}, "dir/b": {Mode: 0o600, Data: []byte("2")}}
	require.NotEqual(t, Hash(a), Hash(d), "mode sensitive")
}
