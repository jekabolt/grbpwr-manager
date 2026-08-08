package patternaccess

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type fakeObjects struct {
	rows        map[int64]*entity.PatternObjectAccess
	ensureCalls int

	// Card-viewer rows, keyed by tech card id — a SEPARATE space from rows above,
	// mirroring the two tables. ensureCardErr forces the best-effort mint path.
	cardRows      map[int]*entity.TechCardPatternViewerAccess
	ensureCardErr error
}

func (f *fakeObjects) GetById(_ context.Context, id int64) (*entity.PatternObjectAccess, error) {
	r, ok := f.rows[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *r
	return &cp, nil
}

func (f *fakeObjects) EnsureByKeys(_ context.Context, refs []entity.PatternObjectRef) (map[string]entity.PatternObjectAccess, error) {
	f.ensureCalls++
	out := map[string]entity.PatternObjectAccess{}
	next := int64(1)
	for _, r := range f.rows {
		if r.Id >= next {
			next = r.Id + 1
		}
	}
	for _, ref := range refs {
		found := false
		for _, r := range f.rows {
			if r.ObjectKey == ref.Key {
				if !r.Filename.Valid && ref.Filename != "" {
					r.Filename = sql.NullString{String: ref.Filename, Valid: true}
				}
				out[ref.Key] = *r
				found = true
				break
			}
		}
		if !found {
			r := &entity.PatternObjectAccess{Id: next, ObjectKey: ref.Key}
			if ref.Filename != "" {
				r.Filename = sql.NullString{String: ref.Filename, Valid: true}
			}
			f.rows[next] = r
			out[ref.Key] = *r
			next++
		}
	}
	return out, nil
}

func (f *fakeObjects) BumpEpoch(_ context.Context, id int64) error {
	f.rows[id].Epoch++
	return nil
}
func (f *fakeObjects) Revoke(_ context.Context, id int64, at time.Time) error {
	f.rows[id].RevokedAt = sql.NullTime{Time: at, Valid: true}
	return nil
}
func (f *fakeObjects) RecordAccess(context.Context, map[int64]int64, map[int64]time.Time) error {
	return nil
}
func (f *fakeObjects) DeleteByKeys(context.Context, []string) error { return nil }

func (f *fakeObjects) EnsureCardViewer(_ context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error) {
	if f.ensureCardErr != nil {
		return nil, f.ensureCardErr
	}
	if f.cardRows == nil {
		f.cardRows = map[int]*entity.TechCardPatternViewerAccess{}
	}
	if r, ok := f.cardRows[techCardID]; ok {
		cp := *r
		return &cp, nil
	}
	r := &entity.TechCardPatternViewerAccess{TechCardId: techCardID, Epoch: 1}
	f.cardRows[techCardID] = r
	cp := *r
	return &cp, nil
}

func (f *fakeObjects) GetCardViewer(_ context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error) {
	r, ok := f.cardRows[techCardID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *r
	return &cp, nil
}

func (f *fakeObjects) RecordCardViewerAccess(context.Context, map[int]int64, map[int]time.Time) error {
	return nil
}

type fakePresigner struct {
	calls        int
	lastDownload string
}

func (p *fakePresigner) PresignPatternObject(_ context.Context, key string, download bool, downloadName string) (string, time.Time, error) {
	p.calls++
	p.lastDownload = downloadName
	u := "https://origin.example/" + key + "?sig=x"
	if download {
		u += "&dl=1"
	}
	return u, time.Now().Add(6 * time.Hour), nil
}

// fakeCards is the CardManifests half of the fakes; cards keyed by tech card id.
type fakeCards struct {
	cards map[int]*entity.PatternViewerCard
	err   error
}

func (f *fakeCards) GetPatternViewerManifest(_ context.Context, techCardID int) (*entity.PatternViewerCard, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.cards[techCardID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *c
	return &cp, nil
}

const testViewerBaseURL = "https://backend.example"

func newTestService(t *testing.T, objects *fakeObjects) *Service {
	t.Helper()
	return newTestServiceWithCards(t, objects, &fakeCards{})
}

func newTestServiceWithCards(t *testing.T, objects *fakeObjects, cards *fakeCards) *Service {
	t.Helper()
	svc, err := New(objects, cards, &fakePresigner{}, "test-pepper", testViewerBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func serve(svc *Service, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/p/{token}", svc)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

const testKey = "base/tech-card-patterns/2026/august/x.pdf"

func TestRedirectHappyPath(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey, Epoch: 0},
	}}
	svc := newTestService(t, objects)
	tok := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)

	w := serve(svc, "/api/p/"+tok)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://origin.example/"+testKey) {
		t.Fatalf("unexpected redirect target %q", loc)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("resolution must not be cacheable, got %q", cc)
	}

	wDl := serve(svc, "/api/p/"+tok+"?dl=1")
	if !strings.Contains(wDl.Header().Get("Location"), "dl=1") {
		t.Fatal("dl=1 must presign with attachment disposition")
	}
}

func TestJSONMode(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey},
	}}
	svc := newTestService(t, objects)
	tok := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)

	w := serve(svc, "/api/p/"+tok+"?mode=json")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"url"`) || !strings.Contains(body, "origin.example") {
		t.Fatalf("json body missing url: %s", body)
	}
}

func TestNegativeOutcomesAreUniform404(t *testing.T) {
	revoked := &entity.PatternObjectAccess{Id: 2, ObjectKey: testKey}
	revoked.RevokedAt = sql.NullTime{Time: time.Now(), Valid: true}
	expired := &entity.PatternObjectAccess{Id: 3, ObjectKey: testKey}
	expired.ExpiresAt = sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey, Epoch: 5},
		2: revoked,
		3: expired,
	}}
	svc := newTestService(t, objects)

	cases := map[string]string{
		"garbage token": "/api/p/not-a-token",
		"stale epoch":   "/api/p/" + svc.minter.Mint(patterntoken.ScopeInternal, 1, 4),
		"unknown row":   "/api/p/" + svc.minter.Mint(patterntoken.ScopeInternal, 99, 0),
		"revoked":       "/api/p/" + svc.minter.Mint(patterntoken.ScopeInternal, 2, 0),
		"expired":       "/api/p/" + svc.minter.Mint(patterntoken.ScopeInternal, 3, 0),
	}
	var bodies []string
	for name, target := range cases {
		w := serve(svc, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: want uniform 404, got %d", name, w.Code)
		}
		bodies = append(bodies, w.Body.String())
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatal("negative outcomes must be indistinguishable")
		}
	}
}

func TestTokenRateLimit(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey},
	}}
	svc := newTestService(t, objects)
	tok := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)
	limited := false
	for i := 0; i < perTokenMax+5; i++ {
		if serve(svc, "/api/p/"+tok).Code == http.StatusNotFound {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("per-token limiter never engaged")
	}
}

func TestFillTechCardPatternURLs(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
	svc := newTestService(t, objects)
	ps := []*pb_common.TechCardSizePattern{
		{Url: "https://files.grbpwr.com/" + testKey},
		{Url: "https://evil.example/not-managed.pdf"},
	}
	svc.FillTechCardPatternURLs(context.Background(), "https://backend.example", ps)
	if ps[0].ViewUrl == "" || !strings.HasPrefix(ps[0].ViewUrl, "https://backend.example/api/p/i") {
		t.Fatalf("managed url must get a view_url, got %q", ps[0].ViewUrl)
	}
	if !strings.HasSuffix(ps[0].DownloadUrl, "?dl=1") {
		t.Fatalf("download_url must carry dl=1, got %q", ps[0].DownloadUrl)
	}
	if ps[1].ViewUrl != "" || ps[1].DownloadUrl != "" {
		t.Fatal("unmanaged url must stay empty (R8), not error")
	}
	// Deterministic: refilling mints identical urls (no cache-busting remounts).
	view := ps[0].ViewUrl
	svc.FillTechCardPatternURLs(context.Background(), "https://backend.example", ps)
	if ps[0].ViewUrl != view {
		t.Fatal("view_url must be stable across reads")
	}
}

func TestNilServiceFillIsNoop(t *testing.T) {
	var svc *Service
	ps := []*pb_common.TechCardSizePattern{{Url: "https://files.grbpwr.com/" + testKey}}
	svc.FillTechCardPatternURLs(context.Background(), "https://b", ps) // must not panic
	if ps[0].ViewUrl != "" {
		t.Fatal("nil service must leave fields empty")
	}
}

// TestTokenLimiterKeysOnParsedID locks the per-token budget to the row id. Parse now
// rejects non-canonical spellings, so the only way a raw-string key could be gamed is
// gone — but the id key also collapses the two SCOPES of one object into one budget,
// which is the identity the limit is actually about.
func TestTokenLimiterKeysOnParsedID(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey},
	}}
	svc := newTestService(t, objects)
	internal := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)
	print := svc.minter.Mint(patterntoken.ScopePrint, 1, 0)
	if internal == print {
		// sanity: distinct strings, same object
		t.Fatal("scopes must produce distinct tokens")
	}
	limited := false
	for i := 0; i < perTokenMax+5; i++ {
		tok := internal
		if i%2 == 1 {
			tok = print
		}
		if serve(svc, "/api/p/"+tok).Code == http.StatusNotFound {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("alternating scopes of one object must share the per-token budget")
	}
}

// TestDownloadNameComesFromTheStoredRow — the object key is a timestamp plus random hex,
// so a download named after it is unusable. The name must come from the access row (fed by
// the pattern row's filename), never from the request.
func TestDownloadNameComesFromTheStoredRow(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey, Filename: sql.NullString{String: "перед.pdf", Valid: true}},
	}}
	presigner := &fakePresigner{}
	svc, err := New(objects, &fakeCards{}, presigner, "test-pepper", testViewerBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	tok := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)

	if w := serve(svc, "/api/p/"+tok+"?dl=1"); w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	if presigner.lastDownload != "перед.pdf" {
		t.Fatalf("presign must get the stored filename, got %q", presigner.lastDownload)
	}
	// A request-supplied name must not reach the presigner (it lands in a header).
	presigner.lastDownload = ""
	serve(svc, "/api/p/"+tok+"?dl=1&filename=evil%22.pdf")
	if presigner.lastDownload != "перед.pdf" {
		t.Fatalf("request parameters must never name the download, got %q", presigner.lastDownload)
	}
}

// TestFillFittingPatternURLsBatchEnsuresOnce — a list RPC must not ensure per fitting.
func TestFillFittingPatternURLsBatchEnsuresOnce(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
	svc := newTestService(t, objects)
	groups := [][]*pb_common.FittingPattern{
		{{Url: "https://files.grbpwr.com/" + testKey, Filename: "a.pdf"}},
		{{Url: "https://files.grbpwr.com/base/tech-card-patterns/2026/august/y.dxf", Filename: "b.dxf"}},
		{{Url: "https://evil.example/nope.pdf"}},
	}
	svc.FillFittingPatternURLsBatch(context.Background(), "https://backend.example", groups)
	if objects.ensureCalls != 1 {
		t.Fatalf("want one ensure pass for the page, got %d", objects.ensureCalls)
	}
	if groups[0][0].ViewUrl == "" || groups[1][0].ViewUrl == "" {
		t.Fatal("managed urls must be filled")
	}
	if groups[2][0].ViewUrl != "" {
		t.Fatal("unmanaged url must stay empty")
	}
	// The filename rides along so downloads are named after the upload.
	for _, r := range objects.rows {
		if r.ObjectKey == testKey && r.Filename.String != "a.pdf" {
			t.Fatalf("filename must be carried into the access row, got %q", r.Filename.String)
		}
	}
}
