package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	return &pb_admin.GetTechCardResponse{TechCard: dto.ConvertEntityTechCardToPb(tc)}, nil
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
// converted. Last write wins when a product is linked to several tech cards.
func (s *Server) seedProductCostsFromTechCard(ctx context.Context, techCardID int) {
	card, err := s.repo.TechCards().GetTechCardById(ctx, techCardID)
	if err != nil || card == nil || len(card.ProductIds) == 0 {
		return
	}
	unit, currency := dto.ComputeTechCardUnitCost(card)
	if !unit.Valid {
		return
	}
	if !strings.EqualFold(currency, cache.GetBaseCurrency()) {
		slog.Default().InfoContext(ctx, "skip seeding product cost from tech card: costing not in base currency",
			slog.Int("tech_card_id", techCardID), slog.String("currency", currency))
		return
	}
	if err := s.repo.Products().SetProductsCostPrice(ctx, card.ProductIds, unit.Decimal); err != nil {
		slog.Default().ErrorContext(ctx, "can't seed product cost_price from tech card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	}
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
