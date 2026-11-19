package admin

import (
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

// Review item 5: a /api/f link carries no team and resolves in team #1, so outside team #1 the
// RPCs that publish one refuse before writing anything, and reads show no url.
func TestFileLinksRefusedOutsideTeamOne(t *testing.T) {
	team2 := tenant.Team{ID: 2, Slug: "acme"}

	t.Run("set level link", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files).Maybe()
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil).Maybe()
		s := &Server{repo: repo}
		ctx := tenant.With(ctxAs("pasha", false, filesWrite), team2)
		_, err := s.SetLibraryFileAccess(ctx, &pb_admin.SetLibraryFileAccessRequest{FileId: 7, Level: "link", LinkTtl: 24})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, "public_links_team_one_only", status.Convert(err).Message())
		files.AssertNotCalled(t, "SetFileAccess", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("rotate", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files).Maybe()
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil).Maybe()
		s := &Server{repo: repo}
		ctx := tenant.With(ctxAs("pasha", false, filesWrite), team2)
		_, err := s.RotateLibraryFileLink(ctx, &pb_admin.RotateLibraryFileLinkRequest{FileId: 7})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		files.AssertNotCalled(t, "RotateFileLink", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("read shows no url outside team one, the url in team one", func(t *testing.T) {
		svc, err := fileaccess.New(nil, nil, nil, "test-pepper", "https://backend.example")
		require.NoError(t, err)
		t.Cleanup(svc.Stop)
		link := &entity.LibraryFilePublicAccess{FileId: 7, Epoch: 2, CreatedAt: time.Now()}
		for _, c := range []struct {
			team    tenant.Team
			wantURL bool
		}{{team2, false}, {tenant.Team{ID: tenant.TeamOneID, Slug: "grbpwr"}, true}} {
			files := mocks.NewMockFiles(t)
			files.EXPECT().GetFileAccess(mock.Anything, 7).Return(&entity.LibraryFileAccess{
				FileId: 7, Level: entity.LibraryFileAccessLink, Link: link,
			}, nil)
			files.EXPECT().ListFileAccessEvents(mock.Anything, 7, 0).Return(nil, nil)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Files().Return(files)
			s := &Server{repo: repo, fileLinks: svc}
			resp, err := s.GetLibraryFileAccess(tenant.With(ctxAs("pasha", false, filesWrite), c.team), &pb_admin.GetLibraryFileAccessRequest{Id: 7})
			require.NoError(t, err)
			require.Equal(t, c.wantURL, resp.GetAccess().GetLink().GetUrl() != "", "team %d", c.team.ID)
		}
	})

	t.Run("label files on a card get no url outside team one", func(t *testing.T) {
		svc, err := fileaccess.New(nil, nil, nil, "test-pepper", "https://backend.example")
		require.NoError(t, err)
		t.Cleanup(svc.Stop)
		s := &Server{fileLinks: svc}
		tc := &entity.TechCard{}
		tc.GarmentLabels = []entity.TechCardGarmentLabel{{Files: []entity.TechCardGarmentLabelFile{{LibraryFileId: 7, LinkEpoch: 1}}}}
		s.fillGarmentLabelFileLinks(tenant.With(t.Context(), team2), tc)
		require.Empty(t, tc.GarmentLabels[0].Files[0].PublicURL)
		s.fillGarmentLabelFileLinks(t.Context(), tc)
		require.NotEmpty(t, tc.GarmentLabels[0].Files[0].PublicURL)
	})
}
