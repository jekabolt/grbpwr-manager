package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestArchiveCodeRequired is the acceptance test for problem 029: archive.code is a real enforced
// public identity. It covers the migration constraints (NOT NULL / CHECK format / UNIQUE), the
// create+round-trip (code generated before insert, resolvable by code, appears in the slug),
// concurrent creation yielding distinct valid codes, and rollback (a failed create persists nothing).
func TestArchiveCodeRequired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	mkInsert := func(heading string) *entity.ArchiveInsert {
		return &entity.ArchiveInsert{
			Tag:         "t35",
			ThumbnailId: mediaID,
			Translations: []entity.ArchiveTranslation{
				{LanguageId: langID, Heading: heading},
			},
		}
	}

	// --- 1) migration constraints: NULL / bad-format / duplicate are all rejected by the DB ---
	// NOT NULL: an explicit NULL code fails regardless of sql_mode.
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES ('c', ?, NULL)`, mediaID)
	require.Error(t, err, "NULL code must be rejected (NOT NULL)")
	// CHECK: a code that is not AR+base36 is rejected.
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES ('c', ?, 'XX01')`, mediaID)
	require.Error(t, err, "malformed code must be rejected (CHECK)")
	// CHECK is explicitly case-sensitive even though the database collation is not.
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES ('c', ?, 'ar000c')`, mediaID)
	require.Error(t, err, "lowercase persisted code must be rejected (case-sensitive CHECK)")
	// UNIQUE: the same code twice collides.
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES ('c', ?, 'ARZZZ9')`, mediaID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES ('c', ?, 'ARZZZ9')`, mediaID)
	require.Error(t, err, "duplicate code must be rejected (UNIQUE)")
	_, _ = testDB.ExecContext(ctx, `DELETE FROM archive WHERE code = 'ARZZZ9'`)

	// --- 2) create + round-trip: code generated before insert, valid, resolvable, in the slug ---
	aid, err := s.Archive().AddArchive(ctx, mkInsert("Spring Drop"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM archive_translation WHERE archive_id = ?", aid)
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM archive WHERE id = ?", aid)
	})

	var persisted string
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT code FROM archive WHERE id = ?", aid).Scan(&persisted))
	require.NotEmpty(t, persisted, "code must be persisted (no NULL window)")
	require.True(t, entity.ValidArchiveCode(persisted), "persisted code must match the format contract: %q", persisted)

	full, err := s.Archive().GetArchiveByCode(ctx, persisted)
	require.NoError(t, err, "archive must resolve by its persisted code")
	require.Equal(t, aid, full.ArchiveList.Id)
	require.Equal(t, persisted, full.ArchiveList.Code)
	require.Contains(t, full.ArchiveList.Slug, persisted, "the public slug must embed the persisted code")
	// lower-case round-trip: the resolver upper-cases the tail.
	lower, err := s.Archive().GetArchiveByCode(ctx, strings.ToLower(persisted))
	require.NoError(t, err, "code lookup must be case-insensitive")
	require.Equal(t, aid, lower.ArchiveList.Id)

	// --- 3) concurrent creation: every archive gets a distinct, valid code ---
	const n = 8
	var wg sync.WaitGroup
	codes := make([]string, n)
	ids := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, e := s.Archive().AddArchive(ctx, mkInsert(fmt.Sprintf("Concurrent %d", i)))
			if e != nil {
				errs[i] = e
				return
			}
			ids[i] = id
			var c string
			errs[i] = testDB.QueryRowContext(ctx, "SELECT code FROM archive WHERE id = ?", id).Scan(&c)
			codes[i] = c
		}(i)
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, id := range ids {
			if id != 0 {
				_, _ = testDB.ExecContext(context.Background(), "DELETE FROM archive_translation WHERE archive_id = ?", id)
				_, _ = testDB.ExecContext(context.Background(), "DELETE FROM archive WHERE id = ?", id)
			}
		}
	})
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "concurrent create %d", i)
		require.True(t, entity.ValidArchiveCode(codes[i]), "concurrent code %q invalid", codes[i])
		require.False(t, seen[codes[i]], "duplicate code %q from concurrent creation", codes[i])
		seen[codes[i]] = true
	}

	// --- 4) rollback: a create that fails (bad thumbnail FK) persists neither archive nor code ---
	var before int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM archive").Scan(&before))
	badMedia := mediaID + 1_000_000 // no such media row -> thumbnail_id FK violation
	_, err = s.Archive().AddArchive(ctx, &entity.ArchiveInsert{
		Tag: "rollback", ThumbnailId: badMedia,
		Translations: []entity.ArchiveTranslation{{LanguageId: langID, Heading: "nope"}},
	})
	require.Error(t, err, "a create with an invalid thumbnail FK must fail")
	var after int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM archive").Scan(&after))
	require.Equal(t, before, after, "a failed create must roll back — no archive row persisted")
}
