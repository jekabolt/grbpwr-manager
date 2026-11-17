package entity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	PatternProductCompositeSidecarV7MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v7+json"

	patternProductCompositeSidecarDocumentV7        = "pattern-import-dxf-product-composite-sidecar-document/7"
	patternProductCompositeV7NestedKind             = "composite-v6"
	patternProductCompositeV7PhysicalKind           = "saved-full-assembly-physical-v1"
	savedFullAssemblyPhysicalDocumentV1             = "saved-full-assembly-physical-component/1"
	savedFullAssemblyPhysicalMediaTypeV1            = "application/vnd.grbpwr.saved-full-assembly-physical.v1+json"
	dxfGarmentPhysicalInitializationDocumentV1      = "dxf-garment-physical-initialization/1"
	dxfGarmentPhysicalInitializationMediaTypeV1     = "application/vnd.grbpwr.dxf-garment-physical-initialization.v1+json"
	dxfExactAssemblyGraphDocumentV1                 = "dxf-exact-assembly-graph/1"
	dxfExactAssemblyGraphFingerprintDomainV1        = "dxf-exact-assembly-graph-fingerprint/1"
	savedFullAssemblyPhysicalProfileV1              = "saved-full-assembly-physical/sewing-surface-zero-allowance/1"
	savedFullAssemblyNativeDrawnSewingProfileV1     = "saved-full-assembly-physical/native-drawn-sewing/1"
	savedFullAssemblyNativeDrawnSewingExporterV1    = "grbpwr.saved-native-drawn-sewing"
	savedFullAssemblyPhysicalReplayProfileV1        = "garment-material-contact-replay/1"
	savedFullAssemblyPhysicalInitializationMaxBytes = 256 * 1024
)

var patternProductCompositeClaimsV7 = map[string]bool{
	"nestedCompositeV6BytesPreservedExactly":               true,
	"registeredFullAssemblyPhysicalEnvelopeVerified":       true,
	"durableFullAssemblyPhysicalAuthorityVerified":         false,
	"completeGraphAndAllPieceInitializationAuthorityAdded": false,
	"exactSavedRowEnvelopeBound":                           true,
	"canonicalComponentOrderAndBase64Required":             true,
	"previousSidecarCasExpectationBound":                   true,
	"secondRegistryCreated":                                false,
	"secondWriterCreated":                                  false,
	"persistenceIntegrated":                                false,
	"productionReady":                                      false,
}

func validatePatternProductCompositeSidecarV7(
	payload []byte,
	expected PatternProductCompositeSidecarExpected,
	requireFullRow bool,
	requireAOContent bool,
) error {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return compositeV5Violation("invalid_composite_v7", "send a composite V7 within the 4 MiB production bound")
	}
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV7,
		PatternProductCompositeSidecarV7MediaType, patternProductCompositeClaimsV7)
	if err != nil {
		return compositeV5Violation("invalid_composite_v7", err.Error())
	}
	if err := validateExpectedCompositeBindings(document, row, previous, expected, requireFullRow); err != nil {
		return compositeV5Violation("composite_v7_binding_mismatch", err.Error())
	}
	if len(document.Components) != 2 {
		return compositeV5Violation("composite_v7_components_required", "include exactly composite-v6 followed by saved-full-assembly-physical-v1")
	}
	v6Bytes, err := compositeComponentBytes(document.Components[0], patternProductCompositeV7NestedKind,
		PatternProductCompositeSidecarV6MediaType)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	physicalBytes, err := compositeComponentBytes(document.Components[1], patternProductCompositeV7PhysicalKind,
		savedFullAssemblyPhysicalMediaTypeV1)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	if err := validateNestedCompositeV6ForV7(v6Bytes, row, expected); err != nil {
		return compositeV5Violation("nested_composite_v6_invalid", err.Error())
	}
	if err := validateSavedFullAssemblyPhysicalV1(physicalBytes, row, expected, requireAOContent); err != nil {
		return compositeV5Violation("saved_full_assembly_physical_invalid", err.Error())
	}
	return nil
}

// V7's transport anchor may name a different selected operation than the nested historical V6.
// The exact row and server Assembly revision must agree; the nested topology digest remains its own
// valid V6 authority and is preserved byte-for-byte by the V7 transition policy.
func validateNestedCompositeV6ForV7(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV6,
		PatternProductCompositeSidecarV6MediaType, patternProductCompositeClaimsV6)
	if err != nil {
		return fmt.Errorf("nested composite V6: %w", err)
	}
	if !exactCompositeRow(row, outerRow) || document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision {
		return fmt.Errorf("nested composite V6 saved-row or Assembly revision differs")
	}
	nestedExpected := expected
	nestedExpected.Row = outerRow
	nestedExpected.ExpectedPreviousSidecarSHA256 = previous
	nestedExpected.AssemblyTopologySHA256 = document.Bindings.AssemblyTopologySHA256
	return validatePatternProductCompositeSidecarV6(payload, nestedExpected, true)
}

func validateSavedFullAssemblyPhysicalV1(
	payload []byte,
	outerRow SavedPatternRowIdentity,
	expected PatternProductCompositeSidecarExpected,
	requireAOContent bool,
) error {
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return fmt.Errorf("decode Full Assembly physical component: %w", err)
	}
	document, ok := exactJSONObject(graph,
		"schemaVersion", "mediaType", "encoding", "profile", "bindings", "graph",
		"graphCanonicalSha256", "compileInputSha256", "physicalInitialization", "savedSourceReplay", "claims")
	profile, profileOK := document["profile"].(string)
	if !ok || !profileOK || document["schemaVersion"] != savedFullAssemblyPhysicalDocumentV1 ||
		document["mediaType"] != savedFullAssemblyPhysicalMediaTypeV1 ||
		document["encoding"] != patternSemanticSidecarEncoding ||
		(profile != savedFullAssemblyPhysicalProfileV1 && profile != savedFullAssemblyNativeDrawnSewingProfileV1) {
		return fmt.Errorf("Full Assembly physical schema differs")
	}
	canonical, err := canonicalJSONWithJSNumbers(payload)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("Full Assembly physical bytes are not canonical JSON")
	}

	bindings, ok := exactJSONObject(document["bindings"], "row", "serverAssemblyRevision", "aoContentSha256")
	if !ok {
		return fmt.Errorf("Full Assembly physical bindings differ")
	}
	row, ok := savedFullAssemblyExactRow(bindings["row"])
	serverRevision, revisionOK := bindings["serverAssemblyRevision"].(string)
	aoSHA, aoOK := bindings["aoContentSha256"].(string)
	if !ok || !revisionOK || serverRevision == "" || !aoOK || !ValidPatternSemanticSidecarSHA256(aoSHA) ||
		!exactCompositeRow(row, outerRow) || serverRevision != expected.ServerAssemblyRevision {
		return fmt.Errorf("Full Assembly physical saved-row or Assembly binding differs")
	}
	if requireAOContent {
		if !ValidPatternSemanticSidecarSHA256(expected.AssemblyOrderContentSHA256) ||
			aoSHA != expected.AssemblyOrderContentSHA256 {
			return fmt.Errorf("complete Assembly Order content digest differs")
		}
	}

	graphDocument, ok := exactJSONObject(document["graph"],
		"schemaVersion", "profile", "row", "serverAssemblyRevision", "source", "pieces", "operations", "seams", "compile")
	if !ok || graphDocument["schemaVersion"] != dxfExactAssemblyGraphDocumentV1 ||
		graphDocument["profile"] != "single-saved-row-size-adjacent-native-stitch" {
		return fmt.Errorf("exact Assembly graph schema differs")
	}
	graphRow, graphRowOK := savedFullAssemblyExactRow(graphDocument["row"])
	graphRevision, graphRevisionOK := graphDocument["serverAssemblyRevision"].(string)
	if !graphRowOK || !graphRevisionOK || !exactCompositeRow(graphRow, row) || graphRevision != serverRevision {
		return fmt.Errorf("exact Assembly graph authority differs")
	}
	if profile == savedFullAssemblyNativeDrawnSewingProfileV1 {
		if err := validateSavedNativeDrawnSewingSourceV1(graphDocument["source"], row); err != nil {
			return err
		}
	}
	graphSHA, graphSHAOK := document["graphCanonicalSha256"].(string)
	compileSHA, compileSHAOK := document["compileInputSha256"].(string)
	if !graphSHAOK || !compileSHAOK || !ValidPatternSemanticSidecarSHA256(graphSHA) ||
		!ValidPatternSemanticSidecarSHA256(compileSHA) {
		return fmt.Errorf("Full Assembly graph or compile digest differs")
	}
	graphCanonical, err := canonicalCompositeValue(graphDocument)
	if err != nil {
		return fmt.Errorf("canonicalize exact Assembly graph: %w", err)
	}
	graphDigest := sha256.Sum256(append([]byte(dxfExactAssemblyGraphFingerprintDomainV1+"\x00"), graphCanonical...))
	if hex.EncodeToString(graphDigest[:]) != graphSHA {
		return fmt.Errorf("exact Assembly graph fingerprint differs")
	}

	initialization, ok := exactJSONObject(document["physicalInitialization"],
		"mediaType", "canonicalSha256", "canonicalByteLength", "base64")
	initializationSHA, shaOK := valueOrNil(initialization, "canonicalSha256").(string)
	initializationLength, lengthOK := safeJSONInteger(valueOrNil(initialization, "canonicalByteLength"), 1)
	initializationBase64, base64OK := valueOrNil(initialization, "base64").(string)
	if !ok || initialization["mediaType"] != dxfGarmentPhysicalInitializationMediaTypeV1 || !shaOK ||
		!ValidPatternSemanticSidecarSHA256(initializationSHA) || !lengthOK ||
		initializationLength > savedFullAssemblyPhysicalInitializationMaxBytes || !base64OK {
		return fmt.Errorf("physical initialization metadata differs")
	}
	initializationBytes, err := base64.StdEncoding.Strict().DecodeString(initializationBase64)
	if err != nil || base64.StdEncoding.EncodeToString(initializationBytes) != initializationBase64 ||
		int64(len(initializationBytes)) != initializationLength ||
		PatternSemanticSidecarPayloadSHA256(initializationBytes) != initializationSHA {
		return fmt.Errorf("physical initialization bytes differ from their digest")
	}
	if err := validateSavedPhysicalInitializationV1(initializationBytes, graphSHA, compileSHA); err != nil {
		return err
	}

	replay, ok := exactJSONObject(document["savedSourceReplay"], "physicalSurface", "support", "replayProfile")
	surface, surfaceOK := exactJSONObject(valueOrNil(replay, "physicalSurface"), "mode", "seamAllowancePhysical")
	if !ok || !surfaceOK || surface["mode"] != "sewing-surface" || surface["seamAllowancePhysical"] != false ||
		replay["support"] != "none" || replay["replayProfile"] != savedFullAssemblyPhysicalReplayProfileV1 {
		return fmt.Errorf("saved physical replay policy differs")
	}
	if !validSavedFullAssemblyPhysicalClaims(document["claims"]) {
		return fmt.Errorf("Full Assembly physical claims differ")
	}
	return nil
}

// Native drawn sewing retains the original saved DXF as export authority. Canonical writer
// reconstruction is separate client evidence and must not replace this digest. This checks the
// persisted envelope only; it does not mint native contour or runtime authority on the server.
func validateSavedNativeDrawnSewingSourceV1(value any, row SavedPatternRowIdentity) error {
	source, ok := exactJSONObject(value,
		"sourceFileId", "nativeDxfSha256", "sizeToken", "handoffCanonicalSha256", "handoffSource")
	if !ok || source["nativeDxfSha256"] != row.SHA256 {
		return fmt.Errorf("native drawn sewing saved DXF binding differs")
	}
	handoff, ok := exactJSONObject(source["handoffSource"],
		"format", "documentId", "revision", "units", "sourceSha256", "exportSha256",
		"exporterId", "exporterVersion", "provenanceKind", "corpusEntryId")
	if !ok || handoff["format"] != "DXF" || handoff["units"] != "mm" ||
		handoff["exporterId"] != savedFullAssemblyNativeDrawnSewingExporterV1 ||
		handoff["exportSha256"] != row.SHA256 || handoff["provenanceKind"] != "untrusted-product" ||
		handoff["corpusEntryId"] != nil {
		return fmt.Errorf("native drawn sewing handoff source differs")
	}
	return nil
}

func validateSavedPhysicalInitializationV1(payload []byte, graphSHA, compileSHA string) error {
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return fmt.Errorf("decode physical initialization: %w", err)
	}
	document, ok := exactJSONObject(graph,
		"schemaVersion", "graphCanonicalSha256", "compileInputSha256", "convention", "placements",
		"observedTarget", "support", "replayProfile")
	if !ok || document["schemaVersion"] != dxfGarmentPhysicalInitializationDocumentV1 ||
		document["graphCanonicalSha256"] != graphSHA || document["compileInputSha256"] != compileSHA ||
		document["support"] != "none" || document["replayProfile"] != savedFullAssemblyPhysicalReplayProfileV1 {
		return fmt.Errorf("physical initialization duplicate bindings differ")
	}
	canonical, err := canonicalJSONWithJSNumbers(payload)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("physical initialization bytes are not canonical JSON")
	}
	return nil
}

func savedFullAssemblyExactRow(value any) (SavedPatternRowIdentity, bool) {
	record, ok := exactJSONObject(value,
		"techCardId", "scopeKey", "patternLineKey", "revision", "cardLockVersion", "url", "sizeBytes", "sha256")
	if !ok {
		return SavedPatternRowIdentity{}, false
	}
	techCardID, techCardOK := safeJSONInteger(record["techCardId"], 1)
	revision, revisionOK := safeJSONInteger(record["revision"], 1)
	cardLockVersion, lockOK := safeJSONInteger(record["cardLockVersion"], 0)
	sizeBytes, sizeOK := safeJSONInteger(record["sizeBytes"], 1)
	scopeKey, scopeOK := record["scopeKey"].(string)
	patternLineKey, lineOK := record["patternLineKey"].(string)
	url, urlOK := record["url"].(string)
	sha, shaOK := record["sha256"].(string)
	if !techCardOK || !revisionOK || !lockOK || !sizeOK || !scopeOK || scopeKey == "" ||
		!lineOK || patternLineKey == "" || !urlOK || url == "" || !shaOK || !ValidPatternSemanticSidecarSHA256(sha) {
		return SavedPatternRowIdentity{}, false
	}
	if revision > int64(^uint32(0)) || techCardID > int64(^uint(0)>>1) ||
		cardLockVersion > int64(^uint(0)>>1) {
		return SavedPatternRowIdentity{}, false
	}
	return SavedPatternRowIdentity{
		TechCardID: int(techCardID), ScopeKey: scopeKey, PatternLineKey: patternLineKey,
		SavedRowRevision: uint32(revision), CardLockVersion: int(cardLockVersion), URL: url,
		SizeBytes: sizeBytes, SHA256: sha,
	}, true
}

func canonicalCompositeValue(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := appendCanonicalJSONValue(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func validSavedFullAssemblyPhysicalClaims(value any) bool {
	claims, ok := exactJSONObject(value,
		"completeGraphPersisted", "allPiecePhysicalInitializationPersisted", "exactSavedRowBound",
		"exactServerAssemblyRevisionBound", "completeAssemblyOrderContentBound", "physicalSurface",
		"seamAllowancePhysical", "support", "replayProfile", "contentConsistencyOnly", "productAuthority", "runtimeAuthority")
	return ok && claims["completeGraphPersisted"] == true &&
		claims["allPiecePhysicalInitializationPersisted"] == true && claims["exactSavedRowBound"] == true &&
		claims["exactServerAssemblyRevisionBound"] == true && claims["completeAssemblyOrderContentBound"] == true &&
		claims["physicalSurface"] == "sewing-surface" && claims["seamAllowancePhysical"] == false &&
		claims["support"] == "none" && claims["replayProfile"] == savedFullAssemblyPhysicalReplayProfileV1 &&
		claims["contentConsistencyOnly"] == true && claims["productAuthority"] == false && claims["runtimeAuthority"] == false
}

func compositeV7ComponentsForTransition(payload []byte) ([]byte, []byte, error) {
	document, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV7,
		PatternProductCompositeSidecarV7MediaType, patternProductCompositeClaimsV7)
	if err != nil || len(document.Components) != 2 {
		return nil, nil, fmt.Errorf("composite V7 is invalid")
	}
	v6, err := compositeComponentBytes(document.Components[0], patternProductCompositeV7NestedKind,
		PatternProductCompositeSidecarV6MediaType)
	if err != nil {
		return nil, nil, err
	}
	physical, err := compositeComponentBytes(document.Components[1], patternProductCompositeV7PhysicalKind,
		savedFullAssemblyPhysicalMediaTypeV1)
	if err != nil {
		return nil, nil, err
	}
	return v6, physical, nil
}

// ValidatePatternProductCompositeSidecarV7Transition preserves the proven V6 chain exactly. A
// first V7 wraps the currently stored V6; later V7 writes may replace the Full Assembly physical
// component but cannot silently rewrite the historical composite chain.
func ValidatePatternProductCompositeSidecarV7Transition(nextPayload []byte, priorMediaType string, priorPayload []byte) error {
	nextV6, _, err := compositeV7ComponentsForTransition(nextPayload)
	if err != nil {
		return compositeV5Violation("invalid_composite_v7_transition", err.Error())
	}
	var priorV6 []byte
	switch priorMediaType {
	case PatternProductCompositeSidecarV6MediaType:
		priorV6 = priorPayload
	case PatternProductCompositeSidecarV7MediaType:
		priorV6, _, err = compositeV7ComponentsForTransition(priorPayload)
		if err != nil {
			return compositeV5Violation("composite_v7_prior_required", err.Error())
		}
	default:
		return compositeV5Violation("composite_v7_prior_required", fmt.Sprintf("prior media type %q is not composite V6 or V7", priorMediaType))
	}
	if !bytes.Equal(nextV6, priorV6) {
		return compositeV5Violation("composite_v7_nested_v6_changed", "Composite V7 must preserve the current Composite V6 bytes exactly")
	}
	return nil
}

// ValidatePatternProductCompositeSidecarV7Binding is the under-lock V7 admission gate. The AO
// digest is independently recomputed from current server data by the store and is mandatory here.
func ValidatePatternProductCompositeSidecarV7Binding(payload []byte, expected PatternProductCompositeSidecarExpected) error {
	if expected.Row.ScopeKey == "" || expected.Row.URL == "" || expected.Row.SizeBytes <= 0 {
		return compositeV5Violation("server_saved_row_incomplete", "the server cannot prove an exact saved row")
	}
	if !ValidPatternSemanticSidecarSHA256(expected.AssemblyOrderContentSHA256) {
		return compositeV5Violation("server_assembly_order_digest_incomplete", "the server cannot prove complete Assembly Order content")
	}
	return validatePatternProductCompositeSidecarV7(payload, expected, true, true)
}

func validateStoredPatternProductCompositeSidecarV7(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string, requireFullRow bool) error {
	document, _, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV7,
		PatternProductCompositeSidecarV7MediaType, patternProductCompositeClaimsV7)
	if err != nil {
		return compositeV5Violation("invalid_composite_v7", "stored composite V7 cannot be decoded")
	}
	return validatePatternProductCompositeSidecarV7(payload, PatternProductCompositeSidecarExpected{
		Row: row, PatternVersion: patternVersion, PostSaveReceiptSHA256: receiptSHA,
		ExpectedPreviousSidecarSHA256: previous,
		ServerAssemblyRevision:        document.Bindings.ServerAssemblyRevision,
		AssemblyTopologySHA256:        document.Bindings.AssemblyTopologySHA256,
	}, requireFullRow, false)
}

// ValidateStoredPatternProductCompositeSidecarV7Exact revalidates persisted envelope/component
// integrity and exact saved-row binding. Current AO freshness is intentionally re-proved only by a
// fresh saved-assembly snapshot or the locked write admission path.
func ValidateStoredPatternProductCompositeSidecarV7Exact(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecarV7(payload, row, patternVersion, receiptSHA, true)
}
