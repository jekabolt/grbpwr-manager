package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ConvertEntityTechCardStageToPb exposes the entity→proto stage mapping outside this package. The
// readiness checklist reports a stage it DERIVES (the one after the card's current one) rather than
// one read off a card, so it has no TechCard to hand to the card converters. UNKNOWN for an
// unrecognised stage, matching every other stage read.
func ConvertEntityTechCardStageToPb(s entity.TechCardStage) pb_common.TechCardStage {
	return pbTechCardStage(s)
}
