// Package platformmail sends the platform's own letters (verify, reset, invite) from the
// control-plane outbox platform_email through Resend, and blanks codes and links in the outbox once
// their letter has expired (02-DESIGN §5).
//
// A row is written by the identity code inside the transaction that creates its token; this worker
// only reads it. With PLATFORM_MAILER_ENABLED=false a row is marked skipped and its payload stays
// (tests and the golden case read the code from it while it is valid). With the mailer on but its
// key or from-address missing, rows stay pending until the settings are fixed.
//
// A send never runs inside a transaction: the row is leased (lease_until) and its attempt counted in
// a short commit, Resend is called, then the outcome is written only if the lease is still ours. A
// crash mid-send leaves the lease to expire and the row is sent again under the same idempotency
// key. The retry time lives in the row (next_attempt_at), so restarts and replicas share it.
package platformmail

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/jmoiron/sqlx"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

const (
	defaultInterval = 5 * time.Second
	batchSize       = 20
	maxAttempts     = 5
	retryBase       = 30 * time.Second
	retryMax        = 10 * time.Minute
	sweepEvery      = time.Hour
	sendTimeout     = 20 * time.Second
	leaseFor        = time.Minute // > sendTimeout: a live send never loses its lease
	resendBaseURL   = "https://api.resend.com/"
	maxErrorLen     = 500
)

// Message is one rendered letter.
type Message struct {
	From, To, Subject, HTML, Text string
}

// Sender delivers a message. idempotencyKey makes a retried send of the same row a no-op upstream.
type Sender interface {
	Send(ctx context.Context, m Message, idempotencyKey string) error
}

// PermanentError is a send failure retrying cannot fix (a 4xx other than 408/429).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Config is the worker's view of the platform settings.
type Config struct {
	Enabled    bool   // PLATFORM_MAILER_ENABLED
	From       string // PLATFORM_MAIL_FROM, e.g. "lekalo <no-reply@lekalo.studio>"
	BaseDomain string // PLATFORM_BASE_DOMAIN: the letters' footer and address line
}

// ErrNotConfigured: the mailer is on but has no sender or from-address; rows stay pending.
var ErrNotConfigured = errors.New("platformmail: PLATFORM_MAILER_ENABLED is on but PLATFORM_MAIL_FROM or the Resend key is missing; letters wait")

// Worker drains platform_email.
type Worker struct {
	db       *sqlx.DB
	sender   Sender // nil with the mailer on: rows wait (ErrNotConfigured)
	cfg      Config
	tpl      *templates
	now      func() time.Time
	interval time.Duration
}

// New builds a worker. sender may be nil when no Resend key is configured.
func New(db *sqlx.DB, sender Sender, cfg Config) (*Worker, error) {
	if db == nil {
		return nil, errors.New("platformmail: db is required")
	}
	tpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	cfg.BaseDomain = strings.ToLower(strings.Trim(strings.TrimSpace(cfg.BaseDomain), "./"))
	cfg.From = strings.TrimSpace(cfg.From)
	return &Worker{db: db, sender: sender, cfg: cfg, tpl: tpl, now: time.Now, interval: defaultInterval}, nil
}

// SetClock replaces the clock (tests).
func (w *Worker) SetClock(now func() time.Time) { w.now = now }

func (w *Worker) clock() time.Time { return w.now().UTC().Truncate(time.Millisecond) }

// Start runs the loop until stop is called; stop waits for the loop to finish its current row.
func (w *Worker) Start(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.loop(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (w *Worker) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	lastSweep := time.Time{}
	for {
		if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Default().ErrorContext(ctx, "platformmail: tick", slog.String("err", err.Error()))
		}
		if now := w.clock(); now.Sub(lastSweep) >= sweepEvery {
			if n, err := w.Sweep(ctx); err != nil {
				if ctx.Err() == nil {
					slog.Default().ErrorContext(ctx, "platformmail: sweep", slog.String("err", err.Error()))
				}
			} else {
				lastSweep = now
				if n > 0 {
					slog.Default().InfoContext(ctx, "platformmail: blanked expired codes and links", slog.Int64("rows", n))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Worker) canSend() bool { return w.cfg.Enabled && w.sender != nil && w.cfg.From != "" }

type outboxRow struct {
	ID       int64  `db:"id"`
	To       string `db:"to_email"`
	Template string `db:"template"`
	Payload  []byte `db:"payload"`
	Attempts int    `db:"attempts"`
}

// Payload is a platform_email payload (written by platformstore).
type Payload struct {
	Link     string `json:"link"`
	Code     string `json:"code"`
	TeamName string `json:"team_name"`
	Slug     string `json:"slug"`
	From     string `json:"from"`
	Email    string `json:"email"` // team_request: who asked for the team
}

// Tick handles up to one batch of due pending rows (FOR UPDATE SKIP LOCKED plus the lease: two
// instances never send the same row at once). It returns how many rows changed status. With the
// mailer on but unconfigured it touches nothing and answers ErrNotConfigured (the loop logs it).
func (w *Worker) Tick(ctx context.Context) (int, error) {
	if w.cfg.Enabled && !w.canSend() {
		return 0, ErrNotConfigured
	}
	var lastID int64
	done := 0
	for range batchSize {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		id, changed, err := w.one(ctx, lastID)
		if err != nil {
			return done, err
		}
		if id == 0 {
			return done, nil
		}
		lastID = id
		if changed {
			done++
		}
	}
	return done, nil
}

// one claims the next due pending row after afterID and acts on it. id 0 = nothing left.
func (w *Worker) one(ctx context.Context, afterID int64) (id int64, changed bool, err error) {
	now := w.clock()
	r, m, lease, changed, err := w.claim(ctx, afterID, now)
	if err != nil || r.ID == 0 || changed {
		return r.ID, changed, err
	}
	// The row is leased and committed: no transaction or row lock is held during the call.
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	serr := w.sender.Send(sctx, m, fmt.Sprintf("platform-email-%d", r.ID))
	cancel()
	attempts := r.Attempts + 1
	done := w.clock()
	var perm *PermanentError
	switch {
	case serr == nil:
		return r.ID, true, w.finish(ctx, r.ID, lease, `status = 'sent', sent_at = ?, last_error = NULL`, done)
	case errors.As(serr, &perm) || attempts >= maxAttempts:
		slog.Default().ErrorContext(ctx, "platformmail: letter failed", slog.Int64("id", r.ID), slog.String("template", r.Template),
			slog.String("err", truncate(serr.Error())))
		return r.ID, true, w.finish(ctx, r.ID, lease, `status = 'failed', last_error = ?`, truncate(serr.Error()))
	default:
		next := done.Add(mail.RetryDelayAfterAttempt(retryBase, retryMax, attempts))
		return r.ID, false, w.finish(ctx, r.ID, lease, `next_attempt_at = ?, last_error = ?`, next, truncate(serr.Error()))
	}
}

// claim locks the next due row in a short transaction. A disabled mailer marks it skipped; a
// payload that cannot be sent marks it failed (changed=true). Otherwise it counts the attempt,
// leases the row until lease and commits, returning the rendered message.
func (w *Worker) claim(ctx context.Context, afterID int64, now time.Time) (r outboxRow, m Message, lease time.Time, changed bool, err error) {
	tx, err := w.db.BeginTxx(ctx, nil)
	if err != nil {
		return r, m, lease, false, fmt.Errorf("platformmail: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.GetContext(ctx, &r, `SELECT id, to_email, template, payload, attempts FROM platform_email
WHERE status = 'pending' AND id > ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?) AND (lease_until IS NULL OR lease_until <= ?)
ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`, afterID, now, now); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return outboxRow{}, m, lease, false, nil
		}
		return outboxRow{}, m, lease, false, fmt.Errorf("platformmail: claim: %w", err)
	}
	set := func(q string, args ...any) error {
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("platformmail: update row: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("platformmail: commit: %w", err)
		}
		return nil
	}
	if !w.cfg.Enabled {
		return r, m, lease, true, set(`UPDATE platform_email SET status = 'skipped' WHERE id = ?`, r.ID)
	}
	var p Payload
	if err := json.Unmarshal(r.Payload, &p); err != nil || p.Link == "" {
		return r, m, lease, true, set(`UPDATE platform_email SET status = 'failed', last_error = ?, lease_until = NULL WHERE id = ?`,
			"expired before send", r.ID)
	}
	m, err = w.tpl.render(r.Template, p, w.cfg.BaseDomain)
	if err != nil {
		return r, m, lease, true, set(`UPDATE platform_email SET status = 'failed', last_error = ?, lease_until = NULL WHERE id = ?`,
			truncate(err.Error()), r.ID)
	}
	m.From, m.To = w.cfg.From, r.To
	lease = now.Add(leaseFor)
	return r, m, lease, false, set(`UPDATE platform_email SET attempts = attempts + 1, lease_until = ? WHERE id = ?`, lease, r.ID)
}

// finish writes a send's outcome and drops the lease, only while the lease is still ours.
func (w *Worker) finish(ctx context.Context, id int64, lease time.Time, assign string, args ...any) error {
	args = append(args, id, lease)
	r, err := w.db.ExecContext(ctx, `UPDATE platform_email SET `+assign+`, lease_until = NULL
WHERE id = ? AND status = 'pending' AND lease_until = ?`, args...)
	if err != nil {
		return fmt.Errorf("platformmail: finish row: %w", err)
	}
	if n, _ := r.RowsAffected(); n == 0 {
		slog.Default().WarnContext(ctx, "platformmail: lease lost before the outcome was written", slog.Int64("id", id))
	}
	return nil
}

// Sweep blanks code and link in payloads whose letter has expired: verify after 24 h, reset after
// 1 h, invite after 7 days. No live secret sits in plain text past its expiry.
func (w *Worker) Sweep(ctx context.Context) (int64, error) {
	now := w.clock()
	r, err := w.db.ExecContext(ctx, `UPDATE platform_email SET payload = JSON_REMOVE(payload, '$.code', '$.link')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.code', '$.link')
  AND ((template = 'verify' AND created_at < ?) OR (template = 'reset' AND created_at < ?) OR (template = 'invite' AND created_at < ?))`,
		now.Add(-24*time.Hour), now.Add(-time.Hour), now.Add(-7*24*time.Hour))
	if err != nil {
		return 0, fmt.Errorf("platformmail: sweep: %w", err)
	}
	return r.RowsAffected()
}

func truncate(s string) string {
	if len(s) > maxErrorLen {
		return s[:maxErrorLen]
	}
	return s
}

// --- templates ---

type templates struct {
	html map[string]*htmltemplate.Template
	text map[string]*texttemplate.Template
}

// Kinds of letter (platform_email.template).
const (
	KindVerify = "verify"
	KindReset  = "reset"
	KindInvite = "invite"
	// KindTeamRequest goes to each operator when a confirmed sign-up waits for approval.
	KindTeamRequest = "team_request"
	// KindTeamApproved goes to the team's owner when an operator approves it.
	KindTeamApproved = "team_approved"
)

type buttonData struct{ Link, Label string }

type letterData struct {
	Subject, Address, Code, Link, Team, From, Base, Email string
}

func parseTemplates() (*templates, error) {
	t := &templates{html: map[string]*htmltemplate.Template{}, text: map[string]*texttemplate.Template{}}
	funcs := htmltemplate.FuncMap{"button": func(link, label string) buttonData { return buttonData{link, label} }}
	for _, k := range []string{KindVerify, KindReset, KindInvite, KindTeamRequest, KindTeamApproved} {
		h, err := htmltemplate.New("layout").Funcs(funcs).ParseFS(templateFS, "templates/layout.html.gohtml", "templates/"+k+".html.gohtml")
		if err != nil {
			return nil, fmt.Errorf("platformmail: parse %s html: %w", k, err)
		}
		x, err := texttemplate.New(k+".txt.gohtml").ParseFS(templateFS, "templates/"+k+".txt.gohtml")
		if err != nil {
			return nil, fmt.Errorf("platformmail: parse %s text: %w", k, err)
		}
		t.html[k], t.text[k] = h, x
	}
	return t, nil
}

// spaced renders a six-digit code as the letter shows it: "482 913".
func spaced(code string) string {
	if len(code) != 6 {
		return code
	}
	return code[:3] + " " + code[3:]
}

func (t *templates) render(kind string, p Payload, base string) (Message, error) {
	h, ok := t.html[kind]
	if !ok {
		return Message{}, fmt.Errorf("platformmail: unknown template %q", kind)
	}
	d := letterData{Code: spaced(p.Code), Link: p.Link, Team: p.TeamName, From: p.From, Base: base, Email: p.Email}
	if p.Slug != "" && base != "" {
		d.Address = p.Slug + "." + base
	}
	switch kind {
	case KindVerify:
		if p.Code == "" {
			return Message{}, errors.New("platformmail: verify letter without a code")
		}
		d.Subject = d.Code + " · confirm email"
	case KindReset:
		if p.Code == "" {
			return Message{}, errors.New("platformmail: reset letter without a code")
		}
		d.Subject = d.Code + " · reset password"
	case KindInvite:
		if p.TeamName == "" {
			return Message{}, errors.New("platformmail: invite letter without a team")
		}
		d.Subject = "join " + p.TeamName
	case KindTeamRequest:
		if p.TeamName == "" || p.Slug == "" {
			return Message{}, errors.New("platformmail: team request letter without a team")
		}
		d.Subject = "new team · " + p.TeamName
	case KindTeamApproved:
		if p.TeamName == "" {
			return Message{}, errors.New("platformmail: team approved letter without a team")
		}
		d.Subject = p.TeamName + " · approved"
	}
	var hb, tb bytes.Buffer
	if err := h.ExecuteTemplate(&hb, "layout", d); err != nil {
		return Message{}, fmt.Errorf("platformmail: render %s html: %w", kind, err)
	}
	if err := t.text[kind].Execute(&tb, d); err != nil {
		return Message{}, fmt.Errorf("platformmail: render %s text: %w", kind, err)
	}
	return Message{Subject: d.Subject, HTML: hb.String(), Text: tb.String()}, nil
}

// --- Resend ---

type resendSender struct{ cli *resend.Client }

// NewResendSender builds a Sender on the generated Resend client with this API key.
func NewResendSender(apiKey string) (Sender, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("platformmail: no Resend API key")
	}
	cli, err := resend.NewClient(resendBaseURL,
		resend.WithHTTPClient(&http.Client{Timeout: sendTimeout}),
		resend.ClientOption(func(rc *resend.Client) error {
			rc.RequestEditors = append(rc.RequestEditors, func(_ context.Context, r *http.Request) error {
				r.Header.Set("Authorization", "Bearer "+apiKey)
				return nil
			})
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("platformmail: resend client: %w", err)
	}
	return &resendSender{cli: cli}, nil
}

func (s *resendSender) Send(ctx context.Context, m Message, key string) error {
	html, text := m.HTML, m.Text
	resp, err := s.cli.PostEmails(ctx, resend.SendEmailRequest{From: m.From, To: []string{m.To}, Subject: m.Subject,
		Html: &html, Text: &text}, func(_ context.Context, r *http.Request) error {
		r.Header.Set("Idempotency-Key", key)
		return nil
	})
	if err != nil {
		return fmt.Errorf("platformmail: send: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	herr := &mail.HTTPSendError{StatusCode: resp.StatusCode, Body: string(body)}
	if resp.StatusCode == http.StatusTooManyRequests || mail.IsTransientSendFailure(herr) {
		return herr
	}
	return &PermanentError{Err: herr}
}
