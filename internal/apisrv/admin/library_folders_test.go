package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var libraryBothFeatures = []feature.Key{feature.Core, feature.Media, feature.Files}

// libraryCallerCtx is an account with the given section levels, both library features on, signed in
// as "kirill".
func libraryCallerCtx(kv ...string) context.Context {
	ctx := libraryCtx(&authsrv.AdminAuthz{Perms: perms(kv...)}, libraryBothFeatures...)
	return authsrv.PutAdminUsername(ctx, "kirill")
}

var (
	contentWriterCtx = func() context.Context {
		return libraryCallerCtx(rbac.SectionContent, string(entity.AccessWrite))
	}
	filesWriterCtx = func() context.Context {
		return libraryCallerCtx(rbac.SectionFiles, string(entity.AccessWrite))
	}
	readOnlyCtx = func() context.Context {
		return libraryCallerCtx(rbac.SectionContent, string(entity.AccessRead), rbac.SectionFiles, string(entity.AccessRead))
	}
)

// libraryRepo wires a strict repository whose Library() and Admin() may be asked for.
func libraryRepo(t *testing.T) (*mocks.MockRepository, *mocks.MockLibrary, *mocks.MockAdmin) {
	repo := mocks.NewMockRepository(t)
	lib := mocks.NewMockLibrary(t)
	adm := mocks.NewMockAdmin(t)
	repo.EXPECT().Library().Return(lib).Maybe()
	repo.EXPECT().Admin().Return(adm).Maybe()
	return repo, lib, adm
}

// A read-only account (or one without either section) never reaches the store: the strict mock has no
// expectations, so any call would fail the test.
func TestLibraryFolderMutationsNeedWrite(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	for name, ctx := range map[string]context.Context{
		"read only":    readOnlyCtx(),
		"no section":   libraryCallerCtx(rbac.SectionOrders, string(entity.AccessWrite)),
		"features off": authsrv.PutAdminUsername(libraryCtx(&authsrv.AdminAuthz{Super: true}, feature.Core), "kirill"),
	} {
		calls := map[string]func() error{
			"create": func() error {
				_, err := s.CreateLibraryFolder(ctx, &pb_admin.CreateLibraryFolderRequest{Name: "a"})
				return err
			},
			"rename": func() error {
				_, err := s.RenameLibraryFolder(ctx, &pb_admin.RenameLibraryFolderRequest{Id: 1, Name: "a"})
				return err
			},
			"move": func() error {
				_, err := s.MoveLibraryFolder(ctx, &pb_admin.MoveLibraryFolderRequest{Id: 1})
				return err
			},
			"delete": func() error {
				_, err := s.DeleteLibraryFolder(ctx, &pb_admin.DeleteLibraryFolderRequest{Id: 1})
				return err
			},
			"move items": func() error {
				_, err := s.MoveLibraryItems(ctx, &pb_admin.MoveLibraryItemsRequest{Refs: []*pb_admin.LibraryItemRef{
					{Source: pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA, Id: 1}}})
				return err
			},
		}
		for op, call := range calls {
			require.Equal(t, codes.PermissionDenied, status.Code(call()), "%s / %s", name, op)
		}
	}
}

// Either section's write is enough for the shared tree, and the folder is created by the caller.
func TestCreateLibraryFolderRecordsTheCaller(t *testing.T) {
	for name, ctx := range map[string]context.Context{"content writer": contentWriterCtx(), "files writer": filesWriterCtx()} {
		repo, lib, adm := libraryRepo(t)
		adm.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(&entity.Admin{Id: 7, Username: "kirill"}, nil)
		lib.EXPECT().CreateFolder(mock.Anything, 3, " Shoots ", 7).
			Return(&entity.LibraryFolder{Id: 11, ParentId: sql.NullInt64{Int64: 3, Valid: true}, Name: "Shoots"}, nil)
		s := &Server{repo: repo}

		resp, err := s.CreateLibraryFolder(ctx, &pb_admin.CreateLibraryFolderRequest{ParentId: 3, Name: " Shoots "})
		require.NoError(t, err, name)
		require.EqualValues(t, 11, resp.GetFolder().GetId())
		require.EqualValues(t, 3, resp.GetFolder().GetParentId())
		require.Equal(t, "Shoots", resp.GetFolder().GetName())
	}
}

// An unknown caller is not a reason to refuse: the folder is made with no author.
func TestCreateLibraryFolderUnknownCallerHasNoAuthor(t *testing.T) {
	repo, lib, adm := libraryRepo(t)
	adm.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(nil, sql.ErrNoRows)
	lib.EXPECT().CreateFolder(mock.Anything, 0, "a", 0).Return(&entity.LibraryFolder{Id: 1, Name: "a"}, nil)
	s := &Server{repo: repo}

	_, err := s.CreateLibraryFolder(contentWriterCtx(), &pb_admin.CreateLibraryFolderRequest{Name: "a"})
	require.NoError(t, err)
}

// The §1 table: every store sentinel answers its own code, the machine word in the message.
func TestLibraryFolderErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
		msg  string
	}{
		{entity.ErrFolderNameInvalid, codes.InvalidArgument, "folder_name_invalid"},
		{entity.ErrFolderNotFound, codes.NotFound, ""},
		{entity.ErrFolderNotEmpty, codes.FailedPrecondition, "folder_not_empty"},
		{entity.ErrFolderDepth, codes.FailedPrecondition, "folder_depth"},
		{entity.ErrFolderCycle, codes.FailedPrecondition, "folder_cycle"},
		{entity.ErrFolderNameTaken, codes.AlreadyExists, "folder_name_taken"},
		{errors.New("boom"), codes.Internal, ""},
	}
	for _, tc := range cases {
		wrapped := fmt.Errorf("store: %w", tc.err)
		check := func(op string, err error) {
			t.Helper()
			require.Equal(t, tc.code, status.Code(err), "%s: %v", op, tc.err)
			if tc.msg != "" {
				require.Equal(t, tc.msg, status.Convert(err).Message(), "%s: %v", op, tc.err)
			}
		}

		repo, lib, adm := libraryRepo(t)
		adm.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(&entity.Admin{Id: 7}, nil).Maybe()
		lib.EXPECT().CreateFolder(mock.Anything, 0, "x", 7).Return(nil, wrapped)
		lib.EXPECT().RenameFolder(mock.Anything, 5, "x").Return(nil, wrapped)
		lib.EXPECT().MoveFolder(mock.Anything, 5, 2).Return(nil, wrapped)
		lib.EXPECT().DeleteFolder(mock.Anything, 5).Return(wrapped)
		s := &Server{repo: repo}
		ctx := contentWriterCtx()

		_, err := s.CreateLibraryFolder(ctx, &pb_admin.CreateLibraryFolderRequest{Name: "x"})
		check("create", err)
		_, err = s.RenameLibraryFolder(ctx, &pb_admin.RenameLibraryFolderRequest{Id: 5, Name: "x"})
		check("rename", err)
		_, err = s.MoveLibraryFolder(ctx, &pb_admin.MoveLibraryFolderRequest{Id: 5, NewParentId: 2})
		check("move", err)
		_, err = s.DeleteLibraryFolder(ctx, &pb_admin.DeleteLibraryFolderRequest{Id: 5})
		check("delete", err)
	}
}

func TestLibraryFolderBadIdsNeverReachTheStore(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	ctx := contentWriterCtx()
	_, err := s.CreateLibraryFolder(ctx, &pb_admin.CreateLibraryFolderRequest{ParentId: -1, Name: "a"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.RenameLibraryFolder(ctx, &pb_admin.RenameLibraryFolderRequest{Name: "a"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.MoveLibraryFolder(ctx, &pb_admin.MoveLibraryFolderRequest{Id: 1, NewParentId: -2})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.MoveLibraryFolder(ctx, &pb_admin.MoveLibraryFolderRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.DeleteLibraryFolder(ctx, &pb_admin.DeleteLibraryFolderRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func mediaRef(id int32) *pb_admin.LibraryItemRef {
	return &pb_admin.LibraryItemRef{Source: pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA, Id: id}
}

func fileRef(id int32) *pb_admin.LibraryItemRef {
	return &pb_admin.LibraryItemRef{Source: pb_admin.LibrarySource_LIBRARY_SOURCE_FILE, Id: id}
}

func TestMoveLibraryItemsValidatesTheBatch(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	ctx := libraryCallerCtx(rbac.SectionContent, string(entity.AccessWrite), rbac.SectionFiles, string(entity.AccessWrite))

	over := make([]*pb_admin.LibraryItemRef, maxLibraryMoveRefs+1)
	for i := range over {
		over[i] = mediaRef(int32(i + 1))
	}
	for name, req := range map[string]*pb_admin.MoveLibraryItemsRequest{
		"no refs":          {},
		"over 500":         {Refs: over},
		"negative folder":  {Refs: []*pb_admin.LibraryItemRef{mediaRef(1)}, FolderId: -1},
		"no source":        {Refs: []*pb_admin.LibraryItemRef{{Id: 3}}},
		"no id":            {Refs: []*pb_admin.LibraryItemRef{mediaRef(0)}},
		"nil ref in batch": {Refs: []*pb_admin.LibraryItemRef{mediaRef(1), nil}},
	} {
		_, err := s.MoveLibraryItems(ctx, req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
	}
}

// A content-only account may move media but not a file, and one file in the batch refuses all of it
// before the store is asked (the strict mock has no MoveItems expectation).
func TestMoveLibraryItemsFileRefByContentOnlyAdminIsRefused(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.MoveLibraryItems(contentWriterCtx(), &pb_admin.MoveLibraryItemsRequest{
		Refs: []*pb_admin.LibraryItemRef{mediaRef(1), mediaRef(2), fileRef(9)}, FolderId: 4})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = s.MoveLibraryItems(filesWriterCtx(), &pb_admin.MoveLibraryItemsRequest{
		Refs: []*pb_admin.LibraryItemRef{fileRef(9), mediaRef(1)}})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestMoveLibraryItemsHandsTheWholeBatchToTheStore(t *testing.T) {
	repo, lib, _ := libraryRepo(t)
	want := []entity.LibraryRef{
		{Source: entity.LibrarySourceMedia, Id: 1},
		{Source: entity.LibrarySourceFile, Id: 9},
	}
	lib.EXPECT().MoveItems(mock.Anything, want, 4).Return(2, nil)
	s := &Server{repo: repo}
	ctx := libraryCallerCtx(rbac.SectionContent, string(entity.AccessWrite), rbac.SectionFiles, string(entity.AccessWrite))

	resp, err := s.MoveLibraryItems(ctx, &pb_admin.MoveLibraryItemsRequest{Refs: []*pb_admin.LibraryItemRef{mediaRef(1), fileRef(9)}, FolderId: 4})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.GetMoved())
}

// Folder 0 takes items out of every folder; it is passed through, not refused.
func TestMoveLibraryItemsFolderZeroTakesOut(t *testing.T) {
	repo, lib, _ := libraryRepo(t)
	lib.EXPECT().MoveItems(mock.Anything, []entity.LibraryRef{{Source: entity.LibrarySourceMedia, Id: 1}}, 0).Return(1, nil)
	s := &Server{repo: repo}
	resp, err := s.MoveLibraryItems(contentWriterCtx(), &pb_admin.MoveLibraryItemsRequest{Refs: []*pb_admin.LibraryItemRef{mediaRef(1)}})
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.GetMoved())
}

// A missing or hidden item, or a missing folder, is NotFound for the whole batch.
func TestMoveLibraryItemsStoreErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{fmt.Errorf("library item: %w", sql.ErrNoRows), codes.NotFound},
		{entity.ErrFolderNotFound, codes.NotFound},
		{errors.New("deadlock"), codes.Internal},
	} {
		repo, lib, _ := libraryRepo(t)
		lib.EXPECT().MoveItems(mock.Anything, mock.Anything, 4).Return(0, tc.err)
		s := &Server{repo: repo}
		_, err := s.MoveLibraryItems(contentWriterCtx(), &pb_admin.MoveLibraryItemsRequest{Refs: []*pb_admin.LibraryItemRef{mediaRef(1)}, FolderId: 4})
		require.Equal(t, tc.code, status.Code(err), "%v", tc.err)
	}
}
