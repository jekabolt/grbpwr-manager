package patternaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
)

// serveViewer routes a request through BOTH mounts exactly as http.go wires them, so the
// cross-endpoint tests exercise the real routing.
func serveViewer(svc *Service, method, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/p/{token}", svc)
	r.Method(http.MethodHead, "/api/p/{token}", svc)
	r.Method(http.MethodGet, "/api/pv/{token}", svc.ManifestHandler())
	r.Method(http.MethodHead, "/api/pv/{token}", svc.ManifestHandler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

const (
	lineMain   = "01LINEMAIN0000000000000000"
	linePocket = "01LINEPOCKET00000000000000"
	lineLining = "01LINELINING00000000000000"
)

// viewerFixtureCard covers every display-resolve branch at once:
//   - sheet A: own purpose, carried by a line          → group "main"
//   - sheet B: line-bound, line later got a purpose    → group "main" (display resolve)
//   - sheet C: line-bound, line has no purpose         → line group (named)
//   - sheet D: own purpose that NO line carries        → _unbound (no phantom group)
//   - sheet E: line-bound to a line the card lost      → _unbound
//   - sheet F: no binding at all                       → _unbound
//   - sheet G: no url                                  → skipped entirely
//   - purpose "lining" is on a line but has no sheets  → group omitted
func viewerFixtureCard() *entity.PatternViewerCard {
	up := func(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: true} }
	return &entity.PatternViewerCard{
		TechCardId:  5,
		StyleNumber: "GR-042",
		StyleName:   "hooded jacket",
		Sizes: []entity.PatternViewerSize{
			{Id: 7, Name: "xs"},
			{Id: 8, Name: "s"},
		},
		BomLines: []entity.PatternViewerBomLine{
			{LineKey: lineMain, Name: "Основная", Purpose: "main"},
			{LineKey: linePocket, Name: "Карманка", Purpose: ""},
			{LineKey: lineLining, Name: "Подкладка", Purpose: "lining"},
		},
		Sheets: []entity.PatternViewerSheet{
			// SizeName rides the SHEET (the store LEFT JOINs the dictionary), not the card's
			// range — see SH below for the case that distinction exists for.
			{LineKey: "SA", FabricPurpose: "main", SizeId: 7, SizeName: "xs", Version: 3, SizeBytes: 111,
				URL:      "https://files.grbpwr.com/base/tech-card-patterns/2026/august/a.pdf",
				Filename: "перед.pdf", Name: "перед",
				UploadedAt: up(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))},
			{LineKey: "SB", BomLineKey: lineMain, SizeId: 0, Version: 1,
				URL:      "https://files.grbpwr.com/base/tech-card-patterns/2026/august/b.dxf",
				Filename: "graded.dxf", Name: "градуированный"},
			{LineKey: "SC", BomLineKey: linePocket, SizeId: 8, SizeName: "s", Version: 2,
				URL:      "https://files.grbpwr.com/base/tech-card-patterns/2026/august/c.pdf",
				Filename: "карман.pdf"},
			{LineKey: "SD", FabricPurpose: "contrast",
				URL: "https://files.grbpwr.com/base/tech-card-patterns/2026/august/d.pdf"},
			{LineKey: "SE", BomLineKey: "01LINEGONE000000000000000X",
				URL: "https://files.grbpwr.com/base/tech-card-patterns/2026/august/e.pdf"},
			{LineKey: "SF",
				URL: "https://files.grbpwr.com/base/tech-card-patterns/2026/august/f.pdf"},
			{LineKey: "SG", FabricPurpose: "main", URL: "   "},
			// A sheet still filed under a size the grade has since DROPPED: id 9 is absent
			// from Sizes above, and its name can only come off the sheet's own join. Were it
			// looked up in the range it would arrive nameless, indistinguishable on the wire
			// from a sizeless sheet — and the viewer would caption this PDF «градуированный».
			{LineKey: "SH", FabricPurpose: "main", SizeId: 9, SizeName: "xxl", Version: 1,
				URL:      "https://files.grbpwr.com/base/tech-card-patterns/2026/august/h.pdf",
				Filename: "старый размер.pdf"},
		},
	}
}

func newViewerFixtureService(t *testing.T) (*Service, *fakeObjects, string) {
	t.Helper()
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	tok := svc.MintCardViewerToken(context.Background(), 5)
	if tok == "" {
		t.Fatal("fixture token must mint")
	}
	return svc, objects, tok
}

func TestManifestHappyPath(t *testing.T) {
	svc, _, tok := newViewerFixtureService(t)

	w := serveViewer(svc, http.MethodGet, "/api/pv/"+tok)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("manifest must not be cacheable, got %q", cc)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}

	// The wire is snake_case — pin the spelling on a raw decode, not on our own structs.
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tech_card_id", "style_number", "style_name", "sizes", "groups"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("manifest missing %q: %s", key, w.Body.String())
		}
	}

	var m Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.TechCardId != 5 || m.StyleNumber != "GR-042" || m.StyleName != "hooded jacket" {
		t.Fatalf("bad header: %+v", m)
	}
	if len(m.Sizes) != 2 || m.Sizes[0].Name != "xs" || m.Sizes[1].Id != 8 {
		t.Fatalf("bad sizes: %+v", m.Sizes)
	}

	// Groups: main (A+B), the pocket line (C), _unbound (D+E+F). lining is empty →
	// omitted; G has no url → nowhere.
	if len(m.Groups) != 3 {
		t.Fatalf("want 3 groups, got %+v", m.Groups)
	}
	main, pocket, unbound := m.Groups[0], m.Groups[1], m.Groups[2]
	if main.Key != "main" || !main.ByPurpose || main.LineName != "" {
		t.Fatalf("bad main group: %+v", main)
	}
	if pocket.Key != linePocket || pocket.ByPurpose || pocket.LineName != "Карманка" {
		t.Fatalf("bad line group: %+v", pocket)
	}
	if unbound.Key != "_unbound" || unbound.ByPurpose {
		t.Fatalf("bad unbound group: %+v", unbound)
	}
	keysOf := func(g ManifestGroup) []string {
		var out []string
		for _, sh := range g.Sheets {
			out = append(out, sh.LineKey)
		}
		return out
	}
	if got := keysOf(main); strings.Join(got, ",") != "SA,SB,SH" {
		t.Fatalf("main group members: %v", got)
	}
	if got := keysOf(pocket); strings.Join(got, ",") != "SC" {
		t.Fatalf("pocket group members: %v", got)
	}
	if got := keysOf(unbound); strings.Join(got, ",") != "SD,SE,SF" {
		t.Fatalf("unbound group members: %v", got)
	}

	// Per-file urls are ScopePrint /api/p urls on the configured base.
	a := main.Sheets[0]
	if !strings.HasPrefix(a.ViewUrl, testViewerBaseURL+"/api/p/p") {
		t.Fatalf("view_url must be a ScopePrint token url, got %q", a.ViewUrl)
	}
	if a.DownloadUrl != a.ViewUrl+"?dl=1" {
		t.Fatalf("download_url must be view_url?dl=1, got %q", a.DownloadUrl)
	}
	if a.Ext != "pdf" || a.SizeName != "xs" || a.SizeId != 7 || a.Version != 3 || a.SizeBytes != 111 {
		t.Fatalf("bad sheet A: %+v", a)
	}
	if a.UploadedAt != "2026-08-01T12:00:00Z" {
		t.Fatalf("uploaded_at must be RFC3339, got %q", a.UploadedAt)
	}
	b := main.Sheets[1]
	if b.Ext != "dxf" || b.SizeId != 0 || b.SizeName != "" {
		t.Fatalf("graded sheet must carry size_id 0 with no size name: %+v", b)
	}
	// A size the grade dropped keeps its NAME on the sheet while staying out of sizes[] —
	// the range feeds the DXF size-token derivation, and a size no file carries must not
	// seed a token, but the sheet still has to say which size it is.
	h := main.Sheets[2]
	if h.SizeId != 9 || h.SizeName != "xxl" {
		t.Fatalf("sheet under a dropped size must still be named: %+v", h)
	}
	for _, sz := range m.Sizes {
		if sz.Id == 9 {
			t.Fatalf("a size dropped from the grade must not reappear in sizes[]: %+v", m.Sizes)
		}
	}
}

func TestManifestGroupOrderIsDeterministic(t *testing.T) {
	// Purpose groups in the VOCABULARY's order (entity.BomPurposeOrder — main before
	// contrast, whatever order their lines happen to sit in), then unsorted lines in BOM
	// order, then _unbound. That is the order the admin panel and the печать use
	// (bom-purpose.ts bomPurposeOrder); ordering here by first-line position instead would
	// leave the two sides preselecting different groups for a url with no ?g.
	lines := []entity.PatternViewerBomLine{
		{LineKey: "01B", Name: "контраст", Purpose: "contrast"},
		{LineKey: "01A", Name: "первая без назначения"},
		{LineKey: "01C", Name: "основа", Purpose: "main"},
		{LineKey: "01D", Name: "вторая без назначения"},
		{LineKey: "01E", Name: "ещё основа", Purpose: "main"}, // dup purpose → no second group
	}
	defs, _ := viewerGroups(lines)
	var got []string
	for _, d := range defs {
		got = append(got, d.key)
	}
	want := "main,contrast,01A,01D,_unbound"
	if strings.Join(got, ",") != want {
		t.Fatalf("group order: want %s, got %v", want, got)
	}
}

func TestViewerGroupsMatchThePanelsBlindSpots(t *testing.T) {
	// Two cases where the manifest must reproduce a LIMITATION of the panel rather than
	// improve on it. Both are unreachable through today's write path; both would show as the
	// печать captioning a QR «без привязки» while the manifest filed those sheets elsewhere,
	// and the viewer then silently opening a group the paper never named.
	t.Run("a line with no line_key defines no group", func(t *testing.T) {
		// bom-purpose.ts fabricScopes skips a keyless line outright, so its назначение is not
		// a scope on screen and must not be one here either.
		defs, keyOf := viewerGroups([]entity.PatternViewerBomLine{
			{LineKey: "", Name: "безымянная", Purpose: "lining"},
		})
		if len(defs) != 1 || defs[0].key != unboundGroupKey {
			t.Fatalf("keyless line must define no group, got %+v", defs)
		}
		if got := keyOf(entity.PatternViewerSheet{FabricPurpose: "lining"}); got != unboundGroupKey {
			t.Fatalf("sheet of a purpose no KEYED line carries: want _unbound, got %q", got)
		}
	})
	t.Run("purpose comparison folds case", func(t *testing.T) {
		// The CHECK constraints pin lowercase, but every other resolver folds
		// (entity.ResolveFabricScope) — disagreeing here would strand a stray "Main" in
		// _unbound while the rest of the system resolves it normally.
		defs, keyOf := viewerGroups([]entity.PatternViewerBomLine{
			{LineKey: "01A", Name: "основа", Purpose: "Main"},
		})
		if len(defs) != 2 || defs[0].key != "main" || !defs[0].byPurpose {
			t.Fatalf("purpose group must be keyed by the folded value, got %+v", defs)
		}
		if got := keyOf(entity.PatternViewerSheet{FabricPurpose: "MAIN"}); got != "main" {
			t.Fatalf("sheet purpose must fold too, got %q", got)
		}
	})
}

func TestViewerGroupsResolveBranches(t *testing.T) {
	lines := []entity.PatternViewerBomLine{
		{LineKey: lineMain, Name: "Основная", Purpose: "main"},
		{LineKey: linePocket, Name: "Карманка"},
	}
	_, keyOf := viewerGroups(lines)
	cases := []struct {
		name  string
		sheet entity.PatternViewerSheet
		want  string
	}{
		{"own purpose carried by a line", entity.PatternViewerSheet{FabricPurpose: "main"}, "main"},
		{"own purpose nobody carries", entity.PatternViewerSheet{FabricPurpose: "contrast"}, unboundGroupKey},
		{"line that acquired a purpose", entity.PatternViewerSheet{BomLineKey: lineMain}, "main"},
		{"line without a purpose", entity.PatternViewerSheet{BomLineKey: linePocket}, linePocket},
		{"line key case-folds", entity.PatternViewerSheet{BomLineKey: strings.ToLower(linePocket)}, linePocket},
		{"line the card lost", entity.PatternViewerSheet{BomLineKey: "01GONE0000000000000000000X"}, unboundGroupKey},
		{"no binding at all", entity.PatternViewerSheet{}, unboundGroupKey},
		{"own purpose beats the line", entity.PatternViewerSheet{FabricPurpose: "main", BomLineKey: linePocket}, "main"},
	}
	for _, c := range cases {
		if got := keyOf(c.sheet); got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// TestObjectEndpointRefusesCardToken locks the cross-scope confusion guard: a card token
// must never resolve on /api/p, where its TECH CARD id would be looked up as a
// pattern_object_access row id and serve whatever object shares the number.
func TestObjectEndpointRefusesCardToken(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		// The landmine: an OBJECT whose row id equals the card id.
		5: {Id: 5, ObjectKey: testKey},
	}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	cardTok := svc.MintCardViewerToken(context.Background(), 5)

	w := serveViewer(svc, http.MethodGet, "/api/p/"+cardTok)
	if w.Code != http.StatusNotFound {
		t.Fatalf("card token on /api/p must 404, got %d", w.Code)
	}
	garbage := serveViewer(svc, http.MethodGet, "/api/p/not-a-token")
	if w.Body.String() != garbage.Body.String() {
		t.Fatal("scope refusal must be indistinguishable from any other 404")
	}
}

// TestManifestRefusesObjectScopes is the mirror guard: object tokens ('i'/'p') must never
// open a manifest — their id names an object row, and the tech card sharing the number is
// a different thing the token holder was never granted.
func TestManifestRefusesObjectScopes(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		5: {Id: 5, ObjectKey: testKey},
	}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	// Make the card side fully valid so ONLY the scope check can be the refusal.
	if tok := svc.MintCardViewerToken(context.Background(), 5); tok == "" {
		t.Fatal("card row must exist")
	}

	for _, scope := range []patterntoken.Scope{patterntoken.ScopeInternal, patterntoken.ScopePrint} {
		tok := svc.minter.Mint(scope, 5, 0)
		w := serveViewer(svc, http.MethodGet, "/api/pv/"+tok)
		if w.Code != http.StatusNotFound {
			t.Errorf("scope %c token on /api/pv must 404, got %d", scope, w.Code)
		}
	}
}

func TestManifestNegativeOutcomesAreUniform404(t *testing.T) {
	objects := &fakeObjects{cardRows: map[int]*entity.TechCardPatternViewerAccess{
		1: {TechCardId: 1, Epoch: 5},
		2: {TechCardId: 2, Epoch: 1, RevokedAt: sql.NullTime{Time: time.Now(), Valid: true}},
		3: {TechCardId: 3, Epoch: 1, ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}},
		4: {TechCardId: 4, Epoch: 1}, // access row alive, но карточка уже удалена
	}}
	svc := newTestServiceWithCards(t, objects, &fakeCards{cards: map[int]*entity.PatternViewerCard{}})

	cases := map[string]string{
		"garbage token": "/api/pv/not-a-token",
		"stale epoch":   "/api/pv/" + svc.minter.Mint(patterntoken.ScopeCard, 1, 4),
		"revoked":       "/api/pv/" + svc.minter.Mint(patterntoken.ScopeCard, 2, 1),
		"expired":       "/api/pv/" + svc.minter.Mint(patterntoken.ScopeCard, 3, 1),
		"no access row": "/api/pv/" + svc.minter.Mint(patterntoken.ScopeCard, 99, 1),
		"card gone":     "/api/pv/" + svc.minter.Mint(patterntoken.ScopeCard, 4, 1),
	}
	var bodies []string
	for name, target := range cases {
		w := serveViewer(svc, http.MethodGet, target)
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

// TestEpochBumpKillsCardToken — the revocation mechanic the table exists for: bump the
// row's epoch and every printed QR dies, while a token minted afterwards works.
func TestEpochBumpKillsCardToken(t *testing.T) {
	svc, objects, tok := newViewerFixtureService(t)
	if w := serveViewer(svc, http.MethodGet, "/api/pv/"+tok); w.Code != http.StatusOK {
		t.Fatalf("pre-bump token must serve, got %d", w.Code)
	}
	objects.cardRows[5].Epoch++
	if w := serveViewer(svc, http.MethodGet, "/api/pv/"+tok); w.Code != http.StatusNotFound {
		t.Fatalf("post-bump token must 404, got %d", w.Code)
	}
	fresh := svc.MintCardViewerToken(context.Background(), 5)
	if w := serveViewer(svc, http.MethodGet, "/api/pv/"+fresh); w.Code != http.StatusOK {
		t.Fatalf("re-minted token must serve, got %d", w.Code)
	}
}

// TestCardAndObjectRateBudgetsAreSeparate — the per-token limiter is shared between the
// two endpoints, so the CARD key must be namespaced: requests against card N must not eat
// the budget of object N (and the exhaustion of one must leave the other serving).
func TestCardAndObjectRateBudgetsAreSeparate(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		5: {Id: 5, ObjectKey: testKey},
	}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	objTok := svc.minter.Mint(patterntoken.ScopeInternal, 5, 0)
	cardTok := svc.MintCardViewerToken(context.Background(), 5)

	// Exhaust the OBJECT budget of id 5.
	limited := false
	for i := 0; i < perTokenMax+5; i++ {
		if serveViewer(svc, http.MethodGet, "/api/p/"+objTok).Code == http.StatusNotFound {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("object budget never engaged")
	}
	// The card sharing the number still has its own budget.
	if w := serveViewer(svc, http.MethodGet, "/api/pv/"+cardTok); w.Code != http.StatusOK {
		t.Fatalf("card budget must be separate from the object's, got %d", w.Code)
	}
}

func TestManifestAnswersHEAD(t *testing.T) {
	svc, _, tok := newViewerFixtureService(t)
	// A HEAD probe must get the same verdicts as GET — a 405 would be the one response
	// that breaks the uniform-404 property.
	if w := serveViewer(svc, http.MethodHead, "/api/pv/"+tok); w.Code != http.StatusOK {
		t.Fatalf("HEAD valid: want 200, got %d", w.Code)
	}
	if w := serveViewer(svc, http.MethodHead, "/api/pv/not-a-token"); w.Code != http.StatusNotFound {
		t.Fatalf("HEAD invalid: want 404, got %d", w.Code)
	}
}

func TestMintCardViewerTokenIsBestEffort(t *testing.T) {
	var nilSvc *Service
	if got := nilSvc.MintCardViewerToken(context.Background(), 5); got != "" {
		t.Fatalf("nil service must mint nothing, got %q", got)
	}
	objects := &fakeObjects{ensureCardErr: errors.New("db down")}
	svc := newTestService(t, objects)
	if got := svc.MintCardViewerToken(context.Background(), 5); got != "" {
		t.Fatalf("ensure failure must mint nothing (the read must not fail), got %q", got)
	}
	if got := svc.MintCardViewerToken(context.Background(), 0); got != "" {
		t.Fatalf("id 0 must mint nothing, got %q", got)
	}

	// Happy path: 'c' scope, the card id, the row's epoch — and deterministic.
	ok := newTestService(t, &fakeObjects{})
	tok := ok.MintCardViewerToken(context.Background(), 42)
	scope, id, epoch, err := ok.minter.Parse(tok)
	if err != nil || scope != patterntoken.ScopeCard || id != 42 || epoch != 1 {
		t.Fatalf("bad minted token %q: (%c,%d,%d) err %v", tok, scope, id, epoch, err)
	}
	if ok.MintCardViewerToken(context.Background(), 42) != tok {
		t.Fatal("card token must be stable across reads")
	}
}
