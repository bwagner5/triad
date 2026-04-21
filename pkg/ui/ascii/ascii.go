// Package ascii renders a short string as an ASCII-art banner.
//
// It's a thin wrapper around github.com/common-nighthawk/go-figure, fixed to
// a single compact font so the TUI header height is stable. Callers who want
// a different look should pass their own pre-rendered Logo to the TUI
// instead of using this package.
package ascii

import (
	"strings"

	figure "github.com/common-nighthawk/go-figure"
)

// Height is the number of rows produced by Render. It matches the "small"
// figlet font and is used by the TUI to size the header block.
const Height = 5

// Render turns s into a multi-line ASCII-art banner.
func Render(s string) string {
	out := figure.NewFigure(s, "small", true).String()
	// go-figure returns a trailing newline; strip it so callers can compose
	// the banner alongside other blocks without an extra blank row.
	return strings.TrimRight(out, "\n")
}
