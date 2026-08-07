package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// markerRequest is a minimal but VALID save payload — everything the form checks demand — so each
// case below differs only in the blob under test.
func markerRequest(layout *pb_common.TechCardMarkerLayout) *pb_admin.SaveTechCardMarkerRequest {
	return &pb_admin.SaveTechCardMarkerRequest{
		TechCardId: 7,
		Marker: &pb_common.TechCardMarkerInsert{
			SizeId:        3,
			Name:          "M · основная",
			BomLineKey:    "01MRKFABRIC0000000000000K1",
			FabricWidthCm: &pb_decimal.Decimal{Value: "140"},
			Sets:          1,
			UsedLengthCm:  &pb_decimal.Decimal{Value: "120"},
			PlacedCount:   1,
			TotalCount:    1,
			Layout:        layout,
		},
	}
}

func markerLayout(version int32, placements ...*pb_common.TechCardMarkerPlacement) *pb_common.TechCardMarkerLayout {
	return &pb_common.TechCardMarkerLayout{
		SchemaVersion: version,
		Pieces: []*pb_common.TechCardMarkerPiece{{
			PieceId: 1, Name: "FP_L", Quantity: 1,
			Poly: []*pb_common.TechCardMarkerPoint{{}, {XCm: 10}, {XCm: 10, YCm: 10}},
		}},
		Placements: placements,
	}
}

// Ф1 bumps the blob format to 3 (`flipped` on a placement). The version gate has to move with it,
// and the facts the direction rule runs on have to reach the store — the store never opens the blob,
// so anything not distilled here is invisible to every check downstream.
func TestSaveTechCardMarkerAcceptsSchema3AndCarriesTheFacts(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)

	var got entity.TechCardMarkerInsert
	cards.EXPECT().SaveMarker(mock.Anything, 7, 0, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _, _ int, ins entity.TechCardMarkerInsert, _ string) { got = ins }).
		Return(42, nil)

	resp, err := (&Server{repo: repo}).SaveTechCardMarker(context.Background(), markerRequest(
		markerLayout(3,
			&pb_common.TechCardMarkerPlacement{PieceId: 1, RotDeg: 90},
			&pb_common.TechCardMarkerPlacement{PieceId: 1, RotDeg: 0, Flipped: true},
		)))
	require.NoError(t, err)
	require.Equal(t, int32(42), resp.GetId())
	require.Equal(t, entity.MarkerLayoutFacts{SchemaVersion: 3, FlipCount: 1}, got.LayoutFacts)
	// The flag must survive the re-marshal too: the blob is what a later read reconstructs from.
	require.Contains(t, got.Layout, `"flipped":true`)

	// The reader for the geometry ALREADY ON FILE has to travel with the payload: the store holds
	// those bytes and must not learn to parse them, and without this the exemption silently
	// withholds itself from every legacy marker (fail-closed, so not dangerous — but every rename of
	// a pre-Ф1 раскладка with a half-turn would start failing).
	require.NotNil(t, got.DistilStoredLayout, "the stored-layout distiller must be injected here")
	stored, err := got.DistilStoredLayout(`{"schemaVersion":1,"placements":[{"rotDeg":180},{"rotDeg":180}]}`)
	require.NoError(t, err)
	require.Equal(t, entity.MarkerLayoutFacts{SchemaVersion: 1, HalfTurnCount: 2}, stored,
		"the stored distiller must COUNT, or one stored half-turn licenses any number of new ones")
	// It must be the tolerant flavour. Wiring the JUDGING distiller here is the failure mode with no
	// other symptom: history may contain angles the payload validator now refuses, and a judging
	// reader would turn «this row is old» into «this row cannot be saved» — for the rows that need
	// the pass most. An out-of-set angle is the cheapest way to tell the two apart.
	legacyAngle, err := got.DistilStoredLayout(`{"schemaVersion":1,"placements":[{"rotDeg":37}]}`)
	require.NoError(t, err, "reading history must judge nothing")
	require.False(t, legacyAngle.HasHalfTurn())
}

// A version this server does not know is refused before anything is stored — a blob whose fields
// readers would silently drop is worse than a rejected save.
func TestSaveTechCardMarkerRefusesUnknownSchema(t *testing.T) {
	// No TechCards() expectation: reaching the store at all would fail the mock.
	_, err := (&Server{repo: mocks.NewMockRepository(t)}).SaveTechCardMarker(
		context.Background(), markerRequest(markerLayout(4,
			&pb_common.TechCardMarkerPlacement{PieceId: 1})))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "schema_version 4")
}

// `flipped` did not exist before schema 3, so a blob declaring an older version cannot legitimately
// carry one. Refused for EVERY marker, not only a linked one: the direction rule never sees an
// unlinked раскладка, but a blob lying about its own format is not a cloth question.
func TestSaveTechCardMarkerRefusesAMirrorUnderALegacySchema(t *testing.T) {
	for _, v := range []int32{1, 2} {
		req := markerRequest(markerLayout(v, &pb_common.TechCardMarkerPlacement{PieceId: 1, Flipped: true}))
		req.Marker.BomLineKey = "" // unlinked: the store-side rule would never be consulted
		_, err := (&Server{repo: mocks.NewMockRepository(t)}).SaveTechCardMarker(context.Background(), req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "schema_version %d", v)
		require.ErrorContains(t, err, "flipped")
	}
}

// rot_deg is policed nowhere else in the system — the proto's "0 | 90 | 180 | 270" is a comment and
// the blob has no CHECK behind it. An uncuttable angle must not reach storage, and an equivalent
// half-turn (-180, 540) must be canonicalised so the stored bytes agree with the facts the server
// judged them by.
func TestSaveTechCardMarkerPolicesRotation(t *testing.T) {
	t.Run("uncuttable angle", func(t *testing.T) {
		_, err := (&Server{repo: mocks.NewMockRepository(t)}).SaveTechCardMarker(context.Background(),
			markerRequest(markerLayout(3, &pb_common.TechCardMarkerPlacement{PieceId: 1, RotDeg: 37})))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.ErrorContains(t, err, "rot_deg")
	})

	t.Run("equivalent half-turn is counted and canonicalised", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)

		var got entity.TechCardMarkerInsert
		cards.EXPECT().SaveMarker(mock.Anything, 7, 0, mock.Anything, mock.Anything).
			Run(func(_ context.Context, _, _ int, ins entity.TechCardMarkerInsert, _ string) { got = ins }).
			Return(9, nil)

		_, err := (&Server{repo: repo}).SaveTechCardMarker(context.Background(),
			markerRequest(markerLayout(3, &pb_common.TechCardMarkerPlacement{PieceId: 1, RotDeg: -180})))
		require.NoError(t, err)
		require.True(t, got.LayoutFacts.HasHalfTurn(), "-180 is a half-turn")
		require.Contains(t, got.Layout, `"rotDeg":180`)
		require.NotContains(t, got.Layout, "-180")
	})
}

// An unset version is a v1 blob, and it must stay one all the way to the store: the version is what
// grandfathers legacy geometry out of the directional-cloth policy, so normalising it to anything
// else would judge old markers by a rule they predate.
func TestSaveTechCardMarkerNormalisesMissingSchemaToV1(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)

	var got entity.TechCardMarkerInsert
	cards.EXPECT().SaveMarker(mock.Anything, 7, 0, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _, _ int, ins entity.TechCardMarkerInsert, _ string) { got = ins }).
		Return(1, nil)

	_, err := (&Server{repo: repo}).SaveTechCardMarker(context.Background(), markerRequest(
		markerLayout(0, &pb_common.TechCardMarkerPlacement{PieceId: 1, RotDeg: 180})))
	require.NoError(t, err)
	require.Equal(t, entity.MarkerLayoutFacts{SchemaVersion: 1, HalfTurnCount: 1}, got.LayoutFacts)
}

// The whole safety argument for withholding the exemption on an unreadable stored blob rests on one
// property of the READ path: it must not hand the client the placements it failed to parse. If that
// ever changed — a well-meaning "salvage what parsed" — a client could round-trip geometry the server
// believes nobody could have loaded, and the argument would die silently with nothing failing.
//
// So it is pinned here, both halves together: the read degrades to summary-plus-warning with no
// geometry, AND the distiller calls the same blob unreadable. The two must agree on what unreadable
// means, or the reasoning connecting them is void.
func TestUnreadableStoredLayoutDegradesWithoutLeakingGeometry(t *testing.T) {
	const broken = `{"schemaVersion":"не число","placements":[{"rotDeg":180}]}`

	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)
	cards.EXPECT().GetMarker(mock.Anything, 5).Return(&entity.TechCardMarker{
		TechCardMarkerSummary: entity.TechCardMarkerSummary{Id: 5, Name: "нечитаемая"},
		Layout:                broken,
	}, nil)

	resp, err := (&Server{repo: repo}).GetTechCardMarker(context.Background(),
		&pb_admin.GetTechCardMarkerRequest{Id: 5})
	require.NoError(t, err, "an unreadable blob must not fail the read")

	layout := resp.GetMarker().GetLayout()
	require.NotEmpty(t, layout.GetWarnings(), "the operator has to be told the geometry is unreadable")
	require.Empty(t, layout.GetPlacements(), "no placement may escape a failed parse")
	require.Empty(t, layout.GetPieces(), "no piece may escape a failed parse")
	require.Equal(t, "нечитаемая", resp.GetMarker().GetSummary().GetName(), "the summary still serves")

	// …and the save path calls exactly this blob unreadable, which is why it withholds the pass.
	_, err = dto.MarkerLayoutFactsFromBlob(broken)
	require.Error(t, err, "the two paths must agree on what unreadable means")
}
