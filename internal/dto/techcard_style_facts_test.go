package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestTechCardEmitsStyleFactsNeverReturned locks audit #34: five columns on tech_card that the read
// dropped. model_wears_* is written by UpdateStyle and was visible only on the product/storefront
// messages; the top/sub/type taxonomy path was emitted on TechCardListItem but not on the card
// itself, so opening a card directly lost the category path a list row shows.
func TestTechCardEmitsStyleFactsNeverReturned(t *testing.T) {
	tc := &entity.TechCard{}
	tc.Name = "Coat"
	tc.ModelWearsHeightCm = sql.NullInt32{Int32: 178, Valid: true}
	tc.ModelWearsSizeId = sql.NullInt32{Int32: 4, Valid: true}
	tc.TopCategoryId = sql.NullInt32{Int32: 11, Valid: true}
	tc.SubCategoryId = sql.NullInt32{Int32: 22, Valid: true}
	tc.TypeId = sql.NullInt32{Int32: 33, Valid: true}

	pb := ConvertEntityTechCardToPb(tc, CostingFx{})
	if pb.ModelWearsHeightCm != 178 || pb.ModelWearsSizeId != 4 {
		t.Errorf("model_wears_* not emitted on the card: height=%d size=%d", pb.ModelWearsHeightCm, pb.ModelWearsSizeId)
	}
	if pb.TopCategoryId != 11 || pb.SubCategoryId != 22 || pb.TypeId != 33 {
		t.Errorf("taxonomy path not emitted on the card: %d/%d/%d", pb.TopCategoryId, pb.SubCategoryId, pb.TypeId)
	}

	// The list item has always carried the taxonomy path; the card must now agree with it.
	li := ConvertEntityTechCardToListItemPb(tc)
	if li.TopCategoryId != pb.TopCategoryId || li.SubCategoryId != pb.SubCategoryId || li.TypeId != pb.TypeId {
		t.Errorf("card and list item disagree on the taxonomy path: %+v vs %d/%d/%d",
			li, pb.TopCategoryId, pb.SubCategoryId, pb.TypeId)
	}
}

// TestTechCardStyleFactsUnsetStayZero: NULL columns emit 0, the documented "unset" on the wire.
func TestTechCardStyleFactsUnsetStayZero(t *testing.T) {
	pb := ConvertEntityTechCardToPb(&entity.TechCard{}, CostingFx{})
	if pb.ModelWearsHeightCm != 0 || pb.ModelWearsSizeId != 0 ||
		pb.TopCategoryId != 0 || pb.SubCategoryId != 0 || pb.TypeId != 0 {
		t.Errorf("unset style facts must emit 0: %+v", pb)
	}
}
