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

func TestCompositeSidecarStoreDispatchesV3ThroughV6UnderLockAndOnStoredRead(t *testing.T) {
	put := funcBody(t, "pattern_semantic_sidecar.go", "func (s *Store) PutTechCardPatternSemanticSidecar")
	lock := strings.Index(put, "lockTechCardRowVersion(")
	dispatch := strings.Index(put, "selectSavedAssemblySeamsForCompositeMedia(")
	validate := strings.Index(put, "validatePatternProductCompositeSidecarBinding(")
	require.GreaterOrEqual(t, lock, 0)
	require.Greater(t, dispatch, lock, "media-aware topology selection must run under the card lock")
	require.Greater(t, validate, dispatch, "the exact media validator must run after topology CAS")

	selector := funcBody(t, "pattern_semantic_sidecar.go", "func selectSavedAssemblySeamsForCompositeMedia")
	require.Contains(t, selector, "case entity.PatternProductCompositeSidecarMediaType:")
	require.Contains(t, selector, "selectSavedAssemblySeamsV3(")
	require.Contains(t, selector, "case entity.PatternProductCompositeSidecarV4MediaType:")
	require.Contains(t, selector, "case entity.PatternProductCompositeSidecarV5MediaType:")
	require.Contains(t, selector, "case entity.PatternProductCompositeSidecarV6MediaType:")
	require.Contains(t, selector, "selectSavedAssemblySeams(")

	writeValidator := funcBody(t, "pattern_semantic_sidecar.go", "func validatePatternProductCompositeSidecarBinding")
	require.Contains(t, writeValidator, "ValidatePatternProductCompositeSidecarBinding(")
	require.Contains(t, writeValidator, "ValidatePatternProductCompositeSidecarV4Binding(")
	require.Contains(t, writeValidator, "ValidatePatternProductCompositeSidecarV5Binding(")
	require.Contains(t, writeValidator, "ValidatePatternProductCompositeSidecarV6Binding(")

	storedValidator := funcBody(t, "pattern_semantic_sidecar.go", "func validateStoredPatternProductCompositeSidecarExact")
	require.Contains(t, storedValidator, "ValidateStoredPatternProductCompositeSidecarExact(")
	require.Contains(t, storedValidator, "ValidateStoredPatternProductCompositeSidecarV4Exact(")
	require.Contains(t, storedValidator, "ValidateStoredPatternProductCompositeSidecarV5Exact(")
	require.Contains(t, storedValidator, "ValidateStoredPatternProductCompositeSidecarV6Exact(")

	require.Contains(t, put, "ValidatePatternProductCompositeSidecarV4Transition(")
	require.Contains(t, put, "ValidatePatternProductCompositeSidecarV5Transition(")
	require.Contains(t, put, "ValidatePatternProductCompositeSidecarV6Transition(")
	require.Contains(t, put, "composite_v5_prior_required")
	require.Contains(t, put, "composite_v6_prior_required")
	priorQuery := strings.Index(put, "SELECT card_lock_version, saved_row_revision, dxf_sha256, pattern_version,")
	firstPriorValidation := strings.Index(put, "validatePriorCompositeSidecarForTransition(")
	v5Transition := strings.Index(put, "ValidatePatternProductCompositeSidecarV5Transition(")
	lastPriorValidation := strings.LastIndex(put, "validatePriorCompositeSidecarForTransition(")
	v6Transition := strings.Index(put, "ValidatePatternProductCompositeSidecarV6Transition(")
	require.GreaterOrEqual(t, priorQuery, 0, "the locked prior row query must load all integrity metadata")
	require.Contains(t, put[priorQuery:], "WHERE pattern_row_id = :id FOR UPDATE")
	require.Greater(t, v5Transition, validate, "V5 transition policy must run after full next-payload binding validation")
	require.Greater(t, v6Transition, validate, "V6 transition policy must run after full next-payload binding validation")
	require.Greater(t, firstPriorValidation, priorQuery)
	require.Greater(t, v5Transition, firstPriorValidation, "V5 must consume only a revalidated prior carrier")
	require.Greater(t, lastPriorValidation, v5Transition)
	require.Greater(t, v6Transition, lastPriorValidation, "V6 must consume only a revalidated prior carrier")

	read := funcBody(t, "pattern_semantic_sidecar.go", "func readPatternSemanticSidecar")
	require.Contains(t, read, "isPatternProductCompositeSidecarMediaType(row.MediaType)")
	require.Contains(t, read, "verifyStoredCompositePatternSemanticSidecar(row, scope)")
}

type compositePriorTestFixture struct {
	Prior          priorPatternSemanticSidecar
	Row            entity.SavedPatternRowIdentity
	PatternVersion int
	Bindings       map[string]any
}

func loadCompositeV5PriorTestFixture(t *testing.T) compositePriorTestFixture {
	t.Helper()
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
		PatternVersion        int    `json:"patternVersion"`
		PostSaveReceiptSHA256 string `json:"postSaveReceiptSha256"`
		OuterBase64           string `json:"outerBase64"`
		OuterSHA256           string `json:"outerSha256"`
		OuterByteLength       int    `json:"outerByteLength"`
	}
	raw, err := os.ReadFile("../../entity/testdata/pattern_product_composite_v5_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	payload, err := base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(payload, &document))
	row := entity.SavedPatternRowIdentity{
		TechCardID: fixture.Row.TechCardID, ScopeKey: fixture.Row.ScopeKey,
		PatternLineKey: fixture.Row.PatternLineKey, SavedRowRevision: fixture.Row.Revision,
		CardLockVersion: fixture.Row.CardLockVersion, URL: fixture.Row.URL,
		SizeBytes: fixture.Row.SizeBytes, SHA256: fixture.Row.SHA256,
	}
	return compositePriorTestFixture{
		Prior: priorPatternSemanticSidecar{
			CardLockVersion: fixture.Row.CardLockVersion, SavedRowRevision: fixture.Row.Revision,
			DxfSHA256: fixture.Row.SHA256, PatternVersion: fixture.PatternVersion,
			SHA256: fixture.OuterSHA256, MediaType: entity.PatternProductCompositeSidecarV5MediaType,
			ByteLength: fixture.OuterByteLength, Payload: payload,
			PostSaveReceiptSHA256: fixture.PostSaveReceiptSHA256,
		},
		Row: row, PatternVersion: fixture.PatternVersion,
		Bindings: document["bindings"].(map[string]any),
	}
}

func TestValidatePriorCompositeSidecarForTransitionRejectsCorruptV5MetadataAndPayload(t *testing.T) {
	fixture := loadCompositeV5PriorTestFixture(t)
	require.NoError(t, validatePriorCompositeSidecarForTransition(fixture.Prior, fixture.Row, fixture.PatternVersion))

	tests := []struct {
		name   string
		mutate func(*priorPatternSemanticSidecar)
	}{
		{name: "stored byte length", mutate: func(prior *priorPatternSemanticSidecar) { prior.ByteLength++ }},
		{name: "stored digest", mutate: func(prior *priorPatternSemanticSidecar) { prior.SHA256 = strings.Repeat("0", 64) }},
		{name: "payload bytes", mutate: func(prior *priorPatternSemanticSidecar) { prior.Payload = append(prior.Payload, '\n') }},
		{name: "saved row metadata", mutate: func(prior *priorPatternSemanticSidecar) { prior.SavedRowRevision++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prior := fixture.Prior
			prior.Payload = append([]byte(nil), prior.Payload...)
			tt.mutate(&prior)
			require.ErrorIs(t,
				validatePriorCompositeSidecarForTransition(prior, fixture.Row, fixture.PatternVersion),
				entity.ErrTechCardPatternSidecarCorrupt)
		})
	}
}

type mannequinArrangementTestDocument struct {
	SchemaVersion string `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Encoding      string `json:"encoding"`
	Bindings      struct {
		Card struct {
			TechCardID      int `json:"techCardId"`
			CardLockVersion int `json:"cardLockVersion"`
		} `json:"card"`
		Scope struct {
			ScopeKey       string `json:"scopeKey"`
			PatternLineKey string `json:"patternLineKey"`
			SizeToken      string `json:"sizeToken"`
		} `json:"scope"`
		SavedRow struct {
			Revision  uint32 `json:"revision"`
			DxfSHA256 string `json:"dxfSha256"`
		} `json:"savedRow"`
		Source struct {
			CatalogCanonicalSHA256       string `json:"catalogCanonicalSha256"`
			SizeSelectionCanonicalSHA256 string `json:"sizeSelectionCanonicalSha256"`
		} `json:"source"`
		Mannequin struct {
			BaseModelID            int    `json:"baseModelId"`
			ModelFormRevisionKeyV1 string `json:"modelFormRevisionKeyV1"`
			PoseBinding            struct {
				CanonicalKey string `json:"canonicalKey"`
			} `json:"poseBinding"`
		} `json:"mannequin"`
	} `json:"bindings"`
	Pieces []struct {
		PieceKey     string `json:"pieceKey"`
		ScopeKey     string `json:"scopeKey"`
		PieceID      string `json:"pieceId"`
		PieceLineKey string `json:"pieceLineKey"`
		Transform    struct {
			OffsetM     [3]float64 `json:"offsetM"`
			RotationRad float64    `json:"rotationRad"`
		} `json:"transform"`
	} `json:"pieces"`
	Claims struct {
		PresentationOnly       bool `json:"presentationOnly"`
		DxfAuthority           bool `json:"dxfAuthority"`
		AssemblyOrderAuthority bool `json:"assemblyOrderAuthority"`
		ClothAuthority         bool `json:"clothAuthority"`
		ScalePersisted         bool `json:"scalePersisted"`
	} `json:"claims"`
}

func buildCompositeV6PriorTestFixture(t *testing.T) compositePriorTestFixture {
	t.Helper()
	fixture := loadCompositeV5PriorTestFixture(t)
	arrangement := mannequinArrangementTestDocument{
		SchemaVersion: "grbpwr.cad-mannequin-arrangement/v1",
		MediaType:     "application/vnd.grbpwr.cad-mannequin-arrangement.v1+json",
		Encoding:      "canonical-json-utf8",
	}
	arrangement.Bindings.Card.TechCardID = fixture.Row.TechCardID
	arrangement.Bindings.Card.CardLockVersion = fixture.Row.CardLockVersion
	arrangement.Bindings.Scope.ScopeKey = fixture.Row.ScopeKey
	arrangement.Bindings.Scope.PatternLineKey = fixture.Row.PatternLineKey
	arrangement.Bindings.Scope.SizeToken = "M"
	arrangement.Bindings.SavedRow.Revision = fixture.Row.SavedRowRevision
	arrangement.Bindings.SavedRow.DxfSHA256 = fixture.Row.SHA256
	arrangement.Bindings.Source.CatalogCanonicalSHA256 = strings.Repeat("c", 64)
	arrangement.Bindings.Source.SizeSelectionCanonicalSHA256 = strings.Repeat("d", 64)
	arrangement.Bindings.Mannequin.BaseModelID = 1
	arrangement.Bindings.Mannequin.ModelFormRevisionKeyV1 = "model:1"
	arrangement.Bindings.Mannequin.PoseBinding.CanonicalKey = "pose:a-rest"
	arrangement.Pieces = make([]struct {
		PieceKey     string `json:"pieceKey"`
		ScopeKey     string `json:"scopeKey"`
		PieceID      string `json:"pieceId"`
		PieceLineKey string `json:"pieceLineKey"`
		Transform    struct {
			OffsetM     [3]float64 `json:"offsetM"`
			RotationRad float64    `json:"rotationRad"`
		} `json:"transform"`
	}, 1)
	arrangement.Pieces[0].PieceKey = "piece:front"
	arrangement.Pieces[0].ScopeKey = fixture.Row.ScopeKey
	arrangement.Pieces[0].PieceID = "front"
	arrangement.Pieces[0].PieceLineKey = "piece:front"
	arrangement.Claims.PresentationOnly = true
	arrangementBytes, err := json.Marshal(arrangement)
	require.NoError(t, err)
	component := func(kind, media string, payload []byte) map[string]any {
		return map[string]any{
			"kind": kind, "mediaType": media,
			"sha256":     entity.PatternSemanticSidecarPayloadSHA256(payload),
			"byteLength": len(payload), "base64": base64.StdEncoding.EncodeToString(payload),
		}
	}
	outer := map[string]any{
		"schemaVersion": "pattern-import-dxf-product-composite-sidecar-document/6",
		"mediaType":     entity.PatternProductCompositeSidecarV6MediaType, "encoding": "canonical-json-utf8",
		"bindings": fixture.Bindings,
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": fixture.Prior.SHA256},
		"components": []any{
			component("composite-v5", entity.PatternProductCompositeSidecarV5MediaType, fixture.Prior.Payload),
			component("cad-mannequin-arrangement-v1", "application/vnd.grbpwr.cad-mannequin-arrangement.v1+json", arrangementBytes),
		},
		"claims": map[string]any{
			"nestedCompositeV5BytesPreservedExactly":         true,
			"mannequinArrangementCanonicalAndDigestVerified": true,
			"exactCardScopeAndSavedRowBound":                 true, "canonicalComponentOrderAndBase64Required": true,
			"previousSidecarCasExpectationBound": true, "mannequinArrangementPresentationOnly": true,
			"dxfAuthorityAdded": false, "assemblyOrderAuthorityAdded": false, "clothAuthorityAdded": false,
			"persistenceIntegrated": false, "productionReady": false,
		},
	}
	payload, err := json.Marshal(outer)
	require.NoError(t, err)
	fixture.Prior.MediaType = entity.PatternProductCompositeSidecarV6MediaType
	fixture.Prior.Payload = payload
	fixture.Prior.ByteLength = len(payload)
	fixture.Prior.SHA256 = entity.PatternSemanticSidecarPayloadSHA256(payload)
	return fixture
}

func TestValidatePriorCompositeSidecarForTransitionRejectsCorruptV6PayloadAndArrangement(t *testing.T) {
	fixture := buildCompositeV6PriorTestFixture(t)
	require.NoError(t, validatePriorCompositeSidecarForTransition(fixture.Prior, fixture.Row, fixture.PatternVersion))

	outerCorrupt := fixture.Prior
	outerCorrupt.Payload = append([]byte(nil), outerCorrupt.Payload...)
	outerCorrupt.Payload[0] = '['
	outerCorrupt.SHA256 = entity.PatternSemanticSidecarPayloadSHA256(outerCorrupt.Payload)
	require.ErrorIs(t,
		validatePriorCompositeSidecarForTransition(outerCorrupt, fixture.Row, fixture.PatternVersion),
		entity.ErrTechCardPatternSidecarCorrupt)

	var outer map[string]any
	require.NoError(t, json.Unmarshal(fixture.Prior.Payload, &outer))
	arrangementComponent := outer["components"].([]any)[1].(map[string]any)
	arrangementBytes, err := base64.StdEncoding.Strict().DecodeString(arrangementComponent["base64"].(string))
	require.NoError(t, err)
	arrangementBytes = append([]byte{' '}, arrangementBytes...)
	arrangementComponent["base64"] = base64.StdEncoding.EncodeToString(arrangementBytes)
	arrangementComponent["byteLength"] = len(arrangementBytes)
	arrangementComponent["sha256"] = entity.PatternSemanticSidecarPayloadSHA256(arrangementBytes)
	arrangementCorruptPayload, err := json.Marshal(outer)
	require.NoError(t, err)
	arrangementCorrupt := fixture.Prior
	arrangementCorrupt.Payload = arrangementCorruptPayload
	arrangementCorrupt.ByteLength = len(arrangementCorruptPayload)
	arrangementCorrupt.SHA256 = entity.PatternSemanticSidecarPayloadSHA256(arrangementCorruptPayload)
	require.ErrorIs(t,
		validatePriorCompositeSidecarForTransition(arrangementCorrupt, fixture.Row, fixture.PatternVersion),
		entity.ErrTechCardPatternSidecarCorrupt)
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

func TestStoredCompositeV5SidecarUsesExactV5ReadValidator(t *testing.T) {
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
		PatternVersion        int    `json:"patternVersion"`
		PostSaveReceiptSHA256 string `json:"postSaveReceiptSha256"`
		OuterBase64           string `json:"outerBase64"`
		OuterSHA256           string `json:"outerSha256"`
		OuterByteLength       int    `json:"outerByteLength"`
	}
	fixtureJSON, err := os.ReadFile("../../entity/testdata/pattern_product_composite_v5_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fixtureJSON, &fixture))
	payload, err := base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)

	row := patternSemanticSidecarRow{
		PatternSemanticSidecar: entity.PatternSemanticSidecar{
			PatternRowId: 10, TechCardId: fixture.Row.TechCardID,
			PatternLineKey: fixture.Row.PatternLineKey, CardLockVersion: fixture.Row.CardLockVersion,
			DxfSHA256: fixture.Row.SHA256, PatternVersion: fixture.PatternVersion,
			SavedRowRevision: fixture.Row.Revision, SidecarSHA256: fixture.OuterSHA256,
			MediaType:  entity.PatternProductCompositeSidecarV5MediaType,
			ByteLength: fixture.OuterByteLength, Payload: payload,
			PostSaveReceiptSHA256: fixture.PostSaveReceiptSHA256,
		},
		CurrentDxfSHA256: fixture.Row.SHA256, CurrentPatternVersion: fixture.PatternVersion,
		CurrentSavedRowRevision: fixture.Row.Revision,
		CurrentCardLockVersion:  fixture.Row.CardLockVersion + 1,
		CurrentURL:              fixture.Row.URL,
		CurrentSizeBytes:        sql.NullInt64{Int64: fixture.Row.SizeBytes, Valid: true},
	}
	scope := entity.FabricScope{Key: fixture.Row.ScopeKey, LineKeys: []string{"BOM-LINE"}}

	require.NoError(t, verifyPatternSemanticSidecarRow(row))
	require.NoError(t, verifyStoredCompositePatternSemanticSidecar(row, scope))

	corrupt := row
	corrupt.CurrentURL = "/patterns/replaced.dxf"
	require.ErrorIs(t, verifyStoredCompositePatternSemanticSidecar(corrupt, scope),
		entity.ErrTechCardPatternSidecarCorrupt)
}
