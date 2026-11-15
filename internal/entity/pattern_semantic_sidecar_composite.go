package entity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

const (
	maxCompositeJSSafeInteger                = int64(1<<53 - 1)
	patternProductCompositeSemanticKind      = "semantic-product-v1"
	patternProductCompositePlacementKind     = "multiseam-placement-v2"
	patternProductCompositeExactTopologyKind = "phase-e-exact-topology-v1"
	patternProductCompositePlacementMedia    = "application/vnd.grbpwr.pattern-dxf-product-multiseam-placement.v2+json"
	patternProductCompositeExactMedia        = "application/vnd.grbpwr.pattern-dxf-product-phase-e-exact-topology.v1+json"
	patternProductExactCarrierDocument       = "pattern-import-dxf-product-phase-e-exact-topology-binding/1"
	patternProductExactCarrierSealDocument   = "pattern-import-dxf-product-phase-e-exact-topology-binding-seal/1"
	patternProductExactCarrierHashDomain     = "pattern-import-dxf-product-phase-e-exact-topology-binding-v1"
)

// PatternProductCompositeSidecarExpected is the complete server-owned binding a composite V3 must
// repeat in its outer document and exact-topology component. Row is the dedicated eight-field
// saved-byte identity; PatternVersion remains a separate legacy semantic-V1 binding.
type PatternProductCompositeSidecarExpected struct {
	Row                           SavedPatternRowIdentity
	PatternVersion                int
	PostSaveReceiptSHA256         string
	ExpectedPreviousSidecarSHA256 string // empty means the V3 JSON writeCas value must be null
	ServerAssemblyRevision        string
	AssemblyTopologySHA256        string
	SelectedOperationNumber       int
}

type compositeSavedRowV3 struct {
	TechCardID      int    `json:"techCardId"`
	ScopeKey        string `json:"scopeKey"`
	PatternLineKey  string `json:"patternLineKey"`
	Revision        uint32 `json:"revision"`
	CardLockVersion int    `json:"cardLockVersion"`
	URL             string `json:"url"`
	SizeBytes       int64  `json:"sizeBytes"`
	SHA256          string `json:"sha256"`
}

type compositeComponentV3 struct {
	Kind       string `json:"kind"`
	MediaType  string `json:"mediaType"`
	SHA256     string `json:"sha256"`
	ByteLength int    `json:"byteLength"`
	Base64     string `json:"base64"`
}

type compositeDocumentV3 struct {
	SchemaVersion string `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Encoding      string `json:"encoding"`
	Bindings      struct {
		Row                    compositeSavedRowV3 `json:"row"`
		ServerAssemblyRevision string              `json:"serverAssemblyRevision"`
		AssemblyTopologySHA256 string              `json:"assemblyTopologySha256"`
	} `json:"bindings"`
	WriteCAS struct {
		ExpectedPreviousSidecarSHA256 json.RawMessage `json:"expectedPreviousSidecarSha256"`
	} `json:"writeCas"`
	Components []compositeComponentV3 `json:"components"`
	Claims     map[string]bool        `json:"claims"`
}

var patternProductCompositeClaimsV3 = map[string]bool{
	"exactSavedRowBound": true, "exactServerAssemblyRevisionBound": true,
	"exactAssemblyTopologySha256Bound": true, "requiredProductV1BytesVerifiedAndPreserved": true,
	"optionalPlacementV2VerifiedWhenPresent": true, "optionalExactTopologyCarrierSealVerifiedWhenPresent": true,
	"canonicalComponentOrderAndBase64Required": true, "previousSidecarCasExpectationBound": true,
	"legacyV1PlacementInferred": false, "techCardSeamPlacementAcceptedAsPlacementAuthority": false,
	"exactTopologyRestoreOwnerMinted": false, "rawDxfAccepted": false, "rawDxfRetained": false,
	"persistenceRequestAdapterAvailable": true, "sidecarGetPerformed": false, "sidecarPutPerformed": false,
	"persistenceIntegrated": false, "placementIntegrated": false, "previewReady": false,
	"runtimeReady": false, "uiIntegrated": false, "productionReady": false,
}

var patternProductExactCarrierClaimsV1 = map[string]any{
	"status": "exact-phase-e-topology-authoring-binding", "exactSavedRowBound": true,
	"exactServerAssemblyRevisionBound": true, "exactAssemblyTopologyBound": true,
	"exactCatalogRecomputed": true, "exactProposalFingerprintRecomputed": true,
	"aggregatePlanSha256Recomputed": true, "allFourSidesBound": true,
	"exactAdjacentOrderedVerticesRequired": true, "freshDocumentRevisionAuthorityBound": false,
	"freshImportGenerationAuthorityBound": false, "edgeHintAcceptedAsGeometryAuthority": false,
	"anchorRangeAcceptedAsGeometryAuthority": false, "nearestGeometryRecoveryUsed": false,
	"shapeFitRecoveryUsed": false, "rawDxfAccepted": false, "rawDxfRetained": false,
	"typedArraysRetained": false, "upstreamCatalogProposalOrChoicesRetained": false,
	"persistenceIntegrated": false, "sidecarWritePerformed": false, "confirmationPerformed": false,
	"placementIntegrated": false, "previewReady": false, "runtimeReady": false,
	"uiIntegrated": false, "productionReady": false,
}

var compositeStableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var compositeIntegerJSON = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)$`)

func decodeStrictJSON(payload []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errorsNewJSONTrailingValue
		}
		return err
	}
	return nil
}

var errorsNewJSONTrailingValue = fmt.Errorf("trailing JSON value")

func compositeRowIdentity(row compositeSavedRowV3) SavedPatternRowIdentity {
	return SavedPatternRowIdentity{
		TechCardID: row.TechCardID, ScopeKey: row.ScopeKey, PatternLineKey: row.PatternLineKey,
		SavedRowRevision: row.Revision, CardLockVersion: row.CardLockVersion,
		URL: row.URL, SizeBytes: row.SizeBytes, SHA256: row.SHA256,
	}
}

func validCompositeSavedRow(row compositeSavedRowV3) bool {
	return row.TechCardID > 0 && int64(row.TechCardID) <= maxCompositeJSSafeInteger &&
		len(row.ScopeKey) <= 256 && compositeStableID.MatchString(row.ScopeKey) &&
		len(row.PatternLineKey) <= 256 && compositeStableID.MatchString(row.PatternLineKey) && row.Revision > 0 &&
		row.CardLockVersion >= 0 && int64(row.CardLockVersion) <= maxCompositeJSSafeInteger &&
		validCompositeSavedURL(row.URL) && row.SizeBytes > 0 && row.SizeBytes <= maxCompositeJSSafeInteger &&
		ValidPatternSemanticSidecarSHA256(row.SHA256)
}

func validCompositeSavedURL(value string) bool {
	if value == "" || len(utf16.Encode([]rune(value))) > 4096 {
		return false
	}
	for _, scalar := range value {
		if scalar <= 0x1f {
			return false
		}
	}
	lower := strings.ToLower(strings.TrimLeftFunc(value, isECMAScriptWhitespace))
	return !strings.HasPrefix(lower, "data:") && !strings.HasPrefix(lower, "javascript:")
}

func validCompositeServerAssemblyRevision(value string) bool {
	if value == "" || len([]byte(value)) > 512 || strings.TrimFunc(value, isECMAScriptWhitespace) != value {
		return false
	}
	decimalOnly := true
	for _, scalar := range value {
		if scalar <= 0x1f || scalar == 0x7f {
			return false
		}
		if scalar < '0' || scalar > '9' {
			decimalOnly = false
		}
	}
	return !decimalOnly
}

// JavaScript's trim/trimStart use ECMAScript WhiteSpace + LineTerminator, which differs subtly
// from unicode.IsSpace (notably U+FEFF is trimmed by JS while U+0085 is not). Composite bytes are
// minted and verified by TypeScript, so this boundary must use the browser's exact semantics.
func isECMAScriptWhitespace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', ' ', '\n', '\r', '\u2028', '\u2029', '\ufeff':
		return true
	default:
		return unicode.Is(unicode.Zs, r)
	}
}

func exactCompositeRow(left, right SavedPatternRowIdentity) bool {
	return left.TechCardID == right.TechCardID && left.ScopeKey == right.ScopeKey &&
		left.PatternLineKey == right.PatternLineKey &&
		left.SavedRowRevision == right.SavedRowRevision && left.CardLockVersion == right.CardLockVersion &&
		left.URL == right.URL && left.SizeBytes == right.SizeBytes && left.SHA256 == right.SHA256
}

func decodeCompositePreviousSHA(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("missing expectedPreviousSidecarSha256")
	}
	if bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !ValidPatternSemanticSidecarSHA256(value) {
		return "", fmt.Errorf("invalid expectedPreviousSidecarSha256")
	}
	return value, nil
}

func compositeComponentBytes(component compositeComponentV3, expectedKind, expectedMedia string) ([]byte, error) {
	if component.Kind != expectedKind || component.MediaType != expectedMedia || component.ByteLength <= 0 ||
		component.ByteLength > PatternSemanticSidecarMaxBytes || !ValidPatternSemanticSidecarSHA256(component.SHA256) {
		return nil, fmt.Errorf("%s component metadata mismatch", expectedKind)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(component.Base64)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != component.Base64 ||
		len(decoded) != component.ByteLength || PatternSemanticSidecarPayloadSHA256(decoded) != component.SHA256 {
		return nil, fmt.Errorf("%s component bytes mismatch", expectedKind)
	}
	return decoded, nil
}

func validateCompositeClaims(claims map[string]bool) bool {
	if len(claims) != len(patternProductCompositeClaimsV3) {
		return false
	}
	for key, want := range patternProductCompositeClaimsV3 {
		if got, ok := claims[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func validateCompositeSemanticComponent(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	if err := validateLegacyPatternSemanticSidecarPayloadBinding(payload, row.TechCardID, row.PatternLineKey,
		row.CardLockVersion, row.SHA256, patternVersion, receiptSHA, PatternProductSemanticSidecarMediaType); err != nil {
		return err
	}
	var document struct {
		Product struct {
			ScopeKey     string `json:"scopeKey"`
			ExportSHA256 string `json:"exportSha256"`
			ByteLength   int64  `json:"byteLength"`
		} `json:"product"`
		Persistence struct {
			Pattern struct {
				URL      string `json:"url"`
				ScopeKey string `json:"scopeKey"`
			} `json:"pattern"`
		} `json:"persistence"`
	}
	if err := json.Unmarshal(payload, &document); err != nil || document.Product.ScopeKey != row.ScopeKey ||
		document.Product.ExportSHA256 != row.SHA256 || document.Product.ByteLength != row.SizeBytes ||
		document.Persistence.Pattern.URL != row.URL || document.Persistence.Pattern.ScopeKey != row.ScopeKey {
		return fmt.Errorf("semantic-product-v1 exact row binding mismatch")
	}
	return nil
}

type exactTopologyWrapperV1 struct {
	Carrier json.RawMessage `json:"carrier"`
	Seal    struct {
		SchemaVersion         string `json:"schemaVersion"`
		Algorithm             string `json:"algorithm"`
		CanonicalSHA256       string `json:"canonicalSha256"`
		CanonicalByteLength   int    `json:"canonicalByteLength"`
		Authority             string `json:"authority"`
		PersistenceIntegrated bool   `json:"persistenceIntegrated"`
		ProductionReady       bool   `json:"productionReady"`
	} `json:"seal"`
}

func canonicalExactJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	if err := validateCompositeCanonicalNumbers(value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func validateCompositeCanonicalNumbers(value any) error {
	switch typed := value.(type) {
	case json.Number:
		if !compositeIntegerJSON.MatchString(string(typed)) || string(typed) == "-0" {
			return fmt.Errorf("non-canonical numeric value %q", typed)
		}
	case []any:
		for _, child := range typed {
			if err := validateCompositeCanonicalNumbers(child); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, child := range typed {
			if err := validateCompositeCanonicalNumbers(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCompositeExactTopologyComponent(payload []byte, outerRow SavedPatternRowIdentity, assemblyRevision, topologySHA string, selectedOperationNumber int) error {
	canonicalWrapper, err := canonicalExactJSON(payload)
	if err != nil || !bytes.Equal(canonicalWrapper, payload) {
		return fmt.Errorf("exact topology wrapper is not canonical JSON")
	}
	var wrapper exactTopologyWrapperV1
	if err := decodeStrictJSON(payload, &wrapper); err != nil {
		return fmt.Errorf("decode exact topology wrapper: %w", err)
	}
	var carrier struct {
		SchemaVersion string `json:"schemaVersion"`
		Status        string `json:"status"`
		Bindings      struct {
			Row                    compositeSavedRowV3 `json:"row"`
			ServerAssemblyRevision string              `json:"serverAssemblyRevision"`
			AssemblyTopologySHA256 string              `json:"assemblyTopologySha256"`
			SelectedOperation      struct {
				SourceOperationNumber int      `json:"sourceOperationNumber"`
				AuthoringOperationID  string   `json:"authoringOperationId"`
				LocalOperationOrder   int      `json:"localOperationOrder"`
				SeamIDs               []string `json:"seamIds"`
			} `json:"selectedOperation"`
			CatalogCanonicalSHA256    string `json:"catalogCanonicalSha256"`
			ProposalFingerprint       string `json:"proposalFingerprint"`
			PhaseEAggregatePlanSHA256 string `json:"phaseEAggregatePlanSha256"`
		} `json:"bindings"`
		Sides []struct {
			SeamID            string   `json:"seamId"`
			SideIndex         int      `json:"sideIndex"`
			PieceLineKey      string   `json:"pieceLineKey"`
			PieceID           string   `json:"pieceId"`
			ContourID         string   `json:"contourId"`
			BoundaryVertexIDs []string `json:"boundaryVertexIds"`
			Direction         string   `json:"direction"`
		} `json:"sides"`
		Claims json.RawMessage `json:"claims"`
	}
	if err := decodeStrictJSON(wrapper.Carrier, &carrier); err != nil {
		return fmt.Errorf("decode exact topology carrier: %w", err)
	}
	operation := carrier.Bindings.SelectedOperation
	var carrierClaims map[string]any
	if err := json.Unmarshal(carrier.Claims, &carrierClaims); err != nil {
		return fmt.Errorf("decode exact topology claims: %w", err)
	}
	if carrier.SchemaVersion != patternProductExactCarrierDocument || carrier.Status != "exact-phase-e-topology-authoring-binding" ||
		!exactCompositeRow(compositeRowIdentity(carrier.Bindings.Row), outerRow) ||
		carrier.Bindings.ServerAssemblyRevision != assemblyRevision ||
		carrier.Bindings.AssemblyTopologySHA256 != topologySHA || operation.SourceOperationNumber <= 0 ||
		(selectedOperationNumber > 0 && operation.SourceOperationNumber != selectedOperationNumber) ||
		operation.AuthoringOperationID == "" || operation.LocalOperationOrder != 0 || len(operation.SeamIDs) != 2 ||
		operation.SeamIDs[0] == "" || operation.SeamIDs[0] == operation.SeamIDs[1] ||
		!ValidPatternSemanticSidecarSHA256(carrier.Bindings.CatalogCanonicalSHA256) ||
		!ValidPatternSemanticSidecarSHA256(carrier.Bindings.PhaseEAggregatePlanSHA256) ||
		!regexp.MustCompile(`^pidpemsv2-fnv1a64:[0-9a-f]{16}$`).MatchString(carrier.Bindings.ProposalFingerprint) ||
		!reflect.DeepEqual(carrierClaims, patternProductExactCarrierClaimsV1) || len(carrier.Sides) != 4 {
		return fmt.Errorf("exact topology carrier binding mismatch")
	}
	for index, side := range carrier.Sides {
		if side.SeamID != operation.SeamIDs[index/2] || side.SideIndex != index%2 ||
			side.PieceLineKey == "" || side.PieceID == "" || side.ContourID == "" ||
			len(side.BoundaryVertexIDs) != 2 || side.BoundaryVertexIDs[0] == "" ||
			side.BoundaryVertexIDs[0] == side.BoundaryVertexIDs[1] ||
			(side.Direction != "forward" && side.Direction != "reverse") {
			return fmt.Errorf("exact topology side binding mismatch")
		}
	}
	seal := wrapper.Seal
	canonicalCarrier, err := canonicalExactJSON(wrapper.Carrier)
	if err != nil {
		return err
	}
	domainBytes := append([]byte(patternProductExactCarrierHashDomain+"\x00"), canonicalCarrier...)
	digest := sha256.Sum256(domainBytes)
	if seal.SchemaVersion != patternProductExactCarrierSealDocument || seal.Algorithm != "SHA-256" ||
		seal.Authority != "unsigned-exact-authoring-subdocument-consistency-only" ||
		seal.PersistenceIntegrated || seal.ProductionReady || seal.CanonicalByteLength != len(canonicalCarrier) ||
		seal.CanonicalSHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("exact topology carrier seal mismatch")
	}
	return nil
}

func validatePatternProductCompositeSidecar(payload []byte, expected PatternProductCompositeSidecarExpected, requireFullRow bool) error {
	var document compositeDocumentV3
	if err := decodeStrictJSON(payload, &document); err != nil {
		return NewFieldViolation("sidecar_bytes", "invalid_composite_v3", "", "send the canonical composite V3 document")
	}
	canonicalPayload, err := canonicalExactJSON(payload)
	if err != nil || !bytes.Equal(canonicalPayload, payload) {
		return NewFieldViolation("sidecar_bytes", "noncanonical_composite_v3", "", "send the exact canonical JSON UTF-8 bytes")
	}
	outerRow := compositeRowIdentity(document.Bindings.Row)
	if document.SchemaVersion != patternProductCompositeSidecarDocument ||
		document.MediaType != PatternProductCompositeSidecarMediaType || document.Encoding != patternSemanticSidecarEncoding ||
		!validCompositeSavedRow(document.Bindings.Row) ||
		!validCompositeServerAssemblyRevision(document.Bindings.ServerAssemblyRevision) ||
		!ValidPatternSemanticSidecarSHA256(document.Bindings.AssemblyTopologySHA256) ||
		!validateCompositeClaims(document.Claims) {
		return NewFieldViolation("sidecar_bytes", "invalid_composite_v3", "", "rebuild composite V3 from verified owners")
	}
	if expected.Row.TechCardID != outerRow.TechCardID ||
		expected.Row.PatternLineKey != outerRow.PatternLineKey ||
		expected.Row.SavedRowRevision != outerRow.SavedRowRevision || expected.Row.CardLockVersion != outerRow.CardLockVersion ||
		expected.Row.SHA256 != outerRow.SHA256 {
		return NewFieldViolation("sidecar_bytes", "saved_row_binding_mismatch", "", "reload the exact saved row and rebuild composite V3")
	}
	if requireFullRow && !exactCompositeRow(expected.Row, outerRow) {
		return NewFieldViolation("sidecar_bytes", "saved_row_binding_mismatch", "", "reload the exact saved row and rebuild composite V3")
	}
	previousSHA, err := decodeCompositePreviousSHA(document.WriteCAS.ExpectedPreviousSidecarSHA256)
	if err != nil || previousSHA != expected.ExpectedPreviousSidecarSHA256 ||
		document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision ||
		document.Bindings.AssemblyTopologySHA256 != expected.AssemblyTopologySHA256 {
		return NewFieldViolation("sidecar_bytes", "assembly_binding_mismatch", "", "reload the Assembly snapshot and prior sidecar CAS, then rebuild composite V3")
	}
	if len(document.Components) != 2 && len(document.Components) != 3 {
		return NewFieldViolation("sidecar_bytes", "exact_components_required", "", "include semantic-product-v1 and phase-e-exact-topology-v1")
	}
	semanticBytes, err := compositeComponentBytes(document.Components[0], patternProductCompositeSemanticKind, PatternProductSemanticSidecarMediaType)
	if err != nil {
		return NewFieldViolation("sidecar_bytes", "component_integrity_mismatch", "", err.Error())
	}
	exactIndex := 1
	if len(document.Components) == 3 {
		if _, err := compositeComponentBytes(document.Components[1], patternProductCompositePlacementKind, patternProductCompositePlacementMedia); err != nil {
			return NewFieldViolation("sidecar_bytes", "component_integrity_mismatch", "", err.Error())
		}
		exactIndex = 2
	}
	exactBytes, err := compositeComponentBytes(document.Components[exactIndex], patternProductCompositeExactTopologyKind, patternProductCompositeExactMedia)
	if err != nil {
		return NewFieldViolation("sidecar_bytes", "component_integrity_mismatch", "", err.Error())
	}
	if err := validateCompositeSemanticComponent(semanticBytes, outerRow, expected.PatternVersion, expected.PostSaveReceiptSHA256); err != nil {
		return NewFieldViolation("sidecar_bytes", "semantic_component_binding_mismatch", "", "rebuild semantic-product-v1 from the exact current row")
	}
	if err := validateCompositeExactTopologyComponent(exactBytes, outerRow,
		expected.ServerAssemblyRevision, expected.AssemblyTopologySHA256, expected.SelectedOperationNumber); err != nil {
		return NewFieldViolation("sidecar_bytes", "exact_topology_binding_mismatch", "", err.Error())
	}
	return nil
}

// ValidatePatternProductCompositeSidecarBinding is the final under-lock V3 gate. Callers must pass
// the exact row resolved from the database, not a browser echo.
func ValidatePatternProductCompositeSidecarBinding(payload []byte, expected PatternProductCompositeSidecarExpected) error {
	if expected.Row.ScopeKey == "" || expected.Row.URL == "" || expected.Row.SizeBytes <= 0 {
		return NewFieldViolation("sidecar_bytes", "server_saved_row_incomplete", "", "the server cannot prove an exact saved row")
	}
	return validatePatternProductCompositeSidecar(payload, expected, true)
}

// ValidateStoredPatternProductCompositeSidecar verifies a stored V3's component integrity and all
// internal cross-bindings against the row metadata persisted beside it. Assembly drift after the
// write is intentionally not corruption; a fresh snapshot exposes the newer opaque token/hash and
// the client then rejects the older carrier fail-closed.
func validateStoredPatternProductCompositeSidecar(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string, requireFullRow bool) error {
	var document compositeDocumentV3
	if err := decodeStrictJSON(payload, &document); err != nil {
		return NewFieldViolation("sidecar_bytes", "invalid_composite_v3", "", "stored composite V3 cannot be decoded")
	}
	previousSHA, err := decodeCompositePreviousSHA(document.WriteCAS.ExpectedPreviousSidecarSHA256)
	if err != nil {
		return err
	}
	return validatePatternProductCompositeSidecar(payload, PatternProductCompositeSidecarExpected{
		Row: row, PatternVersion: patternVersion, PostSaveReceiptSHA256: receiptSHA,
		ExpectedPreviousSidecarSHA256: previousSHA,
		ServerAssemblyRevision:        document.Bindings.ServerAssemblyRevision,
		AssemblyTopologySHA256:        document.Bindings.AssemblyTopologySHA256,
	}, requireFullRow)
}

func ValidateStoredPatternProductCompositeSidecar(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecar(payload, row, patternVersion, receiptSHA, false)
}

// ValidateStoredPatternProductCompositeSidecarExact additionally cross-checks all eight saved-row
// fields against a current server projection. It deliberately does not compare the live Assembly
// clock: later Assembly edits make an old carrier inadmissible, not corrupt.
func ValidateStoredPatternProductCompositeSidecarExact(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecar(payload, row, patternVersion, receiptSHA, true)
}
