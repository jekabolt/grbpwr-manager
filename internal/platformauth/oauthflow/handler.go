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
//	POST /{provider}/start   form: target=signin|invite [, invite_token, terms_accepted]
//	GET  /google/callback    Google's redirect (values in the query)
//	POST /apple/callback     Apple's form_post (values in the body)
//
// A callback always ends in a 303 to https://<PLATFORM_BASE_DOMAIN>/auth/oauth with #code=<one-time
// sign-in code> or #error=<key>; there is no return-URL parameter.
const (
	// CookieName is the browser binder of a started flow: 32 random bytes, the state row keeps only
	// their sha256.
	CookieName = "__Host-lekalo_oauth"
	// ResultPath is the one root page every callback lands on.
	ResultPath = "/auth/oauth"

	maxBodyBytes = 16 << 10
	perMinute    = 20
	maxStateLen  = 128
	maxCodeLen   = 2048
	maxTokenLen  = 128
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
)

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
}

// Origins are the edge's origin rules (tenantbind.Binder): the root host for a sign-in, the
// invite's team host for an invite.
type Origins interface {
	RootOriginAllowed(origin string) bool
	OriginAllowed(origin string, t tenant.Team, legacy bool) bool
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
	resultURL     string
	startLimit    *ratelimit.Limiter // per IP + Origin
	callbackLimit *ratelimit.Limiter // per IP
}

// New builds the handler and its limiters (Close stops them).
func New(d Deps) *Handler {
	h := &Handler{
		d:             d,
		startLimit:    ratelimit.NewLimiter(time.Minute, perMinute),
		callbackLimit: ratelimit.NewLimiter(time.Minute, perMinute),
	}
	if base := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.BaseDomain)), "."); base != "" {
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
	h.startLimit.Stop()
	h.callbackLimit.Stop()
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
// answer 4xx; what a person can run into (a dead invite, the limiter, the store) answers the same
// 303 to the result page as a callback.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	p, ok := h.provider(name)
	if !ok {
		plain(w, http.StatusNotFound, "not available")
		return
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if !h.startLimit.Allow(middleware.ClientIPFromRequest(r) + "|" + origin) {
		h.fail(w, r, name, outLimited)
		return
	}
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
	default:
		plain(w, http.StatusBadRequest, "target must be signin or invite")
		return
	}
	state, err1 := oauth.NewState()
	nonce, err2 := oauth.NewNonce()
	verifier, err3 := oauth.NewVerifier()
	binder, err4 := oauth.NewState()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		slog.Default().ErrorContext(ctx, "oauth: start: no randomness", slog.String("err", err.Error()))
		h.fail(w, r, name, outInternal)
		return
	}
	st.StateHash, st.BinderHash, st.PKCEVerifier, st.Nonce = hashHex(state), hashHex(binder), verifier, nonce
	if err := h.d.Flow.CreateOAuthState(ctx, st); err != nil {
		slog.Default().ErrorContext(ctx, "oauth: start: state not stored", slog.String("provider", name))
		h.fail(w, r, name, outInternal)
		return
	}
	http.SetCookie(w, binderCookie(binder, int(platformstore.OAuthStateTTL/time.Second)))
	noStore(w)
	http.Redirect(w, r, p.AuthURL(state, nonce, oauth.Challenge(verifier)), http.StatusSeeOther)
}

// callback: GET /google/callback (query) or POST /apple/callback (form_post body). The order is
// fixed: read state → spend the row → binder cookie (constant time) → provider error → Exchange
// with the row's verifier and nonce → completion → 303 to the result page.
func (h *Handler) callback(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.provider(name)
		if !ok {
			plain(w, http.StatusNotFound, "not available")
			return
		}
		if !h.callbackLimit.Allow(middleware.ClientIPFromRequest(r)) {
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
			h.fail(w, r, name, outStateInvalid)
			return
		}
		// A missing or foreign cookie leaves the cookie alone: it may be a later tab's live flow.
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" || len(c.Value) > maxStateLen ||
			subtle.ConstantTimeCompare([]byte(hashHex(c.Value)), []byte(row.BinderHash)) != 1 {
			h.fail(w, r, name, outBinderMismatch)
			return
		}
		http.SetCookie(w, binderCookie("", -1))
		if err := oauth.CallbackError(values); err != nil {
			h.fail(w, r, name, providerOutcome(err))
			return
		}
		code := values.Get("code")
		if code == "" || len(code) > maxCodeLen {
			h.fail(w, r, name, outTokenInvalid)
			return
		}
		id, err := p.Exchange(ctx, code, row.PKCEVerifier, row.Nonce)
		if err != nil {
			h.fail(w, r, name, providerOutcome(err))
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
		default:
			key = KeyTryAgain
		}
		if key != "" || signInCode == "" {
			h.fail(w, r, name, completionOutcome(key))
			return
		}
		noStore(w)
		http.Redirect(w, r, h.resultURL+"#code="+url.QueryEscape(signInCode), http.StatusSeeOther)
	}
}

// fail ends a flow on the result page with its key. An unreachable provider is the one outcome an
// operator hears about: one line, provider and kind, nothing of the flow.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, provider string, o outcome) {
	if o == outProviderUnreachable {
		slog.Default().ErrorContext(r.Context(), "oauth: provider unreachable",
			slog.String("provider", provider), slog.String("kind", string(oauth.ErrorProviderUnreachable)))
	}
	noStore(w)
	http.Redirect(w, r, h.resultURL+"#error="+o.key(), http.StatusSeeOther)
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
