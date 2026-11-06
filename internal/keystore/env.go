package keystore

import (
	"errors"
	"time"
)

// envStore reads the key from WHOAS_MASTER_KEY (CI, containers, tests).
type envStore struct{ key string }

func (s envStore) Name() string         { return "env (WHOAS_MASTER_KEY)" }
func (s envStore) Get() (string, error) { return s.key, nil }
func (s envStore) Set(string) error     { return errors.New("WHOAS_MASTER_KEY is read-only") }
func (s envStore) Slow() bool           { return false }
func (s envStore) TTL() time.Duration   { return 0 }
