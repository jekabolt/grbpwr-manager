package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"strconv"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/image/webp"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────── refusals ───────────────────────────

// designErrorDomain is the domain of every ErrorInfo this file attaches. One domain, one place to
// look, and a client can branch on Reason without matching English prose.
const designErrorDomain = "design.grbpwr.com"

// designRefusals is THE table. Every refusal of 10 §3 appears exactly once, with its gRPC code and
// the machine token the client branches on.
//
// WHY A TABLE AND NOT A SWITCH PER HANDLER. The taxonomy is a contract: a code the client has
// never heard of is a code it cannot undo, and the one way that happens is a second, divergent
// mapping growing next to the first. In particular the residual 1062 of the lazy slot birth is
// mapped INTO slot_rev_mismatch by the store, so it can never reach a client as Internal.
var designRefusals = []struct {
	err    error
	code   codes.Code
	reason string
}{
	{entity.ErrDesignSlotRevMismatch, codes.Aborted, "slot_rev_mismatch"},
	{entity.ErrDesignLayerRevMismatch, codes.Aborted, "layer_rev_mismatch"},
	{entity.ErrDesignForeignCardPlate, codes.FailedPrecondition, "foreign_card_plate"},
	{entity.ErrDesignCompositePlate, codes.FailedPrecondition, "composite_plate"},
	{entity.ErrDesignHiddenPlate, codes.FailedPrecondition, "hidden_plate"},
	{entity.ErrDesignWrongKind, codes.FailedPrecondition, "wrong_kind"},
	{entity.ErrDesignPictureAlreadyInSlot, codes.FailedPrecondition, "picture_already_in_slot"},
	{entity.ErrDesignDetailNameRequired, codes.FailedPrecondition, "detail_name_required"},
	{entity.ErrDesignSlotFilled, codes.FailedPrecondition, "slot_filled"},
	{entity.ErrDesignSlotInVersion, codes.FailedPrecondition, "slot_in_version"},
	{entity.ErrDesignNotADetailSlot, codes.FailedPrecondition, "not_a_detail_slot"},
	{entity.ErrDesignInSlot, codes.FailedPrecondition, "in_slot"},
	{entity.ErrDesignInVersion, codes.FailedPrecondition, "in_version"},
	{entity.ErrDesignLiveRunInput, codes.FailedPrecondition, "live_run_input"},
	{entity.ErrDesignLiveCropParent, codes.FailedPrecondition, "live_crop_parent"},
	{entity.ErrDesignNotComposite, codes.FailedPrecondition, "not_composite"},
	{entity.ErrDesignEmptyLayer, codes.FailedPrecondition, "empty_layer"},
	{entity.ErrDesignForeignMedia, codes.FailedPrecondition, "foreign_media"},
	{entity.ErrDesignStrokesTooLarge, codes.InvalidArgument, "strokes_too_large"},
	// InvalidArgument, NOT NotFound. «Unknown ghost_view» answered with NotFound would send a
	// person to reload a band that is perfectly present, instead of fixing the request.
	{entity.ErrDesignInvalidArgument, codes.InvalidArgument, "invalid_argument"},
	{entity.ErrDesignNotFound, codes.NotFound, "not_found"},
	{entity.ErrDesignNotImplemented, codes.Unimplemented, "not_implemented"},
	// ─── минт версии листа ───
	//
	// bench_moved — ВТОРОЙ ЗАМОК, а не дубль slot_rev_mismatch: тот стережёт ОДНУ постановку
	// плиты, этот — весь состав, который человек видел в диалоге минта.
	{entity.ErrDesignBenchMoved, codes.Aborted, "bench_moved"},
	{entity.ErrDesignMixedNeedsConsent, codes.FailedPrecondition, "mixed_needs_consent"},
	{entity.ErrDesignUploadedFitUnconfirmed, codes.FailedPrecondition, "uploaded_fit_unconfirmed"},
	{entity.ErrDesignFitMismatch, codes.FailedPrecondition, "fit_mismatch"},
	{entity.ErrDesignSheetMinUnmet, codes.FailedPrecondition, "sheet_min_unmet"},
	{entity.ErrDesignUnrepinnedCallouts, codes.FailedPrecondition, "unrepinned_callouts"},
	// plates_not_in_document — ПОЯС П-А. Клиенту он в норме не показывается вовсе: плиты
	// вкладывает сервер. Если он всё-таки приехал, значит верстак уехал между чтением и
	// транзакцией, и правильный ответ человеку — «перечитай полосу», а не молча оторванные детали.
	{entity.ErrDesignPlatesNotInDocument, codes.FailedPrecondition, "plates_not_in_document"},
}

// designError translates a store error into the status the client knows how to act on. metadata is
// optional extra the caller wants carried — the slot's current state, for instance.
//
// If the detail cannot be attached THE REFUSAL STILL GOES OUT, plain: a client that cannot read
// the reason falls back to what it always showed, while one that lost the refusal itself would be
// handed a broken screen.
func designError(ctx context.Context, op string, err error, metadata map[string]string) error {
	for _, r := range designRefusals {
		if errors.Is(err, r.err) {
			st := status.New(r.code, err.Error())
			md := map[string]string{"reason": r.reason}
			for k, v := range metadata {
				md[k] = v
			}
			withDetails, derr := st.WithDetails(&errdetails.ErrorInfo{
				Reason: r.reason, Domain: designErrorDomain, Metadata: md,
			})
			if derr != nil {
				return st.Err()
			}
			return withDetails.Err()
		}
	}
	slog.Default().ErrorContext(ctx, op, slog.String("err", err.Error()))
	return status.Errorf(codes.Internal, "%s", op)
}

// designSlotDetails is what rides along with slot_rev_mismatch. THE PLATE IS IN IT, not only its
// id: the whole point of this refusal is to show the person what stands in the slot right now, and
// a bare id shows nothing.
func designSlotDetails(slot *entity.DesignBenchSlot) map[string]string {
	if slot == nil {
		return nil
	}
	md := map[string]string{
		"slot_id":  strconv.Itoa(slot.Id),
		"slot_rev": strconv.Itoa(slot.SlotRev),
		"view_key": slot.ViewKey,
		"set_by":   slot.SetBy,
		"picture_id": func() string {
			if slot.PictureId.Valid {
				return strconv.Itoa(int(slot.PictureId.Int32))
			}
			return "0"
		}(),
	}
	if slot.Picture != nil {
		md["picture_source_class"] = slot.Picture.SourceClass
		if slot.Picture.Media != nil {
			md["picture_thumbnail"] = slot.Picture.Media.ThumbnailMediaURL
		}
	}
	return md
}

// designActor is the username stamped on every write of the band. Without it a race between two
// authors is invisible on the row.
func designActor(ctx context.Context) string {
	return authsrv.GetAdminUsername(ctx)
}

// ─────────────────────────── reads ───────────────────────────

// GetDesignBand is ONE read of the whole band.
func (s *Server) GetDesignBand(ctx context.Context, req *pb_admin.GetDesignBandRequest) (*pb_admin.GetDesignBandResponse, error) {
	band, err := s.repo.Design().GetBand(ctx, int(req.GetTechCardId()), design.DefaultRunPageLimit)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band", err, nil)
	}
	resp := &pb_admin.GetDesignBandResponse{
		Bench:          designBenchToPb(band.Bench),
		VersionNumbers: intsToInt32(band.VersionNumbers),
		Journal:        designIssuesToPb(band.Journal),
		Budget:         designBudgetToPb(band.Budget),
		References:     designReferencesToPb(band.References),
		Layers:         designLayersToPb(band.Layers, false),
		TotalRuns:      int32(band.TotalRuns),
		ArchivedRuns:   int32(band.ArchivedRuns),
		MaxRrev:        int32(band.MaxRrev),
		ColourRecipes:  designColourRecipesToPb(ctx, band.ColourRecipes),
		HiddenByRun:    intMapToPb(band.HiddenByRun),
		HiddenByBatch:  intMapToPb(band.HiddenByBatch),
		Runs:           designRunsToPb(ctx, band.Runs),
		Batches:        designBatchesToPb(band.Batches),
		NextPageToken:  design.EncodePageToken(band.NextCursor, band.NextBatchCursor, true),
	}
	if band.LatestVersion != nil {
		resp.LatestVersion = designSheetVersionToPb(ctx, *band.LatestVersion)
	}
	s.stripDesignCosting(ctx, resp.Runs, resp.Budget)
	return resp, nil
}

// ListDesignRuns is one page of the history, with the upload shelves of the same page.
func (s *Server) ListDesignRuns(ctx context.Context, req *pb_admin.ListDesignRunsRequest) (*pb_admin.ListDesignRunsResponse, error) {
	if req.GetLimit() > design.MaxRunPageLimit {
		return nil, status.Errorf(codes.InvalidArgument,
			"limit %d is above the ceiling of %d", req.GetLimit(), design.MaxRunPageLimit)
	}
	runCursor, batchCursor, tokenArchived, ok := design.DecodePageToken(req.GetPageToken())
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "page_token is not a cursor of this list")
	}
	includeArchived := req.GetIncludeArchived()
	if req.GetPageToken() != "" {
		// THE TOKEN WINS. Its cursor was taken over a particular row set, and continuing it under
		// a different filter is what silently skips rows halfway through a pagination. Starting a
		// new listing (empty token) is where the request's own flag decides.
		includeArchived = tokenArchived
	}
	page, err := s.repo.Design().ListRuns(ctx, entity.DesignRunPage{
		TechCardId:      int(req.GetTechCardId()),
		Limit:           int(req.GetLimit()),
		Cursor:          runCursor,
		BatchCursor:     batchCursor,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, designError(ctx, "failed to list design runs", err, nil)
	}
	resp := &pb_admin.ListDesignRunsResponse{
		Runs:          designRunsToPb(ctx, page.Runs),
		Batches:       designBatchesToPb(page.Batches),
		NextPageToken: design.EncodePageToken(page.NextCursor, page.NextBatchCursor, includeArchived),
	}
	s.stripDesignCosting(ctx, resp.Runs, nil)
	return resp, nil
}

// GetDesignSheetVersion reads ONE frozen version whole.
func (s *Server) GetDesignSheetVersion(ctx context.Context, req *pb_admin.GetDesignSheetVersionRequest) (*pb_admin.GetDesignSheetVersionResponse, error) {
	full, err := s.repo.Design().GetSheetVersion(ctx, int(req.GetTechCardId()), int(req.GetVersionNumber()))
	if err != nil {
		return nil, designError(ctx, "failed to read the design sheet version", err, nil)
	}
	return &pb_admin.GetDesignSheetVersionResponse{
		Version: designSheetVersionToPb(ctx, full.Version),
		Issues:  designIssuesToPb(full.Issues),
	}, nil
}

// GetDesignEditLayer reads ONE layer WITH its strokes — the only place strokes are served.
func (s *Server) GetDesignEditLayer(ctx context.Context, req *pb_admin.GetDesignEditLayerRequest) (*pb_admin.GetDesignEditLayerResponse, error) {
	layer, err := s.repo.Design().GetEditLayer(ctx, int(req.GetTechCardId()), int(req.GetLayerId()))
	if err != nil {
		return nil, designError(ctx, "failed to read the design edit layer", err, nil)
	}
	return &pb_admin.GetDesignEditLayerResponse{Layer: designLayerToPb(*layer, true)}, nil
}

// ─────────────────────────── the bench ───────────────────────────

// SetDesignBenchSlot places, displaces or unmarks a plate.
func (s *Server) SetDesignBenchSlot(ctx context.Context, req *pb_admin.SetDesignBenchSlotRequest) (*pb_admin.SetDesignBenchSlotResponse, error) {
	ref, err := designSlotRefFromPb(req.GetSlot())
	if err != nil {
		return nil, err
	}
	slot, err := s.repo.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:      int(req.GetTechCardId()),
		Slot:            ref,
		PictureId:       int(req.GetPictureId()),
		ExpectedSlotRev: int(req.GetExpectedSlotRev()),
		NewDetailName:   strings.TrimSpace(req.GetNewDetailName()),
		Actor:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to set the design bench slot", err, designSlotDetails(slot))
	}
	return &pb_admin.SetDesignBenchSlotResponse{Slot: designSlotToPb(*slot)}, nil
}

// DeleteDesignDetailSlot removes an EMPTY detail slot that no version quotes.
func (s *Server) DeleteDesignDetailSlot(ctx context.Context, req *pb_admin.DeleteDesignDetailSlotRequest) (*pb_admin.DeleteDesignDetailSlotResponse, error) {
	if err := s.repo.Design().DeleteDetailSlot(ctx, int(req.GetSlotId())); err != nil {
		return nil, designError(ctx, "failed to delete the design detail slot", err, nil)
	}
	return &pb_admin.DeleteDesignDetailSlotResponse{}, nil
}

// SetDesignReferenceRole states which side of the garment a reference is about; an empty role
// clears it and the response then carries no reference.
func (s *Server) SetDesignReferenceRole(ctx context.Context, req *pb_admin.SetDesignReferenceRoleRequest) (*pb_admin.SetDesignReferenceRoleResponse, error) {
	ref, err := s.repo.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: int(req.GetTechCardId()),
		MediaId:    int(req.GetMediaId()),
		Role:       strings.TrimSpace(req.GetRole()),
		Ordinal:    int(req.GetOrdinal()),
		Actor:      designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to set the design reference role", err, nil)
	}
	resp := &pb_admin.SetDesignReferenceRoleResponse{}
	if ref != nil {
		resp.Reference = designReferenceToPb(*ref)
	}
	return resp, nil
}

// ─────────────────────────── pictures ───────────────────────────

// RegisterDesignUpload files ONE gesture as one batch plus its pictures.
func (s *Server) RegisterDesignUpload(ctx context.Context, req *pb_admin.RegisterDesignUploadRequest) (*pb_admin.RegisterDesignUploadResponse, error) {
	if len(req.GetItems()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "an upload batch needs at least one item")
	}
	if strings.TrimSpace(req.GetClientRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "client_request_id is required")
	}
	items := make([]entity.DesignUploadItem, 0, len(req.GetItems()))
	for i, it := range req.GetItems() {
		if it.GetGhostView() != "" && !entity.IsDesignGhostView(it.GetGhostView()) {
			return nil, status.Errorf(codes.InvalidArgument,
				"items.%d.ghost_view %q is not a view of the garment", i, it.GetGhostView())
		}
		items = append(items, entity.DesignUploadItem{
			MediaId: int(it.GetMediaId()), GhostView: it.GetGhostView(),
		})
	}
	in := entity.DesignBatchRegister{
		TechCardId:      int(req.GetTechCardId()),
		ClientRequestId: strings.TrimSpace(req.GetClientRequestId()),
		Items:           items,
		ExpectedSlotRev: int(req.GetExpectedSlotRev()),
		Actor:           designActor(ctx),
	}
	if req.GetTarget() != nil {
		ref, err := designSlotRefFromPb(req.GetTarget())
		if err != nil {
			return nil, err
		}
		in.Target = &ref
	}
	res, err := s.repo.Design().RegisterBatch(ctx, in)
	if err != nil {
		return nil, designError(ctx, "failed to register the design upload", err, nil)
	}
	resp := &pb_admin.RegisterDesignUploadResponse{
		Batch:    designBatchToPb(res.Batch),
		Pictures: designPicturesToPb(res.Pictures),
	}
	if res.Slot != nil {
		resp.Slot = designSlotToPb(*res.Slot)
	}
	return resp, nil
}

// HideDesignPicture is the only persistent verb for picture invisibility.
func (s *Server) HideDesignPicture(ctx context.Context, req *pb_admin.HideDesignPictureRequest) (*pb_admin.HideDesignPictureResponse, error) {
	pic, err := s.repo.Design().HidePicture(ctx, int(req.GetPictureId()), req.GetHidden(), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to set design picture visibility", err, nil)
	}
	return &pb_admin.HideDesignPictureResponse{Picture: designPictureToPb(*pic)}, nil
}

// ArchiveDesignRun flips a presentational, reversible flag on a history row.
func (s *Server) ArchiveDesignRun(ctx context.Context, req *pb_admin.ArchiveDesignRunRequest) (*pb_admin.ArchiveDesignRunResponse, error) {
	run, err := s.repo.Design().ArchiveRun(ctx, int(req.GetRunId()), req.GetArchived(), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to archive the design run", err, nil)
	}
	pb := designRunToPb(ctx, *run)
	s.stripDesignCosting(ctx, []*pb_common.DesignRun{pb}, nil)
	return &pb_admin.ArchiveDesignRunResponse{Run: pb}, nil
}

// RecordDesignSheetIssue writes a printed/shared line into a version's append-only journal.
func (s *Server) RecordDesignSheetIssue(ctx context.Context, req *pb_admin.RecordDesignSheetIssueRequest) (*pb_admin.RecordDesignSheetIssueResponse, error) {
	issue, err := s.repo.Design().RecordSheetIssue(ctx, entity.DesignSheetIssueRecord{
		TechCardId:      int(req.GetTechCardId()),
		VersionNumber:   int(req.GetVersionNumber()),
		Action:          strings.TrimSpace(req.GetAction()),
		ClientRequestId: strings.TrimSpace(req.GetClientRequestId()),
		Actor:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to record the design sheet issue", err, nil)
	}
	return &pb_admin.RecordDesignSheetIssueResponse{Issue: designIssueToPb(*issue)}, nil
}

// ─────────────────────────── edit layers ───────────────────────────

// SaveDesignEditLayer stores a vector layer under compare-and-set on its rev.
func (s *Server) SaveDesignEditLayer(ctx context.Context, req *pb_admin.SaveDesignEditLayerRequest) (*pb_admin.SaveDesignEditLayerResponse, error) {
	strokes := req.GetStrokes()
	if len(strokes) > design.MaxStrokesBytes {
		return nil, designError(ctx, "strokes too large",
			fmt.Errorf("%w: %d bytes, the ceiling is %d",
				entity.ErrDesignStrokesTooLarge, len(strokes), design.MaxStrokesBytes), nil)
	}
	// The column VALIDATES JSON, so an opaque or compressed payload would be refused by MySQL with
	// a raw 3140 naming a column. Checking here means the person is told what is wrong with what
	// they sent, in the same vocabulary as the size refusal.
	if len(strokes) > 0 && !json.Valid(strokes) {
		return nil, status.Error(codes.InvalidArgument, "strokes must be JSON")
	}
	layer, err := s.repo.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId:  int(req.GetTechCardId()),
		LayerId:     int(req.GetLayerId()),
		BaseMediaId: int(req.GetBaseMediaId()),
		ExpectedRev: int(req.GetExpectedRev()),
		Strokes:     json.RawMessage(strokes),
		Actor:       designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to save the design edit layer", err, nil)
	}
	return &pb_admin.SaveDesignEditLayerResponse{Layer: designLayerToPb(*layer, true)}, nil
}

// FlattenDesignEditLayer records an ALREADY-RASTERISED image as a picture of the band.
//
// THE SERVER CREATES NO MEDIA HERE, and that is why this handler carries no compensation path
// while SplitDesignPicture does: the raster was produced and uploaded by the client through
// UploadContentImage (Р-2), and it arrives as a media id. Deleting that media when the flatten is
// refused would be an active harm — it would break the client's retry with the same id.
func (s *Server) FlattenDesignEditLayer(ctx context.Context, req *pb_admin.FlattenDesignEditLayerRequest) (*pb_admin.FlattenDesignEditLayerResponse, error) {
	pic, err := s.repo.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId:  int(req.GetTechCardId()),
		LayerId:     int(req.GetLayerId()),
		ExpectedRev: int(req.GetExpectedRev()),
		MediaId:     int(req.GetMediaId()),
		Actor:       designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to flatten the design edit layer", err, nil)
	}
	return &pb_admin.FlattenDesignEditLayerResponse{Picture: designPictureToPb(*pic)}, nil
}

// ─────────────────────────── the split ───────────────────────────

// designSplitMaxSourceBytes bounds how much of a composite is pulled into memory to cut it. The
// upload path already caps a picture at ~40 MP; this is the byte-side twin of that bound so a
// corrupt or hostile object cannot be streamed into the process without limit.
const designSplitMaxSourceBytes = 64 << 20

// designCoordinateScale mirrors the card's annotation precision guard. It is not decoration: an
// eleven-byte coordinate with an exponent («1e-10000000») costs SECONDS of CPU to rescale, so the
// guard itself was once the attack. The exponent is checked after parsing (cheap) and BEFORE any
// comparison against the frame (expensive), and an out-of-scale value is REFUSED rather than
// rounded — rounding is the very rescale being defended against.
const designCoordinateScale = 6

// SplitDesignPicture cuts a composite into per-view pictures.
//
// THE BYTE WORK HAPPENS BEFORE THE TRANSACTION — read the original object, cut it, upload each
// crop — and that opens a window in which media rows exist with no owner. Three real things land
// in that window: an idempotent short-circuit, a lost race, an ordinary database error. The
// verbatim upload helper cleans up its bucket object only WHILE the media row does not yet exist;
// once AddMedia succeeds the row is the caller's responsibility, which begins exactly where the
// helper's ends.
//
// So every unsuccessful exit compensates: DeleteMediaByIdIfUnused for each media this call minted
// and nothing adopted. That helper is safe by construction — it deletes only what nothing
// references — so a transaction that DID commit leaves its pictures untouched. The compensation
// never masks the original error; that error is what is returned.
func (s *Server) SplitDesignPicture(ctx context.Context, req *pb_admin.SplitDesignPictureRequest) (*pb_admin.SplitDesignPictureResponse, error) {
	if len(req.GetFrames()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "a split needs at least one frame")
	}
	if strings.TrimSpace(req.GetClientRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "client_request_id is required")
	}
	rects, err := designSplitRects(req.GetFrames())
	if err != nil {
		return nil, err
	}

	parent, err := s.repo.Design().GetPicture(ctx, int(req.GetPictureId()))
	if err != nil {
		return nil, designError(ctx, "failed to read the composite", err, nil)
	}
	// COMPOSITENESS IS NOT A PRECONDITION, and the guard that demanded it was removed rather than
	// relaxed, because it could never be satisfied.
	//
	// `design_picture.composite_views` says «this one image has these views glued into it». Its only
	// writer was the arrival of a generative run, and generation is cut from this wave — so the
	// column is empty on every picture that can exist here. `DesignUploadItem` carries `media_id`
	// and `ghost_view` and nothing else, so a human who brings in one sheet holding front and back
	// has no way to declare it composite either. The check therefore refused EVERY split, which is
	// worse than no feature: the door opens and the server says «not a composite» about a picture
	// that plainly is one.
	//
	// What replaces it is the operator's own statement: `DesignSplitFrame.view_key` says what is on
	// each piece, one frame at a time, and `designSplitRects` already refuses frames that do not
	// describe a rectangle inside the source. Compositeness was a guess about the file; the frames
	// are a fact about the cut.
	if parent.Media == nil {
		return nil, status.Error(codes.FailedPrecondition, "the composite's file is gone")
	}

	src, err := s.designFetchImage(ctx, parent.Media.FullSizeMediaURL)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to fetch the composite bytes",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.FailedPrecondition, "the composite's file could not be read")
	}

	// minted collects everything this call created, so the compensation knows exactly what it may
	// take back. It is appended to as each upload succeeds, never rebuilt from a guess.
	var minted []*pb_common.MediaFull
	adopted := false
	defer func() {
		if adopted {
			return
		}
		s.designCompensateMedia(ctx, minted)
	}()

	frames := make([]entity.DesignSplitFrame, 0, len(rects))
	bounds := src.Bounds()
	for i, r := range rects {
		raw, err := designCropPNG(src, bounds, r)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "frames.%d: %v", i, err)
		}
		media, err := s.bucket.UploadContentImageVerbatim(ctx, raw, "design",
			fmt.Sprintf("crop-%d-%d", parent.Id, i))
		if err != nil {
			slog.Default().ErrorContext(ctx, "failed to store a design crop",
				slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "failed to store the crop")
		}
		minted = append(minted, media)
		frames = append(frames, entity.DesignSplitFrame{
			MediaId: int(media.GetId()), ViewKey: req.GetFrames()[i].GetViewKey(),
		})
	}

	pics, err := s.repo.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId:       parent.Id,
		ClientRequestId: strings.TrimSpace(req.GetClientRequestId()),
		Frames:          frames,
		Actor:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to file the design crops", err, nil)
	}
	// Adoption is decided by what the transaction actually filed, not by "it returned no error":
	// an idempotent short-circuit returns the crops of an EARLIER split, and this call's fresh
	// uploads are then orphans that must still be swept.
	mintedIDs := make([]int, 0, len(minted))
	byID := make(map[int]*pb_common.MediaFull, len(minted))
	for _, m := range minted {
		mintedIDs = append(mintedIDs, int(m.GetId()))
		byID[int(m.GetId())] = m
	}
	adoptedIDs := make([]int, 0, len(pics))
	for _, p := range pics {
		adoptedIDs = append(adoptedIDs, p.MediaId)
	}
	var orphans []*pb_common.MediaFull
	for _, id := range design.OrphanedMedia(mintedIDs, adoptedIDs) {
		orphans = append(orphans, byID[id])
	}
	adopted = true
	if len(orphans) > 0 {
		s.designCompensateMedia(ctx, orphans)
	}
	return &pb_admin.SplitDesignPictureResponse{Pictures: designPicturesToPb(pics)}, nil
}

// designCompensateMedia takes back the media rows this call minted and nobody adopted, then drops
// the objects behind them. DeleteMediaByIdIfUnused decides and deletes under one lock and refuses
// anything still referenced, so it can never take a picture away from a transaction that landed.
//
// It is best-effort and LOUD: a failure here is logged, never returned, because the caller's own
// error is the one the person has to see.
func (s *Server) designCompensateMedia(ctx context.Context, minted []*pb_common.MediaFull) {
	for _, m := range minted {
		if m == nil || m.GetId() == 0 {
			continue
		}
		deleted, refs, err := s.repo.Media().DeleteMediaByIdIfUnused(ctx, int(m.GetId()))
		if err != nil {
			slog.Default().ErrorContext(ctx, "failed to compensate an orphaned design crop",
				slog.Int("media_id", int(m.GetId())), slog.String("err", err.Error()))
			continue
		}
		if !deleted {
			slog.Default().WarnContext(ctx, "an orphaned design crop was adopted meanwhile",
				slog.Int("media_id", int(m.GetId())), slog.Int("refs", len(refs)))
			continue
		}
		urls := []string{}
		if mi := m.GetMedia(); mi != nil {
			for _, v := range []*pb_common.MediaInfo{mi.GetFullSize(), mi.GetThumbnail(), mi.GetCompressed()} {
				if v.GetMediaUrl() != "" {
					urls = append(urls, v.GetMediaUrl())
				}
			}
		}
		if len(urls) > 0 {
			if err := s.bucket.DeleteObjects(ctx, urls...); err != nil {
				slog.Default().ErrorContext(ctx, "failed to drop the objects of an orphaned crop",
					slog.Int("media_id", int(m.GetId())), slog.String("err", err.Error()))
			}
		}
	}
}

// designFetchImage reads a managed object by the url stored on the media row and decodes it.
// The key comes from a DB row and only from a DB row; the segment gate lives in the bucket.
func (s *Server) designFetchImage(ctx context.Context, rawURL string) (image.Image, error) {
	key, err := archiveObjectKeyFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	rc, size, err := s.bucket.GetManagedObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("object %q: %w", key, err)
	}
	defer rc.Close()
	if size > designSplitMaxSourceBytes {
		return nil, fmt.Errorf("object %q is %d bytes, over the %d ceiling", key, size, designSplitMaxSourceBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, designSplitMaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	if len(raw) > designSplitMaxSourceBytes {
		return nil, fmt.Errorf("object %q is over the %d byte ceiling", key, designSplitMaxSourceBytes)
	}
	return designDecodeImage(raw)
}

// designDecodeImage sniffs the format from the leading bytes rather than trusting anything
// declared. The full-size object of a media row is WebP when it came through the re-encoding
// upload and whatever was uploaded when it came through the verbatim one, so both must decode.
func designDecodeImage(raw []byte) (image.Image, error) {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return png.Decode(bytes.NewReader(raw))
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return jpeg.Decode(bytes.NewReader(raw))
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return webp.Decode(bytes.NewReader(raw))
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return gif.Decode(bytes.NewReader(raw))
	default:
		return nil, errors.New("unrecognised image format")
	}
}

// designUnitRect is one crop frame in normalised coordinates.
type designUnitRect struct{ x, y, w, h decimal.Decimal }

// designSplitRects validates every frame before a single byte is read. Validation first is not
// tidiness: reading and decoding a composite costs real memory, and a request that was never going
// to be accepted should not buy it.
func designSplitRects(frames []*pb_admin.DesignSplitFrame) ([]designUnitRect, error) {
	out := make([]designUnitRect, 0, len(frames))
	for i, f := range frames {
		x, err := designUnitInterval(fmt.Sprintf("frames.%d.x", i), f.GetX())
		if err != nil {
			return nil, err
		}
		y, err := designUnitInterval(fmt.Sprintf("frames.%d.y", i), f.GetY())
		if err != nil {
			return nil, err
		}
		w, err := designUnitInterval(fmt.Sprintf("frames.%d.w", i), f.GetW())
		if err != nil {
			return nil, err
		}
		h, err := designUnitInterval(fmt.Sprintf("frames.%d.h", i), f.GetH())
		if err != nil {
			return nil, err
		}
		if w.LessThanOrEqual(decimal.Zero) || h.LessThanOrEqual(decimal.Zero) {
			return nil, status.Errorf(codes.InvalidArgument, "frames.%d has no area", i)
		}
		if x.Add(w).GreaterThan(decimal.NewFromInt(1)) || y.Add(h).GreaterThan(decimal.NewFromInt(1)) {
			return nil, status.Errorf(codes.InvalidArgument, "frames.%d reaches outside the picture", i)
		}
		if f.GetViewKey() != "" && !entity.IsDesignGhostView(f.GetViewKey()) {
			return nil, status.Errorf(codes.InvalidArgument,
				"frames.%d.view_key %q is not a view of the garment", i, f.GetViewKey())
		}
		out = append(out, designUnitRect{x: x, y: y, w: w, h: h})
	}
	return out, nil
}

// designUnitInterval parses one normalised coordinate under the precision guard.
func designUnitInterval(field string, d *pb_decimal.Decimal) (decimal.Decimal, error) {
	if d == nil || d.GetValue() == "" {
		return decimal.Zero, nil
	}
	v, err := decimal.NewFromString(d.GetValue())
	if err != nil {
		return decimal.Zero, status.Errorf(codes.InvalidArgument,
			"%s is not a fraction of the picture from 0 to 1", field)
	}
	// Order is part of the defence: the exponent is checked after the (cheap) parse and before the
	// (expensive) comparison against the frame.
	if exp := v.Exponent(); exp < -designCoordinateScale {
		return decimal.Zero, status.Errorf(codes.InvalidArgument,
			"%s carries more than %d decimal places: the picture resolves nothing finer",
			field, designCoordinateScale)
	} else if exp > 0 {
		return decimal.Zero, status.Errorf(codes.InvalidArgument,
			"%s is written as an ordinary fraction from 0 to 1, not in exponent notation", field)
	}
	if v.LessThan(decimal.Zero) || v.GreaterThan(decimal.NewFromInt(1)) {
		return decimal.Zero, status.Errorf(codes.InvalidArgument,
			"%s is a fraction of the picture from 0 to 1", field)
	}
	return v, nil
}

// designCropPNG cuts one frame out of the decoded source and encodes it as PNG.
//
// PNG, and not the source's own format, because the cut must be LOSSLESS: re-encoding a JPEG
// composite as JPEG would add a generation of loss to every crop, and a flat that gets printed
// cannot afford one. The bytes then go up the verbatim path, so what is stored is exactly what was
// cut.
func designCropPNG(src image.Image, bounds image.Rectangle, r designUnitRect) ([]byte, error) {
	w := decimal.NewFromInt(int64(bounds.Dx()))
	h := decimal.NewFromInt(int64(bounds.Dy()))
	x0 := bounds.Min.X + int(r.x.Mul(w).IntPart())
	y0 := bounds.Min.Y + int(r.y.Mul(h).IntPart())
	x1 := bounds.Min.X + int(r.x.Add(r.w).Mul(w).IntPart())
	y1 := bounds.Min.Y + int(r.y.Add(r.h).Mul(h).IntPart())
	if x1 > bounds.Max.X {
		x1 = bounds.Max.X
	}
	if y1 > bounds.Max.Y {
		y1 = bounds.Max.Y
	}
	if x1-x0 < 1 || y1-y0 < 1 {
		return nil, errors.New("the frame is smaller than one pixel of the source")
	}
	rect := image.Rect(x0, y0, x1, y1)
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	var cropped image.Image
	if si, ok := src.(subImager); ok {
		cropped = si.SubImage(rect)
	} else {
		// A decoder that hands back an image without SubImage still has to be cuttable, so the
		// pixels are copied. Correctness first; this path is not the common one.
		dst := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		for y := 0; y < rect.Dy(); y++ {
			for x := 0; x < rect.Dx(); x++ {
				dst.Set(x, y, src.At(rect.Min.X+x, rect.Min.Y+y))
			}
		}
		cropped = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, cropped); err != nil {
		return nil, fmt.Errorf("encode crop: %w", err)
	}
	return buf.Bytes(), nil
}

// ─────────────────────────── costing ───────────────────────────

// stripDesignCosting redacts every money field of the band from an account without costing:read.
//
// THE BUDGET BAR IS REMOVED WHOLE rather than blanked. A bar with empty numbers in it reads as
// «the budget is zero», which is a different and false statement — the honest rendering of «you
// may not see this» is no bar at all.
func (s *Server) stripDesignCosting(ctx context.Context, runs []*pb_common.DesignRun, budget *pb_common.DesignBudget) {
	if read, _ := s.costingAccess(ctx); read {
		return
	}
	for _, r := range runs {
		if r == nil {
			continue
		}
		r.PriceEstimate = nil
		r.PriceActual = nil
		for _, a := range r.Attempts {
			if a != nil {
				a.Price = nil
			}
		}
	}
	if budget != nil {
		budget.Spent = nil
		budget.Reserved = nil
		budget.Cap = nil
	}
}

// ─────────────────────────── conversions ───────────────────────────

// designSlotRefFromPb turns the wire's oneof into the store's address.
//
// `view_key = "detail"` means MINT A NEW DETAIL SLOT — that is the only form the oneof can carry,
// since it cannot send a view and an id at once, and a detail's identity is its minted id rather
// than its name. The name is then required, and the refusal for a missing one already exists.
func designSlotRefFromPb(ref *pb_admin.DesignBenchSlotRef) (entity.DesignSlotRef, error) {
	if ref == nil {
		return entity.DesignSlotRef{}, status.Error(codes.InvalidArgument,
			"a bench slot must be addressed by view_key or slot_id")
	}
	switch v := ref.GetSlot().(type) {
	case *pb_admin.DesignBenchSlotRef_ViewKey:
		if !entity.IsDesignGhostView(v.ViewKey) {
			return entity.DesignSlotRef{}, status.Errorf(codes.InvalidArgument,
				"slot.view_key %q is not a view of the garment", v.ViewKey)
		}
		return entity.DesignSlotRef{ViewKey: v.ViewKey}, nil
	case *pb_admin.DesignBenchSlotRef_SlotId:
		if v.SlotId <= 0 {
			return entity.DesignSlotRef{}, status.Error(codes.InvalidArgument, "slot.slot_id must be positive")
		}
		return entity.DesignSlotRef{SlotId: int(v.SlotId)}, nil
	default:
		return entity.DesignSlotRef{}, status.Error(codes.InvalidArgument,
			"a bench slot must be addressed by view_key or slot_id")
	}
}

func designBenchToPb(in []entity.DesignBenchSlot) []*pb_common.DesignBenchSlot {
	out := make([]*pb_common.DesignBenchSlot, 0, len(in))
	for _, s := range in {
		out = append(out, designSlotToPb(s))
	}
	return out
}

func designSlotToPb(s entity.DesignBenchSlot) *pb_common.DesignBenchSlot {
	out := &pb_common.DesignBenchSlot{
		Id:         int32(s.Id),
		ViewKey:    s.ViewKey,
		DetailName: s.DetailName.String,
		PictureId:  int32(s.PictureId.Int32),
		SlotRev:    int32(s.SlotRev),
		SetBy:      s.SetBy,
	}
	if s.SetAt.Valid {
		out.SetAt = timestamppb.New(s.SetAt.Time)
	}
	if s.Picture != nil {
		out.Picture = designPictureToPb(*s.Picture)
	}
	return out
}

func designPicturesToPb(in []entity.DesignPicture) []*pb_common.DesignPicture {
	out := make([]*pb_common.DesignPicture, 0, len(in))
	for _, p := range in {
		out = append(out, designPictureToPb(p))
	}
	return out
}

func designPictureToPb(p entity.DesignPicture) *pb_common.DesignPicture {
	out := &pb_common.DesignPicture{
		Id:          int32(p.Id),
		TechCardId:  int32(p.TechCardId),
		RunId:       p.RunId.Int32,
		BatchId:     p.BatchId.Int32,
		Ordinal:     int32(p.Ordinal),
		Kind:        p.Kind,
		GhostView:   p.GhostView.String,
		DerivedFrom: p.DerivedFrom.Int32,
		SourceClass: p.SourceClass,
		MixedInput:  p.MixedInput,
		LayerRev:    int32(p.LayerRev),
		HiddenBy:    p.HiddenBy.String,
		CreatedAt:   timestamppb.New(p.CreatedAt),
	}
	if p.HiddenAt.Valid {
		out.HiddenAt = timestamppb.New(p.HiddenAt.Time)
	}
	if len(p.CompositeViews) > 0 {
		var views []string
		if err := json.Unmarshal(p.CompositeViews, &views); err == nil {
			out.CompositeViews = views
		}
	}
	out.Media = designMediaToPb(p.Media)
	return out
}

// designMediaToPb converts a media row and fills the content hash the shared converter does not
// carry yet. The hash is what earns a plate its «stale» badge — the client compares it against the
// hash frozen in the run's input snapshot — so a picture served without it cannot be told fresh
// from re-flattened.
func designMediaToPb(m *entity.MediaFull) *pb_common.MediaFull {
	if m == nil {
		return nil
	}
	out := dto.ConvertEntityToCommonMedia(m)
	if out != nil && m.ContentHash.Valid {
		out.ContentHash = m.ContentHash.String
	}
	return out
}

func designBatchesToPb(in []entity.DesignBatch) []*pb_common.DesignBatch {
	out := make([]*pb_common.DesignBatch, 0, len(in))
	for _, b := range in {
		out = append(out, designBatchToPb(b))
	}
	return out
}

func designBatchToPb(b entity.DesignBatch) *pb_common.DesignBatch {
	return &pb_common.DesignBatch{
		Id:              int32(b.Id),
		TechCardId:      int32(b.TechCardId),
		ClientRequestId: b.ClientRequestId,
		Author:          b.Author,
		FilesCount:      int32(b.FilesCount),
		SizeBytes:       b.SizeBytes,
		CreatedAt:       timestamppb.New(b.CreatedAt),
		Pictures:        designPicturesToPb(b.Pictures),
	}
}

func designRunsToPb(ctx context.Context, in []entity.DesignRun) []*pb_common.DesignRun {
	out := make([]*pb_common.DesignRun, 0, len(in))
	for _, r := range in {
		out = append(out, designRunToPb(ctx, r))
	}
	return out
}

func designRunToPb(ctx context.Context, r entity.DesignRun) *pb_common.DesignRun {
	out := &pb_common.DesignRun{
		Id:              int32(r.Id),
		TechCardId:      int32(r.TechCardId),
		Kind:            r.Kind,
		Status:          r.Status,
		ClientRequestId: r.ClientRequestId,
		ProfileName:     r.ProfileName,
		// profile_version is an INT column and a STRING field on the wire — the contract keeps it
		// open so a profile may one day be versioned as «v4.1» rather than «4». Formatting here is
		// the whole adaptation; the column stays numeric until the contract's freedom is used.
		ProfileVersion:   strconv.Itoa(r.ProfileVersion),
		Ask:              r.Ask.String,
		FitAtLaunch:      r.FitAtLaunch.String,
		Rrev:             int32(r.Rrev),
		RequestedOutputs: int32(r.RequestedOutputs),
		Currency:         r.Currency,
		Author:           r.Author,
		ArchivedBy:       r.ArchivedBy.String,
		ErrorCode:        r.ErrorCode.String,
		LastError:        r.LastError.String,
		OutputText:       r.OutputText.String,
		CreatedAt:        timestamppb.New(r.CreatedAt),
		Pictures:         designPicturesToPb(r.Pictures),
	}
	if r.CancelRequestedAt.Valid {
		out.CancelRequestedAt = timestamppb.New(r.CancelRequestedAt.Time)
	}
	if r.ArchivedAt.Valid {
		out.ArchivedAt = timestamppb.New(r.ArchivedAt.Time)
	}
	if r.StartedAt.Valid {
		out.StartedAt = timestamppb.New(r.StartedAt.Time)
	}
	if r.CompletedAt.Valid {
		out.CompletedAt = timestamppb.New(r.CompletedAt.Time)
	}
	if r.PriceEstimate.Valid {
		out.PriceEstimate = &pb_decimal.Decimal{Value: r.PriceEstimate.Decimal.String()}
	}
	if r.PriceActual.Valid {
		out.PriceActual = &pb_decimal.Decimal{Value: r.PriceActual.Decimal.String()}
	}
	if len(r.Params) > 0 {
		p := &pb_common.DesignRunParams{}
		if err := designUnmarshalJSON(r.Params, p); err == nil {
			out.Params = p
		} else {
			slog.Default().WarnContext(ctx, "design run params did not parse",
				slog.Int("run_id", r.Id), slog.String("err", err.Error()))
		}
	}
	if len(r.Inputs) > 0 {
		in := &pb_common.DesignInputSnapshot{}
		if err := designUnmarshalJSON(r.Inputs, in); err == nil {
			out.Inputs = in
		} else {
			slog.Default().WarnContext(ctx, "design run inputs did not parse",
				slog.Int("run_id", r.Id), slog.String("err", err.Error()))
		}
	}
	for _, a := range r.Attempts {
		att := &pb_common.DesignRunAttempt{
			AttemptNo:         int32(a.AttemptNo),
			Provider:          a.Provider,
			ProviderRequestId: a.ProviderRequestId.String,
			// The provider idempotency key is stored ONCE, on the run, and repeated on every
			// attempt here so a reader holding one attempt can reconcile against the provider's
			// billing without loading the run. Two homes for one key is how a retry ends up buying
			// a second picture, so the attempt table has no column of its own.
			ProviderIdempotencyKey: r.ProviderIdempotencyKey,
			State:                  a.State,
			StartedAt:              timestamppb.New(a.StartedAt),
			ErrorCode:              a.ErrorCode.String,
		}
		if a.FinishedAt.Valid {
			att.FinishedAt = timestamppb.New(a.FinishedAt.Time)
		}
		if a.Price.Valid {
			att.Price = &pb_decimal.Decimal{Value: a.Price.Decimal.String()}
		}
		out.Attempts = append(out.Attempts, att)
	}
	return out
}

func designSheetVersionToPb(ctx context.Context, v entity.DesignSheetVersion) *pb_common.DesignSheetVersion {
	out := &pb_common.DesignSheetVersion{
		Id:              int32(v.Id),
		VersionNumber:   int32(v.VersionNumber),
		ClientRequestId: v.ClientRequestId,
		MixedConsent:    v.MixedConsent,
		MintedVia:       v.MintedVia,
		MintedBy:        v.MintedBy,
		MintedAt:        timestamppb.New(v.MintedAt),
	}
	for _, p := range v.Plates {
		out.Plates = append(out.Plates, &pb_common.DesignSheetPlate{
			ViewKey:     p.ViewKey,
			SlotId:      p.SlotId.Int32,
			DetailName:  p.DetailName.String,
			Media:       designMediaToPb(p.Media),
			ContentHash: p.ContentHash.String,
			LayerRev:    int32(p.LayerRev),
			SourceClass: p.SourceClass,
			RunId:       p.RunId.Int32,
			FitStamp:    p.FitStamp.String,
			MixedInput:  p.MixedInput,
			Ordinal:     int32(p.Ordinal),
		})
	}
	for _, c := range v.Callouts {
		pc := &pb_common.DesignSheetCallout{
			Number: int32(c.Number),
			Media:  designMediaToPb(c.Media),
			Text:   c.Text.String,
		}
		if len(c.Annotation) > 0 {
			a := &pb_common.TechCardAnnotation{}
			if err := designUnmarshalJSON(c.Annotation, a); err == nil {
				pc.Annotation = a
			} else {
				slog.Default().WarnContext(ctx, "a frozen design callout's geometry did not parse",
					slog.Int("version_id", v.Id), slog.String("err", err.Error()))
			}
		}
		out.Callouts = append(out.Callouts, pc)
	}
	return out
}

func designIssuesToPb(in []entity.DesignSheetIssue) []*pb_common.DesignSheetIssue {
	out := make([]*pb_common.DesignSheetIssue, 0, len(in))
	for _, i := range in {
		out = append(out, designIssueToPb(i))
	}
	return out
}

func designIssueToPb(i entity.DesignSheetIssue) *pb_common.DesignSheetIssue {
	return &pb_common.DesignSheetIssue{
		Id:            int32(i.Id),
		VersionNumber: int32(i.VersionNumber),
		Action:        i.Action,
		Actor:         i.Actor,
		CreatedAt:     timestamppb.New(i.CreatedAt),
	}
}

func designReferencesToPb(in []entity.DesignReference) []*pb_common.DesignReference {
	out := make([]*pb_common.DesignReference, 0, len(in))
	for _, r := range in {
		out = append(out, designReferenceToPb(r))
	}
	return out
}

func designReferenceToPb(r entity.DesignReference) *pb_common.DesignReference {
	return &pb_common.DesignReference{
		TechCardId: int32(r.TechCardId),
		MediaId:    int32(r.MediaId),
		Role:       r.Role,
		Ordinal:    int32(r.Ordinal),
		SetBy:      r.SetBy,
		SetAt:      timestamppb.New(r.SetAt),
	}
}

func designLayersToPb(in []entity.DesignEditLayer, withStrokes bool) []*pb_common.DesignEditLayer {
	out := make([]*pb_common.DesignEditLayer, 0, len(in))
	for _, l := range in {
		out = append(out, designLayerToPb(l, withStrokes))
	}
	return out
}

func designLayerToPb(l entity.DesignEditLayer, withStrokes bool) *pb_common.DesignEditLayer {
	out := &pb_common.DesignEditLayer{
		Id:          int32(l.Id),
		TechCardId:  int32(l.TechCardId),
		BaseMediaId: l.BaseMediaId.Int32,
		Rev:         int32(l.Rev),
		UpdatedBy:   l.UpdatedBy,
		UpdatedAt:   timestamppb.New(l.UpdatedAt),
	}
	if withStrokes {
		out.Strokes = l.Strokes
	}
	return out
}

func designBudgetToPb(b entity.DesignBudget) *pb_common.DesignBudget {
	return &pb_common.DesignBudget{
		Day:      b.Day,
		Spent:    &pb_decimal.Decimal{Value: b.Spent.String()},
		Reserved: &pb_decimal.Decimal{Value: b.Reserved.String()},
		Cap:      &pb_decimal.Decimal{Value: b.Cap.String()},
		Currency: b.Currency,
		Timezone: b.Timezone,
	}
}

// designColourRecipesToPb turns the raw recipe objects the store carried through into the wire's
// shape. A recipe that does not parse is DROPPED rather than shipped half-built: a chip restores a
// recipe, and half a recipe restores the wrong colour.
func designColourRecipesToPb(ctx context.Context, in []json.RawMessage) []*pb_common.DesignColourRecipe {
	out := make([]*pb_common.DesignColourRecipe, 0, len(in))
	for _, raw := range in {
		r := &pb_common.DesignColourRecipe{}
		if err := designUnmarshalJSON(raw, r); err != nil {
			slog.Default().WarnContext(ctx, "a design colour recipe did not parse",
				slog.String("err", err.Error()))
			continue
		}
		out = append(out, r)
	}
	return out
}

// designUnmarshalJSON reads a stored JSON column into a protobuf message.
//
// DiscardUnknown is TRUE here and that is deliberate: the columns hold snapshots written by an
// older binary, and a field that has since been renamed must not make a whole history row
// unreadable. It is the opposite decision from the gateway's request parsing, where an unknown
// field means the client is confused and must be told.
var designJSONUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// Принимает []byte, а не json.RawMessage: JSON-колонки полосы приезжают из стора как
// entity.RawJSON (NULL-безопасный сырой JSON), и оба типа суть []byte.
func designUnmarshalJSON(raw []byte, into proto.Message) error {
	return designJSONUnmarshal.Unmarshal(raw, into)
}

// intsToInt32 / intMapToPb are the two shapes the wire wants that Go does not carry.
func intsToInt32(in []int) []int32 {
	out := make([]int32, 0, len(in))
	for _, v := range in {
		out = append(out, int32(v))
	}
	return out
}

func intMapToPb(in map[int]int) map[int32]int32 {
	out := make(map[int32]int32, len(in))
	for k, v := range in {
		out[int32(k)] = int32(v)
	}
	return out
}
