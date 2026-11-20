package admin

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var libraryAllFeatures = []feature.Key{feature.Core, feature.Media, feature.Files}

func libraryMediaFixture(id int) entity.MediaFull {
	m := entity.MediaFull{Id: id, CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	m.FullSizeMediaURL = "https://cdn.example/clip.mp4"
	m.ThumbnailMediaURL = "https://cdn.example/clip-thumb.mp4"
	m.ThumbnailWidth, m.ThumbnailHeight = 1080, 1350
	m.Kind = entity.LibraryKindValueVideo
	m.FileName = sql.NullString{String: "clip.mp4", Valid: true}
	m.SizeBytes = sql.NullInt64{Int64: 1234, Valid: true}
	m.FolderId = sql.NullInt64{Int64: 3, Valid: true}
	m.UploadedById = sql.NullInt64{Int64: 7, Valid: true}
	return m
}

func TestLibraryItemsContentOnlySeesMediaOnly(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionContent, string(entity.AccessRead))}, libraryAllFeatures...)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().ListItems(mock.Anything, entity.LibraryBranches{Media: true}, mock.Anything, entity.LibrarySortNew, 60, 0).
		Return([]entity.LibraryItemRow{{Src: 1, Id: 5}, {Src: 1, Id: 6}}, 2, nil)
	media := mocks.NewMockMedia(t)
	// Media 6 vanished between the list and the load: it is skipped, not an error.
	media.EXPECT().GetMediaByIds(mock.Anything, []int{5, 6}).Return(map[int]entity.MediaFull{5: libraryMediaFixture(5)}, nil)
	media.EXPECT().GetMediaUsage(mock.Anything, []int{5, 6}).
		Return(map[int][]entity.MediaUsageRef{5: {{Kind: "product", EntityId: 1}}}, nil)
	admin := mocks.NewMockAdmin(t)
	admin.EXPECT().ListAdminRefs(mock.Anything).Return([]entity.AdminRef{{Id: 7, Username: "pasha"}}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(lib)
	repo.EXPECT().Media().Return(media)
	repo.EXPECT().Admin().Return(admin)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{Sort: pb_admin.LibrarySort_LIBRARY_SORT_NEW, Limit: 60})
	require.NoError(t, err)
	require.Equal(t, []pb_admin.LibrarySource{pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA}, resp.GetSourcesVisible())
	require.EqualValues(t, 2, resp.GetTotal())
	require.Len(t, resp.GetItems(), 1)
	it := resp.GetItems()[0]
	require.Equal(t, pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA, it.GetRef().GetSource())
	require.EqualValues(t, 5, it.GetRef().GetId())
	require.Equal(t, pb_admin.LibraryKind_LIBRARY_KIND_VIDEO, it.GetKind())
	require.Equal(t, "clip.mp4", it.GetName())
	require.Equal(t, "video/mp4", it.GetContentType())
	require.EqualValues(t, 1234, it.GetSizeBytes())
	require.EqualValues(t, 3, it.GetFolderId())
	require.EqualValues(t, 7, it.GetUploadedById())
	require.Equal(t, "pasha", it.GetUploadedBy())
	require.True(t, it.GetInUse())
	require.EqualValues(t, 1080, it.GetWidth())
	require.EqualValues(t, 1350, it.GetHeight())
	require.NotNil(t, it.GetMedia())
	require.EqualValues(t, 5, it.GetMedia().GetId())
	require.Nil(t, it.GetFile())
}

func TestLibraryItemsFilesOnlySeesFilesOnly(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionFiles, string(entity.AccessRead))}, libraryAllFeatures...)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().ListItems(mock.Anything, entity.LibraryBranches{Files: true}, mock.Anything, entity.LibrarySortUnspecified, 0, 0).
		Return([]entity.LibraryItemRow{{Src: 2, Id: 9}}, 1, nil)
	lib.EXPECT().FileUsedIDs(mock.Anything, []int{9}).Return(map[int]bool{9: true}, nil)
	files := mocks.NewMockFiles(t)
	f := entity.LibraryFile{Id: 9, CreatedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		UploadedById: sql.NullInt64{Int64: 4, Valid: true}}
	f.FileName, f.ContentType, f.SizeBytes, f.UploadedBy = "doc.pdf", "application/pdf", 99, "kirill"
	files.EXPECT().ListFilesByIds(mock.Anything, []int{9}).Return([]entity.LibraryFile{f}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(lib)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{})
	require.NoError(t, err)
	require.Equal(t, []pb_admin.LibrarySource{pb_admin.LibrarySource_LIBRARY_SOURCE_FILE}, resp.GetSourcesVisible())
	require.Len(t, resp.GetItems(), 1)
	it := resp.GetItems()[0]
	require.Equal(t, pb_admin.LibrarySource_LIBRARY_SOURCE_FILE, it.GetRef().GetSource())
	require.Equal(t, pb_admin.LibraryKind_LIBRARY_KIND_DOCUMENT, it.GetKind())
	require.Equal(t, "doc.pdf", it.GetName())
	require.Equal(t, "application/pdf", it.GetContentType())
	require.Equal(t, "kirill", it.GetUploadedBy())
	require.EqualValues(t, 4, it.GetUploadedById())
	require.True(t, it.GetInUse())
	require.NotNil(t, it.GetFile())
	require.EqualValues(t, 9, it.GetFile().GetId())
	require.Nil(t, it.GetMedia())
}

func TestLibraryItemsMediaFeatureOffDropsMedia(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Super: true}, feature.Core, feature.Files)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().ListItems(mock.Anything, entity.LibraryBranches{Files: true}, mock.Anything, mock.Anything, 0, 0).
		Return([]entity.LibraryItemRow{}, 0, nil)
	lib.EXPECT().Facets(mock.Anything, entity.LibraryBranches{Files: true}, mock.Anything).
		Return(&entity.LibraryFacets{Total: 0}, nil)
	lib.EXPECT().ListFolders(mock.Anything, entity.LibraryBranches{Files: true}, mock.Anything).
		Return([]entity.LibraryFolder{{Id: 1, Name: "a", ChildCount: 1}, {Id: 2, ParentId: sql.NullInt64{Int64: 1, Valid: true}, Name: "b", ItemCount: 2}}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(lib)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{})
	require.NoError(t, err)
	require.Equal(t, []pb_admin.LibrarySource{pb_admin.LibrarySource_LIBRARY_SOURCE_FILE}, resp.GetSourcesVisible())
	require.Empty(t, resp.GetItems())

	_, err = s.GetLibraryFacets(ctx, &pb_admin.GetLibraryFacetsRequest{})
	require.NoError(t, err)

	folders, err := s.ListLibraryFolders(ctx, &pb_admin.ListLibraryFoldersRequest{})
	require.NoError(t, err)
	require.Len(t, folders.GetFolders(), 2)
	require.EqualValues(t, 1, folders.GetFolders()[1].GetParentId())
	require.EqualValues(t, 2, folders.GetFolders()[1].GetItemCount())
	require.EqualValues(t, 1, folders.GetFolders()[0].GetChildCount())
}

func TestLibraryReadsNeitherSectionIsPermissionDenied(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionOrders, string(entity.AccessWrite))}, libraryAllFeatures...)
	// No expectations on the repository: mockery fails the test on any call, which proves the refusal
	// happens before the store.
	s := &Server{repo: mocks.NewMockRepository(t)}
	calls := map[string]func() error{
		"ListLibraryItems": func() error { _, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{}); return err },
		"GetLibraryFacets": func() error { _, err := s.GetLibraryFacets(ctx, &pb_admin.GetLibraryFacetsRequest{}); return err },
		"ListLibraryFolders": func() error {
			_, err := s.ListLibraryFolders(ctx, &pb_admin.ListLibraryFoldersRequest{})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		require.Equal(t, codes.PermissionDenied, status.Code(err), name)
		require.Contains(t, status.Convert(err).Message(), "library_no_section", name)
	}
	// Files section with the Files feature off is no section at all either.
	ctx = libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionFiles, string(entity.AccessWrite))}, feature.Core, feature.Media)
	_, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestLibraryReadsInvalidFilterIsInvalidArgument(t *testing.T) {
	ctx := libraryCtx(&authsrv.AdminAuthz{Super: true})
	bad := fmt.Errorf("%w: no_folder together with folder_id", entity.ErrLibraryItemsFilterInvalid)
	lib := mocks.NewMockLibrary(t)
	lib.EXPECT().ListItems(mock.Anything, entity.LibraryBranches{Media: true, Files: true}, mock.Anything, mock.Anything, 201, 0).
		Return(nil, 0, bad)
	lib.EXPECT().Facets(mock.Anything, mock.Anything, mock.Anything).Return(nil, bad)
	lib.EXPECT().ListFolders(mock.Anything, mock.Anything, mock.Anything).Return(nil, bad)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(lib)
	s := &Server{repo: repo}

	_, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{Limit: 201})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "library_filter_invalid")
	_, err = s.GetLibraryFacets(ctx, &pb_admin.GetLibraryFacetsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.ListLibraryFolders(ctx, &pb_admin.ListLibraryFoldersRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestLibraryMediaContentType(t *testing.T) {
	for _, tc := range []struct {
		full, thumb string
		want        string
	}{
		{"https://x/a.webp", "https://x/a-t.webp", "image/webp"},
		{"https://x/a.mp4", "https://x/a-t.WEBM", "video/webm"},
		{"https://x/a.svg", "https://x/a-t.png", "image/svg+xml"},
		{"https://x/a.glb", "https://x/a-t.png", "model/gltf-binary"},
		{"https://x/a", "https://x/a-t", "image/webp"},
	} {
		m := &entity.MediaFull{}
		m.FullSizeMediaURL, m.ThumbnailMediaURL = tc.full, tc.thumb
		kind := entity.MediaKindOf(tc.full, tc.thumb)
		require.Equal(t, tc.want, libraryMediaContentType(kind, m), tc.full)
	}
}
