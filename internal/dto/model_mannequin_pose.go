package dto

import (
	"crypto/subtle"
	"fmt"
	"regexp"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var modelMannequinPoseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var modelMannequinPoseRevisionDecimal = regexp.MustCompile(`^[1-9][0-9]*$`)

// ParseModelMannequinPoseRevisionGuard preserves optional presence while
// keeping the wire lossless for JavaScript. Only the canonical positive base-10
// representation of uint64 is admitted: no zero, sign, whitespace or padding.
func ParseModelMannequinPoseRevisionGuard(value *string) (entity.ModelMannequinPoseRevisionGuard, error) {
	if value == nil {
		return entity.NewModelMannequinPoseRevisionGuard(nil)
	}
	if !modelMannequinPoseRevisionDecimal.MatchString(*value) {
		return entity.ModelMannequinPoseRevisionGuard{}, fmt.Errorf("expected_revision must be a canonical positive decimal uint64 string")
	}
	revision, err := strconv.ParseUint(*value, 10, 64)
	if err != nil {
		return entity.ModelMannequinPoseRevisionGuard{}, fmt.Errorf("expected_revision must be a canonical positive decimal uint64 string")
	}
	return entity.NewModelMannequinPoseRevisionGuard(&revision)
}

func modelMannequinArmModeFromPB(mode pb_common.ModelMannequinArmMode) (entity.ModelMannequinArmMode, error) {
	switch mode {
	case pb_common.ModelMannequinArmMode_MODEL_MANNEQUIN_ARM_MODE_LINKED:
		return entity.ModelMannequinArmModeLinked, nil
	case pb_common.ModelMannequinArmMode_MODEL_MANNEQUIN_ARM_MODE_INDEPENDENT:
		return entity.ModelMannequinArmModeIndependent, nil
	default:
		return "", fmt.Errorf("arm_mode must be LINKED or INDEPENDENT")
	}
}

func modelMannequinLegModeFromPB(mode pb_common.ModelMannequinLegMode) (entity.ModelMannequinLegMode, error) {
	switch mode {
	case pb_common.ModelMannequinLegMode_MODEL_MANNEQUIN_LEG_MODE_LINKED:
		return entity.ModelMannequinLegModeLinked, nil
	case pb_common.ModelMannequinLegMode_MODEL_MANNEQUIN_LEG_MODE_INDEPENDENT:
		return entity.ModelMannequinLegModeIndependent, nil
	default:
		return "", fmt.Errorf("leg_mode must be LINKED or INDEPENDENT")
	}
}

// ConvertPbModelMannequinPoseV2ToEntity validates the complete current V2
// envelope and verifies the client digest against server canonical bytes.
func ConvertPbModelMannequinPoseV2ToEntity(pb *pb_common.ModelMannequinPoseV2) (entity.ModelMannequinPoseV2, error) {
	var zero entity.ModelMannequinPoseV2
	if pb == nil {
		return zero, fmt.Errorf("pose is required")
	}
	if len(pb.ProtoReflect().GetUnknown()) != 0 {
		return zero, fmt.Errorf("pose contains unknown protobuf fields")
	}
	if pb.GetSchemaVersion() != entity.ModelMannequinPoseSchemaVersionV2 {
		return zero, fmt.Errorf("schema_version must be exactly %d", entity.ModelMannequinPoseSchemaVersionV2)
	}
	if pb.GetPoseSpace() != pb_common.ModelMannequinPoseSpace_MODEL_MANNEQUIN_POSE_SPACE_MAKEHUMAN_OPEN_REFERENCE_DIRECT_POSE_V9 {
		return zero, fmt.Errorf("pose_space must be MAKEHUMAN_OPEN_REFERENCE_DIRECT_POSE_V9")
	}
	if pb.LeftArm == nil || pb.RightArm == nil || pb.LeftLeg == nil || pb.RightLeg == nil {
		return zero, fmt.Errorf("left_arm, right_arm, left_leg and right_leg are required")
	}
	if len(pb.LeftArm.ProtoReflect().GetUnknown()) != 0 {
		return zero, fmt.Errorf("left_arm contains unknown protobuf fields")
	}
	if len(pb.RightArm.ProtoReflect().GetUnknown()) != 0 {
		return zero, fmt.Errorf("right_arm contains unknown protobuf fields")
	}
	if len(pb.LeftLeg.ProtoReflect().GetUnknown()) != 0 {
		return zero, fmt.Errorf("left_leg contains unknown protobuf fields")
	}
	if len(pb.RightLeg.ProtoReflect().GetUnknown()) != 0 {
		return zero, fmt.Errorf("right_leg contains unknown protobuf fields")
	}
	armMode, err := modelMannequinArmModeFromPB(pb.GetArmMode())
	if err != nil {
		return zero, err
	}
	legMode, err := modelMannequinLegModeFromPB(pb.GetLegMode())
	if err != nil {
		return zero, err
	}
	pose := entity.ModelMannequinPoseV2{
		BodyFlexionDegrees:  pb.GetBodyFlexionDegrees(),
		BodySideBendDegrees: pb.GetBodySideBendDegrees(),
		BodyTwistDegrees:    pb.GetBodyTwistDegrees(),
		ArmMode:             armMode,
		LeftArm: entity.ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: pb.LeftArm.GetShoulderRaiseDegrees(), ShoulderForwardDegrees: pb.LeftArm.GetShoulderForwardDegrees(),
			ElbowBendDegrees: pb.LeftArm.GetElbowBendDegrees(), WristBendDegrees: pb.LeftArm.GetWristBendDegrees(),
		},
		RightArm: entity.ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: pb.RightArm.GetShoulderRaiseDegrees(), ShoulderForwardDegrees: pb.RightArm.GetShoulderForwardDegrees(),
			ElbowBendDegrees: pb.RightArm.GetElbowBendDegrees(), WristBendDegrees: pb.RightArm.GetWristBendDegrees(),
		},
		HeadPitchDegrees:           pb.GetHeadPitchDegrees(),
		HeadYawDegrees:             pb.GetHeadYawDegrees(),
		HeadSideDegrees:            pb.GetHeadSideDegrees(),
		ShoulderProtractionDegrees: pb.GetShoulderProtractionDegrees(),
		ShoulderElevationDegrees:   pb.GetShoulderElevationDegrees(),
		ShoulderLevelDegrees:       pb.GetShoulderLevelDegrees(),
		LegMode:                    legMode,
		LeftLeg: entity.ModelMannequinLegPoseV1{
			HipForwardDegrees: pb.LeftLeg.GetHipForwardDegrees(), HipSideDegrees: pb.LeftLeg.GetHipSideDegrees(),
			KneeBendDegrees: pb.LeftLeg.GetKneeBendDegrees(), AnkleBendDegrees: pb.LeftLeg.GetAnkleBendDegrees(),
		},
		RightLeg: entity.ModelMannequinLegPoseV1{
			HipForwardDegrees: pb.RightLeg.GetHipForwardDegrees(), HipSideDegrees: pb.RightLeg.GetHipSideDegrees(),
			KneeBendDegrees: pb.RightLeg.GetKneeBendDegrees(), AnkleBendDegrees: pb.RightLeg.GetAnkleBendDegrees(),
		},
	}
	if err := pose.Validate(); err != nil {
		return zero, err
	}
	want, err := pose.CanonicalSHA256()
	if err != nil {
		return zero, err
	}
	got := pb.GetCanonicalSha256()
	if !modelMannequinPoseSHA256.MatchString(got) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return zero, fmt.Errorf("canonical_sha256 does not match the canonical V2 pose payload")
	}
	return pose, nil
}

func modelMannequinArmModeToPB(mode entity.ModelMannequinArmMode) pb_common.ModelMannequinArmMode {
	if mode == entity.ModelMannequinArmModeLinked {
		return pb_common.ModelMannequinArmMode_MODEL_MANNEQUIN_ARM_MODE_LINKED
	}
	return pb_common.ModelMannequinArmMode_MODEL_MANNEQUIN_ARM_MODE_INDEPENDENT
}

func modelMannequinLegModeToPB(mode entity.ModelMannequinLegMode) pb_common.ModelMannequinLegMode {
	if mode == entity.ModelMannequinLegModeLinked {
		return pb_common.ModelMannequinLegMode_MODEL_MANNEQUIN_LEG_MODE_LINKED
	}
	return pb_common.ModelMannequinLegMode_MODEL_MANNEQUIN_LEG_MODE_INDEPENDENT
}

func ConvertEntityModelMannequinPoseToPb(pose entity.ModelMannequinPoseV2, canonicalSHA256 string) *pb_common.ModelMannequinPoseV2 {
	return &pb_common.ModelMannequinPoseV2{
		SchemaVersion:       entity.ModelMannequinPoseSchemaVersionV2,
		PoseSpace:           pb_common.ModelMannequinPoseSpace_MODEL_MANNEQUIN_POSE_SPACE_MAKEHUMAN_OPEN_REFERENCE_DIRECT_POSE_V9,
		BodyFlexionDegrees:  pose.BodyFlexionDegrees,
		BodySideBendDegrees: pose.BodySideBendDegrees,
		BodyTwistDegrees:    pose.BodyTwistDegrees,
		ArmMode:             modelMannequinArmModeToPB(pose.ArmMode),
		LeftArm: &pb_common.ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: pose.LeftArm.ShoulderRaiseDegrees, ShoulderForwardDegrees: pose.LeftArm.ShoulderForwardDegrees,
			ElbowBendDegrees: pose.LeftArm.ElbowBendDegrees, WristBendDegrees: pose.LeftArm.WristBendDegrees,
		},
		RightArm: &pb_common.ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: pose.RightArm.ShoulderRaiseDegrees, ShoulderForwardDegrees: pose.RightArm.ShoulderForwardDegrees,
			ElbowBendDegrees: pose.RightArm.ElbowBendDegrees, WristBendDegrees: pose.RightArm.WristBendDegrees,
		},
		HeadPitchDegrees: pose.HeadPitchDegrees,
		HeadYawDegrees:   pose.HeadYawDegrees,
		HeadSideDegrees:  pose.HeadSideDegrees,
		LegMode:          modelMannequinLegModeToPB(pose.LegMode),
		LeftLeg: &pb_common.ModelMannequinLegPoseV1{
			HipForwardDegrees: pose.LeftLeg.HipForwardDegrees, HipSideDegrees: pose.LeftLeg.HipSideDegrees,
			KneeBendDegrees: pose.LeftLeg.KneeBendDegrees, AnkleBendDegrees: pose.LeftLeg.AnkleBendDegrees,
		},
		RightLeg: &pb_common.ModelMannequinLegPoseV1{
			HipForwardDegrees: pose.RightLeg.HipForwardDegrees, HipSideDegrees: pose.RightLeg.HipSideDegrees,
			KneeBendDegrees: pose.RightLeg.KneeBendDegrees, AnkleBendDegrees: pose.RightLeg.AnkleBendDegrees,
		},
		CanonicalSha256:            canonicalSHA256,
		ShoulderProtractionDegrees: pose.ShoulderProtractionDegrees,
		ShoulderElevationDegrees:   pose.ShoulderElevationDegrees,
		ShoulderLevelDegrees:       pose.ShoulderLevelDegrees,
	}
}

func ConvertEntityStoredModelMannequinPoseToPb(stored *entity.StoredModelMannequinPose) *pb_common.StoredModelMannequinPose {
	if stored == nil {
		return nil
	}
	return &pb_common.StoredModelMannequinPose{
		ModelId:   int32(stored.ModelID),
		Pose:      ConvertEntityModelMannequinPoseToPb(stored.Pose, stored.CanonicalSHA256),
		Revision:  strconv.FormatUint(stored.Revision, 10),
		UpdatedAt: timestamppb.New(stored.UpdatedAt),
		UpdatedBy: stored.UpdatedBy,
	}
}
