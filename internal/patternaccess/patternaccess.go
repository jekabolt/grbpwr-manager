// Package patternaccess is the service half of the tokenized pattern read path (Ф7):
// it mints the stable /api/p/{token} urls embedded in admin API responses
// (view_url/download_url) and printed tech-pack QR codes, and serves the endpoint that
// resolves a token to a short-lived presigned origin url.
//
// The token authenticates BY ITSELF (capability url) — admin JWT plays no part, which is
// exactly what lets <object>/<iframe>/QR consumers work. Security properties live in the
// pattern_object_access row: epoch (revocation), expires_at (policy), revoked_at (hard
// off), plus per-request rate limiting and slog audit here.
package patternaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// Presigner is the narrow slice of dependency.FileStore this service needs.
type Presigner interface {
	PresignPatternObject(ctx context.Context, objectKey string, download bool, downloadName string) (string, time.Time, error)
}

// CardManifests is the narrow slice of dependency.TechCards the viewer manifest needs
// (see viewer.go). dependency.TechCards satisfies it.
type CardManifests interface {
	GetPatternViewerManifest(ctx context.Context, techCardID int) (*entity.PatternViewerCard, error)
}

const (
	// Per (ip|token) — an admin card with a dozen tiles bursts that many distinct
	// tokens from one IP, so the per-pair budget only has to cover retries/reloads.
	perTokenWindow = time.Minute
	perTokenMax    = 60
	// Per ip across all tokens — the anti-scan backstop.
	perIPWindow = time.Minute
	perIPMax    = 600

	// The viewer manifest gets its OWN per-ip budget instead of sharing the object one.
	// The two have different populations: /api/p is one admin browsing tiles, /api/pv is a
	// whole workshop of phones behind a single NAT address, each scan costing a manifest
	// plus a file hop per sheet opened. Sharing would let a busy cutting floor spend the
	// anti-scan backstop that exists for a different threat, and the failure is silent by
	// construction — the same bare 404 as a revoked link, with the reason sampled to Debug,
	// so the field report would be «QR не работает» with nothing in the logs to contradict
	// it. Separate budgets keep one population from starving the other.
	perViewerIPWindow = time.Minute
	perViewerIPMax    = 1200

	statsFlushInterval = time.Minute

	// deniedLogSample is the 1-in-N Info sampling rate for denials (see notFound).
	deniedLogSample = 10
)

// Service mints pattern urls and serves /api/p/{token} plus the card-level viewer
// manifest /api/pv/{token} (viewer.go).
type Service struct {
	objects dependency.PatternObjects
	cards   CardManifests
	minter  *patterntoken.Minter
	presign Presigner

	// viewerBaseURL is this backend's external origin (PatternToken.PublicBaseURL,
	// trailing slash trimmed) — the per-file /api/p urls inside a viewer manifest are
	// absolute because the manifest is consumed from the ADMIN SPA's origin.
	viewerBaseURL string

	tokenLimiter *ratelimit.Limiter
	ipLimiter    *ratelimit.Limiter
	// viewerIPLimiter is the /api/pv twin of ipLimiter — see perViewerIPMax for why the
	// two populations must not share a budget.
	viewerIPLimiter *ratelimit.Limiter

	// Debounced access stats (design R7): the audit trail is the slog line; these
	// counters exist for the UI and are flushed asynchronously so tile bursts do not
	// turn into row updates per request. Object and card-viewer counters are SEPARATE
	// maps flushed to separate tables — the id spaces overlap numerically, and one
	// shared map would credit a card's views to whatever object shares the number.
	statsMu        sync.Mutex
	statsCount     map[int64]int64
	statsLast      map[int64]time.Time
	cardStatsCount map[int]int64
	cardStatsLast  map[int]time.Time
	stopCh         chan struct{}
	stopOnce       sync.Once

	// deniedSeq drives the 1-in-N sampling of denial log lines.
	deniedSeq atomic.Int64
}

// New builds the service. pepper failing closed happens in patterntoken.NewMinter.
// viewerBaseURL must be the backend's external https origin without a trailing slash
// (config.Validate pins the shape).
func New(objects dependency.PatternObjects, cards CardManifests, presign Presigner, pepper, viewerBaseURL string) (*Service, error) {
	minter, err := patterntoken.NewMinter(pepper)
	if err != nil {
		return nil, err
	}
	s := &Service{
		objects:         objects,
		cards:           cards,
		minter:          minter,
		presign:         presign,
		viewerBaseURL:   strings.TrimRight(viewerBaseURL, "/"),
		tokenLimiter:    ratelimit.NewLimiter(perTokenWindow, perTokenMax),
		ipLimiter:       ratelimit.NewLimiter(perIPWindow, perIPMax),
		viewerIPLimiter: ratelimit.NewLimiter(perViewerIPWindow, perViewerIPMax),
		statsCount:      map[int64]int64{},
		statsLast:       map[int64]time.Time{},
		cardStatsCount:  map[int]int64{},
		cardStatsLast:   map[int]time.Time{},
		stopCh:          make(chan struct{}),
	}
	go s.flushLoop()
	return s, nil
}

// Stop terminates the stats flusher (idempotent) and flushes what is pending.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.flushStats()
		s.tokenLimiter.Stop()
		s.ipLimiter.Stop()
		s.viewerIPLimiter.Stop()
	})
}

func (s *Service) flushLoop() {
	t := time.NewTicker(statsFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.flushStats()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Service) flushStats() {
	s.statsMu.Lock()
	counts := s.statsCount
	last := s.statsLast
	cardCounts := s.cardStatsCount
	cardLast := s.cardStatsLast
	s.statsCount = map[int64]int64{}
	s.statsLast = map[int64]time.Time{}
	s.cardStatsCount = map[int]int64{}
	s.cardStatsLast = map[int]time.Time{}
	s.statsMu.Unlock()
	if len(counts) == 0 && len(cardCounts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(counts) > 0 {
		if err := s.objects.RecordAccess(ctx, counts, last); err != nil {
			slog.Default().ErrorContext(ctx, "pattern access stats flush failed",
				slog.String("err", err.Error()))
		}
	}
	if len(cardCounts) > 0 {
		if err := s.objects.RecordCardViewerAccess(ctx, cardCounts, cardLast); err != nil {
			slog.Default().ErrorContext(ctx, "pattern viewer stats flush failed",
				slog.String("err", err.Error()))
		}
	}
}

func (s *Service) noteAccess(id int64) {
	s.statsMu.Lock()
	s.statsCount[id]++
	s.statsLast[id] = time.Now().UTC()
	s.statsMu.Unlock()
}

func (s *Service) noteCardAccess(techCardID int) {
	s.statsMu.Lock()
	s.cardStatsCount[techCardID]++
	s.cardStatsLast[techCardID] = time.Now().UTC()
	s.statsMu.Unlock()
}

// FillTechCardPatternURLs populates the output-only view_url/download_url on tech-card
// pattern messages. Nil-receiver-safe (tests construct servers without the service) and
// best-effort: an unparseable url leaves the fields empty rather than failing the read
// (design R8). NOT used for persisted snapshots — release blobs must stay token-free.
func (s *Service) FillTechCardPatternURLs(ctx context.Context, baseURL string, ps []*pb_common.TechCardSizePattern) {
	if s == nil || len(ps) == 0 {
		return
	}
	refs := make([]entity.PatternObjectRef, 0, len(ps))
	for _, p := range ps {
		if k, ok := storeutil.PatternObjectKey(p.GetUrl()); ok {
			refs = append(refs, entity.PatternObjectRef{Key: k, Filename: p.GetFilename()})
		}
	}
	rows, err := s.objects.EnsureByKeys(ctx, refs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "fill pattern urls failed", slog.String("err", err.Error()))
		return
	}
	s.applyTechCardURLs(baseURL, rows, ps)
}

func (s *Service) applyTechCardURLs(baseURL string, rows map[string]entity.PatternObjectAccess, ps []*pb_common.TechCardSizePattern) {
	for _, p := range ps {
		k, ok := storeutil.PatternObjectKey(p.GetUrl())
		if !ok {
			continue
		}
		row, ok := rows[k]
		if !ok {
			continue
		}
		tok := s.minter.Mint(patterntoken.ScopeInternal, row.Id, row.Epoch)
		p.ViewUrl = baseURL + "/api/p/" + tok
		p.DownloadUrl = baseURL + "/api/p/" + tok + "?dl=1"
	}
}

// FillFittingPatternURLs is the fitting twin of FillTechCardPatternURLs.
func (s *Service) FillFittingPatternURLs(ctx context.Context, baseURL string, ps []*pb_common.FittingPattern) {
	if s == nil || len(ps) == 0 {
		return
	}
	s.FillFittingPatternURLsBatch(ctx, baseURL, [][]*pb_common.FittingPattern{ps})
}

// FillFittingPatternURLsBatch fills several fittings' patterns in ONE ensure pass. A list
// RPC returns up to a page of fittings, and a per-fitting ensure would put a query (and,
// for a fresh object, a write) per fitting behind every list read.
func (s *Service) FillFittingPatternURLsBatch(ctx context.Context, baseURL string, groups [][]*pb_common.FittingPattern) {
	if s == nil || len(groups) == 0 {
		return
	}
	refs := make([]entity.PatternObjectRef, 0, len(groups))
	for _, ps := range groups {
		for _, p := range ps {
			if k, ok := storeutil.PatternObjectKey(p.GetUrl()); ok {
				refs = append(refs, entity.PatternObjectRef{Key: k, Filename: p.GetFilename()})
			}
		}
	}
	if len(refs) == 0 {
		return
	}
	rows, err := s.objects.EnsureByKeys(ctx, refs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "fill pattern urls failed", slog.String("err", err.Error()))
		return
	}
	for _, ps := range groups {
		for _, p := range ps {
			k, ok := storeutil.PatternObjectKey(p.GetUrl())
			if !ok {
				continue
			}
			row, ok := rows[k]
			if !ok {
				continue
			}
			tok := s.minter.Mint(patterntoken.ScopeInternal, row.Id, row.Epoch)
			p.ViewUrl = baseURL + "/api/p/" + tok
			p.DownloadUrl = baseURL + "/api/p/" + tok + "?dl=1"
		}
	}
}

// ServeHTTP handles GET /api/p/{token}. Every negative outcome is the same plain 404 —
// probing must not be able to distinguish a bad signature from a revoked object.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	ip := middleware.ClientIPFromRequest(r)

	notFound := func(reason string) {
		// The REASON goes to the audit log only, never to the response. Denials are
		// SAMPLED at Info (every deniedLogSample-th) with the rest at Debug: this
		// endpoint is unauthenticated, so an unmetered log line per rejected request is
		// a log-volume amplifier anyone on the internet can pull.
		level := slog.LevelDebug
		if s.deniedSeq.Add(1)%deniedLogSample == 0 {
			level = slog.LevelInfo
		}
		slog.Default().Log(ctx, level, "pattern access denied",
			slog.String("reason", reason), slog.String("ip", ip),
			slog.String("ua", r.UserAgent()))
		http.NotFound(w, r)
	}

	if !s.ipLimiter.Allow(ip) {
		notFound("ip rate limited")
		return
	}
	scope, id, epoch, err := s.minter.Parse(token)
	if err != nil {
		notFound("bad token")
		return
	}
	// SCOPE ALLOWLIST. This endpoint resolves ids against pattern_object_access, so only
	// the two OBJECT scopes may pass. A ScopeCard token carries a TECH CARD id in the same
	// numeric range — looked up here it would resolve to an unrelated object whose row id
	// happens to equal the card id, and serve a file the token never named. Card tokens
	// are served by ServeManifest (/api/pv) alone; allowlist, not denylist, so a future
	// scope fails closed here instead of inheriting object semantics by default.
	if scope != patterntoken.ScopeInternal && scope != patterntoken.ScopePrint {
		notFound("wrong token scope")
		return
	}
	// Keyed on the PARSED id, never the token string: Parse pins canonical spelling, but
	// the id is the identity the budget is actually about (both scopes of one object share
	// it), and a string key would hand an attacker a fresh bucket per spelling variant.
	if !s.tokenLimiter.Allow(ip + "|" + strconv.FormatInt(id, 10)) {
		notFound("token rate limited")
		return
	}
	row, err := s.objects.GetById(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound("no access row")
		} else {
			slog.Default().ErrorContext(ctx, "pattern access lookup failed", slog.String("err", err.Error()))
			notFound("lookup error")
		}
		return
	}
	now := time.Now().UTC()
	switch {
	case row.Epoch != epoch:
		notFound("stale epoch")
		return
	case row.RevokedAt.Valid:
		notFound("revoked")
		return
	case row.ExpiresAt.Valid && now.After(row.ExpiresAt.Time):
		notFound("expired")
		return
	}

	download := r.URL.Query().Get("dl") == "1"
	signed, expiresAt, err := s.presign.PresignPatternObject(ctx, row.ObjectKey, download, row.Filename.String)
	if err != nil {
		slog.Default().ErrorContext(ctx, "pattern presign failed", slog.String("err", err.Error()))
		notFound("presign error")
		return
	}

	slog.Default().InfoContext(ctx, "pattern access",
		slog.String("object_key", row.ObjectKey), slog.Int64("token_id", id),
		slog.String("scope", string(rune(scope))), slog.String("ip", ip),
		slog.String("ua", r.UserAgent()), slog.Bool("dl", download))
	s.noteAccess(id)

	// A tokenized url is stable; its RESOLUTION must not be cached by shared caches.
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("mode") == "json" {
		// For fetch→blob consumers: a cross-origin redirect taints the request origin to
		// null, which fails the bucket's CORS rules — a JSON hop lets the SPA fetch the
		// presigned url as an ordinary single cross-origin GET instead.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":        signed,
			"expires_at": expiresAt.Format(time.RFC3339),
		})
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}
