//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"
)

func (p *Plan) spawn() (Result, error) {
	return Result{}, fmt.Errorf("%w: %w", ErrStart, errors.New("running CLIs is not supported on native Windows yet; use WSL"))
}

// Reraise is a no-op on Windows.
func Reraise(os.Signal) {}
