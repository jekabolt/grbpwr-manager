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

// TestPatternSemanticSidecarCompositeV6MigrationReplay proves on disposable MySQL that 0475 is
// replay-safe, preserves V5, refuses to discard V6 on rollback, and restores the exact V5 CHECK
// after an explicit V6 removal.
func TestPatternSemanticSidecarCompositeV6MigrationReplay(t *testing.T) {
	skipUnlessLocalContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	body, err := os.ReadFile("sql/0475_pattern_semantic_sidecar_composite_v6_media.sql")
	require.NoError(t, err)
	migration, err := sqlparse.ParseMigration(strings.NewReader(string(body)))
	require.NoError(t, err)
	require.NotEmpty(t, migration.UpStatements)
	require.NotEmpty(t, migration.DownStatements)

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
	t.Cleanup(func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer restoreCancel()
		restoreConn, restoreErr := testDB.Conn(restoreCtx)
		if restoreErr != nil {
			t.Errorf("open connection to restore 0475 Up: %v", restoreErr)
			return
		}
		defer func() {
			if closeErr := restoreConn.Close(); closeErr != nil {
				t.Errorf("close 0475 restore connection: %v", closeErr)
			}
		}()
		for index, statement := range migration.UpStatements {
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
		bomV5     = "01JABCDEFGHJKMNPQRSTVWXYZ0"
		bomV6     = "01JABCDEFGHJKMNPQRSTVWXYZ1"
		patternV5 = "01JABCDEFGHJKMNPQRSTVWXYZ2"
		patternV6 = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	)
	card := &entity.TechCardInsert{
		Name: "Composite V6 migration replay", StyleNumber: present("V6-MIGRATION-CHECK"),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, SizeIds: []int{sizeID},
		BomItems: []entity.TechCardBomItem{
			{LineKey: bomV5, Section: entity.BomSectionFabric, Name: "V5 row"},
			{LineKey: bomV6, Section: entity.BomSectionFabric, Name: "V6 row"},
		},
		Patterns: []entity.TechCardSizePattern{
			{SizeId: sizeID, LineKey: patternV5, BomLineKey: present(bomV5), URL: "https://cdn.example/v5.dxf"},
			{SizeId: sizeID, LineKey: patternV6, BomLineKey: present(bomV6), URL: "https://cdn.example/v6.dxf"},
		},
	}
	cfg := *testCfg
	cfg.Automigrate = false
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()
	techCardID, err := store.TechCards().AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", techCardID)
	})

	rows, err := conn.QueryContext(ctx, `SELECT id FROM tech_card_size_pattern WHERE tech_card_id = ? ORDER BY id`, techCardID)
	require.NoError(t, err)
	var patternIDs []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		patternIDs = append(patternIDs, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, patternIDs, 2)

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

	execStatements("0475 Up replay 1", migration.UpStatements)
	execStatements("0475 Up replay 2", migration.UpStatements)
	require.NoError(t, insertSidecar(patternIDs[0], entity.PatternProductCompositeSidecarV5MediaType, "preserve-v5"))
	require.NoError(t, insertSidecar(patternIDs[1], entity.PatternProductCompositeSidecarV6MediaType, "preserve-v6"))

	var downErr error
	for _, statement := range migration.DownStatements {
		if _, downErr = conn.ExecContext(ctx, statement); downErr != nil {
			break
		}
	}
	require.Error(t, downErr, "Down must fail closed while V6 rows exist")
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV5MediaType))
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV6MediaType))

	_, err = conn.ExecContext(ctx, `DELETE FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id = ?`, patternIDs[1])
	require.NoError(t, err)
	execStatements("0475 Down after explicit removal", migration.DownStatements)
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV5MediaType), "rollback must preserve V5")
	require.Error(t, insertSidecar(patternIDs[1], entity.PatternProductCompositeSidecarV6MediaType, "rejected-v6"),
		"restored 0474 CHECK must reject Composite V6")

	// Simulate the half-applied state left by the superseded two-DDL rollback implementation. Down
	// must first restore the permissive V6 CHECK, then atomically narrow it; it may never leave the
	// table unconstrained when replayed.
	_, err = conn.ExecContext(ctx, `ALTER TABLE tech_card_pattern_semantic_sidecar
		DROP CHECK chk_pattern_semantic_sidecar_media_type`)
	require.NoError(t, err)
	execStatements("0475 Down recovers absent CHECK", migration.DownStatements)
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV5MediaType))
	require.Error(t, insertSidecar(patternIDs[1], entity.PatternProductCompositeSidecarV6MediaType, "rejected-after-recovery"),
		"half-apply recovery must finish with the V5 CHECK installed")

	execStatements("0475 Up after rollback", migration.UpStatements)
	execStatements("0475 Up replay after rollback", migration.UpStatements)
	require.NoError(t, insertSidecar(patternIDs[1], entity.PatternProductCompositeSidecarV6MediaType, "v6-readmitted"))
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV5MediaType))
	require.Equal(t, 1, countMedia(entity.PatternProductCompositeSidecarV6MediaType))
}
