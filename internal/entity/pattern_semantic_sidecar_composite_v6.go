package entity

import (
	"bytes"
	"fmt"
	"math"
	"unicode/utf16"
)

const (
	patternProductCompositeSidecarDocumentV6 = "pattern-import-dxf-product-composite-sidecar-document/6"
	mannequinArrangementDocumentV1           = "grbpwr.cad-mannequin-arrangement/v1"
	mannequinArrangementMediaTypeV1          = "application/vnd.grbpwr.cad-mannequin-arrangement.v1+json"
	mannequinArrangementMaxPieces            = 20_000
	mannequinArrangementMaxIdentifier        = 512
	mannequinArrangementMaxModelRevision     = 256 * 1024
	mannequinArrangementMaxPoseKey           = 256 * 1024
	mannequinArrangementMaxOffsetMetres      = 10
	mannequinArrangementMaxRotationRadians   = math.Pi * 8
)

var patternProductCompositeClaimsV6 = map[string]bool{
	"nestedCompositeV5BytesPreservedExactly":         true,
	"mannequinArrangementCanonicalAndDigestVerified": true,
	"exactCardScopeAndSavedRowBound":                 true,
	"canonicalComponentOrderAndBase64Required":       true,
	"previousSidecarCasExpectationBound":             true,
	"mannequinArrangementPresentationOnly":           true,
	"dxfAuthorityAdded":                              false,
	"assemblyOrderAuthorityAdded":                    false,
	"clothAuthorityAdded":                            false,
	"persistenceIntegrated":                          false,
	"productionReady":                                false,
}

var mannequinArrangementClaimsV1 = map[string]bool{
	"presentationOnly":       true,
	"dxfAuthority":           false,
	"assemblyOrderAuthority": false,
	"clothAuthority":         false,
	"scalePersisted":         false,
}

func validatePatternProductCompositeSidecarV6(payload []byte, expected PatternProductCompositeSidecarExpected, requireFullRow bool) error {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return compositeV5Violation("invalid_composite_v6", "send a composite V6 within the 4 MiB production bound")
	}
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV6,
		PatternProductCompositeSidecarV6MediaType, patternProductCompositeClaimsV6)
	if err != nil {
		return compositeV5Violation("invalid_composite_v6", err.Error())
	}
	if err := validateExpectedCompositeBindings(document, row, previous, expected, requireFullRow); err != nil {
		return compositeV5Violation("composite_v6_binding_mismatch", err.Error())
	}
	if len(document.Components) != 2 {
		return compositeV5Violation("composite_v6_components_required", "include exactly composite-v5 followed by the mannequin arrangement")
	}
	v5Bytes, err := compositeComponentBytes(document.Components[0], "composite-v5", PatternProductCompositeSidecarV5MediaType)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	arrangementBytes, err := compositeComponentBytes(document.Components[1], "cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1)
	if err != nil {
		return compositeV5Violation("component_integrity_mismatch", err.Error())
	}
	if err := validateNestedCompositeV5ForV6(v5Bytes, row, expected); err != nil {
		return compositeV5Violation("nested_composite_v5_invalid", err.Error())
	}
	if err := validateMannequinArrangementV1(arrangementBytes, row); err != nil {
		return compositeV5Violation("mannequin_arrangement_invalid", err.Error())
	}
	return nil
}

func validateNestedCompositeV5ForV6(payload []byte, outerRow SavedPatternRowIdentity, expected PatternProductCompositeSidecarExpected) error {
	document, row, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV5,
		PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5)
	if err != nil {
		return fmt.Errorf("nested composite V5: %w", err)
	}
	if !exactCompositeRow(row, outerRow) || document.Bindings.ServerAssemblyRevision != expected.ServerAssemblyRevision ||
		document.Bindings.AssemblyTopologySHA256 != expected.AssemblyTopologySHA256 {
		return fmt.Errorf("nested composite V5 authority differs")
	}
	nestedExpected := expected
	nestedExpected.Row = outerRow
	nestedExpected.ExpectedPreviousSidecarSHA256 = previous
	return validatePatternProductCompositeSidecarV5(payload, nestedExpected, true)
}

func validateMannequinArrangementV1(payload []byte, outerRow SavedPatternRowIdentity) error {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return fmt.Errorf("arrangement is outside the 4 MiB production bound")
	}
	// Go strings cannot preserve lone UTF-16 surrogate code units: encoding/json decodes their JSON
	// escapes as U+FFFD. The exact canonical-byte comparison below deliberately fails closed. The
	// frontend arrangement codec must reject lone surrogates before Composite V6 can claim complete
	// cross-runtime canonical parity; accepting and silently rewriting them here would corrupt bytes.
	graph, err := decodeJSONGraph(payload)
	if err != nil {
		return fmt.Errorf("decode arrangement: %w", err)
	}
	document, ok := exactJSONObject(graph, "schemaVersion", "mediaType", "encoding", "bindings", "pieces", "claims")
	if !ok || document["schemaVersion"] != mannequinArrangementDocumentV1 ||
		document["mediaType"] != mannequinArrangementMediaTypeV1 || document["encoding"] != patternSemanticSidecarEncoding ||
		!exactBoolClaims(document["claims"], mannequinArrangementClaimsV1) {
		return fmt.Errorf("arrangement envelope or claims differ")
	}
	bindings, ok := exactJSONObject(document["bindings"], "card", "scope", "savedRow", "source", "mannequin")
	if !ok {
		return fmt.Errorf("arrangement bindings differ")
	}
	card, ok := exactJSONObject(bindings["card"], "techCardId", "cardLockVersion")
	if !ok {
		return fmt.Errorf("arrangement card binding differs")
	}
	techCardID, techCardOK := safeJSONInteger(card["techCardId"], 1)
	cardLockVersion, cardLockOK := safeJSONInteger(card["cardLockVersion"], 0)
	scope, scopeOK := exactJSONObject(bindings["scope"], "scopeKey", "patternLineKey", "sizeToken")
	savedRow, savedRowOK := exactJSONObject(bindings["savedRow"], "revision", "dxfSha256")
	revision, revisionOK := safeJSONInteger(valueOrNil(savedRow, "revision"), 1)
	source, sourceOK := exactJSONObject(bindings["source"], "catalogCanonicalSha256", "sizeSelectionCanonicalSha256")
	mannequin, mannequinOK := exactJSONObject(bindings["mannequin"], "baseModelId", "modelFormRevisionKeyV1", "poseBinding")
	baseModelID, baseModelOK := safeJSONInteger(valueOrNil(mannequin, "baseModelId"), 1)
	pose, poseOK := exactJSONObject(valueOrNil(mannequin, "poseBinding"), "canonicalKey")
	_ = baseModelID
	if !techCardOK || !cardLockOK || !scopeOK || !savedRowOK || !revisionOK || !sourceOK || !mannequinOK || !baseModelOK || !poseOK {
		return fmt.Errorf("arrangement binding shape or integers differ")
	}
	scopeKey, scopeKeyOK := boundedArrangementText(scope["scopeKey"], mannequinArrangementMaxIdentifier)
	patternLineKey, patternLineKeyOK := boundedArrangementText(scope["patternLineKey"], mannequinArrangementMaxIdentifier)
	_, sizeTokenOK := boundedArrangementText(scope["sizeToken"], mannequinArrangementMaxIdentifier)
	dxfSHA, dxfOK := savedRow["dxfSha256"].(string)
	catalogSHA, catalogOK := source["catalogCanonicalSha256"].(string)
	selectionSHA, selectionOK := source["sizeSelectionCanonicalSha256"].(string)
	_, modelRevisionOK := boundedArrangementText(mannequin["modelFormRevisionKeyV1"], mannequinArrangementMaxModelRevision)
	_, poseKeyOK := boundedArrangementText(pose["canonicalKey"], mannequinArrangementMaxPoseKey)
	if !scopeKeyOK || !patternLineKeyOK || !sizeTokenOK || !dxfOK || !catalogOK || !selectionOK || !modelRevisionOK || !poseKeyOK ||
		!ValidPatternSemanticSidecarSHA256(dxfSHA) || !ValidPatternSemanticSidecarSHA256(catalogSHA) ||
		!ValidPatternSemanticSidecarSHA256(selectionSHA) {
		return fmt.Errorf("arrangement binding text or digest differs")
	}
	if techCardID != int64(outerRow.TechCardID) || cardLockVersion != int64(outerRow.CardLockVersion) ||
		scopeKey != outerRow.ScopeKey || patternLineKey != outerRow.PatternLineKey ||
		revision != int64(outerRow.SavedRowRevision) || dxfSHA != outerRow.SHA256 {
		return fmt.Errorf("arrangement card, scope, saved row or DXF authority differs")
	}

	pieces, ok := document["pieces"].([]any)
	if !ok || len(pieces) == 0 || len(pieces) > mannequinArrangementMaxPieces {
		return fmt.Errorf("arrangement must contain 1..%d pieces", mannequinArrangementMaxPieces)
	}
	seenPieceKeys := make(map[string]struct{}, len(pieces))
	seenSourceRows := make(map[string]struct{}, len(pieces))
	var previousPiece arrangementPieceIdentity
	for index, candidate := range pieces {
		piece, err := validateMannequinArrangementPiece(candidate, scopeKey)
		if err != nil {
			return fmt.Errorf("piece %d: %w", index, err)
		}
		if index > 0 && compareArrangementPieces(previousPiece, piece) > 0 {
			return fmt.Errorf("pieces are not in canonical source-identity order")
		}
		previousPiece = piece
		if _, exists := seenPieceKeys[piece.PieceKey]; exists {
			return fmt.Errorf("duplicate pieceKey %q", piece.PieceKey)
		}
		seenPieceKeys[piece.PieceKey] = struct{}{}
		sourceIdentity := piece.ScopeKey + "\x00" + piece.PieceLineKey + "\x00" + piece.PieceID
		if _, exists := seenSourceRows[sourceIdentity]; exists {
			return fmt.Errorf("duplicate piece source identity")
		}
		seenSourceRows[sourceIdentity] = struct{}{}
	}
	canonical, err := canonicalMannequinArrangementV1(document)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("arrangement bytes are not canonical")
	}
	return nil
}

type arrangementPieceIdentity struct {
	PieceKey, ScopeKey, PieceID, PieceLineKey string
}

func validateMannequinArrangementPiece(value any, expectedScopeKey string) (arrangementPieceIdentity, error) {
	record, ok := exactJSONObject(value, "pieceKey", "scopeKey", "pieceId", "pieceLineKey", "transform")
	if !ok {
		return arrangementPieceIdentity{}, fmt.Errorf("piece fields differ")
	}
	pieceKey, pieceKeyOK := boundedArrangementText(record["pieceKey"], mannequinArrangementMaxIdentifier)
	scopeKey, scopeKeyOK := boundedArrangementText(record["scopeKey"], mannequinArrangementMaxIdentifier)
	pieceID, pieceIDOK := boundedArrangementText(record["pieceId"], mannequinArrangementMaxIdentifier)
	pieceLineKey, pieceLineKeyOK := boundedArrangementText(record["pieceLineKey"], mannequinArrangementMaxIdentifier)
	if !pieceKeyOK || !scopeKeyOK || !pieceIDOK || !pieceLineKeyOK || scopeKey != expectedScopeKey {
		return arrangementPieceIdentity{}, fmt.Errorf("piece identity or scope differs")
	}
	transform, ok := exactJSONObject(record["transform"], "offsetM", "rotationRad")
	if !ok {
		return arrangementPieceIdentity{}, fmt.Errorf("piece transform fields differ")
	}
	offset, ok := transform["offsetM"].([]any)
	if !ok || len(offset) != 3 {
		return arrangementPieceIdentity{}, fmt.Errorf("piece offset must contain three coordinates")
	}
	for _, coordinate := range offset {
		value, valid := finiteJSONNumber(coordinate)
		if !valid || math.Abs(value) > mannequinArrangementMaxOffsetMetres {
			return arrangementPieceIdentity{}, fmt.Errorf("piece offset exceeds its presentation bound")
		}
	}
	rotation, valid := finiteJSONNumber(transform["rotationRad"])
	if !valid || math.Abs(rotation) > mannequinArrangementMaxRotationRadians {
		return arrangementPieceIdentity{}, fmt.Errorf("piece rotation exceeds its presentation bound")
	}
	return arrangementPieceIdentity{PieceKey: pieceKey, ScopeKey: scopeKey, PieceID: pieceID, PieceLineKey: pieceLineKey}, nil
}

func boundedArrangementText(value any, maximum int) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" || utf16Length(text) > maximum || trimECMAScriptWhitespace(text) != text {
		return "", false
	}
	for _, scalar := range text {
		if scalar <= 0x08 || scalar == 0x0b || scalar == 0x0c || scalar >= 0x0e && scalar <= 0x1f {
			return "", false
		}
	}
	return text, true
}

func trimECMAScriptWhitespace(value string) string {
	return string(bytes.TrimFunc([]byte(value), func(r rune) bool { return isECMAScriptWhitespace(r) }))
}

func valueOrNil(record map[string]any, key string) any {
	if record == nil {
		return nil
	}
	return record[key]
}

func compareArrangementPieces(left, right arrangementPieceIdentity) int {
	for _, values := range [][2]string{{left.ScopeKey, right.ScopeKey}, {left.PieceLineKey, right.PieceLineKey}, {left.PieceID, right.PieceID}, {left.PieceKey, right.PieceKey}} {
		if compared := compareUTF16(values[0], values[1]); compared != 0 {
			return compared
		}
	}
	return 0
}

func compareUTF16(left, right string) int {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] < rightUnits[index] {
			return -1
		}
		if leftUnits[index] > rightUnits[index] {
			return 1
		}
	}
	if len(leftUnits) < len(rightUnits) {
		return -1
	}
	if len(leftUnits) > len(rightUnits) {
		return 1
	}
	return 0
}

func canonicalMannequinArrangementV1(document map[string]any) ([]byte, error) {
	var out bytes.Buffer
	appendOrderedJSONObject := func(record map[string]any, keys []string, appendValue func(*bytes.Buffer, string, any) error) error {
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			appendCompositeJSONString(&out, key)
			out.WriteByte(':')
			if err := appendValue(&out, key, record[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
		return nil
	}
	var appendPlain = func(target *bytes.Buffer, _ string, value any) error { return appendCanonicalJSONValue(target, value) }
	var appendBindings func(*bytes.Buffer, string, any) error
	appendBindings = func(target *bytes.Buffer, key string, value any) error {
		record, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("arrangement %s is not an object", key)
		}
		var keys []string
		switch key {
		case "bindings":
			keys = []string{"card", "scope", "savedRow", "source", "mannequin"}
		case "card":
			keys = []string{"techCardId", "cardLockVersion"}
		case "scope":
			keys = []string{"scopeKey", "patternLineKey", "sizeToken"}
		case "savedRow":
			keys = []string{"revision", "dxfSha256"}
		case "source":
			keys = []string{"catalogCanonicalSha256", "sizeSelectionCanonicalSha256"}
		case "mannequin":
			keys = []string{"baseModelId", "modelFormRevisionKeyV1", "poseBinding"}
		case "poseBinding":
			keys = []string{"canonicalKey"}
		default:
			return fmt.Errorf("unknown arrangement binding object %s", key)
		}
		target.WriteByte('{')
		for index, childKey := range keys {
			if index > 0 {
				target.WriteByte(',')
			}
			appendCompositeJSONString(target, childKey)
			target.WriteByte(':')
			child := record[childKey]
			if childKey == "card" || childKey == "scope" || childKey == "savedRow" || childKey == "source" || childKey == "mannequin" || childKey == "poseBinding" {
				if err := appendBindings(target, childKey, child); err != nil {
					return err
				}
			} else if err := appendCanonicalJSONValue(target, child); err != nil {
				return err
			}
		}
		target.WriteByte('}')
		return nil
	}
	appendPieces := func(target *bytes.Buffer, value any) error {
		pieces, ok := value.([]any)
		if !ok {
			return fmt.Errorf("arrangement pieces is not an array")
		}
		target.WriteByte('[')
		for index, candidate := range pieces {
			if index > 0 {
				target.WriteByte(',')
			}
			piece, ok := candidate.(map[string]any)
			if !ok {
				return fmt.Errorf("arrangement piece is not an object")
			}
			target.WriteByte('{')
			for fieldIndex, key := range []string{"pieceKey", "scopeKey", "pieceId", "pieceLineKey", "transform"} {
				if fieldIndex > 0 {
					target.WriteByte(',')
				}
				appendCompositeJSONString(target, key)
				target.WriteByte(':')
				if key != "transform" {
					if err := appendCanonicalJSONValue(target, piece[key]); err != nil {
						return err
					}
					continue
				}
				transform, ok := piece[key].(map[string]any)
				if !ok {
					return fmt.Errorf("arrangement transform is not an object")
				}
				target.WriteString(`{"offsetM":`)
				if err := appendCanonicalJSONValue(target, transform["offsetM"]); err != nil {
					return err
				}
				target.WriteString(`,"rotationRad":`)
				if err := appendCanonicalJSONValue(target, transform["rotationRad"]); err != nil {
					return err
				}
				target.WriteByte('}')
			}
			target.WriteByte('}')
		}
		target.WriteByte(']')
		return nil
	}
	err := appendOrderedJSONObject(document,
		[]string{"schemaVersion", "mediaType", "encoding", "bindings", "pieces", "claims"},
		func(target *bytes.Buffer, key string, value any) error {
			switch key {
			case "bindings":
				return appendBindings(target, key, value)
			case "pieces":
				return appendPieces(target, value)
			case "claims":
				claims, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("arrangement claims is not an object")
				}
				target.WriteByte('{')
				for index, claim := range []string{"presentationOnly", "dxfAuthority", "assemblyOrderAuthority", "clothAuthority", "scalePersisted"} {
					if index > 0 {
						target.WriteByte(',')
					}
					appendCompositeJSONString(target, claim)
					target.WriteByte(':')
					if err := appendCanonicalJSONValue(target, claims[claim]); err != nil {
						return err
					}
				}
				target.WriteByte('}')
				return nil
			default:
				return appendPlain(target, key, value)
			}
		})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func compositeV6ComponentsForTransition(payload []byte) ([]byte, []byte, error) {
	document, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV6,
		PatternProductCompositeSidecarV6MediaType, patternProductCompositeClaimsV6)
	if err != nil || len(document.Components) != 2 {
		return nil, nil, fmt.Errorf("composite V6 is invalid")
	}
	v5, err := compositeComponentBytes(document.Components[0], "composite-v5", PatternProductCompositeSidecarV5MediaType)
	if err != nil {
		return nil, nil, fmt.Errorf("nested composite V5: %w", err)
	}
	arrangement, err := compositeComponentBytes(document.Components[1], "cad-mannequin-arrangement-v1", mannequinArrangementMediaTypeV1)
	if err != nil {
		return nil, nil, fmt.Errorf("nested mannequin arrangement: %w", err)
	}
	return v5, arrangement, nil
}

func compositeV5BytesForV6Transition(mediaType string, payload []byte) ([]byte, error) {
	switch mediaType {
	case PatternProductCompositeSidecarV5MediaType:
		if _, _, _, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV5,
			PatternProductCompositeSidecarV5MediaType, patternProductCompositeClaimsV5); err != nil {
			return nil, fmt.Errorf("prior composite V5 is invalid: %w", err)
		}
		return payload, nil
	case PatternProductCompositeSidecarV6MediaType:
		v5, _, err := compositeV6ComponentsForTransition(payload)
		if err != nil {
			return nil, fmt.Errorf("prior composite V6 is invalid: %w", err)
		}
		return v5, nil
	default:
		return nil, fmt.Errorf("prior media type %q is not composite V5 or V6", mediaType)
	}
}

// ValidatePatternProductCompositeSidecarV6Transition enforces two lossless write lanes under the
// row lock. V5 -> V6 carries the verified V5 bytes unchanged. V6 -> V6 may change either the
// presentation-only arrangement while carrying V5 unchanged, or the annotations nested in V5
// while carrying both the verified V4 and arrangement bytes unchanged. A single write may never
// change both nested V5 and arrangement. The wire claim nestedCompositeV5BytesPreservedExactly
// describes byte-exact embedding and round-trip of the supplied V5 component; prior-carrier
// equality is this transition policy rather than a different V6 document schema.
func ValidatePatternProductCompositeSidecarV6Transition(nextPayload []byte, priorMediaType string, priorPayload []byte) error {
	nextV5, nextArrangement, err := compositeV6ComponentsForTransition(nextPayload)
	if err != nil {
		return compositeV5Violation("invalid_composite_v6_transition", "next Composite V6 envelope is invalid")
	}
	switch priorMediaType {
	case PatternProductCompositeSidecarV5MediaType:
		priorV5, priorErr := compositeV5BytesForV6Transition(priorMediaType, priorPayload)
		if priorErr != nil {
			return compositeV5Violation("composite_v6_prior_required", priorErr.Error())
		}
		if !bytes.Equal(nextV5, priorV5) {
			return compositeV5Violation("composite_v6_nested_v5_changed", "V5 to V6 must preserve the current Composite V5 bytes exactly")
		}
		return nil
	case PatternProductCompositeSidecarV6MediaType:
		priorV5, priorArrangement, priorErr := compositeV6ComponentsForTransition(priorPayload)
		if priorErr != nil {
			return compositeV5Violation("composite_v6_prior_required", priorErr.Error())
		}
		v5Changed := !bytes.Equal(nextV5, priorV5)
		arrangementChanged := !bytes.Equal(nextArrangement, priorArrangement)
		if !v5Changed {
			return nil
		}
		if arrangementChanged {
			return compositeV5Violation("composite_v6_components_changed_together", "change either Composite V5 annotations or the mannequin arrangement, never both in one write")
		}
		if err := ValidatePatternProductCompositeSidecarV5Transition(
			nextV5, PatternProductCompositeSidecarV5MediaType, priorV5,
		); err != nil {
			return compositeV5Violation("composite_v6_nested_v5_transition_invalid", err.Error())
		}
		return nil
	default:
		return compositeV5Violation("composite_v6_prior_required", fmt.Sprintf("prior media type %q is not composite V5 or V6", priorMediaType))
	}
}

// ValidatePatternProductCompositeSidecarV6Binding is the under-lock Composite V6 admission gate.
func ValidatePatternProductCompositeSidecarV6Binding(payload []byte, expected PatternProductCompositeSidecarExpected) error {
	if expected.Row.ScopeKey == "" || expected.Row.URL == "" || expected.Row.SizeBytes <= 0 {
		return compositeV5Violation("server_saved_row_incomplete", "the server cannot prove an exact saved row")
	}
	return validatePatternProductCompositeSidecarV6(payload, expected, true)
}

func validateStoredPatternProductCompositeSidecarV6(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string, requireFullRow bool) error {
	document, _, previous, err := validOuterCompositeDocument(payload, patternProductCompositeSidecarDocumentV6,
		PatternProductCompositeSidecarV6MediaType, patternProductCompositeClaimsV6)
	if err != nil {
		return compositeV5Violation("invalid_composite_v6", "stored composite V6 cannot be decoded")
	}
	return validatePatternProductCompositeSidecarV6(payload, PatternProductCompositeSidecarExpected{
		Row: row, PatternVersion: patternVersion, PostSaveReceiptSHA256: receiptSHA,
		ExpectedPreviousSidecarSHA256: previous,
		ServerAssemblyRevision:        document.Bindings.ServerAssemblyRevision,
		AssemblyTopologySHA256:        document.Bindings.AssemblyTopologySHA256,
	}, requireFullRow)
}

// ValidateStoredPatternProductCompositeSidecarV6Exact revalidates every persisted V6 byte and its
// complete current saved-row projection on GET.
func ValidateStoredPatternProductCompositeSidecarV6Exact(payload []byte, row SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	return validateStoredPatternProductCompositeSidecarV6(payload, row, patternVersion, receiptSHA, true)
}
