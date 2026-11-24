package mail

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// fakeOutbox is an in-memory send_email_request with the claim semantics of the store: a claim
// leases eligible rows (next_retry_at = lease end, claim token set), release hands them back without
// an attempt, a retry counts one, quarantine takes a row out for good.
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

func (f *fakeOutbox) AddMail(_ context.Context, ser *entity.SendEmailRequest) (int, error) {
	if err := f.failure("AddMail"); err != nil {
		return 0, err
	}
	return f.put(*ser), nil
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

func (f *fakeOutbox) UpdateSent(_ context.Context, id int) error {
	if err := f.failure("UpdateSent"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.Sent, r.token = true, ""
	return nil
}

func (f *fakeOutbox) ScheduleSendRetry(_ context.Context, id int, errMsg string, next time.Time) error {
	if err := f.failure("ScheduleSendRetry"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.SendAttemptCount++
	r.ErrMsg.String, r.ErrMsg.Valid = errMsg, true
	r.NextRetryAt.Time, r.NextRetryAt.Valid = next, true
	r.token = ""
	return nil
}

func (f *fakeOutbox) MarkSendDead(_ context.Context, id int, errMsg string, maxAttempts int) error {
	if err := f.failure("MarkSendDead"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.dead, r.token = true, ""
	r.SendAttemptCount = maxAttempts
	r.ErrMsg.String, r.ErrMsg.Valid = errMsg, true
	return nil
}

func (f *fakeOutbox) QuarantineMail(_ context.Context, id int, reason string) error {
	if err := f.failure("QuarantineMail"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.quarantined, r.token = reason, ""
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
