package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// Saved раскладки (markers, 0257). Summaries ride GetTechCard.markers; only the blob read and the
// two writes need RPCs of their own. The layout travels as an opaque proto-JSON blob between the
// API layer and the store (idiom of the release snapshot) — the store never parses it.

// maxMarkerLayoutBytes caps the marshalled layout blob. A realistic marker (~30 pieces × ~200
// points) is 60-100 KB; two megabytes means a runaway payload, not a bigger raskладка.
const maxMarkerLayoutBytes = 2 << 20

// Cardinality bounds checked before the blob re-marshal (see SaveTechCardMarker). Generous
// against reality — a marker is ~dozens of pieces with RDP'd contours of ~100 points.
const (
	maxMarkerPieces        = 300
	maxMarkerPlacements    = 5000
	maxMarkerContourPoints = 200_000
)

// maxMarkerLayoutSchema is the newest blob format this server understands. v1 = the original
// geometry; v2 adds piece_line_key/block_name on a piece; v3 adds `flipped` on a placement — a
// MIRRORED instance, the зеркальная пара no rot_deg can express (Ф1) — and with it the
// directional-cloth policy the save path applies; v4 adds `composition` on the layout and `size_id`
// on a piece (Ф2). Every older version keeps reading and keeps saving unchanged.
//
// ONLY THIS CONSTANT MOVES with a new schema. entity.MarkerLayoutSchemaWithFlip stays at 3: it gates
// the Ф1.6 grandfathering, and bumping it in step would hand the directional-cloth exemption back to
// every v3 marker — repealing Ф1.6 the day after it shipped.
const maxMarkerLayoutSchema = entity.MarkerLayoutSchemaWithComposition

// SaveTechCardMarker creates (id=0) or fully replaces (id>0) one saved раскладка. Last-write-wins
// by design, and deliberately NOT bumping tech_card.lock_version — saving a marker from the
// nesting modal must not 409 the operator's own open card form.
func (s *Server) SaveTechCardMarker(ctx context.Context, req *pb_admin.SaveTechCardMarkerRequest) (*pb_admin.SaveTechCardMarkerResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if req.GetId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "id must not be negative")
	}
	ins, err := dto.ConvertPbTechCardMarkerInsertToEntity(req.GetMarker())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid marker: %v", err)
	}
	layout := req.GetMarker().GetLayout()
	if layout == nil || len(layout.GetPieces()) == 0 || len(layout.GetPlacements()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "marker layout with pieces and placements is required")
	}
	// Cardinality is bounded BEFORE the re-marshal — the 2MB cap below measures the output,
	// and without these guards a write-RBAC caller could ship ~50MB of points through the
	// admin body limit and have it parsed and re-marshalled first.
	if len(layout.GetPieces()) > maxMarkerPieces || len(layout.GetPlacements()) > maxMarkerPlacements {
		return nil, status.Errorf(codes.InvalidArgument,
			"marker layout too large: max %d pieces / %d placements", maxMarkerPieces, maxMarkerPlacements)
	}
	points := 0
	for _, p := range layout.GetPieces() {
		points += len(p.GetPoly())
		// source_url is provenance the server refuses to trust: a second, opaque write path
		// for urls the pattern-row validation cannot see — and after Ф7 a client hydrating
		// from view_url would bake a capability token into an immutable blob. The filename
		// in piece.source stays as the human provenance.
		p.SourceUrl = ""
	}
	if points > maxMarkerContourPoints {
		return nil, status.Errorf(codes.InvalidArgument,
			"marker layout too large: %d contour points, max %d", points, maxMarkerContourPoints)
	}
	if layout.GetSchemaVersion() == 0 {
		layout.SchemaVersion = 1
	}
	// Anything newer than maxMarkerLayoutSchema is a client this server does not understand —
	// refuse rather than store a blob readers would silently degrade on.
	if v := layout.GetSchemaVersion(); v < 1 || v > maxMarkerLayoutSchema {
		return nil, status.Errorf(codes.InvalidArgument,
			"marker layout schema_version %d is not supported (1..%d)", v, maxMarkerLayoutSchema)
	}
	// Distilled here because this is the last place the blob is a parsed message: the store gets the
	// facts, not a second parser (Ф1.5/Ф1.6 — the direction rule needs the version and whether any
	// placement is upside down, and only the DB knows the cloth's направление). This is also where
	// rot_deg is validated at all — nothing else in the system constrains it.
	facts, err := dto.MarkerLayoutFactsFromPb(layout)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid marker layout: %v", err)
	}
	ins.LayoutFacts = facts
	// A mirrored placement under a version that could not express one is refused for EVERY marker,
	// linked or not: the direction rule below only sees markers bound to cloth, but a blob that lies
	// about its own format is not a cloth question. See entity.FlipPredatesSchema.
	if entity.FlipPredatesSchema(facts) {
		return nil, status.Errorf(codes.InvalidArgument,
			"marker layout declares schema_version %d but carries a mirrored placement; `flipped` exists only from version %d",
			facts.SchemaVersion, entity.MarkerLayoutSchemaWithFlip)
	}
	// Same shape of refusal, same reason: a blob carrying a состав (or a size on a piece) under a
	// version in which neither field existed is a client writing a format it does not speak. Left
	// alone it would be stored as a v3 blob whose состав no reader is told to look for — a marker
	// that is silently wrong rather than loudly refused.
	//
	// Emitted as a FIELD VIOLATION, not a bare status: entity.ReasonCompositionPredatesSchema is
	// declared as a stable code clients switch on, and a reason code that is never actually put on
	// the wire is a promise nobody keeps — the sibling ReasonFlipInLegacySchema does reach a client,
	// through the direction rule.
	if entity.CompositionPredatesSchema(facts) {
		return nil, apierr.Invalid(entity.NewFieldViolation("layout.composition",
			entity.ReasonCompositionPredatesSchema, "",
			fmt.Sprintf("the layout declares schema_version %d but carries a состав or a size on a piece, "+
				"and both exist only from version %d on — no stored раскладка can contain them, so this is a "+
				"client writing a version it does not actually speak; save it as version %d",
				facts.SchemaVersion, entity.MarkerLayoutSchemaWithComposition,
				entity.MarkerLayoutSchemaWithComposition)))
	}
	// ДВОЙНОЙ ПРИПУСК. Refused HERE and not in the dto conversion for the same reason
	// CompositionPredatesSchema is: this has to reach the client as a FIELD violation with a stable
	// reason code, and an error returned from ConvertPbTechCardMarkerInsertToEntity is flattened into
	// a bare InvalidArgument two lines above.
	//
	// The same invariant also stands as chk_tcm_no_double_allowance in the schema, and that is a NET
	// rather than a message: a CHECK answers 3819 with no field and no prose, so a violation reaching
	// techCardMarkerError is a server bug and correctly surfaces as Internal.
	if ve := entity.MarkerAllowanceRefusal(ins.SeamAllowanceCm, ins.ContourAllowanceCm,
		ins.ContourLayer.String); ve != nil {
		return nil, apierr.Invalid(ve)
	}
	blob, err := protojson.Marshal(layout)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "marker layout does not marshal: %v", err)
	}
	if len(blob) > maxMarkerLayoutBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"marker layout is %d bytes, max %d", len(blob), maxMarkerLayoutBytes)
	}
	ins.Layout = string(blob)
	id, err := s.repo.TechCards().SaveMarker(ctx, int(req.GetTechCardId()), int(req.GetId()), ins,
		authsrv.GetAdminUsername(ctx))
	if err != nil {
		return nil, s.techCardMarkerError(ctx, "save", int(req.GetTechCardId()), err)
	}
	return &pb_admin.SaveTechCardMarkerResponse{Id: int32(id)}, nil
}

// GetTechCardMarker returns one marker WITH its layout. A stored blob the current schema cannot
// parse degrades to summary-only plus a warning instead of failing the read (hero-v2 style) — the
// summary's numbers are still true, and костинг reads only those.
func (s *Server) GetTechCardMarker(ctx context.Context, req *pb_admin.GetTechCardMarkerRequest) (*pb_admin.GetTechCardMarkerResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	m, err := s.repo.TechCards().GetMarker(ctx, int(req.GetId()))
	if err != nil {
		return nil, s.techCardMarkerError(ctx, "get", 0, err)
	}
	out := &pb_common.TechCardMarker{Summary: dto.TechCardMarkerSummaryToPb(m.TechCardMarkerSummary)}
	var layout pb_common.TechCardMarkerLayout
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(m.Layout), &layout); err != nil {
		slog.Default().ErrorContext(ctx, "stored marker layout does not parse; serving summary only",
			slog.Int("marker_id", m.Id), slog.String("err", err.Error()))
		out.Layout = &pb_common.TechCardMarkerLayout{
			Warnings: []string{"сохранённая раскладка не читается этой версией — доступны только итоговые цифры"},
		}
	} else {
		out.Layout = &layout
	}
	return &pb_admin.GetTechCardMarkerResponse{Marker: out}, nil
}

// DeleteTechCardMarker removes a saved раскладка. Markers are measurements — nothing references
// them, so the only refusals are "gone" and "the card is released".
func (s *Server) DeleteTechCardMarker(ctx context.Context, req *pb_admin.DeleteTechCardMarkerRequest) (*pb_admin.DeleteTechCardMarkerResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.TechCards().DeleteMarker(ctx, int(req.GetId())); err != nil {
		return nil, s.techCardMarkerError(ctx, "delete", 0, err)
	}
	return &pb_admin.DeleteTechCardMarkerResponse{}, nil
}

// SetTechCardMarkerNorm designates one раскладка as the НОРМИРОВОЧНАЯ one for its cloth, or clears
// it. A dedicated RPC rather than a field on the save: designation has a side effect on a sibling
// row, and a boolean on the Insert would arrive as `false` from a stale bundle and clear the norm
// while knowing nothing about it — the failure that forced fabric_direction to become optional in Ф1.
func (s *Server) SetTechCardMarkerNorm(ctx context.Context, req *pb_admin.SetTechCardMarkerNormRequest) (*pb_admin.SetTechCardMarkerNormResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	previous, err := s.repo.TechCards().SetMarkerNorm(ctx, int(req.GetId()), req.GetIsNorm(),
		authsrv.GetAdminUsername(ctx))
	if err != nil {
		return nil, s.techCardMarkerError(ctx, "set-norm", 0, err)
	}
	return &pb_admin.SetTechCardMarkerNormResponse{
		PreviousNormMarkerId: int32(previous),
		// A CONSTANT true, and not a stub. Re-designating the norm recomputes nothing: a recipe holds a
		// COPIED consumption figure with provenance consumption_source='marker' and no reference back to
		// the marker, and there is no server-side «apply marker» at all. The screen is obliged to say so,
		// or the operator is left to assume the costing followed the designation.
		RecipesNotRecalculated: true,
	}, nil
}

// techCardMarkerError maps the store's typed refusals to gRPC statuses, following the
// output-variant path: malformed payload = InvalidArgument, a well-formed request the current
// state blocks = FailedPrecondition, only an unrecognised error = Internal.
func (s *Server) techCardMarkerError(ctx context.Context, op string, techCardID int, err error) error {
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		return apierr.Invalid(ve)
	case errors.Is(err, entity.ErrMarkerNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "tech card not found")
	case errors.Is(err, entity.ErrMarkerIncomplete):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrTechCardReleased):
		return status.Error(codes.FailedPrecondition, entity.ErrTechCardReleased.Error())
	case s.repo.IsErrUniqueViolation(err):
		// uniq_tcm_card_sizekey_name (0273). "for this size" became a half-truth with Ф2: markers
		// with a состав all share size_key 0, so on those the name is unique per CARD, and only the
		// legacy rows still scope it by size. The message states the part that is true for both.
		return status.Error(codes.FailedPrecondition,
			"a раскладка with this name already exists on this tech card; pick another name")
	case s.repo.IsErrForeignKeyViolation(err):
		return status.Error(codes.InvalidArgument, "marker references a missing tech card, size or BOM line")
	}
	slog.Default().ErrorContext(ctx, "tech card marker write failed",
		slog.String("op", op), slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't save the раскладка; try again")
}
