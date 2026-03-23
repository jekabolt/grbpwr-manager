package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) UpsertProduct(ctx context.Context, req *pb_admin.UpsertProductRequest) (*pb_admin.UpsertProductResponse, error) {

	prdNew, err := dto.ConvertCommonProductToEntity(req.GetProduct())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert proto product to entity product",
			slog.String("err", err.Error()),
		)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("can't convert proto product to entity product: %v", err))
	}

	_, err = v.ValidateStruct(prdNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation add product request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation add product request failed: %v", err))
	}

	id := int(req.Id)
	// new product
	if req.Id == 0 {
		id, err = s.repo.Products().AddProduct(ctx, prdNew)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't create a product",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't create a product: %v", err)
		}
	}

	// update product
	if req.Id != 0 {
		err := s.repo.Products().UpdateProduct(ctx, prdNew, int(req.Id))
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update a product",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't update a product: %v", err)
		}
	}

	err = s.repo.Hero().RefreshHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't refresh hero: %v", err)
	}

	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh dictionary counts",
			slog.String("err", err.Error()),
		)
	} else {
		cache.RefreshDictionary(di)
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Products: []int{id},
		Hero:     true,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate product: %v", err)
	}

	return &pb_admin.UpsertProductResponse{
		Id: int32(id),
	}, nil
}

func (s *Server) DeleteProductByID(ctx context.Context, req *pb_admin.DeleteProductByIDRequest) (*pb_admin.DeleteProductByIDResponse, error) {
	err := s.repo.Products().DeleteProductById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, store.ErrProductInOrders) {
			return nil, status.Errorf(codes.FailedPrecondition, "cannot delete product: it exists in one or more orders")
		}
		slog.Default().ErrorContext(ctx, "can't delete product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product")
	}

	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh dictionary counts",
			slog.String("err", err.Error()),
		)
	} else {
		cache.RefreshDictionary(di)
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Products: []int{int(req.Id)},
		Hero:     true,
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate product")
	}
	return &pb_admin.DeleteProductByIDResponse{}, nil
}

func (s *Server) GetProductByID(ctx context.Context, req *pb_admin.GetProductByIDRequest) (*pb_admin.GetProductByIDResponse, error) {

	pf, err := s.repo.Products().GetProductByIdShowHidden(ctx, int(req.Id))
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

	return &pb_admin.GetProductByIDResponse{
		Product: pbPrd,
	}, nil

}

func (s *Server) GetProductsPaged(ctx context.Context, req *pb_admin.GetProductsPagedRequest) (*pb_admin.GetProductsPagedResponse, error) {

	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

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

	prds, _, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, req.ShowHidden)
	if err != nil {
		if err.Error() == "price sorting requires currency to be specified in filter conditions" {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_common.Product, 0, len(prds))
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

	return &pb_admin.GetProductsPagedResponse{
		Products: prdsPb,
	}, nil
}

func (s *Server) UpdateProductSizeStock(ctx context.Context, req *pb_admin.UpdateProductSizeStockRequest) (*pb_admin.UpdateProductSizeStockResponse, error) {
	productId := int(req.ProductId)
	sizeId := int(req.SizeId)
	quantity := int(req.Quantity)

	// Validate required fields
	if productId == 0 {
		return nil, status.Error(codes.InvalidArgument, "product_id is required")
	}
	if sizeId == 0 {
		return nil, status.Error(codes.InvalidArgument, "size_id is required")
	}

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

	// compute new quantity

	// Get previous quantity to detect stock transition and compute final value
	previousQuantity, _, err := s.repo.Products().GetProductSizeStock(ctx, productId, sizeId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get previous product size quantity",
			slog.String("err", err.Error()),
		)
		// Continue anyway, we'll just skip waitlist notifications
		previousQuantity = decimal.Zero
	}

	var newQuantity int
	if isSetMode {
		// mode="set": quantity IS the final stock value
		newQuantity = quantity
	} else {
		// mode="adjust": compute final value from direction + quantity
		prevQtyInt := int(previousQuantity.IntPart())
		if req.Direction == pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE {
			newQuantity = prevQtyInt + quantity
		} else {
			newQuantity = prevQtyInt - quantity
			if newQuantity < 0 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("adjustment would result in negative stock (%d - %d = %d)", prevQtyInt, quantity, newQuantity))
			}
		}
	}

	err = s.repo.Products().UpdateProductSizeStockWithHistory(ctx, productId, sizeId, newQuantity, reason, comment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update product size stock",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product size stock")
	}

	// Check if stock transitioned from 0 to >0
	newQuantityDecimal := decimal.NewFromInt(int64(newQuantity))
	if previousQuantity.LessThanOrEqual(decimal.Zero) && newQuantityDecimal.GreaterThan(decimal.Zero) {
		// Trigger waitlist notifications asynchronously
		go s.notifyWaitlist(ctx, productId, sizeId)
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Products: []int{productId},
		Hero:     true,
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate product")
	}
	return &pb_admin.UpdateProductSizeStockResponse{}, nil
}

func (s *Server) ListStockChangeHistory(ctx context.Context, req *pb_admin.ListStockChangeHistoryRequest) (*pb_admin.ListStockChangeHistoryResponse, error) {
	var productId, sizeId *int
	if req.ProductId != 0 {
		pid := int(req.ProductId)
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
	if req.ProductId != nil {
		pid := int(*req.ProductId)
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

	// Get product details
	product, err := s.repo.Products().GetProductByIdShowHidden(notifyCtx, productId)
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
