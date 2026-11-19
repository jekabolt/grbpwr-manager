package entity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	patternProductCompositeSidecarDocumentV4 = "pattern-import-dxf-product-composite-sidecar-document/4"
	patternProductCompositeExactNMediaV2     = "application/vnd.grbpwr.pattern-dxf-product-phase-e-exact-n-seam-consistency.v2+json"
	patternProductCompositeSidecarDocumentV5 = "pattern-import-dxf-product-composite-sidecar-document/5"
	patternTopologyAnnotationsDocumentV1     = "pattern-import-dxf-phase-e-topology-annotations/1"
	patternTopologyAnnotationsMediaTypeV1    = "application/vnd.grbpwr.pattern-dxf-phase-e-topology-annotations.v1+json"
	patternTopologyCalloutDocumentV1         = "pattern-import-dxf-phase-e-callout-session/1"
	patternExactNCarrierDocumentV2           = "pattern-import-dxf-phase-e-exact-n-seam-consistency-carrier/2"
	patternExactNSealDocumentV2              = "pattern-import-dxf-phase-e-exact-n-seam-consistency-seal/2"
	patternExactNFingerprintDomainV2         = "pattern-import-dxf-phase-e-exact-n-seam-consistency-carrier-fingerprint-v2"
	patternExactNSealDomainV2                = "pattern-import-dxf-phase-e-exact-n-seam-consistency-seal-v2"
	patternV5MaxAnnotations                  = 20_000
)

var patternProductCompositeClaimsV4 = map[string]bool{
	"exactSavedRowBound": true, "exactServerAssemblyRevisionBound": true,
	"exactAssemblyTopologySha256Bound": true, "nestedCompositeV3BytesVerified": true,
	"nestedCompositeV3BindingsVerified": true, "nestedCompositeV3ExactTopologyForbidden": true,
	"exactNConsistencyCarrierAndSealVerified": true, "exactNConsistencyRestoreProjectionAvailable": true,
	"exactNAuthorityRestorePatchAvailable": false, "canonicalComponentOrderAndBase64Required": true,
	"previousSidecarCasExpectationBound": true, "twoSeamV1BypassCreated": false,
	"persistenceIntegrated": false, "sidecarGetPerformed": false, "sidecarPutPerformed": false,
	"productionReady": false,
}

var patternProductCompositeClaimsV5 = map[string]bool{
	"exactSavedRowBound": true, "exactServerAssemblyRevisionBound": true,
	"exactAssemblyTopologySha256Bound": true, "exactSourceRevisionAndSha256Bound": true,
	"nestedCompositeV4BytesPreservedExactly": true, "topologyAnnotationsCanonicalAndDigestVerified": true,
	"exactContourVertexAndMillimetreCoordinatesRequired": true,
	"canonicalComponentOrderAndBase64Required":           true, "previousSidecarCasExpectationBound": true,
	"autosaveAllowed": false, "localStorageAllowed": false, "persistenceIntegrated": false,
	"productionReady": false,
}

var patternExactNClaimsV2 = map[string]bool{
	"orderedSeamsBound": true, "exactlyTwoOrderedSidesPerSeamBound": true,
	"stableExactTopologyIdentitiesBound": true, "fullCandidateFingerprintBound": true,
	"carrierSealBound": true, "backendShapeChangeRequired": false,
	"opaqueSemanticSidecarCanCarryFutureCodec": true, "callerCandidatePromotedToAuthority": false,
	"proposalChoicesAuthorityAvailable": false, "persistenceIntegrated": false,
	"productionReady": false,
}

var exactNStableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/#-]{0,255}$`)
var annotationStableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// These sizes and the vertex grammar match the DXF V1 provenance builder. Only
// existing annotation anchor fields may carry this qualified slash syntax.
const (
	annotationDXFHex     = `([0-9a-f]{2})+`
	annotationDXFOrdinal = `(0|[1-9][0-9]*)`
	annotationDXFEntity  = `(h/` + annotationDXFHex + `|p/` + annotationDXFOrdinal + `)`
	annotationDXFInsert  = `b/` + annotationDXFHex + `/` + annotationDXFEntity +
		`/o/` + annotationDXFOrdinal + `/r/` + annotationDXFOrdinal + `/c/` + annotationDXFOrdinal
	annotationDXFVertexPrefix = "dxf-vertex-v1/"
)

var annotationDXFContourID = regexp.MustCompile(`^dxf-piece-v1/p/` + annotationDXFHex +
	`/s/` + annotationDXFHex + `/g/(cut|seam)$`)
var annotationDXFVertexID = regexp.MustCompile(`^` + annotationDXFVertexPrefix +
	`dxf-curve-v1/f/` + annotationDXFHex + `/i/(m|n/` + annotationDXFInsert + `(/x/` + annotationDXFInsert + `)*)` +
	`/e/` + annotationDXFEntity + `/s/` + annotationDXFOrdinal + `/p/` + annotationDXFOrdinal +
	`/sample/` + annotationDXFOrdinal + `$`)

func compositeV5Violation(code, detail string) error {
	return NewFieldViolation("sidecar_bytes", code, "", detail)
}

// canonicalJSONWithJSNumbers mirrors the frontend's sorted-key canonical JSON. encoding/json's
// float spelling follows Go's exponent thresholds, not JSON.stringify's, so annotations use the
// explicit ECMAScript number formatter below. Integer-only V3/V4/V5 envelopes continue to use the
// older canonicalExactJSON verifier.
func canonicalJSONWithJSNumbers(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	var out bytes.Buffer
	if err := appendCanonicalJSONValue(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func appendCanonicalJSONValue(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case string:
		if !utf8.ValidString(typed) {
			return fmt.Errorf("invalid UTF-8 string")
		}
		appendCompositeJSONString(out, typed)
	case json.Number:
		formatted, err := formatECMAScriptNumber(string(typed))
		if err != nil {
			return err
		}
		out.WriteString(formatted)
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		formatted, err := formatECMAScriptNumber(strconv.FormatFloat(typed, 'g', -1, 64))
		if err != nil {
			return err
		}
		out.WriteString(formatted)
	case []any:
		out.WriteByte('[')
		for index, child := range typed {
			if index != 0 {
				out.WriteByte(',')
			}
			if err := appendCanonicalJSONValue(out, child); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys) // every contract key is ASCII, matching UTF-16 sort order
		out.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				out.WriteByte(',')
			}
			if err := appendCanonicalJSONValue(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendCanonicalJSONValue(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case map[string]bool:
		converted := make(map[string]any, len(typed))
		for key, child := range typed {
			converted[key] = child
		}
		return appendCanonicalJSONValue(out, converted)
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func appendCompositeJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, scalar := range value {
		switch scalar {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(scalar)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if scalar < 0x20 {
				out.WriteString(`\u00`)
				const hexDigits = "0123456789abcdef"
				out.WriteByte(hexDigits[byte(scalar)>>4])
				out.WriteByte(hexDigits[byte(scalar)&0x0f])
			} else {
				out.WriteRune(scalar)
			}
		}
	}
	out.WriteByte('"')
}

func formatECMAScriptNumber(raw string) (string, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value == 0 && math.Signbit(value) {
		return "", fmt.Errorf("invalid canonical number %q", raw)
	}
	if value == 0 {
		return "0", nil
	}
	negative := value < 0
	if negative {
		value = -value
	}
	shortest := strconv.FormatFloat(value, 'g', -1, 64)
	mantissa := shortest
	exponent := 0
	if at := strings.IndexByte(shortest, 'e'); at >= 0 {
		mantissa = shortest[:at]
		exponent, err = strconv.Atoi(shortest[at+1:])
		if err != nil {
			return "", err
		}
	}
	dot := strings.IndexByte(mantissa, '.')
	digitsBeforeDot := len(mantissa)
	if dot >= 0 {
		digitsBeforeDot = dot
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return "0", nil
	}
	leadingRemoved := len(mantissa) - len(digits)
	decimalPosition := digitsBeforeDot + exponent - leadingRemoved
	adjustedExponent := decimalPosition - 1
	var result string
	if adjustedExponent >= -6 && adjustedExponent < 21 {
		switch {
		case decimalPosition <= 0:
			result = "0." + strings.Repeat("0", -decimalPosition) + digits
		case decimalPosition >= len(digits):
			result = digits + strings.Repeat("0", decimalPosition-len(digits))
		default:
			result = digits[:decimalPosition] + "." + digits[decimalPosition:]
		}
	} else {
		result = digits[:1]
		if len(digits) > 1 {
			result += "." + digits[1:]
		}
		if adjustedExponent >= 0 {
			result += "e+" + strconv.Itoa(adjustedExponent)
		} else {
			result += "e" + strconv.Itoa(adjustedExponent)
		}
	}
	if negative {
		result = "-" + result
	}
	return result, nil
}

func decodeJSONGraph(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	return value, nil
}

func exactJSONObject(value any, keys ...string) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	if !ok || len(record) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if _, exists := record[key]; !exists {
			return nil, false
		}
	}
	return record, true
}

func safeJSONInteger(value any, minimum int64) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || !compositeIntegerJSON.MatchString(string(number)) || string(number) == "-0" {
		return 0, false
	}
	integer, err := strconv.ParseInt(string(number), 10, 64)
	return integer, err == nil && integer >= minimum && integer <= maxCompositeJSSafeInteger
}

func exactBoolClaims(value any, expected map[string]bool) bool {
	record, ok := value.(map[string]any)
	if !ok || len(record) != len(expected) {
		return false
	}
	for key, want := range expected {
		got, ok := record[key].(bool)
		if !ok || got != want {
			return false
		}
	}
	return true
}

func exactCompositeEnvelopeShape(value any, expectedClaims map[string]bool) bool {
	document, ok := exactJSONObject(value, "schemaVersion", "mediaType", "encoding", "bindings", "writeCas", "components", "claims")
	if !ok || !exactBoolClaims(document["claims"], expectedClaims) {
		return false
	}
	bindings, ok := exactJSONObject(document["bindings"], "row", "serverAssemblyRevision", "assemblyTopologySha256")
	if !ok {
		return false
	}
	if _, ok = exactJSONObject(bindings["row"], "techCardId", "scopeKey", "patternLineKey", "revision", "cardLockVersion", "url", "sizeBytes", "sha256"); !ok {
		return false
	}
	if _, ok = exactJSONObject(document["writeCas"], "expectedPreviousSidecarSha256"); !ok {
		return false
	}
	components, ok := document["components"].([]any)
	if !ok {
		return false
	}
	for _, component := range components {
		if _, ok := exactJSONObject(component, "kind", "mediaType", "sha256", "byteLength", "base64"); !ok {
			return false
		}
	}
	return true
}

func validOuterCompositeDocument(payload []byte, expectedDocument, expectedMedia string, expectedClaims map[string]bool) (compositeDocumentV3, SavedPatternRowIdentity, string, error) {
	var document compositeDocumentV3
	graph, err := decodeJSONGraph(payload)
	if err != nil || !exactCompositeEnvelopeShape(graph, expectedClaims) || decodeStrictJSON(payload, &document) != nil {
		return document, SavedPatternRowIdentity{}, "", fmt.Errorf("invalid exact composite envelope")
	}
	canonical, err := canonicalExactJSON(payload)
	if err != nil || !bytes.Equal(canonical, payload) {
		return document, SavedPatternRowIdentity{}, "", fmt.Errorf("composite envelope is not canonical JSON")
	}
	row := compositeRowIdentity(document.Bindings.Row)
	if document.SchemaVersion != expectedDocument || document.MediaType != expectedMedia ||
		document.Encoding != patternSemanticSidecarEncoding || !validCompositeSavedRow(document.Bindings.Row) ||
		!validCompositeServerAssemblyRevision(document.Bindings.ServerAssemblyRevision) ||
		!ValidPatternSemanticSidecarSHA256(document.Bindings.AssemblyTopologySHA256) {
		return document, row, "", fmt.Errorf("invalid composite schema or bindings")
	}
	previous, err := decodeCompositePreviousSHA(document.WriteCAS.ExpectedPreviousSidecarSHA256)
	if err != nil {
		return document, row, "", err
	}
	return document, row, previous, nil
}

func validateExpectedCompositeBindings(document compositeDocumentV3, row SavedPatternRowIdentity, previous string, expected PatternProductCompositeSidecarExpected, requireFullRow bool) error {
	if expected.Row.TechCardID != row.TechCardID || expected.Row.PatternLineKey != row.PatternLineKey ||
		expected.Row.SavedRowRevision != row.SavedRowRevision || expected.Row.CardLockVersion != row.CardLockVersion ||
		expected.Row.SHA256 != row.SHA256 || requireFullRow && !exactCompositeRow(expected.Row, row) {
		return fmt.Errorf("saved row binding mismatch")
	}
	if previous != expected.ExpectedPreviousSidecarSHA256 ||
		document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision ||
		document.Bindings.AssemblyTopologySHA256 != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("assembly or previous-sidecar binding mismatch")
	}
	return nil
}

func validateNestedCompositeV3ForV4(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	document, row, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocument,
		PatternProductCompositeSidecarMediaType, patternProductCompositeClaimsV3)
	if err != nil {
		return fmt.Errorf("nested composite V3: %w", err)
	}
	if !exactCompositeRow(row, outerRow) || document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision ||
		document.Bindings.AssemblyTopologySHA256 != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("nested composite V3 authority differs")
	}
	if len(document.Components) < 1 || len(document.Components) > 2 {
		return fmt.Errorf("nested composite V3 must omit fixed two-seam exact topology")
	}
	semanticBytes, err := compositeComponentBytes(document.Components[0], patternProductCompositeSemanticKind, PatternProductSemanticSidecarMediaType)
	if err != nil {
		return err
	}
	semanticCanonical, canonicalErr := canonicalJSONWithJSNumbers(semanticBytes)
	if canonicalErr != nil || !bytes.Equal(semanticCanonical, semanticBytes) {
		return fmt.Errorf("nested semantic-product V1 is not canonical JSON")
	}
	if err := validateNestedSemanticProductEnvelope(semanticBytes); err != nil {
		return err
	}
	if err := validateCompositeSemanticComponent(semanticBytes, outerRow, expected.PatternVersion, expected.PostSaveReceiptSHA256); err != nil {
		return fmt.Errorf("nested semantic component: %w", err)
	}
	if len(document.Components) == 2 {
		placementBytes, err := compositeComponentBytes(document.Components[1], patternProductCompositePlacementKind, patternProductCompositePlacementMedia)
		if err != nil {
			return err
		}
		canonical, canonicalErr := canonicalJSONWithJSNumbers(placementBytes)
		if canonicalErr != nil || !bytes.Equal(canonical, placementBytes) {
			return fmt.Errorf("nested placement V2 is not canonical JSON")
		}
		if err := validateNestedPlacementBindings(placementBytes, outerRow, expected); err != nil {
			return err
		}
	}
	return nil
}

func validateNestedSemanticProductEnvelope(payload []byte) error {
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return fmt.Errorf("decode nested semantic-product V1: %w", err)
	}
	document, ok := graph.(map[string]any)
	if !ok || document["schemaVersion"] != patternProductSemanticSidecarDocument ||
		document["mediaType"] != PatternProductSemanticSidecarMediaType || document["encoding"] != patternSemanticSidecarEncoding {
		return fmt.Errorf("nested semantic-product V1 document fields differ")
	}
	if _, ok := document["product"].(map[string]any); !ok {
		return fmt.Errorf("nested semantic-product V1 product fields differ")
	}
	if _, ok := document["mapping"].(map[string]any); !ok {
		return fmt.Errorf("nested semantic-product V1 mapping differs")
	}
	persistence, ok := document["persistence"].(map[string]any)
	if !ok {
		return fmt.Errorf("nested semantic-product V1 persistence fields differ")
	}
	if _, ok := persistence["card"].(map[string]any); !ok {
		return fmt.Errorf("nested semantic-product V1 card fields differ")
	}
	if _, ok := persistence["pattern"].(map[string]any); !ok {
		return fmt.Errorf("nested semantic-product V1 pattern fields differ")
	}
	return nil
}

func validateNestedPlacementBindings(payload []byte, row SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return fmt.Errorf("decode nested placement V2: %w", err)
	}
	document, ok := graph.(map[string]any)
	if !ok || document["schemaVersion"] != "pattern-import-dxf-product-multiseam-placement-sidecar-document/2" ||
		document["mediaType"] != patternProductCompositePlacementMedia || document["encoding"] != patternSemanticSidecarEncoding {
		return fmt.Errorf("nested placement V2 schema mismatch")
	}
	bindings, ok := document["bindings"].(map[string]any)
	if !ok {
		return fmt.Errorf("nested placement V2 bindings missing")
	}
	placementRow, ok := bindings["row"].(map[string]any)
	if !ok {
		return fmt.Errorf("nested placement V2 row missing")
	}
	techCardID, okTech := safeJSONInteger(placementRow["techCardId"], 1)
	lockVersion, okLock := safeJSONInteger(placementRow["cardLockVersion"], 0)
	revision, okRevision := safeJSONInteger(placementRow["revision"], 0)
	patternVersion, okVersion := safeJSONInteger(placementRow["patternVersion"], 1)
	topology, ok := bindings["topology"].(map[string]any)
	if !ok || !okTech || !okLock || !okRevision || !okVersion ||
		int(techCardID) != row.TechCardID || int(lockVersion) != row.CardLockVersion || uint32(revision) != row.SavedRowRevision ||
		int(patternVersion) != expected.PatternVersion || placementRow["scopeKey"] != row.ScopeKey ||
		placementRow["patternLineKey"] != row.PatternLineKey || placementRow["patternUrl"] != row.URL ||
		placementRow["dxfSha256"] != row.SHA256 || placementRow["postSaveReceiptSha256"] != expected.PostSaveReceiptSHA256 ||
		topology["assemblyTopologySha256"] != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("nested placement V2 authority differs")
	}
	return nil
}

func validateNestedCompositeV4(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	document, row, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV4,
		PatternProductCompositeSidecarV4MediaType, patternProductCompositeClaimsV4)
	if err != nil {
		return fmt.Errorf("nested composite V4: %w", err)
	}
	if !exactCompositeRow(row, outerRow) || document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision ||
		document.Bindings.AssemblyTopologySHA256 != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("nested composite V4 authority differs")
	}
	if len(document.Components) != 2 {
		return fmt.Errorf("nested composite V4 requires exactly two ordered components")
	}
	v3Bytes, err := compositeComponentBytes(document.Components[0], "composite-v3", PatternProductCompositeSidecarMediaType)
	if err != nil {
		return err
	}
	exactNBytes, err := compositeComponentBytes(document.Components[1], "phase-e-exact-n-seam-consistency-v2", patternProductCompositeExactNMediaV2)
	if err != nil {
		return err
	}
	if err := validateNestedCompositeV3ForV4(v3Bytes, row, expected); err != nil {
		return err
	}
	if err := validateExactNSeamComponent(exactNBytes, row, expected); err != nil {
		return err
	}
	return nil
}

func validateExactNSeamComponent(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	canonical, err := canonicalExactJSON(payload)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("exact-N component is not canonical JSON")
	}
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return err
	}
	envelope, ok := exactJSONObject(graph, "carrier", "seal")
	if !ok {
		return fmt.Errorf("exact-N envelope fields differ")
	}
	carrier, ok := exactJSONObject(envelope["carrier"], "schemaVersion", "status", "authorityAvailable", "bindings", "seams", "fingerprint", "claims")
	if !ok || carrier["schemaVersion"] != patternExactNCarrierDocumentV2 ||
		carrier["status"] != "exact-structure-untrusted-authority" || carrier["authorityAvailable"] != false ||
		!exactBoolClaims(carrier["claims"], patternExactNClaimsV2) {
		return fmt.Errorf("exact-N carrier fields differ")
	}
	bindings, ok := exactJSONObject(carrier["bindings"], "row", "serverAssemblyRevision", "assemblyTopologySha256", "selectedOperation", "catalogCanonicalSha256", "phaseEAggregatePlanSha256")
	if !ok {
		return fmt.Errorf("exact-N carrier bindings differ")
	}
	row, ok := validateExactNRow(bindings["row"])
	if !ok || !exactCompositeRow(row, outerRow) || bindings["serverAssemblyRevision"] != expected.ServerAssemblyRevision ||
		bindings["assemblyTopologySha256"] != expected.AssemblyTopologySHA256 ||
		!validGraphSHA(bindings["catalogCanonicalSha256"]) || !validGraphSHA(bindings["phaseEAggregatePlanSha256"]) {
		return fmt.Errorf("exact-N saved-row or Assembly binding differs")
	}
	operation, seamIDs, ok := validateExactNOperation(bindings["selectedOperation"])
	if !ok || expected.SelectedOperationNumber > 0 && operation != expected.SelectedOperationNumber {
		return fmt.Errorf("exact-N operation binding differs")
	}
	if len(seamIDs) == 0 || len(seamIDs) == 2 || len(seamIDs) > 256 {
		return fmt.Errorf("exact-N carrier must contain one or greater-than-two seams")
	}
	seams, ok := carrier["seams"].([]any)
	if !ok || len(seams) != len(seamIDs) {
		return fmt.Errorf("exact-N seam cardinality differs")
	}
	for index, value := range seams {
		seam, ok := exactJSONObject(value, "seamId", "sides")
		if !ok || seam["seamId"] != seamIDs[index] {
			return fmt.Errorf("exact-N seam order differs")
		}
		sides, ok := seam["sides"].([]any)
		if !ok || len(sides) != 2 || !validExactNSide(sides[0], seamIDs[index], 0) || !validExactNSide(sides[1], seamIDs[index], 1) {
			return fmt.Errorf("exact-N ordered side binding differs")
		}
	}
	fingerprint, ok := exactJSONObject(carrier["fingerprint"], "algorithm", "canonicalSha256", "canonicalByteLength")
	fingerprintLength, lengthOK := safeJSONInteger(fingerprint["canonicalByteLength"], 1)
	if !ok || !lengthOK || fingerprint["algorithm"] != "SHA-256" || !validGraphSHA(fingerprint["canonicalSha256"]) {
		return fmt.Errorf("exact-N fingerprint fields differ")
	}
	unsigned := make(map[string]any, len(carrier)-1)
	for key, value := range carrier {
		if key != "fingerprint" {
			unsigned[key] = value
		}
	}
	unsignedBytes, err := canonicalGraphBytes(unsigned)
	if err != nil {
		return err
	}
	fingerprintInput := append([]byte(patternExactNFingerprintDomainV2+"\x00"), unsignedBytes...)
	if int64(len(fingerprintInput)) != fingerprintLength || shaHex(fingerprintInput) != fingerprint["canonicalSha256"] {
		return fmt.Errorf("exact-N fingerprint does not recompute")
	}
	seal, ok := exactJSONObject(envelope["seal"], "schemaVersion", "algorithm", "boundCarrierFingerprintSha256", "canonicalSha256", "canonicalByteLength", "authority", "persistenceIntegrated", "productionReady")
	sealLength, sealLengthOK := safeJSONInteger(seal["canonicalByteLength"], 1)
	if !ok || !sealLengthOK || seal["schemaVersion"] != patternExactNSealDocumentV2 || seal["algorithm"] != "SHA-256" ||
		seal["boundCarrierFingerprintSha256"] != fingerprint["canonicalSha256"] || !validGraphSHA(seal["canonicalSha256"]) ||
		seal["authority"] != "untrusted-exact-structure-consistency-only" || seal["persistenceIntegrated"] != false || seal["productionReady"] != false {
		return fmt.Errorf("exact-N seal fields differ")
	}
	carrierBytes, err := canonicalGraphBytes(carrier)
	if err != nil {
		return err
	}
	sealInput := append([]byte(patternExactNSealDomainV2+"\x00"), carrierBytes...)
	if int64(len(sealInput)) != sealLength || shaHex(sealInput) != seal["canonicalSha256"] {
		return fmt.Errorf("exact-N seal does not recompute")
	}
	return nil
}

func validateExactNRow(value any) (SavedPatternRowIdentity, bool) {
	record, ok := exactJSONObject(value, "techCardId", "scopeKey", "patternLineKey", "revision", "cardLockVersion", "url", "sizeBytes", "sha256")
	if !ok {
		return SavedPatternRowIdentity{}, false
	}
	techCardID, techOK := safeJSONInteger(record["techCardId"], 1)
	revision, revisionOK := safeJSONInteger(record["revision"], 1)
	lockVersion, lockOK := safeJSONInteger(record["cardLockVersion"], 0)
	sizeBytes, sizeOK := safeJSONInteger(record["sizeBytes"], 1)
	scope, scopeOK := record["scopeKey"].(string)
	line, lineOK := record["patternLineKey"].(string)
	url, urlOK := record["url"].(string)
	sha, shaOK := record["sha256"].(string)
	if !techOK || !revisionOK || !lockOK || !sizeOK || !scopeOK || !lineOK || !urlOK || !shaOK ||
		!exactNStableID.MatchString(scope) || !exactNStableID.MatchString(line) || !validCompositeSavedURL(url) || !ValidPatternSemanticSidecarSHA256(sha) {
		return SavedPatternRowIdentity{}, false
	}
	return SavedPatternRowIdentity{TechCardID: int(techCardID), ScopeKey: scope, PatternLineKey: line,
		SavedRowRevision: uint32(revision), CardLockVersion: int(lockVersion), URL: url,
		SizeBytes: sizeBytes, SHA256: sha}, true
}

func validateExactNOperation(value any) (int, []string, bool) {
	record, ok := exactJSONObject(value, "sourceOperationNumber", "authoringOperationId", "localOperationOrder", "seamIds")
	if !ok {
		return 0, nil, false
	}
	number, numberOK := safeJSONInteger(record["sourceOperationNumber"], 1)
	order, orderOK := safeJSONInteger(record["localOperationOrder"], 0)
	authoringID, idOK := record["authoringOperationId"].(string)
	values, arrayOK := record["seamIds"].([]any)
	if !numberOK || !orderOK || order != 0 || !idOK || !exactNStableID.MatchString(authoringID) || !arrayOK || len(values) > 256 {
		return 0, nil, false
	}
	ids := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		id, ok := value.(string)
		if !ok || !exactNStableID.MatchString(id) {
			return 0, nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			return 0, nil, false
		}
		seen[id] = struct{}{}
		ids[index] = id
	}
	return int(number), ids, true
}

func validExactNSide(value any, seamID string, sideIndex int) bool {
	record, ok := exactJSONObject(value, "seamId", "sideIndex", "pieceLineKey", "pieceId", "contourId", "boundaryVertexIds", "direction")
	if !ok || record["seamId"] != seamID {
		return false
	}
	index, ok := safeJSONInteger(record["sideIndex"], 0)
	if !ok || int(index) != sideIndex {
		return false
	}
	for _, key := range []string{"pieceLineKey", "pieceId", "contourId"} {
		id, ok := record[key].(string)
		if !ok || !exactNStableID.MatchString(id) {
			return false
		}
	}
	vertices, ok := record["boundaryVertexIds"].([]any)
	if !ok || len(vertices) != 2 {
		return false
	}
	first, firstOK := vertices[0].(string)
	second, secondOK := vertices[1].(string)
	if !firstOK || !secondOK || first == second || !exactNStableID.MatchString(first) || !exactNStableID.MatchString(second) {
		return false
	}
	return record["direction"] == "forward" || record["direction"] == "reverse"
}

func validGraphSHA(value any) bool {
	text, ok := value.(string)
	return ok && ValidPatternSemanticSidecarSHA256(text)
}

func canonicalGraphBytes(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := appendCanonicalJSONValue(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func shaHex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validateTopologyAnnotations(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	canonical, err := canonicalJSONWithJSNumbers(payload)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("topology annotations are not canonical JSON")
	}
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return err
	}
	document, ok := exactJSONObject(graph, "schemaVersion", "mediaType", "encoding", "bindings", "measurements", "callouts")
	if !ok || document["schemaVersion"] != patternTopologyAnnotationsDocumentV1 ||
		document["mediaType"] != patternTopologyAnnotationsMediaTypeV1 || document["encoding"] != patternSemanticSidecarEncoding {
		return fmt.Errorf("topology annotation schema differs")
	}
	if err := validateAnnotationBindings(document["bindings"], outerRow, expected); err != nil {
		return err
	}
	measurements, measurementsOK := document["measurements"].([]any)
	callouts, calloutsOK := document["callouts"].([]any)
	if !measurementsOK || !calloutsOK || len(measurements)+len(callouts) > patternV5MaxAnnotations {
		return fmt.Errorf("topology annotation record bound differs")
	}
	ids := make(map[string]struct{}, len(measurements)+len(callouts))
	for _, value := range measurements {
		id, err := validateAnnotationMeasurement(value)
		if err != nil {
			return err
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("duplicate topology annotation id")
		}
		ids[id] = struct{}{}
	}
	for _, value := range callouts {
		id, err := validateAnnotationCallout(value)
		if err != nil {
			return err
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("duplicate topology annotation id")
		}
		ids[id] = struct{}{}
	}
	return nil
}

func validateAnnotationBindings(value any, row SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	bindings, ok := exactJSONObject(value, "card", "scope", "savedRow", "source", "assembly")
	if !ok {
		return fmt.Errorf("annotation bindings fields differ")
	}
	card, cardOK := exactJSONObject(bindings["card"], "techCardId", "cardLockVersion")
	scope, scopeOK := exactJSONObject(bindings["scope"], "scopeKey", "patternLineKey")
	savedRow, savedOK := exactJSONObject(bindings["savedRow"], "revision", "dxfSha256")
	source, sourceOK := exactJSONObject(bindings["source"], "identity", "revision", "sha256")
	assembly, assemblyOK := exactJSONObject(bindings["assembly"], "serverRevision", "topologySha256")
	if !cardOK || !scopeOK || !savedOK || !sourceOK || !assemblyOK {
		return fmt.Errorf("annotation binding record differs")
	}
	techCardID, techOK := safeJSONInteger(card["techCardId"], 1)
	lockVersion, lockOK := safeJSONInteger(card["cardLockVersion"], 0)
	revision, revisionOK := safeJSONInteger(savedRow["revision"], 1)
	sourceIdentity, identityOK := source["identity"].(string)
	sourceRevision, sourceRevisionOK := source["revision"].(string)
	serverRevision, serverOK := assembly["serverRevision"].(string)
	if !techOK || !lockOK || !revisionOK || !identityOK || sourceIdentity == "" || utf16Length(sourceIdentity) > 4096 ||
		!sourceRevisionOK || sourceRevision == "" || utf16Length(sourceRevision) > 256 || !validGraphSHA(source["sha256"]) ||
		!serverOK || serverRevision == "" || !validGraphSHA(assembly["topologySha256"]) ||
		int(techCardID) != row.TechCardID || int(lockVersion) != row.CardLockVersion ||
		scope["scopeKey"] != row.ScopeKey || scope["patternLineKey"] != row.PatternLineKey ||
		uint32(revision) != row.SavedRowRevision || savedRow["dxfSha256"] != row.SHA256 ||
		serverRevision != expected.ServerAssemblyRevision || assembly["topologySha256"] != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("annotation authority differs from outer authority")
	}
	return nil
}

func validateAnnotationMeasurement(value any) (string, error) {
	record, ok := exactJSONObject(value, "id", "kind", "anchors", "valueMm", "label")
	if !ok {
		return "", fmt.Errorf("annotation measurement fields differ")
	}
	id, ok := annotationID(record["id"])
	if !ok {
		return "", fmt.Errorf("annotation measurement id differs")
	}
	kind, kindOK := record["kind"].(string)
	if !kindOK || kind != "point" && kind != "line" && kind != "edge" {
		return "", fmt.Errorf("annotation measurement kind differs")
	}
	anchors, ok := record["anchors"].([]any)
	required := 2
	if kind == "point" {
		required = 1
	}
	if !ok || len(anchors) != required {
		return "", fmt.Errorf("annotation measurement anchor count differs")
	}
	for _, anchor := range anchors {
		if err := validateAnnotationAnchor(anchor); err != nil {
			return "", err
		}
	}
	if kind == "point" {
		if record["valueMm"] != nil {
			return "", fmt.Errorf("point measurement value must be null")
		}
	} else if number, ok := finiteJSONNumber(record["valueMm"]); !ok || number < 0 {
		return "", fmt.Errorf("line or edge measurement value differs")
	}
	if !boundedAnnotationText(record["label"], 512) {
		return "", fmt.Errorf("annotation measurement label differs")
	}
	return id, nil
}

func validateAnnotationCallout(value any) (string, error) {
	record, ok := exactJSONObject(value, "schemaVersion", "id", "anchor", "text")
	if !ok || record["schemaVersion"] != patternTopologyCalloutDocumentV1 {
		return "", fmt.Errorf("annotation callout fields differ")
	}
	id, ok := annotationID(record["id"])
	if !ok || !boundedAnnotationText(record["text"], 500) {
		return "", fmt.Errorf("annotation callout content differs")
	}
	if err := validateAnnotationAnchor(record["anchor"]); err != nil {
		return "", err
	}
	return id, nil
}

func validateAnnotationAnchor(value any) error {
	record, ok := exactJSONObject(value, "pieceId", "pieceLineKey", "contourId", "vertexId", "pointMm")
	if !ok {
		return fmt.Errorf("annotation anchor fields differ")
	}
	pieceID, pieceOK := annotationID(record["pieceId"])
	_, lineOK := annotationID(record["pieceLineKey"])
	contourID, contourOK := annotationID(record["contourId"])
	if !contourOK {
		contourOK = len(contourID) <= 256 && annotationDXFContourID.MatchString(contourID) &&
			strings.HasPrefix(contourID, "dxf-piece-v1/p/"+hex.EncodeToString([]byte(pieceID))+"/s/")
	}
	vertexID, vertexOK := annotationID(record["vertexId"])
	if !vertexOK {
		vertexOK = validAnnotationDXFVertexID(vertexID)
	}
	if !pieceOK || !lineOK || !contourOK || !vertexOK {
		return fmt.Errorf("annotation anchor identity differs")
	}
	point, ok := exactJSONObject(record["pointMm"], "x", "y")
	if !ok {
		return fmt.Errorf("annotation point fields differ")
	}
	if _, ok := finiteJSONNumber(point["x"]); !ok {
		return fmt.Errorf("annotation x coordinate differs")
	}
	if _, ok := finiteJSONNumber(point["y"]); !ok {
		return fmt.Errorf("annotation y coordinate differs")
	}
	return nil
}

func validAnnotationDXFVertexID(value string) bool {
	if len(value) > 256 || !annotationDXFVertexID.MatchString(value) {
		return false
	}
	curve := strings.TrimPrefix(value, annotationDXFVertexPrefix)
	if strings.LastIndex(curve, "/sample/") > 192 {
		return false
	}
	tokens := strings.Split(value, "/")
	for i, tag := range tokens[:len(tokens)-1] {
		switch tag {
		case "p", "o", "r", "c", "s", "sample":
			// The TypeScript provenance builder requires every index to be a safe integer.
			if _, err := strconv.ParseUint(tokens[i+1], 10, 53); err != nil {
				return false
			}
		}
	}
	return true
}

func annotationID(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != "" && utf16Length(text) <= 256 && annotationStableID.MatchString(text)
}

func boundedAnnotationText(value any, maximum int) bool {
	text, ok := value.(string)
	if !ok || text == "" || utf16Length(text) > maximum || strings.TrimFunc(text, isECMAScriptWhitespace) != text {
		return false
	}
	for _, scalar := range text {
		if scalar <= 0x08 || scalar == 0x0b || scalar == 0x0c || scalar >= 0x0e && scalar <= 0x1f {
			return false
		}
	}
	return true
}

func finiteJSONNumber(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(string(number), 64)
	return parsed, err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) && !(parsed == 0 && math.Signbit(parsed))
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func validatePatternProductCompositeSidecarV5(payload []byte, expected PatternProductCompositeSidecarExpected, requireFullRow bool) error {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return compositeV5Violation("invalid_composite_v5", "send a composite V5 within the 4 MiB production bound")
	}
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV5,
		PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5)
	if err != nil {
		return compositeV5Violation("invalid_composite_v5", err.Error())
	}
	if err := validateExpectedCompositeBindings(document, row, previous, expected, requireFullRow); err != nil {
		return compositeV5Violation("composite_v5_binding_mismatch", err.Error())
	}
	if len(document.Components) != 2 {
		return compositeV5Violation("composite_v5_components_required", "include exactly composite-v4 followed by topology annotations")
	}
	v4Bytes, err := compositeComponentBytes(document.Components[0], "composite-v4", PatternProductCompositeSidecarV4MediaType)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	annotationBytes, err := compositeComponentBytes(document.Components[1], "phase-e-topology-annotations-v1", patternTopologyAnnotationsMediaTypeV1)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	if err := validateNestedCompositeV4(v4Bytes, row, expected); err != nil {
		return compositeV5Violation("nested_composite_v4_invalid", err.Error())
	}
	if err := validateTopologyAnnotations(annotationBytes, row, expected); err != nil {
		return compositeV5Violation("topology_annotations_invalid", err.Error())
	}
	return nil
}

func validatePatternProductCompositeSidecarV4(payload []byte, expected PatternProductCompositeSidecarExpected, requireFullRow bool) error {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return compositeV5Violation("invalid_composite_v4", "send a composite V4 within the 4 MiB production bound")
	}
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV4,
		PatternProductCompositeSidecarV4MediaType, patternProductCompositeClaimsV4)
	if err != nil {
		return compositeV5Violation("invalid_composite_v4", err.Error())
	}
	if err := validateExpectedCompositeBindings(document, row, previous, expected, requireFullRow); err != nil {
		return compositeV5Violation("composite_v4_binding_mismatch", err.Error())
	}
	if err := validateNestedCompositeV4(payload, row, expected); err != nil {
		return compositeV5Violation("composite_v4_invalid", err.Error())
	}
	return nil
}

// ValidatePatternProductCompositeSidecarV4Binding is the under-lock Composite V4 admission gate.
// It checks the complete saved row, Assembly/CAS bindings and both canonical nested components.
func ValidatePatternProductCompositeSidecarV4Binding(payload []byte, expected PatternProductCompositeSidecarExpected) error {
	if expected.Row.ScopeKey == "" || expected.Row.URL == "" || expected.Row.SizeBytes <= 0 {
		return compositeV5Violation("server_saved_row_incomplete", "the server cannot prove an exact saved row")
	}
	return validatePatternProductCompositeSidecarV4(payload, expected, true)
}

func validateStoredPatternProductCompositeSidecarV4(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string, requireFullRow bool) error {
	document, _, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV4,
		PatternProductCompositeSidecarV4MediaType, patternProductCompositeClaimsV4)
	if err != nil {
		return compositeV5Violation("invalid_composite_v4", "stored composite V4 cannot be decoded")
	}
	return validatePatternProductCompositeSidecarV4(payload, PatternProductCompositeSidecarExpected{
		Row: row, PatternVersion: patternVersion, PostSaveReceiptSHA256: receiptSHA,
		ExpectedPreviousSidecarSHA256: previous,
		ServerAssemblyRevision:        document.Bindings.ServerAssemblyRevision,
		AssemblyTopologySHA256:        document.Bindings.AssemblyTopologySHA256,
	}, requireFullRow)
}

// ValidateStoredPatternProductCompositeSidecarV4Exact verifies a stored V4 against the current
// complete saved-row projection. Live Assembly drift is caller-visible staleness, not corruption.
func ValidateStoredPatternProductCompositeSidecarV4Exact(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecarV4(payload, row, patternVersion, receiptSHA, true)
}

func compositeV4BytesForTransition(mediaType string, payload []byte) ([]byte, error) {
	switch mediaType {
	case PatternProductCompositeSidecarV4MediaType:
		if _, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV4,
			PatternProductCompositeSidecarV4MediaType, patternProductCompositeClaimsV4); err != nil {
			return nil, fmt.Errorf("prior composite V4 is invalid: %w", err)
		}
		return payload, nil
	case PatternProductCompositeSidecarV5MediaType:
		document, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV5,
			PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5)
		if err != nil || len(document.Components) != 2 {
			return nil, fmt.Errorf("prior composite V5 is invalid")
		}
		return compositeComponentBytes(document.Components[0], "composite-v4", PatternProductCompositeSidecarV4MediaType)
	default:
		return nil, fmt.Errorf("prior media type %q is not composite V4 or V5", mediaType)
	}
}

func semanticV1BytesFromCompositeV3(payload []byte) ([]byte, error) {
	document, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocument,
		PatternProductCompositeSidecarMediaType, patternProductCompositeClaimsV3)
	if err != nil || len(document.Components) == 0 {
		return nil, fmt.Errorf("composite V3 is invalid")
	}
	semantic, err := compositeComponentBytes(document.Components[0], patternProductCompositeSemanticKind, PatternProductSemanticSidecarMediaType)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalJSONWithJSNumbers(semantic)
	if err != nil || !bytes.Equal(canonical, semantic) {
		return nil, fmt.Errorf("semantic-product-v1 is not canonical")
	}
	if err := validateNestedSemanticProductEnvelope(semantic); err != nil {
		return nil, err
	}
	return semantic, nil
}

func semanticV1BytesFromCompositeV4(payload []byte) ([]byte, error) {
	document, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV4,
		PatternProductCompositeSidecarV4MediaType, patternProductCompositeClaimsV4)
	if err != nil || len(document.Components) != 2 {
		return nil, fmt.Errorf("composite V4 is invalid")
	}
	v3, err := compositeComponentBytes(document.Components[0], "composite-v3", PatternProductCompositeSidecarMediaType)
	if err != nil {
		return nil, err
	}
	return semanticV1BytesFromCompositeV3(v3)
}

// ValidatePatternProductCompositeSidecarV4Transition proves the application-level V4 invariant
// under the row lock. A first V4 may be written when no sidecar exists; every replacement must
// preserve the exact semantic-product-v1 bytes from a supported current product V1/V3/V4 row.
func ValidatePatternProductCompositeSidecarV4Transition(nextPayload []byte, priorMediaType string, priorPayload []byte) error {
	nextSemantic, err := semanticV1BytesFromCompositeV4(nextPayload)
	if err != nil {
		return compositeV5Violation("invalid_composite_v4_transition", err.Error())
	}
	var priorSemantic []byte
	switch priorMediaType {
	case PatternProductSemanticSidecarMediaType:
		canonical, canonicalErr := canonicalJSONWithJSNumbers(priorPayload)
		if canonicalErr != nil || !bytes.Equal(canonical, priorPayload) {
			return compositeV5Violation("invalid_composite_v4_prior", "prior semantic-product-v1 is not canonical")
		}
		if err := validateNestedSemanticProductEnvelope(priorPayload); err != nil {
			return compositeV5Violation("invalid_composite_v4_prior", err.Error())
		}
		priorSemantic = priorPayload
	case PatternProductCompositeSidecarMediaType:
		priorSemantic, err = semanticV1BytesFromCompositeV3(priorPayload)
	case PatternProductCompositeSidecarV4MediaType:
		priorSemantic, err = semanticV1BytesFromCompositeV4(priorPayload)
	default:
		return compositeV5Violation("composite_v4_prior_unsupported", "current sidecar must be product semantic V1, Composite V3 or Composite V4")
	}
	if err != nil {
		return compositeV5Violation("invalid_composite_v4_prior", err.Error())
	}
	if !bytes.Equal(nextSemantic, priorSemantic) {
		return compositeV5Violation("composite_v4_semantic_changed", "preserve the current semantic-product-v1 bytes exactly")
	}
	return nil
}

// ValidatePatternProductCompositeSidecarV5Transition proves the application-level V5 invariant
// under the row lock: annotations may replace annotations, but the original verified V4 bytes may
// never be invented, upgraded or changed by a V5 write.
func ValidatePatternProductCompositeSidecarV5Transition(nextPayload []byte, priorMediaType string, priorPayload []byte) error {
	nextDocument, _, _, err := validOuterCompositeDocument(nextPayload, patternProductCompositeSidecarDocumentV5,
		PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5)
	if err != nil || len(nextDocument.Components) != 2 {
		return compositeV5Violation("invalid_composite_v5_transition", "next Composite V5 envelope is invalid")
	}
	nextV4, err := compositeComponentBytes(nextDocument.Components[0], "composite-v4", PatternProductCompositeSidecarV4MediaType)
	if err != nil {
		return compositeV5Violation("invalid_composite_v5_transition", err.Error())
	}
	priorV4, err := compositeV4BytesForTransition(priorMediaType, priorPayload)
	if err != nil {
		return compositeV5Violation("composite_v5_prior_required", err.Error())
	}
	if !bytes.Equal(nextV4, priorV4) {
		return compositeV5Violation("composite_v5_nested_v4_changed", "preserve the current Composite V4 bytes exactly")
	}
	return nil
}

// ValidatePatternProductCompositeSidecarV5Binding is the under-lock Composite V5 admission gate.
// It checks all eight server-owned row fields, Assembly/CAS bindings and every nested component.
func ValidatePatternProductCompositeSidecarV5Binding(payload []byte, expected PatternProductCompositeSidecarExpected) error {
	if expected.Row.ScopeKey == "" || expected.Row.URL == "" || expected.Row.SizeBytes <= 0 {
		return compositeV5Violation("server_saved_row_incomplete", "the server cannot prove an exact saved row")
	}
	return validatePatternProductCompositeSidecarV5(payload, expected, true)
}

func validateStoredPatternProductCompositeSidecarV5(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string, requireFullRow bool) error {
	document, _, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV5,
		PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5)
	if err != nil {
		return compositeV5Violation("invalid_composite_v5", "stored composite V5 cannot be decoded")
	}
	return validatePatternProductCompositeSidecarV5(payload, PatternProductCompositeSidecarExpected{
		Row: row, PatternVersion: patternVersion, PostSaveReceiptSHA256: receiptSHA,
		ExpectedPreviousSidecarSHA256: previous,
		ServerAssemblyRevision:        document.Bindings.ServerAssemblyRevision,
		AssemblyTopologySHA256:        document.Bindings.AssemblyTopologySHA256,
	}, requireFullRow)
}

// ValidateStoredPatternProductCompositeSidecarV5Exact verifies a stored V5 against the current
// complete saved-row projection. Live Assembly drift is not treated as stored-byte corruption.
func ValidateStoredPatternProductCompositeSidecarV5Exact(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecarV5(payload, row, patternVersion, receiptSHA, true)
}
