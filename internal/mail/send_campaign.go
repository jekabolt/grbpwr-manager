package mail

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

const campaignTestSubjectPrefix = "[TEST] "

// SendCampaignTest sends an operator-requested campaign test immediately. It
// intentionally does not write the transactional mail queue or any campaign
// recipient/metrics records.
func (m *Mailer) SendCampaignTest(
	ctx context.Context,
	_ dependency.Repository,
	to, subject, htmlBody, textBody string,
) error {
	normalizedTo, err := validateEmailAddress(to)
	if err != nil {
		return fmt.Errorf("invalid campaign test recipient: %w", err)
	}

	subject = strings.TrimSpace(subject)
	if !strings.HasPrefix(subject, campaignTestSubjectPrefix) {
		subject = campaignTestSubjectPrefix + subject
	}

	replyTo := m.c.FromEmail
	if m.c.ReplyTo != "" {
		replyTo = m.c.ReplyTo
	}
	req := &resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", m.c.FromName, m.c.FromEmail),
		To:      []string{normalizedTo},
		Html:    &htmlBody,
		Text:    &textBody,
		Subject: subject,
		ReplyTo: &replyTo,
		Headers: m.listUnsubscribeHeaders(normalizedTo),
	}
	if err := m.sendWithOptions(ctx, req, sendOptions{
		exemptDisabledSuppression: true,
		campaignLane:              true,
	}); err != nil {
		return fmt.Errorf("send campaign test: %w", err)
	}
	return nil
}
