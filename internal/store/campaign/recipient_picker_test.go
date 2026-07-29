package campaign

import (
	"strings"
	"testing"
	"time"
)

func normalizedSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func TestDispatchCampaignPickerSkipsHeldRemainderAndKeepsRecovery(t *testing.T) {
	picker := normalizedSQL(dispatchCampaignPickerSQL)
	for _, fragment := range []string{
		"AND EXISTS (",
		normalizedSQL(campaignRecipientFreshClaimableSQL),
		normalizedSQL(campaignRecipientReclaimableSQL),
		"ORDER BY ec.id",
	} {
		if !strings.Contains(picker, fragment) {
			t.Fatalf("dispatch campaign picker lost eligibility fragment %q:\n%s", fragment, picker)
		}
	}

	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	type recipientState struct {
		variantAssigned bool
		batchAssigned   bool
		nextAttemptAt   time.Time
		claimToken      bool
		claimExpiresAt  time.Time
	}
	claimable := func(row recipientState) bool {
		fresh := row.variantAssigned &&
			!row.batchAssigned &&
			!row.nextAttemptAt.After(now)
		reclaimable := row.batchAssigned &&
			!row.nextAttemptAt.After(now) &&
			(!row.claimToken || row.claimExpiresAt.Before(now))
		return fresh || reclaimable
	}
	campaigns := []struct {
		id   int
		rows []recipientState
	}{
		{
			id: 1,
			rows: []recipientState{{
				// Lower-id A/B campaign: held remainder during decision wait.
				nextAttemptAt: now,
			}},
		},
		{
			id: 2,
			rows: []recipientState{{
				variantAssigned: true,
				nextAttemptAt:   now,
			}},
		},
	}
	picked := 0
	for _, campaign := range campaigns {
		for _, row := range campaign.rows {
			if claimable(row) {
				picked = campaign.id
				break
			}
		}
		if picked != 0 {
			break
		}
	}
	if picked != 2 {
		t.Fatalf("picked campaign %d, want higher-id claimable campaign 2", picked)
	}

	expiredBatch := recipientState{
		batchAssigned:  true,
		nextAttemptAt:  now,
		claimToken:     true,
		claimExpiresAt: now.Add(-time.Microsecond),
	}
	if !claimable(expiredBatch) {
		t.Fatal("expired persisted batch is no longer reclaimable")
	}
}
