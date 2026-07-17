package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UpdateStyle writes a style's catalogue facts (brand/season/collection/gender/fit/composition/care/
// model-wears/categories) — the sole writer of those facts (R4/§14.7). A stale expected_lock_version
// is ABORTED; a SKU-fact (season) change with any SKU-frozen sibling colourway is FailedPrecondition
// (clone for the new season instead); an unknown style is NotFound.
func (s *Server) UpdateStyle(ctx context.Context, req *pb_admin.UpdateStyleRequest) (*pb_admin.UpdateStyleResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	p := req.GetPatch()
	patch, err := dto.ConvertPbStylePatchToEntity(p.GetBrand(), p.GetSeason(), p.GetCollection(), p.GetTargetGender(),
		p.GetFit(), p.GetComposition(), p.GetCareInstructions(),
		p.GetModelWearsHeightCm(), p.GetModelWearsSizeId(), p.GetTopCategoryId(), p.GetSubCategoryId(), p.GetTypeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid style patch: %v", err)
	}
	lockVersion, err := s.repo.Products().UpdateStyle(ctx, int(req.StyleId), int(req.ExpectedLockVersion), patch)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		case errors.Is(err, entity.ErrTechCardConflict):
			return nil, status.Error(codes.Aborted, "style was modified concurrently; reload and retry")
		case errors.Is(err, entity.ErrStyleFrozenSiblings):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		default:
			slog.Default().ErrorContext(ctx, "can't update style", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't update style: %v", err)
		}
	}
	// A style change re-resolves every colourway of the style; revalidate the storefront broadly.
	if di, err := s.repo.Cache().GetDictionaryInfo(ctx); err == nil {
		cache.RefreshDictionary(di)
	}
	s.revalidateAsync(&dto.RevalidationData{Hero: true})
	return &pb_admin.UpdateStyleResponse{LockVersion: int32(lockVersion)}, nil
}

// GetStyleSizeChart returns a style's full size chart (R5). The admin UI loads it before editing
// because the update is a full-replace of the whole chart.
func (s *Server) GetStyleSizeChart(ctx context.Context, req *pb_admin.GetStyleSizeChartRequest) (*pb_admin.GetStyleSizeChartResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	chart, err := s.repo.TechCards().GetStyleSizeChart(ctx, int(req.StyleId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		}
		slog.Default().ErrorContext(ctx, "can't get style size chart", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get style size chart: %v", err)
	}
	return &pb_admin.GetStyleSizeChartResponse{Chart: dto.StyleSizeChartToPb(chart)}, nil
}

// UpdateStyleSizeChart replaces a style's ENTIRE size chart in one versioned request (R5). A stale
// expected_lock_version is ABORTED; an unknown measurement/size is InvalidArgument (FK).
func (s *Server) UpdateStyleSizeChart(ctx context.Context, req *pb_admin.UpdateStyleSizeChartRequest) (*pb_admin.UpdateStyleSizeChartResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	cells, err := dto.StyleSizeChartCellsFromPb(req.Cells)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	chart, err := s.repo.TechCards().UpdateStyleSizeChart(ctx, int(req.StyleId), int(req.ExpectedLockVersion), cells)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		case errors.Is(err, entity.ErrTechCardConflict):
			return nil, status.Error(codes.Aborted, "style was modified concurrently; reload the chart and retry")
		case s.repo.IsErrForeignKeyViolation(err):
			return nil, status.Error(codes.InvalidArgument, "size chart references an unknown size or measurement name")
		default:
			slog.Default().ErrorContext(ctx, "can't update style size chart", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't update style size chart: %v", err)
		}
	}
	return &pb_admin.UpdateStyleSizeChartResponse{Chart: dto.StyleSizeChartToPb(chart)}, nil
}

// RelinkDraftColorway moves a DRAFT colourway onto a different style (R4). A non-draft colourway is
// FailedPrecondition; a stale version on either side is ABORTED; an unknown colourway/target is NotFound.
func (s *Server) RelinkDraftColorway(ctx context.Context, req *pb_admin.RelinkDraftColorwayRequest) (*pb_admin.RelinkDraftColorwayResponse, error) {
	if req.ColorwayId <= 0 || req.TargetStyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "colorway_id and target_style_id are required")
	}
	err := s.repo.Products().RelinkDraftColorway(ctx, int(req.ColorwayId), int(req.TargetStyleId),
		int(req.ExpectedColorwayVersion), int(req.ExpectedTargetStyleVersion))
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "colourway %d or target style %d not found", req.ColorwayId, req.TargetStyleId)
		case errors.Is(err, entity.ErrColorwayNotDraft):
			return nil, status.Errorf(codes.FailedPrecondition, "colourway %d is not a draft; only drafts can be relinked", req.ColorwayId)
		case errors.Is(err, entity.ErrTechCardConflict):
			return nil, status.Error(codes.Aborted, "the colourway or a style was modified concurrently; reload and retry")
		default:
			slog.Default().ErrorContext(ctx, "can't relink draft colourway", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't relink colourway: %v", err)
		}
	}
	s.afterColorwayLifecycleChange(ctx, int(req.ColorwayId))
	return &pb_admin.RelinkDraftColorwayResponse{}, nil
}

// CloneStyleForSeason deep-clones a style (tech card header + ALL children) under a new sku_season
// (R4). It reuses the proven tech-card converters for a faithful copy and AddTechCard's child
// insertion; the clone starts as a fresh DRAFT with no colourways. A stale expected_source_version is
// ABORTED; an unknown source is NotFound; a (style_number, season) collision (a same-season clone) is
// FailedPrecondition.
func (s *Server) CloneStyleForSeason(ctx context.Context, req *pb_admin.CloneStyleForSeasonRequest) (*pb_admin.CloneStyleForSeasonResponse, error) {
	if req.SourceStyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "source_style_id is required")
	}
	if req.SkuSeason == nil || req.SkuSeason.Code == pb_common.SeasonEnum_SEASON_ENUM_UNKNOWN || req.SkuSeason.Year == 0 {
		return nil, status.Error(codes.InvalidArgument, "sku_season (code and year) is required")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, int(req.SourceStyleId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.SourceStyleId)
		}
		slog.Default().ErrorContext(ctx, "can't load source style for clone", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't load source style: %v", err)
	}
	if card.LockVersion != int(req.ExpectedSourceVersion) {
		return nil, status.Error(codes.Aborted, "source style was modified concurrently; reload and retry")
	}
	// Round-trip through the tech-card converters (header + every child) then override the season.
	pbInsert := dto.ConvertEntityTechCardToPb(card, s.costingFx(ctx)).GetTechCard()
	pbInsert.SkuSeason = req.SkuSeason
	insert, err := dto.ConvertPbTechCardInsertToEntity(pbInsert)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "can't build style clone: %v", err)
	}
	// A clone is a fresh design cycle for the new season — reset the PLM freeze so it is editable.
	insert.ApprovalState = entity.TechCardApprovalDraft
	newID, err := s.repo.TechCards().AddTechCard(ctx, insert)
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Errorf(codes.FailedPrecondition, "a style with this style number already exists for that season")
		}
		slog.Default().ErrorContext(ctx, "can't create style clone", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't create style clone: %v", err)
	}
	return &pb_admin.CloneStyleForSeasonResponse{NewStyleId: int32(newID)}, nil
}
