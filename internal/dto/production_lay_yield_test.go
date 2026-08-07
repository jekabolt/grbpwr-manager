package dto

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// ------------------------------------------------------------------ fixtures

func layoutBlob(t *testing.T, l *pb_common.TechCardMarkerLayout) string {
	t.Helper()
	b, err := protojson.Marshal(l)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

func piece(id int32, name, lineKey string, sizeID, quantity int32) *pb_common.TechCardMarkerPiece {
	return &pb_common.TechCardMarkerPiece{
		PieceId: id, Name: name, PieceLineKey: lineKey, SizeId: sizeID, Quantity: quantity,
	}
}

func placement(pieceID int32, flipped bool) *pb_common.TechCardMarkerPlacement {
	return &pb_common.TechCardMarkerPlacement{PieceId: pieceID, Flipped: flipped}
}

func placements(pieceID int32, asDrawn, mirrored int) []*pb_common.TechCardMarkerPlacement {
	out := make([]*pb_common.TechCardMarkerPlacement, 0, asDrawn+mirrored)
	for i := 0; i < asDrawn; i++ {
		out = append(out, placement(pieceID, false))
	}
	for i := 0; i < mirrored; i++ {
		out = append(out, placement(pieceID, true))
	}
	return out
}

func comp(pairs ...[2]int32) []*pb_common.TechCardMarkerCompositionEntry {
	out := make([]*pb_common.TechCardMarkerCompositionEntry, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, &pb_common.TechCardMarkerCompositionEntry{SizeId: p[0], Quantity: p[1]})
	}
	return out
}

func marked(s entity.TechCardPieceCutSymmetry) sql.NullString {
	return sql.NullString{String: string(s), Valid: true}
}

// unmarked is cut_symmetry IS NULL — «НЕ РАЗМЕЧЕНО» (0275), the state that must never read as
// `identical`.
var unmarked = sql.NullString{}

// ------------------------------------------------- the four schema versions

// Every stored blob stays readable forever (techcard.proto), and the distiller must say something
// TRUE about each of the four generations rather than the same thing about all of them. What changes
// across the versions is not the geometry but WHAT CAN BE ASKED of it: v1 cannot attribute a piece to
// the card, v2 can; v1-v2 cannot speak about chirality, v3 can; v1-v3 do not know their own состав,
// v4 does. Each of those gaps has to come back as «не могу», not as a zero.
func TestMarkerYieldFromBlobAcrossSchemaVersions(t *testing.T) {
	t.Run("v1 geometry only: pieces are unattributable", func(t *testing.T) {
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 1,
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "", 0, 2)},
			Placements:    placements(1, 4, 0),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if y.SchemaVersion != 1 {
			t.Errorf("schema version = %d, want 1", y.SchemaVersion)
		}
		if y.UnattributedPieces != 1 {
			t.Errorf("unattributed pieces = %d, want 1", y.UnattributedPieces)
		}
		if y.Attributable() {
			t.Error("a v1 blob must not claim its pieces are attributable")
		}
		if y.CompositionKnown() {
			t.Error("a v1 blob carries no состав")
		}
		if y.ChiralityKnown() {
			t.Error("a v1 blob has no `flipped`, so chirality is not knowable")
		}
		// The instances are still visible in the unattributed bucket — they are not silently dropped.
		if got := y.Pieces[MarkerPieceKey{}]; got.AsDrawn != 4 {
			t.Errorf("unattributed bucket = %+v, want 4 as-drawn", got)
		}
	})

	t.Run("v1 stays unattributable even when the bytes carry a line key", func(t *testing.T) {
		// piece_line_key did not exist before schema 2, so protojson filling it here says nothing
		// about what the writer resolved. The version, not the field, is the authority.
		y, err := MarkerYieldFromBlob(
			`{"schemaVersion":1,"pieces":[{"pieceId":1,"pieceLineKey":"K_FRONT","quantity":1}],` +
				`"composition":[{"sizeId":10,"quantity":1}],"placements":[{"pieceId":1}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if y.UnattributedPieces != 0 {
			t.Fatalf("unattributed = %d; the fixture deliberately carries a key", y.UnattributedPieces)
		}
		if y.Attributable() {
			t.Error("a blob declaring schema 1 must never be attributable")
		}
		if got := y.PerLayerInstances("K_FRONT", 10); got.Known {
			t.Errorf("got %+v, want unknown", got)
		}
	})

	t.Run("v2 adds piece_line_key: attributable, still no chirality and no состав", func(t *testing.T) {
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 2,
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 2)},
			Placements:    placements(1, 4, 0),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !y.Attributable() {
			t.Error("a v2 blob with resolved keys is attributable")
		}
		if y.ChiralityKnown() {
			t.Error("v2 predates `flipped`")
		}
		if y.CompositionKnown() {
			t.Error("v2 predates the состав")
		}
		if got := y.Pieces[MarkerPieceKey{PieceLineKey: "K_FRONT"}]; got != (MarkerPieceCounts{AsDrawn: 4}) {
			t.Errorf("counts = %+v, want 4 as-drawn", got)
		}
		if y.PieceNames["K_FRONT"] != "ПОЛОЧКА" {
			t.Errorf("piece name = %q, want ПОЛОЧКА", y.PieceNames["K_FRONT"])
		}
	})

	t.Run("v3 adds flipped: chirality becomes evidence", func(t *testing.T) {
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 3,
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 2)},
			Placements:    placements(1, 3, 3),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !y.ChiralityKnown() {
			t.Error("v3 carries `flipped`")
		}
		if got := y.Pieces[MarkerPieceKey{PieceLineKey: "K_FRONT"}]; got != (MarkerPieceCounts{AsDrawn: 3, Mirrored: 3}) {
			t.Errorf("counts = %+v, want 3/3", got)
		}
		if y.CompositionKnown() {
			t.Error("v3 predates the состав")
		}
	})

	t.Run("v4 adds the состав and per-size pieces", func(t *testing.T) {
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}, [2]int32{20, 3}),
			Pieces: []*pb_common.TechCardMarkerPiece{
				piece(1, "ПОЛОЧКА", "K_FRONT", 10, 2),
				piece(2, "ПОЛОЧКА", "K_FRONT", 20, 2),
			},
			Placements: append(placements(1, 2, 2), placements(2, 3, 3)...),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !y.CompositionKnown() || y.TotalUnits != 5 {
			t.Fatalf("composition = %v, total units = %d, want {10:2,20:3} and 5", y.Composition, y.TotalUnits)
		}
		if got := y.Pieces[MarkerPieceKey{PieceLineKey: "K_FRONT", SizeId: 10}]; got != (MarkerPieceCounts{AsDrawn: 2, Mirrored: 2}) {
			t.Errorf("size 10 counts = %+v, want 2/2", got)
		}
		if got := y.Pieces[MarkerPieceKey{PieceLineKey: "K_FRONT", SizeId: 20}]; got != (MarkerPieceCounts{AsDrawn: 3, Mirrored: 3}) {
			t.Errorf("size 20 counts = %+v, want 3/3", got)
		}
	})
}

// A blob that declares 0 is a v1 blob (the reading MarkerLayoutFactsFromPb's contract states), and a
// blob from a NEWER server must stay readable after a rollback — that is what DiscardUnknown is for.
// Neither of those is corruption, and neither may become an error.
func TestMarkerYieldFromBlobToleratesVersionEdges(t *testing.T) {
	t.Run("schema_version 0 reads as v1", func(t *testing.T) {
		y, err := MarkerYieldFromBlob(`{"pieces":[{"pieceId":1,"quantity":1}],"placements":[{"pieceId":1}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if y.SchemaVersion != 1 {
			t.Errorf("schema version = %d, want 1", y.SchemaVersion)
		}
		if y.ChiralityKnown() {
			t.Error("a version-0 blob must not be credited with `flipped`")
		}
	})

	t.Run("a future version with an unknown field still parses", func(t *testing.T) {
		y, err := MarkerYieldFromBlob(
			`{"schemaVersion":5,"someFutureField":7,` +
				`"pieces":[{"pieceId":1,"pieceLineKey":"K","quantity":1}],` +
				`"composition":[{"sizeId":10,"quantity":1}],` +
				`"placements":[{"pieceId":1,"flipped":true}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if y.SchemaVersion != 5 || !y.ChiralityKnown() {
			t.Errorf("version = %d, chirality known = %v; a newer blob keeps its version and its flips",
				y.SchemaVersion, y.ChiralityKnown())
		}
	})
}

// ------------------------------------------------------- incoherent blobs

// A blob that is not a coherent document must FAIL, not resolve to a smaller number. Every case here
// would otherwise attribute fewer instances than were really laid, and fewer instances is a shortage
// the cutting room cannot reproduce — a BLOCKER, which §6.2 ranks above UNKNOWN precisely because it
// is supposed to mean «доказано».
func TestMarkerYieldFromBlobRefusesIncoherentBlobs(t *testing.T) {
	full := &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{10, 1}),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
		Placements:    placements(1, 1, 0),
	}
	cases := []struct {
		name     string
		blob     string
		contains string
	}{
		{"empty string", "", "does not parse"},
		{"not json at all", "не json", "does not parse"},
		{"schema_version is not a number", `{"schemaVersion":"четыре"}`, "does not parse"},
		{"empty object has no placements", `{}`, "no placements"},
		{
			"a layout with pieces but no placements",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				Composition:   comp([2]int32{10, 1}),
				Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
			}),
			"no placements",
		},
		{
			"placement points at a piece that is not there",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				Composition:   comp([2]int32{10, 1}),
				Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
				Placements:    placements(7, 1, 0),
			}),
			"unknown piece_id 7",
		},
		{
			"two pieces share a piece_id",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				Composition:   comp([2]int32{10, 1}),
				Pieces: []*pb_common.TechCardMarkerPiece{
					piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1),
					piece(1, "СПИНКА", "K_BACK", 10, 1),
				},
				Placements: placements(1, 1, 0),
			}),
			"piece_id 1 twice",
		},
		{
			"a mirror in a blob that predates `flipped`",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 2,
				Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 2)},
				Placements:    placements(1, 1, 1),
			}),
			"flipped in a blob declaring schema_version 2",
		},
		{
			"состав lists one size twice",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				Composition:   comp([2]int32{10, 1}, [2]int32{10, 2}),
				Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
				Placements:    placements(1, 1, 0),
			}),
			"состав",
		},
		{
			"состав carries a zero quantity",
			layoutBlob(t, &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				Composition:   comp([2]int32{10, 0}),
				Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
				Placements:    placements(1, 1, 0),
			}),
			"состав",
		},
		{"a negative schema version", `{"schemaVersion":-3,"placements":[{"pieceId":1}]}`, "schema_version -3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			y, err := MarkerYieldFromBlob(c.blob)
			if err == nil {
				t.Fatalf("expected a refusal, got %+v", y)
			}
			if !strings.Contains(err.Error(), c.contains) {
				t.Errorf("error %q does not mention %q", err, c.contains)
			}
			// A refusal must hand back nothing usable: a half-filled yield read as «this marker cuts
			// little» is the exact substitution the refusal exists to prevent.
			if y.SchemaVersion != 0 || len(y.Pieces) != 0 || y.TotalUnits != 0 {
				t.Errorf("a refused blob must return the zero yield, got %+v", y)
			}
		})
	}

	// …and the very same document, coherent, parses. Without this the table above would also pass on
	// a distiller that refuses everything.
	if _, err := MarkerYieldFromBlob(layoutBlob(t, full)); err != nil {
		t.Fatalf("the coherent control fixture must parse, got %v", err)
	}
}

// ------------------------------------------------- legacy состав from the summary

func TestWithSummaryComposition(t *testing.T) {
	legacy := layoutBlob(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 2,
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 2)},
		Placements:    placements(1, 8, 0),
	})
	y, err := MarkerYieldFromBlob(legacy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Before the summary is folded in, the blob cannot answer — and says so instead of dividing by a
	// zero TotalUnits.
	if got := y.PerLayerInstances("K_FRONT", 10); got.Known {
		t.Fatalf("a blob with no состав must not answer, got %+v", got)
	}

	filled, err := y.WithSummaryComposition(10, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filled.TotalUnits != 4 || filled.Composition[10] != 4 {
		t.Fatalf("composition = %v / %d, want {10:4} / 4", filled.Composition, filled.TotalUnits)
	}
	// Pre-Ф2 semantics restored exactly: quantity × sets = 2 × 4 = 8 instances, all of them at the
	// one size the marker cut.
	got := filled.PerLayerInstances("K_FRONT", 10)
	if !got.Known || got.Counts != (MarkerPieceCounts{AsDrawn: 8}) {
		t.Errorf("legacy instances = %+v, want 8 as-drawn and known", got)
	}

	t.Run("the summary must not override a состав the blob already carries", func(t *testing.T) {
		v4, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}),
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
			Placements:    placements(1, 2, 0),
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := v4.WithSummaryComposition(20, 9); err == nil {
			t.Error("expected a refusal: the measured состав wins over the summary's")
		}
	})

	t.Run("nonsense from the summary is refused", func(t *testing.T) {
		for _, c := range []struct{ sizeID, sets int }{{0, 4}, {-1, 4}, {10, 0}, {10, -2}} {
			if _, err := y.WithSummaryComposition(c.sizeID, c.sets); err == nil {
				t.Errorf("WithSummaryComposition(%d, %d) should have been refused", c.sizeID, c.sets)
			}
		}
	})
}

// ------------------------------------------------------ per-layer instances

// A piece with no size does not grade: ONE set of it is cut for every garment of the WHOLE состав
// (techcard.proto, TechCardMarkerPiece.quantity). Its instances therefore divide between the sizes
// exactly as the состав does — which only matters, and can only be tested, on a MIXED состав.
func TestPerLayerInstancesSizeAgnosticPieceInMixedComposition(t *testing.T) {
	// Состав: 2 garments of size 10 and 3 of size 20 = 5 garments. The pocket does not grade and is
	// cut once per garment ⇒ 5 instances, which split 2/3.
	blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{10, 2}, [2]int32{20, 3}),
		Pieces: []*pb_common.TechCardMarkerPiece{
			piece(1, "КАРМАН", "K_POCKET", 0, 1),
			piece(2, "ПОЛОЧКА", "K_FRONT", 10, 1),
			piece(3, "ПОЛОЧКА", "K_FRONT", 20, 1),
		},
		Placements: concat(placements(1, 5, 0), placements(2, 2, 0), placements(3, 3, 0)),
	})
	y, err := MarkerYieldFromBlob(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	small := y.PerLayerInstances("K_POCKET", 10)
	if !small.Known || small.Counts.AsDrawn != 2 {
		t.Errorf("pocket at size 10 = %+v, want 2 as-drawn", small)
	}
	large := y.PerLayerInstances("K_POCKET", 20)
	if !large.Known || large.Counts.AsDrawn != 3 {
		t.Errorf("pocket at size 20 = %+v, want 3 as-drawn", large)
	}
	if len(small.Caveats)+len(large.Caveats) != 0 {
		t.Errorf("an exact split must not raise a caveat: %v %v", small.Caveats, large.Caveats)
	}
	// The graded piece is read straight off its own size and is untouched by the split.
	if got := y.PerLayerInstances("K_FRONT", 10); !got.Known || got.Counts.AsDrawn != 2 {
		t.Errorf("front at size 10 = %+v, want 2 as-drawn", got)
	}

	t.Run("an unbalanced expansion floors and names the piece", func(t *testing.T) {
		// 4 instances of a piece cut once per garment against a состав of 3 garments: the blob does
		// not divide. Floor, and say so — the missing panel is a real one somebody has to account for.
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 1}, [2]int32{20, 2}),
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "КАРМАН", "K_POCKET", 0, 1)},
			Placements:    placements(1, 4, 0),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := y.PerLayerInstances("K_POCKET", 10)
		if !got.Known || got.Counts.AsDrawn != 1 { // 4 × 1 / 3 = 1.33 → 1
			t.Errorf("unbalanced split = %+v, want 1 as-drawn and known", got)
		}
		if len(got.Caveats) != 1 || !strings.Contains(got.Caveats[0], "КАРМАН") {
			t.Errorf("caveats = %v, want one naming КАРМАН", got.Caveats)
		}
	})

	t.Run("graded and size-agnostic contours of one piece are added", func(t *testing.T) {
		// A card piece may appear twice in one blob: graded contours for the sizes plus a block that
		// does not grade. Reading only one of them would understate the piece.
		blob := layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}, [2]int32{20, 2}),
			Pieces: []*pb_common.TechCardMarkerPiece{
				piece(1, "ОБТАЧКА", "K_FACING", 10, 1),
				piece(2, "ОБТАЧКА", "K_FACING", 0, 1),
			},
			Placements: concat(placements(1, 2, 0), placements(2, 4, 0)),
		})
		y, err := MarkerYieldFromBlob(blob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 2 graded at size 10 + 4 × 2/4 = 2 from the agnostic block = 4.
		if got := y.PerLayerInstances("K_FACING", 10); !got.Known || got.Counts.AsDrawn != 4 {
			t.Errorf("combined = %+v, want 4 as-drawn", got)
		}
	})
}

// Chirality is the INPUT of the question, not a variable of it: a mirrored pair is n left panels and
// n right ones, and the halves have to be counted apart to see when they are not equal.
func TestPerLayerInstancesKeepsMirrorPairsApart(t *testing.T) {
	build := func(asDrawn, mirrored int) MarkerYield {
		t.Helper()
		y, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}),
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 2)},
			Placements:    placements(1, asDrawn, mirrored),
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return y
	}

	t.Run("balanced pairs", func(t *testing.T) {
		got := build(2, 2).PerLayerInstances("K_FRONT", 10)
		if !got.Known || got.Counts != (MarkerPieceCounts{AsDrawn: 2, Mirrored: 2}) {
			t.Errorf("counts = %+v, want 2/2", got.Counts)
		}
		// Two pairs per layer, one pair per garment ⇒ two garments' worth.
		y := PieceYieldGarments(got.Counts, marked(entity.PieceCutSymmetryMirrored), 2, true)
		if !y.Known || y.Garments != 2 {
			t.Errorf("yield = %+v, want 2 garments", y)
		}
	})

	t.Run("unbalanced pairs yield by the SHORT hand, not by the total", func(t *testing.T) {
		// 4 as drawn and 0 mirrored is the half-set-of-patterns defect: four left fronts, no rights.
		// A reader that summed them would report two garments off a marker that makes none.
		got := build(4, 0).PerLayerInstances("K_FRONT", 10)
		if got.Counts != (MarkerPieceCounts{AsDrawn: 4}) {
			t.Fatalf("counts = %+v, want 4/0", got.Counts)
		}
		y := PieceYieldGarments(got.Counts, marked(entity.PieceCutSymmetryMirrored), 2, true)
		if !y.Known || y.Garments != 0 {
			t.Errorf("yield = %+v, want 0 garments — there is not a single right front", y)
		}

		// 3 and 1: one pair, not two.
		got = build(3, 1).PerLayerInstances("K_FRONT", 10)
		y = PieceYieldGarments(got.Counts, marked(entity.PieceCutSymmetryMirrored), 2, true)
		if !y.Known || y.Garments != 1 {
			t.Errorf("yield = %+v, want 1 garment", y)
		}
	})
}

// The three shapes of «нечего сказать», each of which must come back as UNKNOWN, and the two shapes
// of «сказать есть что, и это ноль», each of which must come back as a known zero. Confusing the two
// directions is the whole failure mode: an unknown read as zero invents a shortage, a zero read as
// unknown hides one.
func TestPerLayerInstancesSeparatesUnknownFromZero(t *testing.T) {
	v4 := func(t *testing.T) MarkerYield {
		t.Helper()
		y, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}),
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1)},
			Placements:    placements(1, 2, 0),
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return y
	}

	t.Run("unattributable blob answers nothing, even about a key it seems to hold", func(t *testing.T) {
		// One resolved piece and one unresolved one. The unresolved block may BE another contour of
		// K_FRONT, so the answer about K_FRONT would be a floor — and a floor read as a fact is a
		// shortage nobody can reproduce.
		y, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 2}),
			Pieces: []*pb_common.TechCardMarkerPiece{
				piece(1, "ПОЛОЧКА", "K_FRONT", 10, 1),
				piece(2, "БЕЗ ИМЕНИ", "", 10, 1),
			},
			Placements: concat(placements(1, 2, 0), placements(2, 2, 0)),
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := y.PerLayerInstances("K_FRONT", 10); got.Known {
			t.Errorf("got %+v, want unknown: one unattributed piece poisons every lookup", got)
		}
	})

	t.Run("no состав, no answer", func(t *testing.T) {
		y, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
			SchemaVersion: 3,
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 1)},
			Placements:    placements(1, 2, 0),
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := y.PerLayerInstances("K_FRONT", 10); got.Known {
			t.Errorf("got %+v, want unknown", got)
		}
	})

	t.Run("the empty key is a bucket, never an answer", func(t *testing.T) {
		if got := v4(t).PerLayerInstances("", 10); got.Known {
			t.Errorf("got %+v, want unknown", got)
		}
	})

	t.Run("a size this marker does not cut is an honest zero", func(t *testing.T) {
		got := v4(t).PerLayerInstances("K_FRONT", 99)
		if !got.Known || got.Counts != (MarkerPieceCounts{}) {
			t.Errorf("got %+v, want a KNOWN zero", got)
		}
	})

	t.Run("a piece this marker does not carry is an honest zero", func(t *testing.T) {
		got := v4(t).PerLayerInstances("K_LINING", 10)
		if !got.Known || got.Counts != (MarkerPieceCounts{}) {
			t.Errorf("got %+v, want a KNOWN zero", got)
		}
	})
}

// ------------------------------------------------------------- plies × mode

func TestLayerCutInstances(t *testing.T) {
	perLayer := MarkerPieceCounts{AsDrawn: 3, Mirrored: 1}

	t.Run("face_up multiplies each hand", func(t *testing.T) {
		got, err := LayerCutInstances(perLayer, LayFaceModeFaceUp, 20)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (MarkerPieceCounts{AsDrawn: 60, Mirrored: 20}) {
			t.Errorf("got %+v, want 60/20", got)
		}
	})

	t.Run("face_to_face halves the plies into each hand", func(t *testing.T) {
		// Alternating faces mean one placement yields half its instances as drawn and half mirrored:
		// 4 instances per layer × 20 plies = 80, split 40/40 whatever the per-layer chirality was.
		got, err := LayerCutInstances(perLayer, LayFaceModeFaceToFace, 20)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (MarkerPieceCounts{AsDrawn: 40, Mirrored: 40}) {
			t.Errorf("got %+v, want 40/40", got)
		}
	})

	t.Run("an odd ply count in face_to_face contributes NOTHING", func(t *testing.T) {
		got, err := LayerCutInstances(perLayer, LayFaceModeFaceToFace, 21)
		if !errors.Is(err, ErrLayModeParity) {
			t.Fatalf("error = %v, want ErrLayModeParity", err)
		}
		if got != (MarkerPieceCounts{}) {
			t.Errorf("got %+v, want the zero contribution: half a pair is not a pair", got)
		}
	})

	t.Run("one ply is legal face up and illegal face to face", func(t *testing.T) {
		if _, err := LayerCutInstances(perLayer, LayFaceModeFaceUp, 1); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if _, err := LayerCutInstances(perLayer, LayFaceModeFaceToFace, 1); !errors.Is(err, ErrLayModeParity) {
			t.Errorf("error = %v, want ErrLayModeParity", err)
		}
	})

	t.Run("nonsense inputs are refused, not rounded", func(t *testing.T) {
		for _, c := range []struct {
			mode  LayFaceMode
			plies int
		}{{"", 4}, {"Face_Up", 4}, {"face_down", 4}, {LayFaceModeFaceUp, 0}, {LayFaceModeFaceUp, -3}} {
			if _, err := LayerCutInstances(perLayer, c.mode, c.plies); err == nil {
				t.Errorf("LayerCutInstances(_, %q, %d) should have been refused", c.mode, c.plies)
			}
		}
		if _, err := LayerCutInstances(MarkerPieceCounts{AsDrawn: -1}, LayFaceModeFaceUp, 2); err == nil {
			t.Error("a negative instance count should have been refused")
		}
	})
}

// ------------------------------------------------------------ cut_symmetry

func TestPieceYieldGarments(t *testing.T) {
	cut := MarkerPieceCounts{AsDrawn: 22, Mirrored: 20}
	cases := []struct {
		name           string
		cut            MarkerPieceCounts
		sym            sql.NullString
		perGarment     int
		chiralityKnown bool
		want           PieceYield
	}{
		{
			name: "identical: the mirrors are overcut, not garments",
			cut:  cut, sym: marked(entity.PieceCutSymmetryIdentical), perGarment: 2, chiralityKnown: true,
			want: PieceYield{Garments: 11, Overcut: 20, Known: true},
		},
		{
			name: "mirrored: the short hand decides",
			cut:  cut, sym: marked(entity.PieceCutSymmetryMirrored), perGarment: 2, chiralityKnown: true,
			want: PieceYield{Garments: 20, Known: true},
		},
		{
			name: "mirrored with two pairs per garment",
			cut:  MarkerPieceCounts{AsDrawn: 9, Mirrored: 8}, sym: marked(entity.PieceCutSymmetryMirrored),
			perGarment: 4, chiralityKnown: true,
			want: PieceYield{Garments: 4, Known: true}, // min(9,8)=8, 8/2 pairs = 4
		},
		{
			name: "fold: chirality does not arise, the total divides",
			cut:  cut, sym: marked(entity.PieceCutSymmetryFold), perGarment: 2, chiralityKnown: true,
			want: PieceYield{Garments: 21, Known: true},
		},
		{
			name: "fold does not need chirality to be knowable",
			cut:  MarkerPieceCounts{AsDrawn: 6}, sym: marked(entity.PieceCutSymmetryFold), perGarment: 1,
			chiralityKnown: false,
			want:           PieceYield{Garments: 6, Known: true},
		},
		{
			name: "identical does not need chirality to be knowable",
			cut:  MarkerPieceCounts{AsDrawn: 6}, sym: marked(entity.PieceCutSymmetryIdentical), perGarment: 1,
			chiralityKnown: false,
			want:           PieceYield{Garments: 6, Known: true},
		},
		{
			// A legacy blob has no `flipped`, so a zero mirrored count is absence of evidence. Reading
			// it as a shortage would fire on EVERY marker taken before Ф1, at once.
			name: "mirrored below schema 3: UNKNOWN, not a shortage",
			cut:  MarkerPieceCounts{AsDrawn: 44}, sym: marked(entity.PieceCutSymmetryMirrored), perGarment: 2,
			chiralityKnown: false,
			want:           PieceYield{},
		},
		{
			// 0275 says it in words: NULL is «НЕ РАЗМЕЧЕНО», not «обычная».
			name: "not marked: UNKNOWN, never `identical`",
			cut:  cut, sym: unmarked, perGarment: 2, chiralityKnown: true,
			want: PieceYield{},
		},
		{
			name: "a value outside the dictionary is UNKNOWN",
			cut:  cut, sym: sql.NullString{String: "paired", Valid: true}, perGarment: 2, chiralityKnown: true,
			want: PieceYield{},
		},
		{
			name: "an odd count on a mirrored piece has no defined rounding",
			cut:  cut, sym: marked(entity.PieceCutSymmetryMirrored), perGarment: 3, chiralityKnown: true,
			want: PieceYield{},
		},
		{
			name: "zero panels per garment is not «unlimited garments»",
			cut:  cut, sym: marked(entity.PieceCutSymmetryIdentical), perGarment: 0, chiralityKnown: true,
			want: PieceYield{},
		},
		{
			name: "an honest zero cut is a KNOWN zero yield",
			cut:  MarkerPieceCounts{}, sym: marked(entity.PieceCutSymmetryIdentical), perGarment: 2,
			chiralityKnown: true,
			want:           PieceYield{Known: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PieceYieldGarments(c.cut, c.sym, c.perGarment, c.chiralityKnown)
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// ------------------------------------------------------------- the ladder

func TestCoverageCellLadder(t *testing.T) {
	ok := PieceYield{Garments: 30, Known: true}
	short := PieceYield{Garments: 12, Known: true}

	t.Run("everything known and sufficient is OK", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_FRONT", ok)
		c.Add("K_BACK", PieceYield{Garments: 20, Known: true})
		if c.Status() != CoverageStatusOK {
			t.Errorf("status = %s, want OK", c.Status())
		}
		if c.CoveredQty() != 20 {
			t.Errorf("covered = %d, want 20 (capped at planned)", c.CoveredQty())
		}
		if got := c.BlockingPieceKeys(); got != nil {
			t.Errorf("blocking = %v, want none on an OK cell", got)
		}
	})

	t.Run("a proven shortage is a BLOCKER and names the pieces that caused it", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_FRONT", ok)
		c.Add("K_LINING", short)
		c.Add("K_FACING", short)
		if c.Status() != CoverageStatusBlocker {
			t.Errorf("status = %s, want BLOCKER", c.Status())
		}
		if c.CoveredQty() != 12 {
			t.Errorf("covered = %d, want 12", c.CoveredQty())
		}
		got := c.BlockingPieceKeys()
		if len(got) != 2 || got[0] != "K_FACING" || got[1] != "K_LINING" {
			t.Errorf("blocking = %v, want [K_FACING K_LINING] sorted", got)
		}
	})

	t.Run("A PROVEN SHORTAGE BEATS AN UNKNOWN — the unknown does not soften it", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_LINING", short)
		c.Add("K_POCKET", PieceYield{}) // не размечено
		if c.Status() != CoverageStatusBlocker {
			t.Errorf("status = %s, want BLOCKER", c.Status())
		}
		if c.UnknownPieceCount() != 1 {
			t.Errorf("unknown count = %d, want 1 — it is still reported", c.UnknownPieceCount())
		}
	})

	t.Run("AN UNKNOWN BEATS OK — one silent piece greys out a green cell", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_FRONT", ok)
		c.Add("K_BACK", ok)
		c.Add("K_POCKET", PieceYield{}) // cut_symmetry IS NULL
		if c.Status() != CoverageStatusUnknown {
			t.Fatalf("status = %s, want UNKNOWN: coverage cannot be proven with a silent piece", c.Status())
		}
		if c.UnknownPieceCount() != 1 {
			t.Errorf("unknown count = %d, want 1", c.UnknownPieceCount())
		}
		// The number is still handed out — as a LOWER BOUND, which is what UnknownPieceCount beside it
		// is for. It must not be suppressed to zero either.
		if c.CoveredQty() != 20 {
			t.Errorf("covered = %d, want 20 as a «не меньше чем»", c.CoveredQty())
		}
	})

	t.Run("a cell whose every piece is silent is UNKNOWN, never OK and never a shortage", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_FRONT", PieceYield{})
		c.Add("K_BACK", PieceYield{})
		if c.Status() != CoverageStatusUnknown {
			t.Errorf("status = %s, want UNKNOWN", c.Status())
		}
		if c.CoveredQty() != 0 {
			t.Errorf("covered = %d, want 0 — nothing was proven", c.CoveredQty())
		}
		if got := c.BlockingPieceKeys(); got != nil {
			t.Errorf("blocking = %v, want none: nothing is proven to block", got)
		}
	})

	t.Run("an empty cell is UNKNOWN, not OK", func(t *testing.T) {
		if got := NewCoverageCell(20).Status(); got != CoverageStatusUnknown {
			t.Errorf("status = %s, want UNKNOWN — a cell nobody filled in has proved nothing", got)
		}
	})

	t.Run("no lay at all is an honest zero and a BLOCKER", func(t *testing.T) {
		// §6.3, first row: «у пары (C, слот) нет ни одного настила ⇒ 0, BLOCKER». It is not a special
		// case in the accumulator — every required piece is added with a zero cut count and yields
		// zero garments, and the ladder produces the blocker on its own.
		c := NewCoverageCell(20)
		for _, k := range []string{"K_FRONT", "K_BACK"} {
			c.Add(k, PieceYieldGarments(MarkerPieceCounts{}, marked(entity.PieceCutSymmetryIdentical), 2, true))
		}
		if c.Status() != CoverageStatusBlocker || c.CoveredQty() != 0 {
			t.Errorf("status = %s, covered = %d, want BLOCKER / 0", c.Status(), c.CoveredQty())
		}
	})

	t.Run("the zero PieceYield registers as unknown, not as zero garments", func(t *testing.T) {
		// The type's zero value is the safe direction ON PURPOSE: a caller who forgets to set Known
		// costs a grey badge, not an invented shortage.
		c := NewCoverageCell(20)
		c.Add("K_FRONT", PieceYield{Garments: 30, Known: true})
		var forgotten PieceYield
		c.Add("K_POCKET", forgotten)
		if c.Status() != CoverageStatusUnknown {
			t.Errorf("status = %s, want UNKNOWN", c.Status())
		}
	})
}

// The ladder (BLOCKER > UNKNOWN > OK) is NOT the constants' numeric order (UNKNOWN=0 < OK=1 <
// BLOCKER=2), and the two orders disagree on exactly the pair that matters. This test exists so that
// a later «simplification» to max(a, b) — which would answer OK for a cell that is UNKNOWN — fails
// here instead of on a run that cannot be cut.
func TestCoverageStatusLadderIsNotNumericOrder(t *testing.T) {
	if !(CoverageStatusUnknown < CoverageStatusOK && CoverageStatusOK < CoverageStatusBlocker) {
		t.Fatal("the constants must keep UNKNOWN as the zero value; the rest of this test reads that order")
	}
	if got := WorstCoverageStatus(CoverageStatusOK, CoverageStatusUnknown); got != CoverageStatusUnknown {
		t.Errorf("worst(OK, UNKNOWN) = %s, want UNKNOWN — numeric max would have said OK", got)
	}
	if got := WorstCoverageStatus(CoverageStatusUnknown, CoverageStatusBlocker); got != CoverageStatusBlocker {
		t.Errorf("worst(UNKNOWN, BLOCKER) = %s, want BLOCKER", got)
	}
	if got := WorstCoverageStatus(CoverageStatusOK, CoverageStatusOK); got != CoverageStatusOK {
		t.Errorf("worst(OK, OK) = %s, want OK", got)
	}
	if got := WorstCoverageStatus(); got != CoverageStatusUnknown {
		t.Errorf("worst() = %s, want UNKNOWN — an empty fold has proved nothing", got)
	}
	if got := WorstCoverageStatus(CoverageStatusOK, CoverageStatusBlocker, CoverageStatusUnknown); got != CoverageStatusBlocker {
		t.Errorf("worst(OK, BLOCKER, UNKNOWN) = %s, want BLOCKER", got)
	}
	// String() is what a log line and a test failure read; an unnamed value must be visible as one.
	if CoverageStatus(42).String() != "CoverageStatus(42)" {
		t.Errorf("String() of an unnamed status = %q", CoverageStatus(42).String())
	}
}

// ------------------------------------------------------------- end to end

// «22 полочки и 20 подкладок дают 20 изделий» must FALL OUT OF THE MINIMUM, not be programmed
// anywhere. The two pieces live on different cloths — the front on the main fabric, the lining on the
// lining — and cross-cloth arithmetic is not a special case: both are pieces of the same colourway
// and land in the same minimum.
func TestTwentyGarmentsFallOutOfTheMinimum(t *testing.T) {
	// Main-fabric marker: one mirrored pair of fronts per layer, 22 plies face up.
	main, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{10, 1}),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 10, 2)},
		Placements:    placements(1, 1, 1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Lining marker: one lining panel per layer, 20 plies face up.
	lining, err := MarkerYieldFromBlob(layoutBlob(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{10, 1}),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОДКЛАДКА", "K_LINING", 10, 1)},
		Placements:    placements(1, 1, 0),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	yieldOf := func(t *testing.T, y MarkerYield, key string, plies int, sym entity.TechCardPieceCutSymmetry, perGarment int) PieceYield {
		t.Helper()
		per := y.PerLayerInstances(key, 10)
		if !per.Known {
			t.Fatalf("%s: per-layer instances unknown", key)
		}
		cut, err := LayerCutInstances(per.Counts, LayFaceModeFaceUp, plies)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return PieceYieldGarments(cut, marked(sym), perGarment, y.ChiralityKnown())
	}

	front := yieldOf(t, main, "K_FRONT", 22, entity.PieceCutSymmetryMirrored, 2)
	if !front.Known || front.Garments != 22 {
		t.Fatalf("fronts = %+v, want 22 garments", front)
	}
	back := yieldOf(t, lining, "K_LINING", 20, entity.PieceCutSymmetryIdentical, 1)
	if !back.Known || back.Garments != 20 {
		t.Fatalf("linings = %+v, want 20 garments", back)
	}

	t.Run("planned 22: the lining proves the shortage", func(t *testing.T) {
		c := NewCoverageCell(22)
		c.Add("K_FRONT", front)
		c.Add("K_LINING", back)
		if c.Status() != CoverageStatusBlocker {
			t.Errorf("status = %s, want BLOCKER", c.Status())
		}
		if c.CoveredQty() != 20 {
			t.Errorf("covered = %d, want 20 — min(22, 20)", c.CoveredQty())
		}
		if got := c.BlockingPieceKeys(); len(got) != 1 || got[0] != "K_LINING" {
			t.Errorf("blocking = %v, want [K_LINING]", got)
		}
	})

	t.Run("planned 20: exactly covered", func(t *testing.T) {
		c := NewCoverageCell(20)
		c.Add("K_FRONT", front)
		c.Add("K_LINING", back)
		if c.Status() != CoverageStatusOK || c.CoveredQty() != 20 {
			t.Errorf("status = %s, covered = %d, want OK / 20", c.Status(), c.CoveredQty())
		}
	})

	t.Run("one unmarked piece on the same cell greys it out at every planned quantity", func(t *testing.T) {
		// The unknown survives the full chain — blob → plies → symmetry → cell — and does not collapse
		// into the green verdict the other two pieces would have produced on their own.
		silent := PieceYieldGarments(MarkerPieceCounts{AsDrawn: 40}, unmarked, 1, true)
		if silent.Known {
			t.Fatalf("an unmarked piece must not yield a number, got %+v", silent)
		}
		c := NewCoverageCell(20)
		c.Add("K_FRONT", front)
		c.Add("K_LINING", back)
		c.Add("K_POCKET", silent)
		if c.Status() != CoverageStatusUnknown {
			t.Errorf("status = %s, want UNKNOWN", c.Status())
		}
		// …and it still loses to a proven shortage on the same cell.
		c2 := NewCoverageCell(22)
		c2.Add("K_FRONT", front)
		c2.Add("K_LINING", back)
		c2.Add("K_POCKET", silent)
		if c2.Status() != CoverageStatusBlocker {
			t.Errorf("status = %s, want BLOCKER", c2.Status())
		}
	})
}

func concat[T any](ss ...[]T) []T {
	var out []T
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}
