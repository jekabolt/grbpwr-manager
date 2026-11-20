package platformmail

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	svix "github.com/svix/svix-webhooks/go"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// idSender returns a Resend-like id per letter.
type idSender struct {
	mu   sync.Mutex
	sent []Message
}

func (f *idSender) Send(ctx context.Context, m Message, key string) error {
	_, err := f.SendID(ctx, m, key)
	return err
}

func (f *idSender) SendID(_ context.Context, m Message, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return fmt.Sprintf("re_%d", len(f.sent)), nil
}

type deliveryRow struct {
	Status   string         `db:"status"`
	Provider sql.NullString `db:"provider_id"`
	Delivery sql.NullString `db:"delivery"`
	LastErr  sql.NullString `db:"last_error"`
}

func deliveryOf(t *testing.T, st *platformstore.Store, to, tpl string) deliveryRow {
	t.Helper()
	var r deliveryRow
	if err := st.DB().Get(&r, `SELECT status, provider_id, delivery, last_error FROM platform_email
WHERE to_email = ? AND template = ? ORDER BY id DESC LIMIT 1`, to, tpl); err != nil {
		t.Fatal(err)
	}
	return r
}

func suppressionOf(t *testing.T, st *platformstore.Store, to string) string {
	t.Helper()
	var reason string
	err := st.DB().Get(&reason, `SELECT reason FROM platform_mail_suppression WHERE email = ?`, to)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return reason
}

func TestMailEventsAndSuppression(t *testing.T) {
	st := openOutbox(t)
	ctx := context.Background()
	const bad, good = "gone@x.test", "fine@x.test"
	for _, l := range []platformstore.Letter{
		{To: bad, Template: "verify", Code: "482913", Link: "https://beta.lekalo.studio/confirm#token=S1", Slug: "a", TeamName: "A"},
		{To: good, Template: "verify", Code: "482914", Link: "https://beta.lekalo.studio/confirm#token=S2", Slug: "b", TeamName: "B"},
	} {
		if err := st.EnqueueLetter(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	fs := &idSender{}
	w, err := New(st.DB(), fs, Config{Enabled: true, From: "lekalo <no-reply@lekalo.studio>", BaseDomain: "beta.lekalo.studio"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	badID := deliveryOf(t, st, bad, "verify").Provider.String
	goodID := deliveryOf(t, st, good, "verify").Provider.String
	if badID == "" || goodID == "" || badID == goodID {
		t.Fatalf("provider ids = %q %q", badID, goodID)
	}

	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("x6-test-secret-x6-test-secret-32"))
	h, err := NewEventHandler(st.DB(), secret)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := svix.NewWebhook(secret)
	n := 0
	post := func(body string, at time.Time, sign bool) int {
		t.Helper()
		n++
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/platform-mail", bytes.NewBufferString(body))
		id := "msg_" + strconv.Itoa(n)
		req.Header.Set("svix-id", id)
		req.Header.Set("svix-timestamp", strconv.FormatInt(at.Unix(), 10))
		sig, _ := signer.Sign(id, at, []byte(body))
		if !sign {
			sig = "v1,AAAA"
		}
		req.Header.Set("svix-signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	ev := func(typ, id, to, extra string) string {
		return `{"type":"` + typ + `","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `","data":{"email_id":"` + id +
			`","to":["` + to + `"]` + extra + `}}`
	}
	permanent := `,"bounce":{"type":"Permanent"}`
	now := time.Now()

	// Forged and stale bodies change nothing.
	if c := post(ev("email.bounced", badID, bad, permanent), now, false); c != http.StatusUnauthorized {
		t.Fatalf("unsigned = %d", c)
	}
	if c := post(ev("email.bounced", badID, bad, permanent), now.Add(-10*time.Minute), true); c != http.StatusUnauthorized {
		t.Fatalf("stale = %d", c)
	}
	// Signed, but the recipient is not this letter's, or the letter is not ours.
	if c := post(ev("email.bounced", badID, good, permanent), now, true); c != http.StatusNoContent {
		t.Fatalf("other recipient = %d", c)
	}
	if c := post(ev("email.complained", "re_foreign", good, ""), now, true); c != http.StatusNoContent {
		t.Fatalf("foreign letter = %d", c)
	}
	if d := deliveryOf(t, st, bad, "verify"); d.Delivery.Valid || suppressionOf(t, st, bad) != "" || suppressionOf(t, st, good) != "" {
		t.Fatalf("rejected events changed state: %+v", d)
	}
	// A transient bounce is its own state (soft_bounce), suppresses nothing, and a later
	// delivered replaces it; email.failed is its own state too.
	if c := post(ev("email.bounced", goodID, good, `,"bounce":{"type":"Transient"}`), now, true); c != http.StatusNoContent {
		t.Fatalf("transient = %d", c)
	}
	if d := deliveryOf(t, st, good, "verify"); d.Delivery.String != DeliverySoftBounce || suppressionOf(t, st, good) != "" {
		t.Fatalf("transient: %+v %q", d, suppressionOf(t, st, good))
	}
	_ = post(ev("email.delivered", goodID, good, ""), now, true)
	if d := deliveryOf(t, st, good, "verify"); d.Delivery.String != DeliveryDelivered {
		t.Fatalf("delivered after a soft bounce: %+v", d)
	}
	_ = post(ev("email.failed", goodID, good, ""), now, true)
	if d := deliveryOf(t, st, good, "verify"); d.Delivery.String != DeliveryFailed || suppressionOf(t, st, good) != "" {
		t.Fatalf("failed: %+v %q", d, suppressionOf(t, st, good))
	}
	// A permanent bounce suppresses; a late delivered does not undo it; a complaint outranks it.
	if c := post(ev("email.bounced", badID, "GONE@x.test", permanent), now, true); c != http.StatusNoContent {
		t.Fatalf("permanent = %d", c)
	}
	if suppressionOf(t, st, bad) != ReasonBounce {
		t.Fatal("permanent bounce not suppressed")
	}
	_ = post(ev("email.delivered", badID, bad, ""), now, true)
	if d := deliveryOf(t, st, bad, "verify"); d.Delivery.String != DeliveryBounced {
		t.Fatalf("late delivered overwrote: %+v", d)
	}
	_ = post(ev("email.complained", badID, bad, ""), now, true)
	if suppressionOf(t, st, bad) != ReasonComplaint || deliveryOf(t, st, bad, "verify").Delivery.String != DeliveryComplained {
		t.Fatal("complaint did not outrank the bounce")
	}
	_ = post(ev("email.bounced", badID, bad, permanent), now, true)
	if suppressionOf(t, st, bad) != ReasonComplaint {
		t.Fatal("bounce downgraded a complaint")
	}

	// The sender: a new confirm letter to the suppressed address is held back; a reset is sent.
	for _, l := range []platformstore.Letter{
		{To: bad, Template: "verify", Code: "111222", Link: "https://beta.lekalo.studio/confirm#token=S3", Slug: "a", TeamName: "A"},
		{To: bad, Template: "reset", Code: "333444", Link: "https://beta.lekalo.studio/reset#token=S4"},
	} {
		if err := st.EnqueueLetter(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	before := len(fs.sent)
	for range 2 {
		if _, err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if d := deliveryOf(t, st, bad, "verify"); d.Status != "failed" || d.LastErr.String != SuppressedError+ReasonComplaint {
		t.Fatalf("suppressed verify = %+v", d)
	}
	if d := deliveryOf(t, st, bad, "reset"); d.Status != "sent" || len(fs.sent) != before+1 {
		t.Fatalf("reset to a suppressed address = %+v, sent %d", d, len(fs.sent)-before)
	}

	// Expiry: a complaint holds for 180 days, a permanent bounce for 30; after that letters go again.
	verifyTo := func(when string, back time.Duration, reason string, wantSent bool) {
		t.Helper()
		if _, err := st.DB().Exec(`UPDATE platform_mail_suppression SET reason = ?, last_at = ? WHERE email = ?`,
			reason, time.Now().UTC().Add(-back), bad); err != nil {
			t.Fatal(err)
		}
		if err := st.EnqueueLetter(ctx, platformstore.Letter{To: bad, Template: "verify", Code: "555666",
			Link: "https://beta.lekalo.studio/confirm#token=S5", Slug: "a", TeamName: "A"}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := w.Tick(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if d := deliveryOf(t, st, bad, "verify"); (d.Status == "sent") != wantSent {
			t.Fatalf("%s: %+v", when, d)
		}
	}
	day := 24 * time.Hour
	verifyTo("complaint 179 days", 179*day, ReasonComplaint, false)
	verifyTo("complaint 181 days", 181*day, ReasonComplaint, true)
	verifyTo("bounce 29 days", 29*day, ReasonBounce, false)
	verifyTo("bounce 31 days", 31*day, ReasonBounce, true)
}
