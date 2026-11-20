package admin

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The unified library reads (02-DESIGN §3): one list over media and library files, its counts and the
// folder rail. The edge lets every account in (allowlist read); the branches are gated here per
// section and team feature, and the store narrows them further by the filter.

// libraryFileFolders is the store's folder read for the page's files. entity.LibraryFile carries no
// folder_id and dependency.Library has no such method yet (lane B2 may not edit either), so the real
// store answers it through this optional interface; a repository without it leaves file folders 0.
type libraryFileFolders interface {
	FileFolderIDs(ctx context.Context, ids []int) (map[int]int, error)
}

// libraryReadError maps a library store error to a status: a meaningless filter is InvalidArgument
// library_filter_invalid, anything else Internal.
func libraryReadError(ctx context.Context, what string, err error) error {
	if errors.Is(err, entity.ErrLibraryItemsFilterInvalid) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	slog.Default().ErrorContext(ctx, "can't "+what, slog.String("err", err.Error()))
	return status.Errorf(codes.Internal, "failed to %s", what)
}

// libraryReadAccess resolves the caller's branches; neither readable is PermissionDenied.
func libraryReadAccess(ctx context.Context) (libraryAccess, error) {
	acc := libraryAccessOf(ctx)
	if !acc.AnyRead() {
		return acc, status.Error(codes.PermissionDenied, entity.ErrLibraryNoSection.Error())
	}
	return acc, nil
}

// librarySourcesVisible is what the caller may read at all, before the filter narrows it.
func librarySourcesVisible(acc libraryAccess) []pb_admin.LibrarySource {
	var out []pb_admin.LibrarySource
	if acc.MediaRead {
		out = append(out, pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA)
	}
	if acc.FilesRead {
		out = append(out, pb_admin.LibrarySource_LIBRARY_SOURCE_FILE)
	}
	return out
}

// ListLibraryItems answers one page of media and files as one list, with the total under the filter.
func (s *Server) ListLibraryItems(ctx context.Context, req *pb_admin.ListLibraryItemsRequest) (*pb_admin.ListLibraryItemsResponse, error) {
	acc, err := libraryReadAccess(ctx)
	if err != nil {
		return nil, err
	}
	rows, total, err := s.repo.Library().ListItems(ctx, acc.Branches(), libraryFilterFromPb(req.GetFilter()),
		librarySortFromPb(req.GetSort()), int(req.GetLimit()), int(req.GetOffset()))
	if err != nil {
		return nil, libraryReadError(ctx, "list library items", err)
	}
	items, err := s.hydrateLibraryItems(ctx, rows)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load library items", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "failed to load library items")
	}
	return &pb_admin.ListLibraryItemsResponse{
		Items:          items,
		Total:          int32(total),
		SourcesVisible: librarySourcesVisible(acc),
	}, nil
}

// GetLibraryFacets answers the month, kind, usage and ratio counts under the filter.
func (s *Server) GetLibraryFacets(ctx context.Context, req *pb_admin.GetLibraryFacetsRequest) (*pb_admin.GetLibraryFacetsResponse, error) {
	acc, err := libraryReadAccess(ctx)
	if err != nil {
		return nil, err
	}
	facets, err := s.repo.Library().Facets(ctx, acc.Branches(), libraryFilterFromPb(req.GetFilter()))
	if err != nil {
		return nil, libraryReadError(ctx, "count library facets", err)
	}
	return libraryFacetsToPb(facets), nil
}

// ListLibraryFolders answers the whole folder tree, flat, with direct item counts under the filter.
func (s *Server) ListLibraryFolders(ctx context.Context, req *pb_admin.ListLibraryFoldersRequest) (*pb_admin.ListLibraryFoldersResponse, error) {
	acc, err := libraryReadAccess(ctx)
	if err != nil {
		return nil, err
	}
	folders, err := s.repo.Library().ListFolders(ctx, acc.Branches(), libraryFilterFromPb(req.GetFilter()))
	if err != nil {
		return nil, libraryReadError(ctx, "list library folders", err)
	}
	out := make([]*pb_admin.LibraryFolder, 0, len(folders))
	for _, f := range folders {
		out = append(out, libraryFolderToPb(f))
	}
	return &pb_admin.ListLibraryFoldersResponse{Folders: out}, nil
}

// hydrateLibraryItems loads the page's media rows and files by id and reassembles them in page
// order. A row that vanished between the list and the load (deleted, or a file whose visibility
// changed) is skipped. in_use and uploaded_by are filled for this page only.
func (s *Server) hydrateLibraryItems(ctx context.Context, rows []entity.LibraryItemRow) ([]*pb_admin.LibraryItem, error) {
	var mediaIDs, fileIDs []int
	for _, r := range rows {
		switch entity.LibrarySource(r.Src) {
		case entity.LibrarySourceMedia:
			mediaIDs = append(mediaIDs, r.Id)
		case entity.LibrarySourceFile:
			fileIDs = append(fileIDs, r.Id)
		}
	}

	media := map[int]entity.MediaFull{}
	mediaUsage := map[int][]entity.MediaUsageRef{}
	usernames := map[int]string{}
	if len(mediaIDs) > 0 {
		var err error
		if media, err = s.repo.Media().GetMediaByIds(ctx, mediaIDs); err != nil {
			return nil, err
		}
		if mediaUsage, err = s.repo.Media().GetMediaUsage(ctx, mediaIDs); err != nil {
			return nil, err
		}
		needNames := false
		for _, m := range media {
			needNames = needNames || m.UploadedById.Valid
		}
		if needNames {
			refs, err := s.repo.Admin().ListAdminRefs(ctx)
			if err != nil {
				return nil, err
			}
			for _, a := range refs {
				usernames[a.Id] = a.Username
			}
		}
	}

	files := map[int]entity.LibraryFile{}
	filesPb := map[int]*pb_admin.LibraryFile{}
	fileUsed := map[int]bool{}
	fileFolders := map[int]int{}
	if len(fileIDs) > 0 {
		list, err := s.repo.Files().ListFilesByIds(ctx, fileIDs)
		if err != nil {
			return nil, err
		}
		pbs := s.libraryFilesToPb(ctx, list)
		if err := s.fillPublicMediaIDs(ctx, list, pbs); err != nil {
			return nil, err
		}
		for i := range list {
			files[list[i].Id] = list[i]
			filesPb[list[i].Id] = pbs[i]
		}
		if fileUsed, err = s.repo.Library().FileUsedIDs(ctx, fileIDs); err != nil {
			return nil, err
		}
		if ff, ok := s.repo.Library().(libraryFileFolders); ok {
			if fileFolders, err = ff.FileFolderIDs(ctx, fileIDs); err != nil {
				return nil, err
			}
		}
	}

	out := make([]*pb_admin.LibraryItem, 0, len(rows))
	for _, r := range rows {
		switch entity.LibrarySource(r.Src) {
		case entity.LibrarySourceMedia:
			m, ok := media[r.Id]
			if !ok {
				continue
			}
			out = append(out, libraryMediaItemToPb(&m, len(mediaUsage[r.Id]) > 0, usernames))
		case entity.LibrarySourceFile:
			f, ok := files[r.Id]
			if !ok {
				continue
			}
			item := &pb_admin.LibraryItem{
				Ref:         libraryRefToPb(entity.LibraryRef{Source: entity.LibrarySourceFile, Id: f.Id}),
				Kind:        libraryKindToPb(entity.FileKindOf(f.ContentType)),
				Name:        f.FileName,
				SizeBytes:   f.SizeBytes,
				ContentType: f.ContentType,
				FolderId:    int32(fileFolders[f.Id]),
				UploadedBy:  f.UploadedBy,
				InUse:       fileUsed[f.Id],
				File:        filesPb[f.Id],
			}
			if !f.CreatedAt.IsZero() {
				item.CreatedAt = timestamppb.New(f.CreatedAt)
			}
			if f.UploadedById.Valid {
				item.UploadedById = int32(f.UploadedById.Int64)
			}
			out = append(out, item)
		}
	}
	return out, nil
}

// libraryMediaItemToPb converts one media row. Kind comes from the generated lib_kind column, with
// the Go twin as the fallback for a row read without it.
func libraryMediaItemToPb(m *entity.MediaFull, inUse bool, usernames map[int]string) *pb_admin.LibraryItem {
	kind := entity.LibraryKindFromValue(m.Kind)
	if kind == entity.LibraryKindUnspecified {
		kind = entity.MediaKindOf(m.FullSizeMediaURL, m.ThumbnailMediaURL)
	}
	item := &pb_admin.LibraryItem{
		Ref:         libraryRefToPb(entity.LibraryRef{Source: entity.LibrarySourceMedia, Id: m.Id}),
		Kind:        libraryKindToPb(kind),
		Name:        m.FileName.String,
		SizeBytes:   m.SizeBytes.Int64,
		ContentType: libraryMediaContentType(kind, m),
		InUse:       inUse,
		Media:       dto.ConvertEntityToCommonMedia(m),
		Width:       int32(m.ThumbnailWidth),
		Height:      int32(m.ThumbnailHeight),
	}
	if !m.CreatedAt.IsZero() {
		item.CreatedAt = timestamppb.New(m.CreatedAt)
	}
	if m.FolderId.Valid {
		item.FolderId = int32(m.FolderId.Int64)
	}
	if m.UploadedById.Valid {
		item.UploadedById = int32(m.UploadedById.Int64)
		item.UploadedBy = usernames[int(m.UploadedById.Int64)]
	}
	return item
}

// libraryMediaContentType derives a media row's content type from its kind and the extension of the
// object that kind is decided on (the thumbnail for a video, the full size otherwise).
func libraryMediaContentType(kind entity.LibraryKind, m *entity.MediaFull) string {
	u := m.FullSizeMediaURL
	if kind == entity.LibraryKindVideo {
		u = m.ThumbnailMediaURL
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	ext := ""
	if dot := strings.LastIndexByte(u, '.'); dot >= 0 && dot < len(u)-1 {
		ext = strings.ToLower(u[dot+1:])
	}
	switch ext {
	case "webp":
		return "image/webp"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "avif":
		return "image/avif"
	case "heic":
		return "image/heic"
	case "svg":
		return "image/svg+xml"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "glb":
		return "model/gltf-binary"
	}
	switch kind {
	case entity.LibraryKindVideo:
		return "video/mp4"
	case entity.LibraryKindModel:
		return "model/gltf-binary"
	case entity.LibraryKindVector:
		return "image/svg+xml"
	default:
		return "image/webp"
	}
}
