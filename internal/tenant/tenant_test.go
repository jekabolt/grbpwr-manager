package tenant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/metadata"
)

func TestWithFrom(t *testing.T) {
	ctx := context.Background()
	if _, ok := tenant.From(ctx); ok {
		t.Fatal("empty ctx has a team")
	}
	if _, ok := tenant.From(tenant.With(ctx, tenant.Team{})); ok {
		t.Fatal("team id 0 counts as a team")
	}
	one := tenant.TeamOne("grbpwr_beta")
	got, ok := tenant.From(tenant.With(ctx, one))
	if !ok || got.ID != tenant.TeamOneID || got.DBName != "grbpwr_beta" || got.Slug != tenant.TeamOneSlug {
		t.Fatalf("From = %+v %v", got, ok)
	}
}

func TestValidSlug(t *testing.T) {
	for _, ok := range []string{"acme", "a1b", "my-team", "x2-y3-z4", "abcdefghijabcdefghijabcdefghijabcdefghij"} {
		if err := tenant.ValidSlug(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "-acme", "acme-", "Acme", "my--team", "my_team", "acme.io",
		"abcdefghijabcdefghijabcdefghijabcdefghijk", "grbpwr", "lekalo", "send", "rsend", "admin", "www"} {
		if err := tenant.ValidSlug(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHeaderSlug(t *testing.T) {
	if s := tenant.HeaderSlug(context.Background()); s != "" {
		t.Fatalf("no metadata: %q", s)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-team", "  Acme "))
	if s := tenant.HeaderSlug(ctx); s != "acme" {
		t.Fatalf("HeaderSlug = %q", s)
	}
}

type fakeDir struct {
	teams []tenant.Team
	err   error
}

func (d fakeDir) ByID(_ context.Context, id int64) (tenant.Team, error) {
	for _, t := range d.teams {
		if t.ID == id {
			return t, nil
		}
	}
	return tenant.Team{}, tenant.ErrNotFound
}
func (d fakeDir) BySlug(context.Context, string) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (d fakeDir) ListActive(context.Context) ([]tenant.Team, error) { return d.teams, d.err }
func (fakeDir) Invalidate(int64)                                    {}

func TestEach(t *testing.T) {
	dir := fakeDir{teams: []tenant.Team{{ID: 1, Slug: "a"}, {ID: 2, Slug: "b"}, {ID: 3, Slug: "c"}}}
	boom := errors.New("boom")
	var seen []int64
	err := tenant.Each(context.Background(), dir, func(ctx context.Context, tm tenant.Team) error {
		cur, ok := tenant.From(ctx)
		if !ok || cur.ID != tm.ID {
			t.Fatalf("ctx carries %+v, want team %d", cur, tm.ID)
		}
		seen = append(seen, tm.ID)
		if tm.ID == 2 {
			return boom
		}
		return nil
	})
	if len(seen) != 3 {
		t.Fatalf("a failing team stopped the loop: %v", seen)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := tenant.Each(context.Background(), fakeDir{err: boom}, func(context.Context, tenant.Team) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("listing error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := 0
	err = tenant.Each(ctx, dir, func(context.Context, tenant.Team) error { n++; return nil })
	if n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: ran %d, err %v", n, err)
	}
}

func TestPinnedAndStatic(t *testing.T) {
	if _, err := tenant.NewStaticDirectory(tenant.Team{ID: 2, DBName: "x"}); err == nil {
		t.Fatal("static directory took team 2")
	}
	if _, err := tenant.NewStaticDirectory(tenant.TeamOne("")); err == nil {
		t.Fatal("static directory took team #1 without a database")
	}
	dir, err := tenant.NewStaticDirectory(tenant.TeamOne("grbpwr"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Team{ID: 7, DBName: "lk_t_aaaaaaaa"})
	pinned, err := tenant.Pinned(ctx, dir, tenant.TeamOneID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := tenant.From(pinned); got.ID != tenant.TeamOneID {
		t.Fatalf("pinned to %d", got.ID)
	}
	if _, err := tenant.Pinned(ctx, dir, 7); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("pin unknown team: %v", err)
	}
	if _, err := dir.BySlug(context.Background(), "grbpwr"); err != nil {
		t.Fatalf("static BySlug: %v", err)
	}
	list, err := dir.ListActive(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != tenant.TeamOneID {
		t.Fatalf("static ListActive %+v %v", list, err)
	}
}
