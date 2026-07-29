package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type fanoutCampaignRow struct {
	ID                    int    `db:"id"`
	Topic                 string `db:"topic"`
	AudiencePredicate     []byte `db:"audience_predicate"`
	FanoutMaxAccountID    int    `db:"fanout_max_account_id"`
	FanoutCursorAccountID int    `db:"fanout_cursor_account_id"`
	ABEnabled             bool   `db:"ab_enabled"`
	ABTestPct             int    `db:"ab_test_pct"`
}

type fanoutCandidateRow struct {
	AccountID  int    `db:"account_id"`
	Email      string `db:"email"`
	LanguageID int    `db:"language_id"`
}

// AdvanceEmailCampaignFanout materializes one keyset page and advances its
// cursor in the same short transaction. A crash can therefore replay a page,
// but UNIQUE(campaign_id,email) plus the deliberate no-op upsert makes replay
// lossless without hiding unrelated constraint failures.
func (s *Store) AdvanceEmailCampaignFanout(
	ctx context.Context,
	pageSize int,
	assign entity.EmailCampaignVariantAssigner,
) (*entity.EmailCampaignFanoutPageResult, error) {
	if pageSize <= 0 {
		return nil, errors.New("fanout page size must be positive")
	}
	if assign == nil {
		return nil, errors.New("campaign variant assigner is required")
	}

	var out *entity.EmailCampaignFanoutPageResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		campaign, err := storeutil.QueryNamedOne[fanoutCampaignRow](ctx, rep.DB(), `
			SELECT id, topic, audience_predicate, fanout_max_account_id,
			       fanout_cursor_account_id, ab_enabled, ab_test_pct
			FROM email_campaign
			WHERE status = 'sending' AND audience_snapshot_at IS NOT NULL
			  AND audience_materialized_at IS NULL
			ORDER BY id
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, map[string]any{})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim campaign fanout page: %w", err)
		}

		var predicate entity.SegmentPredicate
		if err := json.Unmarshal(campaign.AudiencePredicate, &predicate); err != nil {
			return fmt.Errorf("unmarshal frozen audience for campaign %d: %w", campaign.ID, err)
		}
		compiled, err := segment.Compile(predicate)
		if err != nil {
			return fmt.Errorf("compile frozen audience for campaign %d: %w", campaign.ID, err)
		}
		topic := entity.EmailCampaignTopic(campaign.Topic)
		where, params, err := segment.BuildAudiencePredicate(
			compiled,
			segment.ComplianceOpts{Topic: &topic},
		)
		if err != nil {
			return fmt.Errorf("build compliant audience for campaign %d: %w", campaign.ID, err)
		}
		params["cursor"] = campaign.FanoutCursorAccountID
		params["max_account_id"] = campaign.FanoutMaxAccountID
		params["limit"] = pageSize
		candidates, err := storeutil.QueryListNamed[fanoutCandidateRow](ctx, rep.DB(), `
			SELECT sa.id AS account_id,
			       sa.email,
			       COALESCE(l.id, (
			           SELECT dl.id FROM language dl
			           WHERE dl.is_default = 1 ORDER BY dl.id LIMIT 1
			       )) AS language_id
			FROM storefront_account sa
			LEFT JOIN marketing_account_aggregate maa ON maa.account_id = sa.id
			LEFT JOIN email_suppression es ON es.email = sa.email
			LEFT JOIN language l ON l.code = sa.default_language
			WHERE `+where+`
			  AND sa.id > :cursor AND sa.id <= :max_account_id
			ORDER BY sa.id
			LIMIT :limit`, params)
		if err != nil {
			return fmt.Errorf("select campaign %d audience page: %w", campaign.ID, err)
		}

		variantRows, err := storeutil.QueryListNamed[struct {
			ID int `db:"id"`
		}](ctx, rep.DB(), `
			SELECT id FROM email_campaign_variant
			WHERE campaign_id = :campaign_id ORDER BY id`,
			map[string]any{"campaign_id": campaign.ID})
		if err != nil {
			return fmt.Errorf("load campaign %d variants for fanout: %w", campaign.ID, err)
		}
		variantIDs := make([]int, len(variantRows))
		for i := range variantRows {
			variantIDs[i] = variantRows[i].ID
		}
		if len(variantIDs) == 0 {
			return fmt.Errorf("campaign %d has no variants", campaign.ID)
		}

		rows := make([][]any, 0, len(candidates))
		cursor := campaign.FanoutCursorAccountID
		for _, candidate := range candidates {
			email := normalizeAudienceEmail(candidate.Email)
			assignment := assign(
				campaign.ID,
				email,
				campaign.ABEnabled,
				campaign.ABTestPct,
				variantIDs,
			)
			rows = append(rows, []any{
				campaign.ID,
				candidate.AccountID,
				email,
				candidate.LanguageID,
				assignment.VariantID,
				string(assignment.Cohort),
			})
			cursor = candidate.AccountID
		}
		if err := insertRecipientRows(ctx, rep.DB(), rows); err != nil {
			return fmt.Errorf("insert campaign %d recipients: %w", campaign.ID, err)
		}

		materialized := len(candidates) < pageSize || cursor >= campaign.FanoutMaxAccountID
		if len(candidates) == 0 {
			cursor = campaign.FanoutMaxAccountID
		}
		if materialized {
			if _, err := rep.DB().NamedExecContext(ctx, `
				UPDATE email_campaign
				SET fanout_cursor_account_id = :cursor,
				    audience_materialized_at = UTC_TIMESTAMP(6),
				    recipient_count = (
				        SELECT COUNT(*) FROM email_campaign_recipient
				        WHERE campaign_id = :campaign_id
				    )
				WHERE id = :campaign_id AND status = 'sending'
				  AND audience_materialized_at IS NULL`,
				map[string]any{"campaign_id": campaign.ID, "cursor": cursor}); err != nil {
				return fmt.Errorf("complete campaign %d fanout: %w", campaign.ID, err)
			}
		} else {
			if _, err := rep.DB().NamedExecContext(ctx, `
				UPDATE email_campaign
				SET fanout_cursor_account_id = :cursor
				WHERE id = :campaign_id AND status = 'sending'
				  AND audience_materialized_at IS NULL`,
				map[string]any{"campaign_id": campaign.ID, "cursor": cursor}); err != nil {
				return fmt.Errorf("advance campaign %d fanout cursor: %w", campaign.ID, err)
			}
		}
		out = &entity.EmailCampaignFanoutPageResult{
			CampaignID:   campaign.ID,
			Inserted:     len(candidates),
			Cursor:       cursor,
			Materialized: materialized,
		}
		return nil
	})
	return out, err
}

func insertRecipientRows(ctx context.Context, db dependency.DB, rows [][]any) error {
	const columns = 6
	const chunkSize = 250
	for start := 0; start < len(rows); start += chunkSize {
		end := start + chunkSize
		if end > len(rows) {
			end = len(rows)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*columns)
		for _, row := range rows[start:end] {
			placeholders = append(placeholders, "(?,?,?,?,?,?)")
			args = append(args, row...)
		}
		query := `
			INSERT INTO email_campaign_recipient
			    (campaign_id, account_id, email, language_id, variant_id, cohort)
			VALUES ` + strings.Join(placeholders, ",") + `
			ON DUPLICATE KEY UPDATE id = id`
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}
