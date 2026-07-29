package campaign

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestABWinnerPromotionSQLInvariants(t *testing.T) {
	for _, guard := range []string{
		"status = 'sending'",
		"ab_enabled = TRUE",
		"ab_winner_variant_id IS NULL",
	} {
		if !strings.Contains(setABWinnerSQL, guard) {
			t.Errorf("winner update lost exactly-once guard %q", guard)
		}
	}
	for _, release := range []string{
		"SET variant_id = :winner_id",
		"cohort = 'remainder'",
		"variant_id IS NULL",
	} {
		if !strings.Contains(releaseABRemainderSQL, release) {
			t.Errorf("remainder release lost condition %q", release)
		}
	}
	if !strings.Contains(
		normalizedSQL(campaignRecipientFreshClaimableSQL),
		"ecr.variant_id IS NOT NULL",
	) {
		t.Fatalf(
			"fresh claims no longer pick up winner-released rows: %q",
			campaignRecipientFreshClaimableSQL,
		)
	}
}

func TestPromoteEmailCampaignABWinnerIDsIsolatesCampaignErrors(t *testing.T) {
	var called []int
	total, err := promoteEmailCampaignABWinnerIDs(
		context.Background(),
		[]int{10, 20, 30},
		func(_ context.Context, campaignID int) (bool, error) {
			called = append(called, campaignID)
			switch campaignID {
			case 10:
				return false, errors.New("poison campaign")
			case 20:
				return true, nil
			default:
				return false, nil
			}
		},
	)
	if err != nil {
		t.Fatalf("promoteEmailCampaignABWinnerIDs() error = %v", err)
	}
	if total != 1 {
		t.Fatalf("promoted = %d, want 1", total)
	}
	if want := []int{10, 20, 30}; !reflect.DeepEqual(called, want) {
		t.Fatalf("campaign calls = %v, want %v", called, want)
	}
}
