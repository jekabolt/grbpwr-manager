package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// f3 (2026-10-06, live QA of card 38 on beta): the closed vocabulary names EVERY separately cut
// piece (bands, bindings, straps), every inner LAYER of joins.layers, a strap by the neck point it
// starts at, and an opening is never a binding. Goldens on card 38's live join list (beta, saved as
// c38-beta-joins.json in the API's own JSON) and card 49's fixture.

func partsVocabFixture(t *testing.T, name string) entity.DesignJoinsDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "designgen", "testdata", "joins", name))
	require.NoError(t, err)
	if strings.HasSuffix(name, "-beta-joins.json") {
		var pb pb_common.DesignJoins
		require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(raw, &pb))
		return designJoinsDocFromPb(&pb)
	}
	a, ok := entity.ParseDesignJoinsAnswer(string(raw))
	require.True(t, ok)
	return a.Doc()
}

func TestPartsVocabularyGoldenCard38Beta(t *testing.T) {
	got := designPartsVocabulary(partsVocabFixture(t, "c38-beta-joins.json"))
	want := `PART VOCABULARY (closed — use exactly these names; every separately cut piece below is its own part with its own regions, never folded into a body panel; never name a part the construction does not have; a region with no cloth of its own is an opening or the inside of the part seen through it, never a binding or a band):
- front body — the outer front body panel
- back body — the outer back body panel
- front neck binding — a thin binding strip (narrow) cut as its own piece, along NP_R → CFN → NP_L
- left armhole binding — a thin binding strip (narrow) cut as its own piece, along NP_L..SP_L:0.6 → UA_L
- right armhole binding — a thin binding strip (narrow) cut as its own piece, along NP_R..SP_R:0.6 → UA_R
- left strap — a strap cut as its own piece; it STARTS at NP_L..SP_L:0.3 and ends at MB_R; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- right strap — a strap cut as its own piece; it STARTS at NP_R..SP_R:0.3 and ends at MB_L; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- back top binding — a thin binding strip (narrow) cut as its own piece, along MB_L → MB_C → MB_R
- inner front v-panel — layer 1, on the front: a separately cut inner piece; every region where it shows (through the sheer outer layer) is THIS part with this name on every view, never the outer body
- opening: bounded by strap_L, strap_R, back_top_bind — no cloth: label it «opening», or put it in the part whose inside shows through it (seen_through)`
	require.Equal(t, want, got)
	// every separately cut piece and the inner layer are their own names; nothing invented
	for _, n := range []string{"- front neck binding —", "- left armhole binding —", "- right armhole binding —",
		"- back top binding —", "- left strap —", "- right strap —", "- inner front v-panel —"} {
		require.Contains(t, got, n)
	}
	require.NotContains(t, got, "upper back")
	require.NotContains(t, got, "back panel", "layer 0 is the outer shell: the body panels")
}

func TestPartsVocabularyGoldenCard38Layers(t *testing.T) {
	got := designPartsVocabulary(partsVocabFixture(t, "c38-layers-joins.json"))
	want := `PART VOCABULARY (closed — use exactly these names; every separately cut piece below is its own part with its own regions, never folded into a body panel; never name a part the construction does not have; a region with no cloth of its own is an opening or the inside of the part seen through it, never a binding or a band):
- front body — the outer front body panel
- back body — the outer back body panel
- front neckband — a thin band strip (narrow) cut as its own piece, along NP_R → CFN → NP_L
- left strap — a strap cut as its own piece; it STARTS at NP_L and ends at MB_R; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- right strap — a strap cut as its own piece; it STARTS at NP_R and ends at MB_L; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- left front armhole — a thin binding strip (narrow) cut as its own piece, along SP_L → UA_L
- right front armhole — a thin binding strip (narrow) cut as its own piece, along SP_R → UA_R
- back top edge — a thin binding strip (narrow) cut as its own piece, along MB_R → MB_C → MB_L
- inner front v-panel — layer 1, on the front: a separately cut inner piece; every region where it shows (through the sheer outer layer) is THIS part with this name on every view, never the outer body
- opening: bounded by strap_L, strap_R, back_top_edge — no cloth: label it «opening», or put it in the part whose inside shows through it (seen_through)`
	require.Equal(t, want, got)
}

func TestPartsVocabularyGoldenCard49(t *testing.T) {
	got := designPartsVocabulary(partsVocabFixture(t, "c49-joins.json"))
	want := `PART VOCABULARY (closed — use exactly these names; every separately cut piece below is its own part with its own regions, never folded into a body panel; never name a part the construction does not have; a region with no cloth of its own is an opening or the inside of the part seen through it, never a binding or a band):
- left front body — the body panel on the wearer's left of the centre front
- right front body — the body panel on the wearer's right of the centre front
- back body — the outer back body panel
- collar stand — cut as its own piece
- collar — cut as its own piece
- placket — cut as its own piece
- left sleeve — cut as its own piece
- right sleeve — cut as its own piece
- left pocket — cut as its own piece
- back yoke — a separately cut panel above the yoke seam (YOKE_R → CBN_LOW → YOKE_L)
- left cuff — cut as its own piece
- right cuff — cut as its own piece
- left sleeve placket — cut as its own piece
- right sleeve placket — cut as its own piece`
	require.Equal(t, want, got)
	require.NotContains(t, got, "- front body", "a centre-front placket splits the front")
}

func TestPartsStrapNamedByItsNeckEnd(t *testing.T) {
	// a strap drawn from its back end still takes the side of the neck point it starts at
	it := entity.DesignJoinItem{ID: "strap_R", Kind: "strap", From: "MB_R", Via: []string{"UB_C"}, To: "NP_L"}
	require.Equal(t, "left strap", designPartsNameOf(it))
	require.Contains(t, designPartsWhatOf(it), "STARTS at NP_L and ends at MB_R")
	// no neck end: the id's side stands
	require.Equal(t, "right strap", designPartsNameOf(entity.DesignJoinItem{ID: "strap_R", Kind: "strap", From: "MB_R", To: "HEM_R"}))
}

func TestPartsCardPromptF3Rules(t *testing.T) {
	for _, s := range []string{
		"On the FRONT view the wearer's left is picture-RIGHT; on the BACK view the wearer's left is picture-LEFT",
		"the garment's front facing picture-left = the wearer's LEFT flank",
		"A STRAP is named by the shoulder where it STARTS at the neck point",
		"never a binding or a band. Bindings and bands are THIN strips along an edge only.",
		"EVERY SEPARATELY CUT PIECE IS ITS OWN PART",
	} {
		require.Contains(t, designPartsCardSystemPrompt, s)
	}
}

func TestPartsVocabularyBodyPanels(t *testing.T) {
	// a backless top: straps cross UB_C, an opening — no back body is invented
	backless := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "neck", Kind: "binding", From: "NP_R", Via: []string{"CFN"}, To: "NP_L", View: "front"},
		{ID: "strap_L", Kind: "strap", From: "NP_L", Via: []string{"UB_C"}, To: "MB_R", View: "back"},
		{ID: "bare", Kind: "opening", BoundedBy: []string{"strap_L"}},
	}}
	require.NotContains(t, designPartsVocabulary(backless), "back body")
	// a polo placket stops at the chest: one front body
	polo := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "placket", Kind: "placket", From: "CFN", To: "CHEST_C"},
	}}
	require.Contains(t, designPartsVocabulary(polo), "- front body —")
	// a centre-back seam splits the back
	cb := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "cb_seam", Kind: "seam", From: "CBN", To: "HEM_BC"},
	}}
	got := designPartsVocabulary(cb)
	require.Contains(t, got, "- left back body —")
	require.Contains(t, got, "- right back body —")
	require.NotContains(t, got, "- back body —")
	// two layers with one head keep two names
	two := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R"}},
		Layers: []entity.DesignJoinLayer{{Index: 1, Name: "front lining / cotton", Face: "front"}, {Index: 2, Name: "front lining / silk", Face: "front"}}}
	got = designPartsVocabulary(two)
	require.Contains(t, got, "- front lining —")
	require.Contains(t, got, "- front lining 2 —")
}
