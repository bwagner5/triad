package cli

import (
	"fmt"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/spf13/cobra"
)

// AliasOp returns a cobra command that re-exposes an existing (resource, op)
// at a different command path. Useful for promoting a hot-path operation
// (e.g. "app deploy" -> top-level "deploy") without duplicating logic.
//
// Usage:
//
//	root.AddCommand(cli.AliasOp(reg, g, "app", "deploy", "deploy", "deploy an app"))
//
// The returned command inherits the operation's Fields, Confirm, Steps/Run,
// and wizard behavior. Panics if resource or op is not found so wiring
// mistakes fail loudly at startup.
func AliasOp(reg *registry.Registry, g *Globals, resource, op, use, short string) *cobra.Command {
	res := reg.Lookup(resource)
	if res == nil {
		panic(fmt.Sprintf("AliasOp: resource %q not registered", resource))
	}
	o, ok := res.Operations[op]
	if !ok {
		panic(fmt.Sprintf("AliasOp: %q has no operation %q", resource, op))
	}
	// Override Use/Short so the alias appears under its own name in --help.
	alias := o
	alias.Name = use
	alias.Aliases = nil
	if short != "" {
		alias.Short = short
	}
	if len(alias.Steps) > 0 {
		return sagaCmd(*res, alias, g)
	}
	if alias.Run != nil {
		return actionCmd(*res, alias, g)
	}
	panic(fmt.Sprintf("AliasOp: %q.%q has neither Steps nor Run", resource, op))
}
