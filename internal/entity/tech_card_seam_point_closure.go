package entity

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TechCardSeamPointClosureRole string
type TechCardSeamPointClosureIntent string

const (
	TechCardSeamButtonholeMidpoint TechCardSeamPointClosureRole   = "buttonhole_midpoint"
	TechCardSeamButtonMarkCenter   TechCardSeamPointClosureRole   = "button_mark_center"
	TechCardSeamPointClosureOpen   TechCardSeamPointClosureIntent = "open"
	TechCardSeamPointClosureClosed TechCardSeamPointClosureIntent = "closed"

	TechCardSeamPointClosureMaxInstanceDepth = 64
	TechCardSeamPointClosureMaxInstanceToken = 512
)

// TechCardSeamPointClosureAnchor identifies one point by the native occurrence in immutable DXF
// bytes. Intent and both operation references belong to the buttonhole side only.
type TechCardSeamPointClosureAnchor struct {
	PatternLineKey            string                         `json:"pattern_line_key"`
	SavedRowRevision          uint32                         `json:"saved_row_revision"`
	DXFSHA256                 string                         `json:"dxf_sha256"`
	SizeID                    int32                          `json:"size_id"`
	AliasBlockName            string                         `json:"alias_block_name"`
	NativeBlockName           string                         `json:"native_block_name"`
	SourceInstancePath        []string                       `json:"source_instance_path"`
	SourceEntityOp            *uint32                        `json:"source_entity_op"`
	Role                      TechCardSeamPointClosureRole   `json:"role"`
	Intent                    TechCardSeamPointClosureIntent `json:"intent,omitempty"`
	ButtonholeOperationNumber int32                          `json:"buttonhole_operation_number,omitempty"`
	ButtonOperationNumber     int32                          `json:"button_operation_number,omitempty"`
}

var pointClosureInstanceToken = regexp.MustCompile(`^` + annotationDXFInsert + `$`)

func ValidTechCardAssemblyRevision(token string) bool {
	value, ok := strings.CutPrefix(token, "asm:v1:")
	lineage, rawRevision, found := strings.Cut(value, ":")
	if !ok || !found || len(lineage) != 32 || strings.ToLower(lineage) != lineage {
		return false
	}
	if _, err := hex.DecodeString(lineage); err != nil {
		return false
	}
	revision, err := strconv.ParseUint(rawRevision, 10, 64)
	return err == nil && revision > 0 && rawRevision == strconv.FormatUint(revision, 10)
}

// ValidateTechCardSeamPointClosure validates the authored point pair. The store separately verifies
// the saved rows, aliases, rank, and operation references under the card lock.
func ValidateTechCardSeamPointClosure(field string, seam TechCardSeamInput) error {
	if seam.Status != TechCardSeamConfirmed || seam.Direction != TechCardSeamDirectionUnknown ||
		seam.Source != TechCardSeamSourceManual {
		return NewFieldViolation(field, "invalid_point_closure", "", "a point closure is a confirmed manual decision with no run direction")
	}
	for _, side := range []struct {
		field   string
		anchors []TechCardSeamAnchor
		role    TechCardSeamPointClosureRole
	}{
		{field + ".side_a", seam.SideA, TechCardSeamButtonholeMidpoint},
		{field + ".side_b", seam.SideB, TechCardSeamButtonMarkCenter},
	} {
		if len(side.anchors) != 1 {
			return NewFieldViolation(side.field+".parts", "point_closure_one_anchor", "", "send exactly one native point on each side")
		}
		if err := validatePointClosureAnchor(side.field+".parts[0]", side.anchors[0], side.role); err != nil {
			return err
		}
	}
	a, b := seam.SideA[0].PointClosure, seam.SideB[0].PointClosure
	if a.SizeID != b.SizeID {
		return NewFieldViolation(field+".side_b.parts[0].point_closure.size_id", "size_mismatch", "", "both points must use the same saved size")
	}
	if a.PatternLineKey == b.PatternLineKey && (a.SavedRowRevision != b.SavedRowRevision || a.DXFSHA256 != b.DXFSHA256) {
		return NewFieldViolation(field+".side_b.parts[0].point_closure", "row_mismatch", "", "both points must use one exact revision and DXF for a shared row")
	}
	if a.PatternLineKey == b.PatternLineKey && *a.SourceEntityOp == *b.SourceEntityOp &&
		strings.Join(a.SourceInstancePath, "\x00") == strings.Join(b.SourceInstancePath, "\x00") {
		return NewFieldViolation(field+".side_b.parts[0].point_closure", "duplicate_point", "", "choose two different native marks")
	}
	return nil
}

func validatePointClosureAnchor(field string, anchor TechCardSeamAnchor, role TechCardSeamPointClosureRole) error {
	if !ValidTechCardSeamKey(anchor.PieceLineKey) {
		return NewFieldViolation(field+".piece_line_key", "invalid", "", "send the exact saved piece line key")
	}
	if len(anchor.Samples) != 0 || anchor.Notches != 0 || anchor.EdgeHint != "" ||
		anchor.ContourSig != "" || anchor.Placement != nil || anchor.Layer != "" {
		return NewFieldViolation(field, "point_closure_run_fields", "", "omit sewing runs, samples and placements for a point closure")
	}
	for _, value := range []float64{anchor.PerimShare, anchor.LenMm, anchor.TurnDeg, anchor.RangeFrom, anchor.RangeTo} {
		if value != 0 || math.Signbit(value) {
			return NewFieldViolation(field, "point_closure_run_fields", "", "omit sewing run dimensions for a point closure")
		}
	}
	field += ".point_closure"
	point := anchor.PointClosure
	if point == nil || point.Role != role {
		return NewFieldViolation(field+".role", "invalid", "", "side A is the buttonhole midpoint and side B is the button mark center")
	}
	if !ValidTechCardSeamKey(point.PatternLineKey) || point.SavedRowRevision == 0 ||
		!ValidPatternSemanticSidecarSHA256(point.DXFSHA256) || point.SizeID <= 0 {
		return NewFieldViolation(field, "invalid_point_source", "", "send the exact saved pattern line, revision, DXF SHA and size")
	}
	if !validPointClosureBlockName(point.AliasBlockName) || !validPointClosureBlockName(point.NativeBlockName) {
		return NewFieldViolation(field, "invalid_point_block", "", "send the exact alias and native block names")
	}
	if point.SourceEntityOp == nil || len(point.SourceInstancePath) == 0 ||
		len(point.SourceInstancePath) > TechCardSeamPointClosureMaxInstanceDepth {
		return NewFieldViolation(field, "invalid_point_occurrence", "", "send a bounded native INSERT path and the source entity operation")
	}
	for i, token := range point.SourceInstancePath {
		if !validPointClosureInstanceToken(token) {
			return NewFieldViolation(fmt.Sprintf("%s.source_instance_path[%d]", field, i), "invalid", "", "send a canonical INSERT instance token")
		}
	}
	last := strings.Split(point.SourceInstancePath[len(point.SourceInstancePath)-1], "/")
	block, _ := hex.DecodeString(last[1])
	if string(block) != point.NativeBlockName {
		return NewFieldViolation(field+".native_block_name", "occurrence_mismatch", "", "use the block of the selected native occurrence")
	}
	if role == TechCardSeamButtonholeMidpoint {
		if (point.Intent != TechCardSeamPointClosureOpen && point.Intent != TechCardSeamPointClosureClosed) ||
			point.ButtonholeOperationNumber <= 0 || point.ButtonOperationNumber <= 0 ||
			point.ButtonholeOperationNumber == point.ButtonOperationNumber {
			return NewFieldViolation(field, "invalid_point_closure_intent", "", "send Open or Closed and distinct buttonhole and button attach steps on side A")
		}
	} else if point.Intent != "" || point.ButtonholeOperationNumber != 0 || point.ButtonOperationNumber != 0 {
		return NewFieldViolation(field, "invalid_point_closure_intent", "", "omit intent and operation references on side B")
	}
	return nil
}

func validPointClosureBlockName(value string) bool {
	return value != "" && len(value) <= 160 && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func validPointClosureInstanceToken(value string) bool {
	if len(value) > TechCardSeamPointClosureMaxInstanceToken || !pointClosureInstanceToken.MatchString(value) {
		return false
	}
	parts := strings.Split(value, "/")
	if parts[2] == "p" {
		if _, err := strconv.ParseUint(parts[3], 10, 53); err != nil {
			return false
		}
	}
	for _, index := range []int{5, 7, 9} {
		if _, err := strconv.ParseUint(parts[index], 10, 53); err != nil {
			return false
		}
	}
	return true
}

func techCardSeamPointClosurePairSig(seam TechCardSeamInput) string {
	// Size, roles, operations and immutable native source occurrence are identity. A newer
	// row revision of the same content and a change of intent update that same pair.
	var pair [2]struct {
		Piece string
		Point TechCardSeamPointClosureAnchor
	}
	for index, side := range [][]TechCardSeamAnchor{seam.SideA, seam.SideB} {
		if len(side) != 1 || side[0].PointClosure == nil {
			return ""
		}
		pair[index].Piece = side[0].PieceLineKey
		pair[index].Point = *side[0].PointClosure
		pair[index].Point.SavedRowRevision = 0
		pair[index].Point.Intent = ""
	}
	encoded, _ := json.Marshal(pair)
	return string(seam.Kind) + "|" + string(encoded)
}
