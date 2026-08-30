package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// designMediaFolder is the bucket folder every generated picture lands in — the same one
// SplitDesignPicture uses for its crops, because they are the same kind of object.
const designMediaFolder = "design"

// MintedMedia is one file this worker put in the bucket BEFORE any transaction. It carries the
// urls as well as the id because compensation has to remove the OBJECTS too, and a deleted media
// row can no longer tell anyone where they were.
type MintedMedia struct {
	ID   int
	URLs []string
}

// MediaSink is where a provider's bytes become a media row.
//
// IT IS AN INTERFACE FOR TWO REASONS, both of which are about not spending money. Accepts lets the
// pass refuse a route whose output has nowhere to live BEFORE the paid call — today that is the
// whole vector (SVG) and 3D (GLB) story, since the bucket's picture path stores raster only. And
// Drop is the orphan compensation, which the tests can observe without a bucket.
type MediaSink interface {
	// Accepts reports whether this sink can store a file of that content type.
	Accepts(contentType string) bool
	// Put stores one file and returns what it minted.
	Put(ctx context.Context, raw []byte, contentType, name string) (MintedMedia, error)
	// Drop takes back one minted file that nothing adopted. Best-effort and LOUD: a failure here
	// is logged, never returned, because the caller's own error is the one a person must see.
	Drop(ctx context.Context, m MintedMedia)
}

// bucketSink stores generated pictures the way SplitDesignPicture stores crops.
type bucketSink struct {
	files dependency.FileStore
	repo  dependency.Repository
}

// NewBucketSink wires the real sink.
func NewBucketSink(files dependency.FileStore, repo dependency.Repository) MediaSink {
	return &bucketSink{files: files, repo: repo}
}

// rasterTypes is what UploadContentImageVerbatim can actually store. IT IS A COPY OF SOMEBODY
// ELSE'S RULE, and it is here rather than inferred because the alternative is finding out after
// paying: the upload sniffs the bytes and refuses an SVG or a GLB with an error, at which point
// the provider has already been billed.
var rasterTypes = map[string]struct{}{
	ContentTypePNG:  {},
	ContentTypeJPEG: {},
	ContentTypeWEBP: {},
	ContentTypeGIF:  {},
}

func (s *bucketSink) Accepts(contentType string) bool {
	_, ok := rasterTypes[normalizeContentType(contentType)]
	return ok
}

func (s *bucketSink) Put(ctx context.Context, raw []byte, contentType, name string) (MintedMedia, error) {
	if len(raw) == 0 {
		return MintedMedia{}, fmt.Errorf("%w: a generated file carried no bytes", errStorageFailed)
	}
	if !s.Accepts(contentType) {
		return MintedMedia{}, fmt.Errorf("%w: %q", errSinkUnsupported, contentType)
	}
	media, err := s.files.UploadContentImageVerbatim(ctx, raw, designMediaFolder, name)
	if err != nil {
		return MintedMedia{}, fmt.Errorf("%w: %v", errStorageFailed, err)
	}
	if media == nil || media.GetId() == 0 {
		return MintedMedia{}, fmt.Errorf("%w: the bucket returned no media row", errStorageFailed)
	}
	return MintedMedia{ID: int(media.GetId()), URLs: mediaURLs(media)}, nil
}

// Drop mirrors designCompensateMedia in the admin server: the row goes only if nothing references
// it (DeleteMediaByIdIfUnused decides and deletes under one lock, so it can never take a picture
// away from a transaction that landed), and only then are the objects removed.
func (s *bucketSink) Drop(ctx context.Context, m MintedMedia) {
	if m.ID == 0 {
		return
	}
	deleted, refs, err := s.repo.Media().DeleteMediaByIdIfUnused(ctx, m.ID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to compensate an orphaned design output",
			slog.Int("media_id", m.ID), slog.String("err", err.Error()))
		return
	}
	if !deleted {
		slog.Default().WarnContext(ctx, "an orphaned design output was adopted meanwhile",
			slog.Int("media_id", m.ID), slog.Int("refs", len(refs)))
		return
	}
	if len(m.URLs) == 0 {
		return
	}
	if err := s.files.DeleteObjects(ctx, m.URLs...); err != nil {
		slog.Default().ErrorContext(ctx, "failed to drop the objects of an orphaned design output",
			slog.Int("media_id", m.ID), slog.String("err", err.Error()))
	}
}

// mediaURLs collects every object a media row owns, so compensation removes all three variants
// rather than the full size alone.
func mediaURLs(m *pb_common.MediaFull) []string {
	mi := m.GetMedia()
	if mi == nil {
		return nil
	}
	var out []string
	for _, v := range []*pb_common.MediaInfo{mi.GetFullSize(), mi.GetThumbnail(), mi.GetCompressed()} {
		if u := v.GetMediaUrl(); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// normalizeContentType strips the parameters a provider may append ("image/png; charset=binary")
// and lowercases the rest, so a cosmetic difference cannot read as an unsupported type.
func normalizeContentType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}
