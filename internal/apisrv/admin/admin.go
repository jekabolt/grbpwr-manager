package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slices"
	decimalpb "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo              dependency.Repository
	bucket            dependency.FileStore
	mailer            dependency.Mailer
	stripePayment     dependency.Invoicer
	stripePaymentTest dependency.Invoicer
	re                dependency.RevalidationService
	reservationMgr    dependency.StockReservationManager
}

// New creates a new server with admin handlers.
func New(
	r dependency.Repository,
	b dependency.FileStore,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr dependency.StockReservationManager,
) *Server {
	return &Server{
		repo:              r,
		bucket:            b,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		reservationMgr:    reservationMgr,
	}
}

// CONTENT MANAGER

// UploadContentImage
func (s *Server) UploadContentImage(ctx context.Context, req *pb_admin.UploadContentImageRequest) (*pb_admin.UploadContentImageResponse, error) {
	m, err := s.bucket.UploadContentImage(ctx, req.RawB64Image, s.bucket.GetBaseFolder(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content image",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.UploadContentImageResponse{
		Media: m,
	}, err
}

// UploadContentVideo
func (s *Server) UploadContentVideo(ctx context.Context, req *pb_admin.UploadContentVideoRequest) (*pb_admin.UploadContentVideoResponse, error) {
	media, err := s.bucket.UploadContentVideo(ctx, req.GetRaw(), s.bucket.GetBaseFolder(), bucket.GetMediaName(), req.ContentType)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content video",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.UploadContentVideoResponse{
		Media: media,
	}, nil
}

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	resp := &pb_admin.DeleteFromBucketResponse{}
	err := s.repo.Media().DeleteMediaById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return resp, err
	}

	err = s.repo.Hero().RefreshHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't refresh hero")
	}
	return resp, err
}

// ListObjects
func (s *Server) ListObjectsPaged(ctx context.Context, req *pb_admin.ListObjectsPagedRequest) (*pb_admin.ListObjectsPagedResponse, error) {
	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)
	list, err := s.repo.Media().ListMediaPaged(ctx, int(req.Limit), int(req.Offset), of)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list objects from bucket",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	entities := make([]*pb_common.MediaFull, 0, len(list))
	for _, m := range list {
		entities = append(entities, dto.ConvertEntityToCommonMedia(&m))
	}

	return &pb_admin.ListObjectsPagedResponse{
		List: entities,
	}, err
}

// PRODUCT MANAGER

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
		slog.Default().ErrorContext(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get product by id")
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

	prds, _, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, req.ShowHidden)
	if err != nil {
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
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
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
	newQuantity := int(req.Quantity)

	// Get previous quantity to detect stock transition
	previousQuantity, _, err := s.repo.Products().GetProductSizeStock(ctx, productId, sizeId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get previous product size quantity",
			slog.String("err", err.Error()),
		)
		// Continue anyway, we'll just skip waitlist notifications
		previousQuantity = decimal.Zero
	}

	err = s.repo.Products().UpdateProductSizeStockWithHistory(ctx, productId, sizeId, newQuantity)
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

// PROMO MANAGER

func (s *Server) AddPromo(ctx context.Context, req *pb_admin.AddPromoRequest) (*pb_admin.AddPromoResponse, error) {

	pi, err := dto.ConvertPbCommonPromoToEntity(req.Promo)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb promo to entity promo",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb promo to entity promo")
	}

	err = s.repo.Promo().AddPromo(ctx, pi)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add promo",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add promo")
	}
	return &pb_admin.AddPromoResponse{}, nil
}

// delete_promo.go
func (s *Server) DeletePromoCode(ctx context.Context, req *pb_admin.DeletePromoCodeRequest) (*pb_admin.DeletePromoCodeResponse, error) {
	if req.Code == "" {
		return &pb_admin.DeletePromoCodeResponse{}, fmt.Errorf("code is empty")
	}
	err := s.repo.Promo().DeletePromoCode(ctx, req.Code)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete promo code")
	}
	return &pb_admin.DeletePromoCodeResponse{}, nil
}

// disable_promo.go
func (s *Server) DisablePromoCode(ctx context.Context, req *pb_admin.DisablePromoCodeRequest) (*pb_admin.DisablePromoCodeResponse, error) {
	if req.Code == "" {
		return &pb_admin.DisablePromoCodeResponse{}, fmt.Errorf("code is empty")
	}

	err := s.repo.Promo().DisablePromoCode(ctx, req.Code)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't disable promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't disable promo code")
	}
	return &pb_admin.DisablePromoCodeResponse{}, nil
}

func (s *Server) ListPromos(ctx context.Context, req *pb_admin.ListPromosRequest) (*pb_admin.ListPromosResponse, error) {

	promos, err := s.repo.Promo().ListPromos(ctx, int(req.Limit), int(req.Offset), dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list promos",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list promos")
	}

	pbPromos := make([]*pb_common.PromoCode, 0, len(promos))

	for _, promo := range promos {
		pbPromos = append(pbPromos, dto.ConvertEntityPromoToPb(promo))
	}

	return &pb_admin.ListPromosResponse{
		PromoCodes: pbPromos,
	}, nil
}

func (s *Server) GetDictionary(context.Context, *pb_admin.GetDictionaryRequest) (*pb_admin.GetDictionaryResponse, error) {
	return &pb_admin.GetDictionaryResponse{
		Dictionary: dto.ConvertToCommonDictionary(dto.Dict{
			Categories:                  cache.GetCategories(),
			Measurements:                cache.GetMeasurements(),
			OrderStatuses:               cache.GetOrderStatuses(),
			PaymentMethods:              cache.GetPaymentMethodsFilteredByIsProd(),
			ShipmentCarriers:            cache.GetShipmentCarriers(),
			Sizes:                       cache.GetSizes(),
			Collections:                 cache.GetCollections(),
			Languages:                   cache.GetLanguages(),
			Genders:                     cache.GetGenders(),
			SortFactors:                 cache.GetSortFactors(),
			OrderFactors:                cache.GetOrderFactors(),
			SiteEnabled:                 cache.GetSiteAvailability(),
			MaxOrderItems:               cache.GetMaxOrderItems(),
			BaseCurrency:                cache.GetBaseCurrency(),
			BigMenu:                     cache.GetBigMenu(),
			Announce:                    cache.GetAnnounce(),
			OrderExpirationSeconds:      cache.GetOrderExpirationSeconds(),
			ComplimentaryShippingPrices: cache.GetComplimentaryShippingPrices(),
			IsProd:                      cache.GetPaymentIsProd(),
		}),
		Rates: nil,
	}, nil
}

func (s *Server) GetOrderByUUID(ctx context.Context, req *pb_admin.GetOrderByUUIDRequest) (*pb_admin.GetOrderByUUIDResponse, error) {
	o, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order by uuid",
			slog.String("err", err.Error()),
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "order not found")
		}
		return nil, status.Errorf(codes.Internal, "can't get order by uuid")
	}

	os, ok := cache.GetOrderStatusById(o.Order.OrderStatusId)
	if !ok {
		return nil, status.Errorf(codes.Internal, "can't get order status by id")
	}

	if os.Status.Name == entity.AwaitingPayment {
		pm, ok := cache.GetPaymentMethodById(o.Payment.PaymentMethodID)
		if !ok {
			slog.Default().ErrorContext(ctx, "can't get payment method by id",
				slog.Any("paymentMethodId", o.Payment.PaymentMethodID),
			)
			return nil, status.Errorf(codes.Internal, "can't get payment method by id")
		}

		handler, err := s.getPaymentHandler(ctx, pm.Method.Name)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment handler",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get payment handler")
		}

		payment, err := handler.CheckForTransactions(ctx, o.Order.UUID, o.Payment)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't check for transactions",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't check for transactions")
		}

		o.Payment = *payment
	}

	oPb, err := dto.ConvertEntityOrderFullToPbOrderFull(o)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order full to pb order full",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order full to pb order full")
	}

	return &pb_admin.GetOrderByUUIDResponse{
		Order: oPb,
	}, nil
}

func (s *Server) SetTrackingNumber(ctx context.Context, req *pb_admin.SetTrackingNumberRequest) (*pb_admin.SetTrackingNumberResponse, error) {
	if req.TrackingCode == "" {
		slog.Default().ErrorContext(ctx, "tracking code is empty")
		return nil, status.Errorf(codes.InvalidArgument, "tracking code is empty")
	}

	_, err := s.repo.Order().SetTrackingNumber(ctx, req.OrderUuid, req.TrackingCode)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update tracking number info",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipping info")
	}

	// Get full order details for email
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order full by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order details")
	}

	shipmentDetails := dto.OrderFullToOrderShipment(orderFull)
	err = s.mailer.SendOrderShipped(ctx, s.repo, orderFull.Buyer.Email, shipmentDetails)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't send order shipped email",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't send order shipped email")
	}

	return &pb_admin.SetTrackingNumberResponse{}, nil
}

func (s *Server) GetBusinessMetrics(ctx context.Context, req *pb_admin.GetBusinessMetricsRequest) (*pb_admin.GetBusinessMetricsResponse, error) {
	if strings.TrimSpace(req.Period) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "period is required (e.g. 7d, 30d, 90d)")
	}
	dur, err := parseMetricsPeriod(req.Period)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid period %q: %v", req.Period, err)
	}

	endAt := time.Now()
	if req.EndAt != nil {
		endAt = req.EndAt.AsTime()
	}

	periodTo := endAt
	periodFrom := endAt.Add(-dur)
	period := entity.TimeRange{From: periodFrom, To: periodTo}

	comparePeriod := entity.TimeRange{}
	switch req.CompareMode {
	case pb_admin.CompareMode_COMPARE_MODE_PREVIOUS_PERIOD:
		// Same length, immediately before: [period_from - duration, period_from]
		comparePeriod = entity.TimeRange{
			From: periodFrom.Add(-dur),
			To:   periodFrom,
		}
	case pb_admin.CompareMode_COMPARE_MODE_SAME_PERIOD_LAST_YEAR:
		comparePeriod = entity.TimeRange{
			From: periodFrom.AddDate(-1, 0, 0),
			To:   periodTo.AddDate(-1, 0, 0),
		}
	}

	granularity := inferMetricsGranularity(dur)
	metrics, err := s.repo.Metrics().GetBusinessMetrics(ctx, period, comparePeriod, granularity)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get business metrics",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get business metrics")
	}

	return &pb_admin.GetBusinessMetricsResponse{
		Metrics: dto.ConvertEntityBusinessMetricsToPb(metrics),
	}, nil
}

// parseMetricsPeriod parses "7d", "30d", "90d" or ISO8601 duration (e.g. P7D, P30D) into time.Duration.
func parseMetricsPeriod(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	// Shorthand: Nd
	if m := regexp.MustCompile(`^(\d+)[dD]$`).FindStringSubmatch(s); len(m) == 2 {
		days, _ := strconv.Atoi(m[1])
		if days < 1 || days > 365 {
			return 0, fmt.Errorf("days must be 1-365")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	// ISO8601: P{n}D
	if m := regexp.MustCompile(`^[pP](\d+)[dD]$`).FindStringSubmatch(s); len(m) == 2 {
		days, _ := strconv.Atoi(m[1])
		if days < 1 || days > 365 {
			return 0, fmt.Errorf("days must be 1-365")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("expected format: 7d, 30d, 90d or P7D, P30D")
}

// inferMetricsGranularity derives granularity from period length: <=14d→day, 15-90d→week, >90d→month.
func inferMetricsGranularity(dur time.Duration) entity.MetricsGranularity {
	days := int(dur / (24 * time.Hour))
	if days <= 14 {
		return entity.MetricsGranularityDay
	}
	if days <= 90 {
		return entity.MetricsGranularityWeek
	}
	return entity.MetricsGranularityMonth
}

func (s *Server) ListOrders(ctx context.Context, req *pb_admin.ListOrdersRequest) (*pb_admin.ListOrdersResponse, error) {

	if req.Status < 0 {
		slog.Default().ErrorContext(ctx, "status is invalid")
		return nil, status.Errorf(codes.InvalidArgument, "status is invalid")
	}

	if req.PaymentMethod < 0 {
		slog.Default().ErrorContext(ctx, "payment method is invalid")
		return nil, status.Errorf(codes.InvalidArgument, "payment method is invalid")
	}

	orders, err := s.repo.Order().GetOrdersByStatusAndPaymentTypePaged(ctx,
		req.Email,
		req.OrderUuid,
		int(req.Status),
		cache.GetPaymentMethodIdByPbId(req.PaymentMethod),
		int(req.OrderId),
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get orders by status",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get orders by status")
	}

	ordersPb := make([]*pb_common.Order, 0, len(orders))
	for _, order := range orders {
		o, err := dto.ConvertEntityOrderToPbCommonOrder(order)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
		}
		ordersPb = append(ordersPb, o)
	}
	return &pb_admin.ListOrdersResponse{
		Orders: ordersPb,
	}, nil
}

func (s *Server) RefundOrder(ctx context.Context, req *pb_admin.RefundOrderRequest) (*pb_admin.RefundOrderResponse, error) {
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order for refund",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order")
	}

	orderStatus, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		slog.Default().ErrorContext(ctx, "can't get order status by id",
			slog.String("orderUuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.Internal, "can't get order status by id")
	}

	allowed := orderStatus.Status.Name == entity.RefundInProgress || orderStatus.Status.Name == entity.PendingReturn ||
		orderStatus.Status.Name == entity.Delivered || orderStatus.Status.Name == entity.Confirmed
	if !allowed {
		return nil, status.Errorf(codes.InvalidArgument, "order status must be refund_in_progress, pending_return, delivered or confirmed, got %s", orderStatus.Status.Name)
	}

	// Confirmed orders support only full refund
	if orderStatus.Status.Name == entity.Confirmed && len(req.OrderItemIds) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "confirmed orders support only full refund")
	}

	// Stripe refund only for Confirmed status with Stripe payment
	if orderStatus.Status.Name == entity.Confirmed {
		pm, ok := cache.GetPaymentMethodById(orderFull.Payment.PaymentMethodID)
		if ok && (pm.Method.Name == entity.CARD || pm.Method.Name == entity.CARD_TEST) {
			handler, err := s.getPaymentHandler(ctx, pm.Method.Name)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't get payment handler for refund",
					slog.String("err", err.Error()),
				)
				return nil, status.Errorf(codes.Internal, "can't get payment handler")
			}

			// Calculate refund amount based on order items
			var refundAmount *decimal.Decimal
			if orderStatus.Status.Name == entity.Confirmed {
				// Full refund for Confirmed only
				refundAmount = nil // nil = full refund
			} else {
				// RefundInProgress, PendingReturn, Delivered: full or partial based on orderItemIds
				if len(req.OrderItemIds) == 0 {
					refundAmount = nil
				} else {
					amount := calculateRefundAmount(orderFull.OrderItems, req.OrderItemIds, orderFull.Order.Currency)
					refundAmount = &amount
				}
			}

			if err := handler.Refund(ctx, orderFull.Payment, req.OrderUuid, refundAmount, orderFull.Order.Currency); err != nil {
				slog.Default().ErrorContext(ctx, "stripe refund failed",
					slog.String("err", err.Error()),
					slog.String("orderUuid", req.OrderUuid),
				)
				return nil, status.Errorf(codes.Internal, "stripe refund failed: %v", err)
			}
		}
	}

	err = s.repo.Order().RefundOrder(ctx, req.OrderUuid, req.OrderItemIds, req.Reason)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refund order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't refund order")
	}
	return &pb_admin.RefundOrderResponse{}, nil
}

// calculateRefundAmount calculates the total refund amount based on the specified order item IDs.
// Each occurrence of an ID in orderItemIds represents 1 unit to refund.
// Uses currency-aware rounding (0 for KRW/JPY, 2 for EUR/USD).
func calculateRefundAmount(orderItems []entity.OrderItem, orderItemIds []int32, currency string) decimal.Decimal {
	itemByID := make(map[int]entity.OrderItem)
	for _, item := range orderItems {
		itemByID[item.Id] = item
	}

	var total decimal.Decimal
	for _, id := range orderItemIds {
		item, ok := itemByID[int(id)]
		if ok {
			// Each occurrence = 1 unit, use ProductPriceWithSale for the refund amount
			total = total.Add(item.ProductPriceWithSale)
		}
	}
	return dto.RoundForCurrency(total, currency)
}

func (s *Server) DeliveredOrder(ctx context.Context, req *pb_admin.DeliveredOrderRequest) (*pb_admin.DeliveredOrderResponse, error) {
	err := s.repo.Order().DeliveredOrder(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't mark order as delivered",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't mark order as delivered")
	}
	return &pb_admin.DeliveredOrderResponse{}, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *pb_admin.CancelOrderRequest) (*pb_admin.CancelOrderResponse, error) {
	err := s.repo.Order().CancelOrder(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel order")
	}
	if s.reservationMgr != nil {
		s.reservationMgr.Release(ctx, req.OrderUuid)
	}
	return &pb_admin.CancelOrderResponse{}, nil
}

func (s *Server) AddOrderComment(ctx context.Context, req *pb_admin.AddOrderCommentRequest) (*pb_admin.AddOrderCommentResponse, error) {
	// Validate comment
	if req.Comment == "" {
		return nil, status.Errorf(codes.InvalidArgument, "comment is required")
	}

	err := s.repo.Order().AddOrderComment(ctx, req.OrderUuid, req.Comment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add order comment",
			slog.String("err", err.Error()),
			slog.String("orderUuid", req.OrderUuid),
			slog.String("comment", req.Comment),
		)
		return nil, status.Errorf(codes.Internal, "can't add order comment: %v", err)
	}

	slog.Default().InfoContext(ctx, "order comment added",
		slog.String("orderUuid", req.OrderUuid),
		slog.String("comment", req.Comment),
	)

	return &pb_admin.AddOrderCommentResponse{}, nil
}

// HERO MANAGER

func (s *Server) AddHero(ctx context.Context, req *pb_admin.AddHeroRequest) (*pb_admin.AddHeroResponse, error) {

	heroFull := dto.ConvertCommonHeroFullInsertToEntity(req.Hero)

	err := s.repo.Hero().SetHero(ctx, heroFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add hero")
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Hero: true,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate hero")
	}
	return &pb_admin.AddHeroResponse{}, nil
}

// ARCHIVE MANAGER

func (s *Server) AddArchive(ctx context.Context, req *pb_admin.AddArchiveRequest) (*pb_admin.AddArchiveResponse, error) {
	an, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	archiveId, err := s.repo.Archive().AddArchive(ctx, an)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add archive")
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: archiveId,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

	return &pb_admin.AddArchiveResponse{
		Id: int32(archiveId),
	}, nil
}

func (s *Server) UpdateArchive(ctx context.Context, req *pb_admin.UpdateArchiveRequest) (*pb_admin.UpdateArchiveResponse, error) {

	upd, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	err = s.repo.Archive().UpdateArchive(ctx,
		int(req.Id),
		upd,
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update archive")
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

	return &pb_admin.UpdateArchiveResponse{}, nil
}

func (s *Server) DeleteArchiveById(ctx context.Context, req *pb_admin.DeleteArchiveByIdRequest) (*pb_admin.DeleteArchiveByIdResponse, error) {
	err := s.repo.Archive().DeleteArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete archive by id",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

	return &pb_admin.DeleteArchiveByIdResponse{}, nil
}

func (s *Server) GetArchiveByID(ctx context.Context, req *pb_admin.GetArchiveByIDRequest) (*pb_admin.GetArchiveByIDResponse, error) {

	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get archive by id")
	}

	return &pb_admin.GetArchiveByIDResponse{
		Archive: dto.ConvertArchiveFullEntityToPb(af),
	}, nil
}

// SETTINGS MANAGER

// UpdateSettings updates settings
func (s *Server) UpdateSettings(ctx context.Context, req *pb_admin.UpdateSettingsRequest) (*pb_admin.UpdateSettingsResponse, error) {
	for _, sc := range req.ShipmentCarriers {
		err := s.repo.Settings().SetShipmentCarrierAllowance(ctx, sc.Carrier, sc.Allow)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set shipment carrier allowance",
				slog.String("err", err.Error()),
			)
			continue
		}

		// Use prices map
		prices := make(map[string]decimal.Decimal)
		if sc.Prices != nil && len(sc.Prices) > 0 {
			// Use the prices map
			for currency, pbPrice := range sc.Prices {
				price, err := decimal.NewFromString(pbPrice.Value)
				if err != nil {
					slog.Default().ErrorContext(ctx, "can't convert string to decimal",
						slog.String("currency", currency),
						slog.String("err", err.Error()),
					)
					continue
				}
				prices[currency] = dto.RoundForCurrency(price, currency)
			}
		}

		if len(prices) > 0 {
			err = s.repo.Settings().SetShipmentCarrierPrices(ctx, sc.Carrier, prices)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't set shipment carrier prices",
					slog.String("err", err.Error()),
				)
				continue
			}
		}
	}

	for _, pm := range req.PaymentMethods {
		pme := dto.ConvertPbPaymentMethodToEntity(pm.PaymentMethod)
		err := s.repo.Settings().SetPaymentMethodAllowance(ctx, pme, pm.Allow)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set payment method allowance",
				slog.String("err", err.Error()),
			)
			continue
		}
	}

	err := s.repo.Settings().SetSiteAvailability(ctx, req.SiteAvailable)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set site availability",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err = s.repo.Settings().SetMaxOrderItems(ctx, int(req.MaxOrderItems))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set max order items",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err = s.repo.Settings().SetBigMenu(ctx, req.BigMenu)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set big menu",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	// Convert protobuf announce to entity format
	var announceLink string
	var announceTranslations []entity.AnnounceTranslation
	if req.Announce != nil {
		announceLink = req.Announce.Link
		for _, pbTranslation := range req.Announce.Translations {
			announceTranslations = append(announceTranslations, entity.AnnounceTranslation{
				LanguageId: int(pbTranslation.LanguageId),
				Text:       pbTranslation.Text,
			})
		}
	}

	err = s.repo.Settings().SetAnnounce(ctx, announceLink, announceTranslations)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set announce",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err = s.repo.Settings().SetOrderExpirationSeconds(ctx, int(req.OrderExpirationSeconds))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set order expiration seconds",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err = s.repo.Settings().SetPaymentIsProd(ctx, req.IsProd)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set payment is prod",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	if req.ComplimentaryShippingPrices != nil && len(req.ComplimentaryShippingPrices) > 0 {
		prices := make(map[string]decimal.Decimal)
		for currency, pbPrice := range req.ComplimentaryShippingPrices {
			price, err := decimal.NewFromString(pbPrice.Value)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't convert string to decimal for complimentary shipping",
					slog.String("currency", currency),
					slog.String("err", err.Error()),
				)
				continue
			}
			prices[currency] = dto.RoundForCurrency(price, currency)
		}

		if len(prices) > 0 {
			err = s.repo.Settings().SetComplimentaryShippingPrices(ctx, prices)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't set complimentary shipping prices",
					slog.String("err", err.Error()),
				)
				return nil, err
			}
		}
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Hero: true,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate hero")
	}
	return &pb_admin.UpdateSettingsResponse{}, nil
}

func (s *Server) AddShipmentCarrier(ctx context.Context, req *pb_admin.AddShipmentCarrierRequest) (*pb_admin.AddShipmentCarrierResponse, error) {
	if err := validateShipmentCarrierRequest(req.Carrier, req.TrackingUrl, req.Prices, req.AllowedRegions); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	carrier := dto.ConvertShipmentCarrierRequestToEntity(req.Carrier, req.TrackingUrl, req.Description, req.ExpectedDeliveryTime, req.Allowed)
	prices := parseShipmentCarrierPrices(req.Prices)
	allowedRegions := dto.ConvertPbShippingRegionsToEntity(req.AllowedRegions)

	id, err := s.repo.Settings().AddShipmentCarrier(ctx, &carrier, prices, allowedRegions)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add shipment carrier: %v", err)
	}
	return &pb_admin.AddShipmentCarrierResponse{Id: int32(id)}, nil
}

func (s *Server) UpdateShipmentCarrier(ctx context.Context, req *pb_admin.UpdateShipmentCarrierRequest) (*pb_admin.UpdateShipmentCarrierResponse, error) {
	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "id must be positive")
	}
	if err := validateShipmentCarrierRequest(req.Carrier, req.TrackingUrl, req.Prices, req.AllowedRegions); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	carrier := dto.ConvertShipmentCarrierRequestToEntity(req.Carrier, req.TrackingUrl, req.Description, req.ExpectedDeliveryTime, req.Allowed)
	prices := parseShipmentCarrierPrices(req.Prices)
	allowedRegions := dto.ConvertPbShippingRegionsToEntity(req.AllowedRegions)

	err := s.repo.Settings().UpdateShipmentCarrier(ctx, int(req.Id), &carrier, prices, allowedRegions)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipment carrier: %v", err)
	}
	return &pb_admin.UpdateShipmentCarrierResponse{}, nil
}

func (s *Server) DeleteShipmentCarrier(ctx context.Context, req *pb_admin.DeleteShipmentCarrierRequest) (*pb_admin.DeleteShipmentCarrierResponse, error) {
	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "id must be positive")
	}
	err := s.repo.Settings().DeleteShipmentCarrier(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete shipment carrier: %v", err)
	}
	return &pb_admin.DeleteShipmentCarrierResponse{}, nil
}

var requiredCurrencies = []string{"EUR", "USD", "GBP", "JPY", "CNY", "KRW"}

func validateShipmentCarrierRequest(carrier, trackingURL string, prices map[string]*decimalpb.Decimal, allowedRegions []pb_common.ShippingRegion) error {
	if strings.TrimSpace(carrier) == "" {
		return fmt.Errorf("carrier name is required")
	}
	if trackingURL == "" {
		return fmt.Errorf("tracking_url is required")
	}
	if !strings.Contains(trackingURL, "%s") {
		return fmt.Errorf("tracking_url must contain %%s placeholder for tracking code")
	}
	provided := make(map[string]bool)
	for currency := range prices {
		provided[strings.ToUpper(currency)] = true
	}
	for _, c := range requiredCurrencies {
		if !provided[c] {
			return fmt.Errorf("missing required currency: %s", c)
		}
	}
	// Validate each price meets currency minimum (e.g. KRW >= 100)
	for currency, pbPrice := range prices {
		if pbPrice == nil {
			continue
		}
		p, err := decimal.NewFromString(pbPrice.GetValue())
		if err != nil {
			return fmt.Errorf("invalid price for %s: %w", currency, err)
		}
		currencyUpper := strings.ToUpper(currency)
		rounded := dto.RoundForCurrency(p, currencyUpper)
		if err := dto.ValidatePriceMeetsMinimum(rounded, currencyUpper); err != nil {
			return err
		}
	}
	for _, r := range allowedRegions {
		if r == pb_common.ShippingRegion_SHIPPING_REGION_UNKNOWN {
			return fmt.Errorf("invalid region: SHIPPING_REGION_UNKNOWN")
		}
	}
	return nil
}

func parseShipmentCarrierPrices(prices map[string]*decimalpb.Decimal) map[string]decimal.Decimal {
	if prices == nil {
		return nil
	}
	out := make(map[string]decimal.Decimal)
	for currency, pbPrice := range prices {
		if pbPrice == nil {
			continue
		}
		p, err := decimal.NewFromString(pbPrice.GetValue())
		if err != nil {
			continue
		}
		currencyUpper := strings.ToUpper(currency)
		out[currencyUpper] = dto.RoundForCurrency(p, currencyUpper)
	}
	return out
}

// SUPPORT MANAGER

func (s *Server) GetSupportTicketsPaged(ctx context.Context, req *pb_admin.GetSupportTicketsPagedRequest) (*pb_admin.GetSupportTicketsPagedResponse, error) {
	filters := dto.ConvertPbSupportTicketFiltersToEntity(
		req.Status,
		req.GetEmail(),
		req.GetOrderReference(),
		req.GetTopic(),
		req.GetCategory(),
		req.Priority,
		req.GetDateFrom(),
		req.GetDateTo(),
	)

	tickets, totalCount, err := s.repo.Support().GetSupportTicketsPaged(
		ctx,
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
		filters,
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get support tickets paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get support tickets paged")
	}

	return &pb_admin.GetSupportTicketsPagedResponse{
		Tickets:    dto.ConvertEntitySupportTicketsToPb(tickets),
		TotalCount: int32(totalCount),
	}, nil
}

func (s *Server) GetSupportTicketById(ctx context.Context, req *pb_admin.GetSupportTicketByIdRequest) (*pb_admin.GetSupportTicketByIdResponse, error) {
	ticket, err := s.repo.Support().GetSupportTicketById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get support ticket by id",
			slog.String("err", err.Error()),
			slog.Int("id", int(req.Id)),
		)
		return nil, status.Errorf(codes.NotFound, "support ticket not found")
	}

	return &pb_admin.GetSupportTicketByIdResponse{
		Ticket: dto.ConvertEntitySupportTicketToPb(ticket),
	}, nil
}

func (s *Server) GetSupportTicketByCaseNumber(ctx context.Context, req *pb_admin.GetSupportTicketByCaseNumberRequest) (*pb_admin.GetSupportTicketByCaseNumberResponse, error) {
	ticket, err := s.repo.Support().GetSupportTicketByCaseNumber(ctx, req.CaseNumber)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get support ticket by case number",
			slog.String("err", err.Error()),
			slog.String("case_number", req.CaseNumber),
		)
		return nil, status.Errorf(codes.NotFound, "support ticket not found")
	}

	return &pb_admin.GetSupportTicketByCaseNumberResponse{
		Ticket: dto.ConvertEntitySupportTicketToPb(ticket),
	}, nil
}

func (s *Server) UpdateSupportTicketStatus(ctx context.Context, req *pb_admin.UpdateSupportTicketStatusRequest) (*pb_admin.UpdateSupportTicketStatusResponse, error) {
	entityStatus := dto.ConvertPbSupportTicketStatusToEntity(req.Status)
	
	err := s.repo.Support().UpdateStatus(ctx, int(req.Id), entityStatus)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update support ticket status",
			slog.String("err", err.Error()),
			slog.Int("id", int(req.Id)),
		)
		return nil, status.Errorf(codes.Internal, "can't update support ticket status")
	}

	if req.InternalNotes != nil && *req.InternalNotes != "" {
		err = s.repo.Support().UpdateInternalNotes(ctx, int(req.Id), *req.InternalNotes)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update internal notes",
				slog.String("err", err.Error()),
				slog.Int("id", int(req.Id)),
			)
		}
	}

	return &pb_admin.UpdateSupportTicketStatusResponse{}, nil
}

func (s *Server) UpdateSupportTicket(ctx context.Context, req *pb_admin.UpdateSupportTicketRequest) (*pb_admin.UpdateSupportTicketResponse, error) {
	if req.Priority != nil {
		entityPriority := dto.ConvertPbSupportTicketPriorityToEntity(*req.Priority)
		err := s.repo.Support().UpdatePriority(ctx, int(req.Id), entityPriority)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update priority",
				slog.String("err", err.Error()),
				slog.Int("id", int(req.Id)),
			)
			return nil, status.Errorf(codes.Internal, "can't update priority")
		}
	}

	if req.Category != nil {
		err := s.repo.Support().UpdateCategory(ctx, int(req.Id), *req.Category)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update category",
				slog.String("err", err.Error()),
				slog.Int("id", int(req.Id)),
			)
			return nil, status.Errorf(codes.Internal, "can't update category")
		}
	}

	if req.InternalNotes != nil {
		err := s.repo.Support().UpdateInternalNotes(ctx, int(req.Id), *req.InternalNotes)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update internal notes",
				slog.String("err", err.Error()),
				slog.Int("id", int(req.Id)),
			)
			return nil, status.Errorf(codes.Internal, "can't update internal notes")
		}
	}

	return &pb_admin.UpdateSupportTicketResponse{}, nil
}

func (s *Server) getPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	switch pm {
	case entity.CARD:
		return s.stripePayment, nil
	case entity.CARD_TEST:
		return s.stripePaymentTest, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}
