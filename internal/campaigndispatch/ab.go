package campaigndispatch

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// AssignVariant is stable across processes, restarts and Go map iteration.
// Its input is exactly campaign_id || NUL || normalized_email.
func AssignVariant(
	campaignID int,
	normalizedEmail string,
	abEnabled bool,
	abTestPct int,
	variantIDs []int,
) entity.EmailCampaignAudienceAssignment {
	if len(variantIDs) == 0 {
		return entity.EmailCampaignAudienceAssignment{
			Cohort: entity.EmailCampaignCohortRemainder,
		}
	}
	if !abEnabled {
		variantID := variantIDs[0]
		return entity.EmailCampaignAudienceAssignment{
			Cohort:    entity.EmailCampaignCohortRemainder,
			VariantID: &variantID,
		}
	}
	digest := sha256.Sum256([]byte(
		strconv.Itoa(campaignID) + "\x00" + strings.ToLower(strings.TrimSpace(normalizedEmail)),
	))
	bucket := binary.BigEndian.Uint32(digest[0:4]) % 100
	if int(bucket) >= abTestPct {
		return entity.EmailCampaignAudienceAssignment{
			Cohort: entity.EmailCampaignCohortRemainder,
		}
	}
	index := binary.BigEndian.Uint64(digest[8:16]) % uint64(len(variantIDs))
	variantID := variantIDs[index]
	return entity.EmailCampaignAudienceAssignment{
		Cohort:    entity.EmailCampaignCohortAB,
		VariantID: &variantID,
	}
}
