//go:build keyring_integration

package keystore

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// TestOSKeyringRoundTrip hits the real OS keyring under a throwaway service
// name, so it never touches the user's whoas master key.
func TestOSKeyringRoundTrip(t *testing.T) {
	service = fmt.Sprintf("whoas-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = keyring.Delete(service, account)
		service = "whoas"
	})
	s := OS()
	_, err := s.Get()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.Set("AGE-SECRET-KEY-1TEST"))
	got, err := s.Get()
	require.NoError(t, err)
	require.Equal(t, "AGE-SECRET-KEY-1TEST", got)
}
