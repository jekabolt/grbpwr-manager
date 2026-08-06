package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testPatternURL = "https://cdn.example/base/tech-card-patterns/2026/august/pattern.pdf"

// DeleteObjects is VARIADIC (urls ...string), so its generated mock flattens the slice into
// Called(ctx, url1, url2, …). An expectation written as one []string argument therefore never
// matches — it compares a []string against the first url — and the assertion silently degrades
// into "0 of 1 expectations met" at teardown. Every expectation below lists the urls as
// separate arguments for that reason.

type patternCleanupContextKey struct{}

func TestDeleteTechCardRemovesCommittedPatternOrphans(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardAndListOrphanedPatternURLs(mock.Anything, 7).
		Return([]string{testPatternURL}, nil)
	files.On("DeleteObjects", mock.Anything, testPatternURL).Return(nil).Once()

	_, err := (&Server{repo: repo, bucket: files}).DeleteTechCard(
		context.Background(), &pb_admin.DeleteTechCardRequest{Id: 7})
	require.NoError(t, err)
}

func TestUpdateFittingPatternCleanupIsBestEffort(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	fittings := mocks.NewMockFittings(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().Fittings().Return(fittings)
	fittings.EXPECT().UpdateFittingAndListOrphanedPatternURLs(mock.Anything, 9, mock.Anything, 3).
		Return([]string{testPatternURL}, nil)
	files.On("DeleteObjects", mock.Anything, testPatternURL).
		Return(errors.New("object store unavailable")).Once()

	_, err := (&Server{repo: repo, bucket: files}).UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id:                  9,
		ExpectedLockVersion: 3,
		Fitting: &pb_common.FittingInsert{
			TechCardId:  7,
			FittingDate: timestamppb.New(time.Now()),
		},
	})
	require.NoError(t, err, "post-commit bucket cleanup must not fail the fitting RPC")
}

func TestDeleteFittingRemovesCommittedPatternOrphans(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	fittings := mocks.NewMockFittings(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().Fittings().Return(fittings)
	fittings.EXPECT().DeleteFittingAndListOrphanedPatternURLs(mock.Anything, 11).
		Return([]string{testPatternURL}, nil)
	files.On("DeleteObjects", mock.Anything, testPatternURL).Return(nil).Once()

	_, err := (&Server{repo: repo, bucket: files}).DeleteFitting(
		context.Background(), &pb_admin.DeleteFittingRequest{Id: 11})
	require.NoError(t, err)
}

func TestPatternCleanupOutlivesRequestCancellation(t *testing.T) {
	files := mocks.NewMockFileStore(t)
	ctx := context.WithValue(context.Background(), patternCleanupContextKey{}, "kept")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	files.On("DeleteObjects", mock.MatchedBy(func(cleanupCtx context.Context) bool {
		return cleanupCtx.Err() == nil && cleanupCtx.Value(patternCleanupContextKey{}) == "kept"
	}), testPatternURL).Return(nil).Once()

	(&Server{bucket: files}).deleteOrphanedPatternObjects(ctx, "tech_card", 7, []string{testPatternURL})
}
