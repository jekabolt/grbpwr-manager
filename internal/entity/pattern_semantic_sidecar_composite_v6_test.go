package entity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type compositeV6TestFixture struct {
	Payload     []byte
	V5          []byte
	Arrangement []byte
	Expected    PatternProductCompositeSidecarExpected
}

func buildCompositeV6TestFixture(t *testing.T) compositeV6TestFixture {
	t.Helper()
	v5 := buildCompositeV5TestFixture(t)
	arrangementGraph := map[string]any{
		"schemaVersion": mannequinArrangementDocumentV1,
		"mediaType":     mannequinArrangementMediaTypeV1,
		"encoding":      patternSemanticSidecarEncoding,
		"bindings": map[string]any{
			"card": map[string]any{
				"techCardId": v5.Expected.Row.TechCardID, "cardLockVersion": v5.Expected.Row.CardLockVersion,
			},
			"scope": map[string]any{
				"scopeKey": v5.Expected.Row.ScopeKey, "patternLineKey": v5.Expected.Row.PatternLineKey, "sizeToken": "M",
			},
			"savedRow": map[string]any{
				"revision": int64(v5.Expected.Row.SavedRowRevision), "dxfSha256": v5.Expected.Row.SHA256,
			},
			"source": map[string]any{
				"catalogCanonicalSha256": strings.Repeat("d", 64), "sizeSelectionCanonicalSha256": strings.Repeat("e", 64),
			},
			"mannequin": map[string]any{
				"baseModelId": 12, "modelFormRevisionKeyV1": "model:12:revision:3",
				"poseBinding": map[string]any{"canonicalKey": "pose:a-rest"},
			},
		},
		"pieces": []any{
			map[string]any{
				"pieceKey": "piece:back", "scopeKey": v5.Expected.Row.ScopeKey, "pieceId": "back", "pieceLineKey": "piece:back",
				"transform": map[string]any{"offsetM": []any{-0.25, 0, 1e-7}, "rotationRad": -0.5},
			},
			map[string]any{
				"pieceKey": "piece:front", "scopeKey": v5.Expected.Row.ScopeKey, "pieceId": "front", "pieceLineKey": "piece:front",
				"transform": map[string]any{"offsetM": []any{0.25, 1, 0}, "rotationRad": 0.5},
			},
		},
		"claims": map[string]any{
			"presentationOnly": true, "dxfAuthority": false, "assemblyOrderAuthority": false,
			"clothAuthority": false, "scalePersisted": false,
		},
	}
	arrangement, err := canonicalMannequinArrangementV1(arrangementGraph)
	require.NoError(t, err)

	expected := v5.Expected
	expected.ExpectedPreviousSidecarSHA256 = PatternSemanticSidecarPayloadSHA256(v5.Payload)
	bindings := map[string]any{
		"row": map[string]any{
			"techCardId": expected.Row.TechCardID, "scopeKey": expected.Row.ScopeKey,
			"patternLineKey": expected.Row.PatternLineKey, "revision": int64(expected.Row.SavedRowRevision),
			"cardLockVersion": expected.Row.CardLockVersion, "url": expected.Row.URL,
			"sizeBytes": expected.Row.SizeBytes, "sha256": expected.Row.SHA256,
		},
		"serverAssemblyRevision": expected.ServerAssemblyRevision,
		"assemblyTopologySha256": expected.AssemblyTopologySHA256,
	}
	payload := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocumentV6,
		"mediaType":     PatternProductCompositeSidecarV6MediaType,
		"encoding":      patternSemanticSidecarEncoding,
		"bindings":      bindings,
		"writeCas":      map[string]any{"expectedPreviousSidecarSha256": expected.ExpectedPreviousSidecarSHA256},
		"components": []any{
			compositeV5TestComponent("composite-v5", PatternProductCompositeSidecarV5MediaType, v5.Payload),
			compositeV5TestComponent("cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1, arrangement),
		},
		"claims": patternProductCompositeClaimsV6,
	})
	return compositeV6TestFixture{Payload: payload, V5: v5.Payload, Arrangement: arrangement, Expected: expected}
}

func TestPatternProductCompositeSidecarV6BindingAndExactByteRoundTrip(t *testing.T) {
	fixture := buildCompositeV6TestFixture(t)
	require.True(t, ValidPatternSemanticSidecarMediaType(PatternProductCompositeSidecarV6MediaType))
	require.NoError(t, ValidatePatternProductCompositeSidecarV6Binding(fixture.Payload, fixture.Expected))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV6Exact(
		fixture.Payload, fixture.Expected.Row, fixture.Expected.PatternVersion, fixture.Expected.PostSaveReceiptSHA256))
	require.NoError(t, ValidatePatternSemanticSidecarPayloadBinding(
		fixture.Payload, fixture.Expected.Row.TechCardID, fixture.Expected.Row.PatternLineKey,
		fixture.Expected.Row.CardLockVersion, fixture.Expected.Row.SHA256, fixture.Expected.PatternVersion,
		fixture.Expected.Row.SavedRowRevision, fixture.Expected.PostSaveReceiptSHA256,
		PatternProductCompositeSidecarV6MediaType), "GET must revalidate stored V6 payloads")
	require.NoError(t, ValidatePatternProductCompositeSidecarV6Transition(
		fixture.Payload, PatternProductCompositeSidecarV5MediaType, fixture.V5))
	require.NoError(t, ValidatePatternProductCompositeSidecarV6Transition(
		fixture.Payload, PatternProductCompositeSidecarV6MediaType, fixture.Payload))

	var outer compositeDocumentV3
	require.NoError(t, decodeStrictJSON(fixture.Payload, &outer))
	nestedV5, err := compositeComponentBytes(outer.Components[0], "composite-v5", PatternProductCompositeSidecarV5MediaType)
	require.NoError(t, err)
	arrangement, err := compositeComponentBytes(outer.Components[1], "cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1)
	require.NoError(t, err)
	require.True(t, bytes.Equal(fixture.V5, nestedV5), "V5 component bytes must round-trip exactly")
	require.True(t, bytes.Equal(fixture.Arrangement, arrangement), "arrangement bytes must round-trip exactly")
}

func TestPatternProductCompositeSidecarV6WriteAdmissionIncludesCAS(t *testing.T) {
	fixture := buildCompositeV6TestFixture(t)
	lockVersion := fixture.Expected.Row.CardLockVersion
	savedRevision := fixture.Expected.Row.SavedRowRevision
	previous := fixture.Expected.ExpectedPreviousSidecarSHA256
	assemblyRevision := fixture.Expected.ServerAssemblyRevision
	topologySHA := fixture.Expected.AssemblyTopologySHA256
	write := PatternSemanticSidecarWrite{
		TechCardId: fixture.Expected.Row.TechCardID, PatternLineKey: fixture.Expected.Row.PatternLineKey,
		ExpectedCardLockVersion: &lockVersion, ExpectedDxfSHA256: fixture.Expected.Row.SHA256,
		ExpectedPatternVersion: fixture.Expected.PatternVersion, ExpectedSavedRowRevision: &savedRevision,
		ExpectedPreviousSidecarSHA256: &previous, ExpectedServerAssemblyRevision: &assemblyRevision,
		ExpectedAssemblyTopologySHA256: &topologySHA, SelectedOperationNumber: fixture.Expected.SelectedOperationNumber,
		ExpectedPostSaveReceiptSHA256: fixture.Expected.PostSaveReceiptSHA256,
		MediaType:                     PatternProductCompositeSidecarV6MediaType, SidecarSHA256: PatternSemanticSidecarPayloadSHA256(fixture.Payload),
		Payload: fixture.Payload, By: "v6-test",
	}
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&write))

	wrong := strings.Repeat("0", 64)
	write.ExpectedPreviousSidecarSHA256 = &wrong
	require.Error(t, ValidatePatternSemanticSidecarWrite(&write), "outer writeCas must equal request CAS")
}

func TestPatternProductCompositeSidecarV6RejectsCanonicalSHAAndNestedBindingDrift(t *testing.T) {
	fixture := buildCompositeV6TestFixture(t)
	require.Error(t, ValidatePatternProductCompositeSidecarV6Binding(
		append([]byte{' '}, fixture.Payload...), fixture.Expected), "outer bytes must be canonical")

	mutateOuter := func(mutate func(map[string]any)) []byte {
		t.Helper()
		var graph map[string]any
		require.NoError(t, json.Unmarshal(fixture.Payload, &graph))
		mutate(graph)
		return canonicalV5TestJSON(t, graph)
	}
	badSHA := mutateOuter(func(graph map[string]any) {
		graph["components"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	})
	require.Error(t, ValidatePatternProductCompositeSidecarV6Binding(badSHA, fixture.Expected))

	var nestedGraph map[string]any
	require.NoError(t, json.Unmarshal(fixture.V5, &nestedGraph))
	nestedGraph["bindings"].(map[string]any)["row"].(map[string]any)["scopeKey"] = "fabric:wrong"
	driftedV5 := canonicalV5TestJSON(t, nestedGraph)
	nestedBindingDrift := mutateOuter(func(graph map[string]any) {
		graph["components"].([]any)[0] = compositeV5TestComponent(
			"composite-v5", PatternProductCompositeSidecarV5MediaType, driftedV5)
	})
	require.Error(t, ValidatePatternProductCompositeSidecarV6Binding(nestedBindingDrift, fixture.Expected))
}

func TestPatternProductCompositeSidecarV6RejectsArrangementAndV5TransitionDrift(t *testing.T) {
	fixture := buildCompositeV6TestFixture(t)
	var arrangement map[string]any
	require.NoError(t, json.Unmarshal(fixture.Arrangement, &arrangement))
	arrangement["bindings"].(map[string]any)["savedRow"].(map[string]any)["dxfSha256"] = strings.Repeat("0", 64)
	driftedArrangement, err := canonicalMannequinArrangementV1(arrangement)
	require.NoError(t, err)

	var outer map[string]any
	require.NoError(t, json.Unmarshal(fixture.Payload, &outer))
	outer["components"].([]any)[1] = compositeV5TestComponent(
		"cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1, driftedArrangement)
	driftedPayload := canonicalV5TestJSON(t, outer)
	require.Error(t, ValidatePatternProductCompositeSidecarV6Binding(driftedPayload, fixture.Expected))

	noncanonicalArrangement := append([]byte{' '}, fixture.Arrangement...)
	outer["components"].([]any)[1] = compositeV5TestComponent(
		"cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1, noncanonicalArrangement)
	noncanonicalPayload := canonicalV5TestJSON(t, outer)
	require.Error(t, ValidatePatternProductCompositeSidecarV6Binding(noncanonicalPayload, fixture.Expected))

	var priorV5Graph map[string]any
	require.NoError(t, json.Unmarshal(fixture.V5, &priorV5Graph))
	priorV5Graph["writeCas"].(map[string]any)["expectedPreviousSidecarSha256"] = strings.Repeat("1", 64)
	driftedV5 := canonicalV5TestJSON(t, priorV5Graph)
	require.Error(t, ValidatePatternProductCompositeSidecarV6Transition(
		fixture.Payload, PatternProductCompositeSidecarV5MediaType, driftedV5),
		"a canonical but byte-different current V5 must not be replaced by the nested V5")
	require.Error(t, ValidatePatternProductCompositeSidecarV6Transition(
		fixture.Payload, PatternProductCompositeSidecarV4MediaType, fixture.V5))
}

func TestPatternProductCompositeSidecarV6ArrangementUsesFrontendJSONOrder(t *testing.T) {
	fixture := buildCompositeV6TestFixture(t)
	require.True(t, bytes.HasPrefix(fixture.Arrangement,
		[]byte(`{"schemaVersion":"grbpwr.cad-mannequin-arrangement/v1","mediaType":`)))
	require.Contains(t, string(fixture.Arrangement), `"offsetM":[-0.25,0,1e-7],"rotationRad":-0.5`)
	require.Equal(t, fixture.Arrangement, []byte(base64RoundTrip(t, fixture.Arrangement)))
}

func base64RoundTrip(t *testing.T, payload []byte) string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(payload)
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	require.NoError(t, err)
	return string(decoded)
}
