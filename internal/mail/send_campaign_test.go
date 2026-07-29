package mail

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/stretchr/testify/mock"
)

func TestSendCampaignTestUsesSendPrimitiveWithoutPersistence(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)
	mailer.c.Disabled = true
	sender := mocks.NewMockSender(t)
	mailer.cli = sender

	sender.On("PostEmails", ctx, mock.MatchedBy(func(req resend.SendEmailRequest) bool {
		return req.Subject == "[TEST] Subject" &&
			len(req.To) == 1 &&
			req.To[0] == "operator@example.com" &&
			req.Html != nil && *req.Html == "<p>HTML</p>" &&
			req.Text != nil && *req.Text == "Text"
	})).Return(nil, nil).Once()

	if err := mailer.SendCampaignTest(
		ctx, nil, " Operator <operator@example.com> ", "Subject", "<p>HTML</p>", "Text",
	); err != nil {
		t.Fatalf("SendCampaignTest() error: %v", err)
	}
}
