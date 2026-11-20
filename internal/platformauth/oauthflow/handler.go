package oauthflow

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// The routes, under the /api/auth/oauth mount (teams O §3):
//
//	POST /{provider}/start   form: target=signin|invite|link [, invite_token, terms_accepted, ticket]
//	GET  /google/callback    Google's redirect (values in the query)
//	POST /apple/callback     Apple's form_post (values in the body)
//
// A sign-in or invite callback always ends in a 303 to https://<PLATFORM_BASE_DOMAIN>/auth/oauth with
// #code=<one-time sign-in code> or #error=<key>; there is no return-URL parameter. A Google connect
// from the profile (target=link, Google only, P0 §3.7) ends on the profile of the host its ticket was
// issued on — https://<slug>.<base>/profile, or https://<base>/profile for a root token — with
// ?google=<key>: linked, taken, already_linked, unverified, cancelled, expired or try_again.
const (
	// CookieName is the browser binder of a started flow: 32 random bytes, the state row keeps only
	// their sha256.
	CookieName = "__Host-lekalo_oauth"
	// ResultPath is the one root page every sign-in and invite callback lands on.
	ResultPath = "/auth/oauth"
	// ProfilePath is the page a Google connect returns to, on the host its ticket names.
	ProfilePath = "/profile"

	maxBodyBytes = 16 << 10
	// perMinute is the strict limit of the two steps that cost something: a start writing a state
	// row (per IP + canonical origin) and a callback calling the provider's token endpoint (per IP).
	perMinute = 20
	// guardPerMinute is the loose per-IP guard in front of both routes, before the form, the origin
	// or the state table is touched. Three times the strict limit: an office behind one address keeps
	// headroom, junk from it reaches the state table at most once a second, and a callback without a
	// live state and its cookie never counts against the strict limit (it fails before the exchange).
	guardPerMinute = 3 * perMinute
	maxStateLen    = 128
	maxCodeLen     = 2048
	maxTokenLen    = 128
)

// The fixed #error= keys (teams O §3). platformauth's completion answers with the same strings.
const (
	KeyCancelled      = "cancelled"
	KeyExpired        = "expired"
	KeyUnverified     = "unverified"
	KeyInviteMismatch = "invite_mismatch"
	KeyDisabled       = "disabled"
	KeyTryAgain       = "try_again"
	KeyUsePassword    = "use_password"
	// A Google connect's ?google= keys besides cancelled, expired, unverified and try_again
	// (platformauth's link completion answers with the same strings).
	KeyLinked        = "linked"
	KeyTaken         = "taken"
	KeyAlreadyLinked = "already_linked"
)

// linkKey is the ?google= value of a link completion's key; a key outside the set is try_again.
func linkKey(key string) string {
	switch key {
	case KeyLinked, KeyTaken, KeyAlreadyLinked, KeyUnverified, KeyExpired, KeyCancelled, KeyTryAgain:
		return key
	default:
		return KeyTryAgain
	}
}

// outcome is why a callback did not end in a code. key is the one table from outcome to #error=.
type outcome uint8

const (
	outStateInvalid        outcome = iota + 1 // no state, or its row is unknown, spent, expired or another provider's
	outBinderMismatch                         // no binder cookie, or not this flow's
	outCancelled                              // the person said no at the provider
	outTokenInvalid                           // the code or the id_token did not verify
	outProviderUnreachable                    // the provider failed or answered an error
	outUnverified                             // the provider's email cannot back a link or a new account
	outInviteMismatch                         // the provider does not vouch for the invited email
	outInviteGone                             // the invite is no longer live
	outDisabled                               // the account is disabled
	outUsePassword                            // an operator's account: password only
	outLimited                                // the per-IP limiter
	outInternal                               // anything else
)

func (o outcome) key() string {
	switch o {
	case outStateInvalid, outBinderMismatch, outInviteGone:
		return KeyExpired
	case outCancelled:
		return KeyCancelled
	case outUnverified:
		return KeyUnverified
	case outInviteMismatch:
		return KeyInviteMismatch
	case outDisabled:
		return KeyDisabled
	case outUsePassword:
		return KeyUsePassword
	default: // outTokenInvalid, outProviderUnreachable, outLimited, outInternal
		return KeyTryAgain
	}
}

// completionOutcome reads a completion's refusal key; a key outside the table is internal.
func completionOutcome(key string) outcome {
	switch key {
	case KeyUnverified:
		return outUnverified
	case KeyInviteMismatch:
		return outInviteMismatch
	case KeyExpired:
		return outInviteGone
	case KeyDisabled:
		return outDisabled
	case KeyUsePassword:
		return outUsePassword
	default:
		return outInternal
	}
}

// providerOutcome reads an oauth error (CallbackError, Exchange).
func providerOutcome(err error) outcome {
	switch {
	case errors.Is(err, oauth.ErrCancelled):
		return outCancelled
	case errors.Is(err, oauth.ErrTokenInvalid):
		return outTokenInvalid
	default:
		return outProviderUnreachable
	}
}

// Flow is the identity service's side of a provider flow (platformauth.Service).
type Flow interface {
	// OAuthInvite: the live invite of an invite token — its hash, its team, whether its email has an
	// account. platformstore.ErrTokenInvalid when there is none.
	OAuthInvite(ctx context.Context, token string) (inviteHash string, team tenant.Team, accountExists bool, err error)
	TermsVersion() string
	CreateOAuthState(ctx context.Context, st platformstore.OAuthState) error
	ConsumeOAuthState(ctx context.Context, stateHash string) (platformstore.OAuthState, error)
	// The completions: exactly one of code and key is non-empty.
	CompleteProviderSignIn(ctx context.Context, in platformstore.ProviderSignIn) (code, key string)
	AcceptInviteWithProvider(ctx context.Context, inviteHash, termsVersion string, in platformstore.ProviderSignIn) (code, key string)
	// ConsumeLinkTicket spends a Google-connect ticket: the account it connects and the team host it
	// returns to (the zero Team: the root host). platformstore.ErrTokenInvalid when it is not live.
	ConsumeLinkTicket(ctx context.Context, ticket string) (userID int64, team tenant.Team, err error)
	// CompleteGoogleLink connects the verified identity to the account and answers the ?google= key;
	// startedAt is when the flow began.
	CompleteGoogleLink(ctx context.Context, userID int64, startedAt time.Time, in platformstore.ProviderSignIn) (key string)
}

// Origins are the edge's origin rules (tenantbind.Binder): the root host for a sign-in, the
// invite's team host for an invite. CanonicalOrigin is the form those rules compare (the start
// limiter keys on it).
type Origins interface {
	RootOriginAllowed(origin string) bool
	OriginAllowed(origin string, t tenant.Team, legacy bool) bool
	CanonicalOrigin(origin string) string
}

// Providers is the boot-time registry (*oauth.Registry; nil-safe).
type Providers interface {
	Provider(name string) (oauth.Provider, bool)
}

// Deps wire a Handler. Without Flow, Origins, Providers or BaseDomain every route answers 404.
type Deps struct {
	Flow      Flow
	Origins   Origins
	Providers Providers
	// BaseDomain is PLATFORM_BASE_DOMAIN: the result page is https://<BaseDomain>/auth/oauth.
	BaseDomain string
}

// Handler serves the provider routes. It is a chi.Routes, so the router walk sees its routes.
type Handler struct {
	mux           *chi.Mux
	d             Deps
	base          string // BaseDomain, lowercase, no trailing dot
	resultURL     string
	startGuard    *ratelimit.Limiter // per IP, loose: before the form and the origin are read
	startLimit    *ratelimit.Limiter // per IP + canonical origin, strict: before a state row is written
	callbackGuard *ratelimit.Limiter // per IP, loose: before the state table is touched
	exchangeLimit *ratelimit.Limiter // per IP, strict: before the provider's token endpoint
}

// New builds the handler and its limiters (Close stops them).
func New(d Deps) *Handler {
	h := &Handler{
		d:             d,
		startGuard:    ratelimit.NewLimiter(time.Minute, guardPerMinute),
		startLimit:    ratelimit.NewLimiter(time.Minute, perMinute),
		callbackGuard: ratelimit.NewLimiter(time.Minute, guardPerMinute),
		exchangeLimit: ratelimit.NewLimiter(time.Minute, perMinute),
	}
	if base := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.BaseDomain)), "."); base != "" {
		h.base = base
		h.resultURL = "https://" + base + ResultPath
	}
	m := chi.NewRouter()
	m.NotFound(func(w http.ResponseWriter, _ *http.Request) { plain(w, http.StatusNotFound, "not available") })
	m.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		plain(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	m.Post("/{provider}/start", h.start)
	m.Get("/"+oauth.ProviderGoogle+"/callback", h.callback(oauth.ProviderGoogle))
	m.Post("/"+oauth.ProviderApple+"/callback", h.callback(oauth.ProviderApple))
	h.mux = m
	return h
}

// Close stops the limiters (idempotent).
func (h *Handler) Close() {
	h.startGuard.Stop()
	h.startLimit.Stop()
	h.callbackGuard.Stop()
	h.exchangeLimit.Stop()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) Routes() []chi.Route          { return h.mux.Routes() }
func (h *Handler) Middlewares() chi.Middlewares { return h.mux.Middlewares() }
func (h *Handler) Match(rctx *chi.Context, method, path string) bool {
	return h.mux.Match(rctx, method, path)
}

// provider is the enabled provider of name; false when it is not, or the handler is not wired.
func (h *Handler) provider(name string) (oauth.Provider, bool) {
	if h.d.Flow == nil || h.d.Origins == nil || h.d.Providers == nil || h.resultURL == "" {
		return nil, false
	}
	if name != oauth.ProviderGoogle && name != oauth.ProviderApple {
		return nil, false
	}
	p, ok := h.d.Providers.Provider(name)
	return p, ok && p != nil
}

// start: POST /{provider}/start. Client mistakes (no target, wrong origin, terms not ticked)
// answer 4xx; what a person can run into (a dead invite or ticket, the limiter, the store) answers
// the same 303 as a callback (a Google connect's to its profile once its ticket is spent). The order: the loose per-IP guard → the form → the origin
// for the target → the strict limit per IP + canonical origin → the state row. An origin the rules
// refuse never gets a bucket of its own.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p, ok := h.provider(name)
	if !ok {
		plain(w, http.StatusNotFound, "not available")
		return
	}
	ip := middleware.ClientIPFromRequest(r)
	if !h.startGuard.Allow(ip) {
		h.fail(w, r, name, outLimited)
		return
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	form, status := readForm(w, r)
	if status != 0 {
		plain(w, status, http.StatusText(status))
		return
	}
	if origin == "" {
		plain(w, http.StatusForbidden, "origin not allowed")
		return
	}
	ctx := r.Context()
	st := platformstore.OAuthState{Provider: name, Target: form.Get("target")}
	switch st.Target {
	case platformstore.OAuthTargetSignIn:
		if !h.d.Origins.RootOriginAllowed(origin) {
			plain(w, http.StatusForbidden, "origin not allowed")
			return
		}
	case platformstore.OAuthTargetInvite:
		token := strings.TrimSpace(form.Get("invite_token"))
		if token == "" || len(token) > maxTokenLen {
			plain(w, http.StatusBadRequest, "invite_token required")
			return
		}
		hash, team, exists, err := h.d.Flow.OAuthInvite(ctx, token)
		if errors.Is(err, platformstore.ErrTokenInvalid) {
			h.fail(w, r, name, outInviteGone)
			return
		}
		if err != nil {
			slog.Default().ErrorContext(ctx, "oauth: start: invite lookup failed", slog.String("err", err.Error()))
			h.fail(w, r, name, outInternal)
			return
		}
		if !h.d.Origins.OriginAllowed(origin, team, false) {
			plain(w, http.StatusForbidden, "origin not allowed")
			return
		}
		terms, _ := strconv.ParseBool(strings.TrimSpace(form.Get("terms_accepted")))
		if !exists && !terms {
			plain(w, http.StatusBadRequest, "terms_required")
			return
		}
		st.InviteHash = hash
		if terms {
			st.TermsVersion = h.d.Flow.TermsVersion()
		}
	case platformstore.OAuthTargetLink:
		if name != oauth.ProviderGoogle {
			plain(w, http.StatusBadRequest, "link is google only")
			return
		}
		ticket := strings.TrimSpace(form.Get("ticket"))
		if ticket == "" || len(ticket) > maxTokenLen {
			plain(w, http.StatusBadRequest, "ticket required")
			return
		}
		// The ticket is spent before the origin check: it is single use either way, and a page on a
		// foreign origin holding a live ticket gets nothing from it.
		uid, team, err := h.d.Flow.ConsumeLinkTicket(ctx, ticket)
		if errors.Is(err, platformstore.ErrTokenInvalid) {
			h.fail(w, r, name, outStateInvalid)
			return
		}
		if err != nil {
			slog.Default().ErrorContext(ctx, "oauth: start: link ticket failed", slog.String("err", err.Error()))
			h.fail(w, r, name, outInternal)
			return
		}
		allowed := h.d.Origins.RootOriginAllowed(origin)
		if team.ID > 0 {
			allowed = h.d.Origins.OriginAllowed(origin, team, false)
		}
		if !allowed {
			plain(w, http.StatusForbidden, "origin not allowed")
			return
		}
		st.LinkUserID, st.ReturnSlug = uid, team.Slug
	default:
		plain(w, http.StatusBadRequest, "target must be signin, invite or link")
		return
	}
	// The origin passed its rule: the canonical form (the binder's) keys the bucket, so case
	// variants of one origin share it.
	if !h.startLimit.Allow(ip + "|" + h.d.Origins.CanonicalOrigin(origin)) {
		h.end(w, r, name, st, outLimited)
		return
	}
	state, err1 := oauth.NewState()
	nonce, err2 := oauth.NewNonce()
	verifier, err3 := oauth.NewVerifier()
	binder, err4 := oauth.NewState()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		slog.Default().ErrorContext(ctx, "oauth: start: no randomness", slog.String("err", err.Error()))
		h.end(w, r, name, st, outInternal)
		return
	}
	st.StateHash, st.BinderHash, st.PKCEVerifier, st.Nonce = hashHex(state), hashHex(binder), verifier, nonce
	if err := h.d.Flow.CreateOAuthState(ctx, st); err != nil {
		slog.Default().ErrorContext(ctx, "oauth: start: state not stored", slog.String("provider", name))
		h.end(w, r, name, st, outInternal)
		return
	}
	http.SetCookie(w, binderCookie(binder, int(platformstore.OAuthStateTTL/time.Second)))
	seeOther(w, p.AuthURL(state, nonce, oauth.Challenge(verifier)))
}

// callback: GET /google/callback (query) or POST /apple/callback (form_post body). The order is
// fixed: the loose per-IP guard → read state → spend the row → binder cookie (constant time) →
// provider error → the strict per-IP limit → Exchange with the row's verifier and nonce →
// completion → 303 to the result page (a Google connect's: to its profile, from the spent row on). The
// strict limit sits right before the outbound call it protects, so junk callbacks (which never get
// that far) cannot spend a person's flow.
func (h *Handler) callback(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.provider(name)
		if !ok {
			plain(w, http.StatusNotFound, "not available")
			return
		}
		ip := middleware.ClientIPFromRequest(r)
		if !h.callbackGuard.Allow(ip) {
			h.fail(w, r, name, outLimited)
			return
		}
		var values url.Values
		if name == oauth.ProviderApple {
			form, status := readForm(w, r)
			if status != 0 {
				h.fail(w, r, name, outInternal)
				return
			}
			values = form // r.PostForm only; Apple's user field is never read
		} else {
			values = r.URL.Query()
		}
		ctx := r.Context()
		state := values.Get("state")
		if state == "" || len(state) > maxStateLen {
			h.fail(w, r, name, outStateInvalid)
			return
		}
		row, err := h.d.Flow.ConsumeOAuthState(ctx, hashHex(state))
		if errors.Is(err, platformstore.ErrOAuthStateInvalid) {
			h.fail(w, r, name, outStateInvalid)
			return
		}
		if err != nil {
			slog.Default().ErrorContext(ctx, "oauth: callback: state not read", slog.String("provider", name))
			h.fail(w, r, name, outInternal)
			return
		}
		if row.Provider != name {
			h.end(w, r, name, row, outStateInvalid)
			return
		}
		// A missing or foreign cookie leaves the cookie alone: it may be a later tab's live flow.
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" || len(c.Value) > maxStateLen ||
			subtle.ConstantTimeCompare([]byte(hashHex(c.Value)), []byte(row.BinderHash)) != 1 {
			h.end(w, r, name, row, outBinderMismatch)
			return
		}
		http.SetCookie(w, binderCookie("", -1))
		if err := oauth.CallbackError(values); err != nil {
			h.end(w, r, name, row, providerOutcome(err))
			return
		}
		code := values.Get("code")
		if code == "" || len(code) > maxCodeLen {
			h.end(w, r, name, row, outTokenInvalid)
			return
		}
		if !h.exchangeLimit.Allow(ip) {
			h.end(w, r, name, row, outLimited)
			return
		}
		id, err := p.Exchange(ctx, code, row.PKCEVerifier, row.Nonce)
		if err != nil {
			h.end(w, r, name, row, providerOutcome(err))
			return
		}
		in := platformstore.ProviderSignIn{
			Provider:           name,
			Subject:            id.Subject,
			Email:              id.Email,
			EmailVerified:      id.EmailVerified,
			EmailAuthoritative: id.EmailAuthoritative,
			PrivateEmail:       id.PrivateEmail,
		}
		var signInCode, key string
		switch row.Target {
		case platformstore.OAuthTargetSignIn:
			signInCode, key = h.d.Flow.CompleteProviderSignIn(ctx, in)
		case platformstore.OAuthTargetInvite:
			signInCode, key = h.d.Flow.AcceptInviteWithProvider(ctx, row.InviteHash, row.TermsVersion, in)
		case platformstore.OAuthTargetLink:
			if row.LinkUserID <= 0 || name != oauth.ProviderGoogle {
				h.end(w, r, name, row, outInternal)
				return
			}
			// The flow began OAuthStateTTL before its row expires: a password change since ends it.
			key = h.d.Flow.CompleteGoogleLink(ctx, row.LinkUserID, row.ExpiresAt.Add(-platformstore.OAuthStateTTL), in)
			seeOther(w, h.profileURL(row.ReturnSlug)+"?google="+linkKey(key))
			return
		default:
			key = KeyTryAgain
		}
		if key != "" || signInCode == "" {
			h.fail(w, r, name, completionOutcome(key))
			return
		}
		seeOther(w, h.resultURL+"#code="+url.QueryEscape(signInCode))
	}
}

// fail ends a flow on the result page with its key. An unreachable provider is the one outcome an
// operator hears about: one line, provider and kind, nothing of the flow.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, provider string, o outcome) {
	note(r, provider, o)
	seeOther(w, h.resultURL+"#error="+o.key())
}

// end ends a flow whose target is known: a Google connect back on its profile with ?google=<key>,
// anything else on the result page (fail).
func (h *Handler) end(w http.ResponseWriter, r *http.Request, provider string, st platformstore.OAuthState, o outcome) {
	if st.Target != platformstore.OAuthTargetLink {
		h.fail(w, r, provider, o)
		return
	}
	note(r, provider, o)
	seeOther(w, h.profileURL(st.ReturnSlug)+"?google="+o.key())
}

// note logs the one outcome an operator hears about.
func note(r *http.Request, provider string, o outcome) {
	if o == outProviderUnreachable {
		slog.Default().ErrorContext(r.Context(), "oauth: provider unreachable",
			slog.String("provider", provider), slog.String("kind", string(oauth.ErrorProviderUnreachable)))
	}
}

// profileURL is the profile a Google connect returns to: the team host of slug, or the root host for
// "" (or a slug that is not one host label; it comes from the ticket row, never from the request).
func (h *Handler) profileURL(slug string) string {
	if hostLabel(slug) {
		return "https://" + slug + "." + h.base + ProfilePath
	}
	return "https://" + h.base + ProfilePath
}

// hostLabel reports whether s is one lowercase DNS label: a-z, 0-9 and inner hyphens, 1–63 long.
func hostLabel(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// seeOther is the 303 every flow step ends in: Location and no body. http.Redirect would echo the
// target into an HTML body on a GET — the sign-in code, or the state and nonce of a provider URL.
func seeOther(w http.ResponseWriter, location string) {
	noStore(w)
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

// readForm parses an urlencoded body capped at 16 KiB and returns its fields (never the query).
// status is 413 over the cap, 400 for a body that does not parse, 0 when it parsed.
func readForm(w http.ResponseWriter, r *http.Request) (url.Values, int) {
	if r.ContentLength > maxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, http.StatusRequestEntityTooLarge
		}
		return nil, http.StatusBadRequest
	}
	return r.PostForm, 0
}

// binderCookie is the flow cookie: __Host- (Secure, Path=/, no Domain), HttpOnly, SameSite=None so
// Apple's cross-site form_post carries it. maxAge -1 clears it.
func binderCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
	}
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func plain(w http.ResponseWriter, status int, msg string) {
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg + "\n"))
}

func hashHex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
