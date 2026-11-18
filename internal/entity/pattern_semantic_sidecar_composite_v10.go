package entity

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

const (
	PatternProductCompositeSidecarV10MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v10+json"
	patternProductCompositeSidecarDocumentV10  = "pattern-import-dxf-product-composite-sidecar-document/10"
	nativeWholePlacementProfileV10             = "saved-full-assembly/native-whole-world-placement/1"
	nativeWholePlacementDomainV10              = "tech-card-complete-source-native-whole-placement/1"
	nativeWholeWorldPlacementMediaTypeV1       = "application/vnd.grbpwr.pattern-dxf-native-whole-world-placement.v1+json"
	retainedNativeCrossRowKindV10              = "retained-native-cross-row-carrier"
	nativeWholeWorldPlacementKindV10           = "native-whole-world-placement-v1"
	maxNativeWholePlacementBytesV10            = 131072
	maxNativeWholePlacementVerticesV10         = 2000
	maxNativeWholePlacementTrianglesV10        = 5000
)

type nativeWholePlacementDocumentV10 struct {
	SchemaVersion string                   `json:"schemaVersion"`
	MediaType     string                   `json:"mediaType"`
	Encoding      string                   `json:"encoding"`
	Profile       string                   `json:"profile"`
	Bindings      nativeCrossRowBindingsV8 `json:"bindings"`
	WriteCAS      struct {
		ExpectedPreviousSidecarSHA256 string `json:"expectedPreviousSidecarSha256"`
	} `json:"writeCas"`
	Components [2]compositeComponentV3 `json:"components"`
}

type nativeWholeWorldPlacementV1 struct {
	SchemaVersion         string                         `json:"schemaVersion"`
	RetainedCarrierSHA256 string                         `json:"retainedCarrierSha256"`
	Body                  neutralPlacementObservedBodyV1 `json:"body"`
	Layout                nativeWholeWorldLayoutV1       `json:"layout"`
}

type nativeWholeWorldLayoutV1 struct {
	SizeID                 int                            `json:"sizeId"`
	SizeToken              string                         `json:"sizeToken"`
	RuntimeCandidateSHA256 string                         `json:"runtimeCandidateSha256"`
	CutGraphSHA256         string                         `json:"cutGraphSha256"`
	PieceRanges            []nativeWholeWorldPieceRangeV1 `json:"pieceRanges"`
	Positions              struct {
		Encoding   string `json:"encoding"`
		ByteLength int    `json:"byteLength"`
		SHA256     string `json:"sha256"`
		Base64     string `json:"base64"`
	} `json:"positions"`
}

type nativeWholeWorldPieceRangeV1 struct {
	PieceID        string `json:"pieceId"`
	VertexOffset   int    `json:"vertexOffset"`
	VertexCount    int    `json:"vertexCount"`
	TriangleOffset int    `json:"triangleOffset"`
	TriangleCount  int    `json:"triangleCount"`
}

type decodedNativeWholePlacementV10 struct {
	document      nativeWholePlacementDocumentV10
	retainedBytes []byte
	review        *patternProductCompositeSidecarV8Review
	world         nativeWholeWorldPlacementV1
}

// PatternProductNativeWholePlacementReceiptV10 seals the canonical carrier in its receipt domain.
func PatternProductNativeWholePlacementReceiptV10(payload []byte) string {
	return nativeCrossRowSealV8(nativeWholePlacementDomainV10, payload)
}

func nativeWholePlacementViolationV10(detail string) error {
	return NewFieldViolation("sidecar_bytes", "invalid_native_whole_placement", "", detail)
}

func decodeNativeWholePlacementV10(payload []byte) (*decodedNativeWholePlacementV10, error) {
	var parsed decodedNativeWholePlacementV10
	doc := &parsed.document
	if err := decodeCanonicalNativeCrossRowPlacementJSON(payload, doc, PatternSemanticSidecarMaxBytes); err != nil {
		return nil, nativeWholePlacementViolationV10("whole placement carrier must have the exact canonical V10 shape")
	}
	if doc.SchemaVersion != patternProductCompositeSidecarDocumentV10 ||
		doc.MediaType != PatternProductCompositeSidecarV10MediaType ||
		doc.Encoding != patternSemanticSidecarEncoding || doc.Profile != nativeWholePlacementProfileV10 {
		return nil, nativeWholePlacementViolationV10("unsupported whole placement carrier profile")
	}
	retained, world := doc.Components[0], doc.Components[1]
	if !ValidPatternSemanticSidecarSHA256(doc.WriteCAS.ExpectedPreviousSidecarSHA256) ||
		world.ByteLength > maxNativeWholePlacementBytesV10 {
		return nil, nativeWholePlacementViolationV10("a predecessor digest and a bounded whole placement component are required")
	}
	var err error
	parsed.retainedBytes, err = compositeComponentBytes(retained, retainedNativeCrossRowKindV10, retained.MediaType)
	if err != nil {
		return nil, nativeWholePlacementViolationV10("retained carrier bytes differ")
	}
	switch retained.MediaType {
	case PatternProductCompositeSidecarV8MediaType:
		parsed.review, err = decodeNativeCrossRowReviewV8(parsed.retainedBytes)
	case PatternProductCompositeSidecarV9MediaType:
		var v9 *decodedNativeCrossRowPlacementV9
		v9, err = decodeNativeCrossRowPlacementV9(parsed.retainedBytes)
		if err == nil {
			parsed.review = v9.review
		}
	default:
		return nil, nativeWholePlacementViolationV10("only an exact V8 or V9 carrier can be retained")
	}
	if err != nil || parsed.review.Bindings != doc.Bindings {
		return nil, nativeWholePlacementViolationV10("retained V8 review or Complete Source binding differs")
	}
	worldBytes, err := compositeComponentBytes(world, nativeWholeWorldPlacementKindV10, nativeWholeWorldPlacementMediaTypeV1)
	if err != nil {
		return nil, nativeWholePlacementViolationV10("whole placement component bytes differ")
	}
	if err := decodeCanonicalNativeCrossRowPlacementJSON(worldBytes, &parsed.world, maxNativeWholePlacementBytesV10); err != nil {
		return nil, nativeWholePlacementViolationV10("whole placement component is not canonical")
	}
	if parsed.world.SchemaVersion != nativeWholePlacementDomainV10 ||
		parsed.world.RetainedCarrierSHA256 != retained.SHA256 ||
		parsed.world.Layout.SizeID != parsed.review.Selection.SizeID ||
		parsed.world.Layout.SizeToken != parsed.review.Selection.SizeToken {
		return nil, nativeWholePlacementViolationV10("whole placement must retain the exact carrier and reviewed size")
	}
	if err := parsed.world.Body.validate(); err != nil {
		return nil, nativeWholePlacementViolationV10(err.Error())
	}
	if err := validateNativeWholeWorldLayoutV1(parsed.world.Layout); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func validateNativeWholeWorldLayoutV1(layout nativeWholeWorldLayoutV1) error {
	if !ValidPatternSemanticSidecarSHA256(layout.RuntimeCandidateSHA256) ||
		!ValidPatternSemanticSidecarSHA256(layout.CutGraphSHA256) {
		return nativeWholePlacementViolationV10("layout candidate and cut graph digests must be lowercase SHA-256")
	}
	if len(layout.PieceRanges) < 2 || len(layout.PieceRanges) > maxNativeWholePlacementVerticesV10 {
		return nativeWholePlacementViolationV10("whole placement requires at least two bounded piece ranges")
	}
	vertices, triangles := 0, 0
	seenPieces := make(map[string]struct{}, len(layout.PieceRanges))
	for _, piece := range layout.PieceRanges {
		if !ValidTechCardSeamKey(piece.PieceID) || piece.PieceID[0] > '7' {
			return nativeWholePlacementViolationV10("layout piece IDs must be canonical ULIDs")
		}
		if _, seen := seenPieces[piece.PieceID]; seen {
			return nativeWholePlacementViolationV10("layout piece IDs must be unique")
		}
		seenPieces[piece.PieceID] = struct{}{}
		if piece.VertexOffset != vertices || piece.TriangleOffset != triangles ||
			piece.VertexCount <= 0 || piece.VertexCount > maxNativeWholePlacementVerticesV10-vertices ||
			piece.TriangleCount <= 0 || piece.TriangleCount > maxNativeWholePlacementTrianglesV10-triangles {
			return nativeWholePlacementViolationV10("layout piece ranges must be positive, contiguous and bounded")
		}
		vertices += piece.VertexCount
		triangles += piece.TriangleCount
	}
	positions := layout.Positions
	if positions.Encoding != "float32-le-xyz" || positions.ByteLength != vertices*3*4 {
		return nativeWholePlacementViolationV10("layout positions must contain exactly three Float32 values per vertex")
	}
	const positionKind = "layout-positions"
	positionBytes, err := compositeComponentBytes(compositeComponentV3{
		Kind: positionKind, MediaType: positions.Encoding, ByteLength: positions.ByteLength,
		SHA256: positions.SHA256, Base64: positions.Base64,
	}, positionKind, positions.Encoding)
	if err != nil {
		return nativeWholePlacementViolationV10("layout position bytes or digest differ")
	}
	for offset := 0; offset < len(positionBytes); offset += 4 {
		bits := binary.LittleEndian.Uint32(positionBytes[offset : offset+4])
		coordinate := math.Float32frombits(bits)
		if bits == 0x80000000 || math.IsNaN(float64(coordinate)) || coordinate < -10 || coordinate > 10 {
			return nativeWholePlacementViolationV10("layout coordinates must be finite canonical Float32 values within 10 metres")
		}
	}
	return nil
}

func validateNativeWholePlacementWriteV10(in PatternSemanticSidecarWrite) (*decodedNativeWholePlacementV10, error) {
	parsed, err := decodeNativeWholePlacementV10(in.Payload)
	if err != nil {
		return nil, err
	}
	binding, row := parsed.document.Bindings, parsed.document.Bindings.AnchorRow
	if in.MediaType != PatternProductCompositeSidecarV10MediaType ||
		in.TechCardId != row.TechCardID || in.PatternLineKey != row.PatternLineKey ||
		in.ExpectedCardLockVersion == nil || *in.ExpectedCardLockVersion != row.CardLockVersion ||
		in.ExpectedDxfSHA256 != row.SHA256 || in.ExpectedPatternVersion != row.PatternVersion ||
		in.ExpectedSavedRowRevision == nil || *in.ExpectedSavedRowRevision != row.Revision ||
		in.ExpectedPreviousSidecarSHA256 == nil || *in.ExpectedPreviousSidecarSHA256 != parsed.document.WriteCAS.ExpectedPreviousSidecarSHA256 ||
		in.ExpectedServerAssemblyRevision == nil || *in.ExpectedServerAssemblyRevision != binding.ServerAssemblyRevision ||
		in.ExpectedAssemblyTopologySHA256 == nil || *in.ExpectedAssemblyTopologySHA256 != binding.CompleteSourceSHA256 ||
		in.SelectedOperationNumber != parsed.review.Selection.OperationNumber ||
		in.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(in.Payload) ||
		in.ExpectedPostSaveReceiptSHA256 != PatternProductNativeWholePlacementReceiptV10(in.Payload) {
		return nil, nativeWholePlacementViolationV10("request CAS, Complete Source seal or whole placement receipt differs from V10")
	}
	return parsed, nil
}

// CheckPatternProductNativeWholePlacementTransitionV10 checks the actual locked prior media and
// exact original carrier bytes. It returns the outer model authority to resolve in the same transaction.
func CheckPatternProductNativeWholePlacementTransitionV10(in PatternSemanticSidecarWrite, prior PatternSemanticSidecar) (NeutralPlacementModelAuthorityV1, error) {
	parsed, err := validateNativeWholePlacementWriteV10(in)
	if err != nil {
		return NeutralPlacementModelAuthorityV1{}, err
	}
	if prior.SidecarSHA256 != parsed.document.WriteCAS.ExpectedPreviousSidecarSHA256 {
		return NeutralPlacementModelAuthorityV1{}, fmt.Errorf("exact previous whole-placement carrier is required: %w", ErrTechCardPatternSidecarBindingMoved)
	}

	retained := parsed.document.Components[0]
	switch prior.MediaType {
	case PatternProductCompositeSidecarV8MediaType, PatternProductCompositeSidecarV9MediaType:
		if prior.ByteLength != len(prior.Payload) || prior.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(prior.Payload) {
			return NeutralPlacementModelAuthorityV1{}, fmt.Errorf("previous carrier bytes differ: %w", ErrTechCardPatternSidecarCorrupt)
		}
		if err := ValidatePatternSemanticSidecarPayloadBinding(prior.Payload, prior.TechCardId, prior.PatternLineKey,
			prior.CardLockVersion, prior.DxfSHA256, prior.PatternVersion, prior.SavedRowRevision,
			prior.PostSaveReceiptSHA256, prior.MediaType); err != nil {
			return NeutralPlacementModelAuthorityV1{}, fmt.Errorf("previous carrier metadata differs: %w: %v", ErrTechCardPatternSidecarCorrupt, err)
		}
		if retained.MediaType != prior.MediaType || !bytes.Equal(parsed.retainedBytes, prior.Payload) {
			return NeutralPlacementModelAuthorityV1{}, nativeWholePlacementViolationV10("first whole placement must wrap the exact saved V8 or V9")
		}
	case PatternProductCompositeSidecarV10MediaType:
		before, err := readNativeWholePlacementBindingV10(prior)
		if err != nil {
			return NeutralPlacementModelAuthorityV1{}, err
		}
		if retained != before.document.Components[0] {
			return NeutralPlacementModelAuthorityV1{}, nativeWholePlacementViolationV10("whole placement edits must retain the same original carrier bytes")
		}
	default:
		return NeutralPlacementModelAuthorityV1{}, fmt.Errorf("previous V8, V9 or V10 carrier is required: %w", ErrTechCardPatternSidecarBindingMoved)
	}
	return parsed.world.Body.NeutralPlacementModelAuthorityV1, nil
}

func (parsed *decodedNativeWholePlacementV10) verifyCurrent(source PatternProductNativeCrossRowCurrentV8, model NeutralPlacementModelAuthorityV1) error {
	if err := verifyNativeCrossRowReviewCurrentV8(parsed.review, source); err != nil {
		return err
	}
	if err := model.VerifyExpected(parsed.world.Body.NeutralPlacementModelAuthorityV1); err != nil {
		return fmt.Errorf("%w: %w", ErrTechCardPatternSidecarBindingMoved, err)
	}
	return nil
}

// ValidatePatternProductNativeWholePlacementWriteCurrentV10 checks the independently loaded
// Complete Source and outer neutral model under the same transaction.
func ValidatePatternProductNativeWholePlacementWriteCurrentV10(in PatternSemanticSidecarWrite, source PatternProductNativeCrossRowCurrentV8, model NeutralPlacementModelAuthorityV1) error {
	parsed, err := validateNativeWholePlacementWriteV10(in)
	if err != nil {
		return err
	}
	return parsed.verifyCurrent(source, model)
}

func readNativeWholePlacementBindingV10(stored PatternSemanticSidecar) (*decodedNativeWholePlacementV10, error) {
	if stored.ByteLength != len(stored.Payload) || stored.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(stored.Payload) {
		return nil, fmt.Errorf("stored whole-placement bytes differ: %w", ErrTechCardPatternSidecarCorrupt)
	}
	parsed, err := decodeNativeWholePlacementV10(stored.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid stored whole placement: %w: %v", ErrTechCardPatternSidecarCorrupt, err)
	}
	row := parsed.document.Bindings.AnchorRow
	if stored.MediaType != PatternProductCompositeSidecarV10MediaType ||
		stored.TechCardId != row.TechCardID || stored.PatternLineKey != row.PatternLineKey ||
		stored.CardLockVersion != row.CardLockVersion || stored.SavedRowRevision != row.Revision ||
		stored.PatternVersion != row.PatternVersion || stored.DxfSHA256 != row.SHA256 ||
		stored.PostSaveReceiptSHA256 != PatternProductNativeWholePlacementReceiptV10(stored.Payload) {
		return nil, fmt.Errorf("stored whole-placement row metadata differs: %w", ErrTechCardPatternSidecarCorrupt)
	}
	return parsed, nil
}

// ReadPatternProductNativeWholePlacementBindingV10 proves the stored shape and receipt.
// The Complete Source read additionally checks the live source and outer model.
func ReadPatternProductNativeWholePlacementBindingV10(stored PatternSemanticSidecar) (NeutralPlacementModelAuthorityV1, error) {
	parsed, err := readNativeWholePlacementBindingV10(stored)
	if err != nil {
		return NeutralPlacementModelAuthorityV1{}, err
	}
	return parsed.world.Body.NeutralPlacementModelAuthorityV1, nil
}

// ValidateStoredPatternProductNativeWholePlacementV10 checks a cold snapshot.
func ValidateStoredPatternProductNativeWholePlacementV10(stored PatternSemanticSidecar, source PatternProductNativeCrossRowCurrentV8, model NeutralPlacementModelAuthorityV1) error {
	parsed, err := readNativeWholePlacementBindingV10(stored)
	if err != nil {
		return err
	}
	return parsed.verifyCurrent(source, model)
}
