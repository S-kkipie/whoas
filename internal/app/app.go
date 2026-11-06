// Package app wires whoas subcommands and process-level dispatch.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/ref"
)

// Version is set at build time (see version() for the fallback):
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
//
//	argv[0] != whoas            → shim for that CLI
//	whoas [-p P] provider[@a] … → run the CLI as an identity
//	whoas <subcommand> …        → cobra
func Main(args []string) int {
	name := strings.TrimSuffix(filepath.Base(args[0]), ".exe")
	if name != "whoas" {
		return shimMain(name, args[1:])
	}
	profile, rest, err := leadingProfile(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "whoas:", err)
		return 2
	}
	if len(rest) > 0 {
		if _, _, err := ref.Split(rest[0]); err == nil {
			return execMain(profile, rest[0], rest[1:])
		}
	}
	if profile != "" {
		fmt.Fprintln(os.Stderr, "whoas: -p must be followed by a provider (or use: whoas run -p NAME -- cmd)")
		return 2
	}
	return runCobra(args[1:])
}

// leadingProfile strips "-p NAME", "--profile NAME" or "--profile=NAME"
// placed before the provider.
func leadingProfile(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", args, nil
	}
	switch a := args[0]; {
	case a == "-p" || a == "--profile":
		if len(args) < 2 {
			return "", nil, fmt.Errorf("%s needs a profile name", a)
		}
		return args[1], args[2:], nil
	case strings.HasPrefix(a, "--profile="):
		return strings.TrimPrefix(a, "--profile="), args[1:], nil
	}
	return "", args, nil
}
