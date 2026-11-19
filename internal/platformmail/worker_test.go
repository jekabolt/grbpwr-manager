package platformmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

const workerDB = "platformmail_b3_test"

func workerDSN(t *testing.T, db string) string {
	t.Helper()
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

func openOutbox(t *testing.T) *platformstore.Store {
	t.Helper()
	d := workerDSN(t, workerDB)
	if err := testdbguard.CheckMySQLDSN(d); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", workerDSN(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + workerDB + "`", "CREATE DATABASE `" + workerDB + "` CHARACTER SET utf8mb4"} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	root.Close()
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", workerDSN(t, "")); err == nil {
			_, _ = r.Exec("DROP DATABASE IF EXISTS `" + workerDB + "`")
			r.Close()
		}
	})
	st, err := platformstore.Open(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

type fakeSender struct {
	mu   sync.Mutex
	sent []Message
	keys []string
}

func (f *fakeSender) Send(_ context.Context, m Message, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.sent = append(f.sent, m)
	return nil
}

type outRow struct {
	Status   string         `db:"status"`
	Attempts int            `db:"attempts"`
	LastErr  sql.NullString `db:"last_error"`
	Payload  string         `db:"payload"`
}

func rowOf(t *testing.T, st *platformstore.Store, to string) outRow {
	t.Helper()
	var r outRow
	if err := st.DB().Get(&r, `SELECT status, attempts, last_error, payload FROM platform_email WHERE to_email = ?`, to); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWorkerSendsRetriesAndSweeps(t *testing.T) {
	st := openOutbox(t)
	ctx := context.Background()
	enqueue := func(l platformstore.Letter) {
		if err := st.EnqueueLetter(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(platformstore.Letter{To: "delivered+b3ok@resend.dev", Template: "verify", Code: "482913",
		Link: "https://beta.lekalo.studio/confirm#token=SECRET1", Slug: "atelier", TeamName: "Atelier"})
	enqueue(platformstore.Letter{To: "delivered+b3retry@resend.dev", Template: "reset", Code: "731054",
		Link: "https://beta.lekalo.studio/reset#token=SECRET2"})
	enqueue(platformstore.Letter{To: "delivered+b3bad@resend.dev", Template: "invite", TeamName: "Atelier",
		Link: "https://beta.lekalo.studio/join#token=SECRET3"})

	fs := &fakeSender{}
	w, err := New(st.DB(), fs, Config{Enabled: true, From: "lekalo <no-reply@lekalo.studio>", BaseDomain: "beta.lekalo.studio"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w.SetClock(func() time.Time { return now })
	// The first letter sends; the second fails transiently; the third permanently.
	failFor := map[string]error{
		"delivered+b3retry@resend.dev": &mail.HTTPSendError{StatusCode: 503, Body: "down"},
		"delivered+b3bad@resend.dev":   &PermanentError{Err: errors.New("422 invalid")},
	}
	w.sender = senderFunc(func(c context.Context, m Message, key string) error {
		// The send runs with no transaction open: another connection can lock the row at once.
		tx, err := st.DB().Beginx()
		if err != nil {
			return err
		}
		var id int64
		lerr := tx.Get(&id, `SELECT id FROM platform_email WHERE to_email = ? FOR UPDATE NOWAIT`, m.To)
		_ = tx.Rollback()
		if lerr != nil {
			t.Errorf("row of %s locked during the send: %v", m.To, lerr)
		}
		if err := failFor[m.To]; err != nil {
			return err
		}
		return fs.Send(c, m, key)
	})
	if _, err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, st, "delivered+b3ok@resend.dev"); r.Status != "sent" || r.Attempts != 1 {
		t.Fatalf("ok row = %+v", r)
	}
	if len(fs.sent) != 1 || fs.sent[0].Subject != "482 913 · confirm email" || fs.sent[0].From != "lekalo <no-reply@lekalo.studio>" ||
		!strings.HasPrefix(fs.keys[0], "platform-email-") {
		t.Fatalf("sent = %+v keys %v", fs.sent, fs.keys)
	}
	r := rowOf(t, st, "delivered+b3retry@resend.dev")
	if r.Status != "pending" || r.Attempts != 1 || strings.Contains(r.LastErr.String, "SECRET") {
		t.Fatalf("retry row = %+v", r)
	}
	if r := rowOf(t, st, "delivered+b3bad@resend.dev"); r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("bad row = %+v", r)
	}
	// Backoff: an immediate tick does not retry.
	if _, err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, st, "delivered+b3retry@resend.dev"); r.Attempts != 1 {
		t.Fatalf("retried inside the backoff: %+v", r)
	}
	// The backoff lives in the row: a fresh worker (a restart, another replica) waits too.
	w2, err := New(st.DB(), w.sender, Config{Enabled: true, From: "lekalo <no-reply@lekalo.studio>", BaseDomain: "beta.lekalo.studio"})
	if err != nil {
		t.Fatal(err)
	}
	w2.SetClock(func() time.Time { return now })
	if _, err := w2.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, st, "delivered+b3retry@resend.dev"); r.Attempts != 1 {
		t.Fatalf("a fresh worker retried inside the backoff: %+v", r)
	}
	// Five attempts in all, then failed.
	for range 6 {
		now = now.Add(retryMax + time.Second)
		if _, err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r := rowOf(t, st, "delivered+b3retry@resend.dev"); r.Status != "failed" || r.Attempts != maxAttempts {
		t.Fatalf("after retries = %+v", r)
	}
	// The sent letter is never sent twice.
	if len(fs.sent) != 1 {
		t.Fatalf("sent %d letters", len(fs.sent))
	}

	// Disabled mailer: rows are skipped, payload kept.
	enqueue(platformstore.Letter{To: "delivered+b3skip@resend.dev", Template: "verify", Code: "111222", Link: "https://x/confirm#token=S"})
	off, err := New(st.DB(), fs, Config{Enabled: false, From: "lekalo <no-reply@lekalo.studio>", BaseDomain: "beta.lekalo.studio"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := off.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := rowOf(t, st, "delivered+b3skip@resend.dev"); r.Status != "skipped" || !strings.Contains(r.Payload, "111222") {
		t.Fatalf("skipped row = %+v", r)
	}

	// Sweep: a verify letter older than a day loses its code and link; a fresh one keeps them.
	if _, err := st.DB().Exec(`UPDATE platform_email SET created_at = ? WHERE to_email = 'delivered+b3ok@resend.dev'`, now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.SetClock(func() time.Time { return now })
	n, err := w.Sweep(ctx)
	if err != nil || n < 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if r := rowOf(t, st, "delivered+b3ok@resend.dev"); strings.Contains(r.Payload, "482913") || strings.Contains(r.Payload, "SECRET1") ||
		!strings.Contains(r.Payload, "atelier") {
		t.Fatalf("swept payload = %s", r.Payload)
	}
	if r := rowOf(t, st, "delivered+b3skip@resend.dev"); !strings.Contains(r.Payload, "111222") {
		t.Fatalf("fresh payload swept: %s", r.Payload)
	}
}

type senderFunc func(context.Context, Message, string) error

func (f senderFunc) Send(ctx context.Context, m Message, key string) error { return f(ctx, m, key) }

// TestWorkerUnconfiguredAndLease: an enabled mailer without a key leaves letters pending; a send
// whose worker died after the claim is sent again once the lease runs out, under the same key.
func TestWorkerUnconfiguredAndLease(t *testing.T) {
	st := openOutbox(t)
	ctx := context.Background()
	if err := st.EnqueueLetter(ctx, platformstore.Letter{To: "delivered+b3wait@resend.dev", Template: "verify", Code: "482913",
		Link: "https://beta.lekalo.studio/confirm#token=SECRET1", Slug: "atelier", TeamName: "Atelier"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, From: "lekalo <no-reply@lekalo.studio>", BaseDomain: "beta.lekalo.studio"}
	for _, c := range []struct {
		sender Sender
		from   string
	}{{nil, cfg.From}, {&fakeSender{}, ""}} {
		bad := cfg
		bad.From = c.from
		w, err := New(st.DB(), c.sender, bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Tick(ctx); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("unconfigured tick = %v", err)
		}
	}
	if r := rowOf(t, st, "delivered+b3wait@resend.dev"); r.Status != "pending" || r.Attempts != 0 {
		t.Fatalf("unconfigured mailer touched the row: %+v", r)
	}

	fs := &fakeSender{}
	w, err := New(st.DB(), fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	w.SetClock(func() time.Time { return now })
	// A worker claims the row and dies before sending.
	r, _, _, changed, err := w.claim(ctx, 0, now)
	if err != nil || r.ID == 0 || changed {
		t.Fatalf("claim = %+v %v %v", r, changed, err)
	}
	if _, err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.sent) != 0 {
		t.Fatal("a leased row was sent")
	}
	now = now.Add(leaseFor + time.Second)
	if _, err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, st, "delivered+b3wait@resend.dev"); got.Status != "sent" || got.Attempts != 2 {
		t.Fatalf("after the lease = %+v", got)
	}
	if len(fs.keys) != 1 || fs.keys[0] != fmt.Sprintf("platform-email-%d", r.ID) {
		t.Fatalf("keys = %v", fs.keys)
	}
}
