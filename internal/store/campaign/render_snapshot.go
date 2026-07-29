package campaign

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type renderSnapshotRow struct {
	CampaignID     int            `db:"campaign_id"`
	VariantID      int            `db:"variant_id"`
	LanguageID     int            `db:"language_id"`
	Subject        string         `db:"subject"`
	HTMLTemplate   string         `db:"html_template"`
	TextTemplate   string         `db:"text_template"`
	FromValue      string         `db:"from_value"`
	ReplyTo        sql.NullString `db:"reply_to"`
	PayloadVersion int            `db:"payload_version"`
	CreatedAt      sql.NullTime   `db:"created_at"`
}

func renderSnapshotRowToEntity(row renderSnapshotRow) entity.EmailCampaignRenderSnapshot {
	out := entity.EmailCampaignRenderSnapshot{
		CampaignID:     row.CampaignID,
		VariantID:      row.VariantID,
		LanguageID:     row.LanguageID,
		Subject:        row.Subject,
		HTMLTemplate:   row.HTMLTemplate,
		TextTemplate:   row.TextTemplate,
		FromValue:      row.FromValue,
		PayloadVersion: row.PayloadVersion,
		CreatedAt:      row.CreatedAt.Time,
	}
	if row.ReplyTo.Valid {
		value := row.ReplyTo.String
		out.ReplyTo = &value
	}
	return out
}

func (s *Store) PutEmailCampaignRenderSnapshot(
	ctx context.Context,
	snapshot entity.EmailCampaignRenderSnapshot,
) error {
	if snapshot.CampaignID <= 0 || snapshot.VariantID <= 0 || snapshot.LanguageID <= 0 {
		return errors.New("campaign render snapshot keys must be positive")
	}
	result, err := s.DB.NamedExecContext(ctx, `
		INSERT INTO email_campaign_render_snapshot
		    (campaign_id, variant_id, language_id, subject, html_template,
		     text_template, from_value, reply_to, payload_version)
		VALUES
		    (:campaign_id, :variant_id, :language_id, :subject, :html_template,
		     :text_template, :from_value, :reply_to, :payload_version)
		ON DUPLICATE KEY UPDATE campaign_id = campaign_id`,
		map[string]any{
			"campaign_id":     snapshot.CampaignID,
			"variant_id":      snapshot.VariantID,
			"language_id":     snapshot.LanguageID,
			"subject":         snapshot.Subject,
			"html_template":   snapshot.HTMLTemplate,
			"text_template":   snapshot.TextTemplate,
			"from_value":      snapshot.FromValue,
			"reply_to":        snapshot.ReplyTo,
			"payload_version": snapshot.PayloadVersion,
		})
	if err != nil {
		return fmt.Errorf("insert campaign render snapshot: %w", err)
	}
	if _, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("insert campaign render snapshot rows: %w", err)
	}
	stored, err := s.GetEmailCampaignRenderSnapshot(
		ctx,
		snapshot.CampaignID,
		snapshot.VariantID,
		snapshot.LanguageID,
	)
	if err != nil {
		return err
	}
	if stored.Subject != snapshot.Subject ||
		stored.HTMLTemplate != snapshot.HTMLTemplate ||
		stored.TextTemplate != snapshot.TextTemplate ||
		stored.FromValue != snapshot.FromValue ||
		!equalOptionalString(stored.ReplyTo, snapshot.ReplyTo) ||
		stored.PayloadVersion != snapshot.PayloadVersion {
		return fmt.Errorf("campaign render snapshot (%d,%d,%d) immutable content mismatch",
			snapshot.CampaignID, snapshot.VariantID, snapshot.LanguageID)
	}
	return nil
}

func equalOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *Store) GetEmailCampaignRenderSnapshot(
	ctx context.Context,
	campaignID, variantID, languageID int,
) (*entity.EmailCampaignRenderSnapshot, error) {
	row, err := storeutil.QueryNamedOne[renderSnapshotRow](ctx, s.DB, `
		SELECT campaign_id, variant_id, language_id, subject, html_template,
		       text_template, from_value, reply_to, payload_version, created_at
		FROM email_campaign_render_snapshot
		WHERE campaign_id = :campaign_id AND variant_id = :variant_id
		  AND language_id = :language_id`,
		map[string]any{
			"campaign_id": campaignID,
			"variant_id":  variantID,
			"language_id": languageID,
		})
	if err != nil {
		return nil, fmt.Errorf("get campaign render snapshot: %w", err)
	}
	out := renderSnapshotRowToEntity(row)
	return &out, nil
}
