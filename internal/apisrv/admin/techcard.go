package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// techCardFKMsg is returned when a tech card references a missing category, base
// model, base sample size, size, product or media row.
const techCardFKMsg = "tech card references a non-existent category, model, size, product, media or fitting"

// techCardDupMsg is returned when style_number collides within the same season.
const techCardDupMsg = "a tech card with this style_number and season already exists"

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
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.validateCategoryLeaf(ctx, tc.CategoryId); err != nil {
		return nil, err
	}

	id, err := s.repo.TechCards().AddTechCard(ctx, tc)
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardDupMsg)
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
	return &pb_admin.CreateTechCardResponse{Id: int32(id)}, nil
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
	return &pb_admin.GetTechCardResponse{TechCard: dto.ConvertEntityTechCardToPb(tc, s.costingFx(ctx))}, nil
}

// UpdateTechCard updates a tech card, replacing its nested sections.
func (s *Server) UpdateTechCard(ctx context.Context, req *pb_admin.UpdateTechCardRequest) (*pb_admin.UpdateTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.validateCategoryLeaf(ctx, tc.CategoryId); err != nil {
		return nil, err
	}
	if err := s.repo.TechCards().UpdateTechCard(ctx, int(req.Id), tc, int(req.ExpectedLockVersion)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		if errors.Is(err, entity.ErrTechCardConflict) {
			return nil, status.Error(codes.Aborted, "tech card was modified concurrently; reload and retry")
		}
		if errors.Is(err, entity.ErrTechCardReleased) {
			return nil, status.Error(codes.FailedPrecondition, "tech card is released and frozen; re-open to draft to edit")
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardDupMsg)
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
	return &pb_admin.UpdateTechCardResponse{}, nil
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
	if err != nil || card == nil || len(card.ProductIds) == 0 {
		return
	}
	if err := s.repo.Products().AssignPrimaryTechCardIfUnset(ctx, techCardID, card.ProductIds); err != nil {
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

// GetCostingFxRates returns every stored manual FX rate for the admin management surface.
func (s *Server) GetCostingFxRates(ctx context.Context, _ *pb_admin.GetCostingFxRatesRequest) (*pb_admin.GetCostingFxRatesResponse, error) {
	rates, err := s.repo.TechCards().ListCostingFxRates(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list costing fx rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list costing fx rates")
	}
	out := make([]*pb_admin.CostingFxRate, 0, len(rates))
	for _, r := range rates {
		out = append(out, &pb_admin.CostingFxRate{
			Currency:   r.Currency,
			RateToBase: &pb_decimal.Decimal{Value: r.RateToBase.String()},
			ValidFrom:  timestamppb.New(r.ValidFrom),
		})
	}
	return &pb_admin.GetCostingFxRatesResponse{Rates: out}, nil
}

// UpsertCostingFxRates inserts or updates manual FX rates (by currency + effective date).
func (s *Server) UpsertCostingFxRates(ctx context.Context, req *pb_admin.UpsertCostingFxRatesRequest) (*pb_admin.UpsertCostingFxRatesResponse, error) {
	ents := make([]entity.CostingFxRate, 0, len(req.Rates))
	for _, r := range req.Rates {
		ccy := strings.ToUpper(strings.TrimSpace(r.Currency))
		if len(ccy) != 3 {
			return nil, status.Errorf(codes.InvalidArgument, "currency must be a 3-letter ISO code, got %q", r.Currency)
		}
		if r.RateToBase == nil {
			return nil, status.Errorf(codes.InvalidArgument, "rate_to_base is required for %s", ccy)
		}
		rate, err := decimal.NewFromString(r.RateToBase.Value)
		if err != nil || !rate.IsPositive() {
			return nil, status.Errorf(codes.InvalidArgument, "rate_to_base must be a positive number for %s", ccy)
		}
		validFrom := time.Now().UTC().Truncate(24 * time.Hour)
		if r.ValidFrom != nil {
			validFrom = r.ValidFrom.AsTime().UTC().Truncate(24 * time.Hour)
		}
		ents = append(ents, entity.CostingFxRate{Currency: ccy, RateToBase: rate, ValidFrom: validFrom})
	}
	if err := s.repo.TechCards().UpsertCostingFxRates(ctx, ents); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert costing fx rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert costing fx rates")
	}
	return &pb_admin.UpsertCostingFxRatesResponse{}, nil
}

// DeleteTechCard deletes a tech card by id (nested sections cascade).
func (s *Server) DeleteTechCard(ctx context.Context, req *pb_admin.DeleteTechCardRequest) (*pb_admin.DeleteTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if err := s.repo.TechCards().DeleteTechCard(ctx, int(req.Id)); err != nil {
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

	filter := entity.TechCardListFilter{
		Stage:     stage,
		Gender:    gender,
		Brand:     strings.TrimSpace(req.Brand),
		Season:    strings.TrimSpace(req.Season),
		Name:      strings.TrimSpace(req.Name),
		ProductId: int(req.ProductId),
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
