package entity

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const seamTestKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func seamTestAnchor(piece, hint string) TechCardSeamAnchor {
	return TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples:      []TechCardSeamSample{{0, 0}, {0.25, 0.1}, {0.5, 0.2}, {0.75, 0.3}, {1, 0.4}},
		PerimShare:   0.12,
		LenMm:        142.5,
		Notches:      1,
		TurnDeg:      -3,
		EdgeHint:     hint,
		ContourSig:   "abcdef0123456789",
	}
}

func seamTestWrite() TechCardSeamsWrite {
	return TechCardSeamsWrite{TechCardId: 7, By: "ann", Seams: []TechCardSeamInput{{
		SeamKey:   strings.ToLower(seamTestKey),
		Status:    TechCardSeamConfirmed,
		Kind:      TechCardSeamKindEdge,
		Direction: TechCardSeamReversed,
		Source:    TechCardSeamSourceGraph,
		SideA:     []TechCardSeamAnchor{seamTestAnchor("PIECE-FRONT", "FP_L#2")},
		SideB:     []TechCardSeamAnchor{seamTestAnchor("PIECE-BACK", "BP#8")},
	}}}
}

func TestValidateTechCardSeamsWriteAcceptsAWellFormedSeamAndNormalisesTheKey(t *testing.T) {
	in := seamTestWrite()
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
	require.Equal(t, seamTestKey, in.Seams[0].SeamKey, "keys are canonical upper-case ULIDs")
}

func TestValidateTechCardSeamsWriteEmptyDirectionIsUnknown(t *testing.T) {
	in := seamTestWrite()
	in.Seams[0].Direction = ""
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
	require.Equal(t, TechCardSeamDirectionUnknown, in.Seams[0].Direction)
}

// Every refusal names the offending field — the screen binds the error to the row.
func TestValidateTechCardSeamsWriteRefusals(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*TechCardSeamsWrite)
		field string
	}{
		{"no card", func(w *TechCardSeamsWrite) { w.TechCardId = 0 }, "tech_card_id"},
		{"empty", func(w *TechCardSeamsWrite) { w.Seams = nil }, "seams"},
		{"bad key", func(w *TechCardSeamsWrite) { w.Seams[0].SeamKey = "short" }, "seams[0].seam_key"},
		{"key with I", func(w *TechCardSeamsWrite) { w.Seams[0].SeamKey = "01JABCDEFGHJKMNPQRSTVWXYZI" }, "seams[0].seam_key"},
		{"dup key", func(w *TechCardSeamsWrite) { w.Seams = append(w.Seams, w.Seams[0]) }, "seams[1].seam_key"},
		{"status", func(w *TechCardSeamsWrite) { w.Seams[0].Status = "" }, "seams[0].status"},
		{"kind", func(w *TechCardSeamsWrite) { w.Seams[0].Kind = "seam" }, "seams[0].kind"},
		{"direction", func(w *TechCardSeamsWrite) { w.Seams[0].Direction = "left" }, "seams[0].direction"},
		{"source", func(w *TechCardSeamsWrite) { w.Seams[0].Source = "" }, "seams[0].source"},
		{"rejected closure", func(w *TechCardSeamsWrite) {
			w.Seams[0].Kind, w.Seams[0].Status = TechCardSeamKindClosure, TechCardSeamRejected
		}, "seams[0].status"},
		{"composite of single runs", func(w *TechCardSeamsWrite) { w.Seams[0].Kind = TechCardSeamKindComposite }, "seams[0].kind"},
		{"note too long", func(w *TechCardSeamsWrite) { w.Seams[0].Note = strings.Repeat("ш", 256) }, "seams[0].note"},
		{"size too long", func(w *TechCardSeamsWrite) { w.Seams[0].AnchoredSize = strings.Repeat("x", 17) }, "seams[0].anchored_size"},
		{"empty side", func(w *TechCardSeamsWrite) { w.Seams[0].SideB = nil }, "seams[0].side_b.parts"},
		{"too many anchors", func(w *TechCardSeamsWrite) {
			var side []TechCardSeamAnchor
			for i := 0; i < 9; i++ {
				side = append(side, seamTestAnchor("P", strings.Repeat("h", i+1)))
			}
			w.Seams[0].SideA = side
		}, "seams[0].side_a.parts"},
		{"no piece", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].PieceLineKey = " " }, "seams[0].side_a.parts[0].piece_line_key"},
		{"dup anchor", func(w *TechCardSeamsWrite) {
			w.Seams[0].SideA = append(w.Seams[0].SideA, seamTestAnchor("piece-front", "FP_L#2"))
		}, "seams[0].side_a.parts[1]"},
		{"four samples", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].Samples = w.Seams[0].SideA[0].Samples[:4] }, "seams[0].side_a.parts[0].samples"},
		{"sample out of frame", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].Samples[2].U = 1.01 }, "seams[0].side_a.parts[0].samples[2]"},
		{"sample NaN", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].Samples[0].V = math.NaN() }, "seams[0].side_a.parts[0].samples[0]"},
		{"perim share 0", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].PerimShare = 0 }, "seams[0].side_b.parts[0].perim_share"},
		{"perim share > 1", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].PerimShare = 1.5 }, "seams[0].side_b.parts[0].perim_share"},
		{"len 0", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].LenMm = 0 }, "seams[0].side_b.parts[0].len_mm"},
		{"len Inf", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].LenMm = math.Inf(1) }, "seams[0].side_b.parts[0].len_mm"},
		{"notches", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].Notches = -1 }, "seams[0].side_b.parts[0].notches"},
		{"turn NaN", func(w *TechCardSeamsWrite) { w.Seams[0].SideB[0].TurnDeg = math.NaN() }, "seams[0].side_b.parts[0].turn_deg"},
		{"range reversed", func(w *TechCardSeamsWrite) {
			w.Seams[0].SideA[0].RangeFrom, w.Seams[0].SideA[0].RangeTo = 0.6, 0.2
		}, "seams[0].side_a.parts[0].range_from"},
		{"range from only", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].RangeFrom = 0.3 }, "seams[0].side_a.parts[0].range_from"},
		{"hint too long", func(w *TechCardSeamsWrite) { w.Seams[0].SideA[0].EdgeHint = strings.Repeat("h", 65) }, "seams[0].side_a.parts[0].edge_hint"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := seamTestWrite()
			c.edit(&in)
			err := ValidateTechCardSeamsWrite(&in)
			require.Error(t, err)
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, c.field, ve.Field)
		})
	}
}

func TestValidateTechCardSeamsWriteLegalShapes(t *testing.T) {
	in := seamTestWrite()
	// A partial side, a composite across two pieces, a confirmed closure, direction unknown.
	in.Seams[0].Kind = TechCardSeamKindComposite
	in.Seams[0].SideA = []TechCardSeamAnchor{seamTestAnchor("PIECE-FRONT", "FP#4"), seamTestAnchor("PIECE-BACK", "BP#5")}
	in.Seams[0].SideB[0].RangeFrom, in.Seams[0].SideB[0].RangeTo = 0, 0.5
	in.Seams = append(in.Seams, TechCardSeamInput{
		SeamKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", Status: TechCardSeamConfirmed, Kind: TechCardSeamKindClosure,
		Direction: TechCardSeamDirectionUnknown, Source: TechCardSeamSourceManual,
		SideA: []TechCardSeamAnchor{seamTestAnchor("PIECE-FRONT", "FP#0")},
		SideB: []TechCardSeamAnchor{seamTestAnchor("PIECE-FRONT2", "FP2#0")},
	})
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
}

func TestTechCardSeamPieceLineKeysAreDistinctSortedUpper(t *testing.T) {
	in := TechCardSeamInput{
		SideA: []TechCardSeamAnchor{{PieceLineKey: "b"}, {PieceLineKey: "A"}},
		SideB: []TechCardSeamAnchor{{PieceLineKey: " a "}},
	}
	require.Equal(t, []string{"A", "B"}, in.PieceLineKeys())
}

// THE FINGERPRINT IS SCOPED TO THE SEAM'S PIECES: re-uploading the pocketing sheet must not stale a
// shell seam, re-uploading MAIN must; a piece with no block link widens to every scope.
func TestTechCardSeamSourceFingerprintScoping(t *testing.T) {
	sheets := map[string][]PatternSheetRef{
		"main":      {{LineKey: "S-MAIN", URL: "u/main.dxf", Version: 1}},
		"pocketing": {{LineKey: "S-POCK", URL: "u/pock.dxf", Version: 1}},
	}
	blocks := map[string][]PieceAreaBlockRef{
		"main":      {{BlockName: "FRONT", PieceLineKey: "PIECE-FRONT"}, {BlockName: "BACK", PieceLineKey: "PIECE-BACK"}},
		"pocketing": {{BlockName: "POCKET", PieceLineKey: "PIECE-POCKET"}},
	}
	shell := []string{"PIECE-BACK", "PIECE-FRONT"}
	before := TechCardSeamSourceFingerprint(shell, sheets, blocks)
	require.Equal(t, before, TechCardSeamSourceFingerprint([]string{"piece-back", "piece-front"}, sheets, blocks),
		"case-insensitive: the line keys are ULIDs on a case-insensitive column")
	require.Equal(t, PieceAreaSourceFingerprint(sheets["main"], blocks["main"]), before,
		"one device: a shell seam's fingerprint IS the MAIN scope's piece-area fingerprint")

	pock := map[string][]PatternSheetRef{"main": sheets["main"], "pocketing": {{LineKey: "S-POCK", URL: "u/pock2.dxf", Version: 2}}}
	require.Equal(t, before, TechCardSeamSourceFingerprint(shell, pock, blocks), "a pocketing re-upload does not stale a shell seam")

	main := map[string][]PatternSheetRef{"main": {{LineKey: "S-MAIN", URL: "u/main2.dxf", Version: 2}}, "pocketing": sheets["pocketing"]}
	require.NotEqual(t, before, TechCardSeamSourceFingerprint(shell, main, blocks), "a MAIN re-upload stales it")

	realiased := map[string][]PieceAreaBlockRef{
		"main":      {{BlockName: "FRONT_V2", PieceLineKey: "PIECE-FRONT"}, {BlockName: "BACK", PieceLineKey: "PIECE-BACK"}},
		"pocketing": blocks["pocketing"],
	}
	require.NotEqual(t, before, TechCardSeamSourceFingerprint(shell, sheets, realiased), "a re-aliased block stales it")

	// A piece with no link widens to every scope: then the pocketing re-upload DOES stale it.
	unlinked := []string{"PIECE-FRONT", "PIECE-LOOSE"}
	u := TechCardSeamSourceFingerprint(unlinked, sheets, blocks)
	require.NotEqual(t, u, TechCardSeamSourceFingerprint(unlinked, pock, blocks))

	// Cross-scope seam (pocket bag onto the front) covers both scopes.
	cross := []string{"PIECE-FRONT", "PIECE-POCKET"}
	c := TechCardSeamSourceFingerprint(cross, sheets, blocks)
	require.NotEqual(t, c, TechCardSeamSourceFingerprint(cross, pock, blocks))
	require.NotEqual(t, c, TechCardSeamSourceFingerprint(cross, main, blocks))
}
