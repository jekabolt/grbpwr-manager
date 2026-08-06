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

// expectAccessRowCleanup wires the access-row leg of the GC pass (0258/R9): pattern objects and
// their access rows are dropped together, so a test that only expects the bucket call now fails
// on an unexpected PatternObjects() rather than on anything it meant to assert.
func expectAccessRowCleanup(t *testing.T, repo *mocks.MockRepository) {
	t.Helper()
	objects := mocks.NewMockPatternObjects(t)
	repo.EXPECT().PatternObjects().Return(objects)
	objects.On("DeleteByKeys", mock.Anything, mock.Anything).Return(nil).Once()
}

func TestDeleteTechCardRemovesCommittedPatternOrphans(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardAndListOrphanedPatternURLs(mock.Anything, 7).
		Return([]string{testPatternURL}, nil)
	files.On("DeleteObjects", mock.Anything, testPatternURL).Return(nil).Once()
	expectAccessRowCleanup(t, repo)

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
	// NO access-row cleanup is wired, and that is the assertion: when the bucket delete fails the
	// object may still be there, so dropping its access row would reset the revocation epoch and
	// bring a still-reachable object back within tokens an operator had already revoked. The
	// mock repository fails the test if PatternObjects() is touched at all.

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
	expectAccessRowCleanup(t, repo)

	_, err := (&Server{repo: repo, bucket: files}).DeleteFitting(
		context.Background(), &pb_admin.DeleteFittingRequest{Id: 11})
	require.NoError(t, err)
}

func TestPatternCleanupOutlivesRequestCancellation(t *testing.T) {
	files := mocks.NewMockFileStore(t)
	ctx := context.WithValue(context.Background(), patternCleanupContextKey{}, "kept")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	// The DETACHED context is asserted on both writes: the object deletion and the access-row
	// cleanup that now rides with it (0258/R9). A cancelled request must not take either down.
	survives := mock.MatchedBy(func(cleanupCtx context.Context) bool {
		return cleanupCtx.Err() == nil && cleanupCtx.Value(patternCleanupContextKey{}) == "kept"
	})
	files.On("DeleteObjects", survives, testPatternURL).Return(nil).Once()
	repo := mocks.NewMockRepository(t)
	objects := mocks.NewMockPatternObjects(t)
	repo.EXPECT().PatternObjects().Return(objects)
	objects.On("DeleteByKeys", survives, mock.Anything).Return(nil).Once()

	(&Server{repo: repo, bucket: files}).deleteOrphanedPatternObjects(
		ctx, "tech_card", 7, []string{testPatternURL})
}
