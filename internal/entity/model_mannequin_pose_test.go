package entity

import (
	"strings"
	"testing"
)

func mannequinPoseFixture() ModelMannequinPoseV2 {
	return ModelMannequinPoseV2{
		BodyFlexionDegrees:  8,
		BodySideBendDegrees: -4,
		BodyTwistDegrees:    5,
		ArmMode:             ModelMannequinArmModeIndependent,
		LeftArm: ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: 20, ShoulderForwardDegrees: -10,
			ElbowBendDegrees: 35, WristBendDegrees: -5,
		},
		RightArm: ModelMannequinArmPoseV1{
			ShoulderRaiseDegrees: 30, ShoulderForwardDegrees: 15,
			ElbowBendDegrees: 60, WristBendDegrees: 10,
		},
		HeadPitchDegrees:           12,
		HeadYawDegrees:             -18,
		HeadSideDegrees:            7,
		ShoulderProtractionDegrees: 8,
		ShoulderElevationDegrees:   4,
		ShoulderLevelDegrees:       -2,
		LegMode:                    ModelMannequinLegModeIndependent,
		LeftLeg: ModelMannequinLegPoseV1{
			HipForwardDegrees: 20, HipSideDegrees: -8, KneeBendDegrees: 45, AnkleBendDegrees: -10,
		},
		RightLeg: ModelMannequinLegPoseV1{
			HipForwardDegrees: -15, HipSideDegrees: 10, KneeBendDegrees: 70, AnkleBendDegrees: 15,
		},
	}
}

func TestModelMannequinPoseCanonicalJSONMatchesFrontendV2(t *testing.T) {
	pose := mannequinPoseFixture()
	payload, err := pose.CanonicalDocumentJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"grbpwr.model-mannequin-pose/v2","poseSpace":"makehuman-open-reference/direct-pose-v9","pose":{"bodyFlexionDegrees":8,"bodySideBendDegrees":-4,"bodyTwistDegrees":5,"armMode":"independent","leftArm":{"shoulderRaiseDegrees":20,"shoulderForwardDegrees":-10,"elbowBendDegrees":35,"wristBendDegrees":-5},"rightArm":{"shoulderRaiseDegrees":30,"shoulderForwardDegrees":15,"elbowBendDegrees":60,"wristBendDegrees":10},"headPitchDeg":12,"headYawDeg":-18,"headSideDeg":7,"shoulderProtractionDeg":8,"shoulderElevationDeg":4,"shoulderLevelDeg":-2,"legMode":"independent","leftLeg":{"hipForwardDeg":20,"hipSideDeg":-8,"kneeBendDeg":45,"ankleBendDeg":-10},"rightLeg":{"hipForwardDeg":-15,"hipSideDeg":10,"kneeBendDeg":70,"ankleBendDeg":15}}}`
	if string(payload) != want {
		t.Fatalf("canonical payload drifted\ngot:  %s\nwant: %s", payload, want)
	}
	hash, err := pose.CanonicalSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if hash != "2b682b280a4f57e146bf329a9adba3df30bd2fc3faa4242dcaca769d4336fee6" {
		t.Fatalf("canonical hash = %s", hash)
	}
	decoded, err := DecodeModelMannequinPoseCanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != pose {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
}

func TestModelMannequinPoseValidation(t *testing.T) {
	pose := mannequinPoseFixture()
	pose.HeadYawDegrees = 46
	if err := pose.Validate(); err == nil || !strings.Contains(err.Error(), "headYawDeg") {
		t.Fatalf("head range error = %v", err)
	}

	pose = mannequinPoseFixture()
	pose.ArmMode = ModelMannequinArmModeLinked
	if err := pose.Validate(); err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("linked arm error = %v", err)
	}

	pose = mannequinPoseFixture()
	pose.LegMode = ModelMannequinLegMode("future")
	if err := pose.Validate(); err == nil || !strings.Contains(err.Error(), "legMode") {
		t.Fatalf("leg mode error = %v", err)
	}
}

func TestDecodeModelMannequinPoseCanonicalJSONFailsClosed(t *testing.T) {
	payload, err := mannequinPoseFixture().CanonicalDocumentJSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"unknown member": strings.Replace(string(payload), `"bodyFlexionDegrees":8`, `"future":1,"bodyFlexionDegrees":8`, 1),
		"missing member": strings.Replace(string(payload), `"shoulderLevelDeg":-2,`, "", 1),
		"null limb":      strings.Replace(string(payload), `"leftArm":{"shoulderRaiseDegrees":20,"shoulderForwardDegrees":-10,"elbowBendDegrees":35,"wristBendDegrees":-5}`, `"leftArm":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeModelMannequinPoseCanonicalJSON([]byte(mutation)); err == nil {
				t.Fatal("malformed stored payload was accepted")
			}
		})
	}
}

func TestModelMannequinPoseRevisionGuardPreservesPresence(t *testing.T) {
	absent, err := NewModelMannequinPoseRevisionGuard(nil)
	if err != nil || absent.Present() || absent.Conflicts(0) || !absent.Conflicts(1) {
		t.Fatalf("absent guard = %+v, err=%v", absent, err)
	}
	one := uint64(1)
	present, err := NewModelMannequinPoseRevisionGuard(&one)
	if err != nil || !present.Present() || present.Conflicts(1) || !present.Conflicts(2) {
		t.Fatalf("present guard = %+v, err=%v", present, err)
	}
	zero := uint64(0)
	if _, err := NewModelMannequinPoseRevisionGuard(&zero); err == nil {
		t.Fatal("present revision zero was accepted")
	}
}
