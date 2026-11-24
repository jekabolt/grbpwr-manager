package shoppay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	stripeproc "github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MsgPaymentsNotReady is the refusal of a team shop's checkout while its Stripe cannot take this
// payment (frontend.MsgShopPaymentsNotReady, the same word).
const MsgPaymentsNotReady = "shop_payments_not_ready"

// Processor is a team's card processor: the storefront's Invoicer plus the event dispatch of its
// webhooks and the stop of its payment monitors.
type Processor interface {
	dependency.Invoicer
	DispatchEvent(ctx context.Context, ev stripe.Event) int
	StopAllMonitors(ctx context.Context)
}

// ProcessorSpec is what a team's processor is built from.
type ProcessorSpec struct {
	Team      tenant.Team
	Method    entity.PaymentMethodName // card (live) | card-test (test)
	SecretKey string                   // the platform key (Connect) or the team's own key
	Account   string                   // the connected account; "" = own keys
	FeeBps    int
}

// Factory builds a team's processor. ctx carries the team.
type Factory func(ctx context.Context, spec ProcessorSpec) (Processor, error)

// NewStripeFactory builds real processors: the team's payment monitors run under a context that
// carries the team and outlives the request that built them (stopped by the registry).
func NewStripeFactory(rep dependency.Repository, mailer dependency.Mailer, invoiceTTL time.Duration, backends *stripe.Backends) Factory {
	return func(ctx context.Context, spec ProcessorSpec) (Processor, error) {
		p, err := stripeproc.NewTeam(ctx, &stripeproc.Config{Enabled: true, SecretKey: spec.SecretKey, InvoiceExpiration: invoiceTTL},
			rep, mailer, spec.Method, stripeproc.TeamOptions{
				Account:     spec.Account,
				FeeBps:      spec.FeeBps,
				MonitorBase: tenant.With(context.Background(), spec.Team),
				Backends:    backends,
			})
		if err != nil {
			return nil, err
		}
		return p, nil
	}
}

// methodFor is the one payment method a Stripe mode charges as.
func methodFor(mode string) entity.PaymentMethodName {
	if mode == ModeLive {
		return entity.CARD
	}
	return entity.CARD_TEST
}

type built struct {
	fp   string
	proc Processor
}

// Registry keeps one processor per team, built on first use and rebuilt when the team's Stripe
// changes (another account, other keys, another fee). It never serves the storefront team.
type Registry struct {
	st      Store
	teams   Teams
	cfg     Config
	keys    *keys
	factory Factory

	mu      sync.Mutex
	procs   map[int64]*built
	locks   map[int64]*sync.Mutex
	stopped bool
	warm    sync.WaitGroup
	cancel  context.CancelFunc
}

func newRegistry(st Store, teams Teams, cfg Config, k *keys, f Factory) *Registry {
	return &Registry{st: st, teams: teams, cfg: cfg, keys: k, factory: f, procs: make(map[int64]*built), locks: make(map[int64]*sync.Mutex)}
}

func notReady() error { return status.Error(codes.FailedPrecondition, MsgPaymentsNotReady) }

// For is the invoicer of team t's checkout in payment method pm: FailedPrecondition
// shop_payments_not_ready while the team has no live Stripe, or its mode charges as the other card
// method; Unimplemented for a method that is not a card.
func (r *Registry) For(ctx context.Context, t tenant.Team, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	if pm != entity.CARD && pm != entity.CARD_TEST {
		return nil, status.Error(codes.Unimplemented, "payment method unimplemented")
	}
	row, ok, err := r.st.TeamStripe(ctx, t.ID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: team stripe", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "payments")
	}
	if !ok || row.DisconnectedAt != nil || methodFor(row.Mode) != pm {
		return nil, notReady()
	}
	p, err := r.processor(ctx, t, row)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// processor is team t's processor for its live row (built or rebuilt as needed).
func (r *Registry) processor(ctx context.Context, t tenant.Team, row platformstore.TeamStripe) (Processor, error) {
	fp := fingerprint(r.cfg, row)
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil, status.Error(codes.Unavailable, "payments stopping")
	}
	if b := r.procs[t.ID]; b != nil && b.fp == fp {
		r.mu.Unlock()
		return b.proc, nil
	}
	l := r.locks[t.ID]
	if l == nil {
		l = &sync.Mutex{}
		r.locks[t.ID] = l
	}
	r.mu.Unlock()

	l.Lock()
	defer l.Unlock()
	r.mu.Lock()
	if b := r.procs[t.ID]; b != nil && b.fp == fp {
		r.mu.Unlock()
		return b.proc, nil
	}
	r.mu.Unlock()

	spec, err := r.spec(t, row)
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: team stripe unusable", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		return nil, notReady()
	}
	p, err := r.factory(tenant.With(ctx, t), spec)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: build processor", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Unavailable, "payments")
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		stopProc(ctx, p)
		return nil, status.Error(codes.Unavailable, "payments stopping")
	}
	old := r.procs[t.ID]
	r.procs[t.ID] = &built{fp: fp, proc: p}
	r.mu.Unlock()
	if old != nil {
		stopProc(ctx, old.proc)
	}
	return p, nil
}

// spec turns a live row into what the processor is built from; an error = the row cannot charge
// (Connect off on this installation, another mode than the platform's, a key that does not open).
func (r *Registry) spec(t tenant.Team, row platformstore.TeamStripe) (ProcessorSpec, error) {
	s := ProcessorSpec{Team: t, Method: methodFor(row.Mode)}
	switch row.Kind {
	case platformstore.StripeKindConnect:
		if !r.cfg.ConnectReady() {
			return s, errors.New("connect is not configured")
		}
		if row.AccountID == nil || *row.AccountID == "" {
			return s, errors.New("no connected account")
		}
		if row.Mode != r.cfg.ConnectMode() {
			return s, fmt.Errorf("account mode %s, platform mode %s", row.Mode, r.cfg.ConnectMode())
		}
		s.SecretKey, s.Account, s.FeeBps = r.cfg.ConnectSecretKey, *row.AccountID, row.FeeBps
	case platformstore.StripeKindKeys:
		if r.keys == nil {
			return s, errNoMaster
		}
		sk, err := r.keys.open(t.ID, sealSecretKey, row.SecretKeySealed)
		if err != nil {
			return s, err
		}
		if keyMode(sk) != row.Mode {
			return s, errors.New("key mode does not match the row")
		}
		s.SecretKey = sk
	default:
		return s, fmt.Errorf("unknown kind %q", row.Kind)
	}
	return s, nil
}

// fingerprint changes whenever a live row would build another processor.
func fingerprint(cfg Config, row platformstore.TeamStripe) string {
	h := sha256.New()
	acct := ""
	if row.AccountID != nil {
		acct = *row.AccountID
	}
	h.Write([]byte(row.Kind + "|" + acct + "|" + row.Mode + "|" + strconv.Itoa(row.FeeBps) + "|"))
	if row.Kind == platformstore.StripeKindConnect {
		h.Write([]byte(cfg.ConnectSecretKey))
	} else {
		h.Write(row.SecretKeySealed)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Drop stops and forgets team teamID's processor (its Stripe changed or went away).
func (r *Registry) Drop(ctx context.Context, teamID int64) {
	r.mu.Lock()
	old := r.procs[teamID]
	delete(r.procs, teamID)
	r.mu.Unlock()
	if old != nil {
		stopProc(ctx, old.proc)
	}
}

func stopProc(ctx context.Context, p Processor) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	p.StopAllMonitors(sctx)
}

// usableTeam reads a team the payments may act for: active or suspended (a suspended shop's paid
// orders still complete), schema current.
func (r *Registry) usableTeam(ctx context.Context, id int64) (tenant.Team, error) {
	t, err := r.teams.TeamByID(ctx, id)
	if err != nil {
		return tenant.Team{}, err
	}
	if t.Status != tenant.StatusActive && t.Status != tenant.StatusSuspended {
		return tenant.Team{}, fmt.Errorf("team status %s", t.Status)
	}
	if r.teams.SchemaBehind(t) {
		return tenant.Team{}, errSchemaBehind
	}
	return t, nil
}

var errSchemaBehind = errors.New("shoppay: team schema is being updated")

// start builds every live team's processor in the background, so the payment monitors of unpaid
// orders restart after a deploy without waiting for the next checkout, and syncs the accounts.
func (r *Registry) start(ctx context.Context, sync func(ctx context.Context, t tenant.Team, row platformstore.TeamStripe)) {
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	r.warm.Add(1)
	go func() {
		defer r.warm.Done()
		ids, err := r.st.LiveStripeTeams(wctx, "")
		if err != nil {
			slog.Default().ErrorContext(wctx, "shoppay: live stripe teams", slog.String("err", err.Error()))
			return
		}
		for _, id := range ids {
			if wctx.Err() != nil {
				return
			}
			t, err := r.usableTeam(wctx, id)
			if err != nil {
				slog.Default().WarnContext(wctx, "shoppay: warm skips a team", slog.Int64("team", id), slog.String("err", err.Error()))
				continue
			}
			tctx := tenant.With(wctx, t)
			row, ok, err := r.st.TeamStripe(tctx, id)
			if err != nil || !ok || row.DisconnectedAt != nil {
				continue
			}
			if sync != nil {
				sync(tctx, t, row)
			}
			if _, err := r.processor(tctx, t, row); err != nil {
				slog.Default().WarnContext(wctx, "shoppay: warm a team processor", slog.Int64("team", id), slog.String("err", err.Error()))
			}
		}
	}()
}

// stop ends the warm-up and stops every processor's monitors (before the databases close).
func (r *Registry) stop(ctx context.Context) {
	r.mu.Lock()
	r.stopped = true
	cancel := r.cancel
	procs := r.procs
	r.procs = make(map[int64]*built)
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.warm.Wait()
	for _, b := range procs {
		b.proc.StopAllMonitors(ctx)
	}
}
