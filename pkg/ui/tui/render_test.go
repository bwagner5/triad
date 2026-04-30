package tui

import (
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

type detailWidget struct {
	Name  string
	Token string
}

func TestDetailForUsesCustomDetailView(t *testing.T) {
	res := &registry.Resource{
		Name: "widget",
		Detail: func(item any) registry.DetailView {
			w := item.(detailWidget)
			return registry.DetailView{Sections: []registry.DetailSection{
				{
					Title: "Overview",
					Body:  "Ready for launch.",
					Rows: []registry.DetailRow{
						{Label: "Name", Value: w.Name},
						{Label: "Token", Value: w.Token, Sensitive: true},
					},
				},
				{
					Title: "Links",
					Rows:  []registry.DetailRow{{Label: "URL", Value: "https://example.com"}},
				},
			}}
		},
	}

	got := stripANSI(detailFor(res, detailWidget{Name: "alpha", Token: "secret"}))
	for _, want := range []string{
		"Overview",
		"Ready for launch.",
		"Name: alpha",
		"Token: ******",
		"Links",
		"URL: https://example.com",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("sensitive value leaked:\n%s", got)
	}
}

func TestDetailForFallsBackWhenCustomDetailEmpty(t *testing.T) {
	res := &registry.Resource{
		Name: "widget",
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
			{Name: "Token", Flag: "token", Sensitive: true},
		},
		Detail: func(any) registry.DetailView { return registry.DetailView{} },
	}

	got := stripANSI(detailFor(res, detailWidget{Name: "alpha", Token: "secret"}))
	if !strings.Contains(got, "NAME: alpha") {
		t.Fatalf("fallback detail missing name:\n%s", got)
	}
	if !strings.Contains(got, "token: ******") {
		t.Fatalf("fallback detail missing masked token:\n%s", got)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("sensitive fallback value leaked:\n%s", got)
	}
}
