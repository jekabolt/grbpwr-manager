package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformmail"
)

// startTeamsMail syncs the platform operators from PLATFORM_OPERATOR_EMAILS and starts the platform
// mail worker (verify / invite / reset letters from platform_email). Missing settings never fail
// boot: a disabled mailer marks letters skipped; an enabled one without its key or from-address
// leaves them pending and logs an error every tick until fixed.
func (a *App) startTeamsMail(ctx context.Context) (stop func(), err error) {
	if a.platform == nil {
		return func() {}, nil
	}
	emails, invalid := platformauth.ParseOperatorEmails(a.c.Platform.OperatorEmails)
	if len(invalid) > 0 {
		slog.Default().WarnContext(ctx, "teams: PLATFORM_OPERATOR_EMAILS has entries that are not emails; skipped", slog.Int("count", len(invalid)))
	}
	// Same parameters as the admin hasher: a seeded operator's hash costs what any account's costs.
	ph, err := pwhash.New(a.c.Auth.PasswordHasherSaltSize, a.c.Auth.PasswordHasherIterations)
	if err != nil {
		return nil, err
	}
	created, err := a.platform.SyncOperators(ctx, emails, time.Now().UTC().Truncate(time.Millisecond),
		platformauth.UnusableHash(ph), platformauth.HandleBase)
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
	stopMail := w.Start(context.WithoutCancel(ctx))
	stopSweep := startJoiningSweep(context.WithoutCancel(ctx), a.platform)
	return func() { stopSweep(); stopMail() }, nil
}

// joiningSweepEvery is how often reserved memberships whose invite died are removed.
const joiningSweepEvery = time.Hour

// startJoiningSweep removes, now and then hourly, invite reservations that can never finish (their
// invite expired or was revoked); the staged admins row they left stays disabled.
func startJoiningSweep(ctx context.Context, st interface {
	SweepJoining(context.Context, time.Time) (int64, error)
}) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	sweep := func() {
		n, err := st.SweepJoining(ctx, time.Now().UTC())
		switch {
		case err != nil && ctx.Err() == nil:
			slog.Default().ErrorContext(ctx, "teams: sweep stale invite reservations", slog.String("err", err.Error()))
		case n > 0:
			slog.Default().InfoContext(ctx, "teams: stale invite reservations removed", slog.Int64("count", n))
		}
	}
	go func() {
		defer close(done)
		sweep()
		t := time.NewTicker(joiningSweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
	return func() { cancel(); <-done }
}
