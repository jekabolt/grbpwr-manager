package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLibraryFileInUse and ErrFileTopicInUse are the two refusals the library
// makes. Both exist as named errors rather than bare constraint violations
// because a raw "cannot delete: foreign key" tells the person nothing they can
// act on — the message has to name who is holding the thing.
var (
	ErrLibraryFileInUse = errors.New("library file is attached to a task")
	ErrFileTopicInUse   = errors.New("topic still has files")
)

// NewErrLibraryFileInUse wraps ErrLibraryFileInUse with the ids of the tasks
// holding the file, so the caller can say which cards to detach it from.
func NewErrLibraryFileInUse(taskIDs []int) error {
	ids := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		ids = append(ids, fmt.Sprintf("#%d", id))
	}
	return fmt.Errorf("%w: %s", ErrLibraryFileInUse, strings.Join(ids, ", "))
}

// NewErrFileTopicInUse wraps ErrFileTopicInUse with how many files still carry
// the topic. Deleting it anyway would drop them all into «Разобрать» silently.
func NewErrFileTopicInUse(files int) error {
	return fmt.Errorf("%w: %d", ErrFileTopicInUse, files)
}

// LibraryFileInsert is the writable payload of one library file: everything the
// upload path learns while streaming the bytes into private object storage.
// ObjectKey, Sha256 and SizeBytes are all server-derived — the client is never
// trusted for any of them, because the bytes pass through us anyway.
type LibraryFileInsert struct {
	ObjectKey string `db:"object_key"`
	// PreviewObjectKey is the browser-rendered preview image (first PDF page, a
	// downscaled raster, a rasterised SVG). Invalid/NULL means the render failed
	// or the type has no sensible preview — a file without one stays perfectly
	// usable, it just shows an extension plate in the grid.
	PreviewObjectKey sql.NullString `db:"preview_object_key"`
	FileName         string         `db:"file_name"`
	ContentType      string         `db:"content_type"`
	SizeBytes        int64          `db:"size_bytes"`
	Sha256           string         `db:"sha256"`
	UploadedBy       string         `db:"uploaded_by"`
}

// LibraryFile is a stored library file with its topic labels resolved.
type LibraryFile struct {
	Id int `db:"id"`
	LibraryFileInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// Topics are the labels this file carries. A file legitimately carries zero
	// of them: an unclassified file is honest, a wrongly classified one is not.
	Topics []FileTopic `db:"-"`
}

// FileTopic is one topic label. Topics are LABELS, not folders — a file carries
// several at once, which is what removes the "which single folder does this
// belong in" decision that loses files.
type FileTopic struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
	// Description turns a bare label into a minimal project page ("разработка
	// фурнитуры"): a place for what the work is, without the ceremony of a
	// separate project entity that nobody would keep up.
	Description sql.NullString `db:"description"`
	CreatedAt   time.Time      `db:"created_at"`
}

// FileTopicWithCount is a topic plus how many files carry it — the rail badge.
// The count is always derived by query; there is no counter column to drift.
type FileTopicWithCount struct {
	FileTopic
	FilesCount int `db:"files_count"`
}

// MaxLibraryTopicFilters bounds the intersection filter. Each selected topic
// costs one EXISTS subquery, and a set of chips beyond this size cannot come from
// a person narrowing a grid — it comes from a loop.
const MaxLibraryTopicFilters = 20

// LibraryFileSort is the ordering of the grid for the columns that are not time.
// The default (LibraryFileSortDefault) is by created_at, which is the only
// ordering OrderFactor applies to.
type LibraryFileSort int

const (
	LibraryFileSortDefault LibraryFileSort = iota
	LibraryFileSortName
	LibraryFileSortSize
)

// LibraryFileListFilter selects a page of the library.
//
// TopicId, TopicIds and Untopiced are views of the same rail and are resolved in
// that order of precedence, highest first: Untopiced (the «Разобрать» bucket),
// TopicIds (files carrying ALL of them), TopicId (one topic — the old single-value
// field, kept for links already in circulation).
type LibraryFileListFilter struct {
	TopicId int
	// TopicIds is an INTERSECTION: a file matches only when it carries every one
	// of these topics. Union would be the wrong meaning — a second chip is how a
	// person narrows the grid, and OR would widen it instead.
	TopicIds  []int
	Untopiced bool
	// Search matches the file name OR the name of a topic the file carries OR the
	// person who uploaded it. Matching topic names is what lets one input be
	// enough: "фурнитура" must land in the topic even when no single file is named
	// that way; matching the uploader is what makes "что заливал Паша" a question
	// this input can answer. The implementation is LIKE for now; FULLTEXT or
	// embeddings swap in behind this same field without touching the API contract.
	Search      string
	Limit       int
	Offset      int
	OrderFactor OrderFactor
	SortBy      LibraryFileSort
}
