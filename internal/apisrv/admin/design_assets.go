package admin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// THE CARD'S ASSET SHELVES ON THE WIRE (0354, V-11): cloths, patterns and hardware, plus the marks
// they leave on the flats.
//
// FOUR HANDLERS AND NO VALIDATION OF THEIR OWN, and that is the point of the file. Every rule about
// what an asset may say lives in entity.DesignAssetUpsert.Validate and in the store's transaction;
// every rule about what a mark's geometry may say lives in the card's ONE annotation validator.
// A shelf rule re-stated here would be a second opinion that agrees today and diverges on the first
// edit of either copy — the failure this repository has already paid for with the split between two
// coordinate guards.

// UpsertDesignAsset writes ONE shelf row, creating it when asset_id is 0 and replacing its fields
// otherwise.
func (s *Server) UpsertDesignAsset(ctx context.Context, req *pb_admin.UpsertDesignAssetRequest) (*pb_admin.UpsertDesignAssetResponse, error) {
	asset, err := s.repo.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId:         int(req.GetTechCardId()),
		AssetId:            int(req.GetAssetId()),
		Kind:               req.GetKind(),
		Name:               req.GetName(),
		MediaId:            int(req.GetMediaId()),
		ColourCode:         req.GetColourCode(),
		ColourHex:          req.GetColourHex(),
		Note:               req.GetNote(),
		DerivedFromAssetId: int(req.GetDerivedFromAssetId()),
		RepeatMm:           int(req.GetRepeatMm()),
		RotationDeg:        int(req.GetRotationDeg()),
		Ordinal:            int(req.GetOrdinal()),
		Actor:              designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to write the design asset", err, nil)
	}
	return &pb_admin.UpsertDesignAssetResponse{Asset: designAssetToPb(*asset)}, nil
}

// DeleteDesignAsset removes ONE shelf row and reports how many marks went with it.
//
// THE REQUEST CARRIES THE ASSET ID AND NOTHING ELSE — exactly as DeleteDesignDetailSlotRequest
// carries the slot id and nothing else — so the card is passed as 0 and the store addresses the row
// by its own identity. A minted id already names its card; asking the client to repeat that fact
// would be asking it for a fact it can only get wrong.
func (s *Server) DeleteDesignAsset(ctx context.Context, req *pb_admin.DeleteDesignAssetRequest) (*pb_admin.DeleteDesignAssetResponse, error) {
	removed, err := s.repo.Design().DeleteAsset(ctx, 0, int(req.GetAssetId()))
	if err != nil {
		return nil, designError(ctx, "failed to delete the design asset", err, nil)
	}
	return &pb_admin.DeleteDesignAssetResponse{RemovedPlacements: int32(removed)}, nil
}

// SetDesignAssetPlacement puts ONE mark on ONE flat: this asset, this drawing, here.
func (s *Server) SetDesignAssetPlacement(ctx context.Context, req *pb_admin.SetDesignAssetPlacementRequest) (*pb_admin.SetDesignAssetPlacementResponse, error) {
	ann, err := designAssetAnnotationJSON(req.GetAnnotation())
	if err != nil {
		return nil, err
	}
	pl, err := s.repo.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId:  int(req.GetTechCardId()),
		PlacementId: int(req.GetPlacementId()),
		AssetId:     int(req.GetAssetId()),
		PictureId:   int(req.GetPictureId()),
		Annotation:  ann,
		Note:        req.GetNote(),
		Actor:       designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to place the design asset", err, nil)
	}
	return &pb_admin.SetDesignAssetPlacementResponse{Placement: designAssetPlacementToPb(*pl)}, nil
}

// DeleteDesignAssetPlacement takes ONE mark off a flat; the asset stays on its shelf.
func (s *Server) DeleteDesignAssetPlacement(ctx context.Context, req *pb_admin.DeleteDesignAssetPlacementRequest) (*pb_admin.DeleteDesignAssetPlacementResponse, error) {
	if err := s.repo.Design().DeleteAssetPlacement(ctx, 0, int(req.GetPlacementId())); err != nil {
		return nil, designError(ctx, "failed to remove the design asset placement", err, nil)
	}
	return &pb_admin.DeleteDesignAssetPlacementResponse{}, nil
}

// designAssetAnnotationJSON validates the mark's geometry and encodes it for the column.
//
// ⚠ THE VALIDATOR IS THE CARD'S OWN, dto.TechCardAnnotationFromPb, and writing a second one here
// would be the mistake this system already made once: the coordinate precision guard used to exist
// twice, and the copy that lacked it turned the defence itself into the attack. There is ONE
// annotation primitive in this repository and a shape drawn on a flat must be the same shape
// wherever it is read.
//
// ⚠ THE ENCODER IS designMarshalJSON — protojson with UseProtoNames: true — and matching it is not
// stylistic. It is the writer of every other JSON column of the band, its spelling is snake_case,
// and a column written in lowerCamelCase would still parse (protojson accepts both spellings on
// read) while every SQL JSON path over it returned nothing at all, silently and forever.
func designAssetAnnotationJSON(a *pb_common.TechCardAnnotation) (json.RawMessage, error) {
	if _, err := dto.TechCardAnnotationFromPb("annotation", a); err != nil {
		// The refusal keeps its STRUCTURE. entity.ValidationError names the offending field
		// («annotation.points»), apierr.Invalid unfolds it into google.rpc.BadRequest, and a bare
		// status.Errorf would flatten all of it into prose the screen cannot highlight with.
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	// THE MESSAGE IS RE-ENCODED FROM THE WIRE MESSAGE, not from the domain shape the validator
	// returned. The reader parses this column straight back into common.TechCardAnnotation, so a
	// round trip through the domain type would be a second, hand-rolled mapping able to lose a
	// field — and enums decoded from stored strings parse into an EMPTY message without an error,
	// which is how a whole layer of markings once vanished in silence.
	raw, err := designMarshalJSON(a)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode the mark's geometry")
	}
	return raw, nil
}

// ─────────────────────────── converters ───────────────────────────

func designAssetsToPb(in []entity.DesignAsset) []*pb_common.DesignAsset {
	out := make([]*pb_common.DesignAsset, 0, len(in))
	for _, a := range in {
		out = append(out, designAssetToPb(a))
	}
	return out
}

// designAssetToPb carries the WHOLE row, media included. Every nullable column degrades to the
// wire's own «unset» — 0 or "" — because proto3 has no third state and inventing one here would
// mean the client had to learn a second way to say «not stated».
func designAssetToPb(a entity.DesignAsset) *pb_common.DesignAsset {
	return &pb_common.DesignAsset{
		Id:                 int32(a.Id),
		TechCardId:         int32(a.TechCardId),
		Kind:               a.Kind,
		Name:               a.Name,
		MediaId:            a.MediaId.Int32,
		Media:              designMediaToPb(a.Media),
		ColourCode:         a.ColourCode.String,
		ColourHex:          a.ColourHex.String,
		Note:               a.Note.String,
		DerivedFromAssetId: a.DerivedFromAssetId.Int32,
		RepeatMm:           int32(a.RepeatMm),
		RotationDeg:        int32(a.RotationDeg),
		Ordinal:            int32(a.Ordinal),
		CreatedBy:          a.CreatedBy,
		CreatedAt:          timestamppb.New(a.CreatedAt),
		UpdatedAt:          timestamppb.New(a.UpdatedAt),
	}
}

func designAssetPlacementsToPb(in []entity.DesignAssetPlacement) []*pb_common.DesignAssetPlacement {
	out := make([]*pb_common.DesignAssetPlacement, 0, len(in))
	for _, p := range in {
		out = append(out, designAssetPlacementToPb(p))
	}
	return out
}

// designAssetPlacementToPb reads the stored geometry back into the very message it was written
// from, with the band's DiscardUnknown reader.
//
// A COLUMN THAT WILL NOT PARSE LEAVES THE MARK WITHOUT ITS SHAPE RATHER THAN DROPPING THE ROW.
// The row still says «this asset is marked on this flat», which is true and worth showing; hiding
// it would delete a person's statement because a byte of its geometry went bad.
func designAssetPlacementToPb(p entity.DesignAssetPlacement) *pb_common.DesignAssetPlacement {
	out := &pb_common.DesignAssetPlacement{
		Id:        int32(p.Id),
		AssetId:   int32(p.AssetId),
		PictureId: int32(p.PictureId),
		Note:      p.Note.String,
		SetBy:     p.SetBy,
		SetAt:     timestamppb.New(p.SetAt),
	}
	if len(p.Annotation) > 0 {
		ann := &pb_common.TechCardAnnotation{}
		if err := designUnmarshalJSON(p.Annotation, ann); err == nil {
			out.Annotation = ann
		}
	}
	return out
}
