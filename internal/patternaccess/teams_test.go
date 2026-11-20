package patternaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func teamCtx(id int64) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: "t"})
}

func serveWithCtx(svc *Service, ctx context.Context, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/p/{token}", svc)
	r.Method(http.MethodGet, "/api/pv/{token}", svc.ManifestHandler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	return w
}

// TestEveryTeamMints — every team gets links; team #1 (and no team) keeps the v1 token, any other
// team gets a v2 token that carries it signed.
func TestEveryTeamMints(t *testing.T) {
	key := "https://files.grbpwr.com/" + testKey
	for _, tc := range []struct {
		name string
		ctx  context.Context
		team int64
	}{
		{"no team", context.Background(), tenant.TeamOneID},
		{"team one", teamCtx(tenant.TeamOneID), tenant.TeamOneID},
		{"team two", teamCtx(2), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
			svc := newTestService(t, objects)
			tcps := []*pb_common.TechCardSizePattern{{Url: key}}
			svc.FillTechCardPatternURLs(tc.ctx, "https://b", tcps)
			fps := []*pb_common.FittingPattern{{Url: key}}
			svc.FillFittingPatternURLs(tc.ctx, "https://b", fps)
			card := svc.MintCardViewerToken(tc.ctx, 5)
			for what, u := range map[string]string{"tech card": tcps[0].ViewUrl, "fitting": fps[0].ViewUrl, "card": "/api/pv/" + card} {
				tok := u[strings.LastIndex(u, "/")+1:]
				team, _, _, _, err := svc.minter.ParseTeam(tok)
				if err != nil || team != tc.team {
					t.Errorf("%s: token %q team %d err %v, want team %d", what, tok, team, err, tc.team)
				}
				if tc.team == tenant.TeamOneID && strings.Count(tok, ".") != 2 {
					t.Errorf("%s: team #1 must keep the v1 token, got %q", what, tok)
				}
			}
		})
	}
}

// TestTokenOpensOnlyItsTeam — a token served in a request bound to another team is the uniform 404
// on both endpoints: v2 of team 2 under team 3 or team #1, v1 (team #1) under team 2.
func TestTokenOpensOnlyItsTeam(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{1: {Id: 1, ObjectKey: testKey}}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	obj2 := svc.minter.MintFor(2, patterntoken.ScopeInternal, 1, 0)
	card2 := svc.MintCardViewerToken(teamCtx(2), 5)
	obj1 := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)
	card1 := svc.MintCardViewerToken(context.Background(), 5)

	ok := []struct {
		ctx  context.Context
		path string
		code int
	}{
		{teamCtx(2), "/api/p/" + obj2, http.StatusFound},
		{teamCtx(2), "/api/pv/" + card2, http.StatusOK},
		{context.Background(), "/api/p/" + obj1, http.StatusFound},
		{teamCtx(tenant.TeamOneID), "/api/pv/" + card1, http.StatusOK},
	}
	for _, c := range ok {
		if w := serveWithCtx(svc, c.ctx, c.path); w.Code != c.code {
			t.Errorf("own team %s: %d, want %d", c.path, w.Code, c.code)
		}
	}
	for _, c := range []struct {
		ctx  context.Context
		path string
	}{
		{teamCtx(3), "/api/p/" + obj2},
		{teamCtx(3), "/api/pv/" + card2},
		{teamCtx(tenant.TeamOneID), "/api/p/" + obj2},
		{context.Background(), "/api/pv/" + card2},
		{teamCtx(2), "/api/p/" + obj1},
		{teamCtx(2), "/api/pv/" + card1},
	} {
		if w := serveWithCtx(svc, c.ctx, c.path); w.Code != http.StatusNotFound {
			t.Errorf("foreign team %s: %d, want 404", c.path, w.Code)
		}
	}
}

// TestFlushKeepsTeam — the minute flush writes each team's counters with that team in ctx;
// counters recorded with no team flush with no team (teams mode off identity).
func TestFlushKeepsTeam(t *testing.T) {
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: testKey},
	}}
	cards := &fakeCards{cards: map[int]*entity.PatternViewerCard{5: viewerFixtureCard()}}
	svc := newTestServiceWithCards(t, objects, cards)
	objTok := map[bool]string{true: svc.minter.MintFor(2, patterntoken.ScopeInternal, 1, 0), false: svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)}
	cardTok := svc.MintCardViewerToken(teamCtx(2), 5)

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		_, inTeam := tenant.From(ctx)
		if w := serveWithCtx(svc, ctx, "/api/p/"+objTok[inTeam]); w.Code != http.StatusFound {
			t.Fatalf("object serve: %d", w.Code)
		}
	}
	if w := serveWithCtx(svc, teamCtx(2), "/api/pv/"+cardTok); w.Code != http.StatusOK {
		t.Fatalf("manifest serve: %d", w.Code)
	}
	svc.flushStats()

	var team2Obj, plainObj, team2Card bool
	for _, f := range objects.flushes {
		switch {
		case !f.card && f.hasTeam && f.teamID == 2 && f.ids[1] == 2:
			team2Obj = true
		case !f.card && !f.hasTeam && f.ids[1] == 1:
			plainObj = true
		case f.card && f.hasTeam && f.teamID == 2 && f.ids[5] == 1:
			team2Card = true
		default:
			t.Errorf("unexpected flush %+v", f)
		}
	}
	if !team2Obj || !plainObj || !team2Card {
		t.Fatalf("flushes %+v: team2 object=%v plain object=%v team2 card=%v",
			objects.flushes, team2Obj, plainObj, team2Card)
	}
}
