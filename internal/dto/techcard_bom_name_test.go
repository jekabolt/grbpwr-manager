package dto

import (
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestParseTechCardBomItemsNameRequiredOnlyWhenUnlinked pins the relaxed input rule. A LINKED line
// takes its name from the material it links (resolved on read in enrichMaterials), so the client is
// free to send an empty one -- and the admin client now does exactly that for a linked line that
// never had its own name. Only a FREE-TEXT line must name itself, because nothing else can.
func TestParseTechCardBomItemsNameRequiredOnlyWhenUnlinked(t *testing.T) {
	tests := []struct {
		name       string
		item       *pb_common.TechCardBomItem
		wantReject bool
	}{
		{
			name: "linked line may omit its name",
			item: &pb_common.TechCardBomItem{
				Section:    pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				MaterialId: 42,
				Name:       "",
			},
		},
		{
			name: "linked line may still carry a name",
			item: &pb_common.TechCardBomItem{
				Section:    pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				MaterialId: 42,
				Name:       "an override the read path will ignore",
			},
		},
		{
			name: "free-text line with a name is fine",
			item: &pb_common.TechCardBomItem{
				Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:    "hand-typed twill",
			},
		},
		{
			name: "free-text line without a name is rejected",
			item: &pb_common.TechCardBomItem{
				Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:    "",
			},
			wantReject: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{tt.item})
			if !tt.wantReject {
				if err != nil {
					t.Fatalf("expected the line to be accepted, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an unlinked, unnamed line to be rejected")
			}
			assertBomFieldViolation(t, err, "bom_items[0].name")
		})
	}
}

// TestParseTechCardBomItemsEmitsFieldViolations checks BOM rejections are field-tagged and carry the
// row index, so the admin client's applyServerFieldErrors can pin the message to the exact input
// instead of showing a form-level banner the user has to hunt through.
func TestParseTechCardBomItemsEmitsFieldViolations(t *testing.T) {
	fabric := pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC

	t.Run("index identifies the offending row", func(t *testing.T) {
		_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{
			{Section: fabric, Name: "fine"},
			{Section: fabric, Name: "also fine"},
			{Section: fabric, Name: ""}, // the bad one
		})
		if err == nil {
			t.Fatal("expected a rejection")
		}
		assertBomFieldViolation(t, err, "bom_items[2].name")
	})

	t.Run("over-long value is tagged to its own column", func(t *testing.T) {
		_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{
			{Section: fabric, Name: "ok", Supplier: strings.Repeat("x", maxVarchar255+1)},
		})
		if err == nil {
			t.Fatal("expected a rejection")
		}
		assertBomFieldViolation(t, err, "bom_items[0].supplier")
	})

	t.Run("invalid section is tagged", func(t *testing.T) {
		_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN, Name: "ok"},
		})
		if err == nil {
			t.Fatal("expected a rejection")
		}
		assertBomFieldViolation(t, err, "bom_items[0].section")
	})
}

func assertBomFieldViolation(t *testing.T, err error, wantField string) {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a field-tagged *entity.ValidationError so the RPC layer can deep-link it, got %T: %v", err, err)
	}
	if ve.Field != wantField {
		t.Errorf("violation field = %q, want %q", ve.Field, wantField)
	}
}
