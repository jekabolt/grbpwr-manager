package entity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ModelMannequinPoseSchemaVersionV2  uint32 = 2
	ModelMannequinPoseDocumentSchemaV2        = "grbpwr.model-mannequin-pose/v2"
	ModelMannequinPoseSpaceDirectV9           = "makehuman-open-reference/direct-pose-v9"
)

type ModelMannequinArmMode string

const (
	ModelMannequinArmModeLinked      ModelMannequinArmMode = "linked"
	ModelMannequinArmModeIndependent ModelMannequinArmMode = "independent"
)

type ModelMannequinLegMode string

const (
	ModelMannequinLegModeLinked      ModelMannequinLegMode = "linked"
	ModelMannequinLegModeIndependent ModelMannequinLegMode = "independent"
)

// ModelMannequinArmPoseV1 deliberately follows the frontend canonical JSON
// member order. json.Marshal preserves struct field order, which is part of the
// cross-runtime SHA-256 contract.
type ModelMannequinArmPoseV1 struct {
	ShoulderRaiseDegrees   int32 `json:"shoulderRaiseDegrees"`
	ShoulderForwardDegrees int32 `json:"shoulderForwardDegrees"`
	ElbowBendDegrees       int32 `json:"elbowBendDegrees"`
	WristBendDegrees       int32 `json:"wristBendDegrees"`
}

type ModelMannequinLegPoseV1 struct {
	HipForwardDegrees int32 `json:"hipForwardDeg"`
	HipSideDegrees    int32 `json:"hipSideDeg"`
	KneeBendDegrees   int32 `json:"kneeBendDeg"`
	AnkleBendDegrees  int32 `json:"ankleBendDeg"`
}

// ModelMannequinPoseV2 is the direct-pose-v9 value, without transport audit
// fields. Its field order is the canonical frontend pose order.
type ModelMannequinPoseV2 struct {
	BodyFlexionDegrees         int32                   `json:"bodyFlexionDegrees"`
	BodySideBendDegrees        int32                   `json:"bodySideBendDegrees"`
	BodyTwistDegrees           int32                   `json:"bodyTwistDegrees"`
	ArmMode                    ModelMannequinArmMode   `json:"armMode"`
	LeftArm                    ModelMannequinArmPoseV1 `json:"leftArm"`
	RightArm                   ModelMannequinArmPoseV1 `json:"rightArm"`
	HeadPitchDegrees           int32                   `json:"headPitchDeg"`
	HeadYawDegrees             int32                   `json:"headYawDeg"`
	HeadSideDegrees            int32                   `json:"headSideDeg"`
	ShoulderProtractionDegrees int32                   `json:"shoulderProtractionDeg"`
	ShoulderElevationDegrees   int32                   `json:"shoulderElevationDeg"`
	ShoulderLevelDegrees       int32                   `json:"shoulderLevelDeg"`
	LegMode                    ModelMannequinLegMode   `json:"legMode"`
	LeftLeg                    ModelMannequinLegPoseV1 `json:"leftLeg"`
	RightLeg                   ModelMannequinLegPoseV1 `json:"rightLeg"`
}

type modelMannequinPoseDocumentV2 struct {
	Schema    string               `json:"schema"`
	PoseSpace string               `json:"poseSpace"`
	Pose      ModelMannequinPoseV2 `json:"pose"`
}

type StoredModelMannequinPose struct {
	ModelID         int
	Pose            ModelMannequinPoseV2
	CanonicalSHA256 string
	Revision        uint64
	UpdatedAt       time.Time
	UpdatedBy       string
}

var (
	ErrModelMannequinPoseConflict          = errors.New("model mannequin pose revision conflict")
	ErrModelMannequinPoseCorrupt           = errors.New("stored model mannequin pose failed integrity validation")
	ErrModelMannequinPoseRevisionExhausted = errors.New("model mannequin pose revision exhausted")
)

type ModelMannequinPoseConflictError struct {
	CurrentRevision uint64
}

func (e *ModelMannequinPoseConflictError) Error() string {
	return fmt.Sprintf("%v: current revision is %d", ErrModelMannequinPoseConflict, e.CurrentRevision)
}

func (e *ModelMannequinPoseConflictError) Unwrap() error {
	return ErrModelMannequinPoseConflict
}

// ModelMannequinPoseRevisionGuard preserves proto3 optional presence. An absent
// guard means "I observed no pose"; it is not an unconditional update.
type ModelMannequinPoseRevisionGuard struct {
	revision uint64
	present  bool
}

func NewModelMannequinPoseRevisionGuard(v *uint64) (ModelMannequinPoseRevisionGuard, error) {
	if v == nil {
		return ModelMannequinPoseRevisionGuard{}, nil
	}
	if *v == 0 {
		return ModelMannequinPoseRevisionGuard{}, fmt.Errorf("expected_revision must be positive when present")
	}
	return ModelMannequinPoseRevisionGuard{revision: *v, present: true}, nil
}

func (g ModelMannequinPoseRevisionGuard) Present() bool    { return g.present }
func (g ModelMannequinPoseRevisionGuard) Revision() uint64 { return g.revision }
func (g ModelMannequinPoseRevisionGuard) Conflicts(current uint64) bool {
	if !g.present {
		return current != 0
	}
	return current != g.revision
}

func mannequinPoseRange(name string, value, minimum, maximum int32) error {
	if value < minimum || value > maximum {
		return fmt.Errorf("%s must remain within %d..%d degrees", name, minimum, maximum)
	}
	return nil
}

func (p ModelMannequinPoseV2) Validate() error {
	checks := []struct {
		name            string
		value, min, max int32
	}{
		{"bodyFlexionDegrees", p.BodyFlexionDegrees, -20, 20},
		{"bodySideBendDegrees", p.BodySideBendDegrees, -12, 12},
		{"bodyTwistDegrees", p.BodyTwistDegrees, -15, 15},
		{"leftArm.shoulderRaiseDegrees", p.LeftArm.ShoulderRaiseDegrees, -60, 60},
		{"leftArm.shoulderForwardDegrees", p.LeftArm.ShoulderForwardDegrees, -30, 45},
		{"leftArm.elbowBendDegrees", p.LeftArm.ElbowBendDegrees, 0, 100},
		{"leftArm.wristBendDegrees", p.LeftArm.WristBendDegrees, -20, 20},
		{"rightArm.shoulderRaiseDegrees", p.RightArm.ShoulderRaiseDegrees, -60, 60},
		{"rightArm.shoulderForwardDegrees", p.RightArm.ShoulderForwardDegrees, -30, 45},
		{"rightArm.elbowBendDegrees", p.RightArm.ElbowBendDegrees, 0, 100},
		{"rightArm.wristBendDegrees", p.RightArm.WristBendDegrees, -20, 20},
		{"headPitchDeg", p.HeadPitchDegrees, -25, 25},
		{"headYawDeg", p.HeadYawDegrees, -45, 45},
		{"headSideDeg", p.HeadSideDegrees, -20, 20},
		{"shoulderProtractionDeg", p.ShoulderProtractionDegrees, -10, 10},
		{"shoulderElevationDeg", p.ShoulderElevationDegrees, -6, 6},
		{"shoulderLevelDeg", p.ShoulderLevelDegrees, -4, 4},
		{"leftLeg.hipForwardDeg", p.LeftLeg.HipForwardDegrees, -45, 60},
		{"leftLeg.hipSideDeg", p.LeftLeg.HipSideDegrees, -25, 25},
		{"leftLeg.kneeBendDeg", p.LeftLeg.KneeBendDegrees, 0, 120},
		{"leftLeg.ankleBendDeg", p.LeftLeg.AnkleBendDegrees, -30, 45},
		{"rightLeg.hipForwardDeg", p.RightLeg.HipForwardDegrees, -45, 60},
		{"rightLeg.hipSideDeg", p.RightLeg.HipSideDegrees, -25, 25},
		{"rightLeg.kneeBendDeg", p.RightLeg.KneeBendDegrees, 0, 120},
		{"rightLeg.ankleBendDeg", p.RightLeg.AnkleBendDegrees, -30, 45},
	}
	for _, check := range checks {
		if err := mannequinPoseRange(check.name, check.value, check.min, check.max); err != nil {
			return err
		}
	}
	if p.ArmMode != ModelMannequinArmModeLinked && p.ArmMode != ModelMannequinArmModeIndependent {
		return fmt.Errorf("armMode must be linked or independent")
	}
	if p.LegMode != ModelMannequinLegModeLinked && p.LegMode != ModelMannequinLegModeIndependent {
		return fmt.Errorf("legMode must be linked or independent")
	}
	if p.ArmMode == ModelMannequinArmModeLinked && p.LeftArm != p.RightArm {
		return fmt.Errorf("a linked pose must contain identical left and right arms")
	}
	if p.LegMode == ModelMannequinLegModeLinked && p.LeftLeg != p.RightLeg {
		return fmt.Errorf("a linked pose must contain identical left and right legs")
	}
	return nil
}

func (p ModelMannequinPoseV2) CanonicalDocumentJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(modelMannequinPoseDocumentV2{
		Schema:    ModelMannequinPoseDocumentSchemaV2,
		PoseSpace: ModelMannequinPoseSpaceDirectV9,
		Pose:      p,
	})
}

func (p ModelMannequinPoseV2) CanonicalSHA256() (string, error) {
	payload, err := p.CanonicalDocumentJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func exactJSONKeys(raw json.RawMessage, label string, expected ...string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		if err != nil {
			return fmt.Errorf("%s must be an object: %w", label, err)
		}
		return fmt.Errorf("%s must be an object", label)
	}
	actual := make([]string, 0, len(value))
	for key, member := range value {
		actual = append(actual, key)
		if bytes.Equal(bytes.TrimSpace(member), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", label, key)
		}
	}
	sort.Strings(actual)
	sortedExpected := append([]string(nil), expected...)
	sort.Strings(sortedExpected)
	if len(actual) != len(sortedExpected) {
		return fmt.Errorf("%s must contain exactly: %v", label, sortedExpected)
	}
	for i := range actual {
		if actual[i] != sortedExpected[i] {
			return fmt.Errorf("%s must contain exactly: %v", label, sortedExpected)
		}
	}
	return nil
}

func objectMember(raw json.RawMessage, key string) json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value[key]
}

// DecodeModelMannequinPoseCanonicalJSON accepts only the exact current V2
// document shape. Legacy migration belongs in the frontend codec; persisted
// server rows are always rewritten canonically before storage.
func DecodeModelMannequinPoseCanonicalJSON(payload []byte) (ModelMannequinPoseV2, error) {
	var zero ModelMannequinPoseV2
	raw := json.RawMessage(payload)
	if err := exactJSONKeys(raw, "mannequin pose document", "schema", "poseSpace", "pose"); err != nil {
		return zero, err
	}
	poseRaw := objectMember(raw, "pose")
	if err := exactJSONKeys(poseRaw, "pose",
		"bodyFlexionDegrees", "bodySideBendDegrees", "bodyTwistDegrees", "armMode",
		"leftArm", "rightArm", "headPitchDeg", "headYawDeg", "headSideDeg",
		"shoulderProtractionDeg", "shoulderElevationDeg", "shoulderLevelDeg",
		"legMode", "leftLeg", "rightLeg"); err != nil {
		return zero, err
	}
	for _, side := range []string{"leftArm", "rightArm"} {
		if err := exactJSONKeys(objectMember(poseRaw, side), "pose."+side,
			"shoulderRaiseDegrees", "shoulderForwardDegrees", "elbowBendDegrees", "wristBendDegrees"); err != nil {
			return zero, err
		}
	}
	for _, side := range []string{"leftLeg", "rightLeg"} {
		if err := exactJSONKeys(objectMember(poseRaw, side), "pose."+side,
			"hipForwardDeg", "hipSideDeg", "kneeBendDeg", "ankleBendDeg"); err != nil {
			return zero, err
		}
	}
	var document modelMannequinPoseDocumentV2
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return zero, fmt.Errorf("decode mannequin pose document: %w", err)
	}
	if document.Schema != ModelMannequinPoseDocumentSchemaV2 {
		return zero, fmt.Errorf("unsupported mannequin pose schema %q", document.Schema)
	}
	if document.PoseSpace != ModelMannequinPoseSpaceDirectV9 {
		return zero, fmt.Errorf("unsupported mannequin pose space %q", document.PoseSpace)
	}
	if err := document.Pose.Validate(); err != nil {
		return zero, err
	}
	return document.Pose, nil
}

func (s StoredModelMannequinPose) ValidateIntegrity() error {
	if s.ModelID <= 0 || s.Revision == 0 || s.UpdatedAt.IsZero() || strings.TrimSpace(s.UpdatedBy) == "" {
		return ErrModelMannequinPoseCorrupt
	}
	want, err := s.Pose.CanonicalSHA256()
	if err != nil || want != s.CanonicalSHA256 {
		return ErrModelMannequinPoseCorrupt
	}
	return nil
}
