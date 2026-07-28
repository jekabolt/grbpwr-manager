// Package campaign persists email campaign and audience-segment definitions.
package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc executes f within a repository transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Campaigns.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

type campaignRow struct {
	ID                     int            `db:"id"`
	Name                   string         `db:"name"`
	Status                 string         `db:"status"`
	SegmentID              sql.NullInt64  `db:"segment_id"`
	Topic                  string         `db:"topic"`
	Body                   []byte         `db:"body"`
	BackgroundColor        string         `db:"background_color"`
	FromName               sql.NullString `db:"from_name"`
	FromEmail              sql.NullString `db:"from_email"`
	ReplyTo                sql.NullString `db:"reply_to"`
	ScheduleAt             sql.NullTime   `db:"schedule_at"`
	ABEnabled              bool           `db:"ab_enabled"`
	ABDimension            sql.NullString `db:"ab_dimension"`
	ABTestPct              int            `db:"ab_test_pct"`
	ABDecisionAfterMinutes int            `db:"ab_decision_after_minutes"`
	ABWinnerVariantID      sql.NullInt64  `db:"ab_winner_variant_id"`
	SendingStartedAt       sql.NullTime   `db:"sending_started_at"`
	SentAt                 sql.NullTime   `db:"sent_at"`
	CreatedBy              string         `db:"created_by"`
	CreatedAt              sql.NullTime   `db:"created_at"`
	UpdatedAt              sql.NullTime   `db:"updated_at"`
}

type campaignVariantRow struct {
	ID          int          `db:"id"`
	CampaignID  int          `db:"campaign_id"`
	Label       string       `db:"label"`
	SubjectI18n []byte       `db:"subject_i18n"`
	Body        []byte       `db:"body"`
	IsWinner    bool         `db:"is_winner"`
	CreatedAt   sql.NullTime `db:"created_at"`
	UpdatedAt   sql.NullTime `db:"updated_at"`
}

const campaignColumns = `
	id, name, status, segment_id, topic, body, background_color,
	from_name, from_email, reply_to, schedule_at,
	ab_enabled, ab_dimension, ab_test_pct, ab_decision_after_minutes,
	ab_winner_variant_id, sending_started_at, sent_at,
	created_by, created_at, updated_at`

func marshalEmailBlocks(blocks []entity.EmailBlock) ([]byte, error) {
	if blocks == nil {
		blocks = []entity.EmailBlock{}
	}
	return json.Marshal(blocks)
}

func marshalSubjectTranslations(translations []entity.SubjectTranslation) ([]byte, error) {
	if translations == nil {
		translations = []entity.SubjectTranslation{}
	}
	return json.Marshal(translations)
}

func nullableCampaignString(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func campaignParams(campaign *entity.EmailCampaignInsert, body []byte) map[string]any {
	var abDimension any
	if campaign.ABConfig.Enabled && campaign.ABConfig.Dimension != entity.ABDimensionUnknown {
		abDimension = string(campaign.ABConfig.Dimension)
	}
	var segmentID any
	if campaign.SegmentID != nil {
		segmentID = *campaign.SegmentID
	}
	var winnerVariantID any
	if campaign.ABConfig.WinnerVariantID != nil {
		winnerVariantID = *campaign.ABConfig.WinnerVariantID
	}
	return map[string]any{
		"name":                      campaign.Name,
		"status":                    string(campaign.Status),
		"segment_id":                segmentID,
		"topic":                     string(campaign.Topic),
		"body":                      body,
		"background_color":          campaign.BackgroundColor,
		"from_name":                 nullableCampaignString(campaign.FromName),
		"from_email":                nullableCampaignString(campaign.FromEmail),
		"reply_to":                  nullableCampaignString(campaign.ReplyTo),
		"schedule_at":               campaign.ScheduleAt,
		"ab_enabled":                campaign.ABConfig.Enabled,
		"ab_dimension":              abDimension,
		"ab_test_pct":               campaign.ABConfig.TestPct,
		"ab_decision_after_minutes": campaign.ABConfig.DecisionAfterMinutes,
		"ab_winner_variant_id":      winnerVariantID,
		"created_by":                campaign.CreatedBy,
	}
}

// UpsertEmailCampaign inserts or updates a campaign and synchronizes its
// variants in one transaction. id=0 creates a new campaign.
func (s *Store) UpsertEmailCampaign(ctx context.Context, id int, campaign *entity.EmailCampaignInsert) (int, error) {
	if campaign == nil {
		return 0, errors.New("campaign is required")
	}
	body, err := marshalEmailBlocks(campaign.Body)
	if err != nil {
		return 0, fmt.Errorf("marshal campaign body: %w", err)
	}

	params := campaignParams(campaign, body)
	campaignID := id
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if campaignID == 0 {
			campaignID, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
				INSERT INTO email_campaign (
					name, status, segment_id, topic, body, background_color,
					from_name, from_email, reply_to, schedule_at,
					ab_enabled, ab_dimension, ab_test_pct, ab_decision_after_minutes,
					ab_winner_variant_id, created_by
				) VALUES (
					:name, :status, :segment_id, :topic, :body, :background_color,
					:from_name, :from_email, :reply_to, :schedule_at,
					:ab_enabled, :ab_dimension, :ab_test_pct, :ab_decision_after_minutes,
					:ab_winner_variant_id, :created_by
				)`, params)
			if err != nil {
				return fmt.Errorf("insert email campaign: %w", err)
			}
		} else {
			count, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM email_campaign WHERE id = :id`,
				map[string]any{"id": campaignID})
			if err != nil {
				return fmt.Errorf("check email campaign exists: %w", err)
			}
			if count == 0 {
				return fmt.Errorf("email campaign %d not found: %w", campaignID, sql.ErrNoRows)
			}
			params["id"] = campaignID
			if _, err := rep.DB().NamedExecContext(ctx, `
				UPDATE email_campaign SET
					name = :name,
					status = :status,
					segment_id = :segment_id,
					topic = :topic,
					body = :body,
					background_color = :background_color,
					from_name = :from_name,
					from_email = :from_email,
					reply_to = :reply_to,
					schedule_at = :schedule_at,
					ab_enabled = :ab_enabled,
					ab_dimension = :ab_dimension,
					ab_test_pct = :ab_test_pct,
					ab_decision_after_minutes = :ab_decision_after_minutes,
					ab_winner_variant_id = :ab_winner_variant_id
				WHERE id = :id`, params); err != nil {
				return fmt.Errorf("update email campaign %d: %w", campaignID, err)
			}
		}

		if err := syncCampaignVariants(ctx, rep.DB(), campaignID, campaign.Variants); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("upsert email campaign transaction: %w", err)
	}
	return campaignID, nil
}

func syncCampaignVariants(ctx context.Context, db dependency.DB, campaignID int, variants []entity.EmailCampaignVariant) error {
	var existingIDs []int
	if err := db.SelectContext(ctx, &existingIDs,
		`SELECT id FROM email_campaign_variant WHERE campaign_id = ?`, campaignID); err != nil {
		return fmt.Errorf("list existing campaign variants: %w", err)
	}
	existing := make(map[int]struct{}, len(existingIDs))
	for _, id := range existingIDs {
		existing[id] = struct{}{}
	}
	kept := make(map[int]struct{}, len(variants))

	for i := range variants {
		variant := &variants[i]
		subjectJSON, err := marshalSubjectTranslations(variant.SubjectI18n)
		if err != nil {
			return fmt.Errorf("marshal variant %d subjects: %w", i, err)
		}
		var bodyJSON any
		if len(variant.Body) > 0 {
			encoded, err := marshalEmailBlocks(variant.Body)
			if err != nil {
				return fmt.Errorf("marshal variant %d body: %w", i, err)
			}
			bodyJSON = encoded
		}
		params := map[string]any{
			"campaign_id": campaignID,
			"label":       variant.Label,
			"subject":     subjectJSON,
			"body":        bodyJSON,
			"is_winner":   variant.IsWinner,
		}

		if variant.ID == 0 {
			id, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO email_campaign_variant
					(campaign_id, label, subject_i18n, body, is_winner)
				VALUES
					(:campaign_id, :label, :subject, :body, :is_winner)`, params)
			if err != nil {
				return fmt.Errorf("insert campaign variant %d: %w", i, err)
			}
			kept[id] = struct{}{}
			continue
		}

		if _, ok := existing[variant.ID]; !ok {
			return fmt.Errorf("email campaign variant %d not found on campaign %d: %w",
				variant.ID, campaignID, sql.ErrNoRows)
		}
		params["id"] = variant.ID
		if _, err := db.NamedExecContext(ctx, `
			UPDATE email_campaign_variant SET
				label = :label,
				subject_i18n = :subject,
				body = :body,
				is_winner = :is_winner
			WHERE id = :id AND campaign_id = :campaign_id`, params); err != nil {
			return fmt.Errorf("update campaign variant %d: %w", variant.ID, err)
		}
		kept[variant.ID] = struct{}{}
	}

	for _, existingID := range existingIDs {
		if _, ok := kept[existingID]; ok {
			continue
		}
		if _, err := db.NamedExecContext(ctx,
			`DELETE FROM email_campaign_variant WHERE id = :id AND campaign_id = :campaign_id`,
			map[string]any{"id": existingID, "campaign_id": campaignID}); err != nil {
			return fmt.Errorf("delete campaign variant %d: %w", existingID, err)
		}
	}
	return nil
}

func campaignRowToEntity(row campaignRow) (*entity.EmailCampaignFull, error) {
	var body []entity.EmailBlock
	if err := json.Unmarshal(row.Body, &body); err != nil {
		return nil, fmt.Errorf("unmarshal campaign %d body: %w", row.ID, err)
	}
	out := &entity.EmailCampaignFull{
		ID: row.ID,
		EmailCampaignInsert: entity.EmailCampaignInsert{
			Name:            row.Name,
			Topic:           entity.EmailCampaignTopic(row.Topic),
			Body:            body,
			BackgroundColor: row.BackgroundColor,
			ABConfig: entity.ABConfig{
				Enabled:              row.ABEnabled,
				Dimension:            entity.ABDimension(row.ABDimension.String),
				TestPct:              row.ABTestPct,
				DecisionAfterMinutes: row.ABDecisionAfterMinutes,
			},
			Status:    entity.EmailCampaignStatus(row.Status),
			CreatedBy: row.CreatedBy,
		},
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
	if row.SegmentID.Valid {
		value := int(row.SegmentID.Int64)
		out.SegmentID = &value
	}
	if row.FromName.Valid {
		value := row.FromName.String
		out.FromName = &value
	}
	if row.FromEmail.Valid {
		value := row.FromEmail.String
		out.FromEmail = &value
	}
	if row.ReplyTo.Valid {
		value := row.ReplyTo.String
		out.ReplyTo = &value
	}
	if row.ScheduleAt.Valid {
		value := row.ScheduleAt.Time
		out.ScheduleAt = &value
	}
	if row.ABWinnerVariantID.Valid {
		value := int(row.ABWinnerVariantID.Int64)
		out.ABConfig.WinnerVariantID = &value
	}
	if row.SendingStartedAt.Valid {
		value := row.SendingStartedAt.Time
		out.SendingStartedAt = &value
	}
	if row.SentAt.Valid {
		value := row.SentAt.Time
		out.SentAt = &value
	}
	return out, nil
}

func (s *Store) getCampaignVariants(ctx context.Context, campaignID int) ([]entity.EmailCampaignVariant, error) {
	rows, err := storeutil.QueryListNamed[campaignVariantRow](ctx, s.DB, `
		SELECT id, campaign_id, label, subject_i18n, body, is_winner, created_at, updated_at
		FROM email_campaign_variant
		WHERE campaign_id = :campaign_id
		ORDER BY id`, map[string]any{"campaign_id": campaignID})
	if err != nil {
		return nil, fmt.Errorf("list campaign variants: %w", err)
	}
	out := make([]entity.EmailCampaignVariant, 0, len(rows))
	for _, row := range rows {
		var subjects []entity.SubjectTranslation
		if err := json.Unmarshal(row.SubjectI18n, &subjects); err != nil {
			return nil, fmt.Errorf("unmarshal variant %d subjects: %w", row.ID, err)
		}
		var body []entity.EmailBlock
		if len(row.Body) > 0 {
			if err := json.Unmarshal(row.Body, &body); err != nil {
				return nil, fmt.Errorf("unmarshal variant %d body: %w", row.ID, err)
			}
		}
		out = append(out, entity.EmailCampaignVariant{
			ID:          row.ID,
			Label:       row.Label,
			SubjectI18n: subjects,
			Body:        body,
			IsWinner:    row.IsWinner,
			CreatedAt:   row.CreatedAt.Time,
			UpdatedAt:   row.UpdatedAt.Time,
		})
	}
	return out, nil
}

func (s *Store) GetEmailCampaignByID(ctx context.Context, id int) (*entity.EmailCampaignFull, error) {
	row, err := storeutil.QueryNamedOne[campaignRow](ctx, s.DB,
		`SELECT `+campaignColumns+` FROM email_campaign WHERE id = :id`,
		map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("get email campaign %d: %w", id, err)
	}
	out, err := campaignRowToEntity(row)
	if err != nil {
		return nil, err
	}
	out.Variants, err = s.getCampaignVariants(ctx, id)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListEmailCampaignsPaged(
	ctx context.Context,
	limit, offset int,
	status entity.EmailCampaignStatus,
	topic entity.EmailCampaignTopic,
) ([]entity.EmailCampaignFull, int, error) {
	if limit <= 0 {
		return nil, 0, errors.New("limit must be greater than 0")
	}
	if offset < 0 {
		return nil, 0, errors.New("offset must be non-negative")
	}
	params := map[string]any{
		"status": string(status),
		"topic":  string(topic),
		"limit":  limit,
		"offset": offset,
	}
	where := ` WHERE (:status = '' OR status = :status) AND (:topic = '' OR topic = :topic)`
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM email_campaign`+where, params)
	if err != nil {
		return nil, 0, fmt.Errorf("count email campaigns: %w", err)
	}
	rows, err := storeutil.QueryListNamed[campaignRow](ctx, s.DB,
		`SELECT `+campaignColumns+` FROM email_campaign`+where+`
		 ORDER BY updated_at DESC, id DESC LIMIT :limit OFFSET :offset`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list email campaigns: %w", err)
	}
	out := make([]entity.EmailCampaignFull, 0, len(rows))
	for _, row := range rows {
		campaign, err := campaignRowToEntity(row)
		if err != nil {
			return nil, 0, err
		}
		campaign.Variants, err = s.getCampaignVariants(ctx, campaign.ID)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *campaign)
	}
	return out, total, nil
}

func (s *Store) DeleteEmailCampaign(ctx context.Context, id int) error {
	result, err := s.DB.NamedExecContext(ctx,
		`DELETE FROM email_campaign WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("delete email campaign %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("email campaign %d rows affected: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("email campaign %d not found: %w", id, sql.ErrNoRows)
	}
	return nil
}
