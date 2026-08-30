package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
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
// pass — and, through PreflightKind, the DOOR — refuse a route whose output has nowhere to live
// BEFORE the paid call. And Drop is the orphan compensation, which the tests can observe without a
// bucket.
//
// ⚠ ACCEPTS IS THE ONE PLACE THE ANSWER LIVES. Nothing anywhere may carry a list of run kinds that
// «do not work»: such a list is a copy of this method's answer, and a copy diverges silently the
// day a type is added on one side. The handler's refusal is computed from Produces() × Accepts()
// for exactly that reason — it stops refusing by itself the moment the sink can store the type.
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

// nonRasterTypes is not an allowlist — it is the ROUTING. A raster gets its compressed variant,
// its thumbnail and its blurhash derived from its pixels; a vector and a model have no pixels, so
// they take the single-object path, which checks the SVG through recraft.InspectSVG before storing
// a byte of it. Which door a type goes through is this package's business; whether the bucket can
// keep it at all is NOT.
var nonRasterTypes = map[string]struct{}{
	ContentTypeSVG: {},
	ContentTypeGLB: {},
}

// Accepts ASKS THE BUCKET rather than remembering what it once said.
//
// ⚠ THIS USED TO BE A COPY OF THE BUCKET'S RULE, AND THE COPY IS WHAT THE WHOLE DEFECT WAS. While
// it said "raster only", every vector and 3D run was accepted at the door, reserved money, and was
// then refused here a tick later — for as long as the two lists disagreed, which was the entire
// life of the feature. A copy cannot be kept in step by discipline: nothing fails when it drifts.
//
// Two conditions, and both are necessary. The bucket must have a storage path for the type (that
// is its answer, not ours), and this sink must know WHICH of its methods to hand it to — a type
// the bucket learns tomorrow must not be silently posted through the raster door, because that
// refusal would land after the provider had been paid.
func (s *bucketSink) Accepts(contentType string) bool {
	ct := normalizeContentType(contentType)
	if !bucket.CanStoreMediaType(ct) {
		return false
	}
	_, nonRaster := nonRasterTypes[ct]
	return nonRaster || isRasterMediaType(ct)
}

// isRasterMediaType names the types this sink hands to the verbatim picture path. It is the
// complement of nonRasterTypes WITHIN what the bucket accepts, never a second opinion about what
// the bucket accepts.
func isRasterMediaType(ct string) bool {
	switch ct {
	case ContentTypePNG, ContentTypeJPEG, ContentTypeWEBP, ContentTypeGIF:
		return true
	}
	return false
}

func (s *bucketSink) Put(ctx context.Context, raw []byte, contentType, name string) (MintedMedia, error) {
	if len(raw) == 0 {
		return MintedMedia{}, fmt.Errorf("%w: a generated file carried no bytes", errStorageFailed)
	}
	ct := normalizeContentType(contentType)
	if !s.Accepts(ct) {
		return MintedMedia{}, fmt.Errorf("%w: %q", errSinkUnsupported, contentType)
	}
	media, err := s.upload(ctx, raw, ct, name)
	if err != nil {
		return MintedMedia{}, fmt.Errorf("%w: %v", errStorageFailed, err)
	}
	if media == nil || media.GetId() == 0 {
		return MintedMedia{}, fmt.Errorf("%w: the bucket returned no media row", errStorageFailed)
	}
	return MintedMedia{ID: int(media.GetId()), URLs: mediaURLs(media)}, nil
}

// upload picks the storage path by TYPE, which is the only thing that decides it: the raster path
// derives variants from pixels, the non-raster one stores a single verbatim object. Neither is a
// fallback for the other — a mislabelled payload is refused by whichever path it lands in (the
// raster one sniffs, the non-raster one inspects), never quietly re-routed to the other.
func (s *bucketSink) upload(ctx context.Context, raw []byte, ct, name string) (*pb_common.MediaFull, error) {
	if _, ok := nonRasterTypes[ct]; ok {
		return s.files.UploadContentNonRaster(ctx, raw, ct, designMediaFolder, name)
	}
	if isRasterMediaType(ct) {
		return s.files.UploadContentImageVerbatim(ctx, raw, designMediaFolder, name)
	}
	// Unreachable through Put, which asks Accepts first — and that is exactly why it is written
	// down: the alternative to refusing here is posting an unknown type through the picture door
	// on the day Accepts and this switch stop agreeing.
	return nil, fmt.Errorf("%w: %q has no upload route here", errSinkUnsupported, ct)
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
