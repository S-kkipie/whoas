package app

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/S-kkipie/whoas/internal/adapter"
)

func init() { register(newProvidersCmd) }

// providerRow is one line of `whoas providers`.
type providerRow struct {
	Name, Bin, Kind, Source, Installed string
}

// providerRows lists adapters; unless all is set, only installed CLIs (and
// mcp adapters, which need no binary) are kept and hidden counts the rest.
func providerRows(reg *adapter.Registry, find func(bin string) (string, error), all bool) (rows []providerRow, hidden int) {
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		if a.Kind == adapter.KindMCP {
			rows = append(rows, providerRow{Name: a.Name, Bin: "-", Kind: string(a.Kind), Source: reg.Source(n), Installed: "n/a"})
			continue
		}
		installed := "-"
		if p, err := find(a.Bin); err == nil {
			installed = p
		}
		if installed == "-" && !all {
			hidden++
			continue
		}
		kind := string(a.Kind)
		if a.Experimental {
			kind += " (experimental)"
		}
		rows = append(rows, providerRow{Name: a.Name, Bin: a.Bin, Kind: kind, Source: reg.Source(n), Installed: installed})
	}
	return rows, hidden
}

func newProvidersCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List the adapters for installed CLIs and MCP servers (--all for every adapter)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := loadEnv()
			if err != nil {
				return err
			}
			rows, hidden := providerRows(e.registry, e.binIndex().Find, all)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "PROVIDER\tBIN\tKIND\tSOURCE\tINSTALLED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Bin, r.Kind, r.Source, r.Installed)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if hidden > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "+%d more (whoas providers --all)\n", hidden)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list every adapter, installed or not")
	return cmd
}
