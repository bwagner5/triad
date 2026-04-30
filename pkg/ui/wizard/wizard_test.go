package wizard

import (
	"context"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestTypedFieldWithSuggestStillUsesSelectMode(t *testing.T) {
	m := newModel(context.Background(), []registry.Field{{
		Flag: "enabled",
		Kind: registry.KindBool,
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "true"}, {Value: "false"}}, nil
		},
	}}, registry.Input{})

	if !m.isSelect() {
		t.Fatal("typed field with Suggest should render as a selection list")
	}
}

func TestKindFileUsesFileMode(t *testing.T) {
	m := newModel(context.Background(), []registry.Field{{
		Flag: "path",
		Kind: registry.KindFile,
	}}, registry.Input{})

	if !m.isFile() {
		t.Fatal("KindFile should render as a file picker")
	}
}
