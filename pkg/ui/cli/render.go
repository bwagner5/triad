package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/theme"
	"gopkg.in/yaml.v3"
)

// Render prints a list of items according to the output mode.
func Render(w io.Writer, mode string, res registry.Resource, items []any) error {
	switch mode {
	case "yaml":
		return yaml.NewEncoder(w).Encode(items)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	case "wide", "short", "":
		return renderTable(w, res, items, mode == "wide")
	default:
		return fmt.Errorf("unknown output mode %q", mode)
	}
}

func renderTable(w io.Writer, res registry.Resource, items []any, wide bool) error {
	var headers []string
	var fields []registry.Field
	for _, f := range res.Fields {
		if f.Table.Header == "" {
			continue
		}
		if f.Table.Wide && !wide {
			continue
		}
		headers = append(headers, f.Table.Header)
		fields = append(fields, f)
	}
	var rows [][]string
	for _, it := range items {
		row := make([]string, len(fields))
		rv := reflect.Indirect(reflect.ValueOf(it))
		for i, f := range fields {
			row[i] = fieldString(rv, f)
		}
		rows = append(rows, row)
	}
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.HiddenBorder()).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return theme.Label.PaddingRight(2)
			}
			return lipgloss.NewStyle().PaddingRight(2)
		})
	_, _ = fmt.Fprintln(w, t.Render())
	return nil
}

// fieldString reads a struct field by Name (case-insensitive). Falls back to Flag.
func fieldString(rv reflect.Value, f registry.Field) string {
	if !rv.IsValid() {
		return ""
	}
	for _, key := range []string{f.Name, f.Flag} {
		if key == "" {
			continue
		}
		fv := rv.FieldByNameFunc(func(s string) bool { return equalFold(s, key) })
		if fv.IsValid() {
			return fmt.Sprintf("%v", fv.Interface())
		}
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// RenderEvents prints saga events to w with spinners/checkmarks.
// It reads until the channel is closed.
func RenderEvents(w io.Writer, ch <-chan runtime.Event) error {
	var finalErr error
	for e := range ch {
		if e.Done {
			if e.Status == runtime.Failed {
				fmt.Fprintln(w, theme.Err.Render("✗ ")+e.Saga+" failed: "+errStr(e.Err)) //nolint:errcheck
				finalErr = e.Err
			} else {
				fmt.Fprintln(w, theme.OK.Render("✓ ")+e.Saga+" complete") //nolint:errcheck
			}
			continue
		}
		switch e.Status {
		case runtime.Running:
			_, _ = fmt.Fprintf(w, "%s %s\n", theme.RunMark, e.Step)
		case runtime.OK:
			_, _ = fmt.Fprintf(w, "%s %s\n", theme.OKMark, e.Step)
		case runtime.Failed:
			_, _ = fmt.Fprintf(w, "%s %s — %s\n", theme.ErrMark, e.Step, errStr(e.Err))
		case runtime.Skipped:
			_, _ = fmt.Fprintf(w, "%s %s (skipped)\n", theme.SkipMark, e.Step)
		}
	}
	return finalErr
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
