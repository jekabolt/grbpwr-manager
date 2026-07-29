package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

var ErrInvalidCampaignTransition = errors.New("invalid email campaign state transition")

type launchRow struct {
	ID        int           `db:"id"`
	Status    string        `db:"status"`
	Topic     string        `db:"topic"`
	SegmentID sql.NullInt64 `db:"segment_id"`
	Predicate []byte        `db:"predicate"`
}

func (s *Store) freezeCampaign(
	ctx context.Context,
	campaignID int,
	target entity.EmailCampaignStatus,
	scheduleAt *time.Time,
) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		row, err := storeutil.QueryNamedOne[launchRow](ctx, rep.DB(), `
			SELECT ec.id, ec.status, ec.topic, ec.segment_id, es.predicate
			FROM email_campaign ec
			LEFT JOIN email_segment es ON es.id = ec.segment_id
			WHERE ec.id = :id
			FOR UPDATE`, map[string]any{"id": campaignID})
		if err != nil {
			return fmt.Errorf("lock email campaign for launch: %w", err)
		}
		if entity.EmailCampaignStatus(row.Status) != entity.EmailCampaignStatusDraft {
			return fmt.Errorf("%w: campaign %d is %s, want draft",
				ErrInvalidCampaignTransition, campaignID, row.Status)
		}

		predicate := entity.SegmentPredicate{}
		if row.SegmentID.Valid {
			if len(row.Predicate) == 0 {
				return fmt.Errorf("campaign %d segment %d has no predicate: %w",
					campaignID, row.SegmentID.Int64, sql.ErrNoRows)
			}
			if err := json.Unmarshal(row.Predicate, &predicate); err != nil {
				return fmt.Errorf("unmarshal campaign segment predicate: %w", err)
			}
		}
		compiled, err := segment.Compile(predicate)
		if err != nil {
			return fmt.Errorf("compile launch audience: %w", err)
		}
		topic := entity.EmailCampaignTopic(row.Topic)
		if _, _, err := segment.BuildAudiencePredicate(
			compiled,
			segment.ComplianceOpts{Topic: &topic},
		); err != nil {
			return fmt.Errorf("validate launch audience: %w", err)
		}
		frozen, err := json.Marshal(predicate)
		if err != nil {
			return fmt.Errorf("marshal launch audience: %w", err)
		}

		var maxAccountID int
		if err := rep.DB().GetContext(ctx, &maxAccountID,
			`SELECT COALESCE(MAX(id), 0) FROM storefront_account`); err != nil {
			return fmt.Errorf("read campaign audience high-water: %w", err)
		}
		params := map[string]any{
			"id":             campaignID,
			"status":         string(target),
			"schedule_at":    scheduleAt,
			"predicate":      frozen,
			"max_account_id": maxAccountID,
		}
		result, err := rep.DB().NamedExecContext(ctx, `
			UPDATE email_campaign
			SET status = :status,
			    schedule_at = :schedule_at,
			    audience_predicate = :predicate,
			    audience_snapshot_at = UTC_TIMESTAMP(6),
			    fanout_max_account_id = :max_account_id,
			    fanout_cursor_account_id = 0,
			    audience_materialized_at = NULL,
			    recipient_count = NULL,
			    dispatch_error = NULL,
			    sending_started_at = CASE
			        WHEN :status = 'sending' THEN UTC_TIMESTAMP(6)
			        ELSE NULL
			    END
			WHERE id = :id AND status = 'draft'`, params)
		if err != nil {
			return fmt.Errorf("freeze email campaign audience: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("freeze email campaign rows affected: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("%w: campaign %d changed during launch",
				ErrInvalidCampaignTransition, campaignID)
		}
		return nil
	})
}

func (s *Store) ScheduleEmailCampaign(ctx context.Context, campaignID int, at time.Time) error {
	if !at.After(s.Now().UTC()) {
		return errors.New("campaign schedule time must be in the future")
	}
	at = at.UTC()
	return s.freezeCampaign(ctx, campaignID, entity.EmailCampaignStatusScheduled, &at)
}

func (s *Store) SendEmailCampaignNow(ctx context.Context, campaignID int) error {
	var statusValue string
	if err := s.DB.GetContext(ctx, &statusValue,
		`SELECT status FROM email_campaign WHERE id = ?`, campaignID); err != nil {
		return fmt.Errorf("read email campaign state: %w", err)
	}
	switch entity.EmailCampaignStatus(statusValue) {
	case entity.EmailCampaignStatusDraft:
		return s.freezeCampaign(ctx, campaignID, entity.EmailCampaignStatusSending, nil)
	case entity.EmailCampaignStatusScheduled:
		result, err := s.DB.NamedExecContext(ctx, `
			UPDATE email_campaign
			SET status = 'sending', schedule_at = NULL,
			    sending_started_at = COALESCE(sending_started_at, UTC_TIMESTAMP(6)),
			    dispatch_error = NULL
			WHERE id = :id AND status = 'scheduled'`,
			map[string]any{"id": campaignID})
		return transitionResult("send scheduled campaign now", campaignID, result, err)
	default:
		return fmt.Errorf("%w: campaign %d is %s", ErrInvalidCampaignTransition, campaignID, statusValue)
	}
}

func transitionResult(action string, campaignID int, result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s campaign %d", ErrInvalidCampaignTransition, action, campaignID)
	}
	return nil
}

func (s *Store) PauseEmailCampaign(ctx context.Context, campaignID int, dispatchError *string) error {
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign
		SET status = 'paused', dispatch_error = :dispatch_error
		WHERE id = :id AND status = 'sending'`,
		map[string]any{"id": campaignID, "dispatch_error": dispatchError})
	return transitionResult("pause email campaign", campaignID, result, err)
}

func (s *Store) ResumeEmailCampaign(ctx context.Context, campaignID int) error {
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign
		SET status = 'sending', dispatch_error = NULL,
		    sending_started_at = COALESCE(sending_started_at, UTC_TIMESTAMP(6))
		WHERE id = :id AND status = 'paused'`,
		map[string]any{"id": campaignID})
	return transitionResult("resume email campaign", campaignID, result, err)
}

func (s *Store) CancelEmailCampaign(ctx context.Context, campaignID int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		result, err := rep.DB().NamedExecContext(ctx, `
			UPDATE email_campaign
			SET status = 'cancelled', dispatch_error = NULL
			WHERE id = :id AND status IN ('scheduled','sending','paused')`,
			map[string]any{"id": campaignID})
		if err := transitionResult("cancel email campaign", campaignID, result, err); err != nil {
			return err
		}
		if _, err := rep.DB().NamedExecContext(ctx, `
			UPDATE email_campaign_recipient
			SET status = 'skipped', error_code = 'campaign_cancelled',
			    last_error = NULL, completed_at = UTC_TIMESTAMP(6),
			    claim_token = NULL, claim_expires_at = NULL
			WHERE campaign_id = :id AND status = 'pending'
			  AND claim_token IS NULL AND first_provider_attempt_at IS NULL`,
			map[string]any{"id": campaignID}); err != nil {
			return fmt.Errorf("skip unclaimed cancelled campaign recipients: %w", err)
		}
		return nil
	})
}

func (s *Store) PromoteDueEmailCampaign(ctx context.Context) (int, error) {
	promoted := 0
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		row, err := storeutil.QueryNamedOne[struct {
			ID int `db:"id"`
		}](ctx, rep.DB(), `
			SELECT id
			FROM email_campaign
			WHERE status = 'scheduled' AND schedule_at <= UTC_TIMESTAMP(6)
			ORDER BY schedule_at, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, map[string]any{})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim due scheduled campaign: %w", err)
		}
		result, err := rep.DB().NamedExecContext(ctx, `
			UPDATE email_campaign
			SET status = 'sending', schedule_at = NULL,
			    sending_started_at = COALESCE(sending_started_at, UTC_TIMESTAMP(6))
			WHERE id = :id AND status = 'scheduled' AND schedule_at <= UTC_TIMESTAMP(6)`,
			map[string]any{"id": row.ID})
		if err != nil {
			return fmt.Errorf("promote due scheduled campaign: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("promote due scheduled campaign rows: %w", err)
		}
		promoted = int(n)
		return nil
	})
	return promoted, err
}

func normalizeAudienceEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
