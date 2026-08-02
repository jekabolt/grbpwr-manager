package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSnapshotReleaseBuildsStoreNumberedMetadata(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 5).Return(&entity.TechCard{
		Id: 5,
		TechCardInsert: entity.TechCardInsert{
			Name:          "Release Coat",
			ApprovalState: entity.TechCardApprovalReleased,
			ReleasedAt:    sql.NullTime{Time: now, Valid: true},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SaveTechCardRelease(mock.Anything, mock.AnythingOfType("entity.TechCardRelease")).
		Run(func(_ context.Context, rel entity.TechCardRelease) {
			require.Equal(t, 5, rel.TechCardId)
			require.Zero(t, rel.ReleaseNumber, "the store assigns release_number atomically")
		}).Return(nil)

	(&Server{repo: repo}).snapshotReleaseIfReleased(context.Background(), 5)
}

// GetTechCardRelease ignores retired fields in old proto-JSON snapshots so removing a dead wire field
// does not make an otherwise-readable frozen release degrade to snapshot_error.
func TestGetTechCardReleaseParsesSnapshotWithRetiredFields(t *testing.T) {
	blob := `{"id":5,"techCard":{"name":"Release Coat"},"revisions":[{"version":"1.0","revisionDate":"2025-01-02T00:00:00Z","author":"alice"}]}`

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 9).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 9, TechCardId: 5, ReleaseNumber: 1},
		Snapshot:            blob,
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 9})
	require.NoError(t, err)
	require.Empty(t, resp.SnapshotError)
	require.NotNil(t, resp.Snapshot)
	require.Equal(t, int32(5), resp.Snapshot.Id)
	require.NotNil(t, resp.Snapshot.TechCard)
	require.Equal(t, "Release Coat", resp.Snapshot.TechCard.Name)
	require.Equal(t, int32(1), resp.Release.ReleaseNumber)
}

// A stored blob that no longer parses must degrade to metadata + snapshot_error, never a 500
// (hero-v2 rule) — old releases stay listable/openable as the contract evolves.
func TestGetTechCardReleaseDegradesOnBadBlob(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 3).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 3, TechCardId: 5},
		Snapshot:            `{"this":"is valid json but not a TechCard","id":"not-an-int"}`,
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 3})
	require.NoError(t, err, "a bad blob must not fail the call")
	require.Nil(t, resp.Snapshot)
	require.NotEmpty(t, resp.SnapshotError)
	require.Equal(t, int32(3), resp.Release.Id)
}

func TestGetTechCardReleaseNotFoundAndValidation(t *testing.T) {
	// missing id → NotFound
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 404).Return(nil, sql.ErrNoRows)
	s := &Server{repo: repo}
	_, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 404})
	require.Equal(t, codes.NotFound, status.Code(err))

	// zero id → InvalidArgument (no store call)
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ListTechCardReleases converts each stored metadata row to pb (newest-first order preserved).
func TestListTechCardReleases(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().ListTechCardReleases(mock.Anything, 5).Return([]entity.TechCardReleaseMeta{
		{Id: 2, TechCardId: 5, ReleaseNumber: 2},
		{Id: 1, TechCardId: 5, ReleaseNumber: 1},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 5})
	require.NoError(t, err)
	require.Len(t, resp.Releases, 2)
	require.Equal(t, int32(2), resp.Releases[0].ReleaseNumber)
	require.Equal(t, int32(1), resp.Releases[1].ReleaseNumber)

	// zero id → InvalidArgument
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
