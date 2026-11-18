package entity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	PatternProductCompositeSidecarV9MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v9+json"
	patternProductCompositeSidecarDocumentV9  = "pattern-import-dxf-product-composite-sidecar-document/9"
	nativeCrossRowPlacementProfileV9          = "saved-full-assembly/native-cross-row-stitch-world-placement/1"
	nativeCrossRowPlacementDomainV9           = "tech-card-complete-source-native-placement/1"
	nativeCrossRowWorldPlacementDocumentV1    = "tech-card-complete-source-native-world-placement/1"
	nativeCrossRowWorldPlacementMediaTypeV1   = "application/vnd.grbpwr.pattern-dxf-native-world-placement.v1+json"
	maxNativeCrossRowNestedV8Bytes            = 3*1024*1024 - 8192
	maxNativeCrossRowWorldPlacementBytesV1    = 32768
)

type nativeCrossRowPlacementDocumentV9 struct {
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

type nativeCrossRowWorldPlacementV1 struct {
	SchemaVersion string `json:"schemaVersion"`
	ReviewSHA256  string `json:"reviewSha256"`
	Body          struct {
		NeutralPlacementModelAuthorityV1
		Observed struct {
			BodyPositionsFloat32LeSHA256 string `json:"bodyPositionsFloat32LeSha256"`
			BodyTrianglesUint32LeSHA256  string `json:"bodyTrianglesUint32LeSha256"`
		} `json:"observed"`
	} `json:"body"`
	Placement struct {
		CenterMm       [3]float64 `json:"centerMm"`
		DepthOffsetsMm [2]int     `json:"depthOffsetsMm"`
		DirectedCut    struct {
			GridSHA256                              string                                 `json:"gridSha256"`
			CarrierMaterialPositionsFloat64LeSHA256 string                                 `json:"carrierMaterialPositionsFloat64LeSha256"`
			CarrierTrianglesUint32LeSHA256          string                                 `json:"carrierTrianglesUint32LeSha256"`
			PositionsInvMassFloat32LeSHA256         string                                 `json:"positionsInvMassFloat32LeSha256"`
			Pieces                                  [2]nativeCrossRowPieceWorldPlacementV1 `json:"pieces"`
		} `json:"directedCut"`
	} `json:"placement"`
}

type nativeCrossRowPieceWorldPlacementV1 struct {
	PieceID  string     `json:"pieceId"`
	OriginMm [2]float64 `json:"originMm"`
	CenterM  [3]float64 `json:"centerM"`
	Cos      float64    `json:"cos"`
	Sin      float64    `json:"sin"`
}

type decodedNativeCrossRowPlacementV9 struct {
	document    nativeCrossRowPlacementDocumentV9
	review      *patternProductCompositeSidecarV8Review
	reviewBytes []byte
	world       nativeCrossRowWorldPlacementV1
}

// PatternProductNativeCrossRowPlacementCurrentV9 must come from the checked
// Complete Source and the neutral model owner in the same server transaction.
type PatternProductNativeCrossRowPlacementCurrentV9 struct {
	Source PatternProductNativeCrossRowCurrentV8
	Model  NeutralPlacementModelAuthorityV1
}

func nativeCrossRowPlacementViolationV9(detail string) error {
	return NewFieldViolation("sidecar_bytes", "invalid_native_cross_row_placement", "", detail)
}

// PatternProductNativeCrossRowPlacementReceiptV9 hashes the canonical V9 bytes
// in the placement receipt domain, separate from the carrier's raw SHA-256.
func PatternProductNativeCrossRowPlacementReceiptV9(payload []byte) string {
	return nativeCrossRowSealV8(nativeCrossRowPlacementDomainV9, payload)
}

func decodeCanonicalNativeCrossRowPlacementJSON(payload []byte, target any, maxBytes int) error {
	if len(payload) == 0 || len(payload) > maxBytes {
		return fmt.Errorf("byte length exceeds the placement bound")
	}
	if err := decodeStrictJSON(payload, target); err != nil {
		return err
	}
	// Reencoding the typed value detects missing, duplicate or mis-cased keys,
	// including omitted zero-valued fields and fixed-length array members.
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	canonical, err := canonicalJSONWithJSNumbers(encoded)
	if err != nil || !bytes.Equal(payload, canonical) {
		return fmt.Errorf("placement JSON must have the exact canonical shape")
	}
	return nil
}

func decodeNativeCrossRowPlacementV9(payload []byte) (*decodedNativeCrossRowPlacementV9, error) {
	var parsed decodedNativeCrossRowPlacementV9
	document := &parsed.document
	if err := decodeCanonicalNativeCrossRowPlacementJSON(payload, document, PatternSemanticSidecarMaxBytes); err != nil {
		return nil, nativeCrossRowPlacementViolationV9("placement carrier is not canonical V9")
	}
	if document.SchemaVersion != patternProductCompositeSidecarDocumentV9 ||
		document.MediaType != PatternProductCompositeSidecarV9MediaType ||
		document.Encoding != patternSemanticSidecarEncoding || document.Profile != nativeCrossRowPlacementProfileV9 {
		return nil, nativeCrossRowPlacementViolationV9("unsupported placement carrier profile")
	}
	prior := document.WriteCAS.ExpectedPreviousSidecarSHA256
	reviewComponent, worldComponent := document.Components[0], document.Components[1]
	if !ValidPatternSemanticSidecarSHA256(prior) || reviewComponent.SHA256 != prior ||
		reviewComponent.ByteLength > maxNativeCrossRowNestedV8Bytes ||
		worldComponent.ByteLength > maxNativeCrossRowWorldPlacementBytesV1 {
		return nil, nativeCrossRowPlacementViolationV9("the prior V8 digest and bounded placement components are required")
	}
	var err error
	parsed.reviewBytes, err = compositeComponentBytes(reviewComponent, "native-cross-row-review-v8", PatternProductCompositeSidecarV8MediaType)
	if err != nil {
		return nil, nativeCrossRowPlacementViolationV9("retained V8 component bytes differ")
	}
	parsed.review, err = decodeNativeCrossRowReviewV8(parsed.reviewBytes)
	if err != nil || parsed.review.Bindings != document.Bindings {
		return nil, nativeCrossRowPlacementViolationV9("retained V8 review or Complete Source binding differs")
	}
	worldBytes, err := compositeComponentBytes(worldComponent, "native-cross-row-world-placement-v1", nativeCrossRowWorldPlacementMediaTypeV1)
	if err != nil {
		return nil, nativeCrossRowPlacementViolationV9("world placement component bytes differ")
	}
	if err := decodeCanonicalNativeCrossRowPlacementJSON(worldBytes, &parsed.world, maxNativeCrossRowWorldPlacementBytesV1); err != nil {
		return nil, nativeCrossRowPlacementViolationV9("world placement is not canonical")
	}
	if err := validateNativeCrossRowWorldPlacementV1(parsed.world, parsed.review, prior); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func validateNativeCrossRowWorldPlacementV1(world nativeCrossRowWorldPlacementV1, review *patternProductCompositeSidecarV8Review, prior string) error {
	body, placement := world.Body, world.Placement
	directed := placement.DirectedCut
	if world.SchemaVersion != nativeCrossRowWorldPlacementDocumentV1 || world.ReviewSHA256 != prior ||
		directed.GridSHA256 != review.Review.GridSHA256 {
		return nativeCrossRowPlacementViolationV9("world placement must retain the exact V8 review and grid")
	}
	neutralSHA, err := NeutralPlacementPoseCanonicalSHA256V1()
	if err != nil {
		return err
	}
	timestamp, timestampErr := time.Parse(time.RFC3339Nano, body.ModelUpdatedAt)
	if body.ModelID <= 0 || body.ModelID > math.MaxInt32 || timestampErr != nil || timestamp.Year() < 1 ||
		!strings.HasSuffix(body.ModelUpdatedAt, "Z") || body.ModelUpdatedAt != wirePlacementTimestampV9(timestamp) ||
		body.Pose.Kind != NeutralPlacementPoseKindV1 || body.Pose.CanonicalSHA256 != neutralSHA {
		return nativeCrossRowPlacementViolationV9("a valid persisted model with the exact neutral pose is required")
	}
	for _, digest := range []string{
		body.ModelPhysicalInputsSHA256, body.Observed.BodyPositionsFloat32LeSHA256,
		body.Observed.BodyTrianglesUint32LeSHA256, directed.CarrierMaterialPositionsFloat64LeSHA256,
		directed.CarrierTrianglesUint32LeSHA256, directed.PositionsInvMassFloat32LeSHA256,
	} {
		if !ValidPatternSemanticSidecarSHA256(digest) {
			return nativeCrossRowPlacementViolationV9("model, observed body, and ROOT evidence digests must be lowercase SHA-256")
		}
	}
	if placement.DepthOffsetsMm != [2]int{4, 0} {
		return nativeCrossRowPlacementViolationV9("the initial world placement requires ordered 4/0 mm depth offsets")
	}
	for _, coordinate := range placement.CenterMm {
		if math.Abs(coordinate) > 10000 {
			return nativeCrossRowPlacementViolationV9("initial world center lies outside its millimetre bound")
		}
	}
	for index, piece := range directed.Pieces {
		center := [3]float64{
			placement.CenterMm[0] / 1000, placement.CenterMm[1] / 1000,
			placement.CenterMm[2]/1000 + float64(placement.DepthOffsetsMm[index])/1000,
		}
		if piece.PieceID != review.Selection.Members[index].PieceLineKey || piece.CenterM != center ||
			math.Abs(piece.Cos*piece.Cos+piece.Sin*piece.Sin-1) > 1e-12 {
			return nativeCrossRowPlacementViolationV9("ordered world piece, millimetre conversion or rigid axis differs")
		}
	}
	if directed.Pieces[0].Cos != directed.Pieces[1].Cos || directed.Pieces[0].Sin != directed.Pieces[1].Sin {
		return nativeCrossRowPlacementViolationV9("both ordered world pieces must carry the same directed axis")
	}
	return nil
}

func wirePlacementTimestampV9(t time.Time) string {
	// Protobuf timestamp JSON emits UTC with 0, 3, 6 or 9 fractional digits.
	stamp := t.UTC().Format("2006-01-02T15:04:05.000000000")
	for len(stamp) > 19 && strings.HasSuffix(stamp, "000") {
		stamp = strings.TrimSuffix(stamp, "000")
	}
	return strings.TrimSuffix(stamp, ".") + "Z"
}

func validateNativeCrossRowPlacementWriteV9(in PatternSemanticSidecarWrite) (*decodedNativeCrossRowPlacementV9, error) {
	parsed, err := decodeNativeCrossRowPlacementV9(in.Payload)
	if err != nil {
		return nil, err
	}
	binding, row := parsed.document.Bindings, parsed.document.Bindings.AnchorRow
	if in.MediaType != PatternProductCompositeSidecarV9MediaType ||
		in.TechCardId != row.TechCardID || in.PatternLineKey != row.PatternLineKey ||
		in.ExpectedCardLockVersion == nil || *in.ExpectedCardLockVersion != row.CardLockVersion ||
		in.ExpectedDxfSHA256 != row.SHA256 || in.ExpectedPatternVersion != row.PatternVersion ||
		in.ExpectedSavedRowRevision == nil || *in.ExpectedSavedRowRevision != row.Revision ||
		in.ExpectedPreviousSidecarSHA256 == nil || *in.ExpectedPreviousSidecarSHA256 != parsed.document.WriteCAS.ExpectedPreviousSidecarSHA256 ||
		in.ExpectedServerAssemblyRevision == nil || *in.ExpectedServerAssemblyRevision != binding.ServerAssemblyRevision ||
		in.ExpectedAssemblyTopologySHA256 == nil || *in.ExpectedAssemblyTopologySHA256 != binding.CompleteSourceSHA256 ||
		in.SelectedOperationNumber != parsed.review.Selection.OperationNumber ||
		in.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(in.Payload) ||
		in.ExpectedPostSaveReceiptSHA256 != PatternProductNativeCrossRowPlacementReceiptV9(in.Payload) {
		return nil, nativeCrossRowPlacementViolationV9("request CAS, Complete Source seal or placement receipt differs from V9")
	}
	return parsed, nil
}

// ValidatePatternProductNativeCrossRowPlacementWriteV9 checks the staged
// request and returns the model binding to resolve. It confers no write access.
func ValidatePatternProductNativeCrossRowPlacementWriteV9(in PatternSemanticSidecarWrite) (NeutralPlacementModelAuthorityV1, error) {
	parsed, err := validateNativeCrossRowPlacementWriteV9(in)
	if err != nil {
		return NeutralPlacementModelAuthorityV1{}, err
	}
	return parsed.world.Body.NeutralPlacementModelAuthorityV1, nil
}

func (parsed *decodedNativeCrossRowPlacementV9) verifyCurrent(current PatternProductNativeCrossRowPlacementCurrentV9) error {
	if err := verifyNativeCrossRowReviewCurrentV8(parsed.review, current.Source); err != nil {
		return err
	}
	if err := current.Model.VerifyExpected(parsed.world.Body.NeutralPlacementModelAuthorityV1); err != nil {
		return fmt.Errorf("%w: %w", ErrTechCardPatternSidecarBindingMoved, err)
	}
	return nil
}

// ValidateFirstPatternProductNativeCrossRowPlacementV9 checks the exact locked
// V8 prior and current source/model. The retained ROOT and body mesh witnesses
// still require a fresh replay by their producing owners.
func ValidateFirstPatternProductNativeCrossRowPlacementV9(in PatternSemanticSidecarWrite, prior PatternSemanticSidecar, current PatternProductNativeCrossRowPlacementCurrentV9) error {
	parsed, err := validateNativeCrossRowPlacementWriteV9(in)
	if err != nil {
		return err
	}
	if prior.MediaType != PatternProductCompositeSidecarV8MediaType ||
		prior.SidecarSHA256 != parsed.document.WriteCAS.ExpectedPreviousSidecarSHA256 {
		return fmt.Errorf("exact previous V8 carrier is required: %w", ErrTechCardPatternSidecarBindingMoved)
	}
	if prior.ByteLength != len(prior.Payload) || prior.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(prior.Payload) ||
		!bytes.Equal(parsed.reviewBytes, prior.Payload) {
		return fmt.Errorf("previous V8 carrier bytes differ: %w", ErrTechCardPatternSidecarCorrupt)
	}
	if _, err := validateNativeCrossRowReviewStoredBindingV8(prior.Payload, prior); err != nil {
		return fmt.Errorf("previous V8 row metadata differs: %w", ErrTechCardPatternSidecarCorrupt)
	}
	return parsed.verifyCurrent(current)
}

func readNativeCrossRowPlacementBindingV9(stored PatternSemanticSidecar) (*decodedNativeCrossRowPlacementV9, error) {
	if stored.ByteLength != len(stored.Payload) ||
		stored.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(stored.Payload) {
		return nil, fmt.Errorf("stored placement bytes differ: %w", ErrTechCardPatternSidecarCorrupt)
	}
	parsed, err := decodeNativeCrossRowPlacementV9(stored.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid stored placement: %w: %v", ErrTechCardPatternSidecarCorrupt, err)
	}
	row := parsed.document.Bindings.AnchorRow
	if stored.MediaType != PatternProductCompositeSidecarV9MediaType ||
		stored.TechCardId != row.TechCardID || stored.PatternLineKey != row.PatternLineKey ||
		stored.CardLockVersion != row.CardLockVersion || stored.SavedRowRevision != row.Revision ||
		stored.PatternVersion != row.PatternVersion || stored.DxfSHA256 != row.SHA256 ||
		stored.PostSaveReceiptSHA256 != PatternProductNativeCrossRowPlacementReceiptV9(stored.Payload) {
		return nil, fmt.Errorf("stored placement row metadata differs: %w", ErrTechCardPatternSidecarCorrupt)
	}
	return parsed, nil
}

// ReadPatternProductNativeCrossRowPlacementBindingV9 provides structural
// readback. It does not attest to current Complete Source or model authority.
func ReadPatternProductNativeCrossRowPlacementBindingV9(stored PatternSemanticSidecar) (NeutralPlacementModelAuthorityV1, error) {
	parsed, err := readNativeCrossRowPlacementBindingV9(stored)
	if err != nil {
		return NeutralPlacementModelAuthorityV1{}, err
	}
	return parsed.world.Body.NeutralPlacementModelAuthorityV1, nil
}

// ValidateStoredPatternProductNativeCrossRowPlacementV9 checks a cold read
// against authority rebuilt in one repeatable-read server snapshot.
func ValidateStoredPatternProductNativeCrossRowPlacementV9(stored PatternSemanticSidecar, current PatternProductNativeCrossRowPlacementCurrentV9) error {
	parsed, err := readNativeCrossRowPlacementBindingV9(stored)
	if err != nil {
		return err
	}
	return parsed.verifyCurrent(current)
}
