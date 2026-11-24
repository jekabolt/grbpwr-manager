package mail

import (
	"context"
	"sync"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A team shop's transactional letter carries the shop's name and reply-to over the platform sender
// address; a context with no team keeps the configured sender.
func TestTeamShopLetterSender(t *testing.T) {
	m := createTestMailer(t)
	m.SetSender(func(ctx context.Context) (string, string) {
		if tm, ok := tenant.From(ctx); ok && tm.ID == 7 {
			return `Acme "Shop" <x>`, "help@acme.test"
		}
		return "", ""
	})
	data := struct {
		Preheader, EmailB64, OTPCode, MagicLinkURL string
	}{"p", " ", "123456", "https://acme.test/login?token=t"}

	shop, err := m.buildFor(tenant.With(context.Background(), tenant.Team{ID: 7}), "c@example.com", AccountLogin, data)
	require.NoError(t, err)
	require.Equal(t, `"Acme \"Shop\" <x>" <test@example.com>`, shop.From)
	require.Equal(t, "help@acme.test", *shop.ReplyTo)

	plain, err := m.buildFor(context.Background(), "c@example.com", AccountLogin, data)
	require.NoError(t, err)
	require.Equal(t, "Test Mailer <test@example.com>", plain.From)
	require.Equal(t, "reply@example.com", *plain.ReplyTo)
}

// With a team loop one worker tick drains every team's outbox, each in that team's context (each
// team's letters live in its own database).
func TestMailDrainsEveryTeam(t *testing.T) {
	repo := mocks.NewMockMail(t)
	var mu sync.Mutex
	seen := map[int64]bool{}
	repo.EXPECT().GetAllUnsent(mock.Anything, false, mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, _ bool, _ int, _ time.Time) ([]entity.SendEmailRequest, error) {
			tm, ok := tenant.From(ctx)
			require.True(t, ok, "drain without a team")
			mu.Lock()
			seen[tm.ID] = true
			mu.Unlock()
			return nil, nil
		})
	m, err := new(&Config{APIKey: "k", FromEmail: "f@example.com", FromName: "n"}, repo, nil)
	require.NoError(t, err)
	m.SetTeamLoop(func(ctx context.Context, tick func(context.Context) bool) bool {
		ok := true
		for _, id := range []int64{1, 7} {
			ok = tick(tenant.With(ctx, tenant.Team{ID: id})) && ok
		}
		return ok
	})
	require.True(t, m.runOnce(context.Background()))
	require.Equal(t, map[int64]bool{1: true, 7: true}, seen)
	require.False(t, m.LastSuccess().IsZero())
}
