package mail

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/stretchr/testify/require"
)

// plainPrepare sends a row as it is.
func plainPrepare(row *entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error) {
	html := row.Html
	return resend.SendEmailRequest{From: row.From, To: []string{row.To}, Subject: row.Subject, Html: &html}, rowSend, "", nil
}

type laneProbe struct {
	cooldowns []time.Duration
	acquires  int
}

func testLane(repo *fakeOutbox, cli *fakeSender, p *laneProbe) *outboxLane {
	return &outboxLane{
		name:        "test",
		repo:        repo,
		cli:         cli,
		teamID:      7,
		keySpace:    outboxKeySpace("test"),
		batch:       10,
		rounds:      1,
		maxAttempts: 5,
		retryBase:   time.Minute,
		retryMax:    time.Hour,
		acquire: func(context.Context) error {
			p.acquires++
			return nil
		},
		cooldown: func(d time.Duration) { p.cooldowns = append(p.cooldowns, d) },
		prepare:  plainPrepare,
	}
}

func queueRows(repo *fakeOutbox, n int) []int {
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, repo.put(entity.SendEmailRequest{
			From: "a@x.test", To: "c@example.com", Subject: "s", Html: "<p>h</p>",
		}))
	}
	return ids
}

// requireReleased: back in the queue, no attempt counted, no lease, nothing recorded.
func requireReleased(t *testing.T, repo *fakeOutbox, id int) {
	t.Helper()
	r := repo.row(id)
	require.False(t, r.Sent, "row %d sent", id)
	require.Zero(t, r.SendAttemptCount, "row %d: an attempt was counted", id)
	require.False(t, r.NextRetryAt.Valid, "row %d still leased", id)
	require.Empty(t, r.token, "row %d keeps its claim token", id)
	require.Empty(t, r.quarantined)
	require.False(t, r.dead)
}

func TestOutboxLaneSendsWithStableDistinctKeys(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 3)
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))

	sent := cli.sent()
	require.Len(t, sent, 3)
	keys := map[string]bool{}
	keyRe := regexp.MustCompile(`^mail-[0-9a-f]{8}-t7-m\d+-[0-9a-f]{8}$`)
	for _, s := range sent {
		require.Regexp(t, keyRe, s.key)
		keys[s.key] = true
	}
	require.Len(t, keys, 3, "every row has its own key")
	for _, id := range ids {
		require.True(t, repo.row(id).Sent)
	}
	require.Len(t, repo.claims, 1)
	require.Equal(t, outboxLease, repo.claims[0].Lease)
	require.NotEmpty(t, repo.claims[0].Token)
}

func TestOutboxLaneBudgetExhaustionCountsNoAttempt(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 3)
	l := testLane(repo, cli, p)
	calls := 0
	l.acquire = func(context.Context) error {
		calls++
		if calls > 1 {
			return context.DeadlineExceeded // the local limiter's wait ran past the tick deadline
		}
		return nil
	}
	require.NoError(t, l.drain(context.Background()))
	require.Len(t, cli.sent(), 1)
	require.True(t, repo.row(ids[0]).Sent)
	requireReleased(t, repo, ids[1])
	requireReleased(t, repo, ids[2])
}

func TestOutboxLaneDeadlineDuringPostCountsNoAttemptAndKeepsKey(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cli.script = []fakeAnswer{{err: context.Canceled}}
	cli.onPost = func(context.Context) { cancel() }
	require.NoError(t, testLane(repo, cli, p).drain(ctx))
	requireReleased(t, repo, ids[0])
	requireReleased(t, repo, ids[1])
	first := cli.sent()[0].key

	cli.onPost = nil
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	sent := cli.sent()
	require.Len(t, sent, 3)
	require.Equal(t, first, sent[1].key, "the resend of a cut row carries the same idempotency key")
	require.True(t, repo.row(ids[0]).Sent)
	require.True(t, repo.row(ids[1]).Sent)
}

func TestOutboxLane429CoolsDownAndReleases(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 3)
	cli.script = []fakeAnswer{{status: http.StatusOK}, {status: http.StatusTooManyRequests, retryAfter: "7"}}
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	require.Equal(t, []time.Duration{7 * time.Second}, p.cooldowns)
	require.Len(t, cli.sent(), 2, "the lane stops at the 429")
	require.True(t, repo.row(ids[0]).Sent)
	requireReleased(t, repo, ids[1])
	requireReleased(t, repo, ids[2])
}

func TestOutboxLane409IsRetriedUnderTheSameKey(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	id := queueRows(repo, 1)[0]
	cli.script = []fakeAnswer{{status: http.StatusConflict}}
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	r := repo.row(id)
	require.False(t, r.Sent)
	require.False(t, r.dead)
	require.Equal(t, 1, r.SendAttemptCount)
	require.True(t, r.NextRetryAt.Valid && r.NextRetryAt.Time.After(time.Now()), "retry is scheduled with backoff")

	// Retry time passes: the next drain resends under the same key.
	repo.mu.Lock()
	repo.rows[id].NextRetryAt = sql.NullTime{Time: time.Now().Add(-time.Second), Valid: true}
	repo.mu.Unlock()
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	sent := cli.sent()
	require.Len(t, sent, 2)
	require.Equal(t, sent[0].key, sent[1].key)
	require.True(t, repo.row(id).Sent)
}

func TestOutboxLanePermanentFailureDeadLetters(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	id := queueRows(repo, 1)[0]
	cli.script = []fakeAnswer{{status: http.StatusUnprocessableEntity}}
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	require.True(t, repo.row(id).dead)
}

func TestOutboxLaneVerdictsNeverReachTheProvider(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 2)
	l := testLane(repo, cli, p)
	l.prepare = func(row *entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error) {
		if row.Id == ids[0] {
			return resend.SendEmailRequest{}, rowQuarantine, "stale", nil
		}
		return resend.SendEmailRequest{}, rowMarkSent, "", nil
	}
	require.NoError(t, l.drain(context.Background()))
	require.Empty(t, cli.sent())
	require.Zero(t, p.acquires, "no budget spent on a row that is not sent")
	require.Equal(t, "stale", repo.row(ids[0]).quarantined)
	require.True(t, repo.row(ids[1]).Sent)
}

func TestOutboxLaneBookkeepingErrorIsReturned(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 2)
	repo.fail["SettleMail"] = errors.New("db down")
	err := testLane(repo, cli, p).drain(context.Background())
	require.ErrorContains(t, err, "outbox bookkeeping")
	require.Len(t, cli.sent(), 2, "a bookkeeping failure does not abandon the batch")
	for _, id := range ids {
		r := repo.row(id)
		require.True(t, r.NextRetryAt.Valid, "the row keeps its lease and comes back under the same key")
		require.Zero(t, r.SendAttemptCount)
	}
}

func TestOutboxLaneClaimErrorIsReturned(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	repo.fail["ClaimUnsent"] = errors.New("db down")
	require.ErrorContains(t, testLane(repo, cli, p).drain(context.Background()), "claim unsent")
}

func TestOutboxLaneRoundsAreCapped(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	queueRows(repo, 7)
	l := testLane(repo, cli, p)
	l.batch, l.rounds = 2, 3
	require.NoError(t, l.drain(context.Background()))
	require.Len(t, cli.sent(), 6)
	require.Len(t, repo.claims, 3)
}

func TestOutboxIdempotencyKey(t *testing.T) {
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	row := &entity.SendEmailRequest{Id: 42, To: "c@example.com", Subject: "s", CreatedAt: created}
	a := &outboxLane{keySpace: outboxKeySpace("shop", "lekalo.test"), teamID: 3}
	require.Equal(t, a.idempotencyKey(row), a.idempotencyKey(row), "stable for a row")

	other := *a
	other.teamID = 4
	require.NotEqual(t, a.idempotencyKey(row), other.idempotencyKey(row), "team is part of the key")
	space := *a
	space.keySpace = outboxKeySpace("shop", "other.test")
	require.NotEqual(t, a.idempotencyKey(row), space.idempotencyKey(row), "installation is part of the key")
	reused := *row
	reused.CreatedAt = created.Add(time.Hour)
	require.NotEqual(t, a.idempotencyKey(row), a.idempotencyKey(&reused), "a reused id is a new letter")
}

// TestStorefrontLaneKeepsItsShape: the storefront drain claims every kind (no filter, no cutover),
// uses the storefront budget and still attaches List-Unsubscribe.
func TestStorefrontLaneKeepsItsShape(t *testing.T) {
	repo := newFakeOutbox()
	m, err := new(&Config{APIKey: "k", FromEmail: "info@shop.test", FromName: "GRBPWR", UnsubscribeBaseURL: "https://backend.test", UnsubscribePepper: "pepper"}, repo, nil)
	require.NoError(t, err)
	cli := &fakeSender{}
	m.cli = cli
	repo.put(entity.SendEmailRequest{From: "GRBPWR <info@shop.test>", To: "c@example.com", Subject: "Order", Html: "<p>x</p>"})
	repo.put(entity.SendEmailRequest{From: "GRBPWR <info@shop.test>", To: "d@example.com", Subject: "Order", Html: "<p>x</p>",
		Kind: sql.NullString{String: "anything", Valid: true}})

	l := m.storefrontLane(context.Background())
	require.Nil(t, l.kinds)
	require.True(t, l.notBefore.IsZero())
	require.NoError(t, l.drain(context.Background()))
	sent := cli.sent()
	require.Len(t, sent, 2)
	require.NotNil(t, sent[0].req.Headers, "List-Unsubscribe headers stay on storefront letters")
	require.Empty(t, repo.claims[0].Kinds)
}

func TestOutboxLaneUsesTheRowsOwnKeyAndKeepsTheProviderID(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	row := &entity.SendEmailRequest{From: "a@x.test", To: "c@example.com", Subject: "s", Html: "<p>h</p>"}
	id, err := repo.AddMail(context.Background(), row)
	require.NoError(t, err)
	l := testLane(repo, cli, p)
	var accepted []string
	l.accepted = func(_ context.Context, r *entity.SendEmailRequest, pid string) { accepted = append(accepted, pid) }
	l.keySpace = outboxKeySpace("changed config") // a config change cannot move a written key
	require.NoError(t, l.drain(context.Background()))
	sent := cli.sent()
	require.Len(t, sent, 1)
	require.Equal(t, row.IdempotencyKey.String, sent[0].key)
	r := repo.row(id)
	require.True(t, r.Sent)
	require.Equal(t, "x", r.providerID)
	require.Equal(t, []string{"x"}, accepted)
	require.Equal(t, 1, r.dispatches)
	require.True(t, r.FirstDispatchAt.Valid)
}

func TestOutboxLaneMarksTheDispatchBeforeTheCall(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	id := queueRows(repo, 1)[0]
	var marked bool
	cli.onPost = func(context.Context) { marked = repo.row(id).FirstDispatchAt.Valid }
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	require.True(t, marked, "first_dispatch_at is written before the provider call")

	// A failed dispatch mark sends nothing and releases the row.
	repo2, cli2 := newFakeOutbox(), &fakeSender{}
	ids := queueRows(repo2, 2)
	repo2.fail["MarkMailDispatched"] = errors.New("db down")
	require.ErrorContains(t, testLane(repo2, cli2, p).drain(context.Background()), "outbox bookkeeping")
	require.Empty(t, cli2.sent())
	repo2.fail["MarkMailDispatched"] = nil
	for _, id := range ids {
		requireReleased(t, repo2, id)
	}
}

func TestOutboxLaneLostLeaseIsNeverOverwritten(t *testing.T) {
	// Lost before the dispatch mark: the row is skipped, the provider never sees it.
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 2)
	l := testLane(repo, cli, p)
	l.prepare = func(row *entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error) {
		if row.Id == ids[0] {
			repo.steal(row.Id)
		}
		return plainPrepare(row)
	}
	require.NoError(t, l.drain(context.Background()), "a lost lease is not a bookkeeping error")
	sent := cli.sent()
	require.Len(t, sent, 1, "only the row still leased is offered")
	require.Equal(t, "other-worker", repo.row(ids[0]).token, "the new owner's lease is untouched")
	require.Zero(t, repo.row(ids[0]).dispatches)
	require.True(t, repo.row(ids[1]).Sent)

	// Lost during the call: the outcome is not recorded over the new owner (it resends under the
	// same key; the provider answers it from the first call).
	repo, cli = newFakeOutbox(), &fakeSender{}
	id := queueRows(repo, 1)[0]
	cli.onPost = func(context.Context) { repo.steal(id) }
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	r := repo.row(id)
	require.False(t, r.Sent)
	require.Equal(t, "other-worker", r.token)

	// A failure answer after the lease was lost counts no attempt either.
	repo, cli = newFakeOutbox(), &fakeSender{script: []fakeAnswer{{status: http.StatusInternalServerError}}}
	id = queueRows(repo, 1)[0]
	cli.onPost = func(context.Context) { repo.steal(id) }
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	require.Zero(t, repo.row(id).SendAttemptCount)
}

func TestOutboxLaneQuarantinesRowsPastTheProviderKeyWindow(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	old := repo.put(entity.SendEmailRequest{From: "a@x.test", To: "c@example.com", Subject: "s", Html: "<p>h</p>",
		FirstDispatchAt: sql.NullTime{Time: time.Now().Add(-providerKeyWindow - time.Minute), Valid: true}})
	recent := repo.put(entity.SendEmailRequest{From: "a@x.test", To: "d@example.com", Subject: "s", Html: "<p>h</p>",
		FirstDispatchAt: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}})
	require.NoError(t, testLane(repo, cli, p).drain(context.Background()))
	sent := cli.sent()
	require.Len(t, sent, 1)
	require.Equal(t, []string{"d@example.com"}, sent[0].req.To, "inside the window the key still protects a resend")
	require.Contains(t, repo.row(old).quarantined, "idempotency window")
	require.False(t, repo.row(old).Sent)
	require.True(t, repo.row(recent).Sent)
}

func TestOutboxLaneGateRunsRightBeforeTheCall(t *testing.T) {
	repo, cli, p := newFakeOutbox(), &fakeSender{}, &laneProbe{}
	ids := queueRows(repo, 3)
	l := testLane(repo, cli, p)
	l.gate = func(_ context.Context, row *entity.SendEmailRequest) (rowVerdict, string, error) {
		switch row.Id {
		case ids[0]:
			return rowSend, "", nil
		case ids[1]:
			return rowQuarantine, "queued before the shop opened", nil
		}
		return rowHold, "", nil
	}
	require.NoError(t, l.drain(context.Background()))
	require.Len(t, cli.sent(), 1)
	require.True(t, repo.row(ids[0]).Sent)
	require.Equal(t, "queued before the shop opened", repo.row(ids[1]).quarantined)
	requireReleased(t, repo, ids[2])

	repo, cli = newFakeOutbox(), &fakeSender{}
	ids = queueRows(repo, 2)
	l = testLane(repo, cli, p)
	l.gate = func(context.Context, *entity.SendEmailRequest) (rowVerdict, string, error) {
		return rowSend, "", errors.New("platform db down")
	}
	require.ErrorContains(t, l.drain(context.Background()), "dispatch check")
	require.Empty(t, cli.sent())
	requireReleased(t, repo, ids[0])
	requireReleased(t, repo, ids[1])
}
