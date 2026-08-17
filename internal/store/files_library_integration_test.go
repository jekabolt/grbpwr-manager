package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// filesGuard keeps this test off the configured (production) database, exactly as
// the other store integration tests do: without CI set, TestMain builds its DSN
// from config.toml and the cleanup DROPS every table it finds.
func filesGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

// insertLibraryFileFixture inserts one library_file row and registers its removal
// (library_file_topic cascades with it).
func insertLibraryFileFixture(ctx context.Context, t *testing.T, name string, size int64, uploader string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO library_file
		(object_key, file_name, content_type, size_bytes, sha256, uploaded_by)
		VALUES (CONCAT('files-library/test-', UUID_SHORT()), ?, 'application/pdf', ?, '', ?)`,
		name, size, uploader)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM library_file WHERE id = ?`, id)
	})
	return int(id)
}

// insertFileTopicFixture creates a uniquely named topic and registers its removal.
func insertFileTopicFixture(ctx context.Context, t *testing.T, prefix string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO file_topic (name) VALUES (CONCAT(?, '-', UUID_SHORT()))`, prefix)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM library_file_topic WHERE topic_id = ?`, id)
		_, _ = testDB.Exec(`DELETE FROM file_topic WHERE id = ?`, id)
	})
	return int(id)
}

func linkFileTopicFixture(ctx context.Context, t *testing.T, fileID int, topicIDs ...int) {
	t.Helper()
	for _, tid := range topicIDs {
		_, err := testDB.ExecContext(ctx,
			`INSERT IGNORE INTO library_file_topic (file_id, topic_id) VALUES (?, ?)`, fileID, tid)
		require.NoError(t, err)
	}
}

func libraryFileIDs(files []entity.LibraryFile) []int {
	out := make([]int, 0, len(files))
	for _, f := range files {
		out = append(out, f.Id)
	}
	return out
}

// TestLibraryFilesTopicIntersection is the acceptance test of the canvas filter:
// several selected chips must NARROW the grid, not widen it.
//
// It has to be an integration test, because the whole difference lives in the
// generated SQL. `topic_id IN (a, b)` and one EXISTS per topic are both perfectly
// valid queries that both return rows — and the wrong one is not visibly broken,
// it just quietly answers a different question than the label above the chips.
func TestLibraryFilesTopicIntersection(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	// A marker topic carried by every fixture file scopes the assertions to this
	// test's own rows: the library is shared, and other tests upload into it.
	marker := insertFileTopicFixture(ctx, t, "test-marker")
	topicA := insertFileTopicFixture(ctx, t, "test-a")
	topicB := insertFileTopicFixture(ctx, t, "test-b")
	topicC := insertFileTopicFixture(ctx, t, "test-c")

	zebra := insertLibraryFileFixture(ctx, t, "zebra.pdf", 300, "pasha")
	alpha := insertLibraryFileFixture(ctx, t, "alpha.pdf", 100, "kirill")
	mid := insertLibraryFileFixture(ctx, t, "mid.pdf", 200, "pasha")
	lonely := insertLibraryFileFixture(ctx, t, "lonely.pdf", 50, "pasha")

	linkFileTopicFixture(ctx, t, zebra, marker, topicA, topicB)
	linkFileTopicFixture(ctx, t, alpha, marker, topicA)
	linkFileTopicFixture(ctx, t, mid, marker)
	// lonely carries nothing at all — the «Разобрать» bucket.

	list := func(f entity.LibraryFileListFilter) ([]int, int) {
		t.Helper()
		files, total, err := s.Files().ListFiles(ctx, f)
		require.NoError(t, err)
		return libraryFileIDs(files), total
	}

	t.Run("one topic returns everything carrying it", func(t *testing.T) {
		ids, total := list(entity.LibraryFileListFilter{TopicIds: []int{marker, topicA}})
		require.ElementsMatch(t, []int{zebra, alpha}, ids)
		require.Equal(t, 2, total)
	})

	t.Run("adding a topic narrows, never widens", func(t *testing.T) {
		ids, total := list(entity.LibraryFileListFilter{TopicIds: []int{marker, topicA, topicB}})
		require.Equal(t, []int{zebra}, ids)
		require.Equal(t, 1, total)
	})

	t.Run("a topic nobody carries empties the result", func(t *testing.T) {
		ids, total := list(entity.LibraryFileListFilter{TopicIds: []int{marker, topicC}})
		require.Empty(t, ids)
		require.Zero(t, total)
	})

	t.Run("topic_ids wins over the legacy single topic_id", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{TopicId: topicC, TopicIds: []int{marker, topicA}})
		require.ElementsMatch(t, []int{zebra, alpha}, ids)
	})

	t.Run("untopiced wins over topic_ids", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{Untopiced: true, TopicIds: []int{marker, topicA}})
		require.Contains(t, ids, lonely)
		require.NotContains(t, ids, zebra)
	})

	t.Run("sort by name is A to Z regardless of order_factor", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{
			TopicIds:    []int{marker},
			SortBy:      entity.LibraryFileSortName,
			OrderFactor: entity.Descending,
		})
		require.Equal(t, []int{alpha, mid, zebra}, ids)
	})

	t.Run("sort by size is largest first", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{
			TopicIds: []int{marker},
			SortBy:   entity.LibraryFileSortSize,
		})
		require.Equal(t, []int{zebra, mid, alpha}, ids)
	})

	t.Run("search matches the uploader", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{TopicIds: []int{marker}, Search: "pasha"})
		require.ElementsMatch(t, []int{zebra, mid}, ids)
	})

	t.Run("search still matches the file name and the topic name", func(t *testing.T) {
		ids, _ := list(entity.LibraryFileListFilter{TopicIds: []int{marker}, Search: "zebra"})
		require.Equal(t, []int{zebra}, ids)

		name := topicNameOf(ctx, t, topicA)
		ids, _ = list(entity.LibraryFileListFilter{TopicIds: []int{marker}, Search: name})
		require.ElementsMatch(t, []int{zebra, alpha}, ids)
	})

	t.Run("too many topics is refused rather than run", func(t *testing.T) {
		many := make([]int, entity.MaxLibraryTopicFilters+1)
		for i := range many {
			many[i] = marker
		}
		_, _, err := s.Files().ListFiles(ctx, entity.LibraryFileListFilter{TopicIds: many})
		require.Error(t, err)
	})
}

// TestLibraryFileTopicsMergeAndAssign covers the two vocabulary writes: folding a
// duplicated label into the one that survives, and bulk-labelling a selection.
func TestLibraryFileTopicsMergeAndAssign(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	source := insertFileTopicFixture(ctx, t, "test-merge-src")
	target := insertFileTopicFixture(ctx, t, "test-merge-dst")

	both := insertLibraryFileFixture(ctx, t, "both.pdf", 10, "pasha")
	only := insertLibraryFileFixture(ctx, t, "only.pdf", 20, "pasha")
	linkFileTopicFixture(ctx, t, both, source, target)
	linkFileTopicFixture(ctx, t, only, source)

	t.Run("self merge is refused", func(t *testing.T) {
		_, err := s.Files().MergeTopics(ctx, source, source)
		require.Error(t, err)
	})

	t.Run("a missing topic is not found", func(t *testing.T) {
		_, err := s.Files().MergeTopics(ctx, source, 2147483600)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("merge moves links without duplicating and kills the source", func(t *testing.T) {
		moved, err := s.Files().MergeTopics(ctx, source, target)
		require.NoError(t, err)
		// Only `only` gained the target: `both` already carried it, and a file that
		// did not move must not be reported as moved.
		require.Equal(t, 1, moved)

		files, total, err := s.Files().ListFiles(ctx, entity.LibraryFileListFilter{TopicIds: []int{target}})
		require.NoError(t, err)
		require.Equal(t, 2, total)
		require.ElementsMatch(t, []int{both, only}, libraryFileIDs(files))

		var srcLinks, srcTopics int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ?`, source).Scan(&srcLinks))
		require.Zero(t, srcLinks)
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_topic WHERE id = ?`, source).Scan(&srcTopics))
		require.Zero(t, srcTopics)

		// Every file that carried the source still carries exactly one copy of the
		// target: a merge that duplicated links would still "work" and only show up
		// later, as doubled counts on the topics screen.
		var links int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ? AND file_id = ?`,
			target, both).Scan(&links))
		require.Equal(t, 1, links)
	})

	t.Run("assign adds without replacing and is idempotent", func(t *testing.T) {
		extra := insertFileTopicFixture(ctx, t, "test-assign")
		newName := fmt.Sprintf("test-assign-new-%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = testDB.Exec(`DELETE FROM library_file_topic WHERE topic_id IN
				(SELECT id FROM file_topic WHERE name = ?)`, newName)
			_, _ = testDB.Exec(`DELETE FROM file_topic WHERE name = ?`, newName)
		})

		assigned, err := s.Files().AssignTopics(ctx, []int{both, only}, []int{extra}, []string{newName})
		require.NoError(t, err)
		require.Equal(t, 4, assigned) // two files × (extra + the freshly created one)

		// The target topic the merge left on both files is still there: assign
		// ADDS, and a replacing implementation would have wiped it here.
		files, _, err := s.Files().ListFiles(ctx, entity.LibraryFileListFilter{TopicIds: []int{target, extra}})
		require.NoError(t, err)
		require.ElementsMatch(t, []int{both, only}, libraryFileIDs(files))

		again, err := s.Files().AssignTopics(ctx, []int{both, only}, []int{extra}, []string{newName})
		require.NoError(t, err)
		require.Zero(t, again)
	})

	t.Run("assigning a topic that does not exist is not found", func(t *testing.T) {
		_, err := s.Files().AssignTopics(ctx, []int{both}, []int{2147483600}, nil)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})
}

// TestLibraryFileSetPreview covers the preview-replacement endpoint's store half:
// the caller must learn which object it may now delete.
func TestLibraryFileSetPreview(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	id := insertLibraryFileFixture(ctx, t, "preview.pdf", 10, "pasha")

	previous, err := s.Files().SetFilePreview(ctx, id, "files-library/previews/first.png")
	require.NoError(t, err)
	require.Empty(t, previous, "a file that had no preview leaves nothing to clean up")

	previous, err = s.Files().SetFilePreview(ctx, id, "files-library/previews/second.png")
	require.NoError(t, err)
	require.Equal(t, "files-library/previews/first.png", previous)

	stored, err := s.Files().GetFileById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "files-library/previews/second.png", stored.PreviewObjectKey.String)

	_, err = s.Files().SetFilePreview(ctx, 2147483600, "files-library/previews/third.png")
	require.True(t, errors.Is(err, sql.ErrNoRows))
}

func topicNameOf(ctx context.Context, t *testing.T, id int) string {
	t.Helper()
	var name string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT name FROM file_topic WHERE id = ?`, id).Scan(&name))
	return name
}
