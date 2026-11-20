package entity

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The unified library: media rows and library files in one list, keyed by (source, id). Media id 7
// and file id 7 are different rows. Neither table moves; this is a layer on top (media-files wave).

// LibrarySource says which table a library row lives in. The zero value is "unspecified".
type LibrarySource int

const (
	LibrarySourceUnspecified LibrarySource = iota
	LibrarySourceMedia
	LibrarySourceFile
)

// LibraryKind is what a library row is. The zero value is "unspecified".
type LibraryKind int

const (
	LibraryKindUnspecified LibraryKind = iota
	LibraryKindImage
	LibraryKindVideo
	LibraryKindVector
	LibraryKindModel
	LibraryKindDocument
)

// The values of the generated column media.lib_kind (0483) and of the files-branch kind CASE.
const (
	LibraryKindValueImage    = "image"
	LibraryKindValueVideo    = "video"
	LibraryKindValueVector   = "vector"
	LibraryKindValueModel    = "model"
	LibraryKindValueDocument = "document"
)

// String is the SQL value of the kind ("" for unspecified).
func (k LibraryKind) String() string {
	switch k {
	case LibraryKindImage:
		return LibraryKindValueImage
	case LibraryKindVideo:
		return LibraryKindValueVideo
	case LibraryKindVector:
		return LibraryKindValueVector
	case LibraryKindModel:
		return LibraryKindValueModel
	case LibraryKindDocument:
		return LibraryKindValueDocument
	default:
		return ""
	}
}

// LibraryKindFromValue parses the SQL value; anything unknown is unspecified.
func LibraryKindFromValue(v string) LibraryKind {
	switch v {
	case LibraryKindValueImage:
		return LibraryKindImage
	case LibraryKindValueVideo:
		return LibraryKindVideo
	case LibraryKindValueVector:
		return LibraryKindVector
	case LibraryKindValueModel:
		return LibraryKindModel
	case LibraryKindValueDocument:
		return LibraryKindDocument
	default:
		return LibraryKindUnspecified
	}
}

// LibraryAccept narrows the list for a picker. Unspecified means any.
type LibraryAccept int

const (
	LibraryAcceptUnspecified LibraryAccept = iota
	LibraryAcceptAny
	LibraryAcceptVisual
)

// LibraryUsage filters by "is it used anywhere". Unspecified means both.
type LibraryUsage int

const (
	LibraryUsageUnspecified LibraryUsage = iota
	LibraryUsageUsed
	LibraryUsageFree
)

// LibrarySort orders the list. Unspecified means newest first.
type LibrarySort int

const (
	LibrarySortUnspecified LibrarySort = iota
	LibrarySortNew
	LibrarySortOld
	LibrarySortName
	LibrarySortSize
)

// LibraryRef is the row key of the unified list.
type LibraryRef struct {
	Source LibrarySource
	Id     int
}

// LibraryItemFilter mirrors admin.LibraryItemFilter; the Go zero value is the proto zero value.
type LibraryItemFilter struct {
	Kinds             []LibraryKind
	Accept            LibraryAccept
	Sources           []LibrarySource
	FolderId          int
	NoFolder          bool
	IncludeSubfolders bool
	Search            string
	ProjectTopicId    int
	TopicIds          []int
	UploadedById      int
	CreatedFrom       time.Time // zero = open
	CreatedTo         time.Time // zero = open; the range is [from, to)
	Usage             LibraryUsage
	Ratios            []string
}

// LibraryBranches says which tables a library read may touch, decided by the handler from the
// caller's sections and the team's features (02-DESIGN §3.1) and then narrowed by the filter.
type LibraryBranches struct {
	Media bool
	Files bool
}

// LibraryItemRow is one row of the UNION page: which table, which id, and the sort keys. The handler
// loads the full media rows and files for the page by id.
type LibraryItemRow struct {
	Src       int       `db:"src"`
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	SortName  string    `db:"sort_name"`
	SizeBytes int64     `db:"size_bytes"`
}

// LibraryFolder is one folder of the shared tree. ParentId 0 = root. ItemCount is the direct items
// visible to the caller under the filter; ChildCount the direct subfolders.
type LibraryFolder struct {
	Id          int           `db:"id"`
	ParentId    sql.NullInt64 `db:"parent_id"`
	Name        string        `db:"name"`
	CreatedById sql.NullInt64 `db:"created_by_id"`
	CreatedAt   time.Time     `db:"created_at"`
	UpdatedAt   time.Time     `db:"updated_at"`
	ItemCount   int           `db:"item_count"`
	ChildCount  int           `db:"child_count"`
}

// MediaLibraryMeta is the library metadata of a media row (0483). It is embedded in MediaFull with
// json:"-", so JSON holder documents and fingerprints that marshal MediaFull/MediaItem stay
// byte-identical, and SELECT * FROM media fills it. Kind is the VIRTUAL lib_kind (never NULL; "" when
// a query names its columns); the others are NULL for rows uploaded before this wave.
type MediaLibraryMeta struct {
	Kind         string         `db:"lib_kind"`
	FileName     sql.NullString `db:"lib_file_name"`
	SizeBytes    sql.NullInt64  `db:"lib_size_bytes"`
	UploadedById sql.NullInt64  `db:"lib_uploaded_by_id"`
	FolderId     sql.NullInt64  `db:"lib_folder_id"`
	SourceSha256 sql.NullString `db:"lib_source_sha256"`
}

// LibraryKindCount is one kind and its row count.
type LibraryKindCount struct {
	Kind  LibraryKind
	Count int
}

// LibraryRatioCount is one reduced ratio ("4:5" or "other") and its row count.
type LibraryRatioCount struct {
	Ratio string
	Count int
}

// LibraryFacets answers GetLibraryFacets: every count is under the same filter minus its own axis.
type LibraryFacets struct {
	Months []MonthCount
	Kinds  []LibraryKindCount
	Used   int
	Free   int
	Ratios []LibraryRatioCount
	Total  int
}

// Folder rules (store internal/store/library/folders.go).
const (
	LibraryFolderMaxDepth   = 8
	LibraryFolderNameMaxLen = 120 // runes
)

var (
	// ErrNotImplemented is returned by library store stubs until their lane fills them.
	ErrNotImplemented = errors.New("not implemented")

	// ErrLibraryItemsFilterInvalid is a unified-list filter that has no meaning (the files list keeps
	// its own ErrLibraryFilterInvalid).
	ErrLibraryItemsFilterInvalid = errors.New("library_filter_invalid")
	ErrLibraryNoSection          = errors.New("library_no_section")
	ErrFolderNotFound            = errors.New("folder_not_found")
	ErrFolderNotEmpty            = errors.New("folder_not_empty")
	ErrFolderDepth               = errors.New("folder_depth")
	ErrFolderCycle               = errors.New("folder_cycle")
	ErrFolderNameTaken           = errors.New("folder_name_taken")
	ErrFolderNameInvalid         = errors.New("folder_name_invalid")
	ErrNotCopyable               = errors.New("not_copyable")
)

// lastExt is the lowercased text after the last dot; no dot or a trailing dot means none. The same
// rule as mediaThumbnailIsVideo (store/content/media.go).
func lastExt(u string) string {
	dot := strings.LastIndexByte(u, '.')
	if dot < 0 || dot == len(u)-1 {
		return ""
	}
	return strings.ToLower(u[dot+1:])
}

// MediaKindOf is the Go twin of the media.lib_kind generated column (0483), in the same order:
// video (decided on thumbnail, as mediaThumbnailIsVideo), model (.glb full size), vector (.svg full
// size), else image. Keep the two identical; store/library/schema_test.go pins them together.
func MediaKindOf(fullSize, thumbnail string) LibraryKind {
	switch lastExt(thumbnail) {
	case "mp4", "webm":
		return LibraryKindVideo
	}
	switch lastExt(fullSize) {
	case "glb":
		return LibraryKindModel
	case "svg":
		return LibraryKindVector
	}
	return LibraryKindImage
}

func normContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// FileKindOf maps a library file's content type to its kind. image/vnd.dxf is a CAD drawing, so a
// document; svg is a vector; any other image/* an image; video/* a video; glb a model.
func FileKindOf(contentType string) LibraryKind {
	ct := normContentType(contentType)
	switch {
	case ct == "image/svg+xml":
		return LibraryKindVector
	case ct == "image/vnd.dxf":
		return LibraryKindDocument
	case strings.HasPrefix(ct, "image/"):
		return LibraryKindImage
	case strings.HasPrefix(ct, "video/"):
		return LibraryKindVideo
	case ct == "model/gltf-binary":
		return LibraryKindModel
	default:
		return LibraryKindDocument
	}
}

// CopyableToMediaContentTypes are the file content types CopyLibraryFileToMedia accepts: every type
// a media upload door stores through the bucket verb the copy calls. Raster jpeg/png/webp/heic go
// through the 3-size re-encode, gif through the same image verb's verbatim path (it keeps the
// animation, exactly as the image door does), svg as a vector, mp4/webm as a video. avif, heif and
// dxf are refused by every door; a glb model needs a client-rendered preview the copy cannot make.
var CopyableToMediaContentTypes = []string{
	"image/jpeg", "image/png", "image/webp", "image/heic", "image/gif",
	"image/svg+xml",
	"video/mp4", "video/webm",
}

// CopyableToMedia reports whether a library file of this content type can become a media row.
func CopyableToMedia(contentType string) bool {
	ct := normContentType(contentType)
	for _, c := range CopyableToMediaContentTypes {
		if ct == c {
			return true
		}
	}
	return false
}
