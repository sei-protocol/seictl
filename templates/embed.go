// Package templates embeds the autobake-derived YAML/JSON templates
// rendered by `seictl bench up`. Templates are vendored copies of
// sei-protocol/platform autobake/{templates,profiles}/* with engineer-
// bench adaptations (per-engineer namespace, seictl labels). Drift is
// resolved by a deliberate seictl PR per upstream change.
package templates

import "embed"

//go:embed *.yaml *.json
var FS embed.FS
