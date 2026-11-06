// Package keysource picks how vault blobs get decrypted: through a running,
// unlocked agent, or directly with the master key from the keystore. For
// slow stores (WSL bridge, passphrase) it also starts and loads an agent so
// the next invocation is fast.
package keysource

import (
	"fmt"

	"github.com/S-kkipie/whoas/internal/agent"
	"github.com/S-kkipie/whoas/internal/keystore"
	"github.com/S-kkipie/whoas/internal/vault"
)

// Decrypter returns the decrypter to use. self is the whoas executable used
// to spawn an agent; "" disables spawning.
func Decrypter(socket string, store keystore.Store, self string) (vault.Decrypter, error) {
	c := agent.NewClient(socket)
	if locked, err := c.Status(); err == nil && !locked {
		return c, nil
	}
	secret, err := store.Get()
	if err != nil {
		return nil, fmt.Errorf("master key (%s): %w", store.Name(), err)
	}
	d, err := vault.NewIdentityDecrypter(secret)
	if err != nil {
		return nil, err
	}
	if store.Slow() {
		if _, err := c.Status(); err != nil && self != "" {
			_ = agent.Spawn(self, socket)
		}
		_ = c.Load(secret, store.TTL()) // best effort: caching only
	}
	return d, nil
}
