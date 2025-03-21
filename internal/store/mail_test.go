package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupMails(store *MYSQLStore) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ExecNamed(ctx, store.DB(), "DELETE FROM send_email_request", map[string]any{})
}

func TestMailStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize store with migrations
	cfg := *testCfg
	cfg.Automigrate = true
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()

	mailStore := store.Mail()

	// Clean up before each test
	t.Cleanup(func() {
		err := cleanupMails(store)
		assert.NoError(t, err)
	})

	t.Run("AddMail", func(t *testing.T) {
		// Clean up before test
		err := cleanupMails(store)
		require.NoError(t, err)

		tests := []struct {
			name    string
			req     *entity.SendEmailRequest
			wantErr bool
		}{
			{
				name: "valid email request",
				req: &entity.SendEmailRequest{
					From:    "test@example.com",
					To:      "recipient@example.com",
					Html:    "<p>Test email</p>",
					Subject: "Test Subject",
					ReplyTo: "reply@example.com",
					Sent:    false,
				},
				wantErr: false,
			},
			{
				name: "already sent email",
				req: &entity.SendEmailRequest{
					From:    "test@example.com",
					To:      "recipient@example.com",
					Html:    "<p>Test email</p>",
					Subject: "Test Subject",
					ReplyTo: "reply@example.com",
					Sent:    true,
				},
				wantErr: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				id, err := mailStore.AddMail(ctx, tt.req)
				if tt.wantErr {
					assert.Error(t, err)
					return
				}
				assert.NoError(t, err)
				assert.Greater(t, id, 0)
			})
		}
	})

	t.Run("GetAllUnsent", func(t *testing.T) {
		// Clean up before test
		err := cleanupMails(store)
		require.NoError(t, err)

		// Add test emails
		req1 := &entity.SendEmailRequest{
			From:    "test1@example.com",
			To:      "recipient1@example.com",
			Html:    "<p>Test email 1</p>",
			Subject: "Test Subject 1",
			ReplyTo: "reply1@example.com",
			Sent:    false,
		}
		req2 := &entity.SendEmailRequest{
			From:    "test2@example.com",
			To:      "recipient2@example.com",
			Html:    "<p>Test email 2</p>",
			Subject: "Test Subject 2",
			ReplyTo: "reply2@example.com",
			Sent:    false,
		}

		id1, err := mailStore.AddMail(ctx, req1)
		require.NoError(t, err)
		id2, err := mailStore.AddMail(ctx, req2)
		require.NoError(t, err)

		// Add error to one email
		err = mailStore.AddError(ctx, id1, "test error")
		require.NoError(t, err)

		// Test GetAllUnsent without errors
		emails, err := mailStore.GetAllUnsent(ctx, false)
		require.NoError(t, err)
		assert.Len(t, emails, 1) // Should only get the email without error
		assert.Equal(t, id2, emails[0].Id)

		// Test GetAllUnsent with errors
		emails, err = mailStore.GetAllUnsent(ctx, true)
		require.NoError(t, err)
		assert.Len(t, emails, 2) // Should get both emails
		// Sort emails by ID to make assertions deterministic
		if emails[0].Id > emails[1].Id {
			emails[0], emails[1] = emails[1], emails[0]
		}
		assert.Equal(t, id1, emails[0].Id)
		assert.Equal(t, id2, emails[1].Id)
	})

	t.Run("UpdateSent", func(t *testing.T) {
		// Clean up before test
		err := cleanupMails(store)
		require.NoError(t, err)

		// Add a test email
		req := &entity.SendEmailRequest{
			From:    "test@example.com",
			To:      "recipient@example.com",
			Html:    "<p>Test email</p>",
			Subject: "Test Subject",
			ReplyTo: "reply@example.com",
			Sent:    false,
		}

		id, err := mailStore.AddMail(ctx, req)
		require.NoError(t, err)

		// Update sent status
		err = mailStore.UpdateSent(ctx, id)
		require.NoError(t, err)

		// Verify email is not returned in unsent emails
		emails, err := mailStore.GetAllUnsent(ctx, true)
		require.NoError(t, err)
		for _, email := range emails {
			assert.NotEqual(t, id, email.Id)
		}
	})

	t.Run("AddError", func(t *testing.T) {
		// Clean up before test
		err := cleanupMails(store)
		require.NoError(t, err)

		// Add a test email
		req := &entity.SendEmailRequest{
			From:    "test@example.com",
			To:      "recipient@example.com",
			Html:    "<p>Test email</p>",
			Subject: "Test Subject",
			ReplyTo: "reply@example.com",
			Sent:    false,
		}

		id, err := mailStore.AddMail(ctx, req)
		require.NoError(t, err)

		// Add error
		errMsg := "test error message"
		err = mailStore.AddError(ctx, id, errMsg)
		require.NoError(t, err)

		// Verify email is not returned in unsent emails without errors
		emails, err := mailStore.GetAllUnsent(ctx, false)
		require.NoError(t, err)
		for _, email := range emails {
			assert.NotEqual(t, id, email.Id)
		}

		// Verify email is returned in unsent emails with errors
		emails, err = mailStore.GetAllUnsent(ctx, true)
		require.NoError(t, err)
		found := false
		for _, email := range emails {
			if email.Id == id {
				found = true
				assert.Equal(t, sql.NullString{String: errMsg, Valid: true}, email.ErrMsg)
				break
			}
		}
		assert.True(t, found)
	})
}
