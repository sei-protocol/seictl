// Package nodedeployment registers the `seictl nodedeployment` (alias
// `nd`) verb tree. v1 ships `apply`; subsequent PRs add get/list/delete
// and watch.
//
// See docs/design/nodedeployment-cli.md for output shape and exit-code
// conventions.
package nodedeployment

import (
	"github.com/urfave/cli/v3"
)

var Cmd = cli.Command{
	Name:    "nodedeployment",
	Aliases: []string{"nd"},
	Usage:   "Manage SeiNodeDeployment custom resources via embedded presets",
	Commands: []*cli.Command{
		&applyCmd,
	},
}
