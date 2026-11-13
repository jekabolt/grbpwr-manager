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

func TestPieceFusingJSONRoundTripKeepsModes(t *testing.T) {
	spline := fusingZoneJSONTestRingPtr("0", "0", "20", "0", "20", "10", "0", "10")
	spline.GradeMode = entity.PieceFusingGradeModeFollowLine
	spline.Anchor = &entity.TechCardPieceFusingAnchor{X: decimal.RequireFromString("12.5"), Y: decimal.RequireFromString("-3")}
	island := fusingZoneJSONTestRing("30", "0", "40", "0", "40", "10", "30", "10")
	island.GradeMode = entity.PieceFusingGradeModeGrow
	zone := &entity.TechCardPieceFusingZone{Spline: spline, Islands: []entity.TechCardPieceFusingSpline{island}}
	edges := []entity.TechCardPieceFusingEdge{
		{Edge: 0, StartMm: decimal.NewFromInt(10), EndMm: decimal.NewFromInt(10)},
		{Edge: 2, StartMm: decimal.NewFromInt(5), EndMm: decimal.RequireFromString("35.5"),
			StartMode: entity.PieceFusingEndModeNotch, EndMode: entity.PieceFusingEndModeShare},
	}

	zoneRaw, err := pieceFusingZoneJSONValue(zone)
	if err != nil {
		t.Fatalf("encode fusing zone: %v", err)
	}
	edgesRaw, err := pieceFusingEdgesJSON(edges)
	if err != nil {
		t.Fatalf("encode fusing edges: %v", err)
	}
	piece := entity.TechCardPiece{
		LineKey: "FRONT", FusingZoneRaw: entity.RawJSON(zoneRaw.(string)), FusingEdgesRaw: entity.RawJSON(edgesRaw.(string)),
	}
	if err := decodePieceFusingJSON(&piece); err != nil {
		t.Fatalf("decode fusing JSON: %v", err)
	}
	got := piece.FusingZone.Spline
	if got.GradeMode != entity.PieceFusingGradeModeFollowLine || got.Anchor == nil ||
		got.Anchor.X.String() != "12.5" || got.Anchor.Y.String() != "-3" {
		t.Fatalf("decoded spline mode mismatch: %#v", got)
	}
	if mode := piece.FusingZone.Islands[0].GradeMode; mode != entity.PieceFusingGradeModeGrow {
		t.Fatalf("decoded island mode = %q, want grow", mode)
	}
	if e := piece.FusingEdges[0]; e.StartMode != entity.PieceFusingEndModeMm || e.EndMode != entity.PieceFusingEndModeMm {
		t.Fatalf("decoded default edge modes = %q / %q, want empty", e.StartMode, e.EndMode)
	}
	if e := piece.FusingEdges[1]; e.StartMode != entity.PieceFusingEndModeNotch || e.EndMode != entity.PieceFusingEndModeShare {
		t.Fatalf("decoded edge modes = %q / %q", e.StartMode, e.EndMode)
	}
	if reencoded, _ := pieceFusingZoneJSONValue(piece.FusingZone); reencoded != zoneRaw {
		t.Fatalf("zone JSON round trip moved the stored value:\n got  %s\n want %s", reencoded, zoneRaw)
	}
	if reencoded, _ := pieceFusingEdgesJSON(piece.FusingEdges); reencoded != edgesRaw {
		t.Fatalf("edges JSON round trip moved the stored value:\n got  %s\n want %s", reencoded, edgesRaw)
	}
}

// Rows written before the modes existed carry none of the new keys; writing a default ring or edge
// must still produce exactly that JSON.
func TestPieceFusingJSONDefaultModesWriteTheOldShape(t *testing.T) {
	zoneRaw, err := pieceFusingZoneJSONValue(&entity.TechCardPieceFusingZone{Spline: fusingZoneJSONTestRingPtr("0", "0", "20", "0", "20", "10")})
	if err != nil {
		t.Fatalf("encode fusing zone: %v", err)
	}
	if want := `{"spline":{"pts":[[0,0,1],[20,0,1],[20,10,1]]}}`; zoneRaw != want {
		t.Fatalf("default zone JSON = %s, want %s", zoneRaw, want)
	}
	edgesRaw, err := pieceFusingEdgesJSON([]entity.TechCardPieceFusingEdge{{Edge: 1, StartMm: decimal.NewFromInt(10), EndMm: decimal.NewFromInt(0)}})
	if err != nil {
		t.Fatalf("encode fusing edges: %v", err)
	}
	if want := `[{"edge":1,"start_mm":10,"end_mm":0}]`; edgesRaw != want {
		t.Fatalf("default edges JSON = %s, want %s", edgesRaw, want)
	}
}

func TestPieceFusingJSONRejectsBrokenModes(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown grade mode":      `{"spline":{"pts":[[0,0,1],[20,0,1],[20,10,1]],"grade_mode":"wobble"}}`,
		"follow line, no anchor":  `{"spline":{"pts":[[0,0,1],[20,0,1],[20,10,1]],"grade_mode":"follow_line"}}`,
		"anchor on a fixed ring":  `{"spline":{"pts":[[0,0,1],[20,0,1],[20,10,1]],"anchor":[1,1]}}`,
		"anchor with three parts": `{"spline":{"pts":[[0,0,1],[20,0,1],[20,10,1]],"grade_mode":"follow_line","anchor":[1,1,1]}}`,
	} {
		piece := entity.TechCardPiece{LineKey: "FRONT", FusingZoneRaw: entity.RawJSON(raw)}
		if err := decodePieceFusingJSON(&piece); err == nil {
			t.Errorf("%s: decoded, want an error", name)
		}
	}
	piece := entity.TechCardPiece{LineKey: "FRONT", FusingEdgesRaw: entity.RawJSON(`[{"edge":1,"start_mm":0,"end_mm":0,"end_mode":"wobble"}]`)}
	if err := decodePieceFusingJSON(&piece); err == nil {
		t.Error("unknown end mode decoded, want an error")
	}
}
