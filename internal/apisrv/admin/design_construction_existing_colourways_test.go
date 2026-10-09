package admin

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// T06 (owner item 6): a re-run of CONSTRUCTION DRAFT must not propose colourways the card already has.

func existingColourwaysProbe() []entity.TechCardColorway {
	return []entity.TechCardColorway{
		{Name: "Black / Bone", Pantone: sql.NullString{String: "19-4005", Valid: true}},
		{Name: "Field Olive", Colours: []entity.ColorwayColour{{Pantone: "18-0625 TCX", Hex: "#5B6236"}}},
		{Name: "Navy", Hex: sql.NullString{String: "#1F2A44", Valid: true}},
		// Archived: no longer on the card, may be proposed again.
		{Name: "Rust", Pantone: sql.NullString{String: "18-1248 TCX", Valid: true}, Status: entity.ColorwayStatusArchived},
	}
}

func TestDropExistingColourwaysMatchesByNameOrPantoneOrHex(t *testing.T) {
	draft := &pb_common.DesignConstructionDraft{Colourways: []*pb_common.DesignColourwayProposal{
		{Name: "black/bone"},                  // name, folded
		{Name: "Moss", Pantone: "18-0625TCX"}, // main pantone of Field Olive (palette), book glued on
		{Name: "Deep sea", Colours: []*pb_common.ColorwayColour{{Hex: "#1f2a44"}}}, // hex of Navy, case
		{Name: "Ink", Pantone: "PANTONE 19-4005 TPX"},                              // pantone of Black / Bone, other book
		{Name: "Sand", Pantone: "13-1008 TCX", Hex: "#D8CBB0"},                     // new
		{Name: "Rust", Pantone: "18-1248 TCX"},                                     // only archived on the card
	}}
	var stats designConstructionStats
	designDropExistingColourways(draft, existingColourwaysProbe(), nil, &stats)

	var names []string
	for _, cw := range draft.GetColourways() {
		names = append(names, cw.GetName())
	}
	require.Equal(t, []string{"Sand", "Rust"}, names)
	require.Equal(t, 4, stats.ColourwaysExisting)
	require.False(t, stats.Coerced(), "a deliberate filter, not a repair")
}

// BX2: the server's own label «colourway N» for an unnamed proposal names a position, not a colour,
// and never name-matches — but a card colourway REALLY named «colourway 2» still matches a proposal
// the model itself named so. Runs the live order: designVerifyColourways labels, then the drop.
// MUTATION: make designDropExistingColourways ignore autoNamed (or skip every «colourway N» by its
// text, as before) — one of the two assertions goes red.
func TestDropExistingColourwaysTellsTheServerLabelFromARealName(t *testing.T) {
	existing := []entity.TechCardColorway{{Name: "colourway 2", Hex: sql.NullString{String: "#101010", Valid: true}}}

	// The model left the second proposal unnamed: the server labels it «colourway 2», same spelling.
	labelled := &pb_common.DesignConstructionDraft{Colourways: []*pb_common.DesignColourwayProposal{
		{Name: "Sand", Colours: []*pb_common.ColorwayColour{{Hex: "#D8CBB0"}}},
		{Colours: []*pb_common.ColorwayColour{{Hex: "#5B2333"}}},
	}}
	var stats designConstructionStats
	auto := designVerifyColourways(labelled, designBuildColourDictionary(draftProbeColours()), nil, &stats)
	require.Equal(t, "colourway 2", labelled.GetColourways()[1].GetName())
	designDropExistingColourways(labelled, existing, auto, &stats)
	require.Len(t, labelled.GetColourways(), 2, "a server label is not a name")

	// The model NAMED its proposal «Colourway 2»: that is the card's colourway, proposed again.
	named := &pb_common.DesignConstructionDraft{Colourways: []*pb_common.DesignColourwayProposal{
		{Name: "Sand", Colours: []*pb_common.ColorwayColour{{Hex: "#D8CBB0"}}},
		{Name: "Colourway 2", Colours: []*pb_common.ColorwayColour{{Hex: "#5B2333"}}},
	}}
	stats = designConstructionStats{}
	auto = designVerifyColourways(named, designBuildColourDictionary(draftProbeColours()), nil, &stats)
	designDropExistingColourways(named, existing, auto, &stats)
	require.Len(t, named.GetColourways(), 1)
	require.Equal(t, "Sand", named.GetColourways()[0].GetName())
	require.Equal(t, 1, stats.ColourwaysExisting)
}

func TestDesignPantoneKey(t *testing.T) {
	require.Equal(t, "194005", designPantoneKey("19-4005 TCX"))
	require.Equal(t, "194005", designPantoneKey("pantone 19-4005tpx"))
	require.Equal(t, "186", designPantoneKey("PANTONE 186 C"))
	require.Equal(t, "black6", designPantoneKey("Black 6 C"))
	require.Equal(t, "", designPantoneKey(""))
}

// Handler: the prompt names the card's colourways under «do not propose these again», and the
// filtered answer is what is filed in output_text and returned.
// MUTATION: drop the designDropExistingColourways call in DraftDesignIdea — red.
func TestDraftDesignIdeaDoesNotReproposeTheCardsColourways(t *testing.T) {
	card := designMoodCard()
	card.Colorways = existingColourwaysProbe()
	answer := `{"silhouette":"Field jacket","fabric":"Cotton canvas",
	  "aspects":[{"key":"pockets","text":"Four bellows pockets with flaps"}],
	  "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}],
	  "colourways":[
	    {"name":"Black / Bone","pantone":"19-4005 TCX","hex":"#2B2C30"},
	    {"name":"Sand","pantone":"13-1008 TCX","hex":"#D8CBB0"}]}`
	rig := newDraftRigWithCard(t, http.StatusOK, answer, card,
		[]int{designBoardMediaID},
		map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}})
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	require.Contains(t, rig.stub.body, "colourways that already exist — do not propose these again")
	require.Contains(t, rig.stub.body, "Black / Bone · 19-4005")
	require.Contains(t, rig.stub.body, "Field Olive · 18-0625 TCX · #5B6236")
	require.NotContains(t, rig.stub.body, "Rust", "archived colourways are not on the card")

	got := resp.GetConstruction().GetColourways()
	require.Len(t, got, 1)
	require.Equal(t, "Sand", got[0].GetName())
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetColourways(), 1)
	require.NotContains(t, rig.completedText, "Black / Bone")
}
