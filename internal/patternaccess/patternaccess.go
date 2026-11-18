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

// ReadOnlyPatterns is the read-only-host half of the file store (lane B8, BUCKET_S3_READONLY_HOSTS):
// which host a stored pattern url's object lives on, and the link of an object on a read-only host.
// *bucket.Bucket implements it; a Presigner that does not (test fakes) means «no read-only hosts» —
// every object is in our own bucket, exactly today's behaviour.
type ReadOnlyPatterns interface {
	// PatternObjectSource: "" = our own bucket, else the read-only host; ok=false = not one of this
	// team's pattern objects (a read-only host this team may not read).
	PatternObjectSource(ctx context.Context, rawURL string) (host string, ok bool)
	// ReadOnlyPatternURL links an object recorded on read-only host `host`, with no network call.
	ReadOnlyPatternURL(ctx context.Context, host, objectKey string) (string, time.Time, error)
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
	// readOnly is presign's ReadOnlyPatterns side, nil when it has none (see ReadOnlyPatterns).
	readOnly ReadOnlyPatterns

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
	//
	// Pending counters are keyed by the team they were recorded under (teams.go): the flush
	// writes each batch with its own team in ctx, never pinned to team #1.
	statsMu   sync.Mutex
	stats     teamStats[int64]
	cardStats teamStats[int]
	stopCh    chan struct{}
	stopOnce  sync.Once

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
		stats:           teamStats[int64]{},
		cardStats:       teamStats[int]{},
		stopCh:          make(chan struct{}),
	}
	if ro, ok := presign.(ReadOnlyPatterns); ok {
		s.readOnly = ro
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

// patternRef names the access-row identity of a stored pattern url: its key plus the host the object
// lives on ("" = our own bucket). ok=false: not a managed pattern object of this team — no token.
func (s *Service) patternRef(ctx context.Context, rawURL, filename string) (entity.PatternObjectRef, bool) {
	k, ok := storeutil.PatternObjectKey(rawURL)
	if !ok {
		return entity.PatternObjectRef{}, false
	}
	ref := entity.PatternObjectRef{Key: k, Filename: filename}
	if s.readOnly != nil {
		host, ok := s.readOnly.PatternObjectSource(ctx, rawURL)
		if !ok {
			return entity.PatternObjectRef{}, false
		}
		ref.SourceHost = host
	}
	return ref, true
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
	batches := s.stats
	cardBatches := s.cardStats
	s.stats = teamStats[int64]{}
	s.cardStats = teamStats[int]{}
	s.statsMu.Unlock()
	for _, b := range batches {
		s.flushBatch(b.ctx(context.Background()), func(ctx context.Context) error {
			return s.objects.RecordAccess(ctx, b.count, b.last)
		}, "pattern access stats flush failed")
	}
	for _, b := range cardBatches {
		s.flushBatch(b.ctx(context.Background()), func(ctx context.Context) error {
			return s.objects.RecordCardViewerAccess(ctx, b.count, b.last)
		}, "pattern viewer stats flush failed")
	}
}

// flushBatch writes one team's batch under its own timeout; a failure is logged and the batch is
// lost, as before.
func (s *Service) flushBatch(parent context.Context, write func(context.Context) error, msg string) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := write(ctx); err != nil {
		slog.Default().ErrorContext(ctx, msg, slog.String("err", err.Error()))
	}
}

func (s *Service) noteAccess(ctx context.Context, id int64) {
	s.statsMu.Lock()
	s.stats.note(ctx, id, time.Now().UTC())
	s.statsMu.Unlock()
}

func (s *Service) noteCardAccess(ctx context.Context, techCardID int) {
	s.statsMu.Lock()
	s.cardStats.note(ctx, techCardID, time.Now().UTC())
	s.statsMu.Unlock()
}

// refusePublicLinks reports (and logs) whether ctx may not mint public links. The fill/mint
// methods are best-effort by contract — a missing url never fails the read — so a refused caller
// gets the rows without links, the same shape as any other mint failure.
func refusePublicLinks(ctx context.Context, what string) bool {
	if err := PublicLinksAllowed(ctx); err != nil {
		slog.Default().InfoContext(ctx, "public link mint refused",
			slog.String("what", what), slog.String("err", err.Error()))
		return true
	}
	return false
}

// FillTechCardPatternURLs populates the output-only view_url/download_url on tech-card
// pattern messages. Nil-receiver-safe (tests construct servers without the service) and
// best-effort: an unparseable url leaves the fields empty rather than failing the read
// (design R8). NOT used for persisted snapshots — release blobs must stay token-free.
func (s *Service) FillTechCardPatternURLs(ctx context.Context, baseURL string, ps []*pb_common.TechCardSizePattern) {
	if s == nil || len(ps) == 0 || refusePublicLinks(ctx, "tech card pattern urls") {
		return
	}
	refs := make([]entity.PatternObjectRef, 0, len(ps))
	for _, p := range ps {
		if ref, ok := s.patternRef(ctx, p.GetUrl(), p.GetFilename()); ok {
			refs = append(refs, ref)
		}
	}
	rows, err := s.objects.EnsureByKeys(ctx, refs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "fill pattern urls failed", slog.String("err", err.Error()))
		return
	}
	s.applyTechCardURLs(ctx, baseURL, rows, ps)
}

func (s *Service) applyTechCardURLs(ctx context.Context, baseURL string, rows map[entity.PatternObjectID]entity.PatternObjectAccess, ps []*pb_common.TechCardSizePattern) {
	for _, p := range ps {
		ref, ok := s.patternRef(ctx, p.GetUrl(), "")
		if !ok {
			continue
		}
		row, ok := rows[ref.ID()]
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
	if s == nil || len(groups) == 0 || refusePublicLinks(ctx, "fitting pattern urls") {
		return
	}
	refs := make([]entity.PatternObjectRef, 0, len(groups))
	for _, ps := range groups {
		for _, p := range ps {
			if ref, ok := s.patternRef(ctx, p.GetUrl(), p.GetFilename()); ok {
				refs = append(refs, ref)
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
			ref, ok := s.patternRef(ctx, p.GetUrl(), "")
			if !ok {
				continue
			}
			row, ok := rows[ref.ID()]
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
	var signed string
	var expiresAt time.Time
	if row.SourceHost != "" {
		// Recorded on a read-only host (a row copied from another installation): linked straight
		// there, never via our bucket — the same key in our bucket is a different object.
		if s.readOnly == nil {
			notFound("read-only host not served")
			return
		}
		signed, expiresAt, err = s.readOnly.ReadOnlyPatternURL(ctx, row.SourceHost, row.ObjectKey)
	} else {
		signed, expiresAt, err = s.presign.PresignPatternObject(ctx, row.ObjectKey, download, row.Filename.String)
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "pattern presign failed", slog.String("err", err.Error()))
		notFound("presign error")
		return
	}

	slog.Default().InfoContext(ctx, "pattern access",
		slog.String("object_key", row.ObjectKey), slog.Int64("token_id", id),
		slog.String("scope", string(rune(scope))), slog.String("ip", ip),
		slog.String("ua", r.UserAgent()), slog.Bool("dl", download))
	s.noteAccess(ctx, id)

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
