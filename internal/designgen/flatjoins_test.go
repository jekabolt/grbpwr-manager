package designgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func loadJoinsCase(t *testing.T, name string) entity.DesignJoinsDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "joins", name+"-joins.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := entity.ParseDesignJoinsAnswer(string(raw))
	if !ok {
		t.Fatalf("%s: the example list does not parse", name)
	}
	return a.Doc()
}

// TestJoinsListTextGolden — the join list in words, as the PARTS labeller reads it (admin
// design_parts_construction.go), BYTE FOR BYTE the list paragraph the flat prompt carried in rounds
// 5–7 (the goldens are that paragraph, cut from the old full-block goldens at M7b, when the CHECK
// sentences left with the construction): the ruler, the layers of a multi-layer garment, every item
// on one line, the absences — the cleaner already dropped card 49's positive «absence».
//
// MUTATIONS IT CATCHES: any wording drift of a list line; the LAYERS block missing or said for one
// layer; a pocket / opening / closure losing its own line shape.
func TestJoinsListTextGolden(t *testing.T) {
	for _, c := range []string{"c38", "c49", "c2", "c2out", "c49seam", "c38-layers"} {
		t.Run(c, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "joins", c+"-list-golden.txt"))
			require.NoError(t, err)
			d := loadJoinsCase(t, c)
			require.True(t, JoinsUsable(&d))
			require.Equal(t, string(want), JoinsListText(d))
		})
	}
	require.False(t, JoinsUsable(nil))
	require.False(t, JoinsUsable(&entity.DesignJoinsDoc{}))
	require.True(t, JoinsUsable(&entity.DesignJoinsDoc{Absences: []string{"no sleeves"}}))
}

// TestJoinsLayersSanitize — Fable's confirmed multi-layer list for card 38 (out/layers/joins-A.json)
// keeps its uncertain notes, and «through» needs a sheer layer above it.
func TestJoinsLayersSanitize(t *testing.T) {
	d := loadJoinsCase(t, "c38-layers")
	require.Len(t, d.Uncertain, 6)
	require.Contains(t, JoinsListText(d), "LAYERS (depth levels; 0 = outermost):")
	require.NotContains(t, JoinsListText(loadJoinsCase(t, "c38")), "LAYERS")
	n := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{
		Layers: []entity.DesignJoinLayer{{Index: 0, Name: "outer"}, {Index: 1, Name: "lining"}},
		Items:  []entity.DesignJoinItem{{ID: "e", Kind: "edge", From: "HEM_L", To: "HEM_R", Layer: 1, Visibility: "through"}},
	})
	require.Equal(t, entity.DesignJoinVisible, n.Items[0].Visibility, "through needs a sheer layer above")
}

// TestFlatCraftSideFacingAndNoGrey — the side convention only with a side view, never on a detail
// callout; the no-grey sentence on every flat run, before the owner's verbatim style, which closes the
// prompt with the exclusions and the output.
func TestFlatCraftSideFacingAndNoGrey(t *testing.T) {
	p := runParams{Views: []string{"front", "back", "side_l", "side_r"}, Layout: layoutOne}
	got := flatCraft(p, nil, 3)
	require.Contains(t, got, flatSideFacing)
	require.Contains(t, got, flatNoTextNoGrey)
	require.Less(t, strings.Index(got, flatNoTextNoGrey), strings.Index(got, flatStyleGarment))
	require.True(t, strings.HasSuffix(got, flatStyleGarment+"\n\n"+flatExcludedGarment+"\n\n"+flatOutput))
	require.NotContains(t, flatCraft(runParams{Views: []string{"front", "back"}, Layout: layoutOne}, nil, 3), flatSideFacing)
	detail := flatCraft(runParams{Views: []string{"detail"}}, []string{"collar"}, 1)
	require.NotContains(t, detail, flatSideFacing)
	require.Contains(t, detail, flatNoTextNoGrey)
}
