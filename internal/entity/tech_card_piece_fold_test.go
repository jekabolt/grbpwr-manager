package entity

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func foldTestWrite() TechCardPieceFoldsWrite {
	return TechCardPieceFoldsWrite{TechCardId: 7, By: "ann", Folds: []TechCardPieceFoldInput{{
		FoldKey:      strings.ToLower(seamTestKey),
		PieceLineKey: " PIECE-PLACKET ",
		Seq:          1,
		Kind:         TechCardPieceFoldInward,
		Source:       TechCardPieceFoldSourceOwner,
		Line:         []TechCardSeamSample{{0.5, 0}, {0.5, 1}},
		FlipAt:       TechCardSeamSample{0.75, 0.5},
		EdgeHint:     "PL#3",
		EdgeSamples:  []TechCardSeamSample{{1, 0}, {1, 0.25}, {1, 0.5}, {1, 0.75}, {1, 1}},
		OffsetMm:     20.004,
		ContourSig:   "abcdef0123456789",
	}}}
}

func TestValidateTechCardPieceFoldsWriteAcceptsAndNormalises(t *testing.T) {
	in := foldTestWrite()
	require.NoError(t, ValidateTechCardPieceFoldsWrite(&in))
	f := in.Folds[0]
	require.Equal(t, seamTestKey, f.FoldKey)
	require.Equal(t, "PIECE-PLACKET", f.PieceLineKey)
	require.Equal(t, 20.0, f.OffsetMm, "rounded to the column's two decimals")
	require.True(t, f.HasEdge())
}

func TestValidateTechCardPieceFoldsWriteAcceptsALineWithoutLocator(t *testing.T) {
	in := foldTestWrite()
	in.Folds[0].EdgeHint, in.Folds[0].EdgeSamples, in.Folds[0].OffsetMm = "", nil, 0
	require.NoError(t, ValidateTechCardPieceFoldsWrite(&in))
}

func TestValidateTechCardPieceFoldsWriteAllowsSameSeqOnDifferentPieces(t *testing.T) {
	in := foldTestWrite()
	second := in.Folds[0]
	second.FoldKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	second.PieceLineKey = "PIECE-CUFF"
	in.Folds = append(in.Folds, second)
	require.NoError(t, ValidateTechCardPieceFoldsWrite(&in))
}

func TestValidateTechCardPieceFoldsWriteRefusals(t *testing.T) {
	other := func(w *TechCardPieceFoldsWrite) TechCardPieceFoldInput {
		f := w.Folds[0]
		f.FoldKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
		return f
	}
	cases := []struct {
		name  string
		edit  func(*TechCardPieceFoldsWrite)
		field string
	}{
		{"no card", func(w *TechCardPieceFoldsWrite) { w.TechCardId = 0 }, "tech_card_id"},
		{"empty", func(w *TechCardPieceFoldsWrite) { w.Folds = nil }, "folds"},
		{"bad key", func(w *TechCardPieceFoldsWrite) { w.Folds[0].FoldKey = "short" }, "folds[0].fold_key"},
		{"dup key", func(w *TechCardPieceFoldsWrite) {
			f := other(w)
			f.FoldKey, f.Seq = w.Folds[0].FoldKey, 2
			w.Folds = append(w.Folds, f)
		}, "folds[1].fold_key"},
		{"no piece", func(w *TechCardPieceFoldsWrite) { w.Folds[0].PieceLineKey = " " }, "folds[0].piece_line_key"},
		{"piece too long", func(w *TechCardPieceFoldsWrite) { w.Folds[0].PieceLineKey = strings.Repeat("P", 27) }, "folds[0].piece_line_key"},
		{"seq 0", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Seq = 0 }, "folds[0].seq"},
		{"seq 17", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Seq = 17 }, "folds[0].seq"},
		{"dup seq same piece", func(w *TechCardPieceFoldsWrite) {
			f := other(w)
			f.PieceLineKey = "piece-placket"
			w.Folds = append(w.Folds, f)
		}, "folds[1].seq"},
		{"kind", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Kind = "" }, "folds[0].kind"},
		{"source", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Source = "ai" }, "folds[0].source"},
		{"one point", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Line = w.Folds[0].Line[:1] }, "folds[0].line"},
		{"point out of frame", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Line[1].V = 1.2 }, "folds[0].line[1]"},
		{"point NaN", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Line[0].U = math.NaN() }, "folds[0].line[0]"},
		{"degenerate", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Line[1] = w.Folds[0].Line[0] }, "folds[0].line"},
		{"flip out of frame", func(w *TechCardPieceFoldsWrite) { w.Folds[0].FlipAt.U = -0.1 }, "folds[0].flip_at"},
		{"flip on line", func(w *TechCardPieceFoldsWrite) { w.Folds[0].FlipAt = TechCardSeamSample{0.5, 0.3} }, "folds[0].flip_at"},
		{"hint without samples", func(w *TechCardPieceFoldsWrite) {
			w.Folds[0].EdgeSamples, w.Folds[0].OffsetMm = nil, 0
		}, "folds[0].edge_samples"},
		{"offset without samples", func(w *TechCardPieceFoldsWrite) {
			w.Folds[0].EdgeSamples, w.Folds[0].EdgeHint = nil, ""
		}, "folds[0].edge_samples"},
		{"four samples", func(w *TechCardPieceFoldsWrite) { w.Folds[0].EdgeSamples = w.Folds[0].EdgeSamples[:4] }, "folds[0].edge_samples"},
		{"sample out of frame", func(w *TechCardPieceFoldsWrite) { w.Folds[0].EdgeSamples[3].U = 1.01 }, "folds[0].edge_samples[3]"},
		{"samples without offset", func(w *TechCardPieceFoldsWrite) { w.Folds[0].OffsetMm = 0 }, "folds[0].offset_mm"},
		{"offset rounds to 0", func(w *TechCardPieceFoldsWrite) { w.Folds[0].OffsetMm = 0.004 }, "folds[0].offset_mm"},
		{"offset too big", func(w *TechCardPieceFoldsWrite) { w.Folds[0].OffsetMm = 501 }, "folds[0].offset_mm"},
		{"offset Inf", func(w *TechCardPieceFoldsWrite) { w.Folds[0].OffsetMm = math.Inf(1) }, "folds[0].offset_mm"},
		{"hint too long", func(w *TechCardPieceFoldsWrite) { w.Folds[0].EdgeHint = strings.Repeat("h", 65) }, "folds[0].edge_hint"},
		{"sig too long", func(w *TechCardPieceFoldsWrite) { w.Folds[0].ContourSig = strings.Repeat("s", 65) }, "folds[0].contour_sig"},
		{"size too long", func(w *TechCardPieceFoldsWrite) { w.Folds[0].AnchoredSize = strings.Repeat("x", 17) }, "folds[0].anchored_size"},
		{"note too long", func(w *TechCardPieceFoldsWrite) { w.Folds[0].Note = strings.Repeat("ш", 256) }, "folds[0].note"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := foldTestWrite()
			c.edit(&in)
			err := ValidateTechCardPieceFoldsWrite(&in)
			require.Error(t, err)
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, c.field, ve.Field)
		})
	}
}
