package mail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"log/slog"

	gerr "github.com/jekabolt/grbpwr-manager/internal/errors"
)

// Start starts the worker
func (m *Mailer) Start(ctx context.Context) error {
	if m.ctx != nil && m.cancel != nil {
		return fmt.Errorf("Mailer already started")
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	go m.worker(m.ctx)
	return nil
}

// Stop stops the worker gracefully
func (m *Mailer) Stop() error {
	if m.cancel == nil {
		return fmt.Errorf("Mailer already stopped or not started")
	}

	m.cancel() // This will cancel the context used by the worker
	m.cancel = nil
	return nil
}

func (m *Mailer) worker(ctx context.Context) {
	ticker := time.NewTicker(m.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.handleUnsent(ctx); err != nil {
				slog.Default().ErrorContext(ctx, "can't handle unsent mails",
					slog.String("err", err.Error()),
				)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *Mailer) handleUnsent(ctx context.Context) error {
	unsentEmails, err := m.mailRepository.GetAllUnsent(ctx, false)
	if err != nil {
		return fmt.Errorf("can't get unsent mails: %w", err)
	}

	for _, email := range unsentEmails {
		// Check for a stop signal before processing each email
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := m.sendRaw(ctx, &email); err != nil {
			slog.Default().ErrorContext(ctx, "can't send mail",
				slog.String("err", err.Error()),
				slog.Any("mail", email),
			)

			if errors.Is(err, gerr.MailApiLimitReached) {
				return nil // Stop sending mails if API limit is reached
			}

			if err := m.mailRepository.AddError(ctx, email.Id, err.Error()); err != nil {
				return fmt.Errorf("can't log error for email %v: %w", email.Id, err)
			}

		} else {
			// Update the database to mark the email as sent
			if err := m.mailRepository.UpdateSent(ctx, email.Id); err != nil {
				return fmt.Errorf("can't update sent status for email %v: %w", email.Id, err)
			}
		}
	}

	return nil
}
