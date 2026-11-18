package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/model"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatternSemanticSidecarV9TransitionAndColdReopen(t *testing.T) {
	skipUnlessLocalContainer(t)
	if os.Getenv("MYSQL_DATABASE") != "assembly_v9_sidecar_test" {
		t.Skip("requires a disposable assembly_v9_sidecar_test schema with performance_schema enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cfg := *testCfg
	cfg.Automigrate = false
	repository, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(repository.Close)
	cards, models := repository.TechCards(), repository.Models()
	cardID, sizeID, sizeToken := seedNativeCrossRowReviewV8(t, ctx, cards)

	// Both migration directions execute the real stable SQL CHECK.
	migrationBytes, err := os.ReadFile("sql/0478_pattern_semantic_sidecar_composite_v9_media.sql")
	require.NoError(t, err)
	migration, err := sqlparse.ParseMigration(strings.NewReader(string(migrationBytes)))
	require.NoError(t, err)
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	migrate := func(statements []string) error {
		for _, statement := range statements {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}
	require.NoError(t, migrate(migration.UpStatements))
	require.NoError(t, migrate(migration.UpStatements))

	card, err := cards.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	var row entity.SavedPatternRowIdentity
	for _, pattern := range card.Patterns {
		if pattern.LineKey == v8MainPattern {
			row = entity.SavedPatternRowIdentity{
				TechCardID: cardID, PatternLineKey: pattern.LineKey, ScopeKey: "main",
				SavedRowRevision: pattern.SavedRowRevision, CardLockVersion: card.LockVersion,
				PatternVersion: pattern.Version, SHA256: pattern.SHA256.String,
				URL: pattern.URL, SizeBytes: pattern.SizeBytes.Int64,
			}
		}
	}
	request := entity.SavedAssemblySnapshotRequest{Profile: entity.SavedAssemblySnapshotCompleteSourceV1, ExpectedRow: row}
	before, err := cards.GetTechCardSavedAssemblySnapshot(ctx, request)
	require.NoError(t, err)
	require.Nil(t, before.SemanticSidecar)
	request.ExpectedCompleteSourceSHA256 = before.CompleteSourceV1.SourceSHA256
	request.ExpectedServerAssemblyRevision = before.ServerAssemblyRevision

	v8 := nativeCrossRowReviewWriteFixtureV8(t, before, sizeID, sizeToken)
	addModel := func(label string) entity.NeutralPlacementModelAuthorityV1 {
		t.Helper()
		id, err := models.AddModel(ctx, &entity.ModelInsert{
			Name: label, Gender: sql.NullString{String: string(entity.Unisex), Valid: true},
			Measurements: []entity.ModelMeasurement{{Name: entity.BodyWaist, ValueMM: 740}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, models.DeleteModel(context.Background(), id)) })
		return readNativePlacementModelFixtureV9(t, ctx, repository, id)
	}
	neutral := addModel("native v9 measurement race")

	in := nativePlacementWriteFixtureV9(t, v8, neutral, v8.SidecarSHA256, 0)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, in)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	_, err = cards.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.ErrorIs(t, err, sql.ErrNoRows)

	prior, err := cards.PutTechCardPatternSemanticSidecar(ctx, v8)
	require.NoError(t, err)
	assertV8 := func() {
		t.Helper()
		got, err := cards.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
		require.NoError(t, err)
		require.Equal(t, prior, got)
	}
	wrongCAS := in
	otherSHA := strings.Repeat("f", 64)
	wrongCAS.ExpectedPreviousSidecarSHA256 = &otherSHA
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, wrongCAS)
	require.Error(t, err)
	assertV8()

	// Move only the other member while MAIN and the V8 payload stay unchanged.
	setPocketRevision := func(step int) {
		t.Helper()
		_, err := testDB.ExecContext(ctx, `UPDATE tech_card_size_pattern SET saved_row_revision = saved_row_revision + ?
			WHERE tech_card_id = ? AND line_key = ?`, step, cardID, v8PocketPattern)
		require.NoError(t, err)
	}
	setPocketRevision(1)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, in)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	assertV8()
	setPocketRevision(-1)

	// Put first reaches the existing neutral model lock, then sees changed
	// measurements with identical model.updated_at after the blocker commits.
	modelWriter, err := repository.TxBegin(ctx)
	require.NoError(t, err)
	defer modelWriter.TxRollback(ctx)
	var modelID int
	require.NoError(t, modelWriter.DB().GetContext(ctx, &modelID, "SELECT id FROM model WHERE id = ? FOR UPDATE", neutral.ModelID))
	modelRace := startNativePlacementWriteV9(ctx, cards, in)
	requireModelParentWait(t, ctx, neutral.ModelID)
	_, err = modelWriter.DB().ExecContext(ctx, "UPDATE model_measurement SET measurement_value_mm = 745 WHERE model_id = ?", neutral.ModelID)
	require.NoError(t, err)
	require.NoError(t, modelWriter.TxCommit(ctx))
	require.ErrorIs(t, <-modelRace, entity.ErrNeutralPlacementModelMoved)
	freshModel := readNativePlacementModelFixtureV9(t, ctx, repository, neutral.ModelID)
	require.Equal(t, neutral.ModelUpdatedAt, freshModel.ModelUpdatedAt)
	require.NotEqual(t, neutral.ModelPhysicalInputsSHA256, freshModel.ModelPhysicalInputsSHA256)
	assertV8()

	// Hold the existing first card lock while the ordinary model pose writer
	// wins. V9 must load the newly present pose after the card barrier releases.
	poseTarget := addModel("native v9 pose race")
	withPose := nativePlacementWriteFixtureV9(t, v8, poseTarget, v8.SidecarSHA256, 0)
	cardBarrier, err := repository.TxBegin(ctx)
	require.NoError(t, err)
	defer cardBarrier.TxRollback(ctx)
	var cardLock int
	require.NoError(t, cardBarrier.DB().GetContext(ctx, &cardLock, "SELECT lock_version FROM tech_card WHERE id = ? FOR UPDATE", cardID))
	poseRace := startNativePlacementWriteV9(ctx, cards, withPose)
	requireV9CardLockWait(t, ctx, cardID)
	poseGuard, err := entity.NewModelMannequinPoseRevisionGuard(nil)
	require.NoError(t, err)
	_, err = models.PutModelMannequinPose(ctx, poseTarget.ModelID, storeMannequinPoseFixture(), poseGuard, "v9-test-pose-winner")
	require.NoError(t, err)
	require.NoError(t, cardBarrier.TxCommit(ctx))
	require.ErrorIs(t, <-poseRace, entity.ErrNeutralPlacementModelMoved)
	assertV8()

	in = nativePlacementWriteFixtureV9(t, v8, freshModel, v8.SidecarSHA256, 0)
	ready := make(chan struct{})
	type result struct {
		stored entity.PatternSemanticSidecar
		err    error
	}
	writes := make(chan result, 2)
	for range 2 {
		go func() {
			<-ready
			stored, err := cards.PutTechCardPatternSemanticSidecar(ctx, in)
			writes <- result{stored, err}
		}()
	}
	close(ready)
	var persisted entity.PatternSemanticSidecar
	for range 2 {
		r := <-writes
		if r.err == nil {
			require.Empty(t, persisted.Payload)
			persisted = r.stored
		} else {
			require.ErrorIs(t, r.err, entity.ErrTechCardPatternSidecarBindingMoved)
		}
	}
	require.Equal(t, in.Payload, persisted.Payload)
	require.Equal(t, prior.PatternRowId, persisted.PatternRowId)
	require.Equal(t, prior.CardLockVersion, persisted.CardLockVersion)

	standalone, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer standalone.Close()
	reader := standalone.TechCards()
	bare, err := reader.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, persisted, bare)
	snapshot, err := reader.GetTechCardSavedAssemblySnapshot(ctx, request)
	require.NoError(t, err)
	require.Equal(t, persisted, *snapshot.SemanticSidecar)
	require.Equal(t, before.CompleteSourceV1.CanonicalJSON, snapshot.CompleteSourceV1.CanonicalJSON)
	require.Empty(t, snapshot.TopologySHA256)

	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, in)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved, "repeat uses the stale V8 expectation")
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, v8)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	legacy := legacySidecarWriteForNativeCrossRowV8(row)
	for _, expected := range []*string{nil, &persisted.SidecarSHA256} {
		legacy.ExpectedPreviousSidecarSHA256 = expected
		_, err = cards.PutTechCardPatternSemanticSidecar(ctx, legacy)
		require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	}

	forgedModel := nativePlacementWriteFixtureV9(t, v8, poseTarget, persisted.SidecarSHA256, 25)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, forgedModel)
	require.ErrorContains(t, err, "exact saved V8 review and neutral body")
	unchangedCenter := nativePlacementWriteFixtureV9(t, v8, freshModel, persisted.SidecarSHA256, 0)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, unchangedCenter)
	require.ErrorContains(t, err, "changed center")
	bare, err = reader.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, persisted, bare)

	edits := []entity.PatternSemanticSidecarWrite{
		nativePlacementWriteFixtureV9(t, v8, freshModel, persisted.SidecarSHA256, 25),
		nativePlacementWriteFixtureV9(t, v8, freshModel, persisted.SidecarSHA256, 40),
	}
	setPocketRevision(1)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, edits[0])
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	setPocketRevision(-1)
	bare, err = reader.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, persisted, bare)

	startEdit := make(chan struct{})
	for _, edit := range edits {
		go func() {
			<-startEdit
			stored, err := cards.PutTechCardPatternSemanticSidecar(ctx, edit)
			writes <- result{stored, err}
		}()
	}
	close(startEdit)
	var edited entity.PatternSemanticSidecar
	for range edits {
		r := <-writes
		if r.err == nil {
			require.Empty(t, edited.Payload)
			edited = r.stored
		} else {
			require.ErrorIs(t, r.err, entity.ErrTechCardPatternSidecarBindingMoved)
		}
	}
	require.Contains(t, []string{edits[0].SidecarSHA256, edits[1].SidecarSHA256}, edited.SidecarSHA256)
	require.Equal(t, persisted.PatternRowId, edited.PatternRowId)
	snapshot, err = reader.GetTechCardSavedAssemblySnapshot(ctx, request)
	require.NoError(t, err)
	require.Equal(t, edited, *snapshot.SemanticSidecar)
	require.Equal(t, before.CompleteSourceV1.CanonicalJSON, snapshot.CompleteSourceV1.CanonicalJSON)
	for _, edit := range edits {
		_, err := cards.PutTechCardPatternSemanticSidecar(ctx, edit)
		require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	}
	persisted = edited

	_, err = models.PutModelMannequinPose(ctx, freshModel.ModelID, storeMannequinPoseFixture(), poseGuard, "v9-test-post-save-pose")
	require.NoError(t, err)
	afterPose := nativePlacementWriteFixtureV9(t, v8, freshModel, edited.SidecarSHA256, 60)
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, afterPose)
	require.ErrorIs(t, err, entity.ErrNeutralPlacementModelMoved)
	_, err = reader.GetTechCardSavedAssemblySnapshot(ctx, request)
	require.ErrorIs(t, err, entity.ErrNeutralPlacementModelMoved)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	bare, err = reader.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, persisted, bare, "structural standalone read preserves history")

	require.ErrorContains(t, migrate(migration.DownStatements), "chk_pattern_semantic_sidecar_media_type")
	bare, err = reader.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, persisted, bare, "Down cannot remove the occupied V9 slot")
	// Remove only the disposable fixture so Down can restore the V8 allowlist.
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id = ?", persisted.PatternRowId)
	require.NoError(t, err)
	require.NoError(t, migrate(migration.DownStatements))
	_, err = cards.PutTechCardPatternSemanticSidecar(ctx, v8)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card_pattern_semantic_sidecar SET media_type=? WHERE pattern_row_id=?",
		entity.PatternProductCompositeSidecarV9MediaType, persisted.PatternRowId)
	require.ErrorContains(t, err, "chk_pattern_semantic_sidecar_media_type")
	require.NoError(t, migrate(migration.UpStatements))
}

func startNativePlacementWriteV9(ctx context.Context, cards dependency.TechCards, in entity.PatternSemanticSidecarWrite) <-chan error {
	outcome := make(chan error, 1)
	go func() {
		_, err := cards.PutTechCardPatternSemanticSidecar(ctx, in)
		outcome <- err
	}()
	return outcome
}

func requireV9CardLockWait(t *testing.T, ctx context.Context, cardID int) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var waiting int
		require.NoError(c, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM performance_schema.data_lock_waits w
			JOIN performance_schema.data_locks l ON BINARY l.ENGINE_LOCK_ID = BINARY w.BLOCKING_ENGINE_LOCK_ID
			WHERE BINARY l.OBJECT_SCHEMA = BINARY DATABASE() AND l.OBJECT_NAME = 'tech_card' AND l.INDEX_NAME = 'PRIMARY'
			  AND BINARY l.LOCK_DATA = BINARY CAST(? AS CHAR)`, cardID).Scan(&waiting))
		require.Positive(c, waiting)
	}, 5*time.Second, 20*time.Millisecond)
}

func readNativePlacementModelFixtureV9(t *testing.T, ctx context.Context, repo *MYSQLStore, id int) entity.NeutralPlacementModelAuthorityV1 {
	t.Helper()
	var current entity.NeutralPlacementModelAuthorityV1
	require.NoError(t, repo.readTx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		current, err = model.ReadNeutralPlacementModelAuthorityV1(ctx, rep.DB(), id)
		return err
	}))
	return current
}

func nativePlacementWriteFixtureV9(t *testing.T, v8 entity.PatternSemanticSidecarWrite, current entity.NeutralPlacementModelAuthorityV1, previous string, centerX float64) entity.PatternSemanticSidecarWrite {
	t.Helper()
	var inner struct {
		Bindings json.RawMessage `json:"bindings"`
		Review   struct {
			GridSHA256 string `json:"gridSha256"`
		} `json:"review"`
	}
	require.NoError(t, json.Unmarshal(v8.Payload, &inner))
	part := func(kind, media string, payload []byte) map[string]any {
		return map[string]any{"kind": kind, "mediaType": media, "byteLength": len(payload),
			"sha256": entity.PatternSemanticSidecarPayloadSHA256(payload), "base64": base64.StdEncoding.EncodeToString(payload)}
	}
	fakeWitness := func(value string) string { return strings.Repeat(value, 64) }
	positionsDigest := entity.PatternSemanticSidecarPayloadSHA256([]byte(strconv.FormatFloat(centerX, 'f', -1, 64)))
	piece := func(id string, originX, z float64) map[string]any {
		return map[string]any{"pieceId": id, "originMm": []float64{originX, 10}, "centerM": []float64{centerX / 1000, 1, z}, "cos": 1, "sin": 0}
	}
	world, err := json.Marshal(map[string]any{
		"schemaVersion": "tech-card-complete-source-native-world-placement/1", "reviewSha256": v8.SidecarSHA256,
		"body": map[string]any{
			"modelId": current.ModelID, "modelUpdatedAt": current.ModelUpdatedAt, "modelPhysicalInputsSha256": current.ModelPhysicalInputsSHA256,
			"pose":     map[string]any{"kind": current.Pose.Kind, "canonicalSha256": current.Pose.CanonicalSHA256},
			"observed": map[string]any{"bodyPositionsFloat32LeSha256": fakeWitness("1"), "bodyTrianglesUint32LeSha256": fakeWitness("2")},
		},
		"placement": map[string]any{
			"centerMm": []float64{centerX, 1000, 0}, "depthOffsetsMm": []int{4, 0},
			"directedCut": map[string]any{
				"gridSha256": inner.Review.GridSHA256, "carrierMaterialPositionsFloat64LeSha256": fakeWitness("3"),
				"carrierTrianglesUint32LeSha256": fakeWitness("4"), "positionsInvMassFloat32LeSha256": positionsDigest,
				"pieces": []any{piece(v8MainPiece, 5, 0.004), piece(v8PocketPiece, 6, 0)},
			},
		},
	})
	require.NoError(t, err)
	out := v8
	out.MediaType = entity.PatternProductCompositeSidecarV9MediaType
	out.ExpectedPreviousSidecarSHA256 = &previous
	out.Payload, err = json.Marshal(map[string]any{
		"schemaVersion": "pattern-import-dxf-product-composite-sidecar-document/9", "mediaType": out.MediaType,
		"profile": "saved-full-assembly/native-cross-row-stitch-world-placement/1", "encoding": "canonical-json-utf8",
		"bindings": inner.Bindings, "writeCas": map[string]any{"expectedPreviousSidecarSha256": previous},
		"components": []any{
			part("native-cross-row-review-v8", v8.MediaType, v8.Payload),
			part("native-cross-row-world-placement-v1", "application/vnd.grbpwr.pattern-dxf-native-world-placement.v1+json", world),
		},
	})
	require.NoError(t, err)
	out.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(out.Payload)
	out.ExpectedPostSaveReceiptSHA256 = entity.PatternProductNativeCrossRowPlacementReceiptV9(out.Payload)
	return out
}
