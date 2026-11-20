package admin

import (
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fileaccess"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lane X1: every team publishes /api/f links. The team travels signed inside the token, so team 2
// gets its own v2 url and team #1 keeps exactly its old v1 url.
func TestFileLinksEveryTeam(t *testing.T) {
	team2 := tenant.Team{ID: 2, Slug: "acme"}
	teamOne := tenant.Team{ID: tenant.TeamOneID, Slug: "grbpwr"}

	t.Run("rotate is not refused outside team one", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files).Maybe()
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil).Maybe()
		files.EXPECT().RotateFileLink(mock.Anything, 7, mock.Anything).Return(nil, sql.ErrNoRows).Maybe()
		s := &Server{repo: repo}
		ctx := tenant.With(ctxAs("pasha", false, filesWrite), team2)
		_, err := s.RotateLibraryFileLink(ctx, &pb_admin.RotateLibraryFileLinkRequest{FileId: 7})
		require.NotEqual(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("read shows each team its own url", func(t *testing.T) {
		svc, err := fileaccess.New(nil, nil, nil, "test-pepper", "https://backend.example")
		require.NoError(t, err)
		t.Cleanup(svc.Stop)
		link := &entity.LibraryFilePublicAccess{FileId: 7, Epoch: 2, CreatedAt: time.Now()}
		urls := map[int64]string{}
		for _, team := range []tenant.Team{team2, teamOne} {
			files := mocks.NewMockFiles(t)
			files.EXPECT().GetFileAccess(mock.Anything, 7).Return(&entity.LibraryFileAccess{
				FileId: 7, Level: entity.LibraryFileAccessLink, Link: link,
			}, nil)
			files.EXPECT().ListFileAccessEvents(mock.Anything, 7, 0).Return(nil, nil)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Files().Return(files)
			s := &Server{repo: repo, fileLinks: svc}
			resp, err := s.GetLibraryFileAccess(tenant.With(ctxAs("pasha", false, filesWrite), team), &pb_admin.GetLibraryFileAccessRequest{Id: 7})
			require.NoError(t, err)
			urls[team.ID] = resp.GetAccess().GetLink().GetUrl()
			require.NotEmpty(t, urls[team.ID], "team %d", team.ID)
		}
		require.Equal(t, svc.LinkURL(7, 2), urls[tenant.TeamOneID], "team #1 keeps the v1 url")
		require.NotEqual(t, urls[tenant.TeamOneID], urls[2])
	})

	t.Run("label files on a card get a url in every team", func(t *testing.T) {
		svc, err := fileaccess.New(nil, nil, nil, "test-pepper", "https://backend.example")
		require.NoError(t, err)
		t.Cleanup(svc.Stop)
		s := &Server{fileLinks: svc}
		tc := &entity.TechCard{}
		tc.GarmentLabels = []entity.TechCardGarmentLabel{{Files: []entity.TechCardGarmentLabelFile{{LibraryFileId: 7, LinkEpoch: 1}}}}
		s.fillGarmentLabelFileLinks(tenant.With(t.Context(), team2), tc)
		require.Equal(t, svc.LinkURLFor(2, 7, 1), tc.GarmentLabels[0].Files[0].PublicURL)
		s.fillGarmentLabelFileLinks(t.Context(), tc)
		require.Equal(t, svc.LinkURL(7, 1), tc.GarmentLabels[0].Files[0].PublicURL)
	})
}
