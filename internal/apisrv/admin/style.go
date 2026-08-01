package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// styleMaskHas reports whether a field-mask path names the given style fact, folding case and
// underscores so snake_case ("target_gender") and camelCase ("targetGender") both match.
func styleMaskHas(mask []string, field string) bool {
	want := strings.ToLower(strings.ReplaceAll(field, "_", ""))
	for _, m := range mask {
		if strings.ToLower(strings.ReplaceAll(strings.TrimSpace(m), "_", "")) == want {
			return true
		}
	}
	return false
}

// UpdateStyle writes a style's catalogue facts (brand/season/collection/gender/fit/composition/care/
// model-wears/categories) — the sole writer of those facts (R4/§14.7). A stale expected_lock_version
// is ABORTED; a SKU-fact (season) change with any SKU-frozen sibling colourway is FailedPrecondition
// (clone for the new season instead); an unknown style is NotFound. The save also re-derives the
// structural composition (S17) from the style's shell-fabric BOM — a fabric line whose own composition
// does not sum to 100 is a field-tagged InvalidArgument (apierr), same as any other bad-input rejection.
func (s *Server) UpdateStyle(ctx context.Context, req *pb_admin.UpdateStyleRequest) (*pb_admin.UpdateStyleResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	p := req.GetPatch()
	// A field mask limits the write to the named facts (the tech card owns fit/composition/care, the
	// colourway card owns model-wears); no mask ⇒ full replace, the legacy behaviour every current
	// caller relied on. The store honors the mask on the SQL side; here we only make the strict pb→entity
	// conversion tolerant of the enum facts (gender/season) a partial patch legitimately omits — an
	// omitted, unmasked enum arrives as UNKNOWN, which the converter rejects, yet it is never written.
	mask := req.GetUpdateMask().GetPaths()
	season, gender := p.GetSeason(), p.GetTargetGender()
	seasonYear := p.GetSeasonYear()
	if len(mask) > 0 {
		if !styleMaskHas(mask, "season") {
			season = pb_common.SeasonEnum_SEASON_ENUM_SS // placeholder; excluded from the write by the mask
			// The year rides the same mask path, so it is equally excluded — zero it rather than let
			// a stale value from an untouched field fail the converter's range check.
			seasonYear = 0
		}
		if !styleMaskHas(mask, "targetGender") {
			gender = pb_common.GenderEnum_GENDER_ENUM_UNISEX // placeholder; excluded from the write by the mask
		}
	}
	// Care is validated and canonicalised HERE rather than in the store, because this is the only
	// place that knows whether the caller actually MEANT to write it.
	//
	// Strict only when the mask names care. An unmasked full replace carries whatever care string the
	// client happened to have loaded — for a style that still holds pre-ISO free text that is a
	// carry-over, not an authored choice, and rejecting it would make "edit the fit and save"
	// impossible until someone re-picked care. So an unmasked write canonicalises when the value
	// resolves and passes it through untouched when it does not.
	//
	// A masked write is the opposite: the caller is writing care, so it has to land real codes, or
	// the column never converges and the storefront can never rely on it. Canonicalising also means
	// the same picks always store the same string, which is what makes the value comparable and the
	// printed label stable.
	careInstructions := p.GetCareInstructions()
	if careMasked := styleMaskHas(mask, "careInstructions"); careMasked || len(mask) == 0 {
		canonical, cerr := cache.GetCareIndex().Normalize(careInstructions)
		switch {
		case cerr == nil:
			careInstructions = canonical
		case careMasked:
			var ve *entity.ValidationError
			if errors.As(cerr, &ve) {
				return nil, apierr.Invalid(ve)
			}
			return nil, status.Errorf(codes.InvalidArgument, "invalid care instructions: %v", cerr)
		}
	}

	patch, err := dto.ConvertPbStylePatchToEntity(p.GetBrand(), season, seasonYear, p.GetCollection(), gender,
		p.GetFit(), p.GetComposition(), careInstructions,
		p.GetModelWearsHeightCm(), p.GetModelWearsSizeId(), p.GetTopCategoryId(), p.GetSubCategoryId(), p.GetTypeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid style patch: %v", err)
	}
	lockVersion, err := s.repo.Products().UpdateStyle(ctx, int(req.StyleId), int(req.ExpectedLockVersion), patch, mask)
	if err != nil {
		var ve *entity.ValidationError
		switch {
		case errors.As(err, &ve):
			return nil, apierr.Invalid(ve)
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
		return nil, techCardConvertErr(err)
	}
	steps, err := dto.StyleSizeChartGradeStepsFromPb(req.GradeSteps)
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	// A step for a measurement the chart does not carry cannot be applied to anything, and would
	// resurface as a phantom column the next time the grid is opened. Reject it here rather than
	// storing a rule that grades nothing.
	if len(steps) > 0 {
		charted := make(map[int]bool, len(cells))
		for _, c := range cells {
			charted[c.MeasurementNameID] = true
		}
		for _, st := range steps {
			if !charted[st.MeasurementNameID] {
				return nil, status.Errorf(codes.InvalidArgument,
					"grade step for measurement %d, which the chart has no cells for", st.MeasurementNameID)
			}
		}
	}
	// Same reasoning for the rule's BASE size. value(size) = base + step × (position(size) −
	// position(base)) is anchored on the base size's own cells, so a base the chart has no column for
	// leaves every derived value pointing at a cell that does not exist: the grid re-opens with an
	// unusable rule and the "derived vs overtyped" comparison misreads every cell. The store's FK
	// (fk_tech_card_grade_base_size) only proves the size exists GLOBALLY, not that it is charted.
	if req.GradeBaseSizeId > 0 && len(cells) > 0 {
		chartedSize := false
		for _, c := range cells {
			if c.SizeID == int(req.GradeBaseSizeId) {
				chartedSize = true
				break
			}
		}
		if !chartedSize {
			return nil, status.Errorf(codes.InvalidArgument,
				"grade_base_size_id %d is not one of the chart's sizes", req.GradeBaseSizeId)
		}
	}
	chart, err := s.repo.TechCards().UpdateStyleSizeChart(ctx, int(req.StyleId), int(req.ExpectedLockVersion), cells, int(req.GradeBaseSizeId), steps)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		case errors.Is(err, entity.ErrTechCardConflict):
			return nil, status.Error(codes.Aborted, "style was modified concurrently; reload the chart and retry")
		case errors.Is(err, entity.ErrTechCardReleased):
			return nil, status.Error(codes.FailedPrecondition, entity.ErrTechCardReleased.Error())
		case s.repo.IsErrForeignKeyViolation(err):
			return nil, status.Error(codes.InvalidArgument, "size chart references an unknown size, measurement name or grade base size")
		case s.repo.IsErrUniqueViolation(err):
			// Backstop for uniq_tech_card_size_measurement / uniq_tcgr_card_name. The parsers reject a
			// repeated cell or grade step first, so reaching here means a direct/legacy caller — still
			// the client's mistake, not ours, so InvalidArgument rather than an opaque Internal.
			return nil, status.Error(codes.InvalidArgument,
				"the chart lists the same size and point of measure (or the same graded measurement) twice")
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
// ABORTED; an unknown source is NotFound; the clone receives a fresh generated style number for its
// target season.
func (s *Server) CloneStyleForSeason(ctx context.Context, req *pb_admin.CloneStyleForSeasonRequest) (*pb_admin.CloneStyleForSeasonResponse, error) {
	if req.SourceStyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "source_style_id is required")
	}
	seasonCode, seasonYear, err := dto.ConvertPbSkuSeasonToEntity(req.SkuSeason)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid sku_season: %v", err)
	}
	if seasonCode == "" {
		return nil, status.Error(codes.InvalidArgument, "sku_season (code and year) is required")
	}
	card, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, int(req.SourceStyleId))
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
	full := dto.ConvertEntityTechCardToPb(card, s.costingFx(ctx))
	// The round-trip carries the SOURCE card's costing block and BOM purchase prices into the new
	// card, so a products:write account could mint itself a copy of confidential costs it can neither
	// read nor write — CreateTechCard's techCardInsertHasCostingData gate never sees this path because
	// the payload is server-built, not client-sent. Strip the money before the insert (same helper the
	// read path uses): the clone then starts costing-free and a costing role fills it in.
	if _, write := s.costingAccess(ctx); !write {
		stripTechCardCosting(full)
	}
	pbInsert := full.GetTechCard()
	pbInsert.SkuSeason = req.SkuSeason
	styleNumber, err := s.repo.TechCards().SuggestStyleNumber(ctx, string(seasonCode), seasonYear)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't suggest style number for clone", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't generate a style number for the clone")
	}
	pbInsert.StyleNumber = styleNumber
	pbInsert.StyleNumberSource = pb_common.StyleNumberSource_STYLE_NUMBER_SOURCE_GENERATED
	insert, err := dto.ConvertPbTechCardInsertToEntity(pbInsert)
	if err != nil {
		// Field-tagged when the SOURCE card carries something the converter rejects, so the operator
		// is pointed at the offending line rather than at the clone attempt.
		return nil, techCardConvertErr(err)
	}
	// A clone is a fresh design cycle for the new season — reset the PLM freeze so it is editable.
	insert.ApprovalState = entity.TechCardApprovalDraft
	// The full read/convert above is intentionally outside AddTechCard's transaction. Narrow the
	// remaining race window with one cheap header read immediately before the insert; a fully
	// transaction-scoped deep clone is a separate change.
	currentVersion, err := s.repo.TechCards().GetTechCardLockVersion(ctx, int(req.SourceStyleId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.SourceStyleId)
		}
		slog.Default().ErrorContext(ctx, "can't recheck source style for clone", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't recheck source style before cloning")
	}
	if currentVersion != int(req.ExpectedSourceVersion) {
		return nil, status.Error(codes.Aborted, "source style was modified concurrently; reload and retry")
	}
	newID, err := s.repo.TechCards().AddTechCard(ctx, insert)
	if err != nil {
		// A clone round-trips the source card's category_id and size_ids, so AddTechCard can raise the
		// same field-tagged rejections a fresh create can (a size outside the category's size systems,
		// a category whose tree has no top-level ancestor). Surface them as InvalidArgument with the
		// field attached, exactly as CreateTechCard does — otherwise a bad SOURCE card turns into an
		// opaque 500 on the clone and the operator has nothing to act on.
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.FailedPrecondition, "the generated style number was claimed concurrently; retry the clone")
		}
		slog.Default().ErrorContext(ctx, "can't create style clone", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't create style clone: %v", err)
	}
	return &pb_admin.CloneStyleForSeasonResponse{NewStyleId: int32(newID)}, nil
}
