package app

import "strings"

// setEnv returns env with every KEY=VALUE in kvs set, removing earlier
// entries for those keys (exec passes duplicates through as-is).
func setEnv(env []string, kvs ...string) []string {
	drop := map[string]bool{}
	for _, kv := range kvs {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	out := make([]string, 0, len(env)+len(kvs))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, kvs...)
}
