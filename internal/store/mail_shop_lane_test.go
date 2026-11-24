package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/require"
)

// outboxRowState is the lease / quarantine bookkeeping of one send_email_request row.
type outboxRowState struct {
	Sent          bool           `db:"sent"`
	Attempts      int            `db:"send_attempt_count"`
	NextRetryAt   sql.NullTime   `db:"next_retry_at"`
	ClaimToken    sql.NullString `db:"claim_token"`
	QuarantinedAt sql.NullTime   `db:"quarantined_at"`
	ErrMsg        sql.NullString `db:"error_msg"`
	Kind          sql.NullString `db:"kind"`
	ExpiresAt     sql.NullTime   `db:"expires_at"`
	IdemKey       sql.NullString `db:"idempotency_key"`
	FirstDispatch sql.NullTime   `db:"first_dispatch_at"`
	Dispatches    int            `db:"dispatch_count"`
	ProviderID    sql.NullString `db:"provider_email_id"`
}

func outboxRow(t *testing.T, ms *MYSQLStore, id int) outboxRowState {
	t.Helper()
	r, err := QueryNamedOne[outboxRowState](context.Background(), ms.DB(), `
SELECT sent, send_attempt_count, next_retry_at, claim_token, quarantined_at, error_msg, kind, expires_at,
	idempotency_key, first_dispatch_at, dispatch_count, provider_email_id
FROM send_email_request WHERE id = :id`, map[string]any{"id": id})
	require.NoError(t, err)
	return r
}

type outboxSeed struct {
	to        string
	kind      string // "" = NULL
	expiresAt time.Time
	createdAt time.Time
}

func seedOutbox(t *testing.T, ms *MYSQLStore, s outboxSeed) int {
	t.Helper()
	ctx := context.Background()
	to := s.to
	if to == "" {
		to = "c@example.com"
	}
	row := &entity.SendEmailRequest{From: `"Acme via lekalo" <no-reply@lekalo.test>`, To: to, Subject: "s", Html: "<p>x</p>"}
	if s.kind != "" {
		row.Kind = sql.NullString{String: s.kind, Valid: true}
	}
	if !s.expiresAt.IsZero() {
		row.ExpiresAt = sql.NullTime{Time: s.expiresAt.UTC(), Valid: true}
	}
	id, err := ms.Mail().AddMail(ctx, row)
	require.NoError(t, err)
	if !s.createdAt.IsZero() {
		require.NoError(t, ExecNamed(ctx, ms.DB(), `UPDATE send_email_request SET created_at = :c WHERE id = :id`,
			map[string]any{"c": s.createdAt.UTC(), "id": id}))
	}
	return id
}

func claimIDs(rows []entity.SendEmailRequest) []int {
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Id)
	}
	return ids
}

func TestMailStoreShopLane(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	ms, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	mailStore := ms.Mail()

	reset := func(t *testing.T) {
		t.Helper()
		require.NoError(t, cleanupMails(ms))
		require.NoError(t, ExecNamed(ctx, ms.DB(), `DELETE FROM email_suppression WHERE email LIKE '%@shoplane.test'`, map[string]any{}))
	}
	// A defer, not t.Cleanup: the store goes away with ctx, which is cancelled before cleanups run.
	defer func() {
		reset(t)
		ms.Close()
	}()
	shopKinds := []string{entity.MailKindShopSignIn, entity.MailKindShopOrder}
	claim := func(token string, mutate ...func(*entity.MailClaim)) entity.MailClaim {
		q := entity.MailClaim{Limit: 50, MaxAttempts: 5, Lease: 2 * time.Minute, Token: token}
		for _, m := range mutate {
			m(&q)
		}
		return q
	}

	t.Run("AddMail keeps kind and expiry", func(t *testing.T) {
		reset(t)
		exp := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
		id := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopSignIn, expiresAt: exp})
		r := outboxRow(t, ms, id)
		require.Equal(t, entity.MailKindShopSignIn, r.Kind.String)
		require.True(t, r.ExpiresAt.Valid)
		require.True(t, r.ExpiresAt.Time.Equal(exp), "%s != %s", r.ExpiresAt.Time, exp)
		plain := seedOutbox(t, ms, outboxSeed{})
		r = outboxRow(t, ms, plain)
		require.False(t, r.Kind.Valid)
		require.False(t, r.ExpiresAt.Valid)
	})

	t.Run("claim leases rows and a second claimer gets none", func(t *testing.T) {
		reset(t)
		a := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		b := seedOutbox(t, ms, outboxSeed{})
		now := time.Now().UTC().Truncate(time.Second)
		got, err := mailStore.ClaimUnsent(ctx, claim("tok-a", func(q *entity.MailClaim) { q.Now = now }))
		require.NoError(t, err)
		require.Equal(t, []int{a, b}, claimIDs(got))
		require.Equal(t, entity.MailKindShopOrder, got[0].Kind.String, "claimed rows carry kind")
		require.False(t, got[0].CreatedAt.IsZero(), "claimed rows carry created_at (idempotency key)")
		r := outboxRow(t, ms, a)
		require.Equal(t, "tok-a", r.ClaimToken.String)
		require.True(t, r.NextRetryAt.Time.Equal(now.Add(2*time.Minute)), "lease end %s", r.NextRetryAt.Time)
		require.Zero(t, r.Attempts, "a claim is not an attempt")

		again, err := mailStore.ClaimUnsent(ctx, claim("tok-b"))
		require.NoError(t, err)
		require.Empty(t, again, "leased rows are invisible to another claimer")

		// The lease runs out: another worker may take the row over.
		later, err := mailStore.ClaimUnsent(ctx, claim("tok-b", func(q *entity.MailClaim) { q.Now = now.Add(3 * time.Minute) }))
		require.NoError(t, err)
		require.Equal(t, []int{a, b}, claimIDs(later))
		require.Equal(t, "tok-b", outboxRow(t, ms, a).ClaimToken.String)
	})

	t.Run("claim filters kind, cutover, expiry, attempts, suppression, quarantine", func(t *testing.T) {
		reset(t)
		now := time.Now().UTC().Truncate(time.Second)
		opened := now.Add(-time.Hour)
		order := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		signin := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopSignIn, expiresAt: now.Add(5 * time.Minute)})
		legacy := seedOutbox(t, ms, outboxSeed{})                                                                   // NULL kind
		foreign := seedOutbox(t, ms, outboxSeed{kind: "promo"})                                                     // not a shop kind
		expired := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopSignIn, expiresAt: now.Add(-time.Second)}) // past TTL
		early := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder, createdAt: opened.Add(-time.Second)})
		atOpen := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder, createdAt: opened})
		suppressed := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder, to: "gone@shoplane.test"})
		require.NoError(t, mailStore.AddSuppression(ctx, "gone@shoplane.test", entity.SuppressionReasonBounce))
		capped := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		require.NoError(t, mailStore.MarkSendDead(ctx, capped, "dead", 5))
		quarantined := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		require.NoError(t, ExecNamed(ctx, ms.DB(), `UPDATE send_email_request SET quarantined_at = :n WHERE id = :id`,
			map[string]any{"n": now, "id": quarantined}))
		sent := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		require.NoError(t, mailStore.UpdateSent(ctx, sent))

		got, err := mailStore.ClaimUnsent(ctx, claim("tok", func(q *entity.MailClaim) {
			q.Kinds, q.NotBefore, q.Now = shopKinds, opened, now
		}))
		require.NoError(t, err)
		require.Equal(t, []int{order, signin, atOpen}, claimIDs(got))
		for _, id := range []int{legacy, foreign, expired, early, suppressed, capped, quarantined, sent} {
			require.False(t, outboxRow(t, ms, id).ClaimToken.Valid, "row %d must not be claimed", id)
		}

		// The storefront claim (no kinds, no cutover) still takes legacy and foreign rows, never
		// an expired, suppressed, capped, quarantined or sent one.
		require.NoError(t, mailStore.ReleaseClaims(ctx, claimIDs(got), "tok"))
		all, err := mailStore.ClaimUnsent(ctx, claim("sf", func(q *entity.MailClaim) { q.Now = now }))
		require.NoError(t, err)
		ids := claimIDs(all)
		sort.Ints(ids)
		require.Equal(t, []int{order, signin, legacy, foreign, early, atOpen}, ids)
	})

	t.Run("limit and validation", func(t *testing.T) {
		reset(t)
		for i := 0; i < 5; i++ {
			seedOutbox(t, ms, outboxSeed{})
		}
		got, err := mailStore.ClaimUnsent(ctx, claim("tok", func(q *entity.MailClaim) { q.Limit = 2 }))
		require.NoError(t, err)
		require.Len(t, got, 2)
		for _, bad := range []func(*entity.MailClaim){
			func(q *entity.MailClaim) { q.Limit = 0 },
			func(q *entity.MailClaim) { q.Lease = 0 },
			func(q *entity.MailClaim) { q.Token = "" },
			func(q *entity.MailClaim) { q.MaxAttempts = 0 },
		} {
			_, err := mailStore.ClaimUnsent(ctx, claim("tok", bad))
			require.Error(t, err)
		}
	})

	t.Run("concurrent claimers never share a row", func(t *testing.T) {
		reset(t)
		const rows, workers = 60, 8
		want := make([]int, 0, rows)
		for i := 0; i < rows; i++ {
			want = append(want, seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder, to: fmt.Sprintf("c%d@example.com", i)}))
		}
		var (
			mu    sync.Mutex
			owner = map[int]string{}
			dupes []int
			errs  []error
			wg    sync.WaitGroup
			start = make(chan struct{})
		)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(token string) {
				defer wg.Done()
				<-start
				for {
					got, err := mailStore.ClaimUnsent(ctx, claim(token, func(q *entity.MailClaim) {
						q.Limit, q.Kinds = 3, shopKinds
					}))
					mu.Lock()
					if err != nil {
						errs = append(errs, err)
						mu.Unlock()
						return
					}
					for _, r := range got {
						if _, seen := owner[r.Id]; seen {
							dupes = append(dupes, r.Id)
						}
						owner[r.Id] = token
					}
					mu.Unlock()
					if len(got) == 0 {
						return
					}
				}
			}(fmt.Sprintf("worker-%d", w))
		}
		close(start)
		wg.Wait()
		require.Empty(t, errs)
		require.Empty(t, dupes, "a row was claimed twice")
		require.Len(t, owner, rows, "every row claimed exactly once")
		for _, id := range want {
			require.Equal(t, owner[id], outboxRow(t, ms, id).ClaimToken.String)
		}
	})

	t.Run("a row locked by an open claim is skipped, not waited for", func(t *testing.T) {
		reset(t)
		first := seedOutbox(t, ms, outboxSeed{})
		second := seedOutbox(t, ms, outboxSeed{})
		locked := make(chan struct{})
		done := make(chan struct{})
		txErr := make(chan error, 1)
		go func() {
			txErr <- ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
				var id int
				if err := rep.DB().QueryRowxContext(ctx,
					`SELECT id FROM send_email_request WHERE id = ? FOR UPDATE`, first).Scan(&id); err != nil {
					return err
				}
				close(locked)
				<-done
				return nil
			})
		}()
		<-locked
		qctx, qcancel := context.WithTimeout(ctx, 5*time.Second)
		got, err := mailStore.ClaimUnsent(qctx, claim("tok"))
		qcancel()
		close(done)
		require.NoError(t, <-txErr)
		require.NoError(t, err)
		require.Equal(t, []int{second}, claimIDs(got))
		require.False(t, outboxRow(t, ms, first).ClaimToken.Valid)
	})

	t.Run("release needs the token and counts no attempt", func(t *testing.T) {
		reset(t)
		id := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		_, err := mailStore.ClaimUnsent(ctx, claim("tok"))
		require.NoError(t, err)
		require.NoError(t, mailStore.ReleaseClaims(ctx, []int{id}, "someone-else"))
		require.Equal(t, "tok", outboxRow(t, ms, id).ClaimToken.String, "a foreign token releases nothing")
		require.Error(t, mailStore.ReleaseClaims(ctx, []int{id}, ""))
		require.NoError(t, mailStore.ReleaseClaims(ctx, nil, "tok"))

		require.NoError(t, mailStore.ReleaseClaims(ctx, []int{id}, "tok"))
		r := outboxRow(t, ms, id)
		require.False(t, r.ClaimToken.Valid)
		require.False(t, r.NextRetryAt.Valid)
		require.Zero(t, r.Attempts)
		again, err := mailStore.ClaimUnsent(ctx, claim("tok2"))
		require.NoError(t, err)
		require.Equal(t, []int{id}, claimIDs(again), "a released row is claimable at once")
	})

	t.Run("AddMail writes an idempotency key once", func(t *testing.T) {
		reset(t)
		row := &entity.SendEmailRequest{From: "a@x.test", To: "c@example.com", Subject: "s", Html: "<p>x</p>"}
		id, err := mailStore.AddMail(ctx, row)
		require.NoError(t, err)
		require.True(t, row.IdempotencyKey.Valid, "the key is written back for the inline send")
		require.Regexp(t, `^mail-[0-9a-f-]{36}$`, row.IdempotencyKey.String)
		require.Equal(t, row.IdempotencyKey.String, outboxRow(t, ms, id).IdemKey.String)
		other := seedOutbox(t, ms, outboxSeed{})
		require.NotEqual(t, row.IdempotencyKey.String, outboxRow(t, ms, other).IdemKey.String)
		given := &entity.SendEmailRequest{From: "a@x.test", To: "c@example.com", Subject: "s", Html: "<p>x</p>",
			IdempotencyKey: sql.NullString{String: "mail-given", Valid: true}}
		gid, err := mailStore.AddMail(ctx, given)
		require.NoError(t, err)
		require.Equal(t, "mail-given", outboxRow(t, ms, gid).IdemKey.String)
		got, err := mailStore.ClaimUnsent(ctx, claim("tok"))
		require.NoError(t, err)
		require.Len(t, got, 3)
		require.Equal(t, row.IdempotencyKey.String, got[0].IdempotencyKey.String, "claimed rows carry their key")
		require.False(t, got[0].FirstDispatchAt.Valid)
	})

	t.Run("dispatch mark needs the lease and keeps the first time", func(t *testing.T) {
		reset(t)
		id := seedOutbox(t, ms, outboxSeed{})
		_, err := mailStore.ClaimUnsent(ctx, claim("tok"))
		require.NoError(t, err)
		require.ErrorIs(t, mailStore.MarkMailDispatched(ctx, id, "someone-else", time.Now()), entity.ErrMailClaimLost)
		require.Zero(t, outboxRow(t, ms, id).Dispatches)
		first := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		require.NoError(t, mailStore.MarkMailDispatched(ctx, id, "tok", first))
		require.NoError(t, mailStore.MarkMailDispatched(ctx, id, "tok", first), "the same second still counts as a change")
		r := outboxRow(t, ms, id)
		require.Equal(t, 2, r.Dispatches)
		require.True(t, r.FirstDispatch.Time.Equal(first), "%s != %s", r.FirstDispatch.Time, first)
		require.Equal(t, "tok", r.ClaimToken.String, "a dispatch mark keeps the lease")
		got, err := mailStore.ClaimUnsent(ctx, claim("late", func(q *entity.MailClaim) { q.Now = time.Now().Add(3 * time.Minute) }))
		require.NoError(t, err)
		require.True(t, got[0].FirstDispatchAt.Valid, "a later claimer sees the first dispatch")
		require.ErrorIs(t, mailStore.MarkMailDispatched(ctx, id, "tok", time.Now()), entity.ErrMailClaimLost,
			"the old holder cannot dispatch after the lease moved")
		require.Error(t, mailStore.MarkMailDispatched(ctx, id, "", time.Now()))
	})

	t.Run("settle needs the lease token and checks one row", func(t *testing.T) {
		reset(t)
		sentID := seedOutbox(t, ms, outboxSeed{to: "sent@shoplane.test"})
		retryID := seedOutbox(t, ms, outboxSeed{})
		deadID := seedOutbox(t, ms, outboxSeed{})
		quarID := seedOutbox(t, ms, outboxSeed{})
		lostID := seedOutbox(t, ms, outboxSeed{})
		now := time.Now().UTC().Truncate(time.Second)
		_, err := mailStore.ClaimUnsent(ctx, claim("tok", func(q *entity.MailClaim) { q.Now = now }))
		require.NoError(t, err)

		sent := entity.MailSettle{ID: sentID, Token: "tok", Outcome: entity.MailSent, ProviderID: "re_123"}
		wrong := sent
		wrong.Token = "someone-else"
		require.ErrorIs(t, mailStore.SettleMail(ctx, wrong), entity.ErrMailClaimLost)
		require.False(t, outboxRow(t, ms, sentID).Sent, "a foreign token writes nothing")
		require.NoError(t, mailStore.SettleMail(ctx, sent))
		require.ErrorIs(t, mailStore.SettleMail(ctx, sent), entity.ErrMailClaimLost, "a row settles once")
		next := now.Add(time.Minute)
		require.NoError(t, mailStore.SettleMail(ctx, entity.MailSettle{ID: retryID, Token: "tok", Outcome: entity.MailRetry, ErrMsg: "409", NextRetryAt: next}))
		require.NoError(t, mailStore.SettleMail(ctx, entity.MailSettle{ID: deadID, Token: "tok", Outcome: entity.MailDead, ErrMsg: "422", MaxAttempts: 5}))
		require.NoError(t, mailStore.SettleMail(ctx, entity.MailSettle{ID: quarID, Token: "tok", Outcome: entity.MailQuarantined, ErrMsg: "sender is not the shop sender"}))

		r := outboxRow(t, ms, sentID)
		require.True(t, r.Sent)
		require.Equal(t, "re_123", r.ProviderID.String)
		r = outboxRow(t, ms, retryID)
		require.Equal(t, 1, r.Attempts)
		require.Equal(t, "409", r.ErrMsg.String)
		require.True(t, r.NextRetryAt.Time.Equal(next))
		r = outboxRow(t, ms, deadID)
		require.Equal(t, 5, r.Attempts)
		require.False(t, r.NextRetryAt.Valid)
		r = outboxRow(t, ms, quarID)
		require.True(t, r.QuarantinedAt.Valid)
		require.Equal(t, "sender is not the shop sender", r.ErrMsg.String)
		require.False(t, r.NextRetryAt.Valid)
		for _, id := range []int{sentID, retryID, deadID, quarID} {
			require.False(t, outboxRow(t, ms, id).ClaimToken.Valid, "row %d keeps its token", id)
		}

		// The lease runs out and another worker takes the row: the old holder's late answer is lost,
		// never written over the new owner.
		_, err = mailStore.ClaimUnsent(ctx, claim("new-owner", func(q *entity.MailClaim) { q.Now = now.Add(3 * time.Minute) }))
		require.NoError(t, err)
		require.Equal(t, "new-owner", outboxRow(t, ms, lostID).ClaimToken.String)
		require.ErrorIs(t, mailStore.SettleMail(ctx, entity.MailSettle{ID: lostID, Token: "tok", Outcome: entity.MailDead, ErrMsg: "x", MaxAttempts: 5}), entity.ErrMailClaimLost)
		r = outboxRow(t, ms, lostID)
		require.Zero(t, r.Attempts)
		require.Equal(t, "new-owner", r.ClaimToken.String)

		// A quarantined row is never revived by a settle, and a late release of a sent row is a no-op.
		require.NoError(t, mailStore.ReleaseClaims(ctx, []int{sentID}, "tok"))
		require.True(t, outboxRow(t, ms, sentID).Sent)
		got, err := mailStore.ClaimUnsent(ctx, claim("tok2", func(q *entity.MailClaim) { q.Now = now.Add(10 * time.Minute) }))
		require.NoError(t, err)
		require.NotContains(t, claimIDs(got), quarID)
		unsent, err := mailStore.GetAllUnsent(ctx, false, 5, now.Add(time.Hour))
		require.NoError(t, err)
		require.NotContains(t, claimIDs(unsent), quarID, "the worker view never holds a quarantined row")

		for _, bad := range []entity.MailSettle{
			{ID: retryID, Outcome: entity.MailSent},                       // no token
			{ID: retryID, Token: "tok2", Outcome: entity.MailRetry},       // retry without a time
			{ID: retryID, Token: "tok2", Outcome: entity.MailDead},        // dead without a cap
			{ID: retryID, Token: "tok2", Outcome: entity.MailOutcome(99)}, // unknown
		} {
			err := mailStore.SettleMail(ctx, bad)
			require.Error(t, err)
			require.NotErrorIs(t, err, entity.ErrMailClaimLost)
		}

		// The provider id finds the recipient of that row only.
		to, err := mailStore.ProviderMailRecipient(ctx, sentID, "re_123")
		require.NoError(t, err)
		require.Equal(t, "sent@shoplane.test", to)
		for _, q := range []struct {
			id  int
			pid string
		}{{sentID, "re_other"}, {retryID, "re_123"}, {sentID, ""}, {0, "re_123"}} {
			to, err := mailStore.ProviderMailRecipient(ctx, q.id, q.pid)
			require.NoError(t, err)
			require.Empty(t, to, "row %d / %q", q.id, q.pid)
		}
	})

	t.Run("legacy bookkeeping still clears the lease", func(t *testing.T) {
		reset(t)
		sentID := seedOutbox(t, ms, outboxSeed{})
		retryID := seedOutbox(t, ms, outboxSeed{})
		deadID := seedOutbox(t, ms, outboxSeed{})
		_, err := mailStore.ClaimUnsent(ctx, claim("tok"))
		require.NoError(t, err)
		require.NoError(t, mailStore.UpdateSent(ctx, sentID))
		require.NoError(t, mailStore.ScheduleSendRetry(ctx, retryID, "409", time.Now().UTC().Add(time.Minute)))
		require.NoError(t, mailStore.MarkSendDead(ctx, deadID, "422", 5))
		for _, id := range []int{sentID, retryID, deadID} {
			require.False(t, outboxRow(t, ms, id).ClaimToken.Valid, "row %d keeps its token", id)
		}
	})

	t.Run("cutover quarantines what a shop must never send", func(t *testing.T) {
		reset(t)
		now := time.Now().UTC().Truncate(time.Second)
		opened := now.Add(-time.Hour)
		order := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder})
		signin := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopSignIn, expiresAt: now.Add(5 * time.Minute)})
		legacy := seedOutbox(t, ms, outboxSeed{})
		foreign := seedOutbox(t, ms, outboxSeed{kind: "promo"})
		expired := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopSignIn, expiresAt: now.Add(-time.Second)})
		early := seedOutbox(t, ms, outboxSeed{kind: entity.MailKindShopOrder, createdAt: opened.Add(-time.Second)})
		failed := seedOutbox(t, ms, outboxSeed{})
		require.NoError(t, mailStore.ScheduleSendRetry(ctx, failed, "provider 500", now.Add(-time.Second)))
		leased := seedOutbox(t, ms, outboxSeed{kind: "promo"})
		require.NoError(t, ExecNamed(ctx, ms.DB(),
			`UPDATE send_email_request SET claim_token = 'other', next_retry_at = :l WHERE id = :id`,
			map[string]any{"l": now.Add(time.Minute), "id": leased}))
		sent := seedOutbox(t, ms, outboxSeed{})
		require.NoError(t, mailStore.UpdateSent(ctx, sent))

		rule := entity.MailCutover{Kinds: shopKinds, OpenedAt: opened, Now: now, Reason: "shop cutover"}
		n, err := mailStore.QuarantineOutside(ctx, rule)
		require.NoError(t, err)
		require.EqualValues(t, 5, n)
		for _, id := range []int{legacy, foreign, expired, early, failed} {
			require.True(t, outboxRow(t, ms, id).QuarantinedAt.Valid, "row %d should be quarantined", id)
		}
		for _, id := range []int{order, signin, leased, sent} {
			require.False(t, outboxRow(t, ms, id).QuarantinedAt.Valid, "row %d should be left alone", id)
		}
		require.Equal(t, "shop cutover", outboxRow(t, ms, legacy).ErrMsg.String)
		require.Equal(t, "provider 500", outboxRow(t, ms, failed).ErrMsg.String, "the last provider error is kept")
		require.Equal(t, "other", outboxRow(t, ms, leased).ClaimToken.String)

		n, err = mailStore.QuarantineOutside(ctx, rule)
		require.NoError(t, err)
		require.Zero(t, n, "a second pass finds nothing new")
		_, err = mailStore.QuarantineOutside(ctx, entity.MailCutover{Now: now})
		require.Error(t, err, "a cutover without kinds would quarantine everything")

		// Once its lease runs out the foreign row is caught too.
		n, err = mailStore.QuarantineOutside(ctx, entity.MailCutover{Kinds: shopKinds, OpenedAt: opened, Now: now.Add(2 * time.Minute)})
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		require.True(t, outboxRow(t, ms, leased).QuarantinedAt.Valid)
	})
}

// TestMigrationSendEmailShopLaneUpDownUp runs the outbox-lane migration under ANSI_QUOTES: the
// suite starts migrated, Down drops the eight columns (twice is a no-op), Up adds them back with their
// shapes, and a second Up is a no-op. The file is found by name so a renumber does not break it.
func TestMigrationSendEmailShopLaneUpDownUp(t *testing.T) {
	requireGarmentLabelGeometryDB(t) // local container or CI only
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	files, err := filepath.Glob("sql/*_send_email_request_shop_lane.sql")
	require.NoError(t, err)
	require.Len(t, files, 1)
	f, err := os.Open(files[0])
	require.NoError(t, err)
	parsed, err := sqlparse.ParseMigration(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')`)
	require.NoError(t, err)
	run := func(name string, statements []string) {
		t.Helper()
		for i, statement := range statements {
			_, err := conn.ExecContext(ctx, statement)
			require.NoErrorf(t, err, "%s statement %d", name, i)
		}
	}
	t.Cleanup(func() {
		for _, statement := range parsed.UpStatements {
			_, _ = testDB.ExecContext(context.Background(), statement)
		}
	})
	columns := func() map[string]string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, `
			SELECT COLUMN_NAME, CONCAT(COLUMN_TYPE, ' ', IS_NULLABLE, ' ', COALESCE(CHARACTER_SET_NAME, '-'), ' ', COALESCE(COLUMN_DEFAULT, '-'))
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request'
				AND COLUMN_NAME IN ('kind', 'expires_at', 'claim_token', 'quarantined_at',
					'idempotency_key', 'first_dispatch_at', 'dispatch_count', 'provider_email_id')`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, shape string
			require.NoError(t, rows.Scan(&name, &shape))
			out[name] = shape
		}
		require.NoError(t, rows.Err())
		return out
	}
	want := map[string]string{
		"kind":           "varchar(16) YES ascii -",
		"expires_at":     "datetime YES - -",
		"claim_token":    "char(36) YES ascii -",
		"quarantined_at": "datetime YES - -",
		// idempotency, dispatch mark and provider id (Codex review of PR 144, #4 / #5)
		"idempotency_key":   "varchar(64) YES ascii -",
		"first_dispatch_at": "datetime YES - -",
		"dispatch_count":    "int NO - 0",
		"provider_email_id": "varchar(64) YES ascii -",
	}
	require.Equal(t, want, columns(), "the suite starts migrated")
	run("down", parsed.DownStatements)
	require.Empty(t, columns())
	run("down retry", parsed.DownStatements)
	run("up", parsed.UpStatements)
	require.Equal(t, want, columns())
	run("up retry", parsed.UpStatements)
	require.Equal(t, want, columns())
}
