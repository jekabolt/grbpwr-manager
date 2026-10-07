package admin

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Guided create (0407): CreateTechCard's replay key and guided flag, and ExitTechCardGuide.

func guidedCreateReq(key string) *pb_admin.CreateTechCardRequest {
	return &pb_admin.CreateTechCardRequest{
		TechCard:        &pb_common.TechCardInsert{Name: "guided", Stage: pb_common.TechCardStage_TECH_CARD_STAGE_IDEA},
		ClientRequestId: key,
		Guided:          true,
	}
}

// The key reaches the store trimmed, with the guided flag beside it.
func TestCreateTechCardPassesTrimmedKeyAndGuidedToTheStore(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().AddTechCardWithOpts(mock.Anything, mock.Anything,
		entity.TechCardCreateOpts{RequestId: "k-1", Guided: true}).
		Return(7, false, nil)

	resp, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq("  k-1 \t"))
	require.NoError(t, err)
	require.EqualValues(t, 7, resp.Id)
}

func TestCreateTechCardRefusesAnOverlongKeyBeforeTheStore(t *testing.T) {
	repo := mocks.NewMockRepository(t) // strict: any store call fails the test
	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(),
		guidedCreateReq(strings.Repeat("k", entity.TechCardCreateRequestIdMaxRunes+1)))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "client_request_id")
}

// A1: a replay (created=false) runs NONE of the post-create work — the release snapshot is MAX+1 and
// would mint a second Rev per retry. Both hooks start with GetTechCardByIdConsistent, so the strict
// mock having no expectation for it is the assertion.
func TestCreateTechCardReplaySkipsPostCreateWork(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().AddTechCardWithOpts(mock.Anything, mock.Anything, mock.Anything).Return(42, false, nil)

	resp, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq("k-replay"))
	require.NoError(t, err)
	require.EqualValues(t, 42, resp.Id)
}

// The first call (created=true) does run them: cost seeding and the release snapshot each reload.
func TestCreateTechCardFirstCallRunsPostCreateWork(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().AddTechCardWithOpts(mock.Anything, mock.Anything, mock.Anything).Return(42, true, nil)
	techCards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 42).Return(nil, sql.ErrNoRows).Times(2)

	resp, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq("k-first"))
	require.NoError(t, err)
	require.EqualValues(t, 42, resp.Id)
}

// (1) A 1062 on the replay key is a lost race between two first calls under one key: success with the
// winner's card, no post-create work. It must NOT read as «style number taken».
func TestCreateTechCardRequestKeyRaceIsAReplay(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	dup := errors.New("can't add tech card: Error 1062 (23000): Duplicate entry 'k-race' for key 'tech_card." +
		entity.TechCardCreateRequestIdIndex + "'")
	techCards.EXPECT().AddTechCardWithOpts(mock.Anything, mock.Anything, mock.Anything).Return(0, false, dup)
	repo.EXPECT().IsErrUniqueViolation(dup).Return(true)
	techCards.EXPECT().TechCardIdByCreateRequestId(mock.Anything, "k-race").Return(99, nil)

	resp, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq("k-race"))
	require.NoError(t, err)
	require.EqualValues(t, 99, resp.Id)
}

// (2) Every other 1062 keeps its meaning: the style number is taken.
func TestCreateTechCardStyleNumberCollisionStaysTaken(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	dup := errors.New("can't add tech card: Error 1062 (23000): Duplicate entry 'SS26-001' for key 'tech_card.uq_tech_card_style_number'")
	techCards.EXPECT().AddTechCardWithOpts(mock.Anything, mock.Anything, mock.Anything).Return(0, false, dup)
	repo.EXPECT().IsErrUniqueViolation(dup).Return(true)

	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq("k-style"))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "style number is already used")
}

func TestExitTechCardGuide(t *testing.T) {
	t.Run("missing id", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		_, err := (&Server{repo: repo}).ExitTechCardGuide(fullAccessCtx(), &pb_admin.ExitTechCardGuideRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
	t.Run("absent card", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		techCards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(techCards)
		techCards.EXPECT().ExitTechCardGuide(mock.Anything, 5).Return(sql.ErrNoRows)
		_, err := (&Server{repo: repo}).ExitTechCardGuide(fullAccessCtx(), &pb_admin.ExitTechCardGuideRequest{TechCardId: 5})
		require.Equal(t, codes.NotFound, status.Code(err))
	})
	t.Run("ok", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		techCards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(techCards)
		techCards.EXPECT().ExitTechCardGuide(mock.Anything, 5).Return(nil)
		_, err := (&Server{repo: repo}).ExitTechCardGuide(fullAccessCtx(), &pb_admin.ExitTechCardGuideRequest{TechCardId: 5})
		require.NoError(t, err)
	})
}
