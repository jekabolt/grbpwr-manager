package techcard

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func validPatternSemanticSidecarRow() patternSemanticSidecarRow {
	dxfSHA := strings.Repeat("a", 64)
	receiptSHA := strings.Repeat("b", 64)
	payload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","dxf":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":7,"observedLockVersion":8},"pattern":{"lineKey":"01JABCDEFGHJKMNPQRSTVWXYZ0","version":3,"sha256":"%s"}}}`,
		entity.PatternSemanticSidecarMediaType, dxfSHA, receiptSHA, dxfSHA,
	))
	sha := entity.PatternSemanticSidecarPayloadSHA256(payload)
	return patternSemanticSidecarRow{
		PatternSemanticSidecar: entity.PatternSemanticSidecar{
			PatternRowId:          9,
			TechCardId:            7,
			PatternLineKey:        "01JABCDEFGHJKMNPQRSTVWXYZ0",
			CardLockVersion:       8,
			DxfSHA256:             dxfSHA,
			PatternVersion:        3,
			SavedRowRevision:      4,
			SidecarSHA256:         sha,
			MediaType:             entity.PatternSemanticSidecarMediaType,
			ByteLength:            len(payload),
			Payload:               payload,
			PostSaveReceiptSHA256: receiptSHA,
			CreatedAt:             time.Unix(1, 0),
			CreatedBy:             "ann",
		},
		CurrentDxfSHA256:        dxfSHA,
		CurrentPatternVersion:   3,
		CurrentSavedRowRevision: 4,
	}
}

func TestPatternSemanticSidecarWriteLocksBeforeReadingAndDocumentRewriteInvalidatesBeforeUpdate(t *testing.T) {
	body := funcBody(t, "pattern_semantic_sidecar.go", "func (s *Store) PutTechCardPatternSemanticSidecar")
	lock := strings.Index(body, "lockTechCardRowVersion(")
	require.GreaterOrEqual(t, lock, 0)
	ownedToken := strings.Index(body, "expectedCardLockVersion := *in.ExpectedCardLockVersion")
	require.GreaterOrEqual(t, ownedToken, 0)
	cas := strings.Index(body, "cardLockVersion != expectedCardLockVersion")
	require.Greater(t, cas, lock, "card lock_version CAS must run under the card-row lock")
	for _, operation := range []string{"RequireMutableTechCard(", "storeutil.Query", "storeutil.Exec", "readPatternSemanticSidecar("} {
		at := strings.Index(body, operation)
		require.GreaterOrEqual(t, at, 0, operation)
		require.Greater(t, at, lock, operation+" must run after the card-row lock")
		require.Greater(t, at, cas, operation+" must run after the card lock_version CAS")
	}

	source, err := os.ReadFile("techcard.go")
	require.NoError(t, err)
	patterns := string(source)
	start := strings.Index(patterns, "func insertTechCardPatterns")
	require.GreaterOrEqual(t, start, 0)
	patterns = patterns[start:]
	invalidate := strings.Index(patterns, "invalidatePatternSemanticSidecarsForDocumentRewrite(")
	update := strings.Index(patterns, "UPDATE tech_card_size_pattern SET")
	require.GreaterOrEqual(t, invalidate, 0)
	require.Greater(t, update, invalidate, "document rewrite must invalidate every child-bound receipt before a row moves")

	invalidationBody := funcBody(t, "pattern_semantic_sidecar.go", "func invalidatePatternSemanticSidecarsForDocumentRewrite")
	require.Contains(t, invalidationBody, "JOIN tech_card_size_pattern")
	require.Contains(t, invalidationBody, "pattern.tech_card_id = :tech_card_id")

	migration, err := os.ReadFile("../sql/0471_pattern_semantic_sidecar.sql")
	require.NoError(t, err)
	require.Contains(t, string(migration), "card_lock_version INT NOT NULL")
	require.Contains(t, string(migration), "card_lock_version >= 0")
	productMediaMigration, err := os.ReadFile("../sql/0472_pattern_semantic_sidecar_product_media.sql")
	require.NoError(t, err)
	require.Contains(t, string(productMediaMigration), entity.PatternProductSemanticSidecarMediaType)
	require.Contains(t, string(productMediaMigration), "chk_pattern_semantic_sidecar_media_type")
}

func TestVerifyPatternSemanticSidecarRow(t *testing.T) {
	require.NoError(t, verifyPatternSemanticSidecarRow(validPatternSemanticSidecarRow()))
}

func TestVerifyPatternSemanticSidecarRowAcceptsProductEnvelope(t *testing.T) {
	row := validPatternSemanticSidecarRow()
	row.MediaType = entity.PatternProductSemanticSidecarMediaType
	row.Payload = []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-product-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","product":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":7,"observedLockVersion":8},"pattern":{"lineKey":"%s","version":3,"sha256":"%s"}}}`,
		row.MediaType,
		row.DxfSHA256,
		row.PostSaveReceiptSHA256,
		row.PatternLineKey,
		row.DxfSHA256,
	))
	row.ByteLength = len(row.Payload)
	row.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(row.Payload)
	require.NoError(t, verifyPatternSemanticSidecarRow(row))
}

func TestVerifyPatternSemanticSidecarRowRefusesDriftAndCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*patternSemanticSidecarRow)
	}{
		{"dxf moved", func(row *patternSemanticSidecarRow) { row.CurrentDxfSHA256 = strings.Repeat("c", 64) }},
		{"version moved", func(row *patternSemanticSidecarRow) { row.CurrentPatternVersion++ }},
		{"inner card lock moved", func(row *patternSemanticSidecarRow) {
			row.Payload = []byte(strings.Replace(string(row.Payload), `"observedLockVersion":8`, `"observedLockVersion":9`, 1))
			row.ByteLength = len(row.Payload)
			row.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(row.Payload)
		}},
		{"media", func(row *patternSemanticSidecarRow) { row.MediaType = "application/json" }},
		{"length", func(row *patternSemanticSidecarRow) { row.ByteLength++ }},
		{"payload", func(row *patternSemanticSidecarRow) { row.Payload[0] ^= 1 }},
		{"inner binding", func(row *patternSemanticSidecarRow) {
			row.Payload = []byte(strings.Replace(string(row.Payload), `"techCardId":7`, `"techCardId":8`, 1))
			row.ByteLength = len(row.Payload)
			row.SidecarSHA256 = entity.PatternSemanticSidecarPayloadSHA256(row.Payload)
		}},
		{"receipt", func(row *patternSemanticSidecarRow) { row.PostSaveReceiptSHA256 = "bad" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := validPatternSemanticSidecarRow()
			tt.mutate(&row)
			require.ErrorIs(t, verifyPatternSemanticSidecarRow(row), entity.ErrTechCardPatternSidecarCorrupt)
		})
	}
}

func TestStoredCompositeSidecarRemainsReadableAcrossAssemblyOnlyCardLockDrift(t *testing.T) {
	var fixture struct {
		Row struct {
			TechCardID      int    `json:"techCardId"`
			ScopeKey        string `json:"scopeKey"`
			PatternLineKey  string `json:"patternLineKey"`
			Revision        uint32 `json:"revision"`
			CardLockVersion int    `json:"cardLockVersion"`
			URL             string `json:"url"`
			SizeBytes       int64  `json:"sizeBytes"`
			SHA256          string `json:"sha256"`
		} `json:"row"`
		OuterBase64     string `json:"outerBase64"`
		OuterSHA256     string `json:"outerSha256"`
		OuterByteLength int    `json:"outerByteLength"`
	}
	fixtureJSON, err := os.ReadFile("../../entity/testdata/pattern_product_composite_v3_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fixtureJSON, &fixture))
	payload, err := base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)

	row := patternSemanticSidecarRow{
		PatternSemanticSidecar: entity.PatternSemanticSidecar{
			PatternRowId: 9, TechCardId: fixture.Row.TechCardID,
			PatternLineKey: fixture.Row.PatternLineKey, CardLockVersion: fixture.Row.CardLockVersion,
			DxfSHA256: fixture.Row.SHA256, PatternVersion: 4, SavedRowRevision: fixture.Row.Revision,
			SidecarSHA256: fixture.OuterSHA256, MediaType: entity.PatternProductCompositeSidecarMediaType,
			ByteLength: fixture.OuterByteLength, Payload: payload,
			PostSaveReceiptSHA256: strings.Repeat("3", 64),
		},
		CurrentDxfSHA256: fixture.Row.SHA256, CurrentPatternVersion: 4,
		CurrentSavedRowRevision: fixture.Row.Revision,
		// A seam/operation edit moved the card lock, but did not rewrite this pattern row.
		CurrentCardLockVersion: fixture.Row.CardLockVersion + 1,
		CurrentURL:             fixture.Row.URL,
		CurrentSizeBytes:       sql.NullInt64{Int64: fixture.Row.SizeBytes, Valid: true},
	}
	scope := entity.FabricScope{Key: fixture.Row.ScopeKey, LineKeys: []string{"BOM-LINE"}}

	require.NoError(t, verifyPatternSemanticSidecarRow(row))
	require.NoError(t, verifyStoredCompositePatternSemanticSidecar(row, scope),
		"the stored carrier remains readable so the snapshot can return it beside the new receipts")
	require.NotEqual(t, row.CardLockVersion, row.CurrentCardLockVersion,
		"the returned sidecar keeps its old binding while the snapshot row carries the new lock")

	t.Run("stored lock binding corruption still fails", func(t *testing.T) {
		corrupt := row
		corrupt.CardLockVersion++
		require.ErrorIs(t, verifyStoredCompositePatternSemanticSidecar(corrupt, scope),
			entity.ErrTechCardPatternSidecarCorrupt)
	})
	t.Run("current pattern byte hash drift still fails", func(t *testing.T) {
		corrupt := row
		corrupt.CurrentDxfSHA256 = strings.Repeat("9", 64)
		require.ErrorIs(t, verifyPatternSemanticSidecarRow(corrupt),
			entity.ErrTechCardPatternSidecarCorrupt)
	})
	t.Run("current saved row revision drift still fails", func(t *testing.T) {
		corrupt := row
		corrupt.CurrentSavedRowRevision++
		require.ErrorIs(t, verifyPatternSemanticSidecarRow(corrupt),
			entity.ErrTechCardPatternSidecarCorrupt)
	})
	t.Run("current URL drift still fails exact composite binding", func(t *testing.T) {
		corrupt := row
		corrupt.CurrentURL = "/patterns/replaced.dxf"
		require.ErrorIs(t, verifyStoredCompositePatternSemanticSidecar(corrupt, scope),
			entity.ErrTechCardPatternSidecarCorrupt)
	})
}
