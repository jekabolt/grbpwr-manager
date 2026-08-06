package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
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
	require.Equal(t, entity.MarkerLayoutFacts{SchemaVersion: 3, HasFlip: true}, got.LayoutFacts)
	// The flag must survive the re-marshal too: the blob is what a later read reconstructs from.
	require.Contains(t, got.Layout, `"flipped":true`)
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
	require.Equal(t, entity.MarkerLayoutFacts{SchemaVersion: 1, HasHalfTurn: true}, got.LayoutFacts)
}
