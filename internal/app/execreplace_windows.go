//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execReplace runs path as a child and returns its exit code (Windows has no exec).
func execReplace(path string, argv, env []string) int {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas: exec", path+":", err)
		return exitExecFailed
	}
	return 0
}
