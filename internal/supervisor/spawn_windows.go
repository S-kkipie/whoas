//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func (p *Plan) spawn() (Result, error) {
	return spawnCmd(p.Bin, p.Args, p.Env, p.Stdin, p.Stdout, p.Stderr)
}

func spawnCmd(string, []string, []string, io.Reader, io.Writer, io.Writer) (Result, error) {
	return Result{}, fmt.Errorf("%w: %w", ErrStart, errors.New("running CLIs is not supported on native Windows yet; use WSL"))
}

// Reraise is a no-op on Windows.
func Reraise(os.Signal) {}
