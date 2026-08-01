package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSnapshotReleaseUsesReleaseNumberNotLegacyVersion(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 5).Return(&entity.TechCard{
		Id: 5,
		TechCardInsert: entity.TechCardInsert{
			Name:          "Release Coat",
			ApprovalState: entity.TechCardApprovalReleased,
			Version:       sql.NullString{String: "dead legacy version", Valid: true},
			ReleasedAt:    sql.NullTime{Time: now, Valid: true},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SaveTechCardRelease(mock.Anything, mock.AnythingOfType("entity.TechCardRelease")).
		Run(func(_ context.Context, rel entity.TechCardRelease) {
			require.False(t, rel.Version.Valid, "release_number, not the retired card.version, identifies the snapshot")
		}).Return(nil)

	(&Server{repo: repo}).snapshotReleaseIfReleased(context.Background(), 5)
}

// GetTechCardRelease parses the stored proto-JSON blob back into a contract TechCard and returns
// it alongside the metadata — the round-trip that keeps a frozen snapshot readable.
func TestGetTechCardReleaseParsesSnapshot(t *testing.T) {
	blob, err := protojson.Marshal(&pb_common.TechCard{Id: 5, TechCard: &pb_common.TechCardInsert{Name: "Release Coat"}})
	require.NoError(t, err)

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 9).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 9, TechCardId: 5, Version: sql.NullString{String: "1.0", Valid: true}},
		Snapshot:            string(blob),
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 9})
	require.NoError(t, err)
	require.Empty(t, resp.SnapshotError)
	require.NotNil(t, resp.Snapshot)
	require.Equal(t, int32(5), resp.Snapshot.Id)
	require.NotNil(t, resp.Snapshot.TechCard)
	require.Equal(t, "Release Coat", resp.Snapshot.TechCard.Name)
	require.Equal(t, "1.0", resp.Release.Version)
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
		{Id: 2, TechCardId: 5, Version: sql.NullString{String: "2.0", Valid: true}},
		{Id: 1, TechCardId: 5, Version: sql.NullString{String: "1.0", Valid: true}},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 5})
	require.NoError(t, err)
	require.Len(t, resp.Releases, 2)
	require.Equal(t, "2.0", resp.Releases[0].Version)
	require.Equal(t, "1.0", resp.Releases[1].Version)

	// zero id → InvalidArgument
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
