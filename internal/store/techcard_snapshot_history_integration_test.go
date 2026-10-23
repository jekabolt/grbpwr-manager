package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// These tests exercise TestMain's destructive integration database. Keep the same local-container
// guard as the other tech-card integration tests in this package.
func requireSnapshotHistoryIntegrationDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

func addSnapshotHistoryCard(t *testing.T, ctx context.Context, s *MYSQLStore, name string) int {
	t.Helper()
	id, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: name, Stage: entity.TechCardStageIdea,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		Purpose:         entity.TechCardPurposeSellable,
		Pieces: []entity.TechCardPiece{
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ0", Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", Name: "back", PiecesPerGarment: 1, Grainline: "lengthwise"},
		},
		CreatedBy: "snapshot.fixture@example.com", UpdatedBy: "snapshot.fixture@example.com",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id)
	})
	return id
}

// snapshotHistoryJSON follows the production snapshot shape: the resolved common.TechCard read
// model, with an explicit empty seams array so restore can distinguish "none" from an old snapshot
// written before seams were captured.
func snapshotHistoryJSON(t *testing.T, card *entity.TechCard) string {
	t.Helper()
	blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}))
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(blob, &top))
	if _, ok := top["seams"]; !ok {
		top["seams"] = json.RawMessage(`[]`)
	}
	blob, err = json.Marshal(top)
	require.NoError(t, err)
	return string(blob)
}

func saveSnapshotHistoryCard(
	t *testing.T,
	ctx context.Context,
	s *MYSQLStore,
	id int,
	name, sessionID, author string,
	changedPaths []string,
) (*entity.TechCard, string) {
	t.Helper()
	before, err := s.TechCards().GetTechCardById(ctx, id)
	require.NoError(t, err)
	preimage := snapshotHistoryJSON(t, before)

	write := before.TechCardInsert
	write.Name = name
	write.UpdatedBy = author
	write.SnapshotReason = ""
	write.SnapshotSaveSessionId = sessionID
	write.SnapshotChangedPaths = append([]string(nil), changedPaths...)
	write.SnapshotPinned = false
	write.BuildSnapshotFromLockedCard = func(locked *entity.TechCard) (string, error) {
		return snapshotHistoryJSON(t, locked), nil
	}
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, id, &write, before.LockVersion))

	after, err := s.TechCards().GetTechCardById(ctx, id)
	require.NoError(t, err)
	return after, preimage
}

// A seam write deliberately leaves lock_version unchanged. This widens the production race into a
// deterministic sequence: the card payload is built, a seam commits, then the card save starts with
// the still-valid expected version. The autosave must freeze the seam from the locked transactional
// reload, not the seam-less handler read; otherwise restoring the row would erase that newer review.
func TestTechCardSnapshotPreimageReloadsVersionlessSeams(t *testing.T) {
	requireSnapshotHistoryIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	rig := newRTRig(t, ctx)
	T := rig.store.TechCards()
	id := addSnapshotHistoryCard(t, ctx, rig.store, "Snapshot Seam Initial")

	before, err := T.GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.Empty(t, before.Seams)
	stalePreimage := snapshotHistoryJSON(t, before)

	anchor := func(piece string) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: piece,
			Samples: []entity.TechCardSeamSample{
				{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4},
			},
			PerimShare: 0.25, LenMm: 120, Notches: 1, TurnDeg: -2,
		}
	}
	const seamKey = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	_, written, err := T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: id,
		By:         "seam.reviewer@example.com",
		Seams: []entity.TechCardSeamInput{{
			SeamKey: seamKey, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual,
			SideA: []entity.TechCardSeamAnchor{anchor("01JABCDEFGHJKMNPQRSTVWXYZ0")},
			SideB: []entity.TechCardSeamAnchor{anchor("01JABCDEFGHJKMNPQRSTVWXYZ1")},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, written)
	afterSeam, err := T.GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, before.LockVersion, afterSeam.LockVersion, "seam writes do not bump the card version")

	write := before.TechCardInsert
	write.Name = "Snapshot Seam Saved"
	write.UpdatedBy = "card.editor@example.com"
	write.SnapshotSaveSessionId = "33333333-3333-4333-8333-333333333333"
	write.SnapshotChangedPaths = []string{"overview"}
	write.BuildSnapshotFromLockedCard = func(locked *entity.TechCard) (string, error) {
		require.Len(t, locked.Seams, 1, "the builder must receive the post-seam state under the card lock")
		return snapshotHistoryJSON(t, locked), nil
	}
	require.NoError(t, T.UpdateTechCard(ctx, id, &write, before.LockVersion))

	rows, err := T.ListTechCardSnapshots(ctx, id, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	snapshot, err := T.GetTechCardSnapshot(ctx, id, rows[0].Id)
	require.NoError(t, err)
	require.NotEqual(t, stalePreimage, snapshot.Snapshot)
	var frozen pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(snapshot.Snapshot), &frozen))
	require.Len(t, frozen.GetSeams(), 1)
	require.Equal(t, seamKey, frozen.GetSeams()[0].GetSeamKey())
}

func snapshotIDs(rows []entity.TechCardSnapshot) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Id)
	}
	return ids
}

func snapshotBodyName(t *testing.T, raw string) string {
	t.Helper()
	var card pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(raw), &card))
	require.NotNil(t, card.GetTechCard())
	return card.GetTechCard().GetName()
}

func TestTechCardSnapshotHistoryAutosaveListAndRestore(t *testing.T) {
	requireSnapshotHistoryIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	rig := newRTRig(t, ctx)
	s := rig.store
	T := s.TechCards()
	id := addSnapshotHistoryCard(t, ctx, s, "Snapshot History Initial")

	const (
		sessionS = "11111111-1111-4111-8111-111111111111"
		sessionT = "22222222-2222-4222-8222-222222222222"
	)
	bucketOne := time.Date(2026, time.October, 24, 12, 1, 0, 0, time.UTC)
	s.ts = bucketOne

	afterFirst, firstPreimage := saveSnapshotHistoryCard(t, ctx, s, id,
		"Snapshot History First", sessionS, "first.owner@example.com", []string{"overview", "fit"})
	_, secondPreimage := saveSnapshotHistoryCard(t, ctx, s, id,
		"Snapshot History Second", sessionS, "second.owner@example.com", []string{"fit", "pieces", " overview "})
	require.NotEqual(t, firstPreimage, secondPreimage, "the second save supplied a different pre-image")

	rows, err := T.ListTechCardSnapshots(ctx, id, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1, "two saves in one session bucket must coalesce")
	firstRow := rows[0]
	require.Equal(t, "autosave", firstRow.Reason)
	require.Equal(t, afterFirst.LockVersion-1, firstRow.LockVersion)
	require.Equal(t, []string{"overview", "fit", "pieces"}, firstRow.ChangedPaths)
	require.True(t, firstRow.CreatedBy.Valid)
	require.Equal(t, "first.owner@example.com", firstRow.CreatedBy.String,
		"coalescing must keep the first save's author with its immutable pre-image")
	require.False(t, firstRow.Pinned)

	firstSnapshot, err := T.GetTechCardSnapshot(ctx, id, firstRow.Id)
	require.NoError(t, err)
	require.JSONEq(t, firstPreimage, firstSnapshot.Snapshot)
	require.Equal(t, "Snapshot History Initial", snapshotBodyName(t, firstSnapshot.Snapshot),
		"the bucket row must keep the state replaced by the first save")

	var storedSession string
	var storedBucket time.Time
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT save_session_id, bucket_start
		FROM tech_card_snapshot
		WHERE id = ?`, firstRow.Id).Scan(&storedSession, &storedBucket))
	require.Equal(t, sessionS, storedSession)
	require.True(t, storedBucket.Equal(bucketOne.Truncate(5*time.Minute)))

	// Supplying the same pre-image metadata without a session keeps the legacy no-history path.
	afterLegacy, _ := saveSnapshotHistoryCard(t, ctx, s, id,
		"Snapshot History Legacy", "", "legacy.owner@example.com", []string{"notes"})
	rows, err = T.ListTechCardSnapshots(ctx, id, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a save without save_session_id must not append an autosave row")

	// A different session in the same bucket gets its own immutable row.
	afterOtherSession, _ := saveSnapshotHistoryCard(t, ctx, s, id,
		"Snapshot History Other Session", sessionT, "other.owner@example.com", []string{"materials"})
	rows, err = T.ListTechCardSnapshots(ctx, id, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	otherSessionID := rows[0].Id
	require.Greater(t, otherSessionID, firstRow.Id)
	require.Equal(t, afterLegacy.LockVersion, rows[0].LockVersion)

	// The same session crosses the real five-minute store-clock bucket and therefore gets a new row.
	s.ts = bucketOne.Add(5 * time.Minute)
	afterNextBucket, _ := saveSnapshotHistoryCard(t, ctx, s, id,
		"Snapshot History Next Bucket", sessionS, "next.owner@example.com", []string{"construction"})
	rows, err = T.ListTechCardSnapshots(ctx, id, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	nextBucketID := rows[0].Id
	require.Greater(t, nextBucketID, otherSessionID)
	require.Equal(t, afterOtherSession.LockVersion, rows[0].LockVersion)

	// Restore the oldest autosave through the production restore handler over this real store. The
	// handler feeds the restored document through UpdateTechCard and writes an undoable pinned row.
	preRestoreName := afterNextBucket.Name
	preRestoreLock := afterNextBucket.LockVersion
	restoredLabel := "restored " + firstSnapshot.CreatedAt.UTC().Format("02.01 15:04")
	resp, err := rig.srv.RestoreTechCardSnapshot(rig.adminCtx("restore.owner@example.com"),
		&pb_admin.RestoreTechCardSnapshotRequest{
			TechCardId: int32(id), SnapshotId: firstRow.Id,
			ExpectedLockVersion: int32(preRestoreLock),
		})
	require.NoError(t, err)
	require.Equal(t, "Snapshot History Initial", resp.GetTechCard().GetTechCard().GetName())
	require.EqualValues(t, preRestoreLock+1, resp.GetTechCard().GetLockVersion())

	restored, err := T.GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "Snapshot History Initial", restored.Name)
	require.Equal(t, preRestoreLock+1, restored.LockVersion)

	allRows, err := T.ListTechCardSnapshots(ctx, id, 0, 100)
	require.NoError(t, err)
	require.Len(t, allRows, 4)
	restoreRow := allRows[0]
	require.Equal(t, fmt.Sprintf("restore:%d", firstRow.Id), restoreRow.Reason)
	require.Equal(t, preRestoreLock, restoreRow.LockVersion)
	require.Equal(t, []string{restoredLabel}, restoreRow.ChangedPaths)
	require.True(t, restoreRow.Pinned)
	require.True(t, restoreRow.CreatedBy.Valid)
	require.Equal(t, "restore.owner@example.com", restoreRow.CreatedBy.String)
	require.Equal(t, []int64{restoreRow.Id, nextBucketID, otherSessionID, firstRow.Id}, snapshotIDs(allRows),
		"history must be newest-first")

	for _, row := range allRows {
		require.True(t, row.CreatedBy.Valid, "snapshot %d must expose its author", row.Id)
		require.NotEmpty(t, row.CreatedBy.String, "snapshot %d must expose its author", row.Id)
		require.NotEmpty(t, row.ChangedPaths, "snapshot %d must expose its changes", row.Id)
		require.False(t, row.CreatedAt.IsZero(), "snapshot %d must expose its timestamp", row.Id)
	}
	require.False(t, allRows[1].Pinned)
	require.False(t, allRows[2].Pinned)
	require.False(t, allRows[3].Pinned)

	restoreSnapshot, err := T.GetTechCardSnapshot(ctx, id, restoreRow.Id)
	require.NoError(t, err)
	require.Equal(t, preRestoreName, snapshotBodyName(t, restoreSnapshot.Snapshot),
		"the pinned restore row must make the restore itself undoable")

	pageOne, err := T.ListTechCardSnapshots(ctx, id, 0, 2)
	require.NoError(t, err)
	require.Len(t, pageOne, 2)
	pageTwo, err := T.ListTechCardSnapshots(ctx, id, pageOne[len(pageOne)-1].Id, 2)
	require.NoError(t, err)
	require.Len(t, pageTwo, 2)
	require.Equal(t, snapshotIDs(allRows), append(snapshotIDs(pageOne), snapshotIDs(pageTwo)...),
		"before_snapshot_id must continue strictly after the requested page")
}

func TestTechCardSnapshotHistoryRetention(t *testing.T) {
	requireSnapshotHistoryIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	id := addSnapshotHistoryCard(t, ctx, s, "Snapshot Retention")

	const oldAutosaveCount = 105
	oldBase := time.Now().UTC().AddDate(0, 0, -200).Truncate(time.Second)
	oldIDs := make([]int64, 0, oldAutosaveCount)
	for i := 0; i < oldAutosaveCount; i++ {
		createdAt := oldBase.Add(time.Duration(i) * time.Second)
		result, insertErr := testDB.ExecContext(ctx, `
			INSERT INTO tech_card_snapshot
				(tech_card_id, lock_version, reason, snapshot, created_by,
				 save_session_id, bucket_start, changed_paths, pinned, created_at)
			VALUES (?, 0, 'autosave', ?, ?, ?, ?, ?, FALSE, ?)`,
			id, `{}`, "retention.seed@example.com", fmt.Sprintf("retention-%03d", i),
			createdAt.Truncate(5*time.Minute), `["seed"]`, createdAt)
		require.NoError(t, insertErr)
		snapshotID, insertErr := result.LastInsertId()
		require.NoError(t, insertErr)
		oldIDs = append(oldIDs, snapshotID)
	}

	pinnedIDs := make([]int64, 0, 2)
	for i := 0; i < 2; i++ {
		createdAt := oldBase.Add(time.Duration(i) * time.Minute)
		result, insertErr := testDB.ExecContext(ctx, `
			INSERT INTO tech_card_snapshot
				(tech_card_id, lock_version, reason, snapshot, created_by,
				 save_session_id, bucket_start, changed_paths, pinned, created_at)
			VALUES (?, 0, 'autosave', ?, ?, ?, ?, ?, TRUE, ?)`,
			id, `{}`, "retention.pinned@example.com", fmt.Sprintf("pinned-%03d", i),
			createdAt.Truncate(5*time.Minute), `["pinned"]`, createdAt)
		require.NoError(t, insertErr)
		snapshotID, insertErr := result.LastInsertId()
		require.NoError(t, insertErr)
		pinnedIDs = append(pinnedIDs, snapshotID)
	}

	// The oldest row is outside the newest-100 window. Its pattern-history reference is therefore
	// the only reason it survives the age/count prune.
	result, err := testDB.ExecContext(ctx, `
		INSERT INTO tech_card_size_pattern_history
			(tech_card_id, url, reason, snapshot_id)
		VALUES (?, ?, 'retention-reference', ?)`,
		id, "https://cdn.snapshot.test/retained-oldest.dxf", oldIDs[0])
	require.NoError(t, err)
	historyID, err := result.LastInsertId()
	require.NoError(t, err)

	var seeded int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_snapshot WHERE tech_card_id = ?", id).Scan(&seeded))
	require.Equal(t, oldAutosaveCount+len(pinnedIDs), seeded)

	// Any real snapshot append invokes retention. Use a pinned explicit checkpoint so the trigger
	// itself neither enters nor disturbs the newest-100 unpinned-autosave window.
	before, err := s.TechCards().GetTechCardById(ctx, id)
	require.NoError(t, err)
	write := before.TechCardInsert
	write.Name = "Snapshot Retention Pruned"
	write.UpdatedBy = "retention.trigger@example.com"
	write.SnapshotReason = "retention-trigger"
	write.SnapshotJSON = snapshotHistoryJSON(t, before)
	write.SnapshotChangedPaths = []string{"retention"}
	write.SnapshotPinned = true
	write.SnapshotLockVersion = before.LockVersion
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, id, &write, before.LockVersion))

	var unpinnedAutosaves, pinnedAutosaves, triggerRows, total int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_snapshot
		WHERE tech_card_id = ? AND reason = 'autosave' AND pinned = FALSE`, id).Scan(&unpinnedAutosaves))
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_snapshot
		WHERE tech_card_id = ? AND reason = 'autosave' AND pinned = TRUE`, id).Scan(&pinnedAutosaves))
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_snapshot
		WHERE tech_card_id = ? AND reason = 'retention-trigger' AND pinned = TRUE`, id).Scan(&triggerRows))
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_snapshot WHERE tech_card_id = ?", id).Scan(&total))
	require.Equal(t, 101, unpinnedAutosaves, "newest 100 plus the referenced oldest row must survive")
	require.Equal(t, 2, pinnedAutosaves, "pinned autosaves are outside retention")
	require.Equal(t, 1, triggerRows)
	require.Equal(t, 104, total, "exactly four old, unpinned, unreferenced rows should be pruned")

	snapshotExists := func(snapshotID int64) bool {
		var count int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_snapshot WHERE tech_card_id = ? AND id = ?", id, snapshotID).Scan(&count))
		return count == 1
	}
	require.True(t, snapshotExists(oldIDs[0]), "pattern history must protect an otherwise-prunable row")
	for _, snapshotID := range oldIDs[1:5] {
		require.False(t, snapshotExists(snapshotID), "old unpinned snapshot %d should be pruned", snapshotID)
	}
	require.True(t, snapshotExists(oldIDs[5]), "the first row in the newest-100 window must survive")
	require.True(t, snapshotExists(oldIDs[len(oldIDs)-1]))
	for _, snapshotID := range pinnedIDs {
		require.True(t, snapshotExists(snapshotID), "pinned snapshot %d must survive", snapshotID)
	}

	var retainedNewest, historyRefs int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_snapshot
		WHERE tech_card_id = ? AND pinned = FALSE AND reason = 'autosave'
		  AND id BETWEEN ? AND ?`, id, oldIDs[5], oldIDs[len(oldIDs)-1]).Scan(&retainedNewest))
	require.Equal(t, 100, retainedNewest)
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_size_pattern_history
		WHERE id = ? AND tech_card_id = ? AND snapshot_id = ?`, historyID, id, oldIDs[0]).Scan(&historyRefs))
	require.Equal(t, 1, historyRefs)
}
