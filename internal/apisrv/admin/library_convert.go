package admin

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// pb <-> entity converters for the unified library. The enums share numbers on purpose (entity
// zero = proto _UNSPECIFIED), but every value goes through a switch so an unknown number becomes
// unspecified instead of a silently invalid entity value.

func librarySourceFromPb(v pb_admin.LibrarySource) entity.LibrarySource {
	switch v {
	case pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA:
		return entity.LibrarySourceMedia
	case pb_admin.LibrarySource_LIBRARY_SOURCE_FILE:
		return entity.LibrarySourceFile
	default:
		return entity.LibrarySourceUnspecified
	}
}

func librarySourceToPb(v entity.LibrarySource) pb_admin.LibrarySource {
	switch v {
	case entity.LibrarySourceMedia:
		return pb_admin.LibrarySource_LIBRARY_SOURCE_MEDIA
	case entity.LibrarySourceFile:
		return pb_admin.LibrarySource_LIBRARY_SOURCE_FILE
	default:
		return pb_admin.LibrarySource_LIBRARY_SOURCE_UNSPECIFIED
	}
}

func libraryKindFromPb(v pb_admin.LibraryKind) entity.LibraryKind {
	switch v {
	case pb_admin.LibraryKind_LIBRARY_KIND_IMAGE:
		return entity.LibraryKindImage
	case pb_admin.LibraryKind_LIBRARY_KIND_VIDEO:
		return entity.LibraryKindVideo
	case pb_admin.LibraryKind_LIBRARY_KIND_VECTOR:
		return entity.LibraryKindVector
	case pb_admin.LibraryKind_LIBRARY_KIND_MODEL:
		return entity.LibraryKindModel
	case pb_admin.LibraryKind_LIBRARY_KIND_DOCUMENT:
		return entity.LibraryKindDocument
	default:
		return entity.LibraryKindUnspecified
	}
}

func libraryKindToPb(v entity.LibraryKind) pb_admin.LibraryKind {
	switch v {
	case entity.LibraryKindImage:
		return pb_admin.LibraryKind_LIBRARY_KIND_IMAGE
	case entity.LibraryKindVideo:
		return pb_admin.LibraryKind_LIBRARY_KIND_VIDEO
	case entity.LibraryKindVector:
		return pb_admin.LibraryKind_LIBRARY_KIND_VECTOR
	case entity.LibraryKindModel:
		return pb_admin.LibraryKind_LIBRARY_KIND_MODEL
	case entity.LibraryKindDocument:
		return pb_admin.LibraryKind_LIBRARY_KIND_DOCUMENT
	default:
		return pb_admin.LibraryKind_LIBRARY_KIND_UNSPECIFIED
	}
}

func libraryAcceptFromPb(v pb_admin.LibraryAccept) entity.LibraryAccept {
	switch v {
	case pb_admin.LibraryAccept_LIBRARY_ACCEPT_ANY:
		return entity.LibraryAcceptAny
	case pb_admin.LibraryAccept_LIBRARY_ACCEPT_VISUAL:
		return entity.LibraryAcceptVisual
	default:
		return entity.LibraryAcceptUnspecified
	}
}

func libraryUsageFromPb(v pb_admin.LibraryUsage) entity.LibraryUsage {
	switch v {
	case pb_admin.LibraryUsage_LIBRARY_USAGE_USED:
		return entity.LibraryUsageUsed
	case pb_admin.LibraryUsage_LIBRARY_USAGE_FREE:
		return entity.LibraryUsageFree
	default:
		return entity.LibraryUsageUnspecified
	}
}

func librarySortFromPb(v pb_admin.LibrarySort) entity.LibrarySort {
	switch v {
	case pb_admin.LibrarySort_LIBRARY_SORT_NEW:
		return entity.LibrarySortNew
	case pb_admin.LibrarySort_LIBRARY_SORT_OLD:
		return entity.LibrarySortOld
	case pb_admin.LibrarySort_LIBRARY_SORT_NAME:
		return entity.LibrarySortName
	case pb_admin.LibrarySort_LIBRARY_SORT_SIZE:
		return entity.LibrarySortSize
	default:
		return entity.LibrarySortUnspecified
	}
}

func libraryRefFromPb(r *pb_admin.LibraryItemRef) entity.LibraryRef {
	if r == nil {
		return entity.LibraryRef{}
	}
	return entity.LibraryRef{Source: librarySourceFromPb(r.GetSource()), Id: int(r.GetId())}
}

func libraryRefToPb(r entity.LibraryRef) *pb_admin.LibraryItemRef {
	return &pb_admin.LibraryItemRef{Source: librarySourceToPb(r.Source), Id: int32(r.Id)}
}

// libraryFilterFromPb converts the wire filter. It only translates; validation (combinations,
// limits, ratio syntax) is the handler's, so the store sees exactly what was asked. Search is
// trimmed; ratios are trimmed and blanks dropped.
func libraryFilterFromPb(f *pb_admin.LibraryItemFilter) entity.LibraryItemFilter {
	if f == nil {
		return entity.LibraryItemFilter{}
	}
	out := entity.LibraryItemFilter{
		Accept:            libraryAcceptFromPb(f.GetAccept()),
		FolderId:          int(f.GetFolderId()),
		NoFolder:          f.GetNoFolder(),
		IncludeSubfolders: f.GetIncludeSubfolders(),
		Search:            strings.TrimSpace(f.GetSearch()),
		ProjectTopicId:    int(f.GetProjectTopicId()),
		UploadedById:      int(f.GetUploadedById()),
		Usage:             libraryUsageFromPb(f.GetUsage()),
	}
	for _, k := range f.GetKinds() {
		out.Kinds = append(out.Kinds, libraryKindFromPb(k))
	}
	for _, s := range f.GetSources() {
		out.Sources = append(out.Sources, librarySourceFromPb(s))
	}
	for _, id := range f.GetTopicIds() {
		out.TopicIds = append(out.TopicIds, int(id))
	}
	if ts := f.GetCreatedFrom(); ts != nil {
		out.CreatedFrom = ts.AsTime().UTC()
	}
	if ts := f.GetCreatedTo(); ts != nil {
		out.CreatedTo = ts.AsTime().UTC()
	}
	for _, r := range f.GetRatios() {
		if r = strings.TrimSpace(r); r != "" {
			out.Ratios = append(out.Ratios, r)
		}
	}
	return out
}

// libraryFolderToPb converts one folder; a NULL parent is 0 (root).
func libraryFolderToPb(f entity.LibraryFolder) *pb_admin.LibraryFolder {
	out := &pb_admin.LibraryFolder{
		Id:         int32(f.Id),
		Name:       f.Name,
		ItemCount:  int32(f.ItemCount),
		ChildCount: int32(f.ChildCount),
	}
	if f.ParentId.Valid {
		out.ParentId = int32(f.ParentId.Int64)
	}
	if !f.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(f.CreatedAt)
	}
	return out
}

func libraryFacetsToPb(f *entity.LibraryFacets) *pb_admin.GetLibraryFacetsResponse {
	if f == nil {
		return &pb_admin.GetLibraryFacetsResponse{}
	}
	out := &pb_admin.GetLibraryFacetsResponse{
		Used:  int32(f.Used),
		Free:  int32(f.Free),
		Total: int32(f.Total),
	}
	for _, m := range f.Months {
		out.Months = append(out.Months, &pb_admin.MonthCount{Year: int32(m.Year), Month: int32(m.Month), Count: int32(m.Count)})
	}
	for _, k := range f.Kinds {
		out.Kinds = append(out.Kinds, &pb_admin.LibraryKindCount{Kind: libraryKindToPb(k.Kind), Count: int32(k.Count)})
	}
	for _, r := range f.Ratios {
		out.Ratios = append(out.Ratios, &pb_admin.LibraryRatioCount{Ratio: r.Ratio, Count: int32(r.Count)})
	}
	return out
}
