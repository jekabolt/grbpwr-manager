package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxLibraryMoveRefs bounds one MoveLibraryItems batch (two IN lists in one tx).
const maxLibraryMoveRefs = 500

// MoveLibraryItems puts items into folder_id (0 = out of every folder), all or nothing.
//
// Rights are per ref and checked before anything is written: a MEDIA ref needs content write with the
// Media feature on, a FILE ref files write with the Files feature on. One ref the caller may not move
// refuses the whole batch (PermissionDenied) and moves nothing. File visibility is checked by the
// store inside the move tx (fileslibrary's Viewer): a hidden or missing item, or a missing folder,
// is NotFound, again for the whole batch.
func (s *Server) MoveLibraryItems(ctx context.Context, req *pb_admin.MoveLibraryItemsRequest) (*pb_admin.MoveLibraryItemsResponse, error) {
	acc := libraryAccessOf(ctx)
	if !acc.FolderWrite() {
		return nil, status.Error(codes.PermissionDenied, entity.ErrLibraryNoSection.Error())
	}
	if len(req.GetRefs()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "refs are required")
	}
	if len(req.GetRefs()) > maxLibraryMoveRefs {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d items per move", maxLibraryMoveRefs)
	}
	if req.GetFolderId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "folder_id must not be negative")
	}
	refs := make([]entity.LibraryRef, 0, len(req.GetRefs()))
	for _, r := range req.GetRefs() {
		ref := libraryRefFromPb(r)
		if ref.Source == entity.LibrarySourceUnspecified || ref.Id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "every ref needs a source and an id")
		}
		refs = append(refs, ref)
	}
	for _, ref := range refs {
		if !acc.CanWrite(ref.Source) {
			return nil, status.Error(codes.PermissionDenied, "no write access to every item")
		}
	}
	n, err := s.repo.Library().MoveItems(ctx, refs, int(req.GetFolderId()))
	if err != nil {
		switch {
		case errors.Is(err, entity.ErrFolderNotFound):
			return nil, status.Error(codes.NotFound, "folder not found")
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Error(codes.NotFound, "item not found")
		}
		slog.Default().ErrorContext(ctx, "can't move library items", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't move items")
	}
	return &pb_admin.MoveLibraryItemsResponse{Moved: int32(n)}, nil
}
