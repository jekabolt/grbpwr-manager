package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ownerFileStore is the FileStore mock plus the real bucket ownership rule in teams mode.
type ownerFileStore struct {
	*mocks.MockFileStore
	cfg bucket.Config
}

func (o ownerFileStore) OwnsObjectKey(ctx context.Context, key string) bool {
	return bucket.TeamOwnsKey(ctx, &o.cfg, key)
}

// TestPatternURLOwnerOnSave — a tech card / fitting save refuses a pattern url naming another team's
// object of our bucket; every legal old url of team #1 (no team prefix) and a team's own url still save.
func TestPatternURLOwnerOnSave(t *testing.T) {
	const fk = "0123456789abcdef0123456789abcdef"
	legacy := "https://files.grbpwr.com/base/tech-card-patterns/2026/aug/x.pdf"
	own := "https://files.grbpwr.com/base/t/" + fk + "/tech-card-patterns/2026/aug/y.pdf"
	s := &Server{bucket: ownerFileStore{MockFileStore: mocks.NewMockFileStore(t), cfg: bucket.Config{TeamsMode: true, BaseFolder: "base"}}}
	one := tenant.With(context.Background(), tenant.TeamOne("grbpwr"))
	two := tenant.With(context.Background(), tenant.Team{ID: 2, Slug: "b", FileKey: fk})

	for _, c := range []struct {
		name string
		ctx  context.Context
		urls []string
		ok   bool
	}{
		{"team #1 legacy url", one, []string{legacy}, true},
		{"team 2 own url", two, []string{own}, true},
		{"no patterns", two, nil, true},
		{"team 2 saves team #1's url", two, []string{own, legacy}, false},
		{"team #1 saves team 2's url", one, []string{legacy, own}, false},
	} {
		err := s.refuseForeignPatternURLs(c.ctx, c.urls)
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.ok && status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", c.name, err)
		}
	}

	// Teams mode off, or a FileStore without the rule: nothing changes.
	off := &Server{bucket: ownerFileStore{MockFileStore: mocks.NewMockFileStore(t)}}
	if err := off.refuseForeignPatternURLs(two, []string{legacy}); err != nil {
		t.Errorf("teams mode off: %v", err)
	}
	plain := &Server{bucket: mocks.NewMockFileStore(t)}
	if err := plain.refuseForeignPatternURLs(two, []string{legacy}); err != nil {
		t.Errorf("no owner rule: %v", err)
	}
}
