package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// RecordRecipientEngagement attributes a Resend event through the ledger's
// unique resend_email_id index. Unknown ids are intentionally a no-op: most
// Resend events belong to transactional emails, not campaigns.
//
// Webhooks may be redelivered. Event timestamps therefore use set-if-null for
// one-time facts and GREATEST for last-seen facts. Open/click counters are the
// only intentionally non-idempotent values and may over-count a redelivery.
func (s *Store) RecordRecipientEngagement(
	ctx context.Context,
	resendEmailID string,
	kind entity.EmailCampaignEngagementKind,
	at time.Time,
) error {
	resendEmailID = strings.TrimSpace(resendEmailID)
	if resendEmailID == "" {
		return nil
	}
	if at.IsZero() {
		return errors.New("campaign engagement timestamp is required")
	}
	at = at.UTC()

	var setClause string
	switch kind {
	case entity.EmailCampaignEngagementDelivered:
		setClause = `delivered_at = COALESCE(delivered_at, :at)`
	case entity.EmailCampaignEngagementOpened:
		setClause = `
			first_opened_at = COALESCE(first_opened_at, :at),
			last_opened_at = GREATEST(COALESCE(last_opened_at, :at), :at),
			open_count = open_count + 1`
	case entity.EmailCampaignEngagementClicked:
		setClause = `
			first_clicked_at = COALESCE(first_clicked_at, :at),
			last_clicked_at = GREATEST(COALESCE(last_clicked_at, :at), :at),
			click_count = click_count + 1`
	case entity.EmailCampaignEngagementBounced:
		setClause = `bounced_at = COALESCE(bounced_at, :at)`
	case entity.EmailCampaignEngagementComplained:
		setClause = `complained_at = COALESCE(complained_at, :at)`
	case entity.EmailCampaignEngagementHardFailed:
		setClause = `hard_failed_at = COALESCE(hard_failed_at, :at)`
	default:
		return fmt.Errorf("unsupported campaign engagement kind %q", kind)
	}

	_, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET `+setClause+`
		WHERE resend_email_id = :resend_email_id`,
		map[string]any{
			"resend_email_id": resendEmailID,
			"at":              at,
		})
	if err != nil {
		return fmt.Errorf("record campaign recipient %s engagement: %w", kind, err)
	}
	return nil
}
