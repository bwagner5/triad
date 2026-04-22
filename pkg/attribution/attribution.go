// Package attribution embeds ATTRIBUTION.md.
// Run `make attribution` to generate the file before building.
package attribution

import _ "embed"

//go:embed ATTRIBUTION.md
var Text string
