package entity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const patternSidecarTestKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func patternSidecarTestPayload(techCardID, lockVersion int, lineKey, dxfSHA string, version int, receiptSHA string) []byte {
	return []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","dxf":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":"%s","version":%d,"sha256":"%s"}}}`,
		PatternSemanticSidecarMediaType, dxfSHA, receiptSHA, techCardID, lockVersion, lineKey, version, dxfSHA,
	))
}

func patternProductSidecarTestPayload(techCardID, lockVersion int, lineKey, dxfSHA string, version int, receiptSHA string) []byte {
	return []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-product-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","product":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":%d,"observedLockVersion":%d},"pattern":{"lineKey":"%s","version":%d,"sha256":"%s"}}}`,
		PatternProductSemanticSidecarMediaType, dxfSHA, receiptSHA, techCardID, lockVersion, lineKey, version, dxfSHA,
	))
}

func canonicalCompositeTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	canonical, err := canonicalExactJSON(raw)
	require.NoError(t, err)
	return canonical
}

func patternProductCompositeSidecarTestPayload(t *testing.T, techCardID, lockVersion int, lineKey, dxfSHA string, version int, receiptSHA, assemblyRevision, topologySHA string, selectedOperationNumber int) []byte {
	t.Helper()
	row := map[string]any{
		"techCardId": techCardID, "scopeKey": "fabric:outer", "patternLineKey": lineKey,
		"revision": 4, "cardLockVersion": lockVersion, "url": "https://files.example/front.dxf",
		"sizeBytes": 1234, "sha256": dxfSHA,
	}
	semantic := canonicalCompositeTestJSON(t, map[string]any{
		"schemaVersion": patternProductSemanticSidecarDocument,
		"mediaType":     PatternProductSemanticSidecarMediaType, "encoding": patternSemanticSidecarEncoding,
		"product": map[string]any{"scopeKey": "fabric:outer", "exportSha256": dxfSHA, "byteLength": 1234},
		"mapping": map[string]any{},
		"persistence": map[string]any{
			"receiptSealSha256": receiptSHA,
			"card":              map[string]any{"techCardId": techCardID, "observedLockVersion": lockVersion},
			"pattern":           map[string]any{"lineKey": lineKey, "url": "https://files.example/front.dxf", "scopeKey": "fabric:outer", "version": version, "sha256": dxfSHA},
		},
	})
	seamIDs := []string{"seam:left", "seam:right"}
	carrier := map[string]any{
		"schemaVersion": patternProductExactCarrierDocument, "status": "exact-phase-e-topology-authoring-binding",
		"bindings": map[string]any{
			"row": row, "serverAssemblyRevision": assemblyRevision, "assemblyTopologySha256": topologySHA,
			"selectedOperation":      map[string]any{"sourceOperationNumber": selectedOperationNumber, "authoringOperationId": "op:10", "localOperationOrder": 0, "seamIds": seamIDs},
			"catalogCanonicalSha256": strings.Repeat("d", 64), "proposalFingerprint": "pidpemsv2-fnv1a64:0123456789abcdef",
			"phaseEAggregatePlanSha256": strings.Repeat("e", 64),
		},
		"sides": []any{
			map[string]any{"seamId": seamIDs[0], "sideIndex": 0, "pieceLineKey": "piece:front", "pieceId": "front", "contourId": "front:outer", "boundaryVertexIds": []string{"f:0", "f:1"}, "direction": "forward"},
			map[string]any{"seamId": seamIDs[0], "sideIndex": 1, "pieceLineKey": "piece:back", "pieceId": "back", "contourId": "back:outer", "boundaryVertexIds": []string{"b:0", "b:1"}, "direction": "reverse"},
			map[string]any{"seamId": seamIDs[1], "sideIndex": 0, "pieceLineKey": "piece:front", "pieceId": "front", "contourId": "front:outer", "boundaryVertexIds": []string{"f:2", "f:3"}, "direction": "forward"},
			map[string]any{"seamId": seamIDs[1], "sideIndex": 1, "pieceLineKey": "piece:back", "pieceId": "back", "contourId": "back:outer", "boundaryVertexIds": []string{"b:2", "b:3"}, "direction": "reverse"},
		},
		"claims": patternProductExactCarrierClaimsV1,
	}
	carrierBytes := canonicalCompositeTestJSON(t, carrier)
	carrierDigest := sha256.Sum256(append([]byte(patternProductExactCarrierHashDomain+"\x00"), carrierBytes...))
	exact := canonicalCompositeTestJSON(t, map[string]any{
		"carrier": carrier,
		"seal": map[string]any{
			"schemaVersion": patternProductExactCarrierSealDocument, "algorithm": "SHA-256",
			"canonicalSha256": hex.EncodeToString(carrierDigest[:]), "canonicalByteLength": len(carrierBytes),
			"authority":             "unsigned-exact-authoring-subdocument-consistency-only",
			"persistenceIntegrated": false, "productionReady": false,
		},
	})
	component := func(kind, media string, bytes []byte) map[string]any {
		return map[string]any{"kind": kind, "mediaType": media, "sha256": PatternSemanticSidecarPayloadSHA256(bytes),
			"byteLength": len(bytes), "base64": base64.StdEncoding.EncodeToString(bytes)}
	}
	return canonicalCompositeTestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocument,
		"mediaType":     PatternProductCompositeSidecarMediaType, "encoding": patternSemanticSidecarEncoding,
		"bindings":   map[string]any{"row": row, "serverAssemblyRevision": assemblyRevision, "assemblyTopologySha256": topologySHA},
		"writeCas":   map[string]any{"expectedPreviousSidecarSha256": nil},
		"components": []any{component(patternProductCompositeSemanticKind, PatternProductSemanticSidecarMediaType, semantic), component(patternProductCompositeExactTopologyKind, patternProductCompositeExactMedia, exact)},
		"claims":     patternProductCompositeClaimsV3,
	})
}

func validPatternSemanticSidecarWrite() PatternSemanticSidecarWrite {
	lockVersion := 8
	payload := patternSidecarTestPayload(7, lockVersion, patternSidecarTestKey, strings.Repeat("a", 64), 3, strings.Repeat("b", 64))
	return PatternSemanticSidecarWrite{
		TechCardId:                    7,
		PatternLineKey:                strings.ToLower(patternSidecarTestKey),
		ExpectedCardLockVersion:       &lockVersion,
		ExpectedDxfSHA256:             strings.Repeat("a", 64),
		ExpectedPatternVersion:        3,
		ExpectedPostSaveReceiptSHA256: strings.Repeat("b", 64),
		MediaType:                     PatternSemanticSidecarMediaType,
		SidecarSHA256:                 PatternSemanticSidecarPayloadSHA256(payload),
		Payload:                       payload,
		By:                            " ann ",
	}
}

func TestValidatePatternSemanticSidecarWrite(t *testing.T) {
	in := validPatternSemanticSidecarWrite()
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&in))
	require.Equal(t, patternSidecarTestKey, in.PatternLineKey)
	require.Equal(t, "ann", in.By)
}

func TestValidatePatternSemanticSidecarWriteAcceptsPresentZeroCardLockVersion(t *testing.T) {
	in := validPatternSemanticSidecarWrite()
	zero := 0
	in.ExpectedCardLockVersion = &zero
	in.Payload = []byte(strings.Replace(string(in.Payload), `"observedLockVersion":8`, `"observedLockVersion":0`, 1))
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&in))
}

func TestValidatePatternSemanticSidecarWriteAcceptsVersionedProductEnvelope(t *testing.T) {
	in := validPatternSemanticSidecarWrite()
	in.MediaType = PatternProductSemanticSidecarMediaType
	in.Payload = patternProductSidecarTestPayload(
		in.TechCardId,
		*in.ExpectedCardLockVersion,
		in.PatternLineKey,
		in.ExpectedDxfSHA256,
		in.ExpectedPatternVersion,
		in.ExpectedPostSaveReceiptSHA256,
	)
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&in))

	legacySchema := append([]byte(nil), in.Payload...)
	legacySchema = []byte(strings.Replace(
		string(legacySchema),
		"pattern-import-dxf-product-semantic-sidecar-document/1",
		"pattern-import-dxf-semantic-sidecar-document/1",
		1,
	))
	in.Payload = legacySchema
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	require.Error(t, ValidatePatternSemanticSidecarWrite(&in))
}

func TestValidatePatternSemanticSidecarWriteRequiresExactCompositeCAS(t *testing.T) {
	in := validPatternSemanticSidecarWrite()
	in.PatternLineKey = patternSidecarTestKey
	savedRevision := uint32(4)
	previous := ""
	assemblyRevision := "asm:v1:0123456789abcdef0123456789abcdef:9"
	topologySHA := strings.Repeat("c", 64)
	in.ExpectedSavedRowRevision = &savedRevision
	in.ExpectedPreviousSidecarSHA256 = &previous
	in.ExpectedServerAssemblyRevision = &assemblyRevision
	in.ExpectedAssemblyTopologySHA256 = &topologySHA
	in.SelectedOperationNumber = 10
	in.MediaType = PatternProductCompositeSidecarMediaType
	in.Payload = patternProductCompositeSidecarTestPayload(t,
		in.TechCardId, *in.ExpectedCardLockVersion, in.PatternLineKey, in.ExpectedDxfSHA256,
		in.ExpectedPatternVersion, in.ExpectedPostSaveReceiptSHA256, assemblyRevision, topologySHA,
		in.SelectedOperationNumber,
	)
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&in))
	require.NoError(t, ValidatePatternProductCompositeSidecarBinding(in.Payload,
		PatternProductCompositeSidecarExpected{
			Row: SavedPatternRowIdentity{
				TechCardID: in.TechCardId, ScopeKey: "fabric:outer", PatternLineKey: in.PatternLineKey,
				SavedRowRevision: savedRevision, CardLockVersion: *in.ExpectedCardLockVersion,
				URL: "https://files.example/front.dxf", SizeBytes: 1234, SHA256: in.ExpectedDxfSHA256,
			},
			PatternVersion: in.ExpectedPatternVersion, PostSaveReceiptSHA256: in.ExpectedPostSaveReceiptSHA256,
			ExpectedPreviousSidecarSHA256: previous, ServerAssemblyRevision: assemblyRevision,
			AssemblyTopologySHA256: topologySHA, SelectedOperationNumber: in.SelectedOperationNumber,
		}))

	missingRowRevision := in
	missingRowRevision.ExpectedSavedRowRevision = nil
	require.Error(t, ValidatePatternSemanticSidecarWrite(&missingRowRevision))

	missingPriorCAS := in
	missingPriorCAS.ExpectedPreviousSidecarSHA256 = nil
	require.Error(t, ValidatePatternSemanticSidecarWrite(&missingPriorCAS))

	missingExactCarrier := in
	var missingExactDocument map[string]any
	require.NoError(t, json.Unmarshal(in.Payload, &missingExactDocument))
	missingExactDocument["components"] = missingExactDocument["components"].([]any)[:1]
	missingExactCarrier.Payload = canonicalCompositeTestJSON(t, missingExactDocument)
	missingExactCarrier.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(missingExactCarrier.Payload)
	require.Error(t, ValidatePatternSemanticSidecarWrite(&missingExactCarrier))
}

func TestPatternProductCompositeSidecarV3TypeScriptGolden(t *testing.T) {
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
		SelectedOperationNumber int    `json:"selectedOperationNumber"`
		ServerAssemblyRevision  string `json:"serverAssemblyRevision"`
		AssemblyTopologySHA256  string `json:"assemblyTopologySha256"`
		SemanticProductV1Base64 string `json:"semanticProductV1Base64"`
		ExactTopologyBase64     string `json:"exactTopologyBase64"`
		OuterBase64             string `json:"outerBase64"`
		OuterSHA256             string `json:"outerSha256"`
		OuterByteLength         int    `json:"outerByteLength"`
	}
	fixtureJSON, err := os.ReadFile("testdata/pattern_product_composite_v3_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fixtureJSON, &fixture))
	outer, err := base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)
	require.Equal(t, fixture.OuterByteLength, len(outer))
	require.Equal(t, fixture.OuterSHA256, PatternSemanticSidecarPayloadSHA256(outer))

	var document compositeDocumentV3
	require.NoError(t, decodeStrictJSON(outer, &document))
	require.Len(t, document.Components, 2)
	require.Equal(t, fixture.SemanticProductV1Base64, document.Components[0].Base64)
	require.Equal(t, fixture.ExactTopologyBase64, document.Components[1].Base64)
	require.EqualValues(t, 7, document.Bindings.Row.Revision,
		"saved-row revision is a dedicated safe integer, not pattern.version")

	row := SavedPatternRowIdentity{
		TechCardID: fixture.Row.TechCardID, ScopeKey: fixture.Row.ScopeKey,
		PatternLineKey: fixture.Row.PatternLineKey, SavedRowRevision: fixture.Row.Revision,
		CardLockVersion: fixture.Row.CardLockVersion, URL: fixture.Row.URL,
		SizeBytes: fixture.Row.SizeBytes, SHA256: fixture.Row.SHA256,
	}
	require.NoError(t, ValidatePatternProductCompositeSidecarBinding(outer,
		PatternProductCompositeSidecarExpected{
			Row: row, PatternVersion: 4, PostSaveReceiptSHA256: strings.Repeat("3", 64),
			ExpectedPreviousSidecarSHA256: "",
			ServerAssemblyRevision:        fixture.ServerAssemblyRevision,
			AssemblyTopologySHA256:        fixture.AssemblyTopologySHA256,
			SelectedOperationNumber:       fixture.SelectedOperationNumber,
		}))

	for _, driftedKey := range []string{strings.ToUpper(row.PatternLineKey), row.PatternLineKey + " "} {
		drifted := row
		drifted.PatternLineKey = driftedKey
		require.Error(t, ValidatePatternProductCompositeSidecarBinding(outer,
			PatternProductCompositeSidecarExpected{
				Row: drifted, PatternVersion: 4, PostSaveReceiptSHA256: strings.Repeat("3", 64),
				ServerAssemblyRevision:  fixture.ServerAssemblyRevision,
				AssemblyTopologySHA256:  fixture.AssemblyTopologySHA256,
				SelectedOperationNumber: fixture.SelectedOperationNumber,
			}), "pattern line identity must compare literally")
	}

	whitespace := append([]byte{' '}, outer...)
	require.Error(t, ValidatePatternProductCompositeSidecarBinding(whitespace,
		PatternProductCompositeSidecarExpected{Row: row}))
	reordered, err := json.Marshal(document) // struct field order is valid JSON but not TS canonical key order
	require.NoError(t, err)
	require.NotEqual(t, outer, reordered)
	require.Error(t, ValidatePatternProductCompositeSidecarBinding(reordered,
		PatternProductCompositeSidecarExpected{Row: row}))
}

func TestPatternProductCompositeSavedURLFailsClosed(t *testing.T) {
	require.True(t, validCompositeSavedURL("/patterns/main.dxf"))
	for _, value := range []string{"", "data:text/plain,x", "  JAVASCRIPT:alert(1)", "\ufeffdata:text/plain,x", "ok\x00bad", strings.Repeat("x", 4097)} {
		require.False(t, validCompositeSavedURL(value), value)
	}
	// U+0085 is not ECMAScript WhiteSpace and therefore does not expose a forbidden scheme after
	// trimStart. Keep the server verifier byte-for-byte compatible with the TypeScript decoder.
	require.True(t, validCompositeSavedURL("\u0085data:text/plain,x"))
	require.True(t, validCompositeServerAssemblyRevision("asm:v1:lineage:7"))
	for _, value := range []string{"", "123", " asm:v1:x:1", "asm:v1:x:1 ", "\ufeffasm:v1:x:1", "asm:v1:x:\x00", strings.Repeat("x", 513)} {
		require.False(t, validCompositeServerAssemblyRevision(value), value)
	}
	require.True(t, validCompositeServerAssemblyRevision("asm:v1:x:\u0085"))
	require.False(t, validCompositeSavedRow(compositeSavedRowV3{
		TechCardID: 1, ScopeKey: "main", PatternLineKey: "pattern:main", Revision: 1,
		URL: "/main.dxf", SizeBytes: maxCompositeJSSafeInteger + 1, SHA256: strings.Repeat("a", 64),
	}), "TypeScript cannot represent an exact row outside Number.MAX_SAFE_INTEGER")
}

func TestValidatePatternSemanticSidecarWriteRefusesHostileBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PatternSemanticSidecarWrite)
		field  string
	}{
		{"card", func(in *PatternSemanticSidecarWrite) { in.TechCardId = 0 }, "tech_card_id"},
		{"line", func(in *PatternSemanticSidecarWrite) { in.PatternLineKey = "not-a-key" }, "pattern_line_key"},
		{"missing card lock", func(in *PatternSemanticSidecarWrite) { in.ExpectedCardLockVersion = nil }, "expected_card_lock_version"},
		{"negative card lock", func(in *PatternSemanticSidecarWrite) { v := -1; in.ExpectedCardLockVersion = &v }, "expected_card_lock_version"},
		{"dxf digest", func(in *PatternSemanticSidecarWrite) { in.ExpectedDxfSHA256 = strings.Repeat("A", 64) }, "expected_dxf_sha256"},
		{"version", func(in *PatternSemanticSidecarWrite) { in.ExpectedPatternVersion = 0 }, "expected_pattern_version"},
		{"saved row revision", func(in *PatternSemanticSidecarWrite) { v := uint32(0); in.ExpectedSavedRowRevision = &v }, "expected_saved_row_revision"},
		{"previous sidecar digest", func(in *PatternSemanticSidecarWrite) { v := "no"; in.ExpectedPreviousSidecarSHA256 = &v }, "expected_previous_sidecar_sha256"},
		{"receipt", func(in *PatternSemanticSidecarWrite) { in.ExpectedPostSaveReceiptSHA256 = "no" }, "expected_post_save_receipt_sha256"},
		{"media type", func(in *PatternSemanticSidecarWrite) { in.MediaType = "application/json" }, "media_type"},
		{"empty", func(in *PatternSemanticSidecarWrite) { in.Payload = nil }, "sidecar_bytes"},
		{"oversize", func(in *PatternSemanticSidecarWrite) { in.Payload = make([]byte, PatternSemanticSidecarMaxBytes+1) }, "sidecar_bytes"},
		{"digest mismatch", func(in *PatternSemanticSidecarWrite) { in.SidecarSHA256 = strings.Repeat("c", 64) }, "sidecar_sha256"},
		{"invalid json", func(in *PatternSemanticSidecarWrite) {
			in.Payload = []byte(`{`)
			in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
		}, "sidecar_bytes"},
		{"inner binding", func(in *PatternSemanticSidecarWrite) {
			in.Payload = []byte(strings.Replace(string(in.Payload), `"techCardId":7`, `"techCardId":8`, 1))
			in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
		}, "sidecar_bytes"},
		{"inner card lock binding", func(in *PatternSemanticSidecarWrite) {
			in.Payload = []byte(strings.Replace(string(in.Payload), `"observedLockVersion":8`, `"observedLockVersion":9`, 1))
			in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
		}, "sidecar_bytes"},
		{"actor", func(in *PatternSemanticSidecarWrite) { in.By = " " }, "created_by"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validPatternSemanticSidecarWrite()
			tt.mutate(&in)
			err := ValidatePatternSemanticSidecarWrite(&in)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.field)
		})
	}
}
