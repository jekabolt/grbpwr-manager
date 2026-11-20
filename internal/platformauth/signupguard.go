package platformauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
)

// Sign-up abuse guard (X6, lanes/X6-design.md): an invisible proof-of-work check (the ALTCHA v1
// protocol), a honeypot, a time-to-submit floor, MX and disposable-domain checks, and a global
// breaker on sign-up letters. Everything here answers only for SignUp.

// Bot check modes (PLATFORM_SIGNUP_BOT_CHECK).
const (
	BotCheckOff     = "off"
	BotCheckLog     = "log"
	BotCheckEnforce = "enforce"
)

// Metadata keys of the bot check (headers Grpc-Metadata-X-Signup-Proof / -Trap through the gateway).
const (
	MDSignupProof = "x-signup-proof"
	MDSignupTrap  = "x-signup-trap"
)

// Defaults of the guard settings.
const (
	DefaultSignupPowMax         = 100_000
	DefaultSignupMinSubmit      = 2 * time.Second
	DefaultSignupLettersPerHour = 60
	DefaultSignupMaxBouncePct   = 4
	// SignupBounceMinSample: below this many sent letters a day the bounce share is not judged.
	SignupBounceMinSample = 20
	// SignupChallengeTTL is how long a challenge can be solved and sent.
	SignupChallengeTTL = 10 * time.Minute
	// MaxSignupChallengesPerIPMin bounds GET /api/auth/signup-challenge per address.
	MaxSignupChallengesPerIPMin = 30

	powAlgorithm   = "SHA-256"
	maxProofLen    = 2048
	mxTimeout      = 3 * time.Second
	mxCacheTTL     = time.Hour
	mxCacheMax     = 10_000
	usedProofsMax  = 100_000
	breakerRecheck = 30 * time.Second
)

// GuardConfig is the parsed guard settings.
type GuardConfig struct {
	BotCheck       string // off | log | enforce
	PowMax         int
	MinSubmit      time.Duration
	MXCheck        bool
	Blocked        map[string]bool // extra blocked domains
	LettersPerHour int             // 0 = no hourly cap
	MaxBouncePct   int             // 0 = no bounce cap
}

// GuardFromConfig reads PLATFORM_SIGNUP_*. An unreadable value falls back to its default (and
// is logged once by the caller through the returned warnings), never stops a boot.
func GuardFromConfig(c platformstore.Config) (GuardConfig, []string) {
	g := GuardConfig{BotCheck: BotCheckLog, PowMax: DefaultSignupPowMax, MinSubmit: DefaultSignupMinSubmit, MXCheck: true,
		Blocked: map[string]bool{}, LettersPerHour: DefaultSignupLettersPerHour, MaxBouncePct: DefaultSignupMaxBouncePct}
	var warn []string
	switch v := strings.ToLower(strings.TrimSpace(c.SignupBotCheck)); v {
	case "":
	case BotCheckOff, BotCheckLog, BotCheckEnforce:
		g.BotCheck = v
	default:
		warn = append(warn, "PLATFORM_SIGNUP_BOT_CHECK")
	}
	intOf := func(s, name string, dst *int, lo, hi int) {
		if s = strings.TrimSpace(s); s == "" {
			return
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < lo || n > hi {
			warn = append(warn, name)
			return
		}
		*dst = n
	}
	intOf(c.SignupPowMax, "PLATFORM_SIGNUP_POW_MAX", &g.PowMax, 1_000, 10_000_000)
	intOf(c.SignupLettersPerHour, "PLATFORM_SIGNUP_LETTERS_PER_HOUR", &g.LettersPerHour, 0, 1_000_000)
	intOf(c.SignupMaxBouncePct, "PLATFORM_SIGNUP_MAX_BOUNCE_PCT", &g.MaxBouncePct, 0, 100)
	if s := strings.TrimSpace(c.SignupMinSubmit); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 || d > time.Minute {
			warn = append(warn, "PLATFORM_SIGNUP_MIN_SUBMIT")
		} else {
			g.MinSubmit = d
		}
	}
	if s := strings.TrimSpace(c.SignupMXCheck); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			warn = append(warn, "PLATFORM_SIGNUP_MX_CHECK")
		} else {
			g.MXCheck = b
		}
	}
	for d := range strings.SplitSeq(c.SignupBlockedDomains, ",") {
		if d = strings.ToLower(strings.Trim(strings.TrimSpace(d), ".")); d != "" {
			g.Blocked[d] = true
		}
	}
	return g, warn
}

// signupGuard holds the guard's state for one Service.
type signupGuard struct {
	cfg      GuardConfig
	key      []byte // HMAC key of the challenges, derived from the token pepper
	now      func() time.Time
	resolver mxResolver
	stats    func(context.Context, time.Time) (platformstore.SignupLetterStats, error)
	issueIP  *ratelimit.Limiter

	mu      sync.Mutex
	used    map[string]time.Time // signature → expiry: a proof is spent once (per process)
	mxCache map[string]mxEntry

	bmu       sync.Mutex
	open      bool      // breaker latched open
	checkedAt time.Time // last stats read
	reason    string
}

type mxEntry struct {
	ok    bool
	until time.Time
}

// mxResolver is the part of *net.Resolver the address check needs.
type mxResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

func newSignupGuard(cfg GuardConfig, pepper string, now func() time.Time,
	stats func(context.Context, time.Time) (platformstore.SignupLetterStats, error)) *signupGuard {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte("lekalo-signup-challenge-v1"))
	return &signupGuard{cfg: cfg, key: m.Sum(nil), now: now, resolver: net.DefaultResolver, stats: stats,
		issueIP: ratelimit.NewLimiter(time.Minute, MaxSignupChallengesPerIPMin),
		used:    map[string]time.Time{}, mxCache: map[string]mxEntry{}}
}

func (g *signupGuard) stop() { g.issueIP.Stop() }

// --- proof of work (ALTCHA v1) ---

// Challenge is the ALTCHA v1 challenge JSON.
type Challenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// proof is the decoded ALTCHA v1 payload the browser sends back.
type proof struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int    `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

func (g *signupGuard) sign(s string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func powHash(salt string, n int) string {
	h := sha256.Sum256([]byte(salt + strconv.Itoa(n)))
	return hex.EncodeToString(h[:])
}

// issue makes a challenge: the salt carries its expiry and issue time (bound by the hash, so they
// cannot be changed without breaking the signature).
func (g *signupGuard) issue() (Challenge, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Challenge{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(g.cfg.PowMax)+1))
	if err != nil {
		return Challenge{}, err
	}
	now := g.now()
	salt := hex.EncodeToString(b[:]) + "?expires=" + strconv.FormatInt(now.Add(SignupChallengeTTL).Unix(), 10) +
		"&issued=" + strconv.FormatInt(now.UnixMilli(), 10)
	ch := powHash(salt, int(n.Int64()))
	return Challenge{Algorithm: powAlgorithm, Challenge: ch, MaxNumber: g.cfg.PowMax, Salt: salt, Signature: g.sign(ch)}, nil
}

// Bot check outcomes.
type botVerdict int

const (
	botOK      botVerdict = iota
	botNoProof            // missing, malformed, wrong, expired or reused proof → refuse (enforce)
	botTrap               // honeypot filled or sent too fast → silent drop (enforce)
)

// check judges the request's proof and honeypot. why is a short reason for the audit.
func (g *signupGuard) check(ctx context.Context) (botVerdict, string) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := firstMD(md, MDSignupTrap); v != "" {
		return botTrap, "trap"
	}
	raw := firstMD(md, MDSignupProof)
	if raw == "" {
		return botNoProof, "no_proof"
	}
	if len(raw) > maxProofLen {
		return botNoProof, "bad_proof"
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		if dec, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return botNoProof, "bad_proof"
		}
	}
	var p proof
	if json.Unmarshal(dec, &p) != nil || p.Algorithm != powAlgorithm || p.Number < 0 || p.Number > g.cfg.PowMax {
		return botNoProof, "bad_proof"
	}
	if !hmac.Equal([]byte(g.sign(p.Challenge)), []byte(strings.ToLower(p.Signature))) ||
		!hmac.Equal([]byte(powHash(p.Salt, p.Number)), []byte(strings.ToLower(p.Challenge))) {
		return botNoProof, "bad_proof"
	}
	expires, issued, ok := saltTimes(p.Salt)
	now := g.now()
	if !ok || !now.Before(expires) {
		return botNoProof, "expired"
	}
	if !g.spend(p.Signature, expires, now) {
		return botNoProof, "reused"
	}
	if g.cfg.MinSubmit > 0 && now.Sub(issued) < g.cfg.MinSubmit {
		return botTrap, "too_fast"
	}
	return botOK, ""
}

func firstMD(md metadata.MD, k string) string {
	if v := md.Get(k); len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// saltTimes reads expires (unix s) and issued (unix ms) from a salt "<hex>?expires=…&issued=…".
func saltTimes(salt string) (expires, issued time.Time, ok bool) {
	i := strings.IndexByte(salt, '?')
	if i < 0 {
		return expires, issued, false
	}
	q, err := url.ParseQuery(salt[i+1:])
	if err != nil {
		return expires, issued, false
	}
	e, err1 := strconv.ParseInt(q.Get("expires"), 10, 64)
	is, err2 := strconv.ParseInt(q.Get("issued"), 10, 64)
	if err1 != nil || err2 != nil {
		return expires, issued, false
	}
	return time.Unix(e, 0), time.UnixMilli(is), true
}

// spend marks a proof used; false when it already was. Expired entries are dropped as it goes.
func (g *signupGuard) spend(sig string, expires, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, seen := g.used[sig]; seen {
		return false
	}
	if len(g.used) >= usedProofsMax {
		for k, until := range g.used {
			if !now.Before(until) {
				delete(g.used, k)
			}
		}
		if len(g.used) >= usedProofsMax {
			return false // full of live proofs: a flood; refuse rather than forget
		}
	}
	g.used[sig] = expires
	return true
}

// ChallengeHandler serves GET /api/auth/signup-challenge: a fresh ALTCHA v1 challenge, never
// cached. 429 past the per-address limit.
func (s *Service) ChallengeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.usable() != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if !s.guard.issueIP.Allow(middleware.GetClientIP(r.Context())) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		ch, err := s.guard.issue()
		if err != nil {
			s.log(r.Context(), "signup challenge", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ch)
	})
}

// --- address checks ---

// disposableDomains is a short built-in list of the largest throwaway-mail services; the operator
// extends it with PLATFORM_SIGNUP_BLOCKED_DOMAINS. A function, not a package var (tenantgate G4).
func disposableDomains() []string {
	return []string{
		"mailinator.com", "mailinator.net", "mailinator2.com", "guerrillamail.com", "guerrillamail.net",
		"guerrillamail.org", "guerrillamail.biz", "guerrillamail.de", "guerrillamailblock.com", "sharklasers.com",
		"grr.la", "pokemail.net", "spam4.me", "10minutemail.com", "10minutemail.net", "10mail.org",
		"temp-mail.org", "temp-mail.io", "tempmail.com", "tempmail.net", "tempmailo.com", "tempail.com",
		"tempr.email", "tmpmail.org", "tmpmail.net", "yopmail.com", "yopmail.net", "yopmail.fr",
		"trashmail.com", "trashmail.net", "trashmail.de", "getnada.com", "nada.email", "dispostable.com",
		"maildrop.cc", "throwawaymail.com", "fakeinbox.com", "mintemail.com", "mohmal.com", "emailondeck.com",
		"discard.email", "spamgourmet.com", "mailnesia.com", "mailcatch.com", "mytemp.email", "burnermail.io",
		"moakt.com", "getairmail.com", "inboxkitten.com", "harakirimail.com", "mailpoof.com", "emailfake.com",
		"crazymailing.com", "minuteinbox.com", "1secmail.com", "1secmail.net", "1secmail.org", "dropmail.me",
		"emltmp.com", "byom.de", "trbvm.com", "spambox.us",
	}
}

// errEmailDomain is the internal refusal of checkAddress. Built per call (tenantgate G4).
func errEmailDomain() error { return errors.New(msgEmailDomain) }

// msgEmailDomain is the answer to an address whose domain takes no mail or is disposable.
const msgEmailDomain = "email_domain"

// checkAddress refuses an address whose domain is disposable or cannot receive mail (no MX and no
// A/AAAA, or a null MX). A DNS failure other than "not found" lets the address through.
func (g *signupGuard) checkAddress(ctx context.Context, email string) error {
	if !g.cfg.MXCheck {
		return nil
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return errEmailDomain()
	}
	domain := strings.Trim(email[at+1:], ".")
	if g.blocked(domain) {
		return errEmailDomain()
	}
	now := g.now()
	g.mu.Lock()
	e, hit := g.mxCache[domain]
	g.mu.Unlock()
	if hit && now.Before(e.until) {
		if !e.ok {
			return errEmailDomain()
		}
		return nil
	}
	ok, sure := g.lookup(ctx, domain)
	if sure {
		g.mu.Lock()
		if len(g.mxCache) >= mxCacheMax {
			clear(g.mxCache)
		}
		g.mxCache[domain] = mxEntry{ok: ok, until: now.Add(mxCacheTTL)}
		g.mu.Unlock()
	}
	if !ok {
		return errEmailDomain()
	}
	return nil
}

func (g *signupGuard) blocked(domain string) bool {
	set := g.cfg.Blocked
	builtin := disposableDomains()
	for d := domain; d != ""; {
		if set[d] {
			return true
		}
		for _, b := range builtin {
			if d == b {
				return true
			}
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return false
}

// lookup: ok = the domain can receive mail; sure = the answer is definite (cacheable). A lookup
// that fails for any reason other than "no such name / no records" answers ok, not sure.
func (g *signupGuard) lookup(ctx context.Context, domain string) (ok, sure bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mxTimeout)
	defer cancel()
	mx, err := g.resolver.LookupMX(ctx, domain)
	if err == nil && len(mx) > 0 {
		if len(mx) == 1 && (mx[0].Host == "." || mx[0].Host == "") {
			return false, true // null MX (RFC 7505): the domain says it takes no mail
		}
		return true, true
	}
	if err != nil && !notFound(err) {
		return true, false
	}
	// No MX: mail falls back to the address record (RFC 5321 §5.1).
	hosts, err := g.resolver.LookupHost(ctx, domain)
	if err == nil && len(hosts) > 0 {
		return true, true
	}
	if err != nil && !notFound(err) {
		return true, false
	}
	return false, true
}

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// --- breaker ---

// msgSignupBusy is the answer while the breaker is open.
const msgSignupBusy = "signup_busy"

// breaker reports whether new sign-up letters must stop now. The stats are re-read at most every
// breakerRecheck; the open state is latched with one error event per open period (lane X12 alerts
// on its message) and an info line when it closes. A stats read error keeps the last state.
func (g *signupGuard) breaker(ctx context.Context) bool {
	if g.cfg.LettersPerHour <= 0 && g.cfg.MaxBouncePct <= 0 {
		return false
	}
	now := g.now()
	g.bmu.Lock()
	defer g.bmu.Unlock()
	if !g.checkedAt.IsZero() && now.Sub(g.checkedAt) < breakerRecheck {
		return g.open
	}
	st, err := g.stats(ctx, now)
	if err != nil {
		slog.Default().ErrorContext(ctx, "teams: sign-up letter stats", slog.String("err", err.Error()))
		return g.open
	}
	g.checkedAt = now
	reason, count, limit := "", 0, 0
	switch {
	case g.cfg.LettersPerHour > 0 && st.VerifyLastHour >= g.cfg.LettersPerHour:
		reason, count, limit = "letters_per_hour", st.VerifyLastHour, g.cfg.LettersPerHour
	case g.cfg.MaxBouncePct > 0 && st.Sent24h >= SignupBounceMinSample && st.Bounced24h*100 >= g.cfg.MaxBouncePct*st.Sent24h:
		reason, count, limit = "bounce_share", st.Bounced24h*100/st.Sent24h, g.cfg.MaxBouncePct
	}
	switch {
	case reason != "" && !g.open:
		g.open, g.reason = true, reason
		// X12 alerts on this exact message: keep it stable, numbers only in attrs.
		slog.Default().ErrorContext(ctx, "sign-up letter breaker tripped", slog.String("reason", reason),
			slog.Int("sent_last_hour", st.VerifyLastHour), slog.Int("count", count), slog.Int("limit", limit))
	case reason == "" && g.open:
		slog.Default().InfoContext(ctx, "sign-up letter breaker closed", slog.String("reason", g.reason))
		g.open, g.reason = false, ""
	}
	return g.open
}

// --- SignUp hook ---

// guardSignUp runs the guard for one SignUp after its fields were validated. drop = answer {} and
// write nothing (a bot that must not learn it was caught); err = the answer to return.
func (s *Service) guardSignUp(ctx context.Context, email string) (drop bool, err error) {
	g := s.guard
	if g.cfg.BotCheck != BotCheckOff {
		v, why := g.check(ctx)
		if v != botOK {
			s.audit(ctx, 0, 0, "signup_bot", map[string]string{"reason": why, "mode": g.cfg.BotCheck})
			if g.cfg.BotCheck == BotCheckEnforce {
				if v == botTrap {
					return true, nil
				}
				return false, status.Error(codes.FailedPrecondition, "bot_check")
			}
		}
	}
	if err := g.checkAddress(ctx, email); err != nil {
		return false, errBad(msgEmailDomain)
	}
	if g.breaker(ctx) {
		return false, status.Error(codes.Unavailable, msgSignupBusy)
	}
	return false, nil
}
