package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// objectKeyOwner is bucket.TeamOwnsKey for the configured bucket. *bucket.Bucket implements it;
// a FileStore that does not (mocks) means the check is off, as with teams mode off.
type objectKeyOwner interface {
	OwnsObjectKey(ctx context.Context, key string) bool
}

// refuseForeignPatternURLs refuses a save whose pattern url names an object of OUR bucket that the
// team in ctx does not own. The dto converter already insists on a managed pattern object (host and
// folder), but cannot see the team: without this a team could save another team's pattern url, and
// its outside link would serve the other team's file. Team #1 owns every key outside {base}/t/, so
// every legal old url of team #1 still saves; teams mode off owns everything.
func (s *Server) refuseForeignPatternURLs(ctx context.Context, urls []string) error {
	o, ok := s.bucket.(objectKeyOwner)
	if !ok || o == nil {
		return nil
	}
	for _, u := range urls {
		key, ok := storeutil.PatternObjectKey(u)
		if !ok {
			continue
		}
		if !o.OwnsObjectKey(ctx, key) {
			return status.Error(codes.InvalidArgument, "pattern url must be an uploaded pattern object of this team")
		}
	}
	return nil
}

func techCardPatternURLs(ps []entity.TechCardSizePattern) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.URL)
	}
	return out
}

func fittingPatternURLs(ps []entity.FittingPattern) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.URL)
	}
	return out
}
