package techcard

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func fusingZoneJSONTestRing(coords ...string) entity.TechCardPieceFusingSpline {
	points := make([]entity.TechCardPieceFusingSplinePoint, 0, len(coords)/2)
	for i := 0; i < len(coords); i += 2 {
		points = append(points, entity.TechCardPieceFusingSplinePoint{
			X: decimal.RequireFromString(coords[i]), Y: decimal.RequireFromString(coords[i+1]), Corner: true,
		})
	}
	return entity.TechCardPieceFusingSpline{Points: points}
}

func fusingZoneJSONTestRingPtr(coords ...string) *entity.TechCardPieceFusingSpline {
	ring := fusingZoneJSONTestRing(coords...)
	return &ring
}

func TestPieceFusingZoneJSONRoundTripIncludesIslands(t *testing.T) {
	zone := &entity.TechCardPieceFusingZone{
		Spline:  fusingZoneJSONTestRingPtr("0", "0", "20", "0", "20", "10", "0", "10"),
		Islands: []entity.TechCardPieceFusingSpline{fusingZoneJSONTestRing("30", "0", "40", "0", "40", "10", "30", "10")},
		Holes:   []entity.TechCardPieceFusingSpline{fusingZoneJSONTestRing("32", "2", "36", "2", "36", "5", "32", "5")},
	}

	encoded, err := pieceFusingZoneJSONValue(zone)
	if err != nil {
		t.Fatalf("encode fusing zone: %v", err)
	}
	raw, ok := encoded.(string)
	if !ok {
		t.Fatalf("encoded fusing zone has type %T, want string", encoded)
	}
	piece := entity.TechCardPiece{LineKey: "FRONT", FusingZoneRaw: entity.RawJSON(raw)}
	if err := decodePieceFusingJSON(&piece); err != nil {
		t.Fatalf("decode fusing zone: %v", err)
	}
	if piece.FusingZone == nil || len(piece.FusingZone.Islands) != 1 || len(piece.FusingZone.Holes) != 1 {
		t.Fatalf("decoded rings mismatch: %#v", piece.FusingZone)
	}
	if got := piece.FusingZone.Islands[0].Points[2].X.String(); got != "40" {
		t.Fatalf("decoded island x = %q, want 40", got)
	}
	if got := piece.FusingZone.Holes[0].Points[3].Y.String(); got != "5" {
		t.Fatalf("decoded hole y = %q, want 5", got)
	}

	reencoded, err := pieceFusingZoneJSONValue(piece.FusingZone)
	if err != nil {
		t.Fatalf("re-encode fusing zone: %v", err)
	}
	if reencoded != raw {
		t.Fatalf("JSON round trip moved the stored value:\n got  %s\n want %s", reencoded, raw)
	}
}
