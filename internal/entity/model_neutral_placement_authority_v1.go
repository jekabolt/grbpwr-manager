package entity

import "errors"

const NeutralPlacementPoseKindV1 = "neutral-default-direct-v9"

var ErrNeutralPlacementModelMoved = errors.New("neutral placement model binding moved")

type NeutralPlacementPoseBindingV1 struct {
	Kind            string `json:"kind"`
	CanonicalSHA256 string `json:"canonicalSha256"`
}

// NeutralPlacementModelAuthorityV1 is an exact model binding. A current value
// is supplied by the model owner from the same transaction as the carrier.
type NeutralPlacementModelAuthorityV1 struct {
	ModelID                   int                           `json:"modelId"`
	ModelUpdatedAt            string                        `json:"modelUpdatedAt"`
	ModelPhysicalInputsSHA256 string                        `json:"modelPhysicalInputsSha256"`
	Pose                      NeutralPlacementPoseBindingV1 `json:"pose"`
}

// VerifyExpected compares the complete binding after loading current authority.
func (current NeutralPlacementModelAuthorityV1) VerifyExpected(expected NeutralPlacementModelAuthorityV1) error {
	if current != expected || current.ModelID <= 0 {
		return ErrNeutralPlacementModelMoved
	}
	return nil
}

// NeutralPlacementPoseCanonicalSHA256V1 hashes the same linked, zero-angle
// default as the Workbench direct-v9 viewer.
func NeutralPlacementPoseCanonicalSHA256V1() (string, error) {
	p := ModelMannequinPoseV2{
		ArmMode: ModelMannequinArmModeLinked,
		LegMode: ModelMannequinLegModeLinked,
	}
	return p.CanonicalSHA256()
}
