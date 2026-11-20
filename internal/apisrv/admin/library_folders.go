package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Library folder structure (02-DESIGN §1, §2.4, §4). The edge lets every account in (allowlist,
// write class); the decision is here: an edit of the shared tree needs write in either section with
// its team feature on (libraryAccess.FolderWrite).

// libraryFolderError maps the store's folder sentinels to the §1 statuses. ok=false means the error is
// not one of them and the caller answers Internal.
func libraryFolderError(err error) (error, bool) {
	switch {
	case errors.Is(err, entity.ErrFolderNameInvalid):
		return status.Error(codes.InvalidArgument, entity.ErrFolderNameInvalid.Error()), true
	case errors.Is(err, entity.ErrFolderNotFound):
		return status.Error(codes.NotFound, "folder not found"), true
	case errors.Is(err, entity.ErrFolderNotEmpty):
		return status.Error(codes.FailedPrecondition, entity.ErrFolderNotEmpty.Error()), true
	case errors.Is(err, entity.ErrFolderDepth):
		return status.Error(codes.FailedPrecondition, entity.ErrFolderDepth.Error()), true
	case errors.Is(err, entity.ErrFolderCycle):
		return status.Error(codes.FailedPrecondition, entity.ErrFolderCycle.Error()), true
	case errors.Is(err, entity.ErrFolderNameTaken):
		return status.Error(codes.AlreadyExists, entity.ErrFolderNameTaken.Error()), true
	}
	return nil, false
}

func libraryFolderFail(ctx context.Context, op string, err error) error {
	if st, ok := libraryFolderError(err); ok {
		return st
	}
	slog.Default().ErrorContext(ctx, "can't "+op+" library folder", slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+op+" folder")
}

// callerAdminID is admins.id of the caller (the same lookup fileslibrary.ResolveViewer makes), 0 when
// there is no username (legacy token, in-process caller) or no such account. A lookup failure is
// logged and also gives 0: authorship is metadata, never a reason to refuse the action.
func (s *Server) callerAdminID(ctx context.Context) int {
	username := authsrv.GetAdminUsername(ctx)
	if username == "" || s.repo == nil {
		return 0
	}
	a, err := s.repo.Admin().GetAdminByUsername(ctx, username)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Default().WarnContext(ctx, "can't resolve caller admin id", slog.String("err", err.Error()))
		}
		return 0
	}
	return a.Id
}

func requireFolderWrite(ctx context.Context) error {
	if !libraryAccessOf(ctx).FolderWrite() {
		return status.Error(codes.PermissionDenied, entity.ErrLibraryNoSection.Error())
	}
	return nil
}

// CreateLibraryFolder makes a folder under parent_id (0 = root).
func (s *Server) CreateLibraryFolder(ctx context.Context, req *pb_admin.CreateLibraryFolderRequest) (*pb_admin.CreateLibraryFolderResponse, error) {
	if err := requireFolderWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetParentId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "parent_id must not be negative")
	}
	f, err := s.repo.Library().CreateFolder(ctx, int(req.GetParentId()), req.GetName(), s.callerAdminID(ctx))
	if err != nil {
		return nil, libraryFolderFail(ctx, "create", err)
	}
	return &pb_admin.CreateLibraryFolderResponse{Folder: libraryFolderToPb(*f)}, nil
}

// RenameLibraryFolder renames a folder; the name must stay unique among its siblings.
func (s *Server) RenameLibraryFolder(ctx context.Context, req *pb_admin.RenameLibraryFolderRequest) (*pb_admin.RenameLibraryFolderResponse, error) {
	if err := requireFolderWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "folder id is required")
	}
	f, err := s.repo.Library().RenameFolder(ctx, int(req.GetId()), req.GetName())
	if err != nil {
		return nil, libraryFolderFail(ctx, "rename", err)
	}
	return &pb_admin.RenameLibraryFolderResponse{Folder: libraryFolderToPb(*f)}, nil
}

// MoveLibraryFolder moves a folder with its subtree under new_parent_id (0 = root).
func (s *Server) MoveLibraryFolder(ctx context.Context, req *pb_admin.MoveLibraryFolderRequest) (*pb_admin.MoveLibraryFolderResponse, error) {
	if err := requireFolderWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "folder id is required")
	}
	if req.GetNewParentId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "new_parent_id must not be negative")
	}
	f, err := s.repo.Library().MoveFolder(ctx, int(req.GetId()), int(req.GetNewParentId()))
	if err != nil {
		return nil, libraryFolderFail(ctx, "move", err)
	}
	return &pb_admin.MoveLibraryFolderResponse{Folder: libraryFolderToPb(*f)}, nil
}

// DeleteLibraryFolder deletes an empty folder. The store's emptiness check is blind to ACL, so a
// folder holding a file the caller cannot see is folder_not_empty too, without saying what is inside.
func (s *Server) DeleteLibraryFolder(ctx context.Context, req *pb_admin.DeleteLibraryFolderRequest) (*pb_admin.DeleteLibraryFolderResponse, error) {
	if err := requireFolderWrite(ctx); err != nil {
		return nil, err
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "folder id is required")
	}
	if err := s.repo.Library().DeleteFolder(ctx, int(req.GetId())); err != nil {
		return nil, libraryFolderFail(ctx, "delete", err)
	}
	return &pb_admin.DeleteLibraryFolderResponse{}, nil
}
