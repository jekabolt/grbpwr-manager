package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func pbDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

// TestPieceAreaWriteResolvesUngradedSentinel: size_id 0 on the wire is «this piece does not grade»
// and MUST become SQL NULL, not the integer 0.
//
// The distinction is not cosmetic and not deferrable to the reader: MySQL's UNIQUE treats every NULL
// as distinct, so a piece stored under a literal size 0 would collide with itself across scopes on
// re-measure — and, in the other direction, an ungraded piece that reads back as «size 0» would be
// handed to whichever size resolves to 0 and stolen from all the others, understating every other
// size's garment area by that piece.
func TestPieceAreaWriteResolvesUngradedSentinel(t *testing.T) {
	in, err := PieceAreaWriteFromPb(7, "main", []string{"SHEET1"}, []*pb_common.TechCardPieceArea{
		{PieceLineKey: "PIECE_GRADED", SizeId: 4, AreaCm2: pbDec("1234.50")},
		{PieceLineKey: "PIECE_UNGRADED", SizeId: 0, AreaCm2: pbDec("99.25")},
	}, "14", pbDec("10"), "tester")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(in.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(in.Rows))
	}
	if !in.Rows[0].SizeId.Valid || in.Rows[0].SizeId.Int64 != 4 {
		t.Errorf("graded piece: SizeId = %+v, want valid 4", in.Rows[0].SizeId)
	}
	if in.Rows[1].SizeId.Valid {
		t.Errorf("ungraded piece: SizeId = %+v, want NULL — 0 is the sentinel, not a size", in.Rows[1].SizeId)
	}
	// Conditions ride every row: they are a property of the measurement, and a row that lost them
	// would be an area nobody can reproduce.
	for i, r := range in.Rows {
		if r.ContourLayer != "14" {
			t.Errorf("row %d: ContourLayer = %q, want %q", i, r.ContourLayer, "14")
		}
		if r.SeamAllowanceMm.String() != "10" {
			t.Errorf("row %d: SeamAllowanceMm = %s, want 10", i, r.SeamAllowanceMm)
		}
	}
	if in.ScopeKey != "main" || in.TechCardId != 7 || in.ParsedBy != "tester" {
		t.Errorf("envelope = %+v, want card 7 / scope main / tester", in)
	}
}

// TestPieceAreaWriteRefusesMissingArea: a measurement that produced no number is a FAILED READ, and
// the only safe thing to do with it is refuse.
//
// Turning it into zero would be the whole failure mode this feature exists to avoid, arrived at from
// the other side: a piece with no area lowers the garment's area, a lower area lowers the derived
// norm, and an understated norm is discovered in the warehouse when the cloth runs out — not on the
// screen where it was invented.
func TestPieceAreaWriteRefusesMissingArea(t *testing.T) {
	for _, c := range []struct {
		name string
		area *pb_decimal.Decimal
	}{
		{"nil", nil},
		{"empty", pbDec("")},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := PieceAreaWriteFromPb(7, "main", nil, []*pb_common.TechCardPieceArea{
				{PieceLineKey: "PIECE", SizeId: 4, AreaCm2: c.area},
			}, "14", pbDec("10"), "tester")
			if err == nil {
				t.Fatal("missing area accepted; it must be refused, never defaulted to zero")
			}
		})
	}
}

// TestPieceAreaWriteRejectsUnparseableArea keeps a malformed decimal an ERROR rather than a silent
// zero — same argument as above, different way in.
func TestPieceAreaWriteRejectsUnparseableArea(t *testing.T) {
	if _, err := PieceAreaWriteFromPb(7, "main", nil, []*pb_common.TechCardPieceArea{
		{PieceLineKey: "PIECE", SizeId: 4, AreaCm2: pbDec("не число")},
	}, "14", pbDec("10"), "tester"); err == nil {
		t.Fatal("unparseable area accepted")
	}
}
