package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// chk_tcp_cut_symmetry (0275) has to close the vocabulary against a value that arrives WITHOUT the
// application — a raw UPDATE, a DBA fix, an import. tech_card_piece is utf8mb4_0900_ai_ci and REGEXP
// inherits the column's collation, so the pattern alone accepts 'MIRRORED' and 'Fold' as readily as
// 'mirrored'; only 'mirror' is refused. That gap is not cosmetic, because a stored 'MIRRORED' is
// invisible and self-contradictory in three places at once:
//
//  1. dto.PieceCutSymmetryToPb does not find it in the mapping table and returns UNKNOWN, so the card
//     and the cut list both read «не размечено»;
//  2. constructionProjection sees Valid=true and appends the RAW string, so the CONSTRUCTION digest
//     moves and every approved sign-off on that card reads «changed since sign-off» with nothing on
//     screen to explain why;
//  3. the next save from a tab that omits the field carries the value into upsertTechCardPieces,
//     where ValidatePieceCutSymmetry rejects it — the card becomes unsavable, citing a field the
//     operator cannot see.
//
// So the case rule lives in the schema, next to the vocabulary it belongs to, and this test pins it
// against the REAL migrated table rather than against a string search of the migration.
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestPieceCutSymmetryDBCheckIsCaseSensitive(t *testing.T) {
	// Only run in CI (which points MYSQL_* at a container) or when the DSN explicitly targets a local
	// container. Otherwise skip — a bare local `go test ./internal/store/...` uses config.toml's prod
	// DSN and this suite's TestMain drops all tables on cleanup.
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	styleID := seedSpineStyle(ctx, t, "cutsym-case")
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO tech_card_piece (tech_card_id, name, line_key, pieces_per_garment) VALUES (?, ?, ?, 2)`,
		styleID, "полочка", "01CUTSYMCASE00000000000P1")
	require.NoError(t, err)
	pieceID, err := res.LastInsertId()
	require.NoError(t, err)

	set := func(v any) error {
		_, err := testDB.ExecContext(ctx,
			`UPDATE tech_card_piece SET cut_symmetry = ? WHERE id = ?`, v, pieceID)
		return err
	}
	// checkViolation reports whether err is MySQL's ER_CHECK_CONSTRAINT_VIOLATED naming the constraint
	// under test — a plain "some error" assertion would also pass on a typo'd column name.
	requireRejectedBy := func(t *testing.T, constraint string, err error) {
		t.Helper()
		require.Error(t, err)
		var me *mysql.MySQLError
		require.ErrorAs(t, err, &me)
		require.EqualValues(t, 3819, me.Number, "want ER_CHECK_CONSTRAINT_VIOLATED, got %v", me)
		require.Contains(t, me.Message, constraint)
	}

	// The whole point of the fix: a value that is in the vocabulary but in the wrong case.
	for _, v := range []string{"MIRRORED", "Fold", "Identical", "mIrRoReD", "FOLD"} {
		t.Run("rejects "+v, func(t *testing.T) {
			requireRejectedBy(t, "chk_tcp_cut_symmetry", set(v))
		})
	}
	// And the pre-existing half of the constraint keeps working.
	for _, v := range []string{"mirror", "identicals", "on_fold", ""} {
		t.Run("rejects out-of-vocabulary "+v, func(t *testing.T) {
			requireRejectedBy(t, "chk_tcp_cut_symmetry", set(v))
		})
	}
	// The legal set still passes, or the constraint would be closed by being closed to everything.
	for _, v := range []entity.TechCardPieceCutSymmetry{
		entity.PieceCutSymmetryIdentical, entity.PieceCutSymmetryMirrored, entity.PieceCutSymmetryFold,
	} {
		t.Run("accepts "+string(v), func(t *testing.T) {
			require.NoError(t, set(string(v)))
		})
	}
	t.Run("accepts NULL", func(t *testing.T) { require.NoError(t, set(nil)) })

	// The Go leg agrees on its own: entity.ValidTechCardPieceCutSymmetries is keyed on the exact
	// lowercase strings, so a wrong-case value never reaches MySQL through the app either. Asserted
	// here, next to the DB rule, because the two are one decision.
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	require.Error(t, entity.ValidatePieceCutSymmetry("pieces[0].cut_symmetry", ns("MIRRORED"), 2))
	require.NoError(t, entity.ValidatePieceCutSymmetry("pieces[0].cut_symmetry", ns("mirrored"), 2))

	// The evenness constraint is a SEPARATE rule and must still fire on its own terms.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_piece SET cut_symmetry = 'mirrored', pieces_per_garment = 3 WHERE id = ?`, pieceID)
	requireRejectedBy(t, "chk_tcp_mirrored_needs_even_count", err)
}
