package bucket

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────────────── NON-RASTER MEDIA: SVG AND GLB ───────────────────────
//
// WHY THIS PATH EXISTS AT ALL. Every media path above this line is a RASTER path: it sniffs the
// bytes for a picture format, reads a header for dimensions, decodes a frame, and derives a
// compressed variant, a thumbnail and a blurhash from it. None of that has a meaning for a vector
// drawing or for a 3D model — there is no frame to resize and no pixel to blur — so a "just widen
// the allowlist" change would have produced a decode failure one line further down.
//
// A non-raster file is therefore stored the way a VIDEO already is (see video.go): ONE object,
// verbatim, with the content type the browser needs, and one media row whose three url slots all
// point at it. That shape is what makes the row visible to every existing reader — GetMediaById,
// GetMediaByIds, the media library, and GetMediaUsage, which reports a design_picture that
// references it exactly as it reports a raster one. No column and no migration is involved: the
// media table has never carried a content type, and the object's extension is where the file type
// has always lived (the pattern path says the same thing in its own header).
//
// WHAT IS NOT SHARED WITH THE RASTER PATH, and must not be:
//
//   - THE SVG IS NOT STORED UNTIL recraft.InspectSVG HAS PASSED IT. These bytes end up on our own
//     public CDN host and then in an administrator's browser, where an SVG is a DOCUMENT, not a
//     picture: <script>, on* handlers, javascript: urls, <foreignObject> and declared XML entities
//     all run or expand. The check is here, in the storage path, rather than at the one caller that
//     happens to exist today, precisely so that no future caller can store an unchecked one. It
//     also refuses a raster arriving under a vector name, which is how a mis-configured "vector"
//     model would otherwise fill the band with traced PNGs called SVGs.
//   - BOTH CEILINGS REFUSE, THEY DO NOT TRUNCATE. A model cut at a boundary is a file that opens in
//     nothing, and a silently shortened SVG is a drawing with pieces missing; both are worse than an
//     error, because they look like storage succeeded.
const (
	// maxVectorPayloadBytes is the SVG ceiling. It is recraft's own number rather than a second
	// opinion: InspectSVG enforces the same constant a few lines below, and two ceilings that could
	// drift would mean a file the checker accepts and the bucket refuses (or worse, the reverse).
	// Stated here as well so the bound is visible where the storage happens.
	maxVectorPayloadBytes = recraft.MaxSVGBytes
	// maxModelPayloadBytes is the GLB ceiling, the same 64 MiB the 3D provider refuses above
	// (meshy.maxModelBytes). Equal on purpose: a model our own transport agreed to download must
	// not then be refused by our own bucket — that failure would land AFTER the generation was
	// paid for.
	maxModelPayloadBytes = 64 << 20
	// glbHeaderBytes is the fixed glTF-binary header: magic, container version, total length.
	glbHeaderBytes = 12
)

// ─────────────────── WHAT THIS PACKAGE CAN KEEP, ASKED FROM OUTSIDE ───────────────────

// mediaStorableTypes is the answer to one question: "does a file of this content type have a media
// storage path in this package at all?" Four raster types through UploadContentImageVerbatim, two
// non-raster ones through UploadContentNonRaster.
//
// ⚠ IT IS EXPORTED (CanStoreMediaType / StorableMediaTypes) BECAUSE SOMEBODY HAS TO DECIDE BEFORE
// THEY HAVE THE BYTES. The design worker must know whether a route's output can be kept BEFORE it
// pays the provider for it; the only alternative to asking here is a copy of this list living in
// that package, and a copy is what turned "the bucket cannot store SVG" into a whole generation
// route that charged money and then failed, every single time, for as long as the copy stood.
//
// This is an answer about the TYPE, not about a payload: the bytes are still checked by whichever
// path stores them (the raster one sniffs, the non-raster one inspects). "Storable in principle"
// and "this file is acceptable" are different questions and must not be collapsed.
var mediaStorableTypes = map[ContentType]struct{}{
	contentTypeJPEG: {},
	contentTypePNG:  {},
	contentTypeWEBP: {},
	contentTypeGIF:  {},
	contentTypeSVG:  {},
	contentTypeGLB:  {},
}

// CanStoreMediaType reports whether a file of this content type has a media storage path here.
// Parameters ("image/svg+xml; charset=utf-8") and casing are ignored, so a cosmetic difference in
// what a provider labels its answer cannot read as an unsupported type.
func CanStoreMediaType(contentType string) bool {
	_, ok := mediaStorableTypes[ContentType(normalizeContentTypeLabel(contentType))]
	return ok
}

// StorableMediaTypes lists those types, sorted. It exists so a caller can iterate the REAL set
// instead of writing its own copy of it down — including in a test, where a copied list is how a
// probe comes to certify the list rather than the behaviour.
func StorableMediaTypes() []string {
	out := make([]string, 0, len(mediaStorableTypes))
	for ct := range mediaStorableTypes {
		out = append(out, string(ct))
	}
	sort.Strings(out)
	return out
}

// ErrInvalidNonRaster marks a refused non-raster payload — empty, oversized, not the type it
// claims to be, or an SVG carrying active content. It is a sentinel for the same reason
// ErrInvalidPattern is one: the API layer must be able to tell "this file is not acceptable"
// (InvalidArgument) from "S3 broke" (Internal).
var ErrInvalidNonRaster = errors.New("invalid non-raster media file")

// glbMagic opens every glTF binary container.
var glbMagic = []byte("glTF")

// UploadContentNonRaster stores one non-raster media file — today an SVG vector or a GLB model —
// verbatim, and records the media row that makes it a first-class member of the library.
//
// The declared content type is CHECKED AGAINST THE BYTES, never trusted: the SVG branch runs
// recraft.InspectSVG (which refuses a raster, a malformed document and anything executable) and the
// GLB branch reads the container header. That is the same discipline uploadVideoObj applies to a
// declared video, and it is what keeps the object's content type — the one the browser will obey —
// a statement about the payload rather than about the caller.
func (b *Bucket) UploadContentNonRaster(ctx context.Context, raw []byte, contentType, folder, objectName string) (*pb_common.MediaFull, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: payload is empty", ErrInvalidNonRaster)
	}
	ct := ContentType(normalizeContentTypeLabel(contentType))

	// width and height describe the stored object. A vector usually says how big it is and the
	// number is worth keeping (a reader lays the picture out with it); a model has no such thing,
	// and 0 is what the video path already stores for "this file has no pixel dimensions".
	var width, height int

	switch ct {
	case contentTypeSVG:
		if len(raw) > maxVectorPayloadBytes {
			return nil, fmt.Errorf("%w: vector payload too large: %d bytes, max %d bytes",
				ErrInvalidNonRaster, len(raw), maxVectorPayloadBytes)
		}
		// ⚠ THE CHECK THAT MUST NOT MOVE. Everything below this line puts bytes on a public host
		// under our own domain; an SVG that reaches it unchecked is executable content we serve
		// ourselves. InspectSVG refuses <script>, on*/javascript: attributes, <foreignObject>,
		// declared XML entities, and a raster wearing a vector's name.
		stats, err := recraft.InspectSVG(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidNonRaster, err)
		}
		width, height = svgPixelSize(stats)
	case contentTypeGLB:
		if err := checkModelPayload(raw); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %q has no non-raster storage path; this one stores %s and %s",
			ErrInvalidNonRaster, contentType, contentTypeSVG, contentTypeGLB)
	}

	// ONE OBJECT, uploaded through the same verb the raster paths use, so the hash it returns is
	// the sha of the bytes as they lie in the bucket — the invariant media.content_hash carries
	// everywhere else, kept here too rather than re-derived from the payload.
	url, sha, err := b.uploadImageToBucket(ctx, bytes.NewReader(raw), folder,
		fmt.Sprintf("%s-%s", objectName, "og"), ct)
	if err != nil {
		return nil, fmt.Errorf("failed to upload %s object to bucket: %w", ct, err)
	}

	info := &pb_common.MediaInfo{MediaUrl: url, Width: int32(width), Height: int32(height)}
	mediaID, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		// All three slots point at the one object, exactly as the video path does: there is no
		// smaller variant to make, and a reader that asks for the thumbnail must still get a url
		// that resolves rather than an empty string.
		FullSizeMediaURL:   url,
		FullSizeWidth:      width,
		FullSizeHeight:     height,
		CompressedMediaURL: url,
		CompressedWidth:    width,
		CompressedHeight:   height,
		ThumbnailMediaURL:  url,
		ThumbnailWidth:     width,
		ThumbnailHeight:    height,
		// No blurhash: there is no raster to average. Invalid, not empty — "not computed" is a
		// different fact from "computed and empty".
		BlurHash:    sql.NullString{},
		ContentHash: sql.NullString{String: sha, Valid: sha != ""},
	})
	if err != nil {
		// The object is in the bucket and no row references it. The caller gets nil back and would
		// have no url to compensate with, so it is taken back here — the same remedy, and for the
		// same reason, as the video path.
		b.cleanupUploadedVariants(info)
		slog.Default().ErrorContext(ctx, "can't add non-raster media to db",
			slog.String("content_type", string(ct)), slog.String("err", err.Error()))
		return nil, fmt.Errorf("failed to add media to db: %w", err)
	}

	return &pb_common.MediaFull{
		Id: int32(mediaID),
		Media: &pb_common.MediaItem{
			FullSize:   info,
			Compressed: info,
			Thumbnail:  info,
		},
		ContentHash: sha,
	}, nil
}

// ─────────────────────── A MODEL WITH ITS PREVIEW: ONE ROW ───────────────────────
//
// WHY THE PREVIEW LIVES ON THE MODEL'S OWN MEDIA ROW AND NOT ON A SECOND ONE (round 18, D-29). The
// generative 3D route stores a model as TWO media rows and TWO design_picture rows — the .glb and a
// raster poster that follows it in write order — and the client has to pair them back by adjacency
// (its own comment: «threedResults pairs a .glb with the raster that follows it in WRITE order»).
// A hand-uploaded model had no poster at all, so its tile was the .glb handed to an <img>: the
// «broken frame a person reads as "the server broke"». The media row already has the three slots
// this needs, and every reader already reads them the right way round: the tile draws `thumbnail`,
// the model viewer reads `full_size` and decides «this is a model» by the .glb suffix of THAT url.
// Putting the preview into the thumbnail and compressed slots therefore fixes the tile in every
// client that exists today, and leaves the model detection untouched. One object by construction:
// nothing to pair, nothing to order, nothing that can arrive without its other half.
//
// What this row promises differently from a plain raster row: `full_size` has no pixel dimensions
// (0×0, as the video path stores «no frame»), `content_hash` is the sha of the MODEL, and the
// compressed/thumbnail dimensions describe the preview.

// modelPreview is a checked preview: its sniffed type, header dimensions and decoded frame.
type modelPreview struct {
	ct            ContentType
	width, height int
	frame         image.Image
}

// checkModelPayload is the whole model gate — emptiness, the byte ceiling and the container
// header — shared by the preview-less and the preview-carrying door so the two cannot drift.
func checkModelPayload(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: payload is empty", ErrInvalidNonRaster)
	}
	if len(raw) > maxModelPayloadBytes {
		return fmt.Errorf("%w: model payload too large: %d bytes, max %d bytes",
			ErrInvalidNonRaster, len(raw), maxModelPayloadBytes)
	}
	if err := checkGLB(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNonRaster, err)
	}
	return nil
}

// checkModelPreview verifies the preview AS STRICTLY AS checkGLB VERIFIES THE MODEL — by the bytes
// and never by a label: the magic decides the type, the header is read against the raster
// ceilings, and the picture is decoded, because a preview that will not decode is a tile nothing
// can draw, stored under a url a person clicks days later.
//
// JPEG, PNG AND WEBP ONLY — the three stills a canvas snapshot produces. A GIF is refused on
// purpose although the verbatim picture path stores it: a preview is a still, and the animated
// pass-through path exists for a different reason. A model, a vector or a video in this field is
// refused with the type it turned out to be, so the message names what to replace.
func checkModelPreview(raw []byte) (modelPreview, error) {
	if len(raw) > maxRawImagePayloadBytes {
		return modelPreview{}, fmt.Errorf("%w: preview payload too large: %d bytes, max %d bytes",
			ErrInvalidNonRaster, len(raw), maxRawImagePayloadBytes)
	}
	ct := sniffImageType(raw)
	switch ct {
	case contentTypeJPEG, contentTypePNG, contentTypeWEBP:
	case "":
		return modelPreview{}, fmt.Errorf("%w: the preview is not a picture: no JPEG, PNG or WebP header",
			ErrInvalidNonRaster)
	default:
		return modelPreview{}, fmt.Errorf("%w: the preview is %s; a model preview is a still JPEG, PNG or WebP",
			ErrInvalidNonRaster, ct)
	}
	cfg, err := imageHeaderConfig(ct, raw)
	if err != nil {
		return modelPreview{}, fmt.Errorf("%w: cannot read the preview header: %v", ErrInvalidNonRaster, err)
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return modelPreview{}, fmt.Errorf("%w: preview dimensions %dx%d exceed maximum allowed %dpx",
			ErrInvalidNonRaster, cfg.Width, cfg.Height, maxImageDimension)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return modelPreview{}, fmt.Errorf("%w: preview too large: %dx%d exceeds %d-pixel limit",
			ErrInvalidNonRaster, cfg.Width, cfg.Height, maxImagePixels)
	}
	img, err := decodeImage(raw, ct)
	if err != nil {
		return modelPreview{}, fmt.Errorf("%w: the preview does not decode: %v", ErrInvalidNonRaster, err)
	}
	return modelPreview{ct: ct, width: cfg.Width, height: cfg.Height, frame: img}, nil
}

// UploadContentModel stores a GLB model and, when one is handed in, the raster preview that stands
// in for it — as ONE media row. See the section comment above for why one row.
//
// BOTH FILES ARE CHECKED BEFORE EITHER IS STORED, and a failure after the model went up takes the
// model back (cleanupUploadedVariants, the remedy every path here uses): the caller asked for a
// model WITH its preview, and a row carrying only the model would be precisely the half-object
// this verb exists not to mint. An empty preview is the old door, unchanged.
func (b *Bucket) UploadContentModel(ctx context.Context, raw, preview []byte, folder, objectName string) (*pb_common.MediaFull, error) {
	if len(preview) == 0 {
		return b.UploadContentNonRaster(ctx, raw, string(contentTypeGLB), folder, objectName)
	}
	if err := checkModelPayload(raw); err != nil {
		return nil, err
	}
	pv, err := checkModelPreview(preview)
	if err != nil {
		return nil, err
	}

	// THE MODEL FIRST, under the same "-og" name and through the same verb as the preview-less
	// door, so its sha is the sha of the bytes as they lie in the bucket — the invariant
	// media.content_hash carries everywhere else.
	modelURL, sha, err := b.uploadImageToBucket(ctx, bytes.NewReader(raw), folder,
		fmt.Sprintf("%s-%s", objectName, "og"), contentTypeGLB)
	if err != nil {
		return nil, fmt.Errorf("failed to upload %s object to bucket: %w", contentTypeGLB, err)
	}
	// 0×0: a model has no pixel dimensions, exactly as the preview-less row and the video row say.
	model := &pb_common.MediaInfo{MediaUrl: modelURL}

	// THE PREVIEW'S VARIANTS, built the way the verbatim picture path builds them (same qualities,
	// same thumbnail height), so a model's tile and a picture's tile are the same kind of object.
	compressed, _, err := b.uploadSingleImage(ctx, pv.frame, 60, folder,
		fmt.Sprintf("%s-%s", objectName, "compressed"))
	if err != nil {
		b.cleanupUploadedVariants(model)
		return nil, fmt.Errorf("failed to upload the model preview: %w", err)
	}
	thumbImg := resizeImage(pv.frame, 1080)
	thumbnail, _, err := b.uploadSingleImage(ctx, thumbImg, 90, folder,
		fmt.Sprintf("%s-%s", objectName, "thumb"))
	if err != nil {
		b.cleanupUploadedVariants(model, compressed)
		return nil, fmt.Errorf("failed to upload the model preview thumbnail: %w", err)
	}
	h, err := getBlurHash(thumbImg)
	if err != nil {
		// Decorative placeholder, same policy as the picture paths: a preview that will not
		// blur-hash is still a preview.
		slog.Default().WarnContext(ctx, "model preview blurhash failed; storing empty",
			slog.String("err", err.Error()))
		h = ""
	}

	mediaID, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   model.MediaUrl,
		FullSizeWidth:      0,
		FullSizeHeight:     0,
		CompressedMediaURL: compressed.MediaUrl,
		CompressedWidth:    int(compressed.Width),
		CompressedHeight:   int(compressed.Height),
		ThumbnailMediaURL:  thumbnail.MediaUrl,
		ThumbnailWidth:     int(thumbnail.Width),
		ThumbnailHeight:    int(thumbnail.Height),
		BlurHash:           sql.NullString{String: h, Valid: h != ""},
		// The hash describes the MODEL — the full-size object, the one the archive downloads.
		ContentHash: sql.NullString{String: sha, Valid: sha != ""},
	})
	if err != nil {
		b.cleanupUploadedVariants(model, compressed, thumbnail)
		slog.Default().ErrorContext(ctx, "can't add model media to db",
			slog.String("err", err.Error()))
		return nil, fmt.Errorf("failed to add media to db: %w", err)
	}

	return &pb_common.MediaFull{
		Id: int32(mediaID),
		Media: &pb_common.MediaItem{
			FullSize:   model,
			Compressed: compressed,
			Thumbnail:  thumbnail,
			Blurhash:   h,
		},
		ContentHash: sha,
	}, nil
}

// checkGLB verifies the glTF binary container header — the only part of the format we have any
// business reading.
//
// The length field is not decoration: the spec defines it as the total size of the container, so a
// file that declares MORE than arrived is a TRUNCATED download, and storing it would put a model
// that opens in nothing behind a url a person will click days later. The reverse (more bytes than
// declared) is left alone: trailing junk after a complete container is somebody else's untidiness,
// not a broken model, and refusing it would fail a run that has already been paid for.
func checkGLB(raw []byte) error {
	if len(raw) < glbHeaderBytes || !bytes.HasPrefix(raw, glbMagic) {
		return fmt.Errorf("the bytes are not a GLB: no glTF binary header")
	}
	if v := binary.LittleEndian.Uint32(raw[4:8]); v != 2 {
		return fmt.Errorf("glTF binary container version %d is not 2", v)
	}
	if declared := binary.LittleEndian.Uint32(raw[8:12]); uint64(declared) > uint64(len(raw)) {
		return fmt.Errorf("the GLB header declares %d bytes but %d arrived: the file is truncated",
			declared, len(raw))
	}
	return nil
}

// svgPixelSize turns what the root element SAYS about its size into the numbers the media row
// stores, or (0, 0) when it says nothing usable.
//
// width/height are read first because they are the drawing's intrinsic size; they are also the
// pair most often written as a percentage ("100%"), which is a statement about the container and
// not about the picture, so the viewBox is the fallback rather than the other way round. Anything
// that does not parse, is not positive, or is larger than the raster ceiling reads as UNKNOWN: a
// zero pair is honest, while a guessed one silently mis-lays every screen that trusts it.
func svgPixelSize(s recraft.SVGStats) (int, int) {
	if w, wok := svgLength(s.Width); wok {
		if h, hok := svgLength(s.Height); hok {
			return w, h
		}
	}
	return viewBoxSize(s.ViewBox)
}

// svgLength parses one SVG length attribute. Only unitless numbers and explicit pixels are taken:
// every other CSS unit (em, %, mm) needs a rendering context this process does not have.
func svgLength(v string) (int, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimSuffix(v, "px")
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f <= 0 || f > float64(maxImageDimension) {
		return 0, false
	}
	return int(f + 0.5), true
}

// viewBoxSize reads the width and height out of a "min-x min-y width height" viewBox.
func viewBoxSize(v string) (int, int) {
	fields := strings.FieldsFunc(strings.TrimSpace(v), func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) != 4 {
		return 0, 0
	}
	w, wok := svgLength(fields[2])
	h, hok := svgLength(fields[3])
	if !wok || !hok {
		return 0, 0
	}
	return w, h
}

// normalizeContentTypeLabel strips the parameters a caller may append ("image/svg+xml; charset=utf-8")
// and lowercases the rest, so a cosmetic difference cannot read as an unsupported type.
func normalizeContentTypeLabel(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}
