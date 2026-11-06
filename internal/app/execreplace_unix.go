//go:build unix

package app

import (
	"fmt"
	"os"
	"syscall"
)

// execReplace replaces this process with path (zero overhead passthrough).
// It returns only on failure.
func execReplace(path string, argv, env []string) int {
	err := syscall.Exec(path, argv, env)
	fmt.Fprintln(os.Stderr, "whoas: exec", path+":", err)
	return exitExecFailed
}
