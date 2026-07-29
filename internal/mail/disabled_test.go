package mail

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// TestSuppressed locks the MAILER_DISABLED behaviour: bulk emails are dropped,
// but the account sign-in email (OTP/magic link) always dispatches so beta login
// keeps working.
func TestSuppressed(t *testing.T) {
	disabled := &Mailer{c: &Config{Disabled: true}}
	enabled := &Mailer{c: &Config{Disabled: false}}

	loginSubj := templateSubjects[AccountLogin]
	if loginSubj == "" {
		t.Fatal("AccountLogin subject missing")
	}

	if disabled.suppressed(loginSubj) {
		t.Errorf("account sign-in (%q) must NOT be suppressed when disabled — login would break", loginSubj)
	}
	if !disabled.suppressed("[TEST] Campaign subject") {
		t.Error("a normal send with a cosmetic [TEST] prefix must be suppressed when disabled")
	}
	for _, tn := range []templateName{OrderConfirmed, NewSubscriber, OrderShipped, PromoCode, HackerInvite, TierUpgrade} {
		if !disabled.suppressed(templateSubjects[tn]) {
			t.Errorf("bulk email %q (%q) must be suppressed when disabled", tn, templateSubjects[tn])
		}
	}
	// When enabled, nothing is suppressed.
	if enabled.suppressed(loginSubj) || enabled.suppressed(templateSubjects[OrderConfirmed]) {
		t.Error("nothing must be suppressed when mailer is enabled")
	}
}

func TestSendWithTestSubjectDoesNotBypassDisabledSuppression(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)
	mailer.c.Disabled = true
	mailer.cli = mocks.NewMockSender(t)

	err := mailer.send(ctx, &resend.SendEmailRequest{Subject: "[TEST] attacker-controlled"})
	if err != nil {
		t.Fatalf("send() error: %v", err)
	}
}
