package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxPatternFilename bounds the echoed/stored original pattern filename (mirrors the
// tech_card_size_pattern.filename / fitting_pattern.filename VARCHAR(255) columns).
const maxPatternFilename = 255

// maxMediaUsageIds bounds one usage lookup. The batch fans out to one placeholder per id per
// registry entry (twenty-one tables after the DESIGN band, 0340/0342/0343), so an unbounded request
// would build a statement with tens of thousands of binds. A library page is ~50 tiles; 500 leaves
// generous headroom while keeping the widest statement near 10.5k placeholders, well under MySQL's
// limit.
const maxMediaUsageIds = 500

// UploadContentImage stores a picture in the media library by one of two routes, chosen by
// preserve_original.
//
// The ordinary route re-encodes the payload into a fresh full-size WebP. That is right for a
// catalogue photograph and wrong for a document: it changes the bytes, so media.content_hash stops
// being the hash of the file the human has, and a flat sketch prints as a re-encode of itself.
//
// preserve_original takes the verbatim route instead (UploadContentImageVerbatim, already in
// production behind the tech-card archive import): the full-size object IS the payload, and the
// smaller variants are derived from it. The promise is EXACT, not general — JPEG, PNG, WebP and
// GIF are stored byte for byte, and everything else is refused rather than quietly re-encoded
// under a flag that says the opposite (see uploadContentImageVerbatim).
func (s *Server) UploadContentImage(ctx context.Context, req *pb_admin.UploadContentImageRequest) (*pb_admin.UploadContentImageResponse, error) {
	if req.GetPreserveOriginal() {
		return s.uploadContentImageVerbatim(ctx, req.GetRawB64Image())
	}
	m, err := s.bucket.UploadContentImage(ctx, req.RawB64Image, s.bucket.GetBaseFolder(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content image",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to upload image: %v", err)
	}
	return &pb_admin.UploadContentImageResponse{
		Media: m,
	}, nil
}

// uploadContentImageVerbatim is the preserve_original branch.
//
// WHY THE PAYLOAD IS DECODED HERE. The verbatim method takes the raw bytes — it has no base64
// envelope to unwrap, because its production caller (the archive import) hands it bytes out of a
// ZIP. The transport of this RPC is a data URL, so the envelope has to come off on this side.
//
// WHY THE FORMAT IS CHECKED HERE TOO. The bucket sniffs the bytes again and refuses what it cannot
// store verbatim — it is the authority, and this gate does not try to replace it. But its refusal
// is a bare error, and mapping every bucket error to Internal would present "HEIC has no verbatim
// path" as a server fault: the operator would be told something went wrong instead of being told
// what to do. So the ONE case a person can act on is named here, in the caller's own terms, and
// with the remedy (drop the flag and get a re-encoded copy). tcflSniffMedia is reused rather than
// re-written: it already mirrors the bucket's sniffer for exactly this kind of routing decision,
// and an `ftyp` box — HEIC, AVIF, mp4 — is ambiguous to it, which is precisely the answer wanted
// here, since none of the three has a verbatim path.
func (s *Server) uploadContentImageVerbatim(ctx context.Context, rawB64Image string) (*pb_admin.UploadContentImageResponse, error) {
	raw, declared, err := rawImageFromDataURL(rawB64Image)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if family, _ := tcflSniffMedia(raw); family != tcflFamilyImage {
		return nil, status.Errorf(codes.InvalidArgument,
			"preserve_original stores JPEG, PNG, WebP and GIF byte for byte; %s has no verbatim path "+
				"(HEIC in particular) — resend without preserve_original to store a re-encoded copy",
			verbatimPayloadName(declared))
	}

	m, err := s.bucket.UploadContentImageVerbatim(ctx, raw, s.bucket.GetBaseFolder(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content image verbatim",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to upload image: %v", err)
	}
	return &pb_admin.UploadContentImageResponse{
		Media: m,
	}, nil
}

// rawImageFromDataURL unwraps "data:[mediatype];base64,[data]" into the bytes it carries plus the
// mediatype the CLIENT declared.
//
// The declared type is returned for the refusal message ONLY and never routes anything: what the
// payload is gets decided by its magic bytes, here and again inside the bucket. The envelope is
// the same one the re-encoding path accepts (bucket.getB64ImageFromString), so switching
// preserve_original on does not also change what a client has to send.
//
// The size ceiling is left to the bucket, which applies it to the decoded bytes a moment later;
// what reaches this function is already bounded by the transport (grpcMaxRecvMsgSize, and the
// REST route's own body cap).
func rawImageFromDataURL(rawB64Image string) ([]byte, string, error) {
	head, payload, ok := strings.Cut(rawB64Image, ";base64,")
	if !ok {
		return nil, "", fmt.Errorf("invalid base64 image format: expected 'data:[mediatype];base64,[data]'")
	}
	_, declared, _ := strings.Cut(head, ":")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return nil, "", fmt.Errorf("invalid base64 payload: %v", err)
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("image payload is empty")
	}
	return raw, declared, nil
}

// verbatimPayloadName is how the refusal above refers to the payload: by the type the client put
// on it when there is one, and impersonally when there is not. An unbounded label is never echoed
// back — a mediatype is a short token, and a client is free to put anything in that slot.
func verbatimPayloadName(declared string) string {
	ct := strings.ToLower(strings.TrimSpace(declared))
	if ct == "" || len(ct) > 64 || strings.ContainsAny(ct, "\n\r") {
		return "this payload"
	}
	return ct
}

// UploadContentVideo
func (s *Server) UploadContentVideo(ctx context.Context, req *pb_admin.UploadContentVideoRequest) (*pb_admin.UploadContentVideoResponse, error) {
	media, err := s.bucket.UploadContentVideo(ctx, req.GetRaw(), s.bucket.GetBaseFolder(), bucket.GetMediaName(), req.ContentType)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content video",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to upload video: %v", err)
	}
	return &pb_admin.UploadContentVideoResponse{
		Media: media,
	}, nil
}

// UploadPattern uploads a raw cut pattern (выкройка) file — PDF or DXF, sniffed from the
// bytes — and returns its url. The file is stored in object storage (not the media
// library) and referenced by tech-card per-size patterns and fitting iteration patterns;
// the object extension (.pdf / .dxf) carries the file type.
func (s *Server) UploadPattern(ctx context.Context, req *pb_admin.UploadPatternRequest) (*pb_admin.UploadPatternResponse, error) {
	if len(req.GetFilename()) > maxPatternFilename {
		return nil, status.Errorf(codes.InvalidArgument, "filename must be at most %d characters", maxPatternFilename)
	}
	url, sizeBytes, err := s.bucket.UploadPatternFile(ctx, req.GetRaw(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload pattern file",
			slog.String("err", err.Error()),
		)
		// A rejected payload (empty / too large / not a PDF or DXF) is the client's
		// fault; anything else (e.g. an S3 PutObject failure) is an internal error.
		code := codes.Internal
		if errors.Is(err, bucket.ErrInvalidPattern) {
			code = codes.InvalidArgument
		}
		return nil, status.Errorf(code, "failed to upload pattern: %v", err)
	}
	return &pb_admin.UploadPatternResponse{
		Url:       url,
		Filename:  req.GetFilename(),
		SizeBytes: sizeBytes,
	}, nil
}

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	resp := &pb_admin.DeleteFromBucketResponse{}

	// Capture the media's object URLs before the row is gone so the backing S3
	// objects can be removed afterwards; deleting only the row leaves orphaned,
	// still-public CDN files (cost + data-leak). Best effort: a load failure only
	// means we can't clean S3, not that we should block the delete.
	media, mediaErr := s.repo.Media().GetMediaById(ctx, int(req.Id))
	if mediaErr != nil {
		slog.Default().WarnContext(ctx, "can't load media before delete; S3 objects may be orphaned",
			slog.Int("id", int(req.Id)), slog.String("err", mediaErr.Error()))
	}

	err := s.repo.Media().DeleteMediaById(ctx, int(req.Id))
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.FailedPrecondition,
				"media is still referenced (product, archive, model, fitting or tech card) and cannot be deleted")
		}
		slog.Default().ErrorContext(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete media: %v", err)
	}

	// The row is gone; remove the S3 objects it referenced. Failures here only leak
	// bytes (already de-referenced), so log and continue rather than fail the RPC.
	if media != nil {
		if delErr := s.bucket.DeleteObjects(ctx, media.FullSizeMediaURL, media.CompressedMediaURL, media.ThumbnailMediaURL); delErr != nil {
			slog.Default().ErrorContext(ctx, "media row deleted but S3 objects may be orphaned",
				slog.Int("id", int(req.Id)), slog.String("err", delErr.Error()))
		}
	}

	err = s.repo.Hero().RefreshHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "media deleted but failed to refresh hero: %v", err)
	}
	return resp, nil
}

// ListObjects
func (s *Server) ListObjectsPaged(ctx context.Context, req *pb_admin.ListObjectsPagedRequest) (*pb_admin.ListObjectsPagedResponse, error) {
	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)
	limit, offset := clampPagination(int(req.Limit), int(req.Offset))
	list, err := s.repo.Media().ListMediaPaged(ctx, limit, offset, of)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list objects from bucket",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to list media: %v", err)
	}

	entities := make([]*pb_common.MediaFull, 0, len(list))
	for _, m := range list {
		entities = append(entities, dto.ConvertEntityToCommonMedia(&m))
	}

	return &pb_admin.ListObjectsPagedResponse{
		List: entities,
	}, nil
}

// GetMediaUsage reports where each requested media item is still referenced.
//
// Without it the library cannot distinguish a free file from one on the storefront, and
// DeleteFromBucket's FailedPrecondition is the operator's only (useless) feedback: the FK
// refuses, and nothing says which of the twenty-one referencing columns held the file.
//
// Every requested id is answered, including unreferenced and non-existent ones, which come
// back with an empty refs list. Omitting them would make "unused" indistinguishable from a
// response the client failed to match up.
//
// THE KIND DICTIONARY, as the registry emits it (internal/store/content/media_usage.go):
// product | archive | model | material | task | tech_card | fitting | sample | design_picture |
// design_sheet_version | design_edit_layer. The last three are the DESIGN band and all three hold
// their media with ON DELETE RESTRICT, so they are the kinds an operator is most likely to meet on
// a refused delete — a minted sheet version a printed Rev.N depends on, a picture of the band, and
// the base a saved edit layer was drawn over. A kind that reaches the client without being in this
// list is a registry entry somebody added without deciding what the operator should be shown.
func (s *Server) GetMediaUsage(ctx context.Context, req *pb_admin.GetMediaUsageRequest) (*pb_admin.GetMediaUsageResponse, error) {
	// Deduplicate before counting against the cap: a client repeating an id must not be able
	// to burn the budget, and the store would only fold the duplicates away anyway.
	seen := make(map[int]struct{}, len(req.GetMediaIds()))
	ids := make([]int, 0, len(req.GetMediaIds()))
	for _, id := range req.GetMediaIds() {
		if id <= 0 {
			return nil, status.Errorf(codes.InvalidArgument, "media id must be positive, got %d", id)
		}
		if _, dup := seen[int(id)]; dup {
			continue
		}
		seen[int(id)] = struct{}{}
		ids = append(ids, int(id))
	}
	if len(ids) > maxMediaUsageIds {
		return nil, status.Errorf(codes.InvalidArgument,
			"at most %d media ids per request, got %d", maxMediaUsageIds, len(ids))
	}
	if len(ids) == 0 {
		return &pb_admin.GetMediaUsageResponse{}, nil
	}

	usage, err := s.repo.Media().GetMediaUsage(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get media usage",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get media usage: %v", err)
	}

	usages := make([]*pb_admin.MediaUsage, 0, len(ids))
	for _, id := range ids {
		refs := make([]*pb_admin.MediaUsageRef, 0, len(usage[id]))
		for _, r := range usage[id] {
			refs = append(refs, &pb_admin.MediaUsageRef{
				Kind:     r.Kind,
				EntityId: int32(r.EntityId),
				Label:    r.Label,
				Slot:     r.Slot,
			})
		}
		usages = append(usages, &pb_admin.MediaUsage{
			MediaId: int32(id),
			Refs:    refs,
		})
	}

	return &pb_admin.GetMediaUsageResponse{
		Usages: usages,
	}, nil
}
