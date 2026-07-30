package mail

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	"github.com/jekabolt/grbpwr-manager/internal/storefront/tokenhash"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

func campaignUnsubscribeTokenValue(topic entity.EmailCampaignTopic, normalizedEmail string) string {
	return "campaign-unsub:" + string(topic) + ":" + normalizedEmail
}

func (m *Mailer) CampaignDispatchConfigured() error {
	if strings.TrimSpace(m.c.UnsubscribeBaseURL) == "" {
		return errors.New("campaign unsubscribe base URL is not configured")
	}
	if strings.TrimSpace(m.c.UnsubscribePepper) == "" {
		return errors.New("campaign unsubscribe pepper is not configured")
	}
	return nil
}

func (m *Mailer) CampaignSendingDisabled() bool { return m.c.Disabled }

func (m *Mailer) CampaignEnvelope(
	campaign *entity.EmailCampaignFull,
) (string, *string, error) {
	fromName := m.c.FromName
	fromEmail := m.c.FromEmail
	replyTo := m.c.ReplyTo
	if campaign != nil {
		if campaign.FromName != nil && strings.TrimSpace(*campaign.FromName) != "" {
			fromName = strings.TrimSpace(*campaign.FromName)
		}
		if campaign.FromEmail != nil && strings.TrimSpace(*campaign.FromEmail) != "" {
			fromEmail = strings.TrimSpace(*campaign.FromEmail)
		}
		if campaign.ReplyTo != nil && strings.TrimSpace(*campaign.ReplyTo) != "" {
			replyTo = strings.TrimSpace(*campaign.ReplyTo)
		}
	}
	normalizedFrom, err := validateEmailAddress(fromEmail)
	if err != nil {
		return "", nil, fmt.Errorf("invalid campaign from address: %w", err)
	}
	fromValue := fmt.Sprintf("%s <%s>", fromName, normalizedFrom)
	var reply *string
	if replyTo != "" {
		normalizedReply, err := validateEmailAddress(replyTo)
		if err != nil {
			return "", nil, fmt.Errorf("invalid campaign reply-to address: %w", err)
		}
		reply = &normalizedReply
	}
	return fromValue, reply, nil
}

func (m *Mailer) CampaignUnsubscribeURL(
	topic entity.EmailCampaignTopic,
	email string,
) (string, error) {
	if err := m.CampaignDispatchConfigured(); err != nil {
		return "", err
	}
	if _, err := segment.TopicSubscriptionColumn(topic); err != nil {
		return "", err
	}
	normalized, err := validateEmailAddress(email)
	if err != nil {
		return "", err
	}
	normalized = strings.ToLower(normalized)
	emailB64 := base64.RawURLEncoding.EncodeToString([]byte(normalized))
	token := tokenhash.Hash(
		m.c.UnsubscribePepper,
		campaignUnsubscribeTokenValue(topic, normalized),
	)
	return fmt.Sprintf(
		"%s/api/webhooks/list-unsubscribe/%s/%s/%s",
		strings.TrimRight(m.c.UnsubscribeBaseURL, "/"),
		topic,
		emailB64,
		token,
	), nil
}

func (m *Mailer) BuildCampaignSendRequest(
	to string,
	snapshot entity.EmailCampaignRenderSnapshot,
	htmlBody, textBody, unsubscribeURL string,
) (resend.SendEmailRequest, error) {
	normalized, err := validateEmailAddress(to)
	if err != nil {
		return resend.SendEmailRequest{}, fmt.Errorf("invalid campaign recipient: %w", err)
	}
	subject := strings.TrimSpace(snapshot.Subject)
	if subject == "" {
		return resend.SendEmailRequest{}, errors.New("campaign subject is empty")
	}
	if len(subject) > 998 {
		return resend.SendEmailRequest{}, errors.New("campaign subject exceeds 998 characters")
	}
	if strings.TrimSpace(snapshot.FromValue) == "" {
		return resend.SendEmailRequest{}, errors.New("campaign from value is empty")
	}
	headers := map[string]interface{}{
		"List-Unsubscribe":      fmt.Sprintf("<%s>", unsubscribeURL),
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
	return resend.SendEmailRequest{
		From:    snapshot.FromValue,
		To:      []string{normalized},
		Subject: subject,
		Html:    &htmlBody,
		Text:    &textBody,
		ReplyTo: snapshot.ReplyTo,
		Headers: &headers,
	}, nil
}

func unsubscribeCampaignTopic(
	ctx context.Context,
	repo dependency.Repository,
	topic entity.EmailCampaignTopic,
	email string,
) error {
	column, err := segment.TopicSubscriptionColumn(topic)
	if err != nil {
		return err
	}
	return repo.Tx(ctx, func(ctx context.Context, tx dependency.Repository) error {
		result, err := tx.DB().ExecContext(ctx,
			"UPDATE storefront_account SET "+column+" = 0 WHERE LOWER(email) = ?",
			strings.ToLower(email),
		)
		if err != nil {
			return fmt.Errorf("clear storefront campaign subscription: %w", err)
		}
		if _, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("clear storefront campaign subscription rows: %w", err)
		}
		if topic == entity.EmailCampaignTopicNewsletter {
			if _, err := tx.Subscribers().UpsertSubscription(ctx, email, false); err != nil {
				return fmt.Errorf("clear legacy newsletter subscription: %w", err)
			}
		}
		return stampCampaignUnsubscribed(ctx, tx, topic, email)
	})
}

// stampCampaignUnsubscribed records the opt-out on the campaign ledger so the
// per-campaign Unsubscribed metric is populated. The RFC 8058 route is scoped to a
// topic, not to a campaign, so the opt-out is attributed to the recipient's most
// recent send of that topic (last touch) instead of every past campaign, which
// would count one click many times. Driving the lookup from email_campaign keeps
// it on uq_ecr_campaign_email; the recipient table has no index on email alone.
func stampCampaignUnsubscribed(
	ctx context.Context,
	tx dependency.Repository,
	topic entity.EmailCampaignTopic,
	email string,
) error {
	var recipientID uint64
	err := tx.DB().GetContext(ctx, &recipientID, `
		SELECT ecr.id
		FROM email_campaign ec
		JOIN email_campaign_recipient ecr
		  ON ecr.campaign_id = ec.id AND ecr.email = ?
		WHERE ec.topic = ? AND ecr.status = 'sent'
		ORDER BY ecr.sent_at DESC, ecr.id DESC
		LIMIT 1`, strings.ToLower(email), string(topic))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find campaign recipient to mark unsubscribed: %w", err)
	}
	if _, err := tx.DB().ExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET unsubscribed_at = UTC_TIMESTAMP(6)
		WHERE id = ? AND unsubscribed_at IS NULL`, recipientID); err != nil {
		return fmt.Errorf("mark campaign recipient unsubscribed: %w", err)
	}
	return nil
}
