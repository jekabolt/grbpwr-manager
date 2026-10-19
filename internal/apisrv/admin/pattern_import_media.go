package admin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── the pictures of the pattern importer's AI calls (SuggestPatternPieces, ClassifyPatternPages) ───
//
// A media id in these requests is a picture the client uploaded FOR THIS IMPORT a moment ago (the
// marked overview, a crop, a page). The media table records no uploader and no owner, only
// created_at, so "ours" is decided by age: a row older than patternImportMediaMaxAge (or dated in
// the future beyond the clock skew) is refused — an admin cannot point the provider at any media of
// the store by id. And the url that goes to the provider must be on the bucket's own hosts
// (dto.IsManagedMediaURL), never a host a row happens to carry.

const (
	patternImportMediaMaxAge = 2 * time.Hour
	patternImportMediaSkew   = 5 * time.Minute
)

// patternImportPicture — one resolved picture, in request order.
type patternImportPicture struct {
	ID    int
	URL   string // the variant that goes to the provider
	Where string // how a refusal names it ("the picture of page 3")
	Media entity.MediaFull
}

// patternImportPictures resolves ids (in order) to pictures the provider may read, or the refusal:
// a missing file, a file not uploaded within the last patternImportMediaMaxAge, a url off the
// bucket's hosts, a file that is not a picture. thumb = send the thumbnail variant when the row has
// one (the upload path stores it ≤ 1080 px high, re-encoded smaller than the original).
func (s *Server) patternImportPictures(ctx context.Context, ids []int, where []string, thumb bool, now time.Time) ([]patternImportPicture, error) {
	byID, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "pattern import: cannot resolve the pictures", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the pattern pictures")
	}
	out := make([]patternImportPicture, 0, len(ids))
	refs := make([]designInputMediaRef, 0, len(ids))
	for i, id := range ids {
		m, ok := byID[id]
		full := strings.TrimSpace(m.FullSizeMediaURL)
		if !ok || full == "" {
			return nil, status.Errorf(codes.InvalidArgument, "media %d has no file", id)
		}
		age := now.Sub(m.CreatedAt)
		if m.CreatedAt.IsZero() || age > patternImportMediaMaxAge || age < -patternImportMediaSkew {
			return nil, status.Errorf(codes.FailedPrecondition,
				"media %d (%s) was not uploaded for this import: only pictures uploaded within the last %s are read — upload it again",
				id, where[i], patternImportMediaMaxAge)
		}
		u := full
		if t := strings.TrimSpace(m.ThumbnailMediaURL); thumb && t != "" {
			u = t
		}
		if !dto.IsManagedMediaURL(u) {
			slog.Default().WarnContext(ctx, "pattern import: a media url is not on the bucket's hosts", slog.Int("media_id", id))
			return nil, status.Errorf(codes.FailedPrecondition, "media %d (%s) is not a file of this store's storage", id, where[i])
		}
		out = append(out, patternImportPicture{ID: id, URL: u, Where: where[i], Media: m})
		refs = append(refs, designInputMediaRef{ID: id, URL: u, Where: where[i]})
	}
	if ref, ct, bad := designFirstNonPictureInput(refs); bad {
		return nil, designNonPictureRefusal(ref, ct)
	}
	return out, nil
}

// patternImportURLs — the provider urls of resolved pictures, in order.
func patternImportURLs(pics []patternImportPicture) []string {
	out := make([]string, 0, len(pics))
	for _, p := range pics {
		out = append(out, p.URL)
	}
	return out
}

// patternImportSpendExtraSlots books into the hourly window every PHYSICAL paid call beyond the
// slots already taken (a router fallback inside one attempt is a second paid call). The window may
// already be exhausted: the call happened, the slot is spent all the same (allow's false is ignored).
func (s *Server) patternImportSpendExtraSlots(admin string, physical, taken int) {
	for i := taken; i < physical; i++ {
		_ = s.enhanceRuns.allow(admin)
	}
}

// patternImportSlotRefusal — the hourly window is used up.
func patternImportSlotRefusal() error {
	return status.Errorf(codes.ResourceExhausted,
		"this account has used the assistant %d times in the last hour (ideas, text improvements, the quiz, the parts, the pattern pieces and pages and the skeleton share the limit); every call spends the AI key — try again later",
		enhancePerAdminCalls)
}
