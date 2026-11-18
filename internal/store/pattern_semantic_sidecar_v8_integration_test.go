package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/require"
)

const (
	v8MainPattern   = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	v8PocketPattern = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	v8MainPiece     = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	v8PocketPiece   = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	v8Stitch        = "01JABCDEFGHJKMNPQRSTVWXYZ4"
	v8Pose          = "01JABCDEFGHJKMNPQRSTVWXYZ5"
)

func TestPatternSemanticSidecarV8FirstCASAndCompleteReopen(t *testing.T) {
	skipUnlessLocalContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	migrationBytes, err := os.ReadFile("sql/0477_pattern_semantic_sidecar_composite_v8_media.sql")
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
	// Earlier media replay tests may leave their historical fixture CHECK in place.
	require.NoError(t, migrate(migration.UpStatements))
	require.NoError(t, migrate(migration.UpStatements))

	cfg := *testCfg
	cfg.Automigrate = false
	repository, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer repository.Close()
	techCards := repository.TechCards()
	cardID, sizeID, sizeToken := seedNativeCrossRowReviewV8(t, ctx, techCards)

	capture := func() entity.SavedAssemblySnapshot {
		current, err := techCards.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, current.Patterns, 2)
		var row entity.SavedPatternRowIdentity
		for _, p := range current.Patterns {
			if p.LineKey == v8MainPattern {
				row = entity.SavedPatternRowIdentity{
					TechCardID: cardID, ScopeKey: "main", PatternLineKey: p.LineKey,
					SavedRowRevision: p.SavedRowRevision, CardLockVersion: current.LockVersion,
					URL: p.URL, SizeBytes: p.SizeBytes.Int64, SHA256: p.SHA256.String, PatternVersion: p.Version,
				}
			}
		}
		require.Equal(t, v8MainPattern, row.PatternLineKey)
		result, err := techCards.GetTechCardSavedAssemblySnapshot(ctx, entity.SavedAssemblySnapshotRequest{
			ExpectedRow: row, Profile: entity.SavedAssemblySnapshotCompleteSourceV1,
		})
		require.NoError(t, err)
		require.Len(t, result.CompleteSourceV1.Rows, 2)
		require.Len(t, result.CompleteSourceV1.OwnedSeams, 2)
		return result
	}
	before := capture()
	require.Nil(t, before.SemanticSidecar)
	request := nativeCrossRowReviewWriteFixtureV8(t, before, sizeID, sizeToken)
	mutatePocketRevision := func() {
		t.Helper()
		// Change only the secondary row in a local SQL fixture, without a parent lock/clock
		// increment. This forces the writer to prove the member seal independently of MAIN.
		_, err := testDB.ExecContext(ctx, `UPDATE tech_card_size_pattern
			SET saved_row_revision = saved_row_revision + 1 WHERE tech_card_id = ? AND line_key = ?`,
			cardID, v8PocketPattern)
		require.NoError(t, err)
	}
	mutatePocketRevision()
	_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, request)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	_, err = techCards.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.ErrorIs(t, err, sql.ErrNoRows)

	current := capture()
	require.Equal(t, before.Row, current.Row)
	require.Equal(t, before.ServerAssemblyRevision, current.ServerAssemblyRevision)
	require.NotEqual(t, before.CompleteSourceV1.SourceSHA256, current.CompleteSourceV1.SourceSHA256)
	request = nativeCrossRowReviewWriteFixtureV8(t, current, sizeID, sizeToken)

	// The complete seal also pins AO content independently of the card lock and storage row.
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card_operation SET note = 'edited in local fixture'
		WHERE tech_card_id = ?`, cardID)
	require.NoError(t, err)
	_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, request)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	current = capture()
	request = nativeCrossRowReviewWriteFixtureV8(t, current, sizeID, sizeToken)

	old := legacySidecarWriteForNativeCrossRowV8(current.Row)
	legacyStored, err := techCards.PutTechCardPatternSemanticSidecar(ctx, old)
	require.NoError(t, err)
	_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, request)
	require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved, "first V8 may not replace any prior media")
	unchanged, err := techCards.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, legacyStored, unchanged)
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id = ?", legacyStored.PatternRowId)
	require.NoError(t, err)

	// Both requests reach the existing store owner with an absent-slot CAS. The card and prior
	// locks must serialize them, so exactly one writes and the other sees the existing carrier.
	start := make(chan struct{})
	results := make(chan error, 2)
	var racers sync.WaitGroup
	for range 2 {
		racers.Go(func() {
			<-start
			_, err := techCards.PutTechCardPatternSemanticSidecar(ctx, request)
			results <- err
		})
	}
	close(start)
	racers.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		} else {
			t.Logf("absent-slot CAS refused: %v", err)
			require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
		}
	}
	require.Equal(t, 1, successes)

	plain, err := repository.TechCards().GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, request.SidecarSHA256, plain.SidecarSHA256)
	require.Equal(t, request.Payload, plain.Payload)
	require.Equal(t, request.ExpectedPostSaveReceiptSHA256, plain.PostSaveReceiptSHA256)
	require.Equal(t, entity.PatternProductCompositeSidecarV8MediaType, plain.MediaType)

	require.ErrorContains(t, migrate(migration.DownStatements), "chk_pattern_semantic_sidecar_media_type", "narrowing must leave the V8 review intact")
	require.NoError(t, migrate(migration.UpStatements))

	readRequest := entity.SavedAssemblySnapshotRequest{
		ExpectedRow: current.Row, Profile: entity.SavedAssemblySnapshotCompleteSourceV1,
		ExpectedCompleteSourceSHA256:   current.CompleteSourceV1.SourceSHA256,
		ExpectedServerAssemblyRevision: current.ServerAssemblyRevision,
	}
	reader, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer reader.Close()
	reopened, err := reader.TechCards().GetTechCardSavedAssemblySnapshot(ctx, readRequest)
	require.NoError(t, err)
	require.NotNil(t, reopened.SemanticSidecar)
	require.Equal(t, plain.Payload, reopened.SemanticSidecar.Payload)
	require.Equal(t, current.CompleteSourceV1.CanonicalJSON, reopened.CompleteSourceV1.CanonicalJSON)
	require.Zero(t, reopened.SelectedOperationNumber)
	require.Empty(t, reopened.SelectedSeams)
	require.Empty(t, reopened.TopologySHA256)

	older := legacySidecarWriteForNativeCrossRowV8(current.Row)
	for _, prior := range []*string{nil, &plain.SidecarSHA256} {
		older.ExpectedPreviousSidecarSHA256 = prior
		_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, older)
		require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
	}
	sameMediaUpdate := request
	sameMediaUpdate.ExpectedPreviousSidecarSHA256 = &plain.SidecarSHA256
	_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, sameMediaUpdate)
	require.Error(t, err)
	preserved, err := reader.TechCards().GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, plain, preserved)

	mutatePocketRevision()
	// Bare GET still supplies immutable stored bytes for the dual readback, while Complete Source
	// refuses them even if the client did not submit the old full seal.
	rawStale, err := reader.TechCards().GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
	require.NoError(t, err)
	require.Equal(t, plain.Payload, rawStale.Payload)
	_, err = reader.TechCards().GetTechCardSavedAssemblySnapshot(ctx, readRequest)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	readRequest.ExpectedCompleteSourceSHA256 = ""
	_, err = reader.TechCards().GetTechCardSavedAssemblySnapshot(ctx, readRequest)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	// Down can narrow the allowlist once the fixture's V8 record is explicitly removed.
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id = ?", plain.PatternRowId)
	require.NoError(t, err)
	require.NoError(t, migrate(migration.DownStatements))
	require.NoError(t, migrate(migration.UpStatements))

}

func seedNativeCrossRowReviewV8(t *testing.T, ctx context.Context, techCards dependency.TechCards) (int, int, string) {
	t.Helper()
	var sizeID int
	var sizeName string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id, name FROM size WHERE name <> '' ORDER BY id LIMIT 1`).Scan(&sizeID, &sizeName))
	sizeToken, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(sizeName)), "_")
	require.NotEmpty(t, sizeToken)
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	pattern := func(scope, key, digest string) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			LineKey: key, FabricPurpose: present(scope), SizeId: sizeID, URL: "https://cdn.example/" + scope + ".dxf",
			Filename: present(scope + ".dxf"), SHA256: present(strings.Repeat(digest, 64)),
			SizeBytes: sql.NullInt64{Int64: 2048, Valid: true},
		}
	}
	card := &entity.TechCardInsert{
		Name: "Native cross-row review V8", StyleNumber: present("CROSS-ROW-V8"),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, SizeIds: []int{sizeID},
		BomItems: []entity.TechCardBomItem{
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ6", Name: "Shell", Section: entity.BomSectionFabric, Purpose: present("main")},
			{LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ7", Name: "Pocket shell", Section: entity.BomSectionFabric, Purpose: present("pocketing")},
		},
		Patterns: []entity.TechCardSizePattern{
			pattern("main", v8MainPattern, "a"),
			pattern("pocketing", v8PocketPattern, "b"),
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: v8MainPiece, Name: "FRONT", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: v8PocketPiece, Name: "POCKET", PiecesPerGarment: 1, Grainline: "lengthwise"},
		},
		PieceDxfAliasesSet: true,
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{
			{FabricPurpose: "main", BlockName: "FRONT", PieceLineKey: v8MainPiece},
			{FabricPurpose: "pocketing", BlockName: "POCKET", PieceLineKey: v8PocketPiece},
		},
		Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 170, Valid: true},
			OperationType:   entity.OpTypeMachine, Zone: entity.ZoneOuter,
			Work: present("join_lockstitch"), MachineType: present("lockstitch"),
			InputKeys: []string{v8MainPiece, v8PocketPiece},
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputPiece, Key: v8MainPiece},
				{Kind: entity.AssemblyInputPiece, Key: v8PocketPiece},
			},
		}},
	}
	cardID, err := techCards.AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := testDB.ExecContext(context.Background(), "DELETE FROM tech_card_operation WHERE tech_card_id = ?", cardID)
		require.NoError(t, err)
		_, err = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
		require.NoError(t, err)
	})

	// Use the normal seam owner so the persisted fingerprint and ownership are real. The size
	// stays unset to exercise the same saved-seam shape encountered in the archived operation.
	anchor := func(piece string) []entity.TechCardSeamAnchor {
		return []entity.TechCardSeamAnchor{{
			PieceLineKey: piece, EdgeHint: piece + "#1+2", ContourSig: "abcdef0123456789",
			LenMm: 100, PerimShare: .25, Samples: []entity.TechCardSeamSample{
				{U: 0}, {U: .25}, {U: .5}, {U: .75}, {U: 1},
			},
		}}
	}
	storedCard, err := techCards.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, storedCard.Operations, 1)
	op := storedCard.Operations[0]
	require.Equal(t, []string{v8MainPiece, v8PocketPiece}, op.InputKeys)
	number := int(op.OperationNumber.Int32)
	poseA := anchor(v8MainPiece)
	poseA[0].Placement = &entity.TechCardSeamPlacement{XMm: 7, YMm: 4}
	change, err := techCards.ChangeTechCardSeams(ctx, entity.TechCardSeamsChange{
		TechCardId: cardID, ExpectedLockVersion: &storedCard.LockVersion, By: "legacy-source-test",
		Upsert: []entity.TechCardSeamInput{
			{SeamKey: v8Stitch, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
				Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual,
				SideA: anchor(v8MainPiece), SideB: anchor(v8PocketPiece)},
			{SeamKey: v8Pose, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindPose,
				Direction: entity.TechCardSeamDirectionUnknown, Source: entity.TechCardSeamSourceManual,
				SideA: poseA, SideB: anchor(v8PocketPiece)},
		},
		Link: []entity.TechCardSeamLink{
			{OperationNumber: number, SeamKey: v8Stitch},
			{OperationNumber: number, SeamKey: v8Pose},
		},
	})
	require.NoError(t, err)
	require.Len(t, change.Seams, 2)
	for _, seam := range change.Seams {
		require.False(t, seam.Stale)
		require.Empty(t, seam.AnchoredSize)
	}
	return cardID, sizeID, sizeToken
}

func nativeCrossRowReviewWriteFixtureV8(t *testing.T, snapshot entity.SavedAssemblySnapshot, sizeID int, sizeToken string) entity.PatternSemanticSidecarWrite {
	t.Helper()
	source, row, revision := snapshot.CompleteSourceV1, snapshot.Row, snapshot.ServerAssemblyRevision
	require.Len(t, snapshot.Card.Operations, 1)
	operationNumber := int(snapshot.Card.Operations[0].OperationNumber.Int32)
	require.Positive(t, operationNumber)

	var wire struct {
		OwnedSeams []struct {
			SeamKey string                            `json:"seamKey"`
			SideA   struct{ Parts []json.RawMessage } `json:"sideA"`
			SideB   struct{ Parts []json.RawMessage } `json:"sideB"`
		} `json:"ownedSeams"`
	}
	require.NoError(t, json.Unmarshal(source.CanonicalJSON, &wire))
	require.Equal(t, v8Stitch, wire.OwnedSeams[0].SeamKey)
	require.Equal(t, v8Pose, wire.OwnedSeams[1].SeamKey)
	rowWire := func(row entity.SavedPatternRowIdentity) map[string]any {
		return map[string]any{
			"techCardId": row.TechCardID, "scopeKey": row.ScopeKey, "patternLineKey": row.PatternLineKey,
			"revision": row.SavedRowRevision, "cardLockVersion": row.CardLockVersion, "url": row.URL,
			"sizeBytes": row.SizeBytes, "sha256": row.SHA256, "patternVersion": row.PatternVersion,
		}
	}
	sides := [2][]json.RawMessage{wire.OwnedSeams[0].SideA.Parts, wire.OwnedSeams[0].SideB.Parts}
	members := make([]map[string]any, 2)
	for index, member := range source.Rows {
		require.Len(t, sides[index], 1)
		block, piece := "FRONT", v8MainPiece
		if index == 1 {
			block, piece = "POCKET", v8PocketPiece
		}
		sha, err := hex.DecodeString(member.SHA256)
		require.NoError(t, err)
		fileID := "s/" + strconv.FormatInt(int64(member.TechCardID), 36) + "/" + base64.RawURLEncoding.EncodeToString(sha)
		prefix := "dxf-vertex-v1/dxf-curve-v1/f/" + hex.EncodeToString([]byte(fileID)) + "/"
		members[index] = map[string]any{
			"row": rowWire(member), "sideIndex": index, "partOrdinal": 0, "pieceLineKey": piece,
			"blockName": block, "pieceId": block,
			"contourId":    "dxf-piece-v1/p/" + hex.EncodeToString([]byte(block)) + "/s/" + hex.EncodeToString([]byte(sizeToken)) + "/g/seam",
			"nativePathId": 101 + index, "sourceFileId": fileID, "direction": "forward",
			"orderedVertexIds": []string{prefix + "fixture-start", prefix + "fixture-end"},
			"capturedSavedAnchorSha256": entity.PatternSemanticSidecarPayloadSHA256(
				bytes.Join([][]byte{[]byte("tech-card-saved-assembly-native-anchor/1"), sides[index][0]}, []byte{0}),
			),
		}
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "pattern-import-dxf-product-composite-sidecar-document/8",
		"mediaType":     entity.PatternProductCompositeSidecarV8MediaType, "encoding": "canonical-json-utf8",
		"profile":  "saved-full-assembly/native-cross-row-stitch/1",
		"bindings": map[string]any{"anchorRow": rowWire(row), "serverAssemblyRevision": revision, "completeSourceSha256": source.SourceSHA256},
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": nil},
		"selection": map[string]any{
			"operationNumber": operationNumber, "operationOrdinal": 0, "ownedSeamKeys": []string{v8Stitch, v8Pose},
			"savedDirection": "reversed", "savedAnchoredSize": "", "sizeId": sizeID,
			"sizeToken": sizeToken, "sizeBasis": "operator-confirmed-native", "members": members,
		},
		"review": map[string]any{
			"sourceDecisionSha256": strings.Repeat("d", 64), "boundSourceRequestSha256": strings.Repeat("e", 64),
			"gridSha256": strings.Repeat("f", 64), "stationCount": 3,
		},
	})
	require.NoError(t, err)
	prior := ""
	return entity.PatternSemanticSidecarWrite{
		TechCardId: row.TechCardID, PatternLineKey: row.PatternLineKey,
		ExpectedCardLockVersion: &row.CardLockVersion, ExpectedSavedRowRevision: &row.SavedRowRevision,
		ExpectedDxfSHA256: row.SHA256, ExpectedPatternVersion: row.PatternVersion,
		ExpectedPreviousSidecarSHA256: &prior, ExpectedServerAssemblyRevision: &revision,
		ExpectedAssemblyTopologySHA256: &source.SourceSHA256, SelectedOperationNumber: operationNumber,
		ExpectedPostSaveReceiptSHA256: entity.PatternProductNativeCrossRowReviewReceiptV8(payload),
		MediaType:                     entity.PatternProductCompositeSidecarV8MediaType, SidecarSHA256: entity.PatternSemanticSidecarPayloadSHA256(payload),
		Payload: payload, By: "v8-local-transaction-test",
	}
}

func legacySidecarWriteForNativeCrossRowV8(row entity.SavedPatternRowIdentity) entity.PatternSemanticSidecarWrite {
	receipt := strings.Repeat("c", 64)
	payload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":%q,"encoding":"canonical-json-utf8","dxf":{"exportSha256":%q},"persistence":{"receiptSealSha256":%q,"card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":%q,"version":%d,"sha256":%q}}}`,
		entity.PatternSemanticSidecarMediaType, row.SHA256, receipt, row.TechCardID, row.CardLockVersion,
		row.PatternLineKey, row.PatternVersion, row.SHA256,
	))
	return entity.PatternSemanticSidecarWrite{
		TechCardId: row.TechCardID, PatternLineKey: row.PatternLineKey,
		ExpectedCardLockVersion: &row.CardLockVersion, ExpectedDxfSHA256: row.SHA256, ExpectedPatternVersion: row.PatternVersion,
		ExpectedPostSaveReceiptSHA256: receipt, MediaType: entity.PatternSemanticSidecarMediaType,
		SidecarSHA256: entity.PatternSemanticSidecarPayloadSHA256(payload), Payload: payload, By: "legacy-write-test",
	}
}
