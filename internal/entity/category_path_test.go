package entity

import "testing"

// Fixture tree for derivation, mirroring the real seed shape (0001_initial_setup.sql):
//
//	10 tops (top)
//	  20 tshirts (sub)
//	    30 crop (type)
//	40 dresses (top)
//	  50 mini (type)   <-- level 3 hanging DIRECTLY off level 1, no sub-category
const (
	derTops     = 10
	derTshirts  = 20
	derCrop     = 30
	derDresses  = 40
	derMiniDres = 50
)

// TestDeriveStyleCategoryPath pins the level-based classification the whole category derivation
// rests on -- above all the `dresses` shape, whose level-3 types hang directly off the level-1 top
// category with no sub-category in between (0001_initial_setup.sql, "Dresses types (no
// sub-category)"). A positional/depth-based walk would pass every other case here and still get
// dresses wrong, so that case is the reason this test exists.
func TestDeriveStyleCategoryPath(t *testing.T) {
	tests := []struct {
		name    string
		chain   []CategoryNode
		wantOK  bool
		wantTop int32
		wantSub int32 // 0 means "expected NULL"
		wantTyp int32 // 0 means "expected NULL"
	}{
		{
			name:    "top only pick leaves sub and type unset",
			chain:   []CategoryNode{{ID: derTops, LevelID: CategoryLevelTop}},
			wantOK:  true,
			wantTop: derTops,
		},
		{
			name: "sub category pick derives its top",
			chain: []CategoryNode{
				{ID: derTshirts, LevelID: CategoryLevelSub},
				{ID: derTops, LevelID: CategoryLevelTop},
			},
			wantOK:  true,
			wantTop: derTops,
			wantSub: derTshirts,
		},
		{
			name: "full three level chain",
			chain: []CategoryNode{
				{ID: derCrop, LevelID: CategoryLevelType},
				{ID: derTshirts, LevelID: CategoryLevelSub},
				{ID: derTops, LevelID: CategoryLevelTop},
			},
			wantOK:  true,
			wantTop: derTops,
			wantSub: derTshirts,
			wantTyp: derCrop,
		},
		{
			// The exception that forces level-based classification: sub MUST stay NULL.
			name: "dresses type hangs off the top category with no sub",
			chain: []CategoryNode{
				{ID: derMiniDres, LevelID: CategoryLevelType},
				{ID: derDresses, LevelID: CategoryLevelTop},
			},
			wantOK:  true,
			wantTop: derDresses,
			wantTyp: derMiniDres,
		},
		{
			name: "chain with no top level node is rejected",
			chain: []CategoryNode{
				{ID: derCrop, LevelID: CategoryLevelType},
				{ID: derTshirts, LevelID: CategoryLevelSub},
			},
			wantOK: false,
		},
		{
			name:   "empty chain is rejected",
			chain:  nil,
			wantOK: false,
		},
		{
			name: "duplicate level resolves to the node nearest the pick",
			chain: []CategoryNode{
				{ID: derTshirts, LevelID: CategoryLevelSub},
				{ID: 99, LevelID: CategoryLevelSub},
				{ID: derTops, LevelID: CategoryLevelTop},
			},
			wantOK:  true,
			wantTop: derTops,
			wantSub: derTshirts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeriveStyleCategoryPath(tt.chain)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				if got != (StyleCategoryPath{}) {
					t.Errorf("a rejected chain must yield no partial path, got %+v", got)
				}
				return
			}
			if !got.TopCategoryID.Valid || got.TopCategoryID.Int32 != tt.wantTop {
				t.Errorf("top = %+v, want %d", got.TopCategoryID, tt.wantTop)
			}
			if got.SubCategoryID.Valid != (tt.wantSub != 0) || (tt.wantSub != 0 && got.SubCategoryID.Int32 != tt.wantSub) {
				t.Errorf("sub = %+v, want %d (0 means NULL)", got.SubCategoryID, tt.wantSub)
			}
			if got.TypeID.Valid != (tt.wantTyp != 0) || (tt.wantTyp != 0 && got.TypeID.Int32 != tt.wantTyp) {
				t.Errorf("type = %+v, want %d (0 means NULL)", got.TypeID, tt.wantTyp)
			}
		})
	}
}
