package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/require"
)

func TestPatternSemanticSidecarV10CurrentSlotAndColdReopen(t *testing.T) {
	skipUnlessLocalContainer(t)
	if os.Getenv("MYSQL_DATABASE") != "assembly_v10_sidecar_test" || os.Getenv("MYSQL_HOST") != "127.0.0.1" {
		t.Skip("requires the disposable loopback assembly_v10_sidecar_test schema")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg := *testCfg
	repo, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(repo.Close)
	cards, models := repo.TechCards(), repo.Models()

	migrationBytes, err := os.ReadFile("sql/0481_pattern_semantic_sidecar_composite_v10_media.sql")
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

	modelID, err := models.AddModel(ctx, &entity.ModelInsert{
		Name: "whole placement diagnostic body", Gender: sql.NullString{String: string(entity.Unisex), Valid: true},
		Measurements: []entity.ModelMeasurement{{Name: entity.BodyWaist, ValueMM: 740}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, models.DeleteModel(context.Background(), modelID)) })
	neutral := readNativePlacementModelFixtureV9(t, ctx, repo, modelID)

	for _, useV9 := range []bool{false, true} {
		name := "first wrap V8"
		if useV9 {
			name = "first wrap V9 then edit V10"
		}
		t.Run(name, func(t *testing.T) {
			cardID, sizeID, sizeToken := seedNativeCrossRowReviewV8(t, ctx, cards)
			card, err := cards.GetTechCardById(ctx, cardID)
			require.NoError(t, err)
			var row entity.SavedPatternRowIdentity
			for _, saved := range card.Patterns {
				if saved.LineKey == v8MainPattern {
					row = entity.SavedPatternRowIdentity{
						TechCardID: cardID, PatternLineKey: saved.LineKey, ScopeKey: "main",
						SavedRowRevision: saved.SavedRowRevision, CardLockVersion: card.LockVersion,
						PatternVersion: saved.Version, SHA256: saved.SHA256.String, URL: saved.URL, SizeBytes: saved.SizeBytes.Int64,
					}
				}
			}
			req := entity.SavedAssemblySnapshotRequest{Profile: entity.SavedAssemblySnapshotCompleteSourceV1, ExpectedRow: row}
			before, err := cards.GetTechCardSavedAssemblySnapshot(ctx, req)
			require.NoError(t, err)
			req.ExpectedCompleteSourceSHA256 = before.CompleteSourceV1.SourceSHA256
			req.ExpectedServerAssemblyRevision = before.ServerAssemblyRevision
			v8 := nativeCrossRowReviewWriteFixtureV8(t, before, sizeID, sizeToken)
			prior, err := cards.PutTechCardPatternSemanticSidecar(ctx, v8)
			require.NoError(t, err)
			if useV9 {
				v9 := nativePlacementWriteFixtureV9(t, v8, neutral, prior.SidecarSHA256, 0)
				prior, err = cards.PutTechCardPatternSemanticSidecar(ctx, v9)
				require.NoError(t, err)
			}

			first := wholePlacementWriteFixtureV10(t, v8, prior, neutral, sizeID, sizeToken, prior.SidecarSHA256, 0.5)
			setOtherRowRevision := func(step int) {
				t.Helper()
				_, err := testDB.ExecContext(ctx, `UPDATE tech_card_size_pattern SET saved_row_revision = saved_row_revision + ?
					WHERE tech_card_id = ? AND line_key = ?`, step, cardID, v8PocketPattern)
				require.NoError(t, err)
			}
			setOtherRowRevision(1)
			_, err = cards.PutTechCardPatternSemanticSidecar(ctx, first)
			require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
			setOtherRowRevision(-1)

			wrong := neutral
			wrong.ModelPhysicalInputsSHA256 = strings.Repeat("e", 64)
			staleBody := wholePlacementWriteFixtureV10(t, v8, prior, wrong, sizeID, sizeToken, prior.SidecarSHA256, 0.5)
			_, err = cards.PutTechCardPatternSemanticSidecar(ctx, staleBody)
			require.ErrorIs(t, err, entity.ErrNeutralPlacementModelMoved)
			unmoved, err := cards.GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
			require.NoError(t, err)
			require.Equal(t, prior, unmoved)

			stored, err := cards.PutTechCardPatternSemanticSidecar(ctx, first)
			require.NoError(t, err)
			require.Equal(t, prior.PatternRowId, stored.PatternRowId)
			require.Equal(t, prior.CardLockVersion, stored.CardLockVersion)
			require.Equal(t, first.Payload, stored.Payload)
			_, err = cards.PutTechCardPatternSemanticSidecar(ctx, first)
			require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)

			if useV9 {
				oldMedia := nativePlacementWriteFixtureV9(t, v8, neutral, stored.SidecarSHA256, 20)
				_, err = cards.PutTechCardPatternSemanticSidecar(ctx, oldMedia)
				require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
				edit := wholePlacementWriteFixtureV10(t, v8, prior, neutral, sizeID, sizeToken, stored.SidecarSHA256, -0.75)
				stored, err = cards.PutTechCardPatternSemanticSidecar(ctx, edit)
				require.NoError(t, err)
				require.Equal(t, edit.Payload, stored.Payload)
			}

			older := legacySidecarWriteForNativeCrossRowV8(row)
			for _, expected := range []*string{nil, &stored.SidecarSHA256} {
				older.ExpectedPreviousSidecarSHA256 = expected
				_, err = cards.PutTechCardPatternSemanticSidecar(ctx, older)
				require.ErrorIs(t, err, entity.ErrTechCardPatternSidecarBindingMoved)
			}
			cold, err := NewForTest(ctx, cfg)
			require.NoError(t, err)
			defer cold.Close()
			plain, err := cold.TechCards().GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
			require.NoError(t, err)
			require.Equal(t, stored, plain)
			snapshot, err := cold.TechCards().GetTechCardSavedAssemblySnapshot(ctx, req)
			require.NoError(t, err)
			require.Equal(t, stored, *snapshot.SemanticSidecar)
			require.Equal(t, before.CompleteSourceV1.SourceSHA256, snapshot.CompleteSourceV1.SourceSHA256)

			// Equal model timestamp is insufficient when its real persisted measurements change.
			_, err = testDB.ExecContext(ctx, "UPDATE model_measurement SET measurement_value_mm=741 WHERE model_id=?", modelID)
			require.NoError(t, err)
			_, err = cold.TechCards().GetTechCardSavedAssemblySnapshot(ctx, req)
			require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
			require.ErrorIs(t, err, entity.ErrNeutralPlacementModelMoved)
			plain, err = cold.TechCards().GetTechCardPatternSemanticSidecar(ctx, cardID, v8MainPattern)
			require.NoError(t, err)
			require.Equal(t, stored, plain)
			_, err = testDB.ExecContext(ctx, "UPDATE model_measurement SET measurement_value_mm=740 WHERE model_id=?", modelID)
			require.NoError(t, err)

			require.ErrorContains(t, migrate(migration.DownStatements), "chk_pattern_semantic_sidecar_media_type")
			_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id=?", stored.PatternRowId)
			require.NoError(t, err)
			require.NoError(t, migrate(migration.DownStatements))
			require.NoError(t, migrate(migration.UpStatements))
		})
	}
}

func wholePlacementWriteFixtureV10(
	t *testing.T, v8 entity.PatternSemanticSidecarWrite, retained entity.PatternSemanticSidecar,
	model entity.NeutralPlacementModelAuthorityV1, sizeID int, sizeToken, previous string, x float32,
) entity.PatternSemanticSidecarWrite {
	t.Helper()
	var inner struct {
		Bindings json.RawMessage `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(v8.Payload, &inner))
	part := func(kind, media string, bytes []byte) map[string]any {
		return map[string]any{"kind": kind, "mediaType": media, "byteLength": len(bytes),
			"sha256": entity.PatternSemanticSidecarPayloadSHA256(bytes), "base64": base64.StdEncoding.EncodeToString(bytes)}
	}
	xyz := make([]byte, 6*3*4)
	binary.LittleEndian.PutUint32(xyz[:4], math.Float32bits(x))
	world, err := json.Marshal(map[string]any{
		"schemaVersion": "tech-card-complete-source-native-whole-world-placement/1", "retainedCarrierSha256": retained.SidecarSHA256,
		"body": map[string]any{
			"modelId": model.ModelID, "modelUpdatedAt": model.ModelUpdatedAt, "modelPhysicalInputsSha256": model.ModelPhysicalInputsSHA256,
			"pose":     map[string]string{"kind": model.Pose.Kind, "canonicalSha256": model.Pose.CanonicalSHA256},
			"observed": map[string]string{"bodyPositionsFloat32LeSha256": strings.Repeat("1", 64), "bodyTrianglesUint32LeSha256": strings.Repeat("2", 64)},
		},
		"layout": map[string]any{
			"sizeId": sizeID, "sizeToken": sizeToken, "runtimeCandidateSha256": strings.Repeat("3", 64), "cutGraphSha256": strings.Repeat("4", 64),
			"positions": map[string]any{"encoding": "float32-le-xyz", "byteLength": len(xyz),
				"sha256": entity.PatternSemanticSidecarPayloadSHA256(xyz), "base64": base64.StdEncoding.EncodeToString(xyz)},
			"pieceRanges": []map[string]any{
				{"pieceId": v8MainPiece, "vertexOffset": 0, "vertexCount": 3, "triangleOffset": 0, "triangleCount": 1},
				{"pieceId": v8PocketPiece, "vertexOffset": 3, "vertexCount": 3, "triangleOffset": 1, "triangleCount": 1},
			},
		},
	})
	require.NoError(t, err)
	out := v8
	out.MediaType = entity.PatternProductCompositeSidecarV10MediaType
	out.ExpectedPreviousSidecarSHA256 = &previous
	out.Payload, err = json.Marshal(map[string]any{
		"schemaVersion": "pattern-import-dxf-product-composite-sidecar-document/10", "mediaType": out.MediaType,
		"profile": "saved-full-assembly/native-whole-world-placement/1", "encoding": "canonical-json-utf8",
		"bindings": inner.Bindings, "writeCas": map[string]any{"expectedPreviousSidecarSha256": previous},
		"components": []any{
			part("retained-native-cross-row-carrier", retained.MediaType, retained.Payload),
			part("native-whole-world-placement-v1", "application/vnd.grbpwr.pattern-dxf-native-whole-world-placement.v1+json", world),
		},
	})
	require.NoError(t, err)
	out.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(out.Payload)
	out.ExpectedPostSaveReceiptSHA256 = entity.PatternProductNativeWholePlacementReceiptV10(out.Payload)
	return out
}
