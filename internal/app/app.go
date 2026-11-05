// Package app wires whoas subcommands and process-level dispatch.
package app

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time:
// -ldflags "-X github.com/S-kkipie/whoas/internal/app.Version=v1.2.3".
var Version = "dev"

var factories []func() *cobra.Command

// register adds a subcommand constructor; each command file calls it from init.
func register(f func() *cobra.Command) { factories = append(factories, f) }

// exitError carries a specific exit code out of a cobra RunE.
// A nil err means the message was already printed.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "whoas",
		Short:         "Run CLIs as a chosen identity",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	for _, f := range factories {
		root.AddCommand(f())
	}
	return root
}

// runCobra executes subcommands and maps errors to exit codes.
func runCobra(args []string) int {
	root := newRoot()
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(os.Stderr, "whoas:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "whoas:", err)
	return 1
}

// Main is the process entry point and returns the exit code.
// Task 14 replaces this with provider/shim dispatch.
func Main(args []string) int {
	return runCobra(args[1:])
}
