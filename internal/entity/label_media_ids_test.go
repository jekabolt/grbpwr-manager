package entity

import (
	"database/sql"
	"reflect"
	"testing"
)

// TestLabelMediaIds pins the id set the read resolves into ResolvedLabelMedia (M-02): the care-label
// logo first, then garment-label mockups, then packaging-item mockups, deduplicated, zero ids dropped.
func TestLabelMediaIds(t *testing.T) {
	tc := &TechCardInsert{
		CareLabel: &TechCardCareLabel{LogoMediaId: sql.NullInt32{Int32: 7, Valid: true}},
		GarmentLabels: []TechCardGarmentLabel{
			{Key: "main", MediaIds: []int{3, 7}},
			{Key: "size", MediaIds: []int{0, 4}},
		},
		PackagingItems: []TechCardPackagingItem{{Key: "bag", MediaIds: []int{4, 9}}},
	}
	if got, want := tc.LabelMediaIds(), []int{7, 3, 4, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelMediaIds = %v, want %v", got, want)
	}

	// No logo override (NULL = brand mark) contributes nothing; an empty card has no ids at all.
	tc.CareLabel.LogoMediaId = sql.NullInt32{}
	if got, want := tc.LabelMediaIds(), []int{3, 7, 4, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelMediaIds without logo = %v, want %v", got, want)
	}
	if got := (&TechCardInsert{}).LabelMediaIds(); len(got) != 0 {
		t.Fatalf("empty card LabelMediaIds = %v, want none", got)
	}
}
