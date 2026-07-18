package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/stylenumber"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// techCardFKMsg is returned when a tech card references a missing category, base
// model, base sample size, size, product or media row.
const techCardFKMsg = "tech card references a non-existent category, model, size, product, media or fitting"

// styleNumberTaken is the field-tagged rejection for a global-UNIQUE(style_number) collision (Q1).
func styleNumberTaken() error {
	return apierr.Invalid(entity.NewFieldViolation("style_number", "already_exists", "",
		"this style number is already used by another style; choose a different one or accept a fresh generated proposal"))
}

// validateStyleNumberOverride enforces the strict manual-override contract (Q1): when the owner
// hand-sets the article (style_number_source = manual) the value must be present and pass the strict
// format validator, else a field-tagged InvalidArgument on style_number. A generated (server-
// proposed) value is trusted and not re-validated here; the global UNIQUE(style_number) index guards
// collisions for both paths.
func validateStyleNumberOverride(tc *entity.TechCardInsert) error {
	if tc.StyleNumberSource != entity.StyleNumberSourceManual {
		return nil
	}
	v := strings.TrimSpace(tc.StyleNumber.String)
	if !tc.StyleNumber.Valid || v == "" {
		return apierr.Invalid(entity.NewFieldViolation("style_number", "required_for_manual_override", "",
			"a manual override needs a style number; set style_number_source=generated to use the proposal"))
	}
	if reason := stylenumber.ValidateManual(v); reason != "" {
		return apierr.Invalid(entity.NewFieldViolation("style_number", reason, "", stylenumber.ManualHint()))
	}
	return nil
}

// validateCategoryLeaf rejects a non-leaf category_id (one that has child categories): a
// tech card must be filed under a leaf type, not a parent bucket (plan Q5). An unset/zero
// category is allowed; an unknown id falls through to the FK check on write. The category
// tree comes from the dictionary cache (the same source the product admin uses).
func (s *Server) validateCategoryLeaf(ctx context.Context, categoryId sql.NullInt32) error {
	if !categoryId.Valid || categoryId.Int32 <= 0 {
		return nil
	}
	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load categories for leaf check", slog.String("err", err.Error()))
		return status.Error(codes.Internal, "can't validate category")
	}
	for _, c := range di.Categories {
		if c.ParentID != nil && int32(*c.ParentID) == categoryId.Int32 {
			return status.Error(codes.InvalidArgument, "category_id must be a leaf category (it has sub-categories)")
		}
	}
	return nil
}

// CreateTechCard creates a new tech card with its nested sections.
func (s *Server) CreateTechCard(ctx context.Context, req *pb_admin.CreateTechCardRequest) (*pb_admin.CreateTechCardResponse, error) {
	if _, write := s.costingAccess(ctx); !write && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set cost data (costing block or BOM prices)")
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.validateCategoryLeaf(ctx, tc.CategoryId); err != nil {
		return nil, err
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	// Server-stamp the audit trail (norm §2.11); client-sent values are ignored.
	username := authsrv.GetAdminUsername(ctx)
	tc.CreatedBy, tc.UpdatedBy = username, username

	id, err := s.repo.TechCards().AddTechCard(ctx, tc)
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, styleNumberTaken()
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't add tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add tech card")
	}
	s.seedProductCostsFromTechCard(ctx, id)
	s.snapshotReleaseIfReleased(ctx, id)
	return &pb_admin.CreateTechCardResponse{Id: int32(id)}, nil
}

// SuggestStyleNumber proposes the next free style number for a season (Q1). Advisory: the client may
// accept the proposal (style_number_source=GENERATED) or override it (MANUAL) on the tech-card write.
func (s *Server) SuggestStyleNumber(ctx context.Context, req *pb_admin.SuggestStyleNumberRequest) (*pb_admin.SuggestStyleNumberResponse, error) {
	code, year, err := dto.ConvertPbSkuSeasonToEntity(req.SkuSeason)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid sku_season: %v", err)
	}
	if code == "" {
		return nil, status.Error(codes.InvalidArgument, "sku_season (code + year) is required to propose a style number")
	}
	proposal, err := s.repo.TechCards().SuggestStyleNumber(ctx, string(code), year)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't suggest style number", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't suggest style number")
	}
	return &pb_admin.SuggestStyleNumberResponse{StyleNumber: proposal}, nil
}

// GetTechCard returns a tech card by id with its nested sections resolved.
func (s *Server) GetTechCard(ctx context.Context, req *pb_admin.GetTechCardRequest) (*pb_admin.GetTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	tc, err := s.repo.TechCards().GetTechCardById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get tech card")
	}
	pbTc := dto.ConvertEntityTechCardToPb(tc, s.costingFx(ctx))
	if read, _ := s.costingAccess(ctx); !read {
		stripTechCardCosting(pbTc)
	}
	return &pb_admin.GetTechCardResponse{TechCard: pbTc}, nil
}

// UpdateTechCard updates a tech card, replacing its nested sections.
func (s *Server) UpdateTechCard(ctx context.Context, req *pb_admin.UpdateTechCardRequest) (*pb_admin.UpdateTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	_, canWriteCosting := s.costingAccess(ctx)
	if !canWriteCosting && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to modify cost data (costing block or BOM prices)")
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.validateCategoryLeaf(ctx, tc.CategoryId); err != nil {
		return nil, err
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	tc.UpdatedBy = authsrv.GetAdminUsername(ctx) // server-stamp; created_by is preserved (not in SET)
	// A cost-stripped account's full-replace save must not blank the costing it never saw.
	if !canWriteCosting {
		s.preserveStoredCosting(ctx, int(req.Id), tc)
	}
	if err := s.repo.TechCards().UpdateTechCard(ctx, int(req.Id), tc, int(req.ExpectedLockVersion)); err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		if errors.Is(err, entity.ErrTechCardConflict) {
			return nil, status.Error(codes.Aborted, "tech card was modified concurrently; reload and retry")
		}
		if errors.Is(err, entity.ErrTechCardReleased) {
			return nil, status.Error(codes.FailedPrecondition, "tech card is released and frozen; re-open to draft to edit")
		}
		if errors.Is(err, entity.ErrTechCardPurposeLocked) {
			return nil, status.Error(codes.FailedPrecondition, entity.ErrTechCardPurposeLocked.Error())
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, styleNumberTaken()
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't update tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update tech card")
	}
	s.seedProductCostsFromTechCard(ctx, int(req.Id))
	s.snapshotReleaseIfReleased(ctx, int(req.Id))
	return &pb_admin.UpdateTechCardResponse{}, nil
}

// snapshotReleaseIfReleased captures an immutable release snapshot (task 11) when a card is in
// the `released` state after a successful save. Because a released card is frozen — the store
// rejects any non-draft edit — a successful save that ends in `released` is always a genuine
// release transition (an already-released card can only move to draft), so this fires exactly
// once per release episode. The snapshot is the enriched read-model as proto-JSON plus the
// computed base-currency unit cost. It is best-effort: the release itself already committed, and
// the frozen content means an identical snapshot can be regenerated on a later re-release — so a
// failure here is logged, never surfaced as a failed release.
func (s *Server) snapshotReleaseIfReleased(ctx context.Context, techCardID int) {
	card, err := s.repo.TechCards().GetTechCardById(ctx, techCardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "release snapshot: can't reload tech card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	if card == nil || card.ApprovalState != entity.TechCardApprovalReleased {
		return
	}
	fx := s.costingFx(ctx)
	blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(card, fx))
	if err != nil {
		slog.Default().ErrorContext(ctx, "release snapshot: can't marshal snapshot",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	unit, currency := dto.ComputeTechCardUnitCost(card, fx)
	username := authsrv.GetAdminUsername(ctx)
	rel := entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: techCardID,
			Version:    card.Version,
			ReleasedBy: sql.NullString{String: username, Valid: username != ""},
			UnitCost:   unit,
			Currency:   sql.NullString{String: currency, Valid: unit.Valid && currency != ""},
		},
		Snapshot: string(blob),
	}
	if err := s.repo.TechCards().SaveTechCardRelease(ctx, rel); err != nil {
		slog.Default().ErrorContext(ctx, "release snapshot: can't save (card is released; a later re-release will re-snapshot)",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	slog.Default().InfoContext(ctx, "captured tech card release snapshot",
		slog.Int("tech_card_id", techCardID), slog.String("version", card.Version.String))
}

// ListTechCardReleases returns a card's release history (newest-first, metadata only).
func (s *Server) ListTechCardReleases(ctx context.Context, req *pb_admin.ListTechCardReleasesRequest) (*pb_admin.ListTechCardReleasesResponse, error) {
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	rows, err := s.repo.TechCards().ListTechCardReleases(ctx, int(req.TechCardId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech card releases", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list tech card releases")
	}
	read, _ := s.costingAccess(ctx)
	out := make([]*pb_common.TechCardReleaseMeta, 0, len(rows))
	for _, r := range rows {
		m := dto.ConvertTechCardReleaseMetaToPb(r)
		if !read {
			stripReleaseMetaCosting(m)
		}
		out = append(out, m)
	}
	return &pb_admin.ListTechCardReleasesResponse{Releases: out}, nil
}

// GetTechCardRelease returns a single release: its metadata plus the frozen contract TechCard
// parsed from the stored blob. An incompatible/corrupt blob degrades to metadata + snapshot_error
// rather than a 500 (hero-v2 rule), so old releases stay readable as the contract evolves.
func (s *Server) GetTechCardRelease(ctx context.Context, req *pb_admin.GetTechCardReleaseRequest) (*pb_admin.GetTechCardReleaseResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "release id is required")
	}
	rel, err := s.repo.TechCards().GetTechCardRelease(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card release not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card release", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get tech card release")
	}
	read, _ := s.costingAccess(ctx)
	resp := &pb_admin.GetTechCardReleaseResponse{Release: dto.ConvertTechCardReleaseMetaToPb(rel.TechCardReleaseMeta)}
	if !read {
		stripReleaseMetaCosting(resp.Release)
	}
	var snap pb_common.TechCard
	if err := protojson.Unmarshal([]byte(rel.Snapshot), &snap); err != nil {
		resp.SnapshotError = "stored snapshot is incompatible with the current schema: " + err.Error()
		slog.Default().WarnContext(ctx, "tech card release snapshot won't parse",
			slog.Int("release_id", int(req.Id)), slog.String("err", err.Error()))
	} else {
		// The frozen snapshot embeds the full costing block + BOM prices; redact them too.
		if !read {
			stripTechCardCosting(&snap)
		}
		resp.Snapshot = &snap
	}
	return resp, nil
}

// seedProductCostsFromTechCard best-effort propagates a saved tech card's computed unit
// cost to its linked products' cost_price for margin analytics. It is intentionally
// non-fatal (a failure never blocks the tech card save) and only runs when the costing is
// already in the base currency — the shop has no live FX, so a non-base costing cannot be
// converted. Only products whose PRIMARY card is this one are seeded, and a manually-set
// cost is never overwritten (use SyncProductCostFromTechCard to force). Newly-linked
// products with no primary yet adopt this card as their primary.
func (s *Server) seedProductCostsFromTechCard(ctx context.Context, techCardID int) {
	card, err := s.repo.TechCards().GetTechCardById(ctx, techCardID)
	if err != nil || card == nil {
		return
	}
	linkedProducts := card.LinkedProductIDs()
	if len(linkedProducts) == 0 {
		return
	}
	if err := s.repo.Products().AssignPrimaryTechCardIfUnset(ctx, techCardID, linkedProducts); err != nil {
		slog.Default().ErrorContext(ctx, "can't assign primary tech card to products",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	// ComputeTechCardUnitCost returns the base-currency unit cost when the costing can be folded
	// into the base currency via the FX rates (so a non-base costing seeds too); it returns an
	// invalid value when the cost cannot be expressed in base (missing rate), in which case we
	// skip rather than write a wrong-currency number.
	unit, currency := dto.ComputeTechCardUnitCost(card, s.costingFx(ctx))
	if !unit.Valid {
		slog.Default().InfoContext(ctx, "skip seeding product cost from tech card: no base-currency unit cost (check FX rates)",
			slog.Int("tech_card_id", techCardID))
		return
	}
	if !strings.EqualFold(currency, cache.GetBaseCurrency()) {
		slog.Default().InfoContext(ctx, "skip seeding product cost from tech card: unit cost not in base currency",
			slog.Int("tech_card_id", techCardID), slog.String("currency", currency))
		return
	}
	n, err := s.repo.Products().SeedProductsCostPriceFromTechCard(ctx, techCardID, unit.Decimal)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't seed product cost_price from tech card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	slog.Default().InfoContext(ctx, "seeded product cost_price from tech card",
		slog.Int("tech_card_id", techCardID), slog.Int64("products_updated", n))

	// Snapshot the COGS decomposition (task 15) onto the same products, so the COGS-structure
	// report can attribute a period's cost to materials vs CMT vs …. Best-effort and non-fatal:
	// a NULL breakdown (not base-convertible) clears any stale one, keeping it in sync with
	// cost_price. Uses the same FX as the unit-cost fold above.
	breakdownJSON := sql.NullString{}
	if bd, ok := dto.ComputeTechCardCostBreakdownBase(card, s.costingFx(ctx)); ok {
		if b, err := json.Marshal(bd); err == nil {
			breakdownJSON = sql.NullString{String: string(b), Valid: true}
		} else {
			slog.Default().ErrorContext(ctx, "can't marshal cost breakdown",
				slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		}
	}
	if _, err := s.repo.Products().SeedProductsCostBreakdownFromTechCard(ctx, techCardID, breakdownJSON); err != nil {
		slog.Default().ErrorContext(ctx, "can't seed product cost_breakdown from tech card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	}
}

// costingFx loads the effective manual FX rates and pairs them with the base currency, so the
// tech-card costing can be folded into a base-currency unit cost. A load failure degrades to no
// rates (base rollup only for already-base costings) rather than failing the request.
func (s *Server) costingFx(ctx context.Context) dto.CostingFx {
	rates, err := s.repo.TechCards().GetCostingFxRatesToBase(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load costing fx rates", slog.String("err", err.Error()))
		rates = nil
	}
	return dto.CostingFx{ToBase: rates, Base: cache.GetBaseCurrency()}
}

// GetCostingFxRates returns the CURRENT effective FX rate per currency (the latest valid_from on or
// before today), not the full dated history. The rates are auto-maintained by the fxsync ECB worker,
// so the stored history grows daily and only the effective rate is useful to clients (the admin
// margin view and the OPEX/dev-cost base-currency previews). Manual entry has been removed:
// UpsertCostingFxRates is no longer implemented (the RPC falls back to Unimplemented).
func (s *Server) GetCostingFxRates(ctx context.Context, _ *pb_admin.GetCostingFxRatesRequest) (*pb_admin.GetCostingFxRatesResponse, error) {
	rates, err := s.repo.TechCards().ListCostingFxRates(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list costing fx rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list costing fx rates")
	}
	// ListCostingFxRates is ordered by currency, valid_from DESC; keep the first row per currency
	// effective today, mirroring GetCostingFxRatesToBase's as-of semantics and bounding the payload
	// to one row per currency regardless of how much history has accumulated.
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(rates))
	out := make([]*pb_admin.CostingFxRate, 0, len(rates))
	for _, r := range rates {
		if _, ok := seen[r.Currency]; ok {
			continue
		}
		if r.ValidFrom.After(now) {
			continue // not yet effective — look for an earlier row for this currency
		}
		seen[r.Currency] = struct{}{}
		out = append(out, &pb_admin.CostingFxRate{
			Currency:   r.Currency,
			RateToBase: &pb_decimal.Decimal{Value: r.RateToBase.String()},
			ValidFrom:  timestamppb.New(r.ValidFrom),
		})
	}
	return &pb_admin.GetCostingFxRatesResponse{Rates: out}, nil
}

// DeleteTechCard deletes a tech card by id (nested sections cascade). A readable field-tagged
// FailedPrecondition (apierr) is returned when the card is still referenced elsewhere — a sample with
// material movements, a use as an assembly component in another style, or (residual) any other RESTRICT
// the store guard does not explicitly enumerate — never a raw Internal (P4-flyover M2/S24-regression).
func (s *Server) DeleteTechCard(ctx context.Context, req *pb_admin.DeleteTechCardRequest) (*pb_admin.DeleteTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if err := s.repo.TechCards().DeleteTechCard(ctx, int(req.Id)); err != nil {
		if errors.Is(err, entity.ErrSampleHasMovements) {
			return nil, status.Error(codes.FailedPrecondition, "a sample of this tech card has material movements; delete/return them first")
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.FailedPrecondition(ve)
		}
		slog.Default().ErrorContext(ctx, "can't delete tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete tech card")
	}
	return &pb_admin.DeleteTechCardResponse{}, nil
}

// ListTechCards returns a paged list of tech-card headers with optional filters.
func (s *Server) ListTechCards(ctx context.Context, req *pb_admin.ListTechCardsRequest) (*pb_admin.ListTechCardsResponse, error) {
	stage, err := dto.ConvertPbTechCardStageToEntityString(req.Stage)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid stage filter: %v", err)
	}

	gender := ""
	if req.Gender != pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
		g, err := dto.ConvertPbGenderEnumToEntityGenderEnum(req.Gender)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid gender filter: %v", err)
		}
		gender = string(g)
	}

	purpose := strings.ToLower(strings.TrimSpace(req.Purpose))
	if purpose != "" && !entity.ValidTechCardPurposes[entity.TechCardPurpose(purpose)] {
		return nil, status.Errorf(codes.InvalidArgument, "invalid purpose filter: must be sellable|auxiliary")
	}
	seasonCode, seasonYear, err := dto.ConvertPbSkuSeasonToEntity(req.SkuSeason)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid sku_season filter: %v", err)
	}

	filter := entity.TechCardListFilter{
		Stage:      stage,
		Gender:     gender,
		Brand:      strings.TrimSpace(req.Brand),
		SeasonCode: seasonCode,
		SeasonYear: seasonYear,
		Name:       strings.TrimSpace(req.Name),
		ProductId:  int(req.ProductId),
		Purpose:    purpose,
	}

	cards, total, err := s.repo.TechCards().ListTechCards(ctx, int(req.Limit), int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor), filter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech cards",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list tech cards")
	}

	items := make([]*pb_common.TechCardListItem, 0, len(cards))
	for i := range cards {
		items = append(items, dto.ConvertEntityTechCardToListItemPb(&cards[i]))
	}
	return &pb_admin.ListTechCardsResponse{TechCards: items, Total: int32(total)}, nil
}

// GetStylePipeline returns the development board: per-stage counts + a few light preview cards per
// column, so the whole idea→prod pipeline loads in one call (gap-01).
func (s *Server) GetStylePipeline(ctx context.Context, req *pb_admin.GetStylePipelineRequest) (*pb_admin.GetStylePipelineResponse, error) {
	cols, err := s.repo.TechCards().GetStylePipeline(ctx, int(req.GetCardsPerStage()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get style pipeline", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get style pipeline")
	}
	return dto.ConvertStylePipelineToPb(cols), nil
}
