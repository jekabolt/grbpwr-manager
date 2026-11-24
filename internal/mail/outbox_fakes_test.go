package mail

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// fakeOutbox is an in-memory send_email_request with the claim semantics of the store: a claim
// leases eligible rows (next_retry_at = lease end, claim token set), release hands them back without
// an attempt, a retry counts one, quarantine takes a row out for good, and every write to a claimed
// row needs its lease token.
type fakeOutbox struct {
	dependency.Mail // unimplemented methods panic

	mu     sync.Mutex
	rows   map[int]*fakeRow
	nextID int
	claims []entity.MailClaim
	// failures injected per method name: the call returns this error
	fail map[string]error
	// cutovers is every QuarantineOutside rule seen
	cutovers []entity.MailCutover
}

type fakeRow struct {
	entity.SendEmailRequest
	token       string
	quarantined string // reason; "" while not quarantined
	dead        bool
	dispatches  int    // dispatch marks
	providerID  string // the provider's id of the accepted letter
	ownerDue    bool   // owner_pending_at is set
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{rows: map[int]*fakeRow{}, fail: map[string]error{}}
}

func (f *fakeOutbox) put(r entity.SendEmailRequest) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	r.Id = f.nextID
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	f.rows[r.Id] = &fakeRow{SendEmailRequest: r}
	return r.Id
}

func (f *fakeOutbox) row(id int) fakeRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.rows[id]
}

// AddMail is the store's insert: a row without an idempotency key gets one (written back to ser).
// put is a raw insert, as an older binary wrote rows: no key.
func (f *fakeOutbox) AddMail(_ context.Context, ser *entity.SendEmailRequest) (int, error) {
	if err := f.failure("AddMail"); err != nil {
		return 0, err
	}
	if !ser.IdempotencyKey.Valid || ser.IdempotencyKey.String == "" {
		ser.IdempotencyKey = sql.NullString{String: "mail-" + uuid.NewString(), Valid: true}
	}
	return f.put(*ser), nil
}

// steal hands row id's lease to another worker (its lease ran out and someone claimed it).
func (f *fakeOutbox) steal(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id].token = "other-worker"
}

func (f *fakeOutbox) failure(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[name]
}

func (f *fakeOutbox) eligible(r *fakeRow, q entity.MailClaim, now time.Time) bool {
	switch {
	case r.Sent, r.quarantined != "", r.dead, r.SendAttemptCount >= q.MaxAttempts:
		return false
	case r.NextRetryAt.Valid && r.NextRetryAt.Time.After(now):
		return false
	case r.ExpiresAt.Valid && !r.ExpiresAt.Time.After(now):
		return false
	case len(q.Kinds) > 0 && (!r.Kind.Valid || !slices.Contains(q.Kinds, r.Kind.String)):
		return false
	case !q.NotBefore.IsZero() && r.CreatedAt.Unix() < q.NotBefore.Unix():
		return false
	}
	return true
}

func (f *fakeOutbox) ClaimUnsent(_ context.Context, q entity.MailClaim) ([]entity.SendEmailRequest, error) {
	if err := f.failure("ClaimUnsent"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, q)
	now := q.Now
	ids := make([]int, 0, len(f.rows))
	for id := range f.rows {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []entity.SendEmailRequest
	for _, id := range ids {
		r := f.rows[id]
		if len(out) >= q.Limit {
			break
		}
		if !f.eligible(r, q, now) {
			continue
		}
		r.token = q.Token
		r.NextRetryAt.Time, r.NextRetryAt.Valid = now.Add(q.Lease), true
		out = append(out, r.SendEmailRequest)
	}
	return out, nil
}

func (f *fakeOutbox) ReleaseClaims(_ context.Context, ids []int, token string) error {
	if err := f.failure("ReleaseClaims"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		if r := f.rows[id]; r != nil && r.token == token && !r.Sent {
			r.token = ""
			r.NextRetryAt.Valid = false
		}
	}
	return nil
}

func (f *fakeOutbox) MarkMailDispatched(_ context.Context, id int, token, key string, now time.Time) error {
	if err := f.failure("MarkMailDispatched"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if r == nil || r.token != token || r.Sent || r.quarantined != "" {
		return entity.ErrMailClaimLost
	}
	if !r.FirstDispatchAt.Valid {
		r.FirstDispatchAt = sql.NullTime{Time: now.UTC(), Valid: true}
	}
	if !r.IdempotencyKey.Valid {
		r.IdempotencyKey = sql.NullString{String: key, Valid: true}
	}
	r.dispatches++
	return nil
}

// UpdateSent is the inline send's sent mark (no lease).
func (f *fakeOutbox) UpdateSent(_ context.Context, id int) error {
	if err := f.failure("UpdateSent"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.rows[id]; r != nil {
		r.Sent, r.token = true, ""
	}
	return nil
}

// ClearNextRetryAt hands an inline row whose send failed to the worker.
func (f *fakeOutbox) ClearNextRetryAt(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.rows[id]; r != nil && !r.Sent {
		r.NextRetryAt.Valid = false
	}
	return nil
}

// age moves row id's clock: its creation and first dispatch happened d earlier, and its inline hold
// is over.
func (f *fakeOutbox) age(id int, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.CreatedAt = r.CreatedAt.Add(-d)
	if r.FirstDispatchAt.Valid {
		r.FirstDispatchAt.Time = r.FirstDispatchAt.Time.Add(-d)
	}
	r.NextRetryAt.Valid = false
}

func (f *fakeOutbox) MailOwnersDue(_ context.Context, limit int) ([]entity.MailOwnerDue, error) {
	if err := f.failure("MailOwnersDue"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int, 0, len(f.rows))
	for id, r := range f.rows {
		if r.ownerDue && r.Sent && r.providerID != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out []entity.MailOwnerDue
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		out = append(out, entity.MailOwnerDue{ID: id, ProviderID: f.rows[id].providerID})
	}
	return out, nil
}

func (f *fakeOutbox) ClearMailOwnerDue(_ context.Context, id int) error {
	if err := f.failure("ClearMailOwnerDue"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.rows[id]; r != nil {
		r.ownerDue = false
	}
	return nil
}

// SettleMail has the store's token gate: a row no longer leased to q.Token is not touched.
func (f *fakeOutbox) SettleMail(_ context.Context, q entity.MailSettle) error {
	if err := f.failure("SettleMail"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[q.ID]
	if r == nil || r.token != q.Token || r.Sent || r.quarantined != "" {
		return entity.ErrMailClaimLost
	}
	switch q.Outcome {
	case entity.MailSent:
		r.Sent, r.providerID, r.ownerDue = true, q.ProviderID, q.OwnerPending
	case entity.MailRetry:
		r.SendAttemptCount++
		r.NextRetryAt = sql.NullTime{Time: q.NextRetryAt, Valid: true}
	case entity.MailDead:
		r.dead, r.SendAttemptCount = true, q.MaxAttempts
		r.NextRetryAt.Valid = false
	case entity.MailQuarantined:
		r.quarantined = q.ErrMsg
		r.NextRetryAt.Valid = false
	}
	if q.ErrMsg != "" {
		r.ErrMsg = sql.NullString{String: q.ErrMsg, Valid: true}
	}
	r.token = ""
	return nil
}

func (f *fakeOutbox) QuarantineOutside(_ context.Context, rule entity.MailCutover) (int64, error) {
	if err := f.failure("QuarantineOutside"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutovers = append(f.cutovers, rule)
	var n int64
	for _, r := range f.rows {
		if r.Sent || r.quarantined != "" || (r.NextRetryAt.Valid && r.NextRetryAt.Time.After(rule.Now)) {
			continue
		}
		if !r.Kind.Valid || !slices.Contains(rule.Kinds, r.Kind.String) ||
			(r.ExpiresAt.Valid && !r.ExpiresAt.Time.After(rule.Now)) ||
			r.CreatedAt.Unix() < rule.OpenedAt.Unix() {
			r.quarantined = rule.Reason
			n++
		}
	}
	return n, nil
}

// sentMail is one provider call the fake sender saw.
type sentMail struct {
	req resend.SendEmailRequest
	key string // Idempotency-Key header
}

// fakeSender answers PostEmails from a script (status codes, or an error) and records each call
// with the headers its request editors set.
type fakeSender struct {
	mu     sync.Mutex
	calls  []sentMail
	script []fakeAnswer // consumed in order; empty = 200
	onPost func(ctx context.Context)
}

type fakeAnswer struct {
	status     int
	retryAfter string
	err        error
}

func (s *fakeSender) PostEmails(ctx context.Context, body resend.SendEmailRequest, eds ...resend.RequestEditorFn) (*http.Response, error) {
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.test/emails", nil)
	for _, ed := range eds {
		if err := ed(ctx, hreq); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, sentMail{req: body, key: hreq.Header.Get("Idempotency-Key")})
	a := fakeAnswer{status: http.StatusOK}
	if len(s.script) > 0 {
		a, s.script = s.script[0], s.script[1:]
	}
	onPost := s.onPost
	s.mu.Unlock()
	if onPost != nil {
		onPost(ctx)
	}
	if a.err != nil {
		return nil, a.err
	}
	h := http.Header{}
	if a.retryAfter != "" {
		h.Set("Retry-After", a.retryAfter)
	}
	return &http.Response{StatusCode: a.status, Header: h, Body: io.NopCloser(strings.NewReader(`{"id":"x"}`))}, nil
}

func (s *fakeSender) PostEmailsBatch(context.Context, resend.PostEmailsBatchJSONRequestBody, ...resend.RequestEditorFn) (*http.Response, error) {
	panic("not used")
}

func (s *fakeSender) sent() []sentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// fakeRepo is a repository that only answers Mail().
type fakeRepo struct {
	dependency.Repository
	mail dependency.Mail
}

func (r fakeRepo) Mail() dependency.Mail { return r.mail }
