package admin

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// validateLabelsMockupSignGate — owner decision D-04 (labels rework, 0386): every garment label must
// carry a mockup, and the LABELS sign-off cannot be APPROVED while one does not. Only a FRESH approval
// is refused: saving is never blocked (a label is legitimately added before its artwork exists), and a
// carried approval is not re-judged — a label added after the approval makes the section read
// «changed since sign-off» through the digest instead.
//
// The labels are those of THIS payload: an approval is taken over what the payload carries (the
// presence belt refuses a LABELS approval from a payload that does not carry the labels).
func validateLabelsMockupSignGate(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || !fresh[entity.SignoffLabels] {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.Section != entity.SignoffLabels || so.State != entity.SignoffStateApproved {
			continue
		}
		missing := dto.GarmentLabelsWithoutMockup(tc)
		if len(missing) == 0 {
			return nil
		}
		return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
			"cannot approve labels: labels without a mockup", strings.Join(missing, ", "),
			"add a mockup to each of these labels, then approve")
	}
	return nil
}

// validateFreshLabelsSectionsCarried is the presence belt of the labels rework (0386), on the update
// path only (a new card has nothing stored to keep). The composition label record is kept when the
// payload omits it (nil), and the garment labels / packaging items are kept when the payload lacks
// labels_aware. An approval taken over such a payload would fingerprint «nothing» over content the
// card still holds — a signature born stale. So a fresh LABELS / PACKAGING approval is refused exactly
// when the payload would leave STORED content of that section in place; a card that has none is
// approved as before, from any client.
func validateFreshLabelsSectionsCarried(tc, stored *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || stored == nil {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved || !fresh[so.Section] {
			continue
		}
		kept := false
		switch so.Section {
		case entity.SignoffLabels:
			kept = (!tc.LabelsAware && len(stored.GarmentLabels) > 0) || (tc.CareLabel == nil && stored.CareLabel != nil)
		case entity.SignoffPackaging:
			kept = !tc.LabelsAware && len(stored.PackagingItems) > 0
		}
		if kept {
			return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
				fmt.Sprintf("cannot approve %s because this save does not carry its labels / items", so.Section), "",
				"update the admin panel (hard-refresh) and approve again")
		}
	}
	return nil
}
