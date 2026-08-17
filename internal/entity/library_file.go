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
	// ErrLibraryBatchTooLarge is a bound the STORE checks, and it exists as a
	// named error for exactly one reason: without it the handler cannot tell a
	// bound from a broken query. Both arrive as a bare error, both get logged and
	// answered `Internal, "can't assign topics"` — and a person who ran into a
	// documented limit is told nothing at all, with nothing to try next. A limit
	// is the caller's argument being wrong, so it has to reach them as such.
	ErrLibraryBatchTooLarge = errors.New("too many items in one call")
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

// NewErrLibraryBatchTooLarge wraps ErrLibraryBatchTooLarge with the bound and
// the count that broke it. The number is not decoration: "too many" with no
// limit named is nothing the caller can act on, and the limit lives in the
// store — the client cannot restate it without the two drifting apart.
func NewErrLibraryBatchTooLarge(detail string) error {
	return fmt.Errorf("%w: %s", ErrLibraryBatchTooLarge, detail)
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
	// UploadedById is the LIVE account behind UploadedBy. Both are kept because
	// they have different lifetimes: the string is the historical fact and
	// survives the account's deletion, the id is the link and is nulled by it
	// (0314, ON DELETE SET NULL).
	//
	// НЕ В LibraryFileInsert, И ЭТО НЕ КОСМЕТИКА: Insert — это то, что вызывающий
	// ВПРАВЕ задать, а здесь он не вправе. Значение выводит стор из UploadedBy одним
	// оператором, чтобы две половины авторства не могли разойтись; лежи поле в
	// Insert, вызывающий заполнил бы его, не получил ни ошибки, ни эффекта и узнал
	// бы об этом нескоро.
	UploadedById sql.NullInt64 `db:"uploaded_by_id"`
	// Topics are the labels this file carries. A file legitimately carries zero
	// of them: an unclassified file is honest, a wrongly classified one is not.
	Topics []FileTopic `db:"-"`
	// Owners are the people who KEEP this file — who to ask when it goes stale.
	// Zero owners is legal for the same reason zero topics is: an empty field is
	// honest, a randomly assigned one is not.
	Owners []AdminRef `db:"-"`
	// CommentsCount is how many remarks the file's discussion holds — the number
	// on the tile and on the card. NOT a column, and deliberately so: a counter
	// column would have to be kept in step with a feed that cascades away with the
	// file, and it would drift the first time it was not. It is resolved for a
	// WHOLE page in one grouped query, exactly like Topics and Owners above.
	CommentsCount int `db:"-"`
	// AccessLevel is «кому виден файл» (0317). It only ever reaches somebody who
	// can already SEE the file — one that may not be seen is not in the answer at
	// all — so the level is not a secret from its reader: it is what badges the
	// tile «по ссылке» / «ограничен» instead of making a person open the card to
	// find out.
	AccessLevel LibraryFileAccessLevel `db:"access_level"`
	// ContentUpdatedBy / ContentUpdatedAt are «кто правил последним» for a note
	// saved THROUGH THE EDITOR (0318). They stand next to UploadedBy rather than
	// replacing it, because the upload is one fact and the last edit another: a
	// note that arrived as an upload and was never edited here keeps them empty,
	// and the card then falls back to the uploader, which is the truth in that case.
	ContentUpdatedBy string       `db:"content_updated_by"`
	ContentUpdatedAt sql.NullTime `db:"content_updated_at"`
	// ContentExcerpt is the first lines of a note's text: the tile preview where
	// there is no picture to render at all — the one documented exception to «no
	// preview → extension plate». Written on save through the editor; empty for a
	// .md that arrived as an upload, because reading text on the streaming upload
	// path would complicate the single hot path for a rare case.
	ContentExcerpt string `db:"content_excerpt"`
}

// LibraryFileComment is one remark in a file's discussion (0316).
//
// The feed is FLAT by construction — there is no parent id here and none in the
// table. Threads turn a six-person conversation into a tree that has to be read
// twice to find what was decided; a flat feed under the file is the whole of what
// «обсудить этот файл» needs.
type LibraryFileComment struct {
	Id     int `db:"id"`
	FileId int `db:"file_id"`
	// Author is the username AS OF WRITING: the historical fact, exactly like
	// LibraryFile.UploadedBy, and the string the «править можно только свою
	// реплику» rule compares against. AuthorId is the LIVE account behind it (the
	// avatar and the specialty byline read it) and is nulled when that account is
	// deleted. The two are not duplicates — they have different lifetimes.
	//
	// NEITHER is caller-supplied: the store derives both from the caller's JWT
	// username in one statement, so the two halves of authorship cannot disagree.
	Author   string        `db:"author"`
	AuthorId sql.NullInt64 `db:"author_id"`
	// Body is the raw text, @mentions included — the server stores what was typed.
	// A mention is FLAT text on purpose: the highlight and the people popover are
	// the client's, and server-side markup would have to be escaped again by every
	// reader anyway.
	Body      string    `db:"body"`
	CreatedAt time.Time `db:"created_at"`
	// EditedAt invalid/NULL = never edited. Set = the feed prints «изменено»,
	// because a silently rewritten remark is a silently rewritten conversation.
	EditedAt sql.NullTime `db:"edited_at"`
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

// LibraryFilePersonRole is the CAPACITY a person is on record for a file in.
//
// «Файлы Паши» is TWO questions and they must not be collapsed into one: he
// UPLOADED the file (a historical fact, whose string half outlives his account)
// or he KEEPS it (current responsibility, the owners list, which changes without
// the file changing at all). One person plus a role is therefore the whole
// control — two independent people filters would let a grid be asked for «залил
// Паша И ведёт Паша», which is not a question anybody has.
type LibraryFilePersonRole int

const (
	// LibraryFilePersonRoleAny — «где он числится вообще»: uploaded OR owns. The
	// zero value on purpose: it is what an unspecified role has to mean, and it is
	// also the wider of the two, so a filter that lost its role widens rather than
	// silently hides files the person is in fact on record for.
	LibraryFilePersonRoleAny LibraryFilePersonRole = iota
	LibraryFilePersonRoleUploaded
	LibraryFilePersonRoleOwner
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
	Search string
	// PersonId narrows to the files ONE account is on record for; PersonRole says
	// in which capacity. Non-positive means no person filter at all (not
	// «nobody»), and an id belonging to no account matches nothing rather than
	// failing — an error there would answer «does account N exist» to anybody
	// willing to count up.
	//
	// An ACCOUNT ID, never a name, and that is the whole reason this field exists
	// next to Search: Search matches the uploader as a STRING, which is right for
	// a typed query and wrong as a filter — admins.username is UNIQUE and is FREED
	// when the account is deleted, so a namesake hired later would inherit the
	// whole history of the person who left (the hole closed twice already, in Ф3
	// and in the visibility predicate).
	PersonId    int
	PersonRole  LibraryFilePersonRole
	Limit       int
	Offset      int
	OrderFactor OrderFactor
	SortBy      LibraryFileSort
}
