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
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// Presigner is the narrow slice of dependency.FileStore this service needs.
type Presigner interface {
	PresignPatternObject(ctx context.Context, objectKey string, download bool) (string, time.Time, error)
}

const (
	// Per (ip|token) — an admin card with a dozen tiles bursts that many distinct
	// tokens from one IP, so the per-pair budget only has to cover retries/reloads.
	perTokenWindow = time.Minute
	perTokenMax    = 60
	// Per ip across all tokens — the anti-scan backstop.
	perIPWindow = time.Minute
	perIPMax    = 600

	statsFlushInterval = time.Minute
)

// Service mints pattern urls and serves /api/p/{token}.
type Service struct {
	objects dependency.PatternObjects
	minter  *patterntoken.Minter
	presign Presigner

	tokenLimiter *ratelimit.Limiter
	ipLimiter    *ratelimit.Limiter

	// Debounced access stats (design R7): the audit trail is the slog line; these
	// counters exist for the UI and are flushed asynchronously so tile bursts do not
	// turn into row updates per request.
	statsMu    sync.Mutex
	statsCount map[int64]int64
	statsLast  map[int64]time.Time
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// New builds the service. pepper failing closed happens in patterntoken.NewMinter.
func New(objects dependency.PatternObjects, presign Presigner, pepper string) (*Service, error) {
	minter, err := patterntoken.NewMinter(pepper)
	if err != nil {
		return nil, err
	}
	s := &Service{
		objects:      objects,
		minter:       minter,
		presign:      presign,
		tokenLimiter: ratelimit.NewLimiter(perTokenWindow, perTokenMax),
		ipLimiter:    ratelimit.NewLimiter(perIPWindow, perIPMax),
		statsCount:   map[int64]int64{},
		statsLast:    map[int64]time.Time{},
		stopCh:       make(chan struct{}),
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
	s.statsCount = map[int64]int64{}
	s.statsLast = map[int64]time.Time{}
	s.statsMu.Unlock()
	if len(counts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.objects.RecordAccess(ctx, counts, last); err != nil {
		slog.Default().ErrorContext(ctx, "pattern access stats flush failed",
			slog.String("err", err.Error()))
	}
}

func (s *Service) noteAccess(id int64) {
	s.statsMu.Lock()
	s.statsCount[id]++
	s.statsLast[id] = time.Now().UTC()
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
	keys := make([]string, 0, len(ps))
	for _, p := range ps {
		if k, ok := storeutil.PatternObjectKey(p.GetUrl()); ok {
			keys = append(keys, k)
		}
	}
	rows, err := s.objects.EnsureByKeys(ctx, keys)
	if err != nil {
		slog.Default().ErrorContext(ctx, "fill pattern urls failed", slog.String("err", err.Error()))
		return
	}
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
	keys := make([]string, 0, len(ps))
	for _, p := range ps {
		if k, ok := storeutil.PatternObjectKey(p.GetUrl()); ok {
			keys = append(keys, k)
		}
	}
	rows, err := s.objects.EnsureByKeys(ctx, keys)
	if err != nil {
		slog.Default().ErrorContext(ctx, "fill pattern urls failed", slog.String("err", err.Error()))
		return
	}
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

// ServeHTTP handles GET /api/p/{token}. Every negative outcome is the same plain 404 —
// probing must not be able to distinguish a bad signature from a revoked object.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	ip := middleware.ClientIPFromRequest(r)

	notFound := func(reason string) {
		// The REASON goes to the audit log only, never to the response.
		slog.Default().InfoContext(ctx, "pattern access denied",
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
	if !s.tokenLimiter.Allow(ip + "|" + token) {
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
	signed, expiresAt, err := s.presign.PresignPatternObject(ctx, row.ObjectKey, download)
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
