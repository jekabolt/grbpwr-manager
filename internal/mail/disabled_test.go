package mail

import "testing"

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
