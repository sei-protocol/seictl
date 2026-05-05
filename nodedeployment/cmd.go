// Package nodedeployment registers the `seictl nodedeployment` (alias
// `nd`) verb tree. v1 ships `apply`; subsequent PRs add get/list/delete
// and watch.
//
// The CLI is a thin glue over the SeiNodeDeployment CRD. Verb output
// matches `kubectl get snd -o json` shape; errors emit as
// metav1.Status on stderr; exit codes follow kubectl convention (0 on
// success, 1 on anything else, .reason on stderr discriminates).
//
// See docs/design/nodedeployment-cli.md for the design.
package nodedeployment

import (
	"github.com/urfave/cli/v3"
)

// Cmd is the top-level urfave Command for `seictl nodedeployment`.
var Cmd = cli.Command{
	Name:    "nodedeployment",
	Aliases: []string{"nd"},
	Usage:   "Manage SeiNodeDeployment custom resources via embedded presets",
	Commands: []*cli.Command{
		&applyCmd,
	},
}
