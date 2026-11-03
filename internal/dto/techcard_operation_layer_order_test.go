package dto

import (
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestOperationLayerOrderFromPb(t *testing.T) {
	got, err := operationLayerOrderFromPb(0, []string{" BACK ", "", "POCKET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "BACK" || got[1] != "POCKET" {
		t.Fatalf("trimmed keys keep their order, got %v", got)
	}
	if got, err := operationLayerOrderFromPb(0, nil); err != nil || got != nil {
		t.Fatalf("nothing stays nothing, got %v, %v", got, err)
	}

	refused := func(raw []string, reason string) {
		t.Helper()
		_, err := operationLayerOrderFromPb(2, raw)
		var ve *entity.ValidationError
		if !errors.As(err, &ve) || ve.Reason != reason || !strings.HasPrefix(ve.Field, "operations[2].layer_order") {
			t.Fatalf("want %s on layer_order, got %v", reason, err)
		}
	}
	refused([]string{"BACK", "BACK"}, "duplicate")
	refused([]string{strings.Repeat("K", entity.TechCardOperationLayerOrderKeyMaxLen+1)}, "too_long")
	many := make([]string, entity.TechCardOperationMaxLayerOrder+1)
	refused(many, "too_many")
}
