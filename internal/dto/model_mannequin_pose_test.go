package dto

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/proto"
)

func pbMannequinPoseFixture(t *testing.T) *pb_common.ModelMannequinPoseV2 {
	t.Helper()
	pose := entity.ModelMannequinPoseV2{
		ArmMode:                    entity.ModelMannequinArmModeLinked,
		LegMode:                    entity.ModelMannequinLegModeLinked,
		ShoulderProtractionDegrees: 3,
		ShoulderElevationDegrees:   2,
		ShoulderLevelDegrees:       -1,
	}
	hash, err := pose.CanonicalSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return ConvertEntityModelMannequinPoseToPb(pose, hash)
}

func TestConvertPbModelMannequinPoseV2ToEntity(t *testing.T) {
	pb := pbMannequinPoseFixture(t)
	pose, err := ConvertPbModelMannequinPoseV2ToEntity(pb)
	if err != nil {
		t.Fatal(err)
	}
	if pose.ShoulderProtractionDegrees != 3 || pose.ShoulderElevationDegrees != 2 || pose.ShoulderLevelDegrees != -1 {
		t.Fatalf("shoulder posture lost: %+v", pose)
	}

	badHash := proto.Clone(pb).(*pb_common.ModelMannequinPoseV2)
	badHash.CanonicalSha256 = strings.Repeat("0", 64)
	if _, err := ConvertPbModelMannequinPoseV2ToEntity(badHash); err == nil {
		t.Fatal("wrong hash accepted")
	}
	upperHash := proto.Clone(pb).(*pb_common.ModelMannequinPoseV2)
	upperHash.CanonicalSha256 = strings.ToUpper(pb.CanonicalSha256)
	if _, err := ConvertPbModelMannequinPoseV2ToEntity(upperHash); err == nil {
		t.Fatal("uppercase hash accepted")
	}

	missingLimb := proto.Clone(pb).(*pb_common.ModelMannequinPoseV2)
	missingLimb.LeftArm = nil
	if _, err := ConvertPbModelMannequinPoseV2ToEntity(missingLimb); err == nil {
		t.Fatal("missing limb accepted")
	}

	wrongSpace := proto.Clone(pb).(*pb_common.ModelMannequinPoseV2)
	wrongSpace.PoseSpace = pb_common.ModelMannequinPoseSpace_MODEL_MANNEQUIN_POSE_SPACE_MAKEHUMAN_OPEN_REFERENCE_FULL_BODY_V4
	if _, err := ConvertPbModelMannequinPoseV2ToEntity(wrongSpace); err == nil {
		t.Fatal("legacy pose space accepted as V2")
	}
}

func TestConvertPbModelMannequinPoseV2RejectsUnknownWireFields(t *testing.T) {
	// Field 99, varint value 1.
	unknown99 := []byte{0x98, 0x06, 0x01}
	mutations := map[string]func(*pb_common.ModelMannequinPoseV2){
		"pose":      func(p *pb_common.ModelMannequinPoseV2) { p.ProtoReflect().SetUnknown(unknown99) },
		"left arm":  func(p *pb_common.ModelMannequinPoseV2) { p.LeftArm.ProtoReflect().SetUnknown(unknown99) },
		"right arm": func(p *pb_common.ModelMannequinPoseV2) { p.RightArm.ProtoReflect().SetUnknown(unknown99) },
		"left leg":  func(p *pb_common.ModelMannequinPoseV2) { p.LeftLeg.ProtoReflect().SetUnknown(unknown99) },
		"right leg": func(p *pb_common.ModelMannequinPoseV2) { p.RightLeg.ProtoReflect().SetUnknown(unknown99) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			pose := proto.Clone(pbMannequinPoseFixture(t)).(*pb_common.ModelMannequinPoseV2)
			mutate(pose)
			if _, err := ConvertPbModelMannequinPoseV2ToEntity(pose); err == nil || !strings.Contains(err.Error(), "unknown protobuf fields") {
				t.Fatalf("field 99 error = %v", err)
			}
		})
	}
}

func TestConvertStoredModelMannequinPosePreservesUint64(t *testing.T) {
	pbPose := pbMannequinPoseFixture(t)
	pose, err := ConvertPbModelMannequinPoseV2ToEntity(pbPose)
	if err != nil {
		t.Fatal(err)
	}
	stored := &entity.StoredModelMannequinPose{
		ModelID: 7, Pose: pose, CanonicalSHA256: pbPose.CanonicalSha256,
		Revision: math.MaxUint64, UpdatedAt: time.Unix(123, 456).UTC(), UpdatedBy: "admin@example.com",
	}
	got := ConvertEntityStoredModelMannequinPoseToPb(stored)
	if got.GetRevision() != "18446744073709551615" || got.GetPose().GetShoulderLevelDegrees() != -1 {
		t.Fatalf("stored projection drifted: %+v", got)
	}
}

func TestParseModelMannequinPoseRevisionGuardIsLossless(t *testing.T) {
	for _, value := range []string{
		"9007199254740993", // Number.MAX_SAFE_INTEGER + 2
		"18446744073709551615",
	} {
		t.Run(value, func(t *testing.T) {
			guard, err := ParseModelMannequinPoseRevisionGuard(&value)
			if err != nil {
				t.Fatal(err)
			}
			want, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			if !guard.Present() || guard.Revision() != want {
				t.Fatalf("guard revision = %d, want %d", guard.Revision(), want)
			}
		})
	}
	absent, err := ParseModelMannequinPoseRevisionGuard(nil)
	if err != nil || absent.Present() {
		t.Fatalf("absent revision = %+v, err=%v", absent, err)
	}
}

func TestParseModelMannequinPoseRevisionGuardRejectsNonCanonicalValues(t *testing.T) {
	for _, value := range []string{
		"", "0", "00", "01", "-1", "+1", " 1", "1 ", "1.0", "abc",
		"18446744073709551616",
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			if _, err := ParseModelMannequinPoseRevisionGuard(&value); err == nil {
				t.Fatalf("non-canonical revision %q was accepted", value)
			}
		})
	}
}
