package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListTechCardSnapshotsReturnsLightPageAndCursor(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)
	created := time.Date(2026, 10, 23, 12, 34, 0, 0, time.UTC)
	cards.EXPECT().ListTechCardSnapshots(context.Background(), 7, int64(90), 3).Return([]entity.TechCardSnapshot{
		{Id: 89, LockVersion: 8, Reason: "autosave", CreatedBy: sql.NullString{String: "maker@example.com", Valid: true},
			ChangedPaths: []string{"note", "sizes"}, CreatedAt: created},
		{Id: 88, LockVersion: 7, Reason: "restore:42", CreatedBy: sql.NullString{String: "owner", Valid: true},
			ChangedPaths: []string{"restored 23.10 10:00"}, Pinned: true, CreatedAt: created.Add(-time.Minute)},
		{Id: 87},
	}, nil)

	response, err := (&Server{repo: repo}).ListTechCardSnapshots(context.Background(), &pb_admin.ListTechCardSnapshotsRequest{
		TechCardId: 7, PageSize: 2, BeforeSnapshotId: 90,
	})
	require.NoError(t, err)
	require.Len(t, response.Items, 2)
	require.Equal(t, int64(88), response.NextBeforeSnapshotId)
	require.Equal(t, "maker", response.Items[0].AuthorLabel)
	require.Equal(t, []string{"note", "sizes"}, response.Items[0].Changes)
	require.Nil(t, response.Items[0].ProtoReflect().Descriptor().Fields().ByName("snapshot"),
		"the light list item contract must not expose the snapshot body")
	require.True(t, response.Items[1].Pinned)
}

func TestListTechCardSnapshotsMapsMissingCardToNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().ListTechCardSnapshots(context.Background(), 7, int64(0), 101).Return(nil, sql.ErrNoRows)

	_, err := (&Server{repo: repo}).ListTechCardSnapshots(context.Background(), &pb_admin.ListTechCardSnapshotsRequest{TechCardId: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestUpdateTechCardRejectsInvalidSaveSessionIDBeforeReadingTheCard(t *testing.T) {
	_, err := (&Server{}).UpdateTechCard(context.Background(), &pb_admin.UpdateTechCardRequest{
		Id: 7, SaveSessionId: "not-a-uuid",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "save_session_id")
}
