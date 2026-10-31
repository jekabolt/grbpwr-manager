package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// SEAM DECISIONS — wire → domain (AddTechCardSeamDecisions is write only).

var seamDecisionActionFromPb = map[pb_common.TechCardSeamDecisionAction]entity.TechCardSeamDecisionAction{
	pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_ACCEPT: entity.TechCardSeamDecisionAccept,
	pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_OWN:    entity.TechCardSeamDecisionOwn,
	pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_SKIP:   entity.TechCardSeamDecisionSkip,
	pb_common.TechCardSeamDecisionAction_TECH_CARD_SEAM_DECISION_ACTION_REFUSE: entity.TechCardSeamDecisionRefuse,
}

var seamGuessTierFromPb = map[pb_common.TechCardSeamGuessTier]entity.TechCardSeamGuessTier{
	pb_common.TechCardSeamGuessTier_TECH_CARD_SEAM_GUESS_TIER_UNKNOWN:   entity.TechCardSeamGuessTierUnknown,
	pb_common.TechCardSeamGuessTier_TECH_CARD_SEAM_GUESS_TIER_CONFIDENT: entity.TechCardSeamGuessTierConfident,
	pb_common.TechCardSeamGuessTier_TECH_CARD_SEAM_GUESS_TIER_GUESS:     entity.TechCardSeamGuessTierGuess,
	pb_common.TechCardSeamGuessTier_TECH_CARD_SEAM_GUESS_TIER_NONE:      entity.TechCardSeamGuessTierNone,
}

// TechCardSeamDecisionsWriteFromPb converts an AddTechCardSeamDecisions payload and validates it.
// The count is checked on the wire slice before any copy; output-only fields (id, created_*) are
// ignored. UNKNOWN action is refused with the field named; UNKNOWN tier is legal («none shown»).
func TechCardSeamDecisionsWriteFromPb(techCardID int, decisions []*pb_common.TechCardSeamDecision, by string) (entity.TechCardSeamDecisionsWrite, error) {
	if len(decisions) > entity.TechCardSeamDecisionMaxPerCall {
		return entity.TechCardSeamDecisionsWrite{}, entity.NewFieldViolation("decisions", "too_many", "",
			fmt.Sprintf("at most %d decisions per call", entity.TechCardSeamDecisionMaxPerCall))
	}
	in := entity.TechCardSeamDecisionsWrite{TechCardId: techCardID, By: by,
		Decisions: make([]entity.TechCardSeamDecisionInput, 0, len(decisions))}
	for i, d := range decisions {
		f := fmt.Sprintf("decisions[%d]", i)
		if d == nil {
			return in, entity.NewFieldViolation(f, "required", "", "")
		}
		action, ok := seamDecisionActionFromPb[d.GetAction()]
		if !ok {
			return in, entity.NewFieldViolation(f+".action", "invalid", d.GetAction().String(), "ACCEPT, OWN, SKIP or REFUSE")
		}
		tier, ok := seamGuessTierFromPb[d.GetTier()]
		if !ok {
			return in, entity.NewFieldViolation(f+".tier", "invalid", d.GetTier().String(), "CONFIDENT, GUESS, NONE or UNKNOWN")
		}
		in.Decisions = append(in.Decisions, entity.TechCardSeamDecisionInput{
			OperationNumber: int(d.GetOperationNumber()),
			Action:          action,
			SeamKey:         d.GetSeamKey(),
			Tier:            tier,
			Shown:           d.GetShown(),
			ClientVersion:   d.GetClientVersion(),
		})
	}
	if err := entity.ValidateTechCardSeamDecisionsWrite(&in); err != nil {
		return in, err
	}
	return in, nil
}
