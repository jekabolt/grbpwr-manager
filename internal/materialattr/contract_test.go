package materialattr

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestValidateRejectsUnknownFabricDirection(t *testing.T) {
	err := Validate(&entity.MaterialInsert{
		MaterialClass: string(entity.MaterialClassFabric),
		FabricAttr:    &entity.MaterialFabricAttr{FabricDirection: sql.NullString{String: "diagonal", Valid: true}},
	})
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a ValidationError, got %v", err)
	}
	if ve.Field != "material.fabric_attrs.fabric_direction" {
		t.Errorf("field = %q, want material.fabric_attrs.fabric_direction", ve.Field)
	}
}

func TestValidateAcceptsKnownFabricDirection(t *testing.T) {
	dirs := AllowedEnumValues("fabric", "fabric_direction")
	if len(dirs) == 0 {
		t.Fatal("fixture has no fabric_direction values")
	}
	for _, dir := range dirs {
		if err := Validate(&entity.MaterialInsert{
			FabricAttr: &entity.MaterialFabricAttr{FabricDirection: sql.NullString{String: dir, Valid: true}},
		}); err != nil {
			t.Errorf("direction %q should be accepted: %v", dir, err)
		}
	}
}

func TestValidateIgnoresUnsetAttrs(t *testing.T) {
	if err := Validate(&entity.MaterialInsert{FabricAttr: &entity.MaterialFabricAttr{}}); err != nil {
		t.Errorf("unset direction should be accepted: %v", err)
	}
	if err := Validate(&entity.MaterialInsert{}); err != nil {
		t.Errorf("a material with no typed attrs should be accepted: %v", err)
	}
}
