package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/store/content"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertTestMedia creates a throwaway media row and returns its id.
func insertTestMedia(t *testing.T, tag string) int {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO media (
			full_size, full_size_width, full_size_height,
			compressed, compressed_width, compressed_height,
			thumbnail, thumbnail_width, thumbnail_height
		) VALUES (?, 100, 100, ?, 50, 50, ?, 20, 20)`,
		"https://cdn.test/"+tag+"-full.jpg",
		"https://cdn.test/"+tag+"-compressed.jpg",
		"https://cdn.test/"+tag+"-thumb.jpg",
	)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

// TestMediaUsage covers the question the media library could not previously answer: is this file
// referenced, and from where. It exercises the three cases the library actually renders —
// a media item held from two *different* entity kinds, one held from a single place, and one that
// is genuinely free — in a single batched call, which is how a library page will ask.
//
// It is also, incidentally, a syntax check over the whole reference registry: GetMediaUsage
// compiles all seventeen media-referencing columns into one UNION ALL, so a typo'd column or a
// broken join in *any* branch fails this test even though the fixtures only touch model and archive.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates). Cleans up every
// row it inserts, in FK order.
func TestMediaUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// mediaShared is pointed at from two different kinds; mediaSingle from one; mediaFree from none.
	mediaShared := insertTestMedia(t, "shared-"+suffix)
	mediaSingle := insertTestMedia(t, "single-"+suffix)
	mediaFree := insertTestMedia(t, "free-"+suffix)

	// The default language is what the registry resolves labels through, so the fixture writes its
	// archive heading against that same row rather than assuming language id 1.
	var defaultLangID int
	require.NoError(t,
		testDB.QueryRowContext(ctx, `SELECT id FROM language WHERE is_default = 1 LIMIT 1`).Scan(&defaultLangID),
		"migrations must seed a default language; the usage labels are derived through it")

	modelName := "Usage Test Model " + suffix
	modelRes, err := testDB.ExecContext(ctx,
		`INSERT INTO model (name, thumbnail_id) VALUES (?, ?)`, modelName, mediaShared)
	require.NoError(t, err)
	modelID64, err := modelRes.LastInsertId()
	require.NoError(t, err)
	modelID := int(modelID64)

	_, err = testDB.ExecContext(ctx,
		`INSERT INTO model_media (model_id, media_id, display_order) VALUES (?, ?, 1)`,
		modelID, mediaSingle)
	require.NoError(t, err)

	// chk_archive_code_format (0148) demands ^AR[0-9A-Z]{1,10}$.
	archiveCode := "AR" + suffix[len(suffix)-8:]
	archiveHeading := "Usage Test Archive " + suffix
	archiveRes, err := testDB.ExecContext(ctx,
		`INSERT INTO archive (tag, thumbnail_id, code) VALUES (?, ?, ?)`,
		"usage-test", mediaShared, archiveCode)
	require.NoError(t, err)
	archiveID64, err := archiveRes.LastInsertId()
	require.NoError(t, err)
	archiveID := int(archiveID64)

	_, err = testDB.ExecContext(ctx,
		`INSERT INTO archive_translation (archive_id, language_id, heading) VALUES (?, ?, ?)`,
		archiveID, defaultLangID, archiveHeading)
	require.NoError(t, err)

	defer func() {
		bg := context.Background()
		_, _ = testDB.ExecContext(bg, `DELETE FROM model_media WHERE model_id = ?`, modelID)
		_, _ = testDB.ExecContext(bg, `DELETE FROM model WHERE id = ?`, modelID)
		_, _ = testDB.ExecContext(bg, `DELETE FROM archive_translation WHERE archive_id = ?`, archiveID)
		_, _ = testDB.ExecContext(bg, `DELETE FROM archive WHERE id = ?`, archiveID)
		for _, id := range []int{mediaShared, mediaSingle, mediaFree} {
			_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, id)
		}
	}()

	// One call for the whole batch — the point of the RPC.
	usage, err := s.Media().GetMediaUsage(ctx, []int{mediaShared, mediaSingle, mediaFree})
	require.NoError(t, err)

	// mediaShared: two refs, two different kinds, deterministic order (archive before model).
	shared := usage[mediaShared]
	require.Len(t, shared, 2, "media referenced from an archive and a model must report both")

	assert.Equal(t, "archive", shared[0].Kind)
	assert.Equal(t, archiveID, shared[0].EntityId, "the operator must be sent to the archive, not to a join row")
	assert.Equal(t, archiveHeading, shared[0].Label, "label must come from the default-language heading")
	assert.Equal(t, "thumbnail", shared[0].Slot)

	assert.Equal(t, "model", shared[1].Kind)
	assert.Equal(t, modelID, shared[1].EntityId)
	assert.Equal(t, modelName, shared[1].Label)
	assert.Equal(t, "thumbnail", shared[1].Slot)

	// mediaSingle: one ref, and the slot distinguishes a gallery photo from the thumbnail above.
	single := usage[mediaSingle]
	require.Len(t, single, 1)
	assert.Equal(t, "model", single[0].Kind)
	assert.Equal(t, modelID, single[0].EntityId)
	assert.Equal(t, modelName, single[0].Label, "a join-table ref must still carry the owning model's name")
	assert.Equal(t, "photo", single[0].Slot)

	// mediaFree: nobody holds it, so it is safe to delete.
	assert.Empty(t, usage[mediaFree], "an unreferenced media item must report no usage")

	// An empty batch must not hit the database at all.
	empty, err := s.Media().GetMediaUsage(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestMediaUsageRegistryCoversSchema fails when a new foreign key into media(id) is added without a
// matching registry row.
//
// This is the failure mode that makes the whole feature dangerous rather than merely incomplete: a
// reference the registry does not know about makes the library report a referenced file as free to
// delete, and the operator only finds out when the delete is refused — which is exactly the
// situation GetMediaUsage exists to end. The registry cannot be generated from the schema (a
// generic walk yields "product_media #417" instead of a product name), so the schema audits it here.
func TestMediaUsageRegistryCoversSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := testDB.QueryContext(ctx, `
		SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME)
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND REFERENCED_TABLE_NAME = 'media'`)
	require.NoError(t, err)
	defer rows.Close()

	inSchema := map[string]bool{}
	for rows.Next() {
		var target string
		require.NoError(t, rows.Scan(&target))
		inSchema[target] = true
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, inSchema, "information_schema must see the media foreign keys")

	inRegistry := map[string]bool{}
	for _, target := range content.MediaRefRegistryTargets() {
		assert.False(t, inRegistry[target], "registry lists %s twice", target)
		inRegistry[target] = true
	}

	for target := range inSchema {
		assert.True(t, inRegistry[target],
			"%s references media(id) but is missing from the registry in "+
				"internal/store/content/media_usage.go — media using it would be reported as unused", target)
	}
	for target := range inRegistry {
		assert.True(t, inSchema[target],
			"registry lists %s, which no longer references media(id) — the UNION branch is dead", target)
	}
}

// TestMediaUsageColumnsAreIndexed asserts that every media-referencing column can be filtered by
// index.
//
// GetMediaUsage issues one WHERE <col> IN (...) per referencing table, so a column without an
// index where it comes *first* turns a single library page into a full scan of that table — and
// the tables involved (product_media, tech_card_media, task_media) are among the ones that grow
// fastest. InnoDB creates such an index for every foreign key unless a suitable one already
// exists, which is why no migration was needed here; this test is what keeps that true, since a
// future column could be added with an explicit composite index that puts the media column second
// and thus never serves this predicate.
//
// Verified against the freshly migrated schema, not against a deployed database, so it reflects
// what the migration history actually produces.
func TestMediaUsageColumnsAreIndexed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := testDB.QueryContext(ctx, `
		SELECT k.TABLE_NAME, k.COLUMN_NAME, COALESCE(idx.INDEX_NAME, '')
		FROM information_schema.KEY_COLUMN_USAGE k
		LEFT JOIN (
			SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, MIN(INDEX_NAME) AS INDEX_NAME
			FROM information_schema.STATISTICS
			WHERE SEQ_IN_INDEX = 1
			GROUP BY TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME
		) idx ON idx.TABLE_SCHEMA = k.TABLE_SCHEMA
			AND idx.TABLE_NAME = k.TABLE_NAME
			AND idx.COLUMN_NAME = k.COLUMN_NAME
		WHERE k.TABLE_SCHEMA = DATABASE() AND k.REFERENCED_TABLE_NAME = 'media'`)
	require.NoError(t, err)
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var table, column, index string
		require.NoError(t, rows.Scan(&table, &column, &index))
		checked++
		assert.NotEmpty(t, index,
			"%s.%s references media(id) but is not the leading column of any index; "+
				"GetMediaUsage would full-scan %s on every media library page — add a migration",
			table, column, table)
		t.Logf("%s.%s -> %s", table, column, index)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, len(content.MediaRefRegistryTargets()), checked,
		"every registry target must have been index-checked")
}
