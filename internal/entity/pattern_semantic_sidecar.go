package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	PatternSemanticSidecarMediaType           = "application/vnd.grbpwr.pattern-dxf-semantics.v1+json"
	PatternProductSemanticSidecarMediaType    = "application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json"
	PatternProductCompositeSidecarMediaType   = "application/vnd.grbpwr.pattern-dxf-product-composite.v3+json"
	PatternProductCompositeSidecarV4MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v4+json"
	PatternProductCompositeSidecarV5MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v5+json"
	PatternProductCompositeSidecarV6MediaType = "application/vnd.grbpwr.pattern-dxf-product-composite.v6+json"
	PatternSemanticSidecarMaxBytes            = 4 * 1024 * 1024
	patternSemanticSidecarDocument            = "pattern-import-dxf-semantic-sidecar-document/1"
	patternProductSemanticSidecarDocument     = "pattern-import-dxf-product-semantic-sidecar-document/1"
	patternProductCompositeSidecarDocument    = "pattern-import-dxf-product-composite-sidecar-document/3"
	patternSemanticSidecarEncoding            = "canonical-json-utf8"
)

var (
	// ErrTechCardPatternSidecarBindingMoved means the stable line still exists, but its current DXF
	// SHA/version is not the revision the caller read. The caller must rebuild the post-save receipt
	// and sidecar over the new row; silently attaching old semantics to it would be data corruption.
	ErrTechCardPatternSidecarBindingMoved = errors.New("pattern semantic sidecar binding moved")
	// ErrTechCardPatternSidecarCorrupt means stored metadata and bytes disagree. It is an internal
	// integrity failure, never an empty/missing sidecar verdict.
	ErrTechCardPatternSidecarCorrupt = errors.New("pattern semantic sidecar is corrupt")
)

// PatternSemanticSidecarWrite is the post-save CAS request. The DXF already exists: the store locks
// the card and exact row and checks the card lock_version plus pattern SHA/version before accepting
// these bytes.
type PatternSemanticSidecarWrite struct {
	TechCardId                     int
	PatternLineKey                 string
	ExpectedCardLockVersion        *int
	ExpectedDxfSHA256              string
	ExpectedPatternVersion         int
	ExpectedSavedRowRevision       *uint32
	ExpectedPreviousSidecarSHA256  *string
	ExpectedServerAssemblyRevision *string
	ExpectedAssemblyTopologySHA256 *string // selected topology through V7, Complete Source seal for V8
	SelectedOperationNumber        int
	ExpectedPostSaveReceiptSHA256  string
	MediaType                      string
	SidecarSHA256                  string
	Payload                        []byte
	By                             string
}

// PatternSemanticSidecar is the narrow read/write echo. Payload is returned as a fresh allocation.
type PatternSemanticSidecar struct {
	PatternRowId          int       `db:"pattern_row_id"`
	TechCardId            int       `db:"tech_card_id"`
	PatternLineKey        string    `db:"pattern_line_key"`
	CardLockVersion       int       `db:"card_lock_version"`
	DxfSHA256             string    `db:"dxf_sha256"`
	PatternVersion        int       `db:"pattern_version"`
	SavedRowRevision      uint32    `db:"saved_row_revision"`
	SidecarSHA256         string    `db:"sidecar_sha256"`
	MediaType             string    `db:"media_type"`
	ByteLength            int       `db:"byte_length"`
	Payload               []byte    `db:"payload"`
	PostSaveReceiptSHA256 string    `db:"post_save_receipt_sha256"`
	CreatedAt             time.Time `db:"created_at"`
	CreatedBy             string    `db:"created_by"`
}

func ValidPatternSemanticSidecarSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func PatternSemanticSidecarPayloadSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ValidPatternSemanticSidecarMediaType(value string) bool {
	return value == PatternSemanticSidecarMediaType || value == PatternProductSemanticSidecarMediaType ||
		value == PatternProductCompositeSidecarMediaType || value == PatternProductCompositeSidecarV4MediaType ||
		value == PatternProductCompositeSidecarV5MediaType || value == PatternProductCompositeSidecarV6MediaType ||
		value == PatternProductCompositeSidecarV7MediaType || value == PatternProductCompositeSidecarV8MediaType
}

// ValidatePatternSemanticSidecarPayloadBinding checks the cross-binding duplicated inside G4F4's
// canonical JSON. A matching outer hash is only integrity: without this check a caller could attach
// perfectly hashed semantics for another card or DXF revision under the current row's metadata.
// It deliberately validates only the server-owned binding fields; mapping semantics remain browser
// authored and are not promoted to trusted authority by this parser.
func ValidatePatternSemanticSidecarPayloadBinding(
	payload []byte,
	techCardID int,
	patternLineKey string,
	cardLockVersion int,
	dxfSHA256 string,
	patternVersion int,
	savedRowRevision uint32,
	postSaveReceiptSHA256 string,
	mediaType string,
) error {
	if mediaType == PatternProductCompositeSidecarMediaType {
		return ValidateStoredPatternProductCompositeSidecar(payload, SavedPatternRowIdentity{
			TechCardID: techCardID, PatternLineKey: patternLineKey, SavedRowRevision: savedRowRevision,
			CardLockVersion: cardLockVersion, SHA256: dxfSHA256,
		}, patternVersion, postSaveReceiptSHA256)
	}
	if mediaType == PatternProductCompositeSidecarV4MediaType {
		return validateStoredPatternProductCompositeSidecarV4(payload, SavedPatternRowIdentity{
			TechCardID: techCardID, PatternLineKey: patternLineKey, SavedRowRevision: savedRowRevision,
			CardLockVersion: cardLockVersion, SHA256: dxfSHA256,
		}, patternVersion, postSaveReceiptSHA256, false)
	}
	if mediaType == PatternProductCompositeSidecarV5MediaType {
		return validateStoredPatternProductCompositeSidecarV5(payload, SavedPatternRowIdentity{
			TechCardID: techCardID, PatternLineKey: patternLineKey, SavedRowRevision: savedRowRevision,
			CardLockVersion: cardLockVersion, SHA256: dxfSHA256,
		}, patternVersion, postSaveReceiptSHA256, false)
	}
	if mediaType == PatternProductCompositeSidecarV6MediaType {
		return validateStoredPatternProductCompositeSidecarV6(payload, SavedPatternRowIdentity{
			TechCardID: techCardID, PatternLineKey: patternLineKey, SavedRowRevision: savedRowRevision,
			CardLockVersion: cardLockVersion, SHA256: dxfSHA256,
		}, patternVersion, postSaveReceiptSHA256, false)
	}
	if mediaType == PatternProductCompositeSidecarV7MediaType {
		return validateStoredPatternProductCompositeSidecarV7(payload, SavedPatternRowIdentity{
			TechCardID: techCardID, PatternLineKey: patternLineKey, SavedRowRevision: savedRowRevision,
			CardLockVersion: cardLockVersion, SHA256: dxfSHA256,
		}, patternVersion, postSaveReceiptSHA256, false)
	}
	if mediaType == PatternProductCompositeSidecarV8MediaType {
		_, err := validateNativeCrossRowReviewStoredBindingV8(payload, PatternSemanticSidecar{
			TechCardId: techCardID, PatternLineKey: patternLineKey, CardLockVersion: cardLockVersion,
			DxfSHA256: dxfSHA256, PatternVersion: patternVersion, SavedRowRevision: savedRowRevision,
			PostSaveReceiptSHA256: postSaveReceiptSHA256, MediaType: mediaType,
		})
		return err
	}
	return validateLegacyPatternSemanticSidecarPayloadBinding(payload, techCardID, patternLineKey,
		cardLockVersion, dxfSHA256, patternVersion, postSaveReceiptSHA256, mediaType)
}

func validateLegacyPatternSemanticSidecarPayloadBinding(
	payload []byte,
	techCardID int,
	patternLineKey string,
	cardLockVersion int,
	dxfSHA256 string,
	patternVersion int,
	postSaveReceiptSHA256 string,
	mediaType string,
) error {
	var document struct {
		SchemaVersion string `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Encoding      string `json:"encoding"`
		Dxf           struct {
			ExportSHA256 string `json:"exportSha256"`
		} `json:"dxf"`
		Product struct {
			ExportSHA256 string `json:"exportSha256"`
		} `json:"product"`
		Persistence struct {
			ReceiptSealSHA256 string `json:"receiptSealSha256"`
			Card              struct {
				TechCardID          int  `json:"techCardId"`
				ObservedLockVersion *int `json:"observedLockVersion"`
			} `json:"card"`
			Pattern struct {
				LineKey string `json:"lineKey"`
				Version int    `json:"version"`
				SHA256  string `json:"sha256"`
			} `json:"pattern"`
		} `json:"persistence"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return NewFieldViolation("sidecar_bytes", "invalid_json", "", "send canonical G4F4 JSON bytes")
	}
	expectedDocument := patternSemanticSidecarDocument
	exportSHA256 := document.Dxf.ExportSHA256
	if mediaType == PatternProductSemanticSidecarMediaType {
		expectedDocument = patternProductSemanticSidecarDocument
		exportSHA256 = document.Product.ExportSHA256
	}
	if mediaType != PatternSemanticSidecarMediaType && mediaType != PatternProductSemanticSidecarMediaType ||
		document.SchemaVersion != expectedDocument ||
		document.MediaType != mediaType ||
		document.Encoding != patternSemanticSidecarEncoding ||
		exportSHA256 != dxfSHA256 ||
		document.Persistence.ReceiptSealSHA256 != postSaveReceiptSHA256 ||
		document.Persistence.Card.TechCardID != techCardID ||
		document.Persistence.Card.ObservedLockVersion == nil ||
		*document.Persistence.Card.ObservedLockVersion != cardLockVersion ||
		NormalizeTechCardSeamKey(document.Persistence.Pattern.LineKey) != NormalizeTechCardSeamKey(patternLineKey) ||
		document.Persistence.Pattern.Version != patternVersion ||
		document.Persistence.Pattern.SHA256 != dxfSHA256 {
		return NewFieldViolation(
			"sidecar_bytes", "binding_mismatch", "",
			"rebuild G4F4 from the exact current card, pattern revision and G4F3 receipt")
	}
	return nil
}

// ValidatePatternSemanticSidecarWrite validates and normalises all facts that do not require the
// database. The store still computes the digest itself after copying the bytes and compares the
// current pattern row under lock.
func ValidatePatternSemanticSidecarWrite(in *PatternSemanticSidecarWrite) error {
	if in == nil {
		return NewFieldViolation("semantic_sidecar", "required", "", "")
	}
	if in.TechCardId <= 0 {
		return NewFieldViolation("tech_card_id", "required", "", "")
	}
	in.PatternLineKey = NormalizeTechCardSeamKey(in.PatternLineKey)
	if !ValidTechCardSeamKey(in.PatternLineKey) {
		return NewFieldViolation("pattern_line_key", "invalid", in.PatternLineKey, "send the current pattern row's 26-char ULID")
	}
	if in.ExpectedCardLockVersion == nil {
		return NewFieldViolation("expected_card_lock_version", "required", "", "send the card lock_version returned by the post-save read")
	}
	if *in.ExpectedCardLockVersion < 0 || *in.ExpectedCardLockVersion > math.MaxInt32 {
		return NewFieldViolation("expected_card_lock_version", "invalid", fmt.Sprint(*in.ExpectedCardLockVersion), "send the non-negative int32 lock_version returned by the post-save read")
	}
	if !ValidPatternSemanticSidecarSHA256(in.ExpectedDxfSHA256) {
		return NewFieldViolation("expected_dxf_sha256", "invalid", in.ExpectedDxfSHA256, "send a lowercase SHA-256")
	}
	if in.ExpectedPatternVersion <= 0 {
		return NewFieldViolation("expected_pattern_version", "invalid", fmt.Sprint(in.ExpectedPatternVersion), "send the positive version returned by GetTechCard")
	}
	if in.ExpectedSavedRowRevision != nil && *in.ExpectedSavedRowRevision == 0 {
		return NewFieldViolation("expected_saved_row_revision", "invalid", "0", "send the positive server-owned saved row revision")
	}
	if in.ExpectedPreviousSidecarSHA256 != nil {
		previous := *in.ExpectedPreviousSidecarSHA256
		if previous != "" && !ValidPatternSemanticSidecarSHA256(previous) {
			return NewFieldViolation("expected_previous_sidecar_sha256", "invalid", previous, "send empty for absent or a lowercase SHA-256")
		}
	}
	if !ValidPatternSemanticSidecarSHA256(in.ExpectedPostSaveReceiptSHA256) {
		return NewFieldViolation("expected_post_save_receipt_sha256", "invalid", in.ExpectedPostSaveReceiptSHA256, "send the lowercase G4F3 receipt SHA-256")
	}
	in.MediaType = strings.TrimSpace(in.MediaType)
	if !ValidPatternSemanticSidecarMediaType(in.MediaType) {
		return NewFieldViolation(
			"media_type", "unsupported", in.MediaType,
			PatternSemanticSidecarMediaType+", "+PatternProductSemanticSidecarMediaType+", "+PatternProductCompositeSidecarMediaType+", "+PatternProductCompositeSidecarV4MediaType+", "+PatternProductCompositeSidecarV5MediaType+", "+PatternProductCompositeSidecarV6MediaType+", "+PatternProductCompositeSidecarV7MediaType+" or "+PatternProductCompositeSidecarV8MediaType)
	}
	if in.MediaType == PatternProductCompositeSidecarMediaType || in.MediaType == PatternProductCompositeSidecarV4MediaType || in.MediaType == PatternProductCompositeSidecarV5MediaType || in.MediaType == PatternProductCompositeSidecarV6MediaType || in.MediaType == PatternProductCompositeSidecarV7MediaType {
		if in.ExpectedSavedRowRevision == nil || in.ExpectedPreviousSidecarSHA256 == nil ||
			in.ExpectedServerAssemblyRevision == nil || in.ExpectedAssemblyTopologySHA256 == nil ||
			in.SelectedOperationNumber <= 0 {
			return NewFieldViolation("semantic_sidecar", "assembly_cas_required", "", "composite sidecars require saved-row, prior-sidecar and Assembly CAS fields")
		}
		if !strings.HasPrefix(*in.ExpectedServerAssemblyRevision, "asm:v1:") ||
			!ValidPatternSemanticSidecarSHA256(*in.ExpectedAssemblyTopologySHA256) {
			return NewFieldViolation("semantic_sidecar", "assembly_cas_invalid", "", "send the exact Assembly receipt returned by the snapshot RPC")
		}
	}
	if len(in.Payload) == 0 || len(in.Payload) > PatternSemanticSidecarMaxBytes {
		return NewFieldViolation("sidecar_bytes", "invalid_size", fmt.Sprint(len(in.Payload)), fmt.Sprintf("send 1..%d bytes", PatternSemanticSidecarMaxBytes))
	}
	if !ValidPatternSemanticSidecarSHA256(in.SidecarSHA256) {
		return NewFieldViolation("sidecar_sha256", "invalid", in.SidecarSHA256, "send a lowercase SHA-256")
	}
	if actual := PatternSemanticSidecarPayloadSHA256(in.Payload); actual != in.SidecarSHA256 {
		return NewFieldViolation("sidecar_sha256", "digest_mismatch", in.SidecarSHA256, "hash the exact sidecar_bytes")
	}
	if in.MediaType == PatternProductCompositeSidecarV8MediaType {
		if _, err := validateNativeCrossRowReviewWriteV8(*in); err != nil {
			return err
		}
	} else if in.MediaType == PatternProductCompositeSidecarMediaType || in.MediaType == PatternProductCompositeSidecarV4MediaType || in.MediaType == PatternProductCompositeSidecarV5MediaType || in.MediaType == PatternProductCompositeSidecarV6MediaType || in.MediaType == PatternProductCompositeSidecarV7MediaType {
		expected := PatternProductCompositeSidecarExpected{
			Row: SavedPatternRowIdentity{
				TechCardID: in.TechCardId, PatternLineKey: in.PatternLineKey,
				SavedRowRevision: *in.ExpectedSavedRowRevision, CardLockVersion: *in.ExpectedCardLockVersion,
				SHA256: in.ExpectedDxfSHA256,
			},
			PatternVersion: in.ExpectedPatternVersion, PostSaveReceiptSHA256: in.ExpectedPostSaveReceiptSHA256,
			ExpectedPreviousSidecarSHA256: *in.ExpectedPreviousSidecarSHA256,
			ServerAssemblyRevision:        *in.ExpectedServerAssemblyRevision,
			AssemblyTopologySHA256:        *in.ExpectedAssemblyTopologySHA256,
			SelectedOperationNumber:       in.SelectedOperationNumber,
		}
		var err error
		switch in.MediaType {
		case PatternProductCompositeSidecarV7MediaType:
			err = validatePatternProductCompositeSidecarV7(in.Payload, expected, false, false)
		case PatternProductCompositeSidecarV6MediaType:
			err = validatePatternProductCompositeSidecarV6(in.Payload, expected, false)
		case PatternProductCompositeSidecarV5MediaType:
			err = validatePatternProductCompositeSidecarV5(in.Payload, expected, false)
		case PatternProductCompositeSidecarV4MediaType:
			err = validatePatternProductCompositeSidecarV4(in.Payload, expected, false)
		default:
			err = validatePatternProductCompositeSidecar(in.Payload, expected, false)
		}
		if err != nil {
			return err
		}
	} else if err := ValidatePatternSemanticSidecarPayloadBinding(
		in.Payload, in.TechCardId, in.PatternLineKey, *in.ExpectedCardLockVersion,
		in.ExpectedDxfSHA256, in.ExpectedPatternVersion, 0,
		in.ExpectedPostSaveReceiptSHA256, in.MediaType,
	); err != nil {
		return err
	}
	in.By = strings.TrimSpace(in.By)
	if in.By == "" || len(in.By) > 255 {
		return NewFieldViolation("created_by", "invalid", in.By, "send the authenticated admin identity")
	}
	return nil
}
