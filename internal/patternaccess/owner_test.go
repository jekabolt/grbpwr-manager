package patternaccess

import (
	"context"
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

const ownerFileKey = "0123456789abcdef0123456789abcdef"

// ownerPresigner is the fake presigner plus the bucket's real ownership rule in teams mode.
type ownerPresigner struct {
	fakePresigner
	cfg bucket.Config
}

func (p *ownerPresigner) OwnsObjectKey(ctx context.Context, key string) bool {
	return bucket.TeamOwnsKey(ctx, &p.cfg, key)
}

func ownerTeamCtx(id int64) context.Context {
	t := tenant.Team{ID: id, Slug: "t"}
	if id != tenant.TeamOneID {
		t.FileKey = ownerFileKey
	}
	return tenant.With(context.Background(), t)
}

// TestForeignKeyNeverLinked — a pattern url a team saved that names ANOTHER team's object of our
// bucket gets no link at mint time, and a row already holding such a key is the bare uncacheable 404
// at serve time. Team #1's legacy keys and its already-issued v1 links keep working.
func TestForeignKeyNeverLinked(t *testing.T) {
	legacyKey := testKey                                                       // team #1: no team prefix
	teamKey := "base/t/" + ownerFileKey + "/tech-card-patterns/2026/aug/y.pdf" // team 2's own
	objects := &fakeObjects{rows: map[int64]*entity.PatternObjectAccess{
		1: {Id: 1, ObjectKey: legacyKey},
		2: {Id: 2, ObjectKey: teamKey},
	}}
	pres := &ownerPresigner{cfg: bucket.Config{TeamsMode: true, BaseFolder: "base"}}
	svc, err := New(objects, &fakeCards{}, pres, "test-pepper", testViewerBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	one, two := ownerTeamCtx(tenant.TeamOneID), ownerTeamCtx(2)

	// Mint time.
	mint := func(ctx context.Context, key string) string {
		ps := []*pb_common.TechCardSizePattern{{Url: "https://files.grbpwr.com/" + key}}
		svc.FillTechCardPatternURLs(ctx, "https://b", ps)
		fs := []*pb_common.FittingPattern{{Url: "https://files.grbpwr.com/" + key}}
		svc.FillFittingPatternURLs(ctx, "https://b", fs)
		if (ps[0].ViewUrl == "") != (fs[0].ViewUrl == "") {
			t.Fatalf("tech card and fitting disagree for %s", key)
		}
		return ps[0].ViewUrl
	}
	if mint(two, legacyKey) != "" {
		t.Error("team 2 got a link for team #1's key")
	}
	if mint(one, teamKey) != "" {
		t.Error("team #1 got a link for team 2's key")
	}
	if mint(two, teamKey) == "" {
		t.Error("team 2 got no link for its own key")
	}
	if mint(one, legacyKey) == "" {
		t.Error("team #1 got no link for its legacy key")
	}

	// Serve time: rows holding a foreign key (minted before the check, or the card changed since).
	for _, c := range []struct {
		name string
		ctx  context.Context
		tok  string
		code int
	}{
		{"team #1 legacy key, old v1 link", one, svc.minter.Mint(patterntoken.ScopeInternal, 1, 0), http.StatusFound},
		{"team #1 legacy key, print link", one, svc.minter.Mint(patterntoken.ScopePrint, 1, 0), http.StatusFound},
		{"team 2 own key", two, svc.minter.MintFor(2, patterntoken.ScopeInternal, 2, 0), http.StatusFound},
		{"team 2 row with team #1 key", two, svc.minter.MintFor(2, patterntoken.ScopeInternal, 1, 0), http.StatusNotFound},
		{"team #1 row with team 2 key", one, svc.minter.Mint(patterntoken.ScopeInternal, 2, 0), http.StatusNotFound},
	} {
		before := pres.calls
		w := serveWithCtx(svc, c.ctx, "/api/p/"+c.tok)
		if w.Code != c.code {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.code)
			continue
		}
		if c.code == http.StatusNotFound {
			if pres.calls != before {
				t.Errorf("%s: a foreign key reached the presigner", c.name)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control %q, want no-store", c.name, got)
			}
			if body := w.Body.String(); body != "404 page not found\n" {
				t.Errorf("%s: body %q differs from the other refusals", c.name, body)
			}
		}
	}
}
