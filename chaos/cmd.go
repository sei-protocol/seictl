// Package chaos registers the `seictl chaos` verb tree: render Chaos-Mesh
// fault manifests from the sei-k8s-controller harness catalog so the
// manifests an engineer commits to a GitOps workspace are the same ones the
// nightly integration suite injects.
package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sei-protocol/sei-k8s-controller/harness/faults"
	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// Cmd is the `seictl chaos` command tree.
var Cmd = cli.Command{
	Name:  "chaos",
	Usage: "Render Chaos-Mesh fault manifests from the harness catalog",
	Commands: []*cli.Command{
		&listCmd,
		&renderCmd,
	},
}

var listCmd = cli.Command{
	Name:  "list",
	Usage: "List the fault scenarios in the catalog",
	Description: "Prints one line per scenario: name, Chaos-Mesh kind, whether " +
		"the fault is one-shot (no --duration), whether it hits one validator or the whole mesh, and a summary. Use --output json " +
		"for a machine-readable catalog.",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "text", Usage: "text|json"},
	},
	Action: func(_ context.Context, c *cli.Command) error {
		if err := list(os.Stdout, c.String("output")); err != nil {
			cliutil.EmitStatus(os.Stderr, err)
			return cli.Exit("", 1)
		}
		return nil
	},
}

var renderCmd = cli.Command{
	Name:      "render",
	Usage:     "Render one fault scenario as a Chaos-Mesh manifest",
	ArgsUsage: "<fault>",
	Description: "Renders the named scenario against a SeiNetwork. The selector " +
		"targets pods labelled sei.io/nodedeployment=<chain-id> in --namespace; " +
		"one-validator scenarios use mode: one (Chaos-Mesh picks the victim) except network-partition, which isolates <chain-id>-0. " +
		"Resources are named <fault>-<run-id> and labelled sei.io/harness-run=<run-id> " +
		"so metrics and teardown key on the run. Duration-bearing faults require " +
		"--duration; one-shot faults (see `seictl chaos list`) reject it.",
	Arguments: []cli.Argument{&cli.StringArg{Name: "fault"}},
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "chain-id", Usage: "SeiNetwork name / sei.io/nodedeployment label value", Required: true},
		&cli.StringFlag{Name: "run-id", Usage: "Run token; suffixes resource names and sets sei.io/harness-run", Required: true},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace of the SeiNetwork pods", Required: true},
		&cli.StringFlag{Name: "duration", Usage: "Fault duration (Go duration, e.g. 5m); required unless the fault is one-shot"},
	},
	Action: func(_ context.Context, c *cli.Command) error {
		out, err := Render(c.StringArg("fault"), faults.Params{
			ChainID:   c.String("chain-id"),
			RunID:     c.String("run-id"),
			Namespace: c.String("namespace"),
			Duration:  c.String("duration"),
		})
		if err != nil {
			cliutil.EmitStatus(os.Stderr, cliutil.UsageError("%s", err.Error()))
			return cli.Exit("", 1)
		}
		_, err = os.Stdout.Write(out)
		return err
	},
}

// Render validates the CLI-level contract (one-shot faults take no
// duration, the rest need a positive one, identifiers are DNS-1123) and
// emits the fault manifest. The `seictl mcp` chaos_render tool shares it.
func Render(name string, p faults.Params) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("fault argument required: seictl chaos render <fault>; one of %s", strings.Join(faults.Names(), ", "))
	}
	f, err := faults.Lookup(name)
	if err != nil {
		return nil, err
	}
	if err := cliutil.RequireHarnessNames(p.ChainID, p.RunID, name); err != nil {
		return nil, err
	}
	if f.OneShot && p.Duration != "" {
		return nil, fmt.Errorf("fault %q is one-shot; --duration does not apply", name)
	}
	if !f.OneShot {
		if p.Duration == "" {
			return nil, fmt.Errorf("fault %q needs --duration", name)
		}
		d, err := time.ParseDuration(p.Duration)
		if err != nil {
			return nil, fmt.Errorf("--duration: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("--duration must be positive, got %s", p.Duration)
		}
	}
	return f.Render(p)
}

// CatalogEntry is the stable machine-readable shape of one fault. It is
// decoupled from faults.Fault so an upstream field rename cannot change the
// JSON this command emits.
type CatalogEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	OneShot  bool   `json:"oneShot"`
	MeshWide bool   `json:"meshWide"`
	Summary  string `json:"summary"`
}

// Catalog is the fault catalog in the shape `chaos list --output json` emits.
func Catalog() []CatalogEntry {
	entries := make([]CatalogEntry, 0, len(faults.Catalog))
	for _, f := range faults.Catalog {
		entries = append(entries, CatalogEntry{Name: f.Name, Kind: f.Kind, OneShot: f.OneShot, MeshWide: f.MeshWide, Summary: f.Summary})
	}
	return entries
}

func list(w io.Writer, format string) error {
	switch format {
	case "text":
		for _, e := range Catalog() {
			mode := "duration"
			if e.OneShot {
				mode = "one-shot"
			}
			scope := "one-validator"
			if e.MeshWide {
				scope = "mesh-wide"
			}
			if _, err := fmt.Fprintf(w, "%-20s %-13s %-9s %-14s %s\n", e.Name, e.Kind, mode, scope, e.Summary); err != nil {
				return err
			}
		}
		return nil
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(Catalog())
	default:
		return cliutil.UsageError("--output must be text or json, got %q", format)
	}
}
