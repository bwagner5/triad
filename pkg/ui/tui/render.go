package tui

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// detailID returns a short identifier for an item — the first table-visible
// field. Falls back to empty string if none is available.
func detailID(res *registry.Resource, item any) string {
	rv := reflect.Indirect(reflect.ValueOf(item))
	for _, f := range res.Fields {
		if f.Table.Header == "" {
			continue
		}
		return read(rv, f)
	}
	return ""
}

// detailFor renders a full field-by-field view of an item.
func detailFor(res *registry.Resource, item any) string {
	rv := reflect.Indirect(reflect.ValueOf(item))
	var s strings.Builder
	for _, f := range res.Fields {
		name := f.Flag
		if f.Table.Header != "" {
			name = f.Table.Header
		}
		val := read(rv, f)
		if f.Sensitive {
			val = strings.Repeat("*", len(val))
		}
		fmt.Fprintf(&s, "%s %s\n", theme.Label.Render(name+":"), val)
	}
	return s.String()
}

func read(rv reflect.Value, f registry.Field) string {
	if !rv.IsValid() {
		return ""
	}
	for _, k := range []string{f.Name, f.Flag} {
		if k == "" {
			continue
		}
		fv := rv.FieldByNameFunc(func(s string) bool { return strings.EqualFold(s, k) })
		if fv.IsValid() {
			return fmt.Sprintf("%v", fv.Interface())
		}
	}
	return ""
}
