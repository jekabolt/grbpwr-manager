// Package platformmail sends the platform's own letters (verify, reset, invite) from the
// control-plane outbox platform_email through Resend, and blanks codes and links in the outbox once
// their letter has expired (02-DESIGN §5).
//
// A row is written by the identity code inside the transaction that creates its token; this worker
// only reads it. With PLATFORM_MAILER_ENABLED=false a row is marked skipped and its payload stays
// (tests and the golden case read the code from it while it is valid).
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

// Worker drains platform_email.
type Worker struct {
	db       *sqlx.DB
	sender   Sender // nil: sending impossible (no API key), rows are skipped
	cfg      Config
	tpl      *templates
	now      func() time.Time
	interval time.Duration
	nextTry  map[int64]time.Time // in-memory backoff per row id
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
	return &Worker{db: db, sender: sender, cfg: cfg, tpl: tpl, now: time.Now, interval: defaultInterval,
		nextTry: map[int64]time.Time{}}, nil
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
	if !w.canSend() && w.cfg.Enabled {
		slog.Default().ErrorContext(ctx, "platformmail: PLATFORM_MAILER_ENABLED is on but PLATFORM_MAIL_FROM or the Resend key is missing; letters are marked skipped")
	}
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
}

// Tick handles up to one batch of pending rows, each in its own transaction (FOR UPDATE SKIP
// LOCKED: two instances never send the same row). It returns how many rows changed status.
func (w *Worker) Tick(ctx context.Context) (int, error) {
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

// one claims the next pending row after afterID and acts on it. id 0 = nothing left.
func (w *Worker) one(ctx context.Context, afterID int64) (id int64, changed bool, err error) {
	tx, err := w.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("platformmail: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var r outboxRow
	if err := tx.GetContext(ctx, &r, `SELECT id, to_email, template, payload, attempts FROM platform_email
WHERE status = 'pending' AND id > ? ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`, afterID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("platformmail: claim: %w", err)
	}
	now := w.clock()
	if t, ok := w.nextTry[r.ID]; ok && now.Before(t) {
		return r.ID, false, nil
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
	if !w.canSend() {
		return r.ID, true, set(`UPDATE platform_email SET status = 'skipped' WHERE id = ?`, r.ID)
	}
	var p Payload
	if err := json.Unmarshal(r.Payload, &p); err != nil || p.Link == "" {
		delete(w.nextTry, r.ID)
		return r.ID, true, set(`UPDATE platform_email SET status = 'failed', last_error = ? WHERE id = ?`,
			"expired before send", r.ID)
	}
	m, err := w.tpl.render(r.Template, p, w.cfg.BaseDomain)
	if err != nil {
		delete(w.nextTry, r.ID)
		return r.ID, true, set(`UPDATE platform_email SET status = 'failed', last_error = ? WHERE id = ?`,
			truncate(err.Error()), r.ID)
	}
	m.From, m.To = w.cfg.From, r.To
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	serr := w.sender.Send(sctx, m, fmt.Sprintf("platform-email-%d", r.ID))
	cancel()
	attempts := r.Attempts + 1
	if serr == nil {
		delete(w.nextTry, r.ID)
		return r.ID, true, set(`UPDATE platform_email SET status = 'sent', attempts = ?, sent_at = ?, last_error = NULL WHERE id = ?`,
			attempts, now, r.ID)
	}
	var perm *PermanentError
	if errors.As(serr, &perm) || attempts >= maxAttempts {
		delete(w.nextTry, r.ID)
		slog.Default().ErrorContext(ctx, "platformmail: letter failed", slog.Int64("id", r.ID), slog.String("template", r.Template),
			slog.String("err", truncate(serr.Error())))
		return r.ID, true, set(`UPDATE platform_email SET status = 'failed', attempts = ?, last_error = ? WHERE id = ?`,
			attempts, truncate(serr.Error()), r.ID)
	}
	w.nextTry[r.ID] = now.Add(mail.RetryDelayAfterAttempt(retryBase, retryMax, attempts))
	return r.ID, false, set(`UPDATE platform_email SET attempts = ?, last_error = ? WHERE id = ?`,
		attempts, truncate(serr.Error()), r.ID)
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
)

type buttonData struct{ Link, Label string }

type letterData struct {
	Subject, Address, Code, Link, Team, From, Base string
}

func parseTemplates() (*templates, error) {
	t := &templates{html: map[string]*htmltemplate.Template{}, text: map[string]*texttemplate.Template{}}
	funcs := htmltemplate.FuncMap{"button": func(link, label string) buttonData { return buttonData{link, label} }}
	for _, k := range []string{KindVerify, KindReset, KindInvite} {
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
	d := letterData{Code: spaced(p.Code), Link: p.Link, Team: p.TeamName, From: p.From, Base: base}
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
