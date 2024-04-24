package admin

import (
	"context"
	"fmt"
	"time"

	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slices"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo            dependency.Repository
	bucket          dependency.FileStore
	mailer          dependency.Mailer
	rates           dependency.RatesService
	usdtTron        dependency.CryptoInvoice
	usdtTronTestnet dependency.CryptoInvoice
}

// New creates a new server with admin handlers.
func New(
	r dependency.Repository,
	b dependency.FileStore,
	m dependency.Mailer,
	rates dependency.RatesService,
	usdtTron dependency.CryptoInvoice,
	usdtTronTestnet dependency.CryptoInvoice,
) *Server {
	return &Server{
		repo:            r,
		bucket:          b,
		mailer:          m,
		rates:           rates,
		usdtTron:        usdtTron,
		usdtTronTestnet: usdtTronTestnet,
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

func (s *Server) UploadContentMediaLink(ctx context.Context, req *pb_admin.UploadContentMediaLinkRequest) (*pb_admin.UploadContentMediaLinkResponse, error) {
	media, err := s.bucket.UploadContentImageFromUrl(ctx, req.Url, s.bucket.GetBaseFolder(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content media link",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.UploadContentMediaLinkResponse{
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

	entities := make([]*pb_common.Media, 0, len(list))
	for _, m := range list {
		entities = append(entities, dto.ConvertEntityToCommonMedia(m))
	}

	return &pb_admin.ListObjectsPagedResponse{
		List: entities,
	}, err
}

// PRODUCT MANAGER

func (s *Server) AddProduct(ctx context.Context, req *pb_admin.AddProductRequest) (*pb_admin.AddProductResponse, error) {

	prdNew, err := dto.ConvertCommonProductToEntity(req.GetProduct())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert proto product to entity product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert proto product to entity product: %v", err)
	}

	_, err = v.ValidateStruct(prdNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation add product request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation  add product request failed: %v", err).Error())
	}

	prd, err := s.repo.Products().AddProduct(ctx, prdNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create a product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create a product")
	}

	pbPrd, err := dto.ConvertToPbProductFull(prd)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity product to proto product: %v", err)
	}

	return &pb_admin.AddProductResponse{
		Product: pbPrd,
	}, nil
}

func (s *Server) UpdateProductMeasurements(ctx context.Context, req *pb_admin.UpdateProductMeasurementsRequest) (*pb_admin.UpdateProductMeasurementsResponse, error) {

	mUpd, err := dto.ConvertPbMeasurementsUpdateToEntity(req.Measurements)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert proto measurements to entity measurements",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert proto measurements to entity measurements")
	}

	err = s.repo.Products().UpdateProductMeasurements(ctx, int(req.ProductId), mUpd)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add product measurement",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product measurement")
	}
	return &pb_admin.UpdateProductMeasurementsResponse{}, nil
}

func (s *Server) AddProductMedia(ctx context.Context, req *pb_admin.AddProductMediaRequest) (*pb_admin.AddProductMediaResponse, error) {
	err := s.repo.Products().AddProductMedia(ctx, int(req.ProductId), req.FullSize, req.Thumbnail, req.Compressed)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add product media",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product media")
	}
	return &pb_admin.AddProductMediaResponse{}, nil
}

func (s *Server) AddProductTag(ctx context.Context, req *pb_admin.AddProductTagRequest) (*pb_admin.AddProductTagResponse, error) {
	err := s.repo.Products().AddProductTag(ctx, int(req.ProductId), req.Tag)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add product tag",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product tag")
	}
	return &pb_admin.AddProductTagResponse{}, nil
}

func (s *Server) DeleteProductByID(ctx context.Context, req *pb_admin.DeleteProductByIDRequest) (*pb_admin.DeleteProductByIDResponse, error) {
	err := s.repo.Products().DeleteProductById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product")
	}
	return &pb_admin.DeleteProductByIDResponse{}, nil
}

func (s *Server) DeleteProductMedia(ctx context.Context, req *pb_admin.DeleteProductMediaRequest) (*pb_admin.DeleteProductMediaResponse, error) {
	err := s.repo.Products().DeleteProductMedia(ctx, int(req.ProductMediaId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete product media",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product media")
	}
	return &pb_admin.DeleteProductMediaResponse{}, nil
}

func (s *Server) DeleteProductTag(ctx context.Context, req *pb_admin.DeleteProductTagRequest) (*pb_admin.DeleteProductTagResponse, error) {
	err := s.repo.Products().DeleteProductTag(ctx, int(req.ProductId), req.Tag)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete product tag",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product tag")
	}
	return &pb_admin.DeleteProductTagResponse{}, nil
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

	// Validate parameters
	if req.Limit <= 0 || req.Offset < 0 {
		req.Limit, req.Offset = 15, 0
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

	prds, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, req.ShowHidden)
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

func (s *Server) ReduceStockForProductSizes(ctx context.Context, req *pb_admin.ReduceStockForProductSizesRequest) (*pb_admin.ReduceStockForProductSizesResponse, error) {
	if len(req.Items) == 0 {
		return &pb_admin.ReduceStockForProductSizesResponse{}, nil
	}

	items := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, dto.ConvertPbOrderItemToEntity(item))
	}

	err := s.repo.Products().ReduceStockForProductSizes(ctx, items)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't reduce stock for product sizes",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't reduce stock for product sizes")
	}
	return &pb_admin.ReduceStockForProductSizesResponse{}, nil

}

func (s *Server) RestoreStockForProductSizes(ctx context.Context, req *pb_admin.RestoreStockForProductSizesRequest) (*pb_admin.RestoreStockForProductSizesResponse, error) {
	if len(req.Items) == 0 {
		return &pb_admin.RestoreStockForProductSizesResponse{}, nil
	}
	items := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, dto.ConvertPbOrderItemToEntity(item))
	}

	err := s.repo.Products().RestoreStockForProductSizes(ctx, items)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't restore stock for product sizes",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't restore stock for product sizes")
	}
	return &pb_admin.RestoreStockForProductSizesResponse{}, nil
}

func (s *Server) UpdateProduct(ctx context.Context, req *pb_admin.UpdateProductRequest) (*pb_admin.UpdateProductResponse, error) {
	prdInsert, err := dto.ConvertPbProductInsertToEntity(req.Product)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert proto product to entity product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert proto product to entity product")
	}

	_, err = v.ValidateStruct(prdInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation update product request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation update product request failed: %v", err).Error())
	}

	if prdInsert.Price.LessThanOrEqual(decimal.Zero) {
		return nil, status.Errorf(codes.InvalidArgument, "price must be greater than 0")
	}

	if prdInsert.SalePercentage.Valid &&
		(prdInsert.SalePercentage.Decimal.GreaterThan(decimal.NewFromInt(100)) ||
			prdInsert.SalePercentage.Decimal.LessThan(decimal.NewFromInt(0))) {
		return nil, status.Errorf(codes.InvalidArgument, "sale must be between 0 and 100")
	}

	if prdInsert.CategoryID == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "category id is invalid")
	}

	if !v.IsHexcolor(prdInsert.ColorHex) {
		return nil, status.Errorf(codes.InvalidArgument, "color hex is not valid")
	}

	if !entity.IsValidTargetGender(prdInsert.TargetGender) {
		return nil, status.Errorf(codes.InvalidArgument, "gender not valid")
	}

	err = s.repo.Products().UpdateProduct(ctx, prdInsert, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product")
	}
	return &pb_admin.UpdateProductResponse{}, nil
}

func (s *Server) UpdateProductSizeStock(ctx context.Context, req *pb_admin.UpdateProductSizeStockRequest) (*pb_admin.UpdateProductSizeStockResponse, error) {
	err := s.repo.Products().UpdateProductSizeStock(ctx, int(req.ProductId), int(req.SizeId), int(req.Quantity))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update product size stock",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product size stock")
	}
	return &pb_admin.UpdateProductSizeStockResponse{}, nil
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

	promos, err := s.repo.Promo().ListPromos(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list promos",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list promos")
	}

	pbPromos := make([]*pb_common.PromoCode, 0, len(promos))

	for _, promo := range promos {
		pbPromos = append(pbPromos, dto.ConvertEntityPromoToPb(&promo))
	}

	return &pb_admin.ListPromosResponse{
		PromoCodes: pbPromos,
	}, nil
}

func (s *Server) GetDictionary(context.Context, *pb_admin.GetDictionaryRequest) (*pb_admin.GetDictionaryResponse, error) {
	return &pb_admin.GetDictionaryResponse{
		Dictionary: dto.ConvertToCommonDictionary(s.repo.Cache().GetDict()),
		Rates:      dto.CurrencyRateToPb(s.rates.GetRates()),
	}, nil
}

func (s *Server) CreateOrder(ctx context.Context, req *pb_admin.CreateOrderRequest) (*pb_admin.CreateOrderResponse, error) {
	orderNew, receivePromo := dto.ConvertCommonOrderNewToEntity(req.Order)

	_, err := v.ValidateStruct(orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation order create request failed: %v", err).Error())
	}

	order, err := s.repo.Order().CreateOrder(ctx, orderNew, receivePromo)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	o, err := dto.ConvertEntityOrderToPbCommonOrder(order)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}

	return &pb_admin.CreateOrderResponse{
		Order: o,
	}, nil
}

func (s *Server) ValidateOrderItemsInsert(ctx context.Context, req *pb_admin.ValidateOrderItemsInsertRequest) (*pb_admin.ValidateOrderItemsInsertResponse, error) {
	itemsToInsert := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		oii, err := dto.ConvertPbOrderItemInsertToEntity(i)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert pb order item to entity order item",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert pb order item to entity order item")
		}
		itemsToInsert = append(itemsToInsert, *oii)
	}

	oii, subtotal, err := s.repo.Order().ValidateOrderItemsInsert(ctx, itemsToInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't validate order items insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order items insert")
	}

	pbOii := make([]*pb_common.OrderItemInsert, 0, len(oii))
	for _, i := range oii {
		pbOii = append(pbOii, dto.ConvertEntityOrderItemInsertToPb(&i))
	}

	return &pb_admin.ValidateOrderItemsInsertResponse{
		Items:    pbOii,
		Subtotal: &pb_decimal.Decimal{Value: subtotal.String()},
	}, nil

}
func (s *Server) ValidateOrderByUUID(ctx context.Context, req *pb_admin.ValidateOrderByUUIDRequest) (*pb_admin.ValidateOrderByUUIDResponse, error) {
	orderFull, err := s.repo.Order().ValidateOrderByUUID(ctx, req.Uuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't validate order by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order by uuid")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_admin.ValidateOrderByUUIDResponse{
		Order: of,
	}, nil
}

func (s *Server) ApplyPromoCode(ctx context.Context, req *pb_admin.ApplyPromoCodeRequest) (*pb_admin.ApplyPromoCodeResponse, error) {
	orderFull, err := s.repo.Order().ApplyPromoCode(ctx, int(req.OrderId), req.PromoCode)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't apply promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't apply promo code")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_admin.ApplyPromoCodeResponse{
		Order: of,
	}, nil
}

func (s *Server) UpdateOrderItems(ctx context.Context, req *pb_admin.UpdateOrderItemsRequest) (*pb_admin.UpdateOrderItemsResponse, error) {
	itemsToInsert := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		oii, err := dto.ConvertPbOrderItemInsertToEntity(i)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert pb order item to entity order item",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert pb order item to entity order item")
		}
		itemsToInsert = append(itemsToInsert, *oii)
	}

	orderFull, err := s.repo.Order().UpdateOrderItems(ctx, int(req.OrderId), itemsToInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update order items",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update order items")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_admin.UpdateOrderItemsResponse{
		Order: of,
	}, nil
}

func (s *Server) UpdateOrderShippingCarrier(ctx context.Context, req *pb_admin.UpdateOrderShippingCarrierRequest) (*pb_admin.UpdateOrderShippingCarrierResponse, error) {
	orderFull, err := s.repo.Order().UpdateOrderShippingCarrier(ctx, int(req.OrderId), int(req.ShippingCarrierId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update order shipping carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update order shipping carrier")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_admin.UpdateOrderShippingCarrierResponse{
		Order: of,
	}, nil
}

func (s *Server) GetOrderInvoice(ctx context.Context, req *pb_admin.GetOrderInvoiceRequest) (*pb_admin.GetOrderInvoiceResponse, error) {

	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)

	switch pm {
	case entity.USDT_TRON:
		pi, expire, err := s.usdtTron.GetOrderInvoice(ctx, int(req.OrderId))
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order invoice",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get order invoice")
		}

		pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
		}

		return &pb_admin.GetOrderInvoiceResponse{
			Payment:   pbPi,
			ExpiredAt: timestamppb.New(expire),
		}, nil
	case entity.USDT_TRON_TEST:
		pi, expire, err := s.usdtTron.GetOrderInvoice(ctx, int(req.OrderId))
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order invoice",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get order invoice")
		}

		pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
		}

		return &pb_admin.GetOrderInvoiceResponse{
			Payment:   pbPi,
			ExpiredAt: timestamppb.New(expire),
		}, nil
	default:
		slog.Default().ErrorContext(ctx, "payment method unimplemented")
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}

func (s *Server) UpdateShippingInfo(ctx context.Context, req *pb_admin.UpdateShippingInfoRequest) (*pb_admin.UpdateShippingInfoResponse, error) {
	sh := dto.ConvertPbShipmentToEntityShipment(req.ShippingInfo)
	if sh.TrackingCode.String == "" {
		slog.Default().ErrorContext(ctx, "tracking code is empty")
		return nil, status.Errorf(codes.InvalidArgument, "tracking code is empty")
	}

	_, ok := s.repo.Cache().GetShipmentCarrierById(int(sh.CarrierID))
	if !ok {
		slog.Default().ErrorContext(ctx, "can't find shipment carrier by id")
		return nil, status.Errorf(codes.InvalidArgument, "can't find shipment carrier by id")
	}

	err := s.repo.Order().UpdateShippingInfo(ctx, int(req.OrderId), sh)

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update shipping info",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipping info")
	}
	return &pb_admin.UpdateShippingInfoResponse{}, nil
}

func (s *Server) SetTrackingNumber(ctx context.Context, req *pb_admin.SetTrackingNumberRequest) (*pb_admin.SetTrackingNumberResponse, error) {
	if req.TrackingCode == "" {
		slog.Default().ErrorContext(ctx, "tracking code is empty")
		return nil, status.Errorf(codes.InvalidArgument, "tracking code is empty")
	}

	obs, err := s.repo.Order().SetTrackingNumber(ctx, int(req.OrderId), req.TrackingCode)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update tracking number info",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipping info")
	}

	sc, ok := s.repo.Cache().GetShipmentCarrierById(int(obs.Shipment.CarrierID))
	if !ok {
		slog.Default().ErrorContext(ctx, "can't find shipment carrier by id")
		return nil, status.Errorf(codes.InvalidArgument, "can't find shipment carrier by id")
	}

	trackingUrlFull := fmt.Sprintf(sc.ShipmentCarrierInsert.TrackingURL, req.TrackingCode)

	// TODO: in tx
	s.mailer.SendOrderShipped(ctx, s.repo, obs.Buyer.Email, &dto.OrderShipment{
		Name:           fmt.Sprintf("%s %s", obs.Buyer.FirstName, obs.Buyer.LastName),
		OrderUUID:      obs.Order.UUID,
		ShippingDate:   time.Now().Format("2006-01-02"),
		TrackingNumber: req.TrackingCode,
		TrackingURL:    trackingUrlFull,
	})

	return &pb_admin.SetTrackingNumberResponse{}, nil
}

func (s *Server) GetOrderById(ctx context.Context, req *pb_admin.GetOrderByIdRequest) (*pb_admin.GetOrderByIdResponse, error) {
	order, err := s.repo.Order().GetOrderById(ctx, int(req.OrderId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order by id")
	}
	o, err := dto.ConvertEntityOrderFullToPbOrderFull(order)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order full to pb order full",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order full to pb order full")
	}

	return &pb_admin.GetOrderByIdResponse{
		Order: o,
	}, nil
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
		int(req.Status),
		int(req.PaymentMethod),
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
		o, err := dto.ConvertEntityOrderToPbCommonOrder(&order)
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
	err := s.repo.Order().RefundOrder(ctx, int(req.OrderId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refund order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't refund order")
	}
	return &pb_admin.RefundOrderResponse{}, nil
}

func (s *Server) DeliveredOrder(ctx context.Context, req *pb_admin.DeliveredOrderRequest) (*pb_admin.DeliveredOrderResponse, error) {
	err := s.repo.Order().DeliveredOrder(ctx, int(req.OrderId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't mark order as delivered",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't mark order as delivered")
	}
	return &pb_admin.DeliveredOrderResponse{}, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *pb_admin.CancelOrderRequest) (*pb_admin.CancelOrderResponse, error) {
	err := s.repo.Order().CancelOrder(ctx, int(req.OrderId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel order")
	}
	return &pb_admin.CancelOrderResponse{}, nil
}

// HERO MANAGER

func (s *Server) AddHero(ctx context.Context, req *pb_admin.AddHeroRequest) (*pb_admin.AddHeroResponse, error) {
	main := dto.ConvertCommonHeroInsertToEntity(req.Main)

	ads := make([]entity.HeroInsert, 0, len(req.Ads))
	for _, ad := range req.Ads {
		ads = append(ads, dto.ConvertCommonHeroInsertToEntity(ad))
	}

	prdIds := make([]int, 0, len(req.ProductIds))
	for _, id := range req.ProductIds {
		prdIds = append(prdIds, int(id))
	}

	err := s.repo.Hero().SetHero(ctx, main, ads, prdIds)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add hero")
	}
	return &pb_admin.AddHeroResponse{}, nil
}

func (s *Server) GetHero(ctx context.Context, req *pb_admin.GetHeroRequest) (*pb_admin.GetHeroResponse, error) {
	hero, err := s.repo.Hero().GetHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get hero")
	}
	h, err := dto.ConvertEntityHeroFullToCommon(hero)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity hero to pb hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity hero to pb hero")
	}
	return &pb_admin.GetHeroResponse{
		Hero: h,
	}, nil
}

// ARCHIVE MANAGER

func (s *Server) AddArchive(ctx context.Context, req *pb_admin.AddArchiveRequest) (*pb_admin.AddArchiveResponse, error) {
	an := dto.ConvertPbArchiveNewToEntity(req.ArchiveNew)

	archiveId, err := s.repo.Archive().AddArchive(ctx, an)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add archive")
	}

	return &pb_admin.AddArchiveResponse{
		Id: int32(archiveId),
	}, nil
}

func (s *Server) UpdateArchive(ctx context.Context, req *pb_admin.UpdateArchiveRequest) (*pb_admin.UpdateArchiveResponse, error) {
	err := s.repo.Archive().UpdateArchive(ctx,
		int(req.Id),
		&entity.ArchiveInsert{
			Title:       req.Archive.Heading,
			Description: req.Archive.Description,
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update archive")
	}

	return &pb_admin.UpdateArchiveResponse{}, nil
}

func (s *Server) AddArchiveItems(ctx context.Context, req *pb_admin.AddArchiveItemsRequest) (*pb_admin.AddArchiveItemsResponse, error) {
	items := make([]entity.ArchiveItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		ai := dto.ConvertPbArchiveItemInsertToEntity(i)
		items = append(items, *ai)
	}

	err := s.repo.Archive().AddArchiveItems(ctx, int(req.ArchiveId), items)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add archive items",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add archive items")
	}

	return &pb_admin.AddArchiveItemsResponse{}, nil
}

func (s *Server) DeleteArchiveItem(ctx context.Context, req *pb_admin.DeleteArchiveItemRequest) (*pb_admin.DeleteArchiveItemResponse, error) {
	err := s.repo.Archive().DeleteArchiveItem(ctx, int(req.ItemId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete archive items",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.DeleteArchiveItemResponse{}, nil
}

func (s *Server) GetArchivesPaged(ctx context.Context, req *pb_admin.GetArchivesPagedRequest) (*pb_admin.GetArchivesPagedResponse, error) {
	afs, err := s.repo.Archive().GetArchivesPaged(ctx,
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archives paged",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	pbAfs := []*pb_common.ArchiveFull{}

	for _, af := range afs {
		pbAfs = append(pbAfs, dto.ConvertArchiveFullEntityToPb(&af))
	}

	return &pb_admin.GetArchivesPagedResponse{
		Archives: pbAfs,
	}, nil

}

func (s *Server) GetArchiveById(ctx context.Context, req *pb_admin.GetArchiveByIdRequest) (*pb_admin.GetArchiveByIdResponse, error) {
	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.GetArchiveByIdResponse{
		Archive: dto.ConvertArchiveFullEntityToPb(af),
	}, nil
}

func (s *Server) DeleteArchiveById(ctx context.Context, req *pb_admin.DeleteArchiveByIdRequest) (*pb_admin.DeleteArchiveByIdResponse, error) {
	err := s.repo.Archive().DeleteArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete archive by id",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	return &pb_admin.DeleteArchiveByIdResponse{}, nil
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

		price, err := decimal.NewFromString(sc.Price.Value)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert string to decimal",
				slog.String("err", err.Error()),
			)
			continue
		}

		err = s.repo.Settings().SetShipmentCarrierPrice(ctx, sc.Carrier, price)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set shipment carrier price",
				slog.String("err", err.Error()),
			)
			continue
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

	return &pb_admin.UpdateSettingsResponse{}, nil

}
