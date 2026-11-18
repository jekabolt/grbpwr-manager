package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformmail"
)

// startTeamsMail syncs the platform operators from PLATFORM_OPERATOR_EMAILS and starts the platform
// mail worker (verify / invite / reset letters from platform_email). Missing optional settings never
// fail boot: the worker marks letters skipped and logs why.
func (a *App) startTeamsMail(ctx context.Context) (stop func(), err error) {
	if a.platform == nil {
		return func() {}, nil
	}
	emails, invalid := platformauth.ParseOperatorEmails(a.c.Platform.OperatorEmails)
	if len(invalid) > 0 {
		slog.Default().WarnContext(ctx, "teams: PLATFORM_OPERATOR_EMAILS has entries that are not emails; skipped", slog.Int("count", len(invalid)))
	}
	created, err := a.platform.SyncOperators(ctx, emails, time.Now().UTC().Truncate(time.Millisecond),
		platformauth.UnusableHash, platformauth.HandleBase)
	if err != nil {
		return nil, err
	}
	slog.Default().InfoContext(ctx, "teams: operators synced", slog.Int("listed", len(emails)), slog.Int("created", created))

	var sender platformmail.Sender
	if a.c.Platform.MailerEnabled {
		if sender, err = platformmail.NewResendSender(a.c.Mailer.APIKey); err != nil {
			slog.Default().ErrorContext(ctx, "teams: platform letters cannot be sent", slog.String("err", err.Error()))
			sender = nil
		}
	}
	w, err := platformmail.New(a.platform.DB(), sender, platformmail.Config{
		Enabled:    a.c.Platform.MailerEnabled,
		From:       a.c.Platform.MailFrom,
		BaseDomain: a.c.Platform.BaseDomain,
	})
	if err != nil {
		return nil, err
	}
	return w.Start(context.WithoutCancel(ctx)), nil
}
