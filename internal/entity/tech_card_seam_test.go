package entity

import (
	"encoding/json"
	"fmt"
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
			for i := 0; i <= TechCardSeamMaxAnchorsPerSide; i++ {
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
		{"placement x out of range", func(w *TechCardSeamsWrite) {
			w.Seams[0].SideA[0].Placement = &TechCardSeamPlacement{XMm: TechCardSeamPlacementMaxMm + 1}
		}, "seams[0].side_a.parts[0].placement"},
		{"placement y NaN", func(w *TechCardSeamsWrite) {
			w.Seams[0].SideA[0].Placement = &TechCardSeamPlacement{YMm: math.NaN()}
		}, "seams[0].side_a.parts[0].placement"},
		{"placement rot Inf", func(w *TechCardSeamsWrite) {
			w.Seams[0].SideA[0].Placement = &TechCardSeamPlacement{RotDeg: math.Inf(-1)}
		}, "seams[0].side_a.parts[0].placement.rot_deg"},
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

func TestValidateTechCardSeamsWriteOneEdgeTwiceOnASide(t *testing.T) {
	span := func(from, to float64) TechCardSeamAnchor {
		a := seamTestAnchor("PIECE-FRONT", "FP#4")
		a.RangeFrom, a.RangeTo = from, to
		return a
	}
	check := func(x, y TechCardSeamAnchor) error {
		in := seamTestWrite()
		in.Seams[0].Kind = TechCardSeamKindComposite
		in.Seams[0].SideA = []TechCardSeamAnchor{x, y}
		return ValidateTechCardSeamsWrite(&in)
	}
	// An edge broken by a pocket: the stretch above it and the stretch below it.
	require.NoError(t, check(span(0, 0.3), span(0.6, 1)))
	require.NoError(t, check(span(0, 0.5), span(0.5, 1)))
	for name, pair := range map[string][2]TechCardSeamAnchor{
		"same stretch":   {span(0.2, 0.4), span(0.2, 0.4)},
		"overlap":        {span(0, 0.5), span(0.4, 1)},
		"whole twice":    {span(0, 0), span(0, 0)},
		"whole and part": {span(0, 0), span(0.6, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			var ve *ValidationError
			require.ErrorAs(t, check(pair[0], pair[1]), &ve)
			require.Equal(t, "seams[0].side_a.parts[1]", ve.Field)
			require.Contains(t, ve.Error(), "same stretch")
		})
	}
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

func TestTechCardSeamSourceFingerprintDedupesCompatibilityBuckets(t *testing.T) {
	sheet := PatternSheetRef{LineKey: "S-MAIN", URL: "u/main.dxf", Version: 1}
	block := PieceAreaBlockRef{BlockName: "FRONT", PieceLineKey: "PIECE-FRONT"}
	want := PieceAreaSourceFingerprint([]PatternSheetRef{sheet}, []PieceAreaBlockRef{block})

	// A line-only alias/sheet of a sorted ordinary FABRIC line was briefly exposed under both its
	// resolved purpose and raw line key. The seam covers both buckets through the duplicated block,
	// but its source is still one sheet and one link, so deploy compatibility must not move the hash.
	got := TechCardSeamSourceFingerprint([]string{"PIECE-FRONT"},
		map[string][]PatternSheetRef{"main": {sheet}, "FABRIC-LINE": {sheet}},
		map[string][]PieceAreaBlockRef{"main": {block}, "FABRIC-LINE": {block}})
	require.Equal(t, want, got)
}

// A part piece's hand placement and an inner-line edge hint ("i<k>") pass validation untouched.
func TestValidateTechCardSeamsWriteKeepsPlacementAndInnerLineHint(t *testing.T) {
	in := seamTestWrite()
	in.Seams[0].SideB[0].EdgeHint = "i3"
	in.Seams[0].SideB[0].Placement = &TechCardSeamPlacement{XMm: -12.5, YMm: 40, RotDeg: 270, Flip: true}
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
	require.Equal(t, "i3", in.Seams[0].SideB[0].EdgeHint)
	require.Equal(t, &TechCardSeamPlacement{XMm: -12.5, YMm: 40, RotDeg: 270, Flip: true}, in.Seams[0].SideB[0].Placement)
}

func seamTestPoseWrite() TechCardSeamsWrite {
	in := seamTestWrite()
	s := &in.Seams[0]
	s.Kind = TechCardSeamKindPose
	s.Source = TechCardSeamSourceManual
	s.Direction = TechCardSeamDirectionUnknown
	s.SideA[0].Placement = &TechCardSeamPlacement{XMm: 120, YMm: -35.5, RotDeg: 90, Flip: true}
	return in
}

// A pose (P8): one moved piece with its placement, one host piece, confirmed.
func TestValidateTechCardSeamsWritePose(t *testing.T) {
	in := seamTestPoseWrite()
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
	require.False(t, TechCardSeamKindPose.IsSeam())
	for _, k := range []TechCardSeamKind{TechCardSeamKindEdge, TechCardSeamKindPartial, TechCardSeamKindComposite,
		TechCardSeamKindSurface, TechCardSeamKindClosure} {
		require.True(t, k.IsSeam(), k)
	}

	cases := []struct {
		name  string
		edit  func(*TechCardSeamInput)
		field string
		code  string
	}{
		{"no placement", func(s *TechCardSeamInput) { s.SideA[0].Placement = nil }, "seams[0].side_a.parts[0].placement", "required"},
		{"placement on host only", func(s *TechCardSeamInput) {
			s.SideB[0].Placement, s.SideA[0].Placement = s.SideA[0].Placement, nil
		}, "seams[0].side_a.parts[0].placement", "required"},
		{"two moved", func(s *TechCardSeamInput) {
			s.SideA = append(s.SideA, seamTestAnchor("PIECE-SLEEVE", "SL#1"))
		}, "seams[0].side_a.parts", "pose_one_anchor"},
		{"two hosts", func(s *TechCardSeamInput) {
			s.SideB = append(s.SideB, seamTestAnchor("PIECE-SLEEVE", "SL#1"))
		}, "seams[0].side_b.parts", "pose_one_anchor"},
		{"rejected", func(s *TechCardSeamInput) { s.Status = TechCardSeamRejected }, "seams[0].status", "rejected_pose"},
		{"bad anchor still refused", func(s *TechCardSeamInput) { s.SideB[0].LenMm = 0 }, "seams[0].side_b.parts[0].len_mm", "out_of_range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := seamTestPoseWrite()
			c.edit(&in.Seams[0])
			err := ValidateTechCardSeamsWrite(&in)
			require.Error(t, err)
			var fv *ValidationError
			require.ErrorAs(t, err, &fv)
			require.Equal(t, c.field, fv.Field)
			require.Equal(t, c.code, fv.Reason)
		})
	}
}

// Prod card 5 «load balance», OP 570: the collar (NCK_1, 433.5 mm) on the whole neckline. side_b
// below is the row prod already stores for it (7 anchors: the back's edges #11 and #5 are each cut
// in two stretches). The owner's new pick adds both plackets; split by a fold cut each, as the back
// is, that is 11 anchors. The old bound of 8 refused it («TOO MANY PARTS», ACCEPT saved nothing).
const prodCard5Op570SideB = `[
 {"len_mm": 52.3, "notches": 1, "samples": [{"u": 0.289, "v": 0.968}, {"u": 0.266, "v": 0.972}, {"u": 0.243, "v": 0.976}, {"u": 0.223, "v": 0.985}, {"u": 0.213, "v": 1}], "range_to": 0.5825, "turn_deg": -159.5, "edge_hint": "01M1M1S2WRVE27KVQT0GQ9490Y#11", "range_from": 0, "contour_sig": "c7839b9d78be8b7f", "perim_share": 0.0205, "piece_line_key": "01M1M1S2WRVE27KVQT0GQ9490Y"},
 {"len_mm": 92.3, "notches": 0, "samples": [{"u": 0.987, "v": 0.914}, {"u": 0.873, "v": 0.926}, {"u": 0.776, "v": 0.946}, {"u": 0.694, "v": 0.972}, {"u": 0.628, "v": 1}], "range_to": 0, "turn_deg": -121.9, "edge_hint": "01M1M1S2WR7Z1ED4Q4DPAXXA50#1", "range_from": 0, "contour_sig": "a8b3f8d7ae54a595", "perim_share": 0.058, "piece_line_key": "01M1M1S2WR7Z1ED4Q4DPAXXA50"},
 {"len_mm": 52.3, "notches": 1, "samples": [{"u": 0.289, "v": 0.968}, {"u": 0.266, "v": 0.972}, {"u": 0.243, "v": 0.976}, {"u": 0.223, "v": 0.985}, {"u": 0.213, "v": 1}], "range_to": 1, "turn_deg": -159.5, "edge_hint": "01M1M1S2WRVE27KVQT0GQ9490Y#11", "range_from": 0.6032, "contour_sig": "c7839b9d78be8b7f", "perim_share": 0.0205, "piece_line_key": "01M1M1S2WRVE27KVQT0GQ9490Y"},
 {"len_mm": 94.5, "notches": 2, "samples": [{"u": 0.585, "v": 0.875}, {"u": 0.543, "v": 0.87}, {"u": 0.5, "v": 0.868}, {"u": 0.457, "v": 0.87}, {"u": 0.415, "v": 0.875}], "range_to": 0, "turn_deg": -56.7, "edge_hint": "01M1M1S2WRVE27KVQT0GQ9490Y#8", "range_from": 0, "contour_sig": "c7839b9d78be8b7f", "perim_share": 0.037, "piece_line_key": "01M1M1S2WRVE27KVQT0GQ9490Y"},
 {"len_mm": 52.3, "notches": 1, "samples": [{"u": 0.787, "v": 1}, {"u": 0.777, "v": 0.985}, {"u": 0.757, "v": 0.976}, {"u": 0.734, "v": 0.972}, {"u": 0.711, "v": 0.968}], "range_to": 0.3968, "turn_deg": -159.5, "edge_hint": "01M1M1S2WRVE27KVQT0GQ9490Y#5", "range_from": 0, "contour_sig": "c7839b9d78be8b7f", "perim_share": 0.0205, "piece_line_key": "01M1M1S2WRVE27KVQT0GQ9490Y"},
 {"len_mm": 92.3, "notches": 0, "samples": [{"u": 0.372, "v": 1}, {"u": 0.306, "v": 0.972}, {"u": 0.224, "v": 0.946}, {"u": 0.127, "v": 0.926}, {"u": 0.013, "v": 0.914}], "range_to": 0, "turn_deg": -121.9, "edge_hint": "01M1M1S2WR4DTBW8FVRST2G68Y#2", "range_from": 0, "contour_sig": "ddbc1c4b9b48d0ed", "perim_share": 0.058, "piece_line_key": "01M1M1S2WR4DTBW8FVRST2G68Y"},
 {"len_mm": 52.3, "notches": 1, "samples": [{"u": 0.787, "v": 1}, {"u": 0.777, "v": 0.985}, {"u": 0.757, "v": 0.976}, {"u": 0.734, "v": 0.972}, {"u": 0.711, "v": 0.968}], "range_to": 1, "turn_deg": -159.5, "edge_hint": "01M1M1S2WRVE27KVQT0GQ9490Y#5", "range_from": 0.4175, "contour_sig": "c7839b9d78be8b7f", "perim_share": 0.0205, "piece_line_key": "01M1M1S2WRVE27KVQT0GQ9490Y"}
]`

func TestValidateTechCardSeamsWriteAcceptsProdCard5Neckline(t *testing.T) {
	var side []TechCardSeamAnchor
	require.NoError(t, json.Unmarshal([]byte(prodCard5Op570SideB), &side))
	for _, placket := range []string{"01M1M1S2WRSJFKPTJQD6NP5B34", "01M1M1S2WRKPF7CBSVY8VFH76T"} { // PLCK_R, PLCK_L
		for _, r := range [][2]float64{{0, 0.45}, {0.55, 1}} {
			a := seamTestAnchor(placket, placket+"#0")
			a.RangeFrom, a.RangeTo = r[0], r[1]
			side = append(side, a)
		}
	}
	require.Len(t, side, 11)
	require.Greater(t, len(side), 8, "the old bound refused this side")

	in := seamTestWrite()
	in.Seams[0].Kind = TechCardSeamKindComposite
	in.Seams[0].Source = TechCardSeamSourceManual
	in.Seams[0].SideA = []TechCardSeamAnchor{seamTestAnchor("01M1M1S2WRD4PS0KGFPVVX6301", "01M1M1S2WRD4PS0KGFPVVX6301#3")} // NCK_1
	in.Seams[0].SideB = side
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
}

// A collar on a whole neckline: two fronts, the back split by fold cuts, two plackets — 11 anchors,
// the shape prod card 5's op 570 needs. The old bound of 8 refused it.
func TestValidateTechCardSeamsWriteAcceptsAWholeNeckline(t *testing.T) {
	in := seamTestWrite()
	in.Seams[0].Kind = TechCardSeamKindComposite
	var side []TechCardSeamAnchor
	for i := 0; i < 11; i++ {
		side = append(side, seamTestAnchor(fmt.Sprintf("PIECE-%d", i), fmt.Sprintf("P%d#1", i)))
	}
	in.Seams[0].SideB = side
	require.NoError(t, ValidateTechCardSeamsWrite(&in))
}
