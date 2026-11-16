package entity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type compositeV5TestFixture struct {
	Payload  []byte
	V4       []byte
	Expected PatternProductCompositeSidecarExpected
}

func canonicalV5TestJSON(t *testing.T, value any) []byte {
	t.Helper()
	bytes, err := canonicalGraphBytes(value)
	require.NoError(t, err)
	return bytes
}

func compositeV5TestComponent(kind, media string, payload []byte) map[string]any {
	return map[string]any{
		"kind": kind, "mediaType": media, "sha256": PatternSemanticSidecarPayloadSHA256(payload),
		"byteLength": len(payload), "base64": base64.StdEncoding.EncodeToString(payload),
	}
}

func buildCompositeV5TestFixture(t *testing.T) compositeV5TestFixture {
	t.Helper()
	row := map[string]any{
		"techCardId": 7, "scopeKey": "fabric:outer", "patternLineKey": patternSidecarTestKey,
		"revision": 4, "cardLockVersion": 8, "url": "https://files.example/front.dxf",
		"sizeBytes": 1234, "sha256": strings.Repeat("a", 64),
	}
	assemblyRevision := "asm:v1:0123456789abcdef0123456789abcdef:9"
	topologySHA := strings.Repeat("c", 64)
	receiptSHA := strings.Repeat("b", 64)
	bindings := map[string]any{
		"row": row, "serverAssemblyRevision": assemblyRevision, "assemblyTopologySha256": topologySHA,
	}
	semantic := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductSemanticSidecarDocument, "mediaType": PatternProductSemanticSidecarMediaType,
		"encoding": patternSemanticSidecarEncoding,
		"product":  map[string]any{"scopeKey": "fabric:outer", "exportSha256": strings.Repeat("a", 64), "byteLength": 1234},
		"mapping":  map[string]any{},
		"persistence": map[string]any{
			"receiptSealSha256": receiptSHA,
			"card":              map[string]any{"techCardId": 7, "observedLockVersion": 8},
			"pattern":           map[string]any{"lineKey": patternSidecarTestKey, "url": "https://files.example/front.dxf", "scopeKey": "fabric:outer", "version": 3, "sha256": strings.Repeat("a", 64)},
		},
	})
	v3 := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocument, "mediaType": PatternProductCompositeSidecarMediaType,
		"encoding": patternSemanticSidecarEncoding, "bindings": bindings,
		"writeCas":   map[string]any{"expectedPreviousSidecarSha256": nil},
		"components": []any{compositeV5TestComponent(patternProductCompositeSemanticKind, PatternProductSemanticSidecarMediaType, semantic)},
		"claims":     patternProductCompositeClaimsV3,
	})
	selectedOperation := map[string]any{
		"sourceOperationNumber": 10, "authoringOperationId": "op:10", "localOperationOrder": 0,
		"seamIds": []any{"seam:left"},
	}
	side := func(index int, piece string) map[string]any {
		return map[string]any{
			"seamId": "seam:left", "sideIndex": index, "pieceLineKey": "piece:" + piece,
			"pieceId": piece, "contourId": piece + ":outer",
			"boundaryVertexIds": []any{piece + ":0", piece + ":1"}, "direction": map[bool]string{true: "forward", false: "reverse"}[index == 0],
		}
	}
	unsignedCarrier := map[string]any{
		"schemaVersion": patternExactNCarrierDocumentV2, "status": "exact-structure-untrusted-authority", "authorityAvailable": false,
		"bindings": map[string]any{
			"row": row, "serverAssemblyRevision": assemblyRevision, "assemblyTopologySha256": topologySHA,
			"selectedOperation": selectedOperation, "catalogCanonicalSha256": strings.Repeat("d", 64),
			"phaseEAggregatePlanSha256": strings.Repeat("e", 64),
		},
		"seams":  []any{map[string]any{"seamId": "seam:left", "sides": []any{side(0, "front"), side(1, "back")}}},
		"claims": patternExactNClaimsV2,
	}
	fingerprintInput := append([]byte(patternExactNFingerprintDomainV2+"\x00"), canonicalV5TestJSON(t, unsignedCarrier)...)
	carrier := make(map[string]any, len(unsignedCarrier)+1)
	for key, value := range unsignedCarrier {
		carrier[key] = value
	}
	carrier["fingerprint"] = map[string]any{
		"algorithm": "SHA-256", "canonicalSha256": shaHex(fingerprintInput), "canonicalByteLength": len(fingerprintInput),
	}
	sealInput := append([]byte(patternExactNSealDomainV2+"\x00"), canonicalV5TestJSON(t, carrier)...)
	exactN := canonicalV5TestJSON(t, map[string]any{
		"carrier": carrier,
		"seal": map[string]any{
			"schemaVersion": patternExactNSealDocumentV2, "algorithm": "SHA-256",
			"boundCarrierFingerprintSha256": carrier["fingerprint"].(map[string]any)["canonicalSha256"],
			"canonicalSha256":               shaHex(sealInput), "canonicalByteLength": len(sealInput),
			"authority": "untrusted-exact-structure-consistency-only", "persistenceIntegrated": false, "productionReady": false,
		},
	})
	v4 := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocumentV4, "mediaType": PatternProductCompositeSidecarV4MediaType,
		"encoding": patternSemanticSidecarEncoding, "bindings": bindings,
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": nil},
		"components": []any{
			compositeV5TestComponent("composite-v3", PatternProductCompositeSidecarMediaType, v3),
			compositeV5TestComponent("phase-e-exact-n-seam-consistency-v2", patternProductCompositeExactNMediaV2, exactN),
		},
		"claims": patternProductCompositeClaimsV4,
	})
	anchor := func(vertex string, x, y float64) map[string]any {
		return map[string]any{
			"pieceId": "front", "pieceLineKey": "piece:front", "contourId": "front:outer", "vertexId": vertex,
			"pointMm": map[string]any{"x": x, "y": y},
		}
	}
	annotations := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternTopologyAnnotationsDocumentV1, "mediaType": patternTopologyAnnotationsMediaTypeV1,
		"encoding": patternSemanticSidecarEncoding,
		"bindings": map[string]any{
			"card":     map[string]any{"techCardId": 7, "cardLockVersion": 8},
			"scope":    map[string]any{"scopeKey": "fabric:outer", "patternLineKey": patternSidecarTestKey},
			"savedRow": map[string]any{"revision": 4, "dxfSha256": strings.Repeat("a", 64)},
			"source":   map[string]any{"identity": "DXF <>& 😀", "revision": "source:1", "sha256": strings.Repeat("f", 64)},
			"assembly": map[string]any{"serverRevision": assemblyRevision, "topologySha256": topologySHA},
		},
		"measurements": []any{
			map[string]any{"id": "measure:line", "kind": "line", "anchors": []any{anchor("front:0", 0.000001, 1e21), anchor("front:1", 20.5, -2.25)}, "valueMm": 20.5, "label": "Width <>& 😀"},
		},
		"callouts": []any{
			map[string]any{"schemaVersion": patternTopologyCalloutDocumentV1, "id": "callout:1", "anchor": anchor("front:0", 0.000001, 1e21), "text": "Fold <>& 😀"},
		},
	})
	payload := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocumentV5, "mediaType": PatternProductCompositeSidecarV5MediaType,
		"encoding": patternSemanticSidecarEncoding, "bindings": bindings,
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": nil},
		"components": []any{
			compositeV5TestComponent("composite-v4", PatternProductCompositeSidecarV4MediaType, v4),
			compositeV5TestComponent("phase-e-topology-annotations-v1", patternTopologyAnnotationsMediaTypeV1, annotations),
		},
		"claims": patternProductCompositeClaimsV5,
	})
	return compositeV5TestFixture{Payload: payload, V4: v4, Expected: PatternProductCompositeSidecarExpected{
		Row: SavedPatternRowIdentity{TechCardID: 7, ScopeKey: "fabric:outer", PatternLineKey: patternSidecarTestKey,
			SavedRowRevision: 4, CardLockVersion: 8, URL: "https://files.example/front.dxf", SizeBytes: 1234, SHA256: strings.Repeat("a", 64)},
		PatternVersion: 3, PostSaveReceiptSHA256: receiptSHA, ExpectedPreviousSidecarSHA256: "",
		ServerAssemblyRevision: assemblyRevision, AssemblyTopologySHA256: topologySHA, SelectedOperationNumber: 10,
	}}
}

func TestPatternProductCompositeSidecarV5Binding(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	require.NoError(t, ValidatePatternProductCompositeSidecarV4Binding(fixture.V4, fixture.Expected))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV4Exact(fixture.V4, fixture.Expected.Row, fixture.Expected.PatternVersion, fixture.Expected.PostSaveReceiptSHA256))
	require.NoError(t, ValidatePatternProductCompositeSidecarV5Binding(fixture.Payload, fixture.Expected))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV5Exact(fixture.Payload, fixture.Expected.Row, fixture.Expected.PatternVersion, fixture.Expected.PostSaveReceiptSHA256))
	require.NoError(t, ValidatePatternProductCompositeSidecarV5Transition(fixture.Payload, PatternProductCompositeSidecarV4MediaType, fixture.V4))
	require.NoError(t, ValidatePatternProductCompositeSidecarV5Transition(fixture.Payload, PatternProductCompositeSidecarV5MediaType, fixture.Payload))
}

func TestPatternProductCompositeSidecarV5TransitionRequiresAndPreservesV4(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	require.Error(t, ValidatePatternProductCompositeSidecarV5Transition(fixture.Payload, "", nil))
	require.Error(t, ValidatePatternProductCompositeSidecarV5Transition(fixture.Payload, PatternProductCompositeSidecarMediaType, fixture.V4))
	drifted := append([]byte(nil), fixture.V4...)
	drifted[len(drifted)-1] ^= 1
	require.Error(t, ValidatePatternProductCompositeSidecarV5Transition(fixture.Payload, PatternProductCompositeSidecarV4MediaType, drifted))
}

func TestPatternProductCompositeSidecarV4TransitionPreservesSemanticAndRejectsDowngrade(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	semantic, err := semanticV1BytesFromCompositeV4(fixture.V4)
	require.NoError(t, err)
	require.NoError(t, ValidatePatternProductCompositeSidecarV4Transition(fixture.V4, PatternProductSemanticSidecarMediaType, semantic))
	require.NoError(t, ValidatePatternProductCompositeSidecarV4Transition(fixture.V4, PatternProductCompositeSidecarV4MediaType, fixture.V4))
	require.Error(t, ValidatePatternProductCompositeSidecarV4Transition(fixture.V4, PatternProductCompositeSidecarV5MediaType, fixture.Payload))
	drifted := append([]byte(nil), semantic...)
	drifted[len(drifted)-1] ^= 1
	require.Error(t, ValidatePatternProductCompositeSidecarV4Transition(fixture.V4, PatternProductSemanticSidecarMediaType, drifted))
}

func TestPatternProductCompositeSidecarV5TypeScriptKAT(t *testing.T) {
	var fixture struct {
		Row                           compositeSavedRowV3 `json:"row"`
		PatternVersion                int                 `json:"patternVersion"`
		PostSaveReceiptSHA256         string              `json:"postSaveReceiptSha256"`
		ExpectedPreviousSidecarSHA256 string              `json:"expectedPreviousSidecarSha256"`
		ServerAssemblyRevision        string              `json:"serverAssemblyRevision"`
		AssemblyTopologySHA256        string              `json:"assemblyTopologySha256"`
		SelectedOperationNumber       int                 `json:"selectedOperationNumber"`
		OuterBase64                   string              `json:"outerBase64"`
		OuterSHA256                   string              `json:"outerSha256"`
		OuterByteLength               int                 `json:"outerByteLength"`
	}
	fixtureJSON, err := os.ReadFile("testdata/pattern_product_composite_v5_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fixtureJSON, &fixture))
	payload, err := base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)
	require.Equal(t, fixture.OuterByteLength, len(payload))
	require.Equal(t, fixture.OuterSHA256, PatternSemanticSidecarPayloadSHA256(payload))
	expected := PatternProductCompositeSidecarExpected{
		Row: compositeRowIdentity(fixture.Row), PatternVersion: fixture.PatternVersion,
		PostSaveReceiptSHA256:         fixture.PostSaveReceiptSHA256,
		ExpectedPreviousSidecarSHA256: fixture.ExpectedPreviousSidecarSHA256,
		ServerAssemblyRevision:        fixture.ServerAssemblyRevision,
		AssemblyTopologySHA256:        fixture.AssemblyTopologySHA256,
		SelectedOperationNumber:       fixture.SelectedOperationNumber,
	}
	require.NoError(t, ValidatePatternProductCompositeSidecarV5Binding(payload, expected))
	lockVersion := expected.Row.CardLockVersion
	savedRevision := expected.Row.SavedRowRevision
	previous := expected.ExpectedPreviousSidecarSHA256
	assemblyRevision := expected.ServerAssemblyRevision
	topologySHA := expected.AssemblyTopologySHA256
	write := PatternSemanticSidecarWrite{
		TechCardId: expected.Row.TechCardID, PatternLineKey: expected.Row.PatternLineKey,
		ExpectedCardLockVersion: &lockVersion, ExpectedDxfSHA256: expected.Row.SHA256,
		ExpectedPatternVersion: fixture.PatternVersion, ExpectedSavedRowRevision: &savedRevision,
		ExpectedPreviousSidecarSHA256: &previous, ExpectedServerAssemblyRevision: &assemblyRevision,
		ExpectedAssemblyTopologySHA256: &topologySHA, SelectedOperationNumber: fixture.SelectedOperationNumber,
		ExpectedPostSaveReceiptSHA256: fixture.PostSaveReceiptSHA256,
		MediaType:                     PatternProductCompositeSidecarV5MediaType, SidecarSHA256: fixture.OuterSHA256,
		Payload: payload, By: "v5-kat",
	}
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&write))
}

func TestPatternProductCompositeSidecarV5RejectsNoncanonicalAndComponentDrift(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	require.Error(t, ValidatePatternProductCompositeSidecarV5Binding(append([]byte{' '}, fixture.Payload...), fixture.Expected))

	var document map[string]any
	require.NoError(t, json.Unmarshal(fixture.Payload, &document))
	document["unexpected"] = true
	unknown, err := canonicalGraphBytes(document)
	require.NoError(t, err)
	require.Error(t, ValidatePatternProductCompositeSidecarV5Binding(unknown, fixture.Expected))

	components := document["components"].([]any)
	delete(document, "unexpected")
	components[0], components[1] = components[1], components[0]
	reordered, err := canonicalGraphBytes(document)
	require.NoError(t, err)
	require.Error(t, ValidatePatternProductCompositeSidecarV5Binding(reordered, fixture.Expected))
}

func TestPatternProductCompositeSidecarV5RejectsMissingAndComponentIntegrityDrift(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	for _, mutate := range []func(map[string]any){
		func(document map[string]any) { delete(document, "encoding") },
		func(document map[string]any) {
			component := document["components"].([]any)[0].(map[string]any)
			component["sha256"] = strings.Repeat("0", 64)
		},
		func(document map[string]any) {
			component := document["components"].([]any)[0].(map[string]any)
			component["byteLength"] = component["byteLength"].(float64) + 1
		},
		func(document map[string]any) {
			component := document["components"].([]any)[0].(map[string]any)
			component["base64"] = component["base64"].(string)[:len(component["base64"].(string))-1] + "A"
		},
	} {
		var document map[string]any
		require.NoError(t, json.Unmarshal(fixture.Payload, &document))
		mutate(document)
		payload, err := canonicalGraphBytes(document)
		require.NoError(t, err)
		require.Error(t, ValidatePatternProductCompositeSidecarV5Binding(payload, fixture.Expected))
	}
}

func TestPatternProductCompositeSidecarV5RejectsNestedV4V3AndExactNDrift(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	var outer compositeDocumentV3
	require.NoError(t, decodeStrictJSON(fixture.Payload, &outer))
	v4Bytes, err := compositeComponentBytes(outer.Components[0], "composite-v4", PatternProductCompositeSidecarV4MediaType)
	require.NoError(t, err)
	var v4 compositeDocumentV3
	require.NoError(t, decodeStrictJSON(v4Bytes, &v4))
	v3Bytes, err := compositeComponentBytes(v4.Components[0], "composite-v3", PatternProductCompositeSidecarMediaType)
	require.NoError(t, err)
	exactNBytes, err := compositeComponentBytes(v4.Components[1], "phase-e-exact-n-seam-consistency-v2", patternProductCompositeExactNMediaV2)
	require.NoError(t, err)

	var v3 map[string]any
	require.NoError(t, json.Unmarshal(v3Bytes, &v3))
	v3["components"] = append(v3["components"].([]any), map[string]any{
		"kind": patternProductCompositeExactTopologyKind, "mediaType": patternProductCompositeExactMedia,
		"sha256": strings.Repeat("0", 64), "byteLength": 1, "base64": "eA==",
	})
	driftedV3, err := canonicalGraphBytes(v3)
	require.NoError(t, err)
	require.Error(t, validateNestedCompositeV3ForV4(driftedV3, fixture.Expected.Row, fixture.Expected))

	var exactN map[string]any
	require.NoError(t, json.Unmarshal(exactNBytes, &exactN))
	carrier := exactN["carrier"].(map[string]any)
	bindings := carrier["bindings"].(map[string]any)
	operation := bindings["selectedOperation"].(map[string]any)
	operation["seamIds"] = []any{"seam:left", "seam:right"}
	driftedExactN, err := canonicalGraphBytes(exactN)
	require.NoError(t, err)
	require.Error(t, validateExactNSeamComponent(driftedExactN, fixture.Expected.Row, fixture.Expected))

	var v4Graph map[string]any
	require.NoError(t, json.Unmarshal(v4Bytes, &v4Graph))
	v4Graph["claims"].(map[string]any)["nestedCompositeV3BindingsVerified"] = false
	driftedV4, err := canonicalGraphBytes(v4Graph)
	require.NoError(t, err)
	require.Error(t, validateNestedCompositeV4(driftedV4, fixture.Expected.Row, fixture.Expected))
}

func TestPatternProductCompositeSidecarV5RejectsAnnotationBoundsNumbersAndBindings(t *testing.T) {
	fixture := buildCompositeV5TestFixture(t)
	var outer compositeDocumentV3
	require.NoError(t, decodeStrictJSON(fixture.Payload, &outer))
	annotations, err := compositeComponentBytes(outer.Components[1], "phase-e-topology-annotations-v1", patternTopologyAnnotationsMediaTypeV1)
	require.NoError(t, err)

	var graph map[string]any
	require.NoError(t, json.Unmarshal(annotations, &graph))
	bindings := graph["bindings"].(map[string]any)
	bindings["card"].(map[string]any)["techCardId"] = float64(8)
	drifted, err := canonicalGraphBytes(graph)
	require.NoError(t, err)
	require.Error(t, validateTopologyAnnotations(drifted, fixture.Expected.Row, fixture.Expected))

	require.NoError(t, json.Unmarshal(annotations, &graph))
	callout := graph["callouts"].([]any)[0].(map[string]any)
	callout["text"] = strings.Repeat("😀", 251)
	drifted, err = canonicalGraphBytes(graph)
	require.NoError(t, err)
	require.Error(t, validateTopologyAnnotations(drifted, fixture.Expected.Row, fixture.Expected))

	negativeZero := bytes.Replace(annotations, []byte(`"x":0.000001`), []byte(`"x":-0`), 1)
	require.NotEqual(t, annotations, negativeZero)
	require.Error(t, validateTopologyAnnotations(negativeZero, fixture.Expected.Row, fixture.Expected))
}

func TestCompositeV5ECMAScriptCanonicalNumbersAndText(t *testing.T) {
	for input, expected := range map[string]string{
		"0.000001": "0.000001", "1e-7": "1e-7", "1e20": "100000000000000000000", "1e21": "1e+21",
	} {
		formatted, err := formatECMAScriptNumber(input)
		require.NoError(t, err)
		require.Equal(t, expected, formatted)
	}
	_, err := formatECMAScriptNumber("-0")
	require.Error(t, err)
	require.True(t, boundedAnnotationText("<>& 😀", 500))
	require.True(t, boundedAnnotationText(strings.Repeat("😀", 250), 500))
	require.False(t, boundedAnnotationText(strings.Repeat("😀", 251), 500))
	require.False(t, boundedAnnotationText(" bad", 500))
	require.False(t, math.Signbit(0))
}
