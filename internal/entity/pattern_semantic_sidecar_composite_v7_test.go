package entity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type compositeV7TestFixture struct {
	Payload  []byte
	V6       []byte
	Physical []byte
	Expected PatternProductCompositeSidecarExpected
}

func buildCompositeV7TestFixture(t *testing.T) compositeV7TestFixture {
	t.Helper()
	return buildCompositeV7TestFixtureWithProfile(t, savedFullAssemblyPhysicalProfileV1)
}

func buildCompositeV7TestFixtureWithProfile(t *testing.T, profile string) compositeV7TestFixture {
	t.Helper()
	v6 := buildCompositeV6TestFixture(t)
	expected := v6.Expected
	expected.ExpectedPreviousSidecarSHA256 = PatternSemanticSidecarPayloadSHA256(v6.Payload)
	// V7 may use another selected operation as its transport anchor; nested V6 keeps its own valid
	// topology receipt while exact row + server Assembly revision remain equal.
	expected.AssemblyTopologySHA256 = strings.Repeat("f", 64)
	expected.AssemblyOrderContentSHA256 = strings.Repeat("9", 64)

	row := map[string]any{
		"techCardId": expected.Row.TechCardID, "scopeKey": expected.Row.ScopeKey,
		"patternLineKey": expected.Row.PatternLineKey, "revision": int64(expected.Row.SavedRowRevision),
		"cardLockVersion": expected.Row.CardLockVersion, "url": expected.Row.URL,
		"sizeBytes": expected.Row.SizeBytes, "sha256": expected.Row.SHA256,
	}
	graph := map[string]any{
		"schemaVersion":          dxfExactAssemblyGraphDocumentV1,
		"profile":                "single-saved-row-size-adjacent-native-stitch",
		"row":                    row,
		"serverAssemblyRevision": expected.ServerAssemblyRevision,
		"source":                 map[string]any{"sourceFileId": "source:1"},
		"pieces":                 []any{},
		"operations":             []any{},
		"seams":                  []any{},
		"compile":                map[string]any{},
	}
	if profile == savedFullAssemblyNativeDrawnSewingProfileV1 {
		graph["source"] = map[string]any{
			"sourceFileId": "source:1", "nativeDxfSha256": expected.Row.SHA256,
			"sizeToken": "M", "handoffCanonicalSha256": strings.Repeat("6", 64),
			"handoffSource": map[string]any{
				"format": "DXF", "documentId": "document:1", "revision": "revision:1", "units": "mm",
				"sourceSha256": strings.Repeat("5", 64), "exportSha256": expected.Row.SHA256,
				"exporterId": savedFullAssemblyNativeDrawnSewingExporterV1, "exporterVersion": "1",
				"provenanceKind": "untrusted-product", "corpusEntryId": nil,
			},
		}
	}
	graphCanonical, err := canonicalCompositeValue(graph)
	require.NoError(t, err)
	graphDigest := sha256.Sum256(append([]byte(dxfExactAssemblyGraphFingerprintDomainV1+"\x00"), graphCanonical...))
	graphSHA := hex.EncodeToString(graphDigest[:])
	compileSHA := strings.Repeat("8", 64)
	initialization := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion":        dxfGarmentPhysicalInitializationDocumentV1,
		"graphCanonicalSha256": graphSHA,
		"compileInputSha256":   compileSHA,
		"convention":           map[string]any{},
		"placements":           []any{},
		"observedTarget":       map[string]any{},
		"support":              "none",
		"replayProfile":        savedFullAssemblyPhysicalReplayProfileV1,
	})
	physical := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": savedFullAssemblyPhysicalDocumentV1,
		"mediaType":     savedFullAssemblyPhysicalMediaTypeV1,
		"encoding":      patternSemanticSidecarEncoding,
		"profile":       profile,
		"bindings": map[string]any{
			"row": row, "serverAssemblyRevision": expected.ServerAssemblyRevision,
			"aoContentSha256": expected.AssemblyOrderContentSHA256,
		},
		"graph": graph, "graphCanonicalSha256": graphSHA, "compileInputSha256": compileSHA,
		"physicalInitialization": map[string]any{
			"mediaType":           dxfGarmentPhysicalInitializationMediaTypeV1,
			"canonicalSha256":     PatternSemanticSidecarPayloadSHA256(initialization),
			"canonicalByteLength": len(initialization), "base64": base64.StdEncoding.EncodeToString(initialization),
		},
		"savedSourceReplay": map[string]any{
			"physicalSurface": map[string]any{"mode": "sewing-surface", "seamAllowancePhysical": false},
			"support":         "none", "replayProfile": savedFullAssemblyPhysicalReplayProfileV1,
		},
		"claims": map[string]any{
			"completeGraphPersisted": true, "allPiecePhysicalInitializationPersisted": true,
			"exactSavedRowBound": true, "exactServerAssemblyRevisionBound": true,
			"completeAssemblyOrderContentBound": true, "physicalSurface": "sewing-surface",
			"seamAllowancePhysical": false, "support": "none",
			"replayProfile": savedFullAssemblyPhysicalReplayProfileV1, "contentConsistencyOnly": true,
			"productAuthority": false, "runtimeAuthority": false,
		},
	})
	payload := canonicalV5TestJSON(t, map[string]any{
		"schemaVersion": patternProductCompositeSidecarDocumentV7,
		"mediaType":     PatternProductCompositeSidecarV7MediaType,
		"encoding":      patternSemanticSidecarEncoding,
		"bindings": map[string]any{
			"row": row, "serverAssemblyRevision": expected.ServerAssemblyRevision,
			"assemblyTopologySha256": expected.AssemblyTopologySHA256,
		},
		"writeCas": map[string]any{"expectedPreviousSidecarSha256": expected.ExpectedPreviousSidecarSHA256},
		"components": []any{
			compositeV5TestComponent(patternProductCompositeV7NestedKind, PatternProductCompositeSidecarV6MediaType, v6.Payload),
			compositeV5TestComponent(patternProductCompositeV7PhysicalKind, savedFullAssemblyPhysicalMediaTypeV1, physical),
		},
		"claims": patternProductCompositeClaimsV7,
	})
	return compositeV7TestFixture{Payload: payload, V6: v6.Payload, Physical: physical, Expected: expected}
}

func TestPatternProductCompositeSidecarV7BindingStoredReadAndTransition(t *testing.T) {
	fixture := buildCompositeV7TestFixture(t)
	require.True(t, ValidPatternSemanticSidecarMediaType(PatternProductCompositeSidecarV7MediaType))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, fixture.Expected))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV7Exact(
		fixture.Payload, fixture.Expected.Row, fixture.Expected.PatternVersion, fixture.Expected.PostSaveReceiptSHA256))
	require.NoError(t, ValidatePatternSemanticSidecarPayloadBinding(
		fixture.Payload, fixture.Expected.Row.TechCardID, fixture.Expected.Row.PatternLineKey,
		fixture.Expected.Row.CardLockVersion, fixture.Expected.Row.SHA256, fixture.Expected.PatternVersion,
		fixture.Expected.Row.SavedRowRevision, fixture.Expected.PostSaveReceiptSHA256,
		PatternProductCompositeSidecarV7MediaType))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(
		fixture.Payload, PatternProductCompositeSidecarV6MediaType, fixture.V6))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(
		fixture.Payload, PatternProductCompositeSidecarV7MediaType, fixture.Payload))
}

func TestPatternProductCompositeSidecarV7RequiresServerAOContentDigest(t *testing.T) {
	fixture := buildCompositeV7TestFixture(t)
	missing := fixture.Expected
	missing.AssemblyOrderContentSHA256 = ""
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, missing))
	wrong := fixture.Expected
	wrong.AssemblyOrderContentSHA256 = strings.Repeat("0", 64)
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, wrong))
}

func TestPatternProductCompositeSidecarV7NativeDrawnSewingProfile(t *testing.T) {
	fixture := buildCompositeV7TestFixtureWithProfile(t, savedFullAssemblyNativeDrawnSewingProfileV1)
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, fixture.Expected))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV7Exact(
		fixture.Payload, fixture.Expected.Row, fixture.Expected.PatternVersion, fixture.Expected.PostSaveReceiptSHA256))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(
		fixture.Payload, PatternProductCompositeSidecarV6MediaType, fixture.V6))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(
		fixture.Payload, PatternProductCompositeSidecarV7MediaType, fixture.Payload))

	wrongAO := fixture.Expected
	wrongAO.AssemblyOrderContentSHA256 = strings.Repeat("0", 64)
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, wrongAO))
	wrongCAS := fixture.Expected
	wrongCAS.ExpectedPreviousSidecarSHA256 = strings.Repeat("0", 64)
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(fixture.Payload, wrongCAS))
}

func TestPatternProductCompositeSidecarV7NativeDrawnSewingRejectsSourceSubstitution(t *testing.T) {
	fixture := buildCompositeV7TestFixtureWithProfile(t, savedFullAssemblyNativeDrawnSewingProfileV1)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"unknown profile", func(p map[string]any) { p["profile"] = "saved-full-assembly-physical/unknown/1" }, "physical schema differs"},
		{"writer exporter", func(p map[string]any) {
			p["graph"].(map[string]any)["source"].(map[string]any)["handoffSource"].(map[string]any)["exporterId"] = "grbpwr.pattern-import.writeDxfDetailed"
		}, "native drawn sewing handoff source differs"},
		{"writer hash as export", func(p map[string]any) {
			p["graph"].(map[string]any)["source"].(map[string]any)["handoffSource"].(map[string]any)["exportSha256"] = strings.Repeat("0", 64)
		}, "native drawn sewing handoff source differs"},
		{"different native bytes", func(p map[string]any) {
			p["graph"].(map[string]any)["source"].(map[string]any)["nativeDxfSha256"] = strings.Repeat("0", 64)
		}, "native drawn sewing saved DXF binding differs"},
		{"trusted corpus substitution", func(p map[string]any) {
			p["graph"].(map[string]any)["source"].(map[string]any)["handoffSource"].(map[string]any)["provenanceKind"] = "trusted-corpus"
		}, "native drawn sewing handoff source differs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var physical, outer map[string]any
			require.NoError(t, json.Unmarshal(fixture.Physical, &physical))
			require.NoError(t, json.Unmarshal(fixture.Payload, &outer))
			test.mutate(physical)
			outer["components"].([]any)[1] = compositeV5TestComponent(
				patternProductCompositeV7PhysicalKind, savedFullAssemblyPhysicalMediaTypeV1, canonicalV5TestJSON(t, physical))
			require.ErrorContains(t, ValidatePatternProductCompositeSidecarV7Binding(
				canonicalV5TestJSON(t, outer), fixture.Expected), test.want)
		})
	}
}

func TestPatternProductCompositeSidecarV7RequestValidationKeepsExistingWriteSurface(t *testing.T) {
	fixture := buildCompositeV7TestFixture(t)
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
		MediaType:                     PatternProductCompositeSidecarV7MediaType,
		SidecarSHA256:                 PatternSemanticSidecarPayloadSHA256(fixture.Payload), Payload: fixture.Payload, By: "v7-test",
	}
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&write),
		"AO digest is server-recomputed under lock and requires no new RPC request field")
}

func TestPatternProductCompositeSidecarV7RejectsGraphPhysicalAndNestedDrift(t *testing.T) {
	fixture := buildCompositeV7TestFixture(t)
	mutateOuter := func(mutate func(map[string]any)) []byte {
		t.Helper()
		var graph map[string]any
		require.NoError(t, json.Unmarshal(fixture.Payload, &graph))
		mutate(graph)
		return canonicalV5TestJSON(t, graph)
	}

	badPhysicalDigest := mutateOuter(func(graph map[string]any) {
		graph["components"].([]any)[1].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	})
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(badPhysicalDigest, fixture.Expected))

	var physical map[string]any
	require.NoError(t, json.Unmarshal(fixture.Physical, &physical))
	physical["graphCanonicalSha256"] = strings.Repeat("0", 64)
	driftedPhysical := canonicalV5TestJSON(t, physical)
	badGraphFingerprint := mutateOuter(func(graph map[string]any) {
		graph["components"].([]any)[1] = compositeV5TestComponent(
			patternProductCompositeV7PhysicalKind, savedFullAssemblyPhysicalMediaTypeV1, driftedPhysical)
	})
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(badGraphFingerprint, fixture.Expected))

	var nested map[string]any
	require.NoError(t, json.Unmarshal(fixture.V6, &nested))
	nested["bindings"].(map[string]any)["serverAssemblyRevision"] = "asm:v1:0123456789abcdef0123456789abcdef:99"
	driftedV6 := canonicalV5TestJSON(t, nested)
	badNested := mutateOuter(func(graph map[string]any) {
		graph["components"].([]any)[0] = compositeV5TestComponent(
			patternProductCompositeV7NestedKind, PatternProductCompositeSidecarV6MediaType, driftedV6)
	})
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(badNested, fixture.Expected))
}

func TestPatternProductCompositeSidecarV7TransitionPreservesNestedV6(t *testing.T) {
	fixture := buildCompositeV7TestFixture(t)
	var outer map[string]any
	require.NoError(t, json.Unmarshal(fixture.Payload, &outer))
	outer["writeCas"].(map[string]any)["expectedPreviousSidecarSha256"] = PatternSemanticSidecarPayloadSHA256(fixture.Payload)

	var physical map[string]any
	require.NoError(t, json.Unmarshal(fixture.Physical, &physical))
	physical["bindings"].(map[string]any)["aoContentSha256"] = strings.Repeat("7", 64)
	changedPhysical := canonicalV5TestJSON(t, physical)
	outer["components"].([]any)[1] = compositeV5TestComponent(
		patternProductCompositeV7PhysicalKind, savedFullAssemblyPhysicalMediaTypeV1, changedPhysical)
	next := canonicalV5TestJSON(t, outer)
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(
		next, PatternProductCompositeSidecarV7MediaType, fixture.Payload))

	changedV6 := append([]byte(nil), fixture.V6...)
	changedV6[len(changedV6)-1] = ' '
	outer["components"].([]any)[0] = compositeV5TestComponent(
		patternProductCompositeV7NestedKind, PatternProductCompositeSidecarV6MediaType, changedV6)
	drifted := canonicalV5TestJSON(t, outer)
	require.Error(t, ValidatePatternProductCompositeSidecarV7Transition(
		drifted, PatternProductCompositeSidecarV7MediaType, fixture.Payload))
}
