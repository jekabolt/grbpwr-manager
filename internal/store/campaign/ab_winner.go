package campaign

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type dueABCampaignRow struct {
	ID        int    `db:"id"`
	Dimension string `db:"ab_dimension"`
}

type abVariantResultRow struct {
	VariantID     int   `db:"variant_id"`
	Delivered     int64 `db:"delivered"`
	UniqueOpened  int64 `db:"unique_opened"`
	UniqueClicked int64 `db:"unique_clicked"`
}

const setABWinnerSQL = `
	UPDATE email_campaign
	SET ab_winner_variant_id = :winner_id
	WHERE id = :campaign_id
	  AND status = 'sending'
	  AND ab_enabled = TRUE
	  AND ab_winner_variant_id IS NULL`

const releaseABRemainderSQL = `
	UPDATE email_campaign_recipient
	SET variant_id = :winner_id
	WHERE campaign_id = :campaign_id
	  AND cohort = 'remainder'
	  AND variant_id IS NULL`

const dueABCampaignWhereSQL = `
	ec.status = 'sending'
	AND ec.ab_enabled = TRUE
	AND ec.ab_winner_variant_id IS NULL
	AND ec.audience_materialized_at IS NOT NULL
	AND ec.sending_started_at IS NOT NULL
	AND UTC_TIMESTAMP(6) >= TIMESTAMPADD(
	    MINUTE, ec.ab_decision_after_minutes, ec.sending_started_at
	)
	AND NOT EXISTS (
	    SELECT 1
	    FROM email_campaign_recipient ecr
	    WHERE ecr.campaign_id = ec.id
	      AND ecr.cohort = 'ab'
	      AND ecr.status = 'pending'
	)`

func (s *Store) promoteOneEmailCampaignABWinner(
	ctx context.Context,
	campaignID int,
	selectWinner entity.EmailCampaignABWinnerSelector,
) (bool, error) {
	promoted := false
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		campaign, err := storeutil.QueryNamedOne[dueABCampaignRow](ctx, rep.DB(), `
			SELECT ec.id, ec.ab_dimension
			FROM email_campaign ec
			WHERE ec.id = :campaign_id
			  AND `+dueABCampaignWhereSQL+`
			FOR UPDATE SKIP LOCKED`, map[string]any{"campaign_id": campaignID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock due A/B campaign: %w", err)
		}
		rows, err := storeutil.QueryListNamed[abVariantResultRow](ctx, rep.DB(), `
			SELECT ecv.id AS variant_id,
			       COALESCE(SUM(ecr.delivered_at IS NOT NULL), 0) AS delivered,
			       COALESCE(SUM(ecr.first_opened_at IS NOT NULL), 0) AS unique_opened,
			       COALESCE(SUM(ecr.first_clicked_at IS NOT NULL), 0) AS unique_clicked
			FROM email_campaign_variant ecv
			LEFT JOIN email_campaign_recipient ecr
			  ON ecr.campaign_id = ecv.campaign_id
			 AND ecr.variant_id = ecv.id
			 AND ecr.cohort = 'ab'
			WHERE ecv.campaign_id = :campaign_id
			GROUP BY ecv.id
			ORDER BY ecv.id`,
			map[string]any{"campaign_id": campaign.ID})
		if err != nil {
			return fmt.Errorf("read A/B campaign variant results: %w", err)
		}
		variants := make([]entity.EmailCampaignABVariantResult, len(rows))
		for i := range rows {
			variants[i] = entity.EmailCampaignABVariantResult{
				VariantID:     rows[i].VariantID,
				Delivered:     rows[i].Delivered,
				UniqueOpened:  rows[i].UniqueOpened,
				UniqueClicked: rows[i].UniqueClicked,
			}
		}
		winnerID, err := selectWinner(entity.ABDimension(campaign.Dimension), variants)
		if err != nil {
			return fmt.Errorf("select A/B campaign %d winner: %w", campaign.ID, err)
		}
		winnerBelongs := false
		for i := range variants {
			if variants[i].VariantID == winnerID {
				winnerBelongs = true
				break
			}
		}
		if !winnerBelongs {
			return fmt.Errorf("selected A/B winner variant %d does not belong to campaign %d",
				winnerID, campaign.ID)
		}

		// Exactly-once/idempotent across worker ticks and instances: only the
		// transaction that changes NULL to the selected winner may release the
		// held remainder. A crash rolls both writes back together.
		result, err := rep.DB().NamedExecContext(ctx, setABWinnerSQL,
			map[string]any{"campaign_id": campaign.ID, "winner_id": winnerID})
		if err != nil {
			return fmt.Errorf("set A/B campaign winner: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("set A/B campaign winner rows: %w", err)
		}
		if affected == 0 {
			return nil
		}

		if _, err := rep.DB().NamedExecContext(ctx, `
			UPDATE email_campaign_variant
			SET is_winner = (id = :winner_id)
			WHERE campaign_id = :campaign_id`,
			map[string]any{"campaign_id": campaign.ID, "winner_id": winnerID}); err != nil {
			return fmt.Errorf("mark A/B campaign winner variant: %w", err)
		}
		// Assigning the winner makes every still-pending remainder row eligible
		// for the dispatcher's existing variant_id IS NOT NULL fresh claim.
		if _, err := rep.DB().NamedExecContext(ctx, releaseABRemainderSQL,
			map[string]any{"campaign_id": campaign.ID, "winner_id": winnerID}); err != nil {
			return fmt.Errorf("release A/B campaign remainder: %w", err)
		}
		promoted = true
		return nil
	})
	return promoted, err
}

func (s *Store) dueEmailCampaignABWinnerIDs(ctx context.Context) ([]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, s.DB, `
		SELECT ec.id
		FROM email_campaign ec
		WHERE `+dueABCampaignWhereSQL+`
		ORDER BY ec.sending_started_at, ec.id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list due A/B campaigns: %w", err)
	}
	ids := make([]int, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids, nil
}

func promoteEmailCampaignABWinnerIDs(
	ctx context.Context,
	campaignIDs []int,
	promote func(context.Context, int) (bool, error),
) (int, error) {
	total := 0
	for _, campaignID := range campaignIDs {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		promoted, err := promote(ctx, campaignID)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, ctxErr
			}
			// A scoped/selector/data error must not keep other campaigns from
			// promoting or prevent the worker from reaching finalize this tick.
			slog.ErrorContext(ctx, "skip failed A/B campaign winner promotion",
				slog.Int("campaign_id", campaignID),
				slog.String("err", err.Error()))
			continue
		}
		if promoted {
			total++
		}
	}
	return total, nil
}

// PromoteEmailCampaignABWinners resolves every currently eligible campaign.
// The selector must always choose a winner, including a deterministic
// zero-signal fallback, so an A/B campaign can never hang in sending.
func (s *Store) PromoteEmailCampaignABWinners(
	ctx context.Context,
	selectWinner entity.EmailCampaignABWinnerSelector,
) (int, error) {
	if selectWinner == nil {
		return 0, errors.New("A/B winner selector is required")
	}
	campaignIDs, err := s.dueEmailCampaignABWinnerIDs(ctx)
	if err != nil {
		return 0, err
	}
	return promoteEmailCampaignABWinnerIDs(
		ctx,
		campaignIDs,
		func(ctx context.Context, campaignID int) (bool, error) {
			return s.promoteOneEmailCampaignABWinner(ctx, campaignID, selectWinner)
		},
	)
}
