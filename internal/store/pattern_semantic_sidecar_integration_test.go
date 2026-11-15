package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternSemanticSidecarCASAndInvalidation drives migration 0468 and the real transactions.
// The two serial orders of the read→rewrite→late-attach race must both be safe:
//
//  1. PUT wins first, then the full card rewrite deletes its attachment;
//  2. the rewrite wins first, then the stale PUT is refused by card lock_version even when the
//     pattern's line key, SHA and version did not move.
//
// SAFE ONLY against a disposable local/CI MySQL. TestMain drops every table on cleanup.
func TestPatternSemanticSidecarCASAndInvalidation(t *testing.T) {
	skipUnlessLocalContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	techCards := s.TechCards()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	const (
		patternLineKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
		bomLineKey     = "01JABCDEFGHJKMNPQRSTVWXYZ1"
		patternURL     = "https://cdn.example/semantic-sidecar/front-back.dxf"
	)
	dxfSHA := strings.Repeat("a", 64)
	receiptSHA := strings.Repeat("b", 64)
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	card := func(name string) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: name, StyleNumber: present("G4F7-CAS"), Stage: entity.TechCardStageProto,
			ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
			SizeIds: []int{sizeID},
			BomItems: []entity.TechCardBomItem{{
				LineKey: bomLineKey, Section: entity.BomSectionFabric, Name: "Shell",
			}},
			Patterns: []entity.TechCardSizePattern{{
				SizeId: sizeID, LineKey: patternLineKey, BomLineKey: present(bomLineKey),
				URL: patternURL, Filename: present("front-back.dxf"), SHA256: present(dxfSHA),
			}},
		}
	}

	techCardID, err := techCards.AddTechCard(ctx, card("G4F7 sidecar CAS A"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", techCardID)
	})

	readBinding := func() (lockVersion, patternVersion int) {
		stored, readErr := techCards.GetTechCardById(ctx, techCardID)
		require.NoError(t, readErr)
		require.Len(t, stored.Patterns, 1)
		require.Equal(t, patternLineKey, stored.Patterns[0].LineKey)
		require.Equal(t, dxfSHA, stored.Patterns[0].SHA256.String)
		return stored.LockVersion, stored.Patterns[0].Version
	}
	payload := func(lockVersion, patternVersion int) []byte {
		return []byte(fmt.Sprintf(
			`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","dxf":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":"%s","version":%d,"sha256":"%s"}}}`,
			entity.PatternSemanticSidecarMediaType, dxfSHA, receiptSHA, techCardID, lockVersion,
			patternLineKey, patternVersion, dxfSHA,
		))
	}
	write := func(lockVersion, patternVersion int) entity.PatternSemanticSidecarWrite {
		bytes := payload(lockVersion, patternVersion)
		return entity.PatternSemanticSidecarWrite{
			TechCardId: techCardID, PatternLineKey: patternLineKey,
			ExpectedCardLockVersion: &lockVersion, ExpectedDxfSHA256: dxfSHA,
			ExpectedPatternVersion: patternVersion, ExpectedPostSaveReceiptSHA256: receiptSHA,
			MediaType:     entity.PatternSemanticSidecarMediaType,
			SidecarSHA256: entity.PatternSemanticSidecarPayloadSHA256(bytes), Payload: bytes, By: "g4f7-test",
		}
	}

	lockA, patternVersion := readBinding()
	stored, err := techCards.PutTechCardPatternSemanticSidecar(ctx, write(lockA, patternVersion))
	require.NoError(t, err, "order 1: PUT may commit before the rewrite")
	require.Equal(t, lockA, stored.CardLockVersion)
	require.Equal(t, payload(lockA, patternVersion), stored.Payload)

	// A later unrelated lock bump must not corrupt the immutable acceptance binding. The sidecar is
	// compared with the version stored beside it, not with the card's forever-moving current value.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = ?", techCardID)
	require.NoError(t, err)
	readAfterUnrelatedBump, err := techCards.GetTechCardPatternSemanticSidecar(ctx, techCardID, patternLineKey)
	require.NoError(t, err)
	require.Equal(t, lockA, readAfterUnrelatedBump.CardLockVersion)

	lockBeforeRewrite, versionBeforeRewrite := readBinding()
	require.Equal(t, patternVersion, versionBeforeRewrite)
	require.NoError(t, techCards.UpdateTechCard(ctx, techCardID, card("G4F7 sidecar CAS B"), lockBeforeRewrite))

	_, err = techCards.GetTechCardPatternSemanticSidecar(ctx, techCardID, patternLineKey)
	require.ErrorIs(t, err, sql.ErrNoRows, "order 1: rewrite deletes the attachment that committed first")

	lockB, versionB := readBinding()
	require.Equal(t, lockBeforeRewrite+1, lockB)
	require.Equal(t, patternVersion, versionB, "the race witness keeps pattern version unchanged")

	_, err = techCards.PutTechCardPatternSemanticSidecar(ctx, write(lockA, patternVersion))
	require.Error(t, err)
	require.True(t, errors.Is(err, entity.ErrTechCardPatternSidecarBindingMoved),
		"order 2: a late pre-rewrite PUT must lose even though DXF SHA/version are unchanged")
	_, err = techCards.GetTechCardPatternSemanticSidecar(ctx, techCardID, patternLineKey)
	require.ErrorIs(t, err, sql.ErrNoRows, "the refused stale PUT must not recreate a row")

	rebuilt, err := techCards.PutTechCardPatternSemanticSidecar(ctx, write(lockB, versionB))
	require.NoError(t, err, "a sidecar rebuilt from the post-rewrite binding may attach")
	require.Equal(t, lockB, rebuilt.CardLockVersion)
	require.Equal(t, payload(lockB, versionB), rebuilt.Payload)

	// 0469 widens only the versioned media envelope. Product R12 semantics use the same row-lock,
	// DXF revision, receipt and byte-integrity CAS, and may replace the legacy envelope atomically.
	productPayload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-product-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","product":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":"%s","version":%d,"sha256":"%s"}}}`,
		entity.PatternProductSemanticSidecarMediaType, dxfSHA, receiptSHA, techCardID, lockB,
		patternLineKey, versionB, dxfSHA,
	))
	productWrite := write(lockB, versionB)
	productWrite.MediaType = entity.PatternProductSemanticSidecarMediaType
	productWrite.Payload = productPayload
	productWrite.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(productPayload)
	productStored, err := techCards.PutTechCardPatternSemanticSidecar(ctx, productWrite)
	require.NoError(t, err, "product envelope uses the same exact post-save CAS")
	require.Equal(t, entity.PatternProductSemanticSidecarMediaType, productStored.MediaType)
	require.Equal(t, productPayload, productStored.Payload)
	productRead, err := techCards.GetTechCardPatternSemanticSidecar(ctx, techCardID, patternLineKey)
	require.NoError(t, err)
	require.Equal(t, entity.PatternProductSemanticSidecarMediaType, productRead.MediaType)
	require.Equal(t, productPayload, productRead.Payload)
}
