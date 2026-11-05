package app

import (
	"errors"
	"fmt"
	"os"
)

// shimMain runs when whoas is invoked through a shim named after a CLI.
// With an identity it supervises; without one it becomes the real CLI.
func shimMain(bin string, args []string) int {
	e, err := loadEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return exitWhoasError
	}
	if a, ok := e.registry.ByBin(bin); ok {
		code, err := runProvider(e, runRequest{provider: a.Name, args: args})
		if err == nil {
			return code
		}
		if !errors.Is(err, errPassthrough) {
			fmt.Fprintln(os.Stderr, "whoas:", err)
			return exitCodeFor(err)
		}
	}
	real, err := e.realBin(bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return exitNotFound
	}
	return execReplace(real, append([]string{bin}, args...), os.Environ())
}
