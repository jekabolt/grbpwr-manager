package admin

import (
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func libraryShaFile(id int, sha string) entity.LibraryFile {
	f := entity.LibraryFile{Id: id, CreatedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	f.FileName, f.ContentType, f.SizeBytes, f.Sha256 = "p.jpg", "image/jpeg", 10, sha
	return f
}

// A file row of the unified list carries the id of the media made from it; a file without one, 0.
func TestLibraryItemsFileCarriesPublicMediaID(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Super: true}, libraryAllFeatures...)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().ListItems(mock.Anything, entity.LibraryBranches{Media: true, Files: true},
		entity.LibraryItemFilter{Access: entity.LibraryItemAccessPrivate, Sources: []entity.LibrarySource{entity.LibrarySourceFile}},
		mock.Anything, 0, 0).
		Return([]entity.LibraryItemRow{{Src: 2, Id: 1}, {Src: 2, Id: 2}}, 2, nil)
	lib.EXPECT().FileUsedIDs(mock.Anything, []int{1, 2}).Return(map[int]bool{}, nil)
	lib.EXPECT().MediaTwinIDs(mock.Anything, []string{"AB", ""}).Return(map[string]int{"ab": 77}, nil)
	files := mocks.NewMockFiles(t)
	files.EXPECT().ListFilesByIds(mock.Anything, []int{1, 2}).
		Return([]entity.LibraryFile{libraryShaFile(1, "AB"), libraryShaFile(2, "")}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(lib)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{Filter: &pb_admin.LibraryItemFilter{
		Access:  pb_admin.LibraryAccess_LIBRARY_ACCESS_PRIVATE,
		Sources: []pb_admin.LibrarySource{pb_admin.LibrarySource_LIBRARY_SOURCE_FILE},
	}})
	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 2)
	require.EqualValues(t, 77, resp.GetItems()[0].GetFile().GetPublicMediaId())
	require.EqualValues(t, 0, resp.GetItems()[1].GetFile().GetPublicMediaId())
}

// A caller without media read gets 0 and no twin lookup is made (mockery fails on an unexpected call).
func TestGetLibraryFileWithoutMediaReadHasNoPublicMediaID(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionFiles, string(entity.AccessRead))}, libraryAllFeatures...)
	f := libraryShaFile(5, "ab")
	files := mocks.NewMockFiles(t)
	files.EXPECT().GetFileById(mock.Anything, 5).Return(&f, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	resp, err := s.GetLibraryFile(ctx, &pb_admin.GetLibraryFileRequest{Id: 5})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetFile().GetPublicMediaId())
}

func TestGetLibraryFileCarriesPublicMediaID(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Super: true}, libraryAllFeatures...)
	f := libraryShaFile(5, "ab")
	files := mocks.NewMockFiles(t)
	files.EXPECT().GetFileById(mock.Anything, 5).Return(&f, nil)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().MediaTwinIDs(mock.Anything, []string{"ab"}).Return(map[string]int{"ab": 31}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	repo.EXPECT().Library().Return(lib)
	s := &Server{repo: repo}

	resp, err := s.GetLibraryFile(ctx, &pb_admin.GetLibraryFileRequest{Id: 5})
	require.NoError(t, err)
	require.EqualValues(t, 31, resp.GetFile().GetPublicMediaId())
}

func TestLibraryAccessConverters(t *testing.T) {
	for pb, want := range map[pb_admin.LibraryAccess]entity.LibraryItemAccess{
		pb_admin.LibraryAccess_LIBRARY_ACCESS_UNSPECIFIED: entity.LibraryItemAccessUnspecified,
		pb_admin.LibraryAccess_LIBRARY_ACCESS_PRIVATE:     entity.LibraryItemAccessPrivate,
		pb_admin.LibraryAccess_LIBRARY_ACCESS_PUBLIC:      entity.LibraryItemAccessPublic,
		pb_admin.LibraryAccess(42):                        entity.LibraryItemAccessUnspecified,
	} {
		require.Equal(t, want, libraryFilterFromPb(&pb_admin.LibraryItemFilter{Access: pb}).Access, pb.String())
	}
	out := libraryFacetsToPb(&entity.LibraryFacets{Total: 5, AccessPrivate: 2, AccessPublic: 3})
	require.EqualValues(t, 2, out.GetAccessPrivate())
	require.EqualValues(t, 3, out.GetAccessPublic())
}
