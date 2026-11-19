package patternaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// TestPublicLinksAllowedTeamOneOnly — v1 tokens carry no team and resolve in team #1, so a
// link minted in any other team would open team #1's object with the same local id.
func TestPublicLinksAllowedTeamOneOnly(t *testing.T) {
	if err := PublicLinksAllowed(context.Background()); err != nil {
		t.Fatalf("no team (teams mode off) must be allowed: %v", err)
	}
	if err := PublicLinksAllowed(teamCtx(tenant.TeamOneID)); err != nil {
		t.Fatalf("team #1 must be allowed: %v", err)
	}
	err := PublicLinksAllowed(teamCtx(2))
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "public_links_team_one_only" {
		t.Fatalf("team 2: want FailedPrecondition public_links_team_one_only, got %v", err)
	}
}

func TestMintRefusedOutsideTeamOne(t *testing.T) {
	key := "https://files.grbpwr.com/" + testKey
	cases := []struct {
		name string
		ctx  context.Context
		mint bool
	}{
		{"no team", context.Background(), true},
		{"team one", teamCtx(tenant.TeamOneID), true},
		{"team two", teamCtx(2), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{}}
			svc := newTestService(t, objects)

			tcps := []*pb_common.TechCardSizePattern{{Url: key}}
			svc.FillTechCardPatternURLs(tc.ctx, "https://b", tcps)
			fps := []*pb_common.FittingPattern{{Url: key}}
			svc.FillFittingPatternURLs(tc.ctx, "https://b", fps)
			card := svc.MintCardViewerToken(tc.ctx, 5)

			got := map[string]bool{
				"tech card": tcps[0].ViewUrl != "" && tcps[0].DownloadUrl != "",
				"fitting":   fps[0].ViewUrl != "" && fps[0].DownloadUrl != "",
				"card":      card != "",
			}
			for what, minted := range got {
				if minted != tc.mint {
					t.Errorf("%s: minted=%v, want %v", what, minted, tc.mint)
				}
			}
			if !tc.mint && (objects.ensureCalls != 0 || len(objects.cardRows) != 0) {
				t.Errorf("refused mint must not create access rows (ensure=%d card rows=%d)",
					objects.ensureCalls, len(objects.cardRows))
			}
		})
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
	objTok := svc.minter.Mint(patterntoken.ScopeInternal, 1, 0)
	cardTok := svc.MintCardViewerToken(context.Background(), 5)

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		if w := serveWithCtx(svc, ctx, "/api/p/"+objTok); w.Code != http.StatusFound {
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
