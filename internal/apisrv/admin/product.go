package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slices"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CreateColorway creates a new DRAFT colourway attached to an existing style (R2/R4 write
// decomposition, replacing the coupled UpsertColorway). It writes only colourway-owned data — no style
// facts (UpdateStyle), no variants (CreateVariant), no size chart (UpdateStyleSizeChart). The colourway
// starts DRAFT and goes live through PublishColorway.
func (s *Server) CreateColorway(ctx context.Context, req *pb_admin.CreateColorwayRequest) (*pb_admin.CreateColorwayResponse, error) {
	if _, write := s.costingAccess(ctx); !write && costPriceProvided(req.GetCostPrice()) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set a colourway cost_price")
	}
	prd, err := dto.BuildColorwayInsertEntity(req.GetMerchandising(), req.GetCountryCode(), req.GetThumbnailMediaId(), req.GetSecondaryThumbnailMediaId(), req.GetTranslations(), req.GetCostPrice())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("invalid colourway: %v", err))
	}
	id, err := s.repo.Products().CreateColorway(ctx, int(req.GetStyleId()), prd,
		dto.ConvertColorwayMediaIDs(req.GetMediaIds()), dto.ConvertColorwayTags(req.GetTags()), dto.ConvertColorwayPrices(req.GetPrices()))
	if err != nil {
		return nil, colorwayWriteError(ctx, "create", 0, err)
	}
	s.afterColorwayWrite(ctx, id)
	return &pb_admin.CreateColorwayResponse{ColorwayId: int32(id)}, nil
}

// UpdateColorway patches a colourway's own merchandising fields under an optimistic lock (R2/R4). It
// never touches style facts, variants, stock or the size chart.
func (s *Server) UpdateColorway(ctx context.Context, req *pb_admin.UpdateColorwayRequest) (*pb_admin.UpdateColorwayResponse, error) {
	if _, write := s.costingAccess(ctx); !write && costPriceProvided(req.GetCostPrice()) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set a colourway cost_price")
	}
	// Translations/media/tags/prices are a sparse update in the store (empty slice = leave unchanged).
	prd, err := dto.BuildColorwayInsertEntity(req.GetMerchandising(), req.GetCountryCode(), req.GetThumbnailMediaId(), req.GetSecondaryThumbnailMediaId(), req.GetTranslations(), req.GetCostPrice())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("invalid colourway: %v", err))
	}
	lockVersion, err := s.repo.Products().UpdateColorway(ctx, int(req.GetColorwayId()), int(req.GetExpectedColorwayVersion()), prd,
		dto.ConvertColorwayMediaIDs(req.GetMediaIds()), dto.ConvertColorwayTags(req.GetTags()), dto.ConvertColorwayPrices(req.GetPrices()))
	if err != nil {
		return nil, colorwayWriteError(ctx, "update", int(req.GetColorwayId()), err)
	}
	s.afterColorwayWrite(ctx, int(req.GetColorwayId()))
	return &pb_admin.UpdateColorwayResponse{LockVersion: int32(lockVersion)}, nil
}

// UpdateColorwayRecipe replaces a colourway's material recipe (usages) — the write-path cut in the R1
// merge (ColorwayDevelopmentInsert.usages was accepted but never written, A3.4). The recipe is a
// colourway-owned sub-aggregate: optimistically locked on the shared tech_card.lock_version, each
// usage references a style BOM line by its stable line_key (S2/S3).
func (s *Server) UpdateColorwayRecipe(ctx context.Context, req *pb_admin.UpdateColorwayRecipeRequest) (*pb_admin.UpdateColorwayRecipeResponse, error) {
	if req.GetColorwayId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "colorway_id is required")
	}
	usages, err := dto.ParseRecipeUsages(req.GetUsages())
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	newVersion, err := s.repo.TechCards().UpdateColorwayRecipe(ctx, int(req.GetColorwayId()), int(req.GetExpectedColorwayVersion()), usages)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't update colourway recipe",
			slog.Int("colorway_id", int(req.GetColorwayId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update colourway recipe")
	}
	return &pb_admin.UpdateColorwayRecipeResponse{LockVersion: int32(newVersion)}, nil
}

// colorwayWriteError maps a store colourway-write error to a gRPC status: absent style/colourway ->
// NotFound; a stale optimistic version -> Aborted; a business precondition (duplicate colour, frozen)
// -> FailedPrecondition; else Internal.
func colorwayWriteError(ctx context.Context, op string, id int, err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return status.Errorf(codes.NotFound, "colourway/style not found")
	case errors.Is(err, entity.ErrTechCardConflict):
		return status.Errorf(codes.Aborted, "colourway %d was modified concurrently; reload and retry", id)
	case errors.Is(err, entity.ErrColorwayColorExists):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	slog.Default().ErrorContext(ctx, "colourway write failed", slog.String("op", op), slog.Int("colorway_id", id), slog.String("err", err.Error()))
	return status.Errorf(codes.Internal, "can't %s colourway: %v", op, err)
}

// afterColorwayWrite refreshes the server-side hero cache and dictionary counts and triggers storefront
// revalidation after a colourway create/update (matches the legacy UpsertColorway side effects).
func (s *Server) afterColorwayWrite(ctx context.Context, id int) {
	if err := s.repo.Hero().RefreshHero(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh hero", slog.String("err", err.Error()))
	}
	s.afterColorwayLifecycleChange(ctx, id)
}

// ArchiveColorwayByID retires a colourway (archive-not-delete, R6): ACTIVE|HIDDEN -> ARCHIVED. Was
// DeleteColorwayByID (hard delete). The SKU stays frozen and readable; order history is unaffected.
func (s *Server) ArchiveColorwayByID(ctx context.Context, req *pb_admin.ArchiveColorwayByIDRequest) (*pb_admin.ArchiveColorwayByIDResponse, error) {
	if err := s.repo.Products().ArchiveColorway(ctx, int(req.ColorwayId)); err != nil {
		return nil, colorwayTransitionError(ctx, "archive", int(req.ColorwayId), err)
	}
	s.afterColorwayLifecycleChange(ctx, int(req.ColorwayId))
	return &pb_admin.ArchiveColorwayByIDResponse{}, nil
}

// PublishColorway transitions a DRAFT colourway to ACTIVE (R6). The store enforces the sellable
// preconditions and an optimistic guard on the current lifecycle_status.
func (s *Server) PublishColorway(ctx context.Context, req *pb_admin.PublishColorwayRequest) (*pb_admin.PublishColorwayResponse, error) {
	if err := s.repo.Products().PublishColorway(ctx, int(req.ColorwayId)); err != nil {
		return nil, colorwayTransitionError(ctx, "publish", int(req.ColorwayId), err)
	}
	s.afterColorwayLifecycleChange(ctx, int(req.ColorwayId))
	cw, err := s.getPbColorway(ctx, int(req.ColorwayId))
	if err != nil {
		return nil, err
	}
	return &pb_admin.PublishColorwayResponse{Colorway: cw}, nil
}

// TransitionColorwayStatus applies a non-publish lifecycle edge (R6): ACTIVE<->HIDDEN and
// ACTIVE|HIDDEN->ARCHIVED. DRAFT->ACTIVE uses PublishColorway (it carries preconditions). The store
// validates the edge through the entity state machine, so an illegal target is rejected there.
func (s *Server) TransitionColorwayStatus(ctx context.Context, req *pb_admin.TransitionColorwayStatusRequest) (*pb_admin.TransitionColorwayStatusResponse, error) {
	p := s.repo.Products()
	var op string
	var fn func(context.Context, int) error
	switch req.Target {
	case pb_common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_HIDDEN:
		// Target HIDDEN is reachable by two legal edges: hide (ACTIVE->HIDDEN) or restore/unarchive
		// (ARCHIVED->HIDDEN). The store router picks the correct one from the colourway's current state
		// (ARCHIVED->ACTIVE directly is not allowed — restore only ever lands on HIDDEN).
		op, fn = "hide/restore", p.TransitionColorwayToHidden
	case pb_common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ACTIVE:
		op, fn = "unhide", p.UnhideColorway // ACTIVE via transition = HIDDEN->ACTIVE (unhide); DRAFT->ACTIVE is PublishColorway
	case pb_common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ARCHIVED:
		op, fn = "archive", p.ArchiveColorway
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported transition target %v (DRAFT->ACTIVE uses PublishColorway)", req.Target)
	}
	if err := fn(ctx, int(req.ColorwayId)); err != nil {
		return nil, colorwayTransitionError(ctx, op, int(req.ColorwayId), err)
	}
	s.afterColorwayLifecycleChange(ctx, int(req.ColorwayId))
	cw, err := s.getPbColorway(ctx, int(req.ColorwayId))
	if err != nil {
		return nil, err
	}
	return &pb_admin.TransitionColorwayStatusResponse{Colorway: cw}, nil
}

// colorwayTransitionError maps a store lifecycle error to a gRPC status. The store returns descriptive
// wrapped errors (invalid edge, failed preconditions, concurrent change); missing colourway surfaces
// sql.ErrNoRows through the chain.
func colorwayTransitionError(ctx context.Context, op string, id int, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return status.Errorf(codes.NotFound, "colourway %d not found", id)
	}
	slog.Default().ErrorContext(ctx, "colourway lifecycle transition failed",
		slog.String("op", op), slog.Int("colorway_id", id), slog.String("err", err.Error()))
	// Domain refusals (invalid edge, unmet publish preconditions, non-draft relink, frozen siblings)
	// are FailedPrecondition; anything else is infrastructure and must surface as Internal, not a
	// client-fixable precondition (review finding backend-003).
	if errors.Is(err, entity.ErrColorwayNotDraft) ||
		errors.Is(err, entity.ErrStyleFrozenSiblings) ||
		errors.Is(err, entity.ErrColorwayColorExists) ||
		strings.Contains(err.Error(), "transition") ||
		strings.Contains(err.Error(), "precondition") {
		return status.Errorf(codes.FailedPrecondition, "cannot %s colourway %d: %v", op, id, err)
	}
	return status.Errorf(codes.Internal, "cannot %s colourway %d: %v", op, id, err)
}

// afterColorwayLifecycleChange refreshes dictionary counts and triggers storefront revalidation after
// a colourway's visibility changes.
func (s *Server) afterColorwayLifecycleChange(ctx context.Context, id int) {
	if di, err := s.repo.Cache().GetDictionaryInfo(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh dictionary counts", slog.String("err", err.Error()))
	} else {
		cache.RefreshDictionary(di)
	}
	s.revalidateAsync(&dto.RevalidationData{Products: []int{id}, Hero: true})
}

// getPbColorway loads a colourway and projects the admin Colorway (the nested message of ColorwayFull)
// for a transition response.
func (s *Server) getPbColorway(ctx context.Context, id int) (*pb_common.Colorway, error) {
	// includeArchived=false: post-transition reloads land on a non-archived row (hide/unhide/publish ->
	// HIDDEN/ACTIVE, restore -> HIDDEN), so the archived-detail capability is not needed here and stays
	// scoped to GetColorwayByID.
	pf, err := s.repo.Products().GetProductByIdShowHidden(ctx, id, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "colourway %d not found", id)
		}
		return nil, status.Errorf(codes.Internal, "can't load colourway %d: %v", id, err)
	}
	pb, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "can't convert colourway %d: %v", id, err)
	}
	return pb.GetColorway(), nil
}

func (s *Server) GetColorwayByID(ctx context.Context, req *pb_admin.GetColorwayByIDRequest) (*pb_admin.GetColorwayByIDResponse, error) {

	// Admin detail: includeArchived=true so an ARCHIVED colourway loads read-only (viewable/restorable in
	// the admin) instead of surfacing as NotFound. This is the only caller that opts into archived detail;
	// the storefront never reaches this path.
	pf, err := s.repo.Products().GetProductByIdShowHidden(ctx, int(req.ColorwayId), true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get product: %v", err)
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
	}

	// Confidential cost/provenance — admin surface only, on a field of the admin response,
	// and further gated by costing:read (task 19): a scoped account without it gets no cost.
	var costInfo *pb_admin.ColorwayCostInfo
	if read, _ := s.costingAccess(ctx); read {
		if ci, cerr := s.repo.Products().GetProductCostInfo(ctx, int(req.ColorwayId)); cerr != nil {
			slog.Default().ErrorContext(ctx, "can't get product cost info",
				slog.String("err", cerr.Error()))
		} else {
			costInfo = productCostInfoToPb(ci)
		}
	}

	// H1 fix: the recipe is colourway-owned (01-DOMAIN-MODEL §2.3), so GetColorwayByID is the minimum
	// surface that must return it — UpdateColorwayRecipe is a full-replace write, unsafe to edit
	// partially with no matching read (A3.4, the recipe used to be write-only). bomItems/orderQtyBySize
	// come from the owning style, best-effort: a lookup failure degrades to a bare recipe (no derived
	// line_total/size_run_total) rather than failing the whole colourway read.
	usages, err := s.repo.TechCards().GetColorwayRecipe(ctx, int(req.ColorwayId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get colourway recipe",
			slog.Int("colorway_id", int(req.ColorwayId)), slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get colourway recipe")
	}
	var bomItems []entity.TechCardBomItem
	orderQtyBySize := map[int]int{}
	if len(usages) > 0 {
		if styleTC, terr := s.repo.TechCards().GetTechCardById(ctx, pf.Product.StyleId); terr != nil {
			slog.Default().ErrorContext(ctx, "can't load owning style for colourway recipe pricing",
				slog.Int("colorway_id", int(req.ColorwayId)), slog.String("err", terr.Error()))
		} else {
			bomItems = styleTC.BomItems
			for _, q := range styleTC.SizeQuantities {
				orderQtyBySize[q.SizeId] = q.OrderQty
			}
		}
	}
	usagesPb := dto.ConvertRecipeUsagesToPb(usages, bomItems, orderQtyBySize)
	if read, _ := s.costingAccess(ctx); !read {
		stripTechCardColorwayUsageCosting(usagesPb)
	}

	return &pb_admin.GetColorwayByIDResponse{
		Colorway: pbPrd,
		CostInfo: costInfo,
		Usages:   usagesPb,
	}, nil

}

// SyncProductCostFromTechCard forces a product's cost_price to be (re)seeded from a tech
// card, overriding any manual value. tech_card_id (when > 0) repoints the product's primary
// card before seeding; otherwise the product's existing primary card is used. The card must
// currently link the product and have a computable unit cost in the base currency.
func (s *Server) SyncColorwayCostFromOwningStyle(ctx context.Context, req *pb_admin.SyncColorwayCostFromOwningStyleRequest) (*pb_admin.SyncColorwayCostFromOwningStyleResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to sync a colourway cost from its owning style")
	}
	colorwayID := int(req.ColorwayId)
	if colorwayID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "colorway_id is required")
	}
	ci, err := s.repo.Products().GetProductCostInfo(ctx, colorwayID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "colourway not found")
		}
		slog.Default().ErrorContext(ctx, "can't get colourway cost info", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get colourway")
	}

	// R4/p019: cost provenance is separated from ownership. The style is derived from the owner relation
	// (the colourway's primary card) and is NEVER repointed by a cost sync — tech_card_id is gone.
	if !ci.PrimaryTechCardID.Valid {
		return nil, status.Error(codes.FailedPrecondition, "colourway has no owning style (primary card) to source cost from")
	}
	techCardID := int(ci.PrimaryTechCardID.Int32)

	card, err := s.repo.TechCards().GetTechCardById(ctx, techCardID)
	if err != nil || card == nil {
		return nil, status.Error(codes.NotFound, "owning style not found")
	}
	unit, currency := dto.ComputeTechCardUnitCost(card, s.costingFx(ctx))
	if !unit.Valid {
		return nil, status.Error(codes.FailedPrecondition,
			"owning style has no base-currency unit cost — check the costing and its FX rates")
	}
	if !strings.EqualFold(currency, cache.GetBaseCurrency()) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"style unit cost is in %s, not base currency %s", currency, cache.GetBaseCurrency())
	}
	if err := s.repo.Products().ForceSetProductCostPriceFromTechCard(ctx, colorwayID, techCardID, unit.Decimal); err != nil {
		slog.Default().ErrorContext(ctx, "can't sync colourway cost from owning style", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't sync colourway cost")
	}
	return &pb_admin.SyncColorwayCostFromOwningStyleResponse{
		CostPrice:  &pb_decimal.Decimal{Value: unit.Decimal.String()},
		Currency:   currency,
		TechCardId: int32(techCardID),
		Source:     pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_STYLE,
	}, nil
}

// productCostInfoToPb converts the confidential product cost fields to their admin proto form.
func productCostInfoToPb(ci *entity.ColorwayCostInfo) *pb_admin.ColorwayCostInfo {
	if ci == nil {
		return nil
	}
	out := &pb_admin.ColorwayCostInfo{
		CostPriceSource:      costSourceToPb(ci.CostPriceSource), // R4: string -> ColorwayCostSource enum
		CostSourceTechCardId: ci.CostPriceTechCardID.Int32,       // R4: renamed from cost_price_tech_card_id
		PrimaryTechCardId:    ci.PrimaryTechCardID.Int32,
	}
	if ci.CostPrice.Valid {
		out.CostPrice = &pb_decimal.Decimal{Value: ci.CostPrice.Decimal.String()}
	}
	if ci.CostPriceUpdatedAt.Valid {
		out.CostPriceUpdatedAt = timestamppb.New(ci.CostPriceUpdatedAt.Time)
	}
	return out
}

// costSourceToPb maps the stored cost-provenance label to the ColorwayCostSource enum (R4). The
// legacy "tech_card" provenance is a style-owned cost (the primary card of the owning style), so it
// maps to STYLE.
func costSourceToPb(s sql.NullString) pb_common.ColorwayCostSource {
	if !s.Valid {
		return pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_UNKNOWN
	}
	switch s.String {
	case "manual":
		return pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_MANUAL
	case "tech_card", "style":
		return pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_STYLE
	case "production_run":
		return pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_PRODUCTION_RUN
	default:
		return pb_common.ColorwayCostSource_COLORWAY_COST_SOURCE_UNKNOWN
	}
}

func (s *Server) GetColorwaysPaged(ctx context.Context, req *pb_admin.GetColorwaysPagedRequest) (*pb_admin.GetColorwaysPagedResponse, error) {

	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc, err := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Price sorting requires currency; default to base currency when not specified (admin UX)
	baseCurrency := cache.GetBaseCurrency()
	if baseCurrency == "" {
		baseCurrency = "EUR"
	}
	for _, sf := range sfs {
		if sf == entity.Price {
			if fc == nil {
				fc = &entity.FilterConditions{Currency: baseCurrency}
			} else if fc.Currency == "" {
				fc.Currency = baseCurrency
			}
			break
		}
	}

	// R6/§14.6: show_hidden was replaced by an explicit lifecycle-status filter, now honoured in full. This
	// is the ADMIN service, so we always take the admin store path (showHidden=true): the storefront
	// tier-gating/visibility logic applies ONLY to the storefront (showHidden=false) path and must never
	// gate the admin catalogue. The status set selects which lifecycle states to return — empty =
	// ACTIVE-only default; a set may combine DRAFT/ACTIVE/HIDDEN/ARCHIVED and unions. UNKNOWN/invalid
	// statuses are dropped here (and again, fail-safe, in the store).
	statuses := make([]entity.ColorwayStatus, 0, len(req.Statuses))
	for _, st := range req.Statuses {
		if cs := entity.ColorwayStatus(st); cs.IsValid() {
			statuses = append(statuses, cs)
		}
	}
	limit, offset := clampPagination(int(req.Limit), int(req.Offset))
	prds, total, err := s.repo.Products().GetProductsPaged(ctx, limit, offset, sfs, of, fc, statuses, true)
	if err != nil {
		if err.Error() == "price sorting requires currency to be specified in filter conditions" {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_common.Colorway, 0, len(prds))
	for _, prd := range prds {
		pbPrd, err := dto.ConvertEntityProductToCommon(&prd)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
				slog.Int("product_id", prd.Id),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product: product_id=%d: %v", prd.Id, err)
		}
		prdsPb = append(prdsPb, pbPrd)
	}

	return &pb_admin.GetColorwaysPagedResponse{
		Colorways: prdsPb,
		Total:     int32(total),
	}, nil
}

func (s *Server) UpdateVariantStock(ctx context.Context, req *pb_admin.UpdateVariantStockRequest) (*pb_admin.UpdateVariantStockResponse, error) {
	quantity := int(req.Quantity)

	// R2/p012: stock is addressed by the stable variant id (product_size.id) and NEVER creates a variant.
	// Resolve it to the denormalised (product_id, size_id) the stock path keys on; an unknown variant is
	// NOT_FOUND, not a silent implicit insert.
	if req.VariantId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "variant_id is required")
	}
	variant, err := s.repo.Products().GetVariantByID(ctx, int(req.VariantId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "variant %d not found", req.VariantId)
		}
		slog.Default().ErrorContext(ctx, "can't resolve variant for stock update", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't resolve variant")
	}
	// R2/0155: an archived variant is retired — its stock is frozen and it rejects stock writes.
	if variant.Status == uint8(entity.VariantStatusArchived) {
		return nil, status.Errorf(codes.FailedPrecondition, "variant %d is archived", req.VariantId)
	}
	productId := variant.ProductId
	sizeId := variant.SizeId

	// Validate mode
	if req.Mode == pb_common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "mode is required (set or adjust)")
	}

	// Validate reason is provided
	if req.Reason == pb_common.StockChangeReason_STOCK_CHANGE_REASON_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "reason is required for stock adjustment")
	}

	// Convert reason enum to string
	reason := dto.StockChangeReasonToString(req.Reason)
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid reason value")
	}

	// Get comment if provided
	comment := ""
	if req.Comment != nil {
		comment = *req.Comment
	}

	isSetMode := req.Mode == pb_common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET
	isAdjustMode := req.Mode == pb_common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_ADJUST

	// validation matrix

	switch req.Reason {
	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT:
		// stock_count: allowed only with mode="set", direction not allowed
		if !isSetMode {
			return nil, status.Error(codes.InvalidArgument, "stock_count reason is only allowed with mode=set")
		}
		if req.Direction != pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "direction must not be specified for stock_count reason")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_DAMAGE:
		// damage: allowed only with mode="adjust", direction must be "decrease"
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "damage reason is only allowed with mode=adjust")
		}
		if req.Direction != pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_DECREASE {
			return nil, status.Error(codes.InvalidArgument, "damage reason requires direction=decrease")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_LOSS:
		// loss: allowed only with mode="adjust", direction must be "decrease"
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "loss reason is only allowed with mode=adjust")
		}
		if req.Direction != pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_DECREASE {
			return nil, status.Error(codes.InvalidArgument, "loss reason requires direction=decrease")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_FOUND:
		// found: allowed only with mode="adjust", direction must be "increase"
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "found reason is only allowed with mode=adjust")
		}
		if req.Direction != pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE {
			return nil, status.Error(codes.InvalidArgument, "found reason requires direction=increase")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_CORRECTION:
		// correction: allowed only with mode="adjust", direction can be increase or decrease
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "correction reason is only allowed with mode=adjust")
		}
		if req.Direction == pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "direction is required for correction reason")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_RESERVED_RELEASE:
		// reserved_release: allowed only with mode="adjust", direction must be "increase"
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "reserved_release reason is only allowed with mode=adjust")
		}
		if req.Direction != pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE {
			return nil, status.Error(codes.InvalidArgument, "reserved_release reason requires direction=increase")
		}

	case pb_common.StockChangeReason_STOCK_CHANGE_REASON_OTHER:
		// other: allowed only with mode="adjust", direction can be increase or decrease, comment required
		if !isAdjustMode {
			return nil, status.Error(codes.InvalidArgument, "other reason is only allowed with mode=adjust")
		}
		if req.Direction == pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "direction is required for other reason")
		}
		if comment == "" {
			return nil, status.Error(codes.InvalidArgument, "comment is required when reason is other")
		}

	default:
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("unsupported reason for manual stock adjustment: %s", reason))
	}

	// quantity validation

	if isSetMode {
		// mode="set": quantity means final stock value, must be >= 0
		if quantity < 0 {
			return nil, status.Error(codes.InvalidArgument, "quantity must be >= 0 for mode=set")
		}
	}

	if isAdjustMode {
		// mode="adjust": quantity means delta amount, must be > 0
		if quantity <= 0 {
			return nil, status.Error(codes.InvalidArgument, "quantity must be > 0 for mode=adjust")
		}
		// direction is required for adjust mode (already validated per-reason above)
		if req.Direction == pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "direction is required for mode=adjust")
		}
	}

	// Resolve the operation into mode + amount for the store, which reads+computes+writes atomically
	// under a row lock (problem 025): mode=set passes the absolute value, mode=adjust passes a signed
	// delta so concurrent adjustments compose instead of both overwriting a stale-read absolute.
	var mode entity.StockUpdateMode
	var amount int
	if isSetMode {
		mode = entity.StockUpdateModeSet
		amount = quantity
	} else {
		mode = entity.StockUpdateModeAdjust
		if req.Direction == pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE {
			amount = quantity
		} else {
			amount = -quantity
		}
	}

	previousQuantity, newQuantityDecimal, err := s.repo.Products().UpdateProductSizeStockWithHistory(ctx, productId, sizeId, mode, amount, reason, comment)
	if err != nil {
		var verr *entity.ValidationError
		if errors.As(err, &verr) {
			return nil, status.Error(codes.InvalidArgument, verr.Message)
		}
		slog.Default().ErrorContext(ctx, "can't update product size stock",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product size stock")
	}

	// Waitlist notification from the REAL committed transition (0 -> >0), using the store's locked
	// before/after — not a pre-read value that a concurrent adjustment could have invalidated.
	if previousQuantity.LessThanOrEqual(decimal.Zero) && newQuantityDecimal.GreaterThan(decimal.Zero) {
		// Trigger waitlist notifications asynchronously. This is a detached,
		// best-effort side effect; a panic inside it (DB, DTO render, mail) must
		// be logged with a stack and swallowed, never crash the single-process
		// backend that also serves payments and webhooks.
		go func() {
			defer saferun.Recover(context.Background(), "notify-waitlist")
			s.notifyWaitlist(ctx, productId, sizeId)
		}()
	}

	s.revalidateAsync(&dto.RevalidationData{
		Products: []int{productId},
		Hero:     true,
	})
	return &pb_admin.UpdateVariantStockResponse{}, nil
}

func (s *Server) ListStockChangeHistory(ctx context.Context, req *pb_admin.ListStockChangeHistoryRequest) (*pb_admin.ListStockChangeHistoryResponse, error) {
	var productId, sizeId *int
	if req.ColorwayId != 0 {
		pid := int(req.ColorwayId)
		productId = &pid
	}
	if req.SizeId != nil && *req.SizeId != 0 {
		sid := int(*req.SizeId)
		sizeId = &sid
	}
	var dateFrom, dateTo *time.Time
	if req.DateFrom != nil {
		t := req.DateFrom.AsTime()
		dateFrom = &t
	}
	if req.DateTo != nil {
		t := req.DateTo.AsTime()
		dateTo = &t
	}
	limit := int(req.Limit)
	offset := int(req.Offset)
	if offset < 0 {
		offset = 0
	}
	// limit=0 means not set -> return all records; otherwise cap at 100
	if limit > 0 && limit > 100 {
		limit = 100
	}
	orderFactor := entity.Descending
	if req.OrderFactor != nil {
		orderFactor = dto.ConvertPBCommonOrderFactorToEntity(*req.OrderFactor)
	}

	sourceFilter := dto.StockChangeSourceToFilterString(req.Source)
	changes, total, err := s.repo.Products().GetStockChangeHistory(ctx, productId, sizeId, dateFrom, dateTo, sourceFilter, limit, offset, orderFactor)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get stock change history",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get stock change history")
	}

	pbChanges := make([]*pb_common.StockChange, 0, len(changes))
	for _, c := range changes {
		pbChanges = append(pbChanges, dto.StockChangeToProto(&c))
	}
	return &pb_admin.ListStockChangeHistoryResponse{
		Changes: pbChanges,
		Total:   int32(total),
	}, nil
}

// ListStockChanges returns simplified stock changes for reporting.
func (s *Server) ListStockChanges(ctx context.Context, req *pb_admin.ListStockChangesRequest) (*pb_admin.ListStockChangesResponse, error) {
	// Validate required fields
	if req.From == nil || req.To == nil {
		return nil, status.Error(codes.InvalidArgument, "from and to dates are required")
	}

	dateFrom := req.From.AsTime()
	dateTo := req.To.AsTime()

	// Validate date range
	if dateTo.Before(dateFrom) {
		return nil, status.Error(codes.InvalidArgument, "to date must be after from date")
	}

	// Set defaults and limits
	limit := int(req.Limit)
	// If limit is negative, return all records (no pagination)
	// If limit is 0, use default of 100
	// If limit > 0 and <= 10000, use as specified
	if limit == 0 {
		limit = 100 // Default pagination
	} else if limit > 0 && limit > 10000 {
		limit = 10000 // Max limit for safety
	}
	// If limit < 0, it means "return all" - pass it as-is to repository
	offset := int(req.Offset)

	// Optional filters
	var productId *int
	if req.ColorwayId != nil {
		pid := int(*req.ColorwayId)
		productId = &pid
	}

	var sizeId *int
	if req.SizeId != nil {
		sid := int(*req.SizeId)
		sizeId = &sid
	}

	// Convert source enum to string (empty string for UNSPECIFIED = no filter)
	source := dto.StockChangeSourceToFilterString(req.Source)

	// Sort by direction (default = unspecified = no direction filter)
	var sortByDirection entity.StockAdjustmentDirection
	if req.SortByDirection != nil {
		switch *req.SortByDirection {
		case pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE:
			sortByDirection = entity.StockAdjustmentDirectionIncrease
		case pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_DECREASE:
			sortByDirection = entity.StockAdjustmentDirectionDecrease
		}
	}

	// Order factor (default DESC = newest first)
	orderFactor := entity.Descending
	if req.OrderFactor != nil {
		orderFactor = dto.ConvertPBCommonOrderFactorToEntity(*req.OrderFactor)
	}

	// Get data from repository
	changes, total, err := s.repo.Products().GetStockChanges(ctx, dateFrom, dateTo, productId, sizeId, source, limit, offset, sortByDirection, orderFactor)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get stock changes",
			slog.String("err", err.Error()),
		)
		return nil, status.Error(codes.Internal, "failed to get stock changes")
	}

	// Map to proto
	protoChanges := make([]*pb_admin.StockChangeRow, 0, len(changes))
	for i := range changes {
		protoChanges = append(protoChanges, dto.StockChangeRowToProto(&changes[i]))
	}

	return &pb_admin.ListStockChangesResponse{
		Changes: protoChanges,
		Total:   int32(total),
	}, nil
}

// notifyWaitlist processes waitlist entries and sends back-in-stock notifications
func (s *Server) notifyWaitlist(ctx context.Context, productId int, sizeId int) {
	notifyCtx := context.Background() // Use background context to avoid cancellation

	// Get product details (includeArchived=false: a waitlist notification never targets an archived colourway)
	product, err := s.repo.Products().GetProductByIdShowHidden(notifyCtx, productId, false)
	if err != nil {
		slog.Default().ErrorContext(notifyCtx, "can't get product for waitlist notification",
			slog.String("err", err.Error()),
			slog.Int("productId", productId),
		)
		return
	}

	// Get waitlist entries with buyer names in a single query
	entriesWithNames, err := s.repo.Products().GetWaitlistEntriesWithBuyerNames(notifyCtx, productId, sizeId)
	if err != nil {
		slog.Default().ErrorContext(notifyCtx, "can't get waitlist entries with buyer names",
			slog.String("err", err.Error()),
			slog.Int("productId", productId),
			slog.Int("sizeId", sizeId),
		)
		return
	}

	if len(entriesWithNames) == 0 {
		return
	}

	// Send emails to all waitlist entries and remove them
	for _, entry := range entriesWithNames {
		// Build buyer name from the entry data
		buyerName := ""
		if entry.FirstName.Valid && entry.FirstName.String != "" {
			buyerName = entry.FirstName.String
			if entry.LastName.Valid && entry.LastName.String != "" {
				buyerName = fmt.Sprintf("%s %s", entry.FirstName.String, entry.LastName.String)
			}
		}

		// Convert to back-in-stock DTO with buyer name
		productDetails := dto.ProductFullToBackInStock(product, sizeId, buyerName, entry.Email)

		err = s.mailer.SendBackInStock(notifyCtx, s.repo, entry.Email, productDetails)
		if err != nil {
			slog.Default().ErrorContext(notifyCtx, "can't send back in stock email",
				slog.String("err", err.Error()),
				slog.String("email", entry.Email),
				slog.Int("productId", productId),
				slog.Int("sizeId", sizeId),
			)
			// Continue processing other entries even if one fails
		} else {
			// Remove from waitlist after successful email queue
			err = s.repo.Products().RemoveFromWaitlist(notifyCtx, productId, sizeId, entry.Email)
			if err != nil {
				slog.Default().ErrorContext(notifyCtx, "can't remove from waitlist",
					slog.String("err", err.Error()),
					slog.String("email", entry.Email),
					slog.Int("productId", productId),
					slog.Int("sizeId", sizeId),
				)
			}
		}
	}
}
