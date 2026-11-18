package entity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

const (
	PatternProductCompositeSidecarV8MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v8+json"
	patternProductCompositeSidecarDocumentV8  = "pattern-import-dxf-product-composite-sidecar-document/8"
	nativeCrossRowReviewProfileV8             = "saved-full-assembly/native-cross-row-stitch/1"
	nativeCrossRowReviewDomainV8              = "tech-card-complete-source-native-review/1"
	nativeCrossRowAnchorDomainV8              = "tech-card-saved-assembly-native-anchor/1"
	nativeCrossRowReviewSizeBasisV8           = "operator-confirmed-native"
	maxNativeCrossRowVerticesV8               = 512
	maxNativeCrossRowStationsV8               = 4096
)

var nativeCrossRowAssemblyRevisionV8 = regexp.MustCompile(`^asm:v1:[0-9a-f]{32}:[1-9][0-9]{0,19}$`)

type patternProductCompositeSidecarV8Review struct {
	SchemaVersion string                   `json:"schemaVersion"`
	MediaType     string                   `json:"mediaType"`
	Encoding      string                   `json:"encoding"`
	Profile       string                   `json:"profile"`
	Bindings      nativeCrossRowBindingsV8 `json:"bindings"`
	WriteCAS      struct {
		ExpectedPreviousSidecarSHA256 *string `json:"expectedPreviousSidecarSha256"`
	} `json:"writeCas"`
	Selection struct {
		OperationNumber   int                          `json:"operationNumber"`
		OperationOrdinal  int                          `json:"operationOrdinal"`
		OwnedSeamKeys     []string                     `json:"ownedSeamKeys"`
		SavedDirection    TechCardSeamDirection        `json:"savedDirection"`
		SavedAnchoredSize string                       `json:"savedAnchoredSize"`
		SizeID            int                          `json:"sizeId"`
		SizeToken         string                       `json:"sizeToken"`
		SizeBasis         string                       `json:"sizeBasis"`
		Members           []nativeCrossRowReviewSideV8 `json:"members"`
	} `json:"selection"`
	Review struct {
		SourceDecisionSHA256     string `json:"sourceDecisionSha256"`
		BoundSourceRequestSHA256 string `json:"boundSourceRequestSha256"`
		GridSHA256               string `json:"gridSha256"`
		StationCount             int    `json:"stationCount"`
	} `json:"review"`
}

type nativeCrossRowBindingsV8 struct {
	AnchorRow              nativeCrossRowReviewRowV8 `json:"anchorRow"`
	ServerAssemblyRevision string                    `json:"serverAssemblyRevision"`
	CompleteSourceSHA256   string                    `json:"completeSourceSha256"`
}

type nativeCrossRowReviewRowV8 struct {
	compositeSavedRowV3
	PatternVersion int `json:"patternVersion"`
}

type nativeCrossRowReviewSideV8 struct {
	Row                       nativeCrossRowReviewRowV8 `json:"row"`
	SideIndex                 int                       `json:"sideIndex"`
	PartOrdinal               int                       `json:"partOrdinal"`
	PieceLineKey              string                    `json:"pieceLineKey"`
	BlockName                 string                    `json:"blockName"`
	PieceID                   string                    `json:"pieceId"`
	ContourID                 string                    `json:"contourId"`
	NativePathID              int                       `json:"nativePathId"`
	SourceFileID              string                    `json:"sourceFileId"`
	Direction                 string                    `json:"direction"`
	OrderedVertexIDs          []string                  `json:"orderedVertexIds"`
	CapturedSavedAnchorSHA256 string                    `json:"capturedSavedAnchorSha256"`
}

// PatternProductNativeCrossRowCurrentV8 comes from the checked Complete Source builder and the
// same saved card projection. SizeTokensByID must be populated by the current size dictionary.
type PatternProductNativeCrossRowCurrentV8 struct {
	AnchorRow              SavedPatternRowIdentity
	Card                   *TechCard
	Source                 *SavedAssemblyCompleteSourceV1
	ServerAssemblyRevision string
	SizeTokensByID         map[int]string
}

func nativeCrossRowViolationV8(detail string) error {
	return NewFieldViolation("sidecar_bytes", "invalid_native_cross_row_review", "", detail)
}

func nativeCrossRowMovedV8(detail string) error {
	return fmt.Errorf("%s: %w", detail, ErrTechCardPatternSidecarBindingMoved)
}

func nativeCrossRowSealV8(domain string, payload []byte) string {
	digest := sha256.New()
	digest.Write([]byte(domain))
	digest.Write([]byte{0})
	digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

// PatternProductNativeCrossRowReviewReceiptV8 returns the hash expected by the common receipt
// field for a canonical V8 payload. Validation separately checks the canonical payload and source.
func PatternProductNativeCrossRowReviewReceiptV8(payload []byte) string {
	return nativeCrossRowSealV8(nativeCrossRowReviewDomainV8, payload)
}

func validNativeCrossRowAssemblyRevisionV8(token string) bool {
	if !nativeCrossRowAssemblyRevisionV8.MatchString(token) {
		return false
	}
	_, err := strconv.ParseUint(token[strings.LastIndexByte(token, ':')+1:], 10, 64)
	return err == nil
}

func validNativeCrossRowReviewRowV8(row nativeCrossRowReviewRowV8, scope string) bool {
	return row.ScopeKey == scope && row.TechCardID <= math.MaxInt32 && row.CardLockVersion <= math.MaxInt32 &&
		row.PatternVersion > 0 && row.PatternVersion <= math.MaxInt32 &&
		ValidTechCardSeamKey(row.PatternLineKey) && row.PatternLineKey == NormalizeTechCardSeamKey(row.PatternLineKey) &&
		strings.TrimSpace(row.URL) == row.URL && validCompositeSavedRow(row.compositeSavedRowV3)
}

func nativeCrossRowSourceFileIDV8(row nativeCrossRowReviewRowV8) string {
	digest, _ := hex.DecodeString(row.SHA256) // validNativeCrossRowReviewRowV8 has already validated the SHA.
	return "s/" + strconv.FormatInt(int64(row.TechCardID), 36) + "/" + base64.RawURLEncoding.EncodeToString(digest)
}

func validNativeCrossRowReviewSideV8(side nativeCrossRowReviewSideV8, sideIndex int, sizeToken string) bool {
	if side.SideIndex != sideIndex || side.PartOrdinal != 0 ||
		!ValidTechCardSeamKey(side.PieceLineKey) || side.PieceLineKey != NormalizeTechCardSeamKey(side.PieceLineKey) ||
		side.BlockName == "" || len(side.BlockName) > 256 || strings.TrimSpace(side.BlockName) != side.BlockName ||
		strings.IndexFunc(side.BlockName, unicode.IsControl) >= 0 ||
		!exactNStableID.MatchString(side.PieceID) || side.NativePathID < 0 || side.NativePathID > math.MaxInt32 ||
		side.SourceFileID != nativeCrossRowSourceFileIDV8(side.Row) ||
		(side.Direction != "forward" && side.Direction != "reverse") ||
		len(side.OrderedVertexIDs) < 2 || len(side.OrderedVertexIDs) > maxNativeCrossRowVerticesV8 ||
		!ValidPatternSemanticSidecarSHA256(side.CapturedSavedAnchorSHA256) {
		return false
	}
	contourID := "dxf-piece-v1/p/" + hex.EncodeToString([]byte(side.PieceID)) +
		"/s/" + hex.EncodeToString([]byte(sizeToken)) + "/g/seam"
	if side.ContourID != contourID {
		return false
	}
	prefix := "dxf-vertex-v1/dxf-curve-v1/f/" + hex.EncodeToString([]byte(side.SourceFileID)) + "/"
	seen := make(map[string]struct{}, len(side.OrderedVertexIDs))
	for _, id := range side.OrderedVertexIDs {
		if !exactNStableID.MatchString(id) || len(id) <= len(prefix) || !strings.HasPrefix(id, prefix) {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func decodeNativeCrossRowReviewV8(payload []byte) (*patternProductCompositeSidecarV8Review, error) {
	if len(payload) == 0 || len(payload) > PatternSemanticSidecarMaxBytes {
		return nil, nativeCrossRowViolationV8("review payload length is invalid")
	}
	var review patternProductCompositeSidecarV8Review
	if err := decodeStrictJSON(payload, &review); err != nil {
		return nil, nativeCrossRowViolationV8("review schema is invalid")
	}
	// Rebuild canonical bytes from the typed document, so missing, repeated or mis-cased fields
	// fail even when encoding/json could otherwise decode them to the same Go value.
	encoded, err := json.Marshal(review)
	if err != nil {
		return nil, nativeCrossRowViolationV8("review cannot be encoded")
	}
	canonical, err := canonicalJSONWithJSNumbers(encoded)
	if err != nil || !bytes.Equal(payload, canonical) {
		return nil, nativeCrossRowViolationV8("review must have the exact canonical V8 shape")
	}
	selection := review.Selection
	if review.SchemaVersion != patternProductCompositeSidecarDocumentV8 || review.MediaType != PatternProductCompositeSidecarV8MediaType ||
		review.Encoding != patternSemanticSidecarEncoding || review.Profile != nativeCrossRowReviewProfileV8 ||
		review.WriteCAS.ExpectedPreviousSidecarSHA256 != nil {
		return nil, nativeCrossRowViolationV8("only the first native cross-row review is supported")
	}
	if !validNativeCrossRowReviewRowV8(review.Bindings.AnchorRow, "main") ||
		!validNativeCrossRowAssemblyRevisionV8(review.Bindings.ServerAssemblyRevision) ||
		!ValidPatternSemanticSidecarSHA256(review.Bindings.CompleteSourceSHA256) {
		return nil, nativeCrossRowViolationV8("complete source or MAIN anchor binding is invalid")
	}
	if selection.OperationNumber <= 0 || selection.OperationNumber > math.MaxInt32 ||
		selection.OperationOrdinal < 0 || selection.OperationOrdinal > math.MaxInt32 ||
		len(selection.OwnedSeamKeys) != 2 || selection.OwnedSeamKeys[0] == selection.OwnedSeamKeys[1] ||
		(selection.SavedDirection != TechCardSeamSame && selection.SavedDirection != TechCardSeamReversed) ||
		selection.SizeID <= 0 || selection.SizeID > math.MaxInt32 || len(selection.SizeToken) > 32 ||
		!exactNStableID.MatchString(selection.SizeToken) || selection.SizeBasis != nativeCrossRowReviewSizeBasisV8 ||
		(selection.SavedAnchoredSize != "" && selection.SavedAnchoredSize != selection.SizeToken) ||
		len(selection.Members) != 2 {
		return nil, nativeCrossRowViolationV8("ordered operation, physical seam, POSE or confirmed size is invalid")
	}
	for _, key := range selection.OwnedSeamKeys {
		if !ValidTechCardSeamKey(key) || key != NormalizeTechCardSeamKey(key) {
			return nil, nativeCrossRowViolationV8("owned seam keys must be canonical")
		}
	}
	a, b := selection.Members[0], selection.Members[1]
	if a.Row != review.Bindings.AnchorRow || !validNativeCrossRowReviewRowV8(b.Row, "pocketing") ||
		a.Row.TechCardID != b.Row.TechCardID || a.Row.CardLockVersion != b.Row.CardLockVersion ||
		a.Row.PatternLineKey == b.Row.PatternLineKey || a.Row.SHA256 == b.Row.SHA256 ||
		a.PieceLineKey == b.PieceLineKey ||
		!validNativeCrossRowReviewSideV8(a, 0, selection.SizeToken) || !validNativeCrossRowReviewSideV8(b, 1, selection.SizeToken) {
		return nil, nativeCrossRowViolationV8("two ordered native sewing members on MAIN and POCKETING are required")
	}
	if len(a.OrderedVertexIDs) != len(b.OrderedVertexIDs) ||
		review.Review.StationCount < len(a.OrderedVertexIDs) || review.Review.StationCount > maxNativeCrossRowStationsV8 ||
		!ValidPatternSemanticSidecarSHA256(review.Review.SourceDecisionSHA256) ||
		!ValidPatternSemanticSidecarSHA256(review.Review.BoundSourceRequestSHA256) ||
		!ValidPatternSemanticSidecarSHA256(review.Review.GridSHA256) {
		return nil, nativeCrossRowViolationV8("ROOT registration, station count or review digest is invalid")
	}
	return &review, nil
}

func validateNativeCrossRowReviewWriteV8(in PatternSemanticSidecarWrite) (*patternProductCompositeSidecarV8Review, error) {
	review, err := decodeNativeCrossRowReviewV8(in.Payload)
	if err != nil {
		return nil, err
	}
	row := review.Bindings.AnchorRow
	if in.MediaType != PatternProductCompositeSidecarV8MediaType ||
		in.TechCardId != row.TechCardID || in.PatternLineKey != row.PatternLineKey ||
		in.ExpectedCardLockVersion == nil || *in.ExpectedCardLockVersion != row.CardLockVersion ||
		in.ExpectedDxfSHA256 != row.SHA256 || in.ExpectedPatternVersion != row.PatternVersion ||
		in.ExpectedSavedRowRevision == nil || *in.ExpectedSavedRowRevision != row.Revision ||
		in.ExpectedPreviousSidecarSHA256 == nil || *in.ExpectedPreviousSidecarSHA256 != "" ||
		in.ExpectedServerAssemblyRevision == nil || *in.ExpectedServerAssemblyRevision != review.Bindings.ServerAssemblyRevision ||
		in.ExpectedAssemblyTopologySHA256 == nil || *in.ExpectedAssemblyTopologySHA256 != review.Bindings.CompleteSourceSHA256 ||
		in.SelectedOperationNumber != review.Selection.OperationNumber ||
		in.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(in.Payload) ||
		in.ExpectedPostSaveReceiptSHA256 != PatternProductNativeCrossRowReviewReceiptV8(in.Payload) {
		return nil, nativeCrossRowViolationV8("request CAS, complete source seal or review receipt differs from the V8 bytes")
	}
	return review, nil
}

// ValidateFirstPatternProductNativeCrossRowReviewV8 validates the new request and its independently
// rebuilt current source. The writer must enforce actual prior-slot absence under the same lock.
// This validation is scoped to saved-source review, not ROOT geometry.
func ValidateFirstPatternProductNativeCrossRowReviewV8(in PatternSemanticSidecarWrite, current PatternProductNativeCrossRowCurrentV8) error {
	review, err := validateNativeCrossRowReviewWriteV8(in)
	if err != nil {
		return err
	}
	return verifyNativeCrossRowReviewCurrentV8(review, current)
}

func validateNativeCrossRowReviewStoredBindingV8(payload []byte, metadata PatternSemanticSidecar) (*patternProductCompositeSidecarV8Review, error) {
	review, err := decodeNativeCrossRowReviewV8(payload)
	if err != nil {
		return nil, err
	}
	row := review.Bindings.AnchorRow
	if metadata.MediaType != PatternProductCompositeSidecarV8MediaType ||
		metadata.TechCardId != row.TechCardID || metadata.PatternLineKey != row.PatternLineKey ||
		metadata.CardLockVersion != row.CardLockVersion || metadata.SavedRowRevision != row.Revision ||
		metadata.DxfSHA256 != row.SHA256 || metadata.PatternVersion != row.PatternVersion ||
		metadata.PostSaveReceiptSHA256 != PatternProductNativeCrossRowReviewReceiptV8(payload) {
		return nil, nativeCrossRowViolationV8("native cross-row review differs from the stored row binding")
	}
	return review, nil
}

// ValidateStoredPatternProductNativeCrossRowReviewV8 verifies stored bytes against a fresh
// Complete Source projection. Successful verification still requires independent ROOT replay.
func ValidateStoredPatternProductNativeCrossRowReviewV8(stored PatternSemanticSidecar, current PatternProductNativeCrossRowCurrentV8) error {
	if stored.ByteLength != len(stored.Payload) ||
		stored.SidecarSHA256 != PatternSemanticSidecarPayloadSHA256(stored.Payload) {
		return fmt.Errorf("stored native cross-row review bytes differ: %w", ErrTechCardPatternSidecarCorrupt)
	}
	review, err := validateNativeCrossRowReviewStoredBindingV8(stored.Payload, stored)
	if err != nil {
		return fmt.Errorf("invalid stored native cross-row review: %w", ErrTechCardPatternSidecarCorrupt)
	}
	return verifyNativeCrossRowReviewCurrentV8(review, current)
}

func nativeCrossRowMatchesRowV8(expected nativeCrossRowReviewRowV8, current SavedPatternRowIdentity) bool {
	return exactCompositeRow(compositeRowIdentity(expected.compositeSavedRowV3), current) &&
		expected.PatternVersion == current.PatternVersion
}

func verifyNativeCrossRowReviewCurrentV8(review *patternProductCompositeSidecarV8Review, current PatternProductNativeCrossRowCurrentV8) error {
	source, card := current.Source, current.Card
	selection := review.Selection
	if source == nil || card == nil || source.SourceSHA256 != review.Bindings.CompleteSourceSHA256 ||
		current.ServerAssemblyRevision != review.Bindings.ServerAssemblyRevision ||
		!nativeCrossRowMatchesRowV8(review.Bindings.AnchorRow, current.AnchorRow) ||
		card.Id != current.AnchorRow.TechCardID || card.LockVersion != current.AnchorRow.CardLockVersion {
		return nativeCrossRowMovedV8("current complete source, revision or MAIN saved row differs")
	}
	if !slices.Contains(card.SizeIds, selection.SizeID) || current.SizeTokensByID[selection.SizeID] != selection.SizeToken {
		return nativeCrossRowMovedV8("confirmed size is not in the current card size run and dictionary")
	}
	if selection.OperationOrdinal >= len(card.Operations) {
		return nativeCrossRowMovedV8("selected operation ordinal is absent")
	}
	op := card.Operations[selection.OperationOrdinal]
	if !op.OperationNumber.Valid || int(op.OperationNumber.Int32) != selection.OperationNumber ||
		op.Work.String != "join_lockstitch" ||
		(op.FullnessRatio.Valid && !op.FullnessRatio.Decimal.Equal(decimal.NewFromInt(1))) ||
		!slices.Equal(op.SeamKeys, selection.OwnedSeamKeys) ||
		!slices.Equal(op.InputKeys, []string{selection.Members[0].PieceLineKey, selection.Members[1].PieceLineKey}) {
		return nativeCrossRowMovedV8("selected structural operation, seam order or ordered inputs differ")
	}

	ownedIndex := 0
	for _, previous := range card.Operations[:selection.OperationOrdinal] {
		ownedIndex += len(previous.SeamKeys)
	}
	if ownedIndex > len(source.OwnedSeams)-2 {
		return nativeCrossRowMovedV8("current owned physical seam and POSE are absent")
	}
	stitch, pose := source.OwnedSeams[ownedIndex], source.OwnedSeams[ownedIndex+1]
	if stitch.SeamKey != selection.OwnedSeamKeys[0] || stitch.Kind != TechCardSeamKindEdge ||
		stitch.Stale || stitch.Status != TechCardSeamConfirmed || stitch.Source != TechCardSeamSourceManual ||
		stitch.Direction != selection.SavedDirection || stitch.AnchoredSize != selection.SavedAnchoredSize ||
		pose.SeamKey != selection.OwnedSeamKeys[1] || pose.Kind != TechCardSeamKindPose ||
		pose.Stale || pose.Status != TechCardSeamConfirmed {
		return nativeCrossRowMovedV8("current physical seam or unchanged POSE differs")
	}

	anchorSHA, err := readNativeCrossRowAnchorSHAsV8(source.CanonicalJSON, ownedIndex, stitch.SeamKey)
	if err != nil {
		return nativeCrossRowMovedV8("current canonical saved anchors are unavailable")
	}
	stitchSides := [2][]TechCardSeamAnchor{stitch.SideA, stitch.SideB}
	poseSides := [2][]TechCardSeamAnchor{pose.SideA, pose.SideB}
	for index, member := range selection.Members {
		if !slices.ContainsFunc(source.Rows, func(row SavedPatternRowIdentity) bool {
			return nativeCrossRowMatchesRowV8(member.Row, row)
		}) || !slices.Contains(source.Aliases, SavedAssemblySourceAliasV1{
			ScopeKey: member.Row.ScopeKey, BlockName: member.BlockName, PieceLineKey: member.PieceLineKey,
		}) {
			return nativeCrossRowMovedV8("a member row or scoped alias differs")
		}
		side, poseSide := stitchSides[index], poseSides[index]
		if len(side) != 1 || len(poseSide) != 1 || side[0].PieceLineKey != member.PieceLineKey ||
			poseSide[0].PieceLineKey != member.PieceLineKey ||
			len(side[0].Samples) != TechCardSeamSamplesPerAnchor || side[0].LenMm <= 0 ||
			side[0].RangeFrom != 0 || side[0].RangeTo != 0 || side[0].Placement != nil ||
			anchorSHA[index] != member.CapturedSavedAnchorSHA256 {
			return nativeCrossRowMovedV8("a saved physical anchor or linked POSE does not match the ordered member")
		}
	}
	return nil
}

func readNativeCrossRowAnchorSHAsV8(canonical []byte, index int, expectedSeamKey string) ([2]string, error) {
	var result [2]string
	// Complete Source already supplied canonical proto JSON. Preserve the original anchor bytes,
	// including its defaults, rather than remapping to a lossy projection.
	var source struct {
		OwnedSeams []struct {
			SeamKey string                            `json:"seamKey"`
			SideA   struct{ Parts []json.RawMessage } `json:"sideA"`
			SideB   struct{ Parts []json.RawMessage } `json:"sideB"`
		} `json:"ownedSeams"`
	}
	if err := json.Unmarshal(canonical, &source); err != nil || index >= len(source.OwnedSeams) {
		return result, fmt.Errorf("missing canonical Complete Source seam")
	}
	seam := source.OwnedSeams[index]
	if seam.SeamKey != expectedSeamKey || len(seam.SideA.Parts) != 1 || len(seam.SideB.Parts) != 1 {
		return result, fmt.Errorf("canonical seam order or anchor count differs")
	}
	result[0] = nativeCrossRowSealV8(nativeCrossRowAnchorDomainV8, seam.SideA.Parts[0])
	result[1] = nativeCrossRowSealV8(nativeCrossRowAnchorDomainV8, seam.SideB.Parts[0])
	return result, nil
}
