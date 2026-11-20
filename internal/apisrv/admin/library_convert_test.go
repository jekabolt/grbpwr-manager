package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The entity enums mirror the proto numbers; every proto value must round-trip and an unknown
// number must land on unspecified.
func TestLibraryEnumConverters(t *testing.T) {
	for v := range pb_admin.LibrarySource_name {
		p := pb_admin.LibrarySource(v)
		if got := librarySourceToPb(librarySourceFromPb(p)); got != p {
			t.Errorf("source %v round-trips to %v", p, got)
		}
	}
	for v := range pb_admin.LibraryKind_name {
		p := pb_admin.LibraryKind(v)
		if got := libraryKindToPb(libraryKindFromPb(p)); got != p {
			t.Errorf("kind %v round-trips to %v", p, got)
		}
		if int(libraryKindFromPb(p)) != int(v) {
			t.Errorf("kind %v: entity number %d differs from proto", p, libraryKindFromPb(p))
		}
	}
	for v := range pb_admin.LibrarySort_name {
		if int(librarySortFromPb(pb_admin.LibrarySort(v))) != int(v) {
			t.Errorf("sort %d does not mirror the proto number", v)
		}
	}
	for v := range pb_admin.LibraryAccept_name {
		if int(libraryAcceptFromPb(pb_admin.LibraryAccept(v))) != int(v) {
			t.Errorf("accept %d does not mirror the proto number", v)
		}
	}
	for v := range pb_admin.LibraryUsage_name {
		if int(libraryUsageFromPb(pb_admin.LibraryUsage(v))) != int(v) {
			t.Errorf("usage %d does not mirror the proto number", v)
		}
	}
	if libraryKindFromPb(99) != entity.LibraryKindUnspecified || librarySourceFromPb(99) != entity.LibrarySourceUnspecified ||
		librarySortFromPb(99) != entity.LibrarySortUnspecified {
		t.Error("unknown numbers must become unspecified")
	}
}

func TestLibraryFilterFromPb(t *testing.T) {
	if got := libraryFilterFromPb(nil); got.FolderId != 0 || got.Kinds != nil {
		t.Errorf("nil filter = %+v", got)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	got := libraryFilterFromPb(&pb_admin.LibraryItemFilter{
		Kinds:             []pb_admin.LibraryKind{pb_admin.LibraryKind_LIBRARY_KIND_IMAGE, pb_admin.LibraryKind_LIBRARY_KIND_VIDEO},
		Accept:            pb_admin.LibraryAccept_LIBRARY_ACCEPT_VISUAL,
		Sources:           []pb_admin.LibrarySource{pb_admin.LibrarySource_LIBRARY_SOURCE_FILE},
		FolderId:          7,
		IncludeSubfolders: true,
		Search:            "  shoot  ",
		ProjectTopicId:    3,
		TopicIds:          []int32{4, 5},
		UploadedById:      9,
		CreatedFrom:       timestamppb.New(from),
		CreatedTo:         timestamppb.New(to),
		Usage:             pb_admin.LibraryUsage_LIBRARY_USAGE_FREE,
		Ratios:            []string{" 4:5 ", "", "other"},
	})
	if len(got.Kinds) != 2 || got.Kinds[1] != entity.LibraryKindVideo || got.Accept != entity.LibraryAcceptVisual ||
		len(got.Sources) != 1 || got.Sources[0] != entity.LibrarySourceFile || got.FolderId != 7 || !got.IncludeSubfolders ||
		got.Search != "shoot" || got.ProjectTopicId != 3 || len(got.TopicIds) != 2 || got.UploadedById != 9 ||
		!got.CreatedFrom.Equal(from) || !got.CreatedTo.Equal(to) || got.Usage != entity.LibraryUsageFree ||
		len(got.Ratios) != 2 || got.Ratios[0] != "4:5" {
		t.Errorf("filter = %+v", got)
	}
}

func TestLibraryRefAndFolderConverters(t *testing.T) {
	r := entity.LibraryRef{Source: entity.LibrarySourceFile, Id: 7}
	if got := libraryRefFromPb(libraryRefToPb(r)); got != r {
		t.Errorf("ref round trip = %+v", got)
	}
	if got := libraryRefFromPb(nil); got != (entity.LibraryRef{}) {
		t.Errorf("nil ref = %+v", got)
	}
	at := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	root := libraryFolderToPb(entity.LibraryFolder{Id: 1, Name: "shoots", CreatedAt: at, ItemCount: 3, ChildCount: 2})
	if root.ParentId != 0 || root.Name != "shoots" || root.ItemCount != 3 || root.ChildCount != 2 || !root.CreatedAt.AsTime().Equal(at) {
		t.Errorf("root folder = %+v", root)
	}
	child := libraryFolderToPb(entity.LibraryFolder{Id: 2, ParentId: sql.NullInt64{Int64: 1, Valid: true}})
	if child.ParentId != 1 || child.CreatedAt != nil {
		t.Errorf("child folder = %+v", child)
	}
	f := libraryFacetsToPb(&entity.LibraryFacets{
		Months: []entity.MonthCount{{Year: 2026, Month: 11, Count: 4}},
		Kinds:  []entity.LibraryKindCount{{Kind: entity.LibraryKindVector, Count: 1}},
		Used:   1, Free: 3, Total: 4,
		Ratios: []entity.LibraryRatioCount{{Ratio: "4:5", Count: 2}},
	})
	if len(f.Months) != 1 || f.Kinds[0].Kind != pb_admin.LibraryKind_LIBRARY_KIND_VECTOR || f.Used != 1 || f.Free != 3 || f.Total != 4 || f.Ratios[0].Ratio != "4:5" {
		t.Errorf("facets = %+v", f)
	}
	if libraryFacetsToPb(nil) == nil {
		t.Error("nil facets must be an empty response")
	}
}
