package app

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func init() { register(newVersionCmd) }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the whoas version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "whoas", version())
			return nil
		},
	}
}

// version is the running build's version: Version when -ldflags set it,
// else the module version the go command recorded (go install …@v0.3.0).
func version() string {
	module := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		module = info.Main.Version
	}
	return resolveVersion(Version, module)
}

// resolveVersion prefers the -ldflags value; with the "dev" default it falls
// back to module unless that is empty or "(devel)".
func resolveVersion(ldflags, module string) string {
	if ldflags != "dev" || module == "" || module == "(devel)" {
		return ldflags
	}
	return module
}
