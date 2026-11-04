package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetLibraryFileContentFollowsTheCard runs the real handlers over the real predicate: only the
// bucket is a mock. The bytes must open exactly where the card opens, so every case asks both methods
// and compares the codes. A case that declares no bucket read proves no bytes were fetched.
func TestGetLibraryFileContentFollowsTheCard(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	super := superCtx(ctx)
	_, uploader := insertAdminFixture(ctx, t, "test-content-uploader")
	ownerID, owner := insertAdminFixture(ctx, t, "test-content-owner")
	listedID, listed := insertAdminFixture(ctx, t, "test-content-listed")
	_, stranger := insertAdminFixture(ctx, t, "test-content-stranger")

	const sha = "0000000000000000000000000000000000000000000000000000000000c0f7e7"
	people := insertLibraryFileWithSha(ctx, t, "content-people.pdf", uploader, sha)
	team := insertLibraryFileWithSha(ctx, t, "content-team.pdf", uploader, sha)
	link := insertLibraryFileWithSha(ctx, t, "content-link.pdf", uploader, sha)
	deleted := insertLibraryFileWithSha(ctx, t, "content-deleted.pdf", uploader, sha)

	require.NoError(t, s.Files().SetFileOwners(super, people, []int{ownerID}, uploader))
	_, err = s.Files().SetFileAccess(super, people, entity.LibraryFileAccessUpdate{
		Level: entity.LibraryFileAccessPeople, AdminIDs: []int{listedID}, Actor: uploader,
	})
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE library_file SET access_level = 'link' WHERE id = ?`, link)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `DELETE FROM library_file WHERE id = ?`, deleted)
	require.NoError(t, err)

	cases := []struct {
		name string
		ctx  context.Context
		file int
		want codes.Code
	}{
		{"people, stranger", viewerCtx(ctx, stranger), people, codes.NotFound},
		{"people, nobody", ctx, people, codes.NotFound},
		{"people, on the list", viewerCtx(ctx, listed), people, codes.OK},
		{"people, uploader", viewerCtx(ctx, uploader), people, codes.OK},
		{"people, owner", viewerCtx(ctx, owner), people, codes.OK},
		{"people, super", super, people, codes.OK},
		{"team, stranger", viewerCtx(ctx, stranger), team, codes.OK},
		{"link, stranger", viewerCtx(ctx, stranger), link, codes.OK},
		{"deleted, super", super, deleted, codes.NotFound},
		{"unknown, super", super, deleted + 1000000, codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := mocks.NewMockFileStore(t)
			fs.EXPECT().PresignLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Return("https://bucket.test/signed", time.Now().Add(time.Hour), nil).Maybe()
			if tc.want == codes.OK {
				fs.EXPECT().GetLibraryObjectWithin(mock.Anything, mock.Anything, mock.Anything).Return([]byte("pdf"), nil).Once()
			}
			srv, err := admin.New(s, fs, nil, nil, nil, nil, nil, nil, nil, nil, entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
			require.NoError(t, err)

			resp, err := srv.GetLibraryFileContent(tc.ctx, &pb_admin.GetLibraryFileContentRequest{Id: int32(tc.file)})
			require.Equal(t, tc.want, status.Code(err))
			if tc.want == codes.OK {
				require.Equal(t, []byte("pdf"), resp.GetContent())
			}

			_, cardErr := srv.GetLibraryFile(tc.ctx, &pb_admin.GetLibraryFileRequest{Id: int32(tc.file)})
			require.Equal(t, status.Code(cardErr), status.Code(err), "the bytes and the card must agree")
		})
	}
}
