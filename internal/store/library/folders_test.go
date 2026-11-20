package library_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	"github.com/stretchr/testify/require"
)

// Folder DB tests run only against the disposable container of ~/.claude/scripts/test-db.sh (CI=1,
// MYSQL_*), like the design band probes. Names carry a per-test random tag: the folder table is shared
// by every test in the run, root siblings included.

var (
	dbOnce sync.Once
	dbRep  dependency.Repository
	dbRaw  *sql.DB
	dbErr  error
)

func libraryDB(t *testing.T) (dependency.Repository, *sql.DB) {
	t.Helper()
	if os.Getenv("CI") != "1" {
		t.Skip("library folder DB tests run only against a disposable container (CI=1)")
	}
	host, port := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT")
	user, pass, name := os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_DATABASE")
	if host == "" || port == "" || user == "" || name == "" {
		t.Skip("MYSQL_* of the disposable container are not set")
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true", user, pass, host, port, name)
	if err := testdbguard.CheckMySQLDSN(dsn); err != nil {
		t.Fatalf("library DB tests refuse to open MySQL: %v", err)
	}
	dbOnce.Do(func() {
		ctx := context.Background()
		if dbRaw, dbErr = sql.Open("mysql", dsn); dbErr != nil {
			return
		}
		if dbErr = dbRaw.Ping(); dbErr != nil {
			return
		}
		dbRep, dbErr = store.NewForTest(ctx, store.Config{DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5})
	})
	require.NoError(t, dbErr)
	require.NotNil(t, dbRep)
	return dbRep, dbRaw
}

// folderJanitor removes every folder a test made, whatever shape the moves left the tree in: pointers
// into the folders are cleared first, then leaves are deleted until nothing is left.
type folderJanitor struct {
	raw *sql.DB
	ids []int
}

func newJanitor(t *testing.T, raw *sql.DB) *folderJanitor {
	j := &folderJanitor{raw: raw}
	t.Cleanup(func() {
		for _, id := range j.ids {
			_, _ = raw.Exec(`UPDATE media SET lib_folder_id = NULL WHERE lib_folder_id = ?`, id)
			_, _ = raw.Exec(`UPDATE library_file SET folder_id = NULL WHERE folder_id = ?`, id)
		}
		for pass := 0; pass < entity.LibraryFolderMaxDepth+2; pass++ {
			for _, id := range j.ids {
				_, _ = raw.Exec(`DELETE FROM library_folder WHERE id = ?`, id)
			}
		}
	})
	return j
}

func (j *folderJanitor) track(f *entity.LibraryFolder) *entity.LibraryFolder {
	if f != nil {
		j.ids = append(j.ids, f.Id)
	}
	return f
}

func tag() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:10] }

func mustCreate(t *testing.T, lib dependency.Library, j *folderJanitor, parent int, name string) *entity.LibraryFolder {
	t.Helper()
	f, err := lib.CreateFolder(context.Background(), parent, name, 0)
	require.NoError(t, err, "create %q under %d", name, parent)
	return j.track(f)
}

func insertMedia(t *testing.T, raw *sql.DB) int {
	t.Helper()
	key := uuid.NewString()
	res, err := raw.Exec(`INSERT INTO media
		(full_size, full_size_width, full_size_height, thumbnail, thumbnail_width, thumbnail_height,
		 compressed, compressed_width, compressed_height)
		VALUES (?, 100, 100, ?, 10, 10, ?, 50, 50)`,
		"probe/"+key+"-full.png", "probe/"+key+"-thumb.png", "probe/"+key+"-small.png")
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM media WHERE id = ?`, id) })
	return int(id)
}

func insertAdmin(t *testing.T, raw *sql.DB) (int, string) {
	t.Helper()
	name := "lib-" + tag()
	res, err := raw.Exec(`INSERT INTO admins (username, password_hash) VALUES (?, 'x')`, name)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM admins WHERE id = ?`, id) })
	return int(id), name
}

func randomSha() string {
	s := sha256.Sum256([]byte(uuid.NewString()))
	return hex.EncodeToString(s[:])
}

// insertFile puts a library file; access 'people' with an uploader makes it visible to that uploader only.
func insertFile(t *testing.T, raw *sql.DB, ct, access, uploader string, folderID int) (int, string) {
	t.Helper()
	sha := randomSha()
	var folder any
	if folderID > 0 {
		folder = folderID
	}
	res, err := raw.Exec(`INSERT INTO library_file
		(object_key, file_name, content_type, size_bytes, sha256, uploaded_by, uploaded_by_id, access_level, folder_id)
		VALUES (CONCAT('files-library/test-', UUID_SHORT()), 'probe.png', ?, 10, ?, ?,
			(SELECT a.id FROM admins a WHERE a.username = ?), ?, ?)`,
		ct, sha, uploader, uploader, access, folder)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM library_file WHERE id = ?`, id) })
	return int(id), sha
}

func mediaFolder(t *testing.T, raw *sql.DB, id int) sql.NullInt64 {
	t.Helper()
	var f sql.NullInt64
	require.NoError(t, raw.QueryRow(`SELECT lib_folder_id FROM media WHERE id = ?`, id).Scan(&f))
	return f
}

func fileFolder(t *testing.T, raw *sql.DB, id int) sql.NullInt64 {
	t.Helper()
	var f sql.NullInt64
	require.NoError(t, raw.QueryRow(`SELECT folder_id FROM library_file WHERE id = ?`, id).Scan(&f))
	return f
}

func superCtx() context.Context {
	return authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{Super: true})
}

func TestFolderSiblingNamesAreUniqueCaseInsensitive(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	name := "Shoots " + tag()

	root := mustCreate(t, lib, j, 0, name)
	_, err := lib.CreateFolder(ctx, 0, strings.ToUpper(name), 0)
	require.ErrorIs(t, err, entity.ErrFolderNameTaken, "root siblings")

	child := mustCreate(t, lib, j, root.Id, "raw")
	_, err = lib.CreateFolder(ctx, root.Id, "  RAW ", 0)
	require.ErrorIs(t, err, entity.ErrFolderNameTaken, "nested siblings")

	other := mustCreate(t, lib, j, 0, "Other "+tag())
	mustCreate(t, lib, j, other.Id, "raw") // same name under a different parent is fine

	sib := mustCreate(t, lib, j, root.Id, "edits")
	_, err = lib.RenameFolder(ctx, sib.Id, "Raw")
	require.ErrorIs(t, err, entity.ErrFolderNameTaken, "rename onto a sibling")
	_, err = lib.MoveFolder(ctx, child.Id, other.Id)
	require.ErrorIs(t, err, entity.ErrFolderNameTaken, "move next to a same-named folder")

	renamed, err := lib.RenameFolder(ctx, sib.Id, "  Edits v2 ")
	require.NoError(t, err)
	require.Equal(t, "Edits v2", renamed.Name)
}

func TestFolderNameValidation(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, _, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	for _, bad := range []string{"", "   ", "a/b", "tab\there", strings.Repeat("x", entity.LibraryFolderNameMaxLen+1)} {
		_, err := lib.CreateFolder(ctx, 0, bad, 0)
		require.ErrorIs(t, err, entity.ErrFolderNameInvalid, "%q", bad)
	}
	_, err := lib.CreateFolder(ctx, 987654321, "orphan", 0)
	require.ErrorIs(t, err, entity.ErrFolderNotFound)
}

func TestFolderDepthLimit(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()

	parent := 0
	var chain []*entity.LibraryFolder
	for d := 1; d <= entity.LibraryFolderMaxDepth; d++ {
		name := fmt.Sprintf("d%d", d)
		if d == 1 {
			name = "depth " + tag()
		}
		f := mustCreate(t, lib, j, parent, name)
		chain = append(chain, f)
		parent = f.Id
	}
	_, err := lib.CreateFolder(ctx, parent, "d9", 0)
	require.ErrorIs(t, err, entity.ErrFolderDepth, "the ninth level")

	// A subtree of height 3 fits under depth 5 (5+3 = 8) and not under depth 6.
	x := mustCreate(t, lib, j, 0, "sub "+tag())
	y := mustCreate(t, lib, j, x.Id, "y")
	mustCreate(t, lib, j, y.Id, "z")
	_, err = lib.MoveFolder(ctx, x.Id, chain[5].Id)
	require.ErrorIs(t, err, entity.ErrFolderDepth, "subtree past the limit")
	moved, err := lib.MoveFolder(ctx, x.Id, chain[4].Id)
	require.NoError(t, err)
	require.EqualValues(t, chain[4].Id, moved.ParentId.Int64)
	require.Equal(t, 1, moved.ChildCount)

	back, err := lib.MoveFolder(ctx, x.Id, 0)
	require.NoError(t, err)
	require.False(t, back.ParentId.Valid, "0 moves to the root")
}

func TestFolderCycleRefused(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	a := mustCreate(t, lib, j, 0, "cycle "+tag())
	b := mustCreate(t, lib, j, a.Id, "b")
	c := mustCreate(t, lib, j, b.Id, "c")

	_, err := lib.MoveFolder(ctx, a.Id, a.Id)
	require.ErrorIs(t, err, entity.ErrFolderCycle, "into itself")
	_, err = lib.MoveFolder(ctx, a.Id, c.Id)
	require.ErrorIs(t, err, entity.ErrFolderCycle, "into its grandchild")
	_, err = lib.MoveFolder(ctx, 987654321, 0)
	require.ErrorIs(t, err, entity.ErrFolderNotFound)
	_, err = lib.MoveFolder(ctx, b.Id, 987654321)
	require.ErrorIs(t, err, entity.ErrFolderNotFound)
}

func TestFolderDeleteOnlyWhenEmpty(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	_, owner := insertAdmin(t, raw)

	empty := mustCreate(t, lib, j, 0, "empty "+tag())
	require.NoError(t, lib.DeleteFolder(ctx, empty.Id))
	require.ErrorIs(t, lib.DeleteFolder(ctx, empty.Id), entity.ErrFolderNotFound)

	withChild := mustCreate(t, lib, j, 0, "parent "+tag())
	mustCreate(t, lib, j, withChild.Id, "kid")
	require.ErrorIs(t, lib.DeleteFolder(ctx, withChild.Id), entity.ErrFolderNotEmpty, "child folder")

	withMedia := mustCreate(t, lib, j, 0, "media "+tag())
	m := insertMedia(t, raw)
	_, err := raw.Exec(`UPDATE media SET lib_folder_id = ? WHERE id = ?`, withMedia.Id, m)
	require.NoError(t, err)
	require.ErrorIs(t, lib.DeleteFolder(ctx, withMedia.Id), entity.ErrFolderNotEmpty, "media")

	// A file nobody but its uploader can see still keeps the folder: the check is blind to ACL.
	withFile := mustCreate(t, lib, j, 0, "file "+tag())
	insertFile(t, raw, "application/pdf", "people", owner, withFile.Id)
	require.ErrorIs(t, lib.DeleteFolder(ctx, withFile.Id), entity.ErrFolderNotEmpty, "invisible file")
}

func TestMoveLibraryItemsAllOrNothing(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), superCtx()
	_, owner := insertAdmin(t, raw)
	dst := mustCreate(t, lib, j, 0, "dst "+tag())
	m1, m2 := insertMedia(t, raw), insertMedia(t, raw)
	f1, _ := insertFile(t, raw, "application/pdf", "team", owner, 0)

	_, err := lib.MoveItems(ctx, []entity.LibraryRef{
		{Source: entity.LibrarySourceMedia, Id: m1},
		{Source: entity.LibrarySourceFile, Id: f1},
		{Source: entity.LibrarySourceMedia, Id: 987654321},
	}, dst.Id)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.False(t, mediaFolder(t, raw, m1).Valid, "an unknown id moves nothing")
	require.False(t, fileFolder(t, raw, f1).Valid, "an unknown id moves nothing")

	_, err = lib.MoveItems(ctx, []entity.LibraryRef{{Source: entity.LibrarySourceMedia, Id: m1}}, 987654321)
	require.ErrorIs(t, err, entity.ErrFolderNotFound)

	n, err := lib.MoveItems(ctx, []entity.LibraryRef{
		{Source: entity.LibrarySourceMedia, Id: m1},
		{Source: entity.LibrarySourceMedia, Id: m1}, // duplicates count once
		{Source: entity.LibrarySourceMedia, Id: m2},
		{Source: entity.LibrarySourceFile, Id: f1},
	}, dst.Id)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.EqualValues(t, dst.Id, mediaFolder(t, raw, m1).Int64)
	require.EqualValues(t, dst.Id, fileFolder(t, raw, f1).Int64)

	// Moving into the folder they are already in still counts them (no RowsAffected trap).
	n, err = lib.MoveItems(ctx, []entity.LibraryRef{{Source: entity.LibrarySourceMedia, Id: m1}}, dst.Id)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Folder 0 takes them out.
	n, err = lib.MoveItems(ctx, []entity.LibraryRef{{Source: entity.LibrarySourceMedia, Id: m2}, {Source: entity.LibrarySourceFile, Id: f1}}, 0)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.False(t, mediaFolder(t, raw, m2).Valid)
	require.False(t, fileFolder(t, raw, f1).Valid)
}

// A file the mover cannot see is NotFound inside the move tx, and the batch moves nothing.
func TestMoveLibraryItemsInvisibleFile(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j := rep.Library(), newJanitor(t, raw)
	_, owner := insertAdmin(t, raw)
	_, stranger := insertAdmin(t, raw)
	dst := mustCreate(t, lib, j, 0, "vis "+tag())
	m := insertMedia(t, raw)
	hidden, _ := insertFile(t, raw, "application/pdf", "people", owner, 0)

	strangerCtx := authsrv.PutAdminUsername(authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{}), stranger)
	_, err := lib.MoveItems(strangerCtx, []entity.LibraryRef{
		{Source: entity.LibrarySourceMedia, Id: m},
		{Source: entity.LibrarySourceFile, Id: hidden},
	}, dst.Id)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.False(t, mediaFolder(t, raw, m).Valid)
	require.False(t, fileFolder(t, raw, hidden).Valid)

	ownerCtx := authsrv.PutAdminUsername(authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{}), owner)
	n, err := lib.MoveItems(ownerCtx, []entity.LibraryRef{{Source: entity.LibrarySourceFile, Id: hidden}}, dst.Id)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

// Move and Delete racing on one folder never leave an item pointing at a deleted folder: media has no
// FK on lib_folder_id, so the FOR SHARE / FOR UPDATE pair is the only guard.
func TestFolderConcurrentMoveVsDeleteNeverDangles(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j := rep.Library(), newJanitor(t, raw)
	ctx := superCtx()

	for i := 0; i < 15; i++ {
		f := mustCreate(t, lib, j, 0, fmt.Sprintf("race %d %s", i, tag()))
		m := insertMedia(t, raw)
		var wg sync.WaitGroup
		var moveErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, moveErr = lib.MoveItems(ctx, []entity.LibraryRef{{Source: entity.LibrarySourceMedia, Id: m}}, f.Id)
		}()
		go func() {
			defer wg.Done()
			delErr = lib.DeleteFolder(ctx, f.Id)
		}()
		wg.Wait()

		var exists int
		require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM library_folder WHERE id = ?`, f.Id).Scan(&exists))
		got := mediaFolder(t, raw, m)
		if exists == 0 {
			require.False(t, got.Valid, "round %d: media points at a deleted folder (move=%v delete=%v)", i, moveErr, delErr)
			require.True(t, moveErr == nil || errors.Is(moveErr, entity.ErrFolderNotFound), "round %d: move %v", i, moveErr)
		} else {
			require.ErrorIs(t, delErr, entity.ErrFolderNotEmpty, "round %d: folder survived without a refusal", i)
			require.EqualValues(t, f.Id, got.Int64, "round %d", i)
		}
	}
}
