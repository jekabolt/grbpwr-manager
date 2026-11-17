package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/require"
)

// TestPatternSemanticSidecarCompositeV4V5MigrationReplay exercises 0474 against the disposable real
// MySQL used by this package. It proves both deploy directions at the database boundary rather than
// relying only on Go's media allowlist: Up admits V4/V5 and survives replay; Down refuses to destroy
// either format, then restores 0472 only after an explicit removal; replayed Up admits both again.
func TestPatternSemanticSidecarCompositeV4V5MigrationReplay(t *testing.T) {
	skipUnlessLocalContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	body, err := os.ReadFile("sql/0474_pattern_semantic_sidecar_composite_v4_v5_media.sql")
	require.NoError(t, err)
	migration, err := sqlparse.ParseMigration(strings.NewReader(string(body)))
	require.NoError(t, err)
	require.NotEmpty(t, migration.UpStatements)
	require.NotEmpty(t, migration.DownStatements)
	v6Body, err := os.ReadFile("sql/0475_pattern_semantic_sidecar_composite_v6_media.sql")
	require.NoError(t, err)
	v6Migration, err := sqlparse.ParseMigration(strings.NewReader(string(v6Body)))
	require.NoError(t, err)
	require.NotEmpty(t, v6Migration.UpStatements)

	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	execStatements := func(label string, statements []string) {
		t.Helper()
		for index, statement := range statements {
			_, execErr := conn.ExecContext(ctx, statement)
			require.NoError(t, execErr, "%s statement %d", label, index+1)
		}
	}
	// Always hand the rest of the integration package the latest schema, even when an assertion
	// after Down fails. Both Up migrations are replay-safe; 0475 must run after this historical test
	// restores 0474 or gorp would still record 0475 while the live CHECK admitted only through V5.
	t.Cleanup(func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer restoreCancel()
		restoreConn, restoreErr := testDB.Conn(restoreCtx)
		if restoreErr != nil {
			t.Errorf("open connection to restore 0474 Up: %v", restoreErr)
			return
		}
		defer func() {
			if closeErr := restoreConn.Close(); closeErr != nil {
				t.Errorf("close restore connection: %v", closeErr)
			}
		}()
		for index, statement := range migration.UpStatements {
			if _, restoreErr = restoreConn.ExecContext(restoreCtx, statement); restoreErr != nil {
				t.Errorf("restore 0474 Up statement %d: %v", index+1, restoreErr)
				return
			}
		}
		for index, statement := range v6Migration.UpStatements {
			if _, restoreErr = restoreConn.ExecContext(restoreCtx, statement); restoreErr != nil {
				t.Errorf("restore 0475 Up statement %d: %v", index+1, restoreErr)
				return
			}
		}
	})

	var sizeID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	const (
		bomFront      = "01JABCDEFGHJKMNPQRSTVWXYZ0"
		bomBack       = "01JABCDEFGHJKMNPQRSTVWXYZ1"
		bomSleeve     = "01JABCDEFGHJKMNPQRSTVWXYZ4"
		patternFront  = "01JABCDEFGHJKMNPQRSTVWXYZ2"
		patternBack   = "01JABCDEFGHJKMNPQRSTVWXYZ3"
		patternSleeve = "01JABCDEFGHJKMNPQRSTVWXYZ5"
	)
	card := &entity.TechCardInsert{
		Name: "Composite V5 migration replay", StyleNumber: present("V5-MIGRATION-CHECK"),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, SizeIds: []int{sizeID},
		BomItems: []entity.TechCardBomItem{
			{LineKey: bomFront, Section: entity.BomSectionFabric, Name: "V5 front"},
			{LineKey: bomBack, Section: entity.BomSectionFabric, Name: "V5 back"},
			{LineKey: bomSleeve, Section: entity.BomSectionFabric, Name: "V5 sleeve"},
		},
		Patterns: []entity.TechCardSizePattern{
			{SizeId: sizeID, LineKey: patternFront, BomLineKey: present(bomFront), URL: "https://cdn.example/v5-front.dxf"},
			{SizeId: sizeID, LineKey: patternBack, BomLineKey: present(bomBack), URL: "https://cdn.example/v5-back.dxf"},
			{SizeId: sizeID, LineKey: patternSleeve, BomLineKey: present(bomSleeve), URL: "https://cdn.example/v5-sleeve.dxf"},
		},
	}
	cfg := *testCfg
	cfg.Automigrate = false // TestMain already migrated; this test deliberately replays only 0474.
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()
	techCardID, err := store.TechCards().AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", techCardID)
	})

	rows, err := conn.QueryContext(ctx, `SELECT id FROM tech_card_size_pattern
		WHERE tech_card_id = ? ORDER BY id`, techCardID)
	require.NoError(t, err)
	var patternIDs []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		patternIDs = append(patternIDs, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, patternIDs, 3)

	const (
		mediaV3 = "application/vnd.grbpwr.pattern-dxf-product-composite.v3+json"
		mediaV4 = "application/vnd.grbpwr.pattern-dxf-product-composite.v4+json"
		mediaV5 = "application/vnd.grbpwr.pattern-dxf-product-composite.v5+json"
	)
	insertSidecar := func(patternID int, mediaType, marker string) error {
		payload := []byte(fmt.Sprintf(`{"marker":%q}`, marker))
		_, insertErr := conn.ExecContext(ctx, `INSERT INTO tech_card_pattern_semantic_sidecar
			(pattern_row_id, card_lock_version, saved_row_revision, dxf_sha256, pattern_version,
			 sidecar_sha256, media_type, byte_length, payload, post_save_receipt_sha256, created_by)
			VALUES (?, 0, 1, ?, 1, ?, ?, ?, ?, ?, 'migration-replay')`,
			patternID, strings.Repeat("a", 64), strings.Repeat("b", 64), mediaType, len(payload), payload,
			strings.Repeat("c", 64))
		return insertErr
	}
	countMedia := func(mediaType string) int {
		t.Helper()
		var count int
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM tech_card_pattern_semantic_sidecar s
			JOIN tech_card_size_pattern p ON p.id = s.pattern_row_id
			WHERE p.tech_card_id = ? AND s.media_type = ?`, techCardID, mediaType).Scan(&count))
		return count
	}

	require.NoError(t, insertSidecar(patternIDs[0], mediaV3, "keep-v3"))
	require.NoError(t, insertSidecar(patternIDs[1], mediaV4, "preserve-v4"))
	require.NoError(t, insertSidecar(patternIDs[2], mediaV5, "preserve-v5"))
	execStatements("0474 Up replay 1", migration.UpStatements)
	execStatements("0474 Up replay 2", migration.UpStatements)
	require.Equal(t, 1, countMedia(mediaV3))
	require.Equal(t, 1, countMedia(mediaV4))
	require.Equal(t, 1, countMedia(mediaV5))

	var downErr error
	for _, statement := range migration.DownStatements {
		if _, downErr = conn.ExecContext(ctx, statement); downErr != nil {
			break
		}
	}
	require.Error(t, downErr, "Down must fail closed while V4/V5 rows exist")
	require.Equal(t, 1, countMedia(mediaV4), "failed rollback must preserve Composite V4")
	require.Equal(t, 1, countMedia(mediaV5), "failed rollback must preserve Composite V5 annotations")

	_, err = conn.ExecContext(ctx, `DELETE FROM tech_card_pattern_semantic_sidecar
		WHERE pattern_row_id IN (?, ?)`, patternIDs[1], patternIDs[2])
	require.NoError(t, err, "operator-approved removal is explicit in this migration test")
	execStatements("0474 Down after explicit removal", migration.DownStatements)
	require.Equal(t, 1, countMedia(mediaV3), "rollback must preserve Composite V3 admitted by 0472")
	require.Error(t, insertSidecar(patternIDs[1], mediaV4, "rejected-v4-by-v3-check"),
		"0472 CHECK restored by Down must reject Composite V4")
	require.Error(t, insertSidecar(patternIDs[2], mediaV5, "rejected-v5-by-v3-check"),
		"0472 CHECK restored by Down must reject Composite V5")

	var oldCheck int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
		  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
		  AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
		  AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
		  AND cc.CHECK_CLAUSE LIKE '%pattern-dxf-product-composite.v3+json%'
		  AND cc.CHECK_CLAUSE NOT LIKE '%pattern-dxf-product-composite.v4+json%'
		  AND cc.CHECK_CLAUSE NOT LIKE '%pattern-dxf-product-composite.v5+json%'`).Scan(&oldCheck))
	require.Equal(t, 1, oldCheck, "Down must restore the exact pre-0474 CHECK vocabulary")

	execStatements("0474 Up after rollback", migration.UpStatements)
	execStatements("0474 Up replay after rollback", migration.UpStatements)
	require.NoError(t, insertSidecar(patternIDs[1], mediaV4, "v4-admitted-after-reapply"))
	require.NoError(t, insertSidecar(patternIDs[2], mediaV5, "v5-admitted-after-reapply"))
	require.Equal(t, 1, countMedia(mediaV3))
	require.Equal(t, 1, countMedia(mediaV4))
	require.Equal(t, 1, countMedia(mediaV5))
}
