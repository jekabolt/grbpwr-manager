package admin

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// T45 (27.09) — the proposed colourway carries a PALETTE: the model names 1…4 colours, the parse
// holds their shape (both on the live answer and on the canonical replay), and the live-only settle
// step mirrors the main colour into the pre-T45 pantone / hex and proposes the family by the same
// measure CreateColorway uses.

func TestParseConstructionDraftReadsTheProposedPalette(t *testing.T) {
	draft, stats, err := parseConstructionDraft(`{
	  "bom": [{"name":"main fabric"}],
	  "colourways": [
	    {"name":"Black / Bone","color_code":"BLK",
	     "colours":[{"label":"black","pantone":"19-4005 TCX","hex":"#2B2C30"},
	                {"colour":"bone","pantone":"11-0602 TCX","hex":"#EFE9DC"},
	                {"label":"BLACK","pantone":"19-4005 tcx"},
	                {"hex":"#123456"},
	                {"label":"a"},{"label":"b"},{"label":"c"}]},
	    {"name":"US spelling","colors":[{"color":"olive","hex":"#5B6236"}]}
	  ]}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetColourways(), 2)

	first := draft.GetColourways()[0].GetColours()
	require.Len(t, first, designConstructionMaxColourwayColours, "the fifth colour does not fit")
	require.Equal(t, "black", first[0].GetLabel())
	require.Equal(t, "19-4005 TCX", first[0].GetPantone())
	require.Equal(t, "#2B2C30", first[0].GetHex())
	require.Equal(t, "bone", first[1].GetLabel(), "`colour` is read as the label, like a slot's words")
	require.Equal(t, 1, stats.Deduped, "«BLACK / 19-4005 tcx» is the first colour again")
	require.Equal(t, 1, stats.ColoursDropped, "a bare hex is neither a Pantone nor a label")
	require.Positive(t, stats.OverLimit)

	us := draft.GetColourways()[1].GetColours()
	require.Len(t, us, 1, "`colors` / `color` are the same keys spelled American")
	require.Equal(t, "olive", us[0].GetLabel())
}

// A palette is content: a proposal that names only colours survives the emptiness rule; one that
// names only a top-level pantone (the pre-T45 form) still does not.
func TestProposalEmptinessCountsOnlyANamedPalette(t *testing.T) {
	draft, stats, err := parseConstructionDraft(`{"colourways":[
	    {"colours":[{"label":"black"}]},
	    {"pantone":"19-4005 TCX","hex":"#2B2C30"}
	  ]}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetColourways(), 1)
	require.Equal(t, 1, stats.ColourwaysDropped)
}

func TestSettleColourwayPalettesMirrorsAndProposes(t *testing.T) {
	hex := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	dict := []entity.Color{
		{Code: "BLK", Name: "black", Hex: hex("#000000")},
		{Code: "NAV", Name: "navy", Hex: hex("#1A2238")},
		{Code: "OFW", Name: "off-white", Hex: hex("#F5F5F0")},
	}
	draft := &pb_common.DesignConstructionDraft{Colourways: []*pb_common.DesignColourwayProposal{
		// 1. palette named, no code: mirror + family from colours[0].hex
		{Name: "Stretch Limo", Pantone: "stale", Hex: "#FFFFFF",
			Colours: []*pb_common.ColorwayColour{{Pantone: "19-4005 TCX", Hex: "#2B2C30"}, {Label: "bone", Hex: "#EFE9DC"}}},
		// 2. pre-T45 form: the main colour is derived from the top-level pantone / hex
		{Name: "Peacoat", Pantone: "19-3920 TCX", Hex: "#2B2E43"},
		// 3. a verified code is kept even when the hex says otherwise
		{Name: "Ink", ColorCode: "BLK", Colours: []*pb_common.ColorwayColour{{Label: "ink", Hex: "#1F2A44"}}},
		// 4. nothing to go by: the code stays empty for the client to ask
		{Name: "Undyed", Colours: []*pb_common.ColorwayColour{{Label: "undyed"}}},
	}}
	var stats designConstructionStats
	designSettleColourwayPalettes(draft, dict, &stats)

	cw := draft.GetColourways()
	require.Equal(t, "19-4005 TCX", cw[0].GetPantone(), "the top-level pantone mirrors colours[0]")
	require.Equal(t, "#2B2C30", cw[0].GetHex())
	require.Equal(t, "BLK", cw[0].GetColorCode(), "a fabric black files under BLK")

	require.Len(t, cw[1].GetColours(), 1, "a pre-T45 answer gets its main colour as a palette")
	require.Equal(t, "19-3920 TCX", cw[1].GetColours()[0].GetPantone())
	require.Equal(t, "NAV", cw[1].GetColorCode())

	require.Equal(t, "BLK", cw[2].GetColorCode(), "a code the verifier kept is the model's answer")
	require.Equal(t, "", cw[3].GetColorCode())
	require.Equal(t, 2, stats.ColourFamiliesProposed)
	require.False(t, stats.Coerced(), "a proposed family is an addition, not a coercion")
}

// The replay reads the canonical JSON as the client got it: the palette round-trips verbatim, the
// Pantone book included, and a pre-T45 canonical (no `colours`) gets NO palette invented for it —
// the settle step is live-only. MUTATION: synthesise the palette in the parse — the old run's
// replay grows a colour nobody saw.
func TestCanonicalReplayKeepsThePaletteAndInventsNone(t *testing.T) {
	in := &pb_common.DesignConstructionDraft{Colourways: []*pb_common.DesignColourwayProposal{{
		Name: "Black / Bone", ColorCode: "BLK", Pantone: "19-4005", Hex: "#2B2C30",
		Colours: []*pb_common.ColorwayColour{{Label: "black", Pantone: "19-4005", PantoneSystem: "TCX", Hex: "#2B2C30"}},
	}}}
	stored, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back)
	require.Len(t, back.GetColourways()[0].GetColours(), 1)
	require.Equal(t, "TCX", back.GetColourways()[0].GetColours()[0].GetPantoneSystem())
	require.Equal(t, "black", back.GetColourways()[0].GetColours()[0].GetLabel())

	pre := `{"silhouette":"","fabric":"","fit":"","concept":"","aspects":[],"bom":[],"missing":[],` +
		`"flat_details":[],"colourways":[{"name":"Olive","color_code":"","pantone":"18-0625 TCX","hex":"#5B6236","slots":[]}]}`
	old := designConstructionDraftFromRun(pre)
	require.NotNil(t, old)
	require.Empty(t, old.GetColourways()[0].GetColours(), "a run answered before T45 replays without a palette")
	require.Equal(t, "", old.GetColourways()[0].GetColorCode(), "and without a family proposed after the fact")
}

// The schema names the palette and rule 9 says what it is; the top-level pantone / hex are no
// longer asked for (they are the mirror).
func TestConstructionPromptAsksForThePalette(t *testing.T) {
	p := designConstructionSystemPrompt
	require.Contains(t, p, `"colours": [{"label": string, "pantone": string, "hex": string}]`)
	require.Contains(t, p, "several colourways may share a code")
	require.Contains(t, p, "closest to the MAIN colour")
	require.False(t, strings.Contains(p, `"color_code": string, "pantone": string`),
		"the top-level pantone is the mirror of colours[0], not a key to fill")
}

// The settle step runs on the LIVE answer inside DraftDesignIdea, after the verifier and before the
// canonical row is written: the client receives the mirrored main colour and the proposed family,
// and the stored canonical carries them, so a replay (which never settles) returns the same draft.
// MUTATION: drop the designSettleColourwayPalettes call in design_run.go — the draft comes back
// with no pantone, no hex and no family.
func TestDraftDesignIdeaSettlesTheProposedPaletteOnTheLiveAnswer(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, `{"silhouette":"Sleeveless V-neck tank top",
	  "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}],
	  "colourways":[{"name":"Ink","colours":[{"label":"ink","pantone":"19-4005 TCX","hex":"#2B2C30"}]}]}`)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	cws := resp.GetConstruction().GetColourways()
	require.Len(t, cws, 1)
	require.Equal(t, "19-4005 TCX", cws[0].GetPantone(), "the main colour is mirrored into the pre-T45 pantone")
	require.Equal(t, "#2B2C30", cws[0].GetHex())
	require.Equal(t, "BLK", cws[0].GetColorCode(),
		"a fabric black is filed under BLK from the rig's dictionary (draftProbeColours)")

	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetColourways(), 1)
	require.Equal(t, "BLK", stored.GetColourways()[0].GetColorCode(), "the canonical row carries the settled proposal")
	require.Equal(t, "19-4005 TCX", stored.GetColourways()[0].GetPantone())
	require.Empty(t, rig.failed)
}
