package admin

import (
	"context"
	"fmt"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slices"
	"golang.org/x/exp/slog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo   dependency.Repository
	bucket dependency.FileStore
}

// New creates a new server with admin handlers.
func New(r dependency.Repository, b dependency.FileStore) *Server {
	return &Server{
		repo:   r,
		bucket: b,
	}
}

// CONTENT MANAGER

// UploadContentImage
func (s *Server) UploadContentImage(ctx context.Context, req *pb_admin.UploadContentImageRequest) (*pb_admin.UploadContentImageResponse, error) {
	m, err := s.bucket.UploadContentImage(ctx, req.RawB64Image, req.Folder, req.ImageName)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't upload content image",
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
	media, err := s.bucket.UploadContentVideo(ctx, req.GetRaw(), req.Folder, req.VideoName, req.ContentType)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't upload content video",
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
		slog.Default().ErrorCtx(ctx, "can't delete object from bucket",
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
		slog.Default().ErrorCtx(ctx, "can't list objects from bucket",
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

	prdNew, err := dto.ConvertFromPbToEntity(req.GetProduct())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert proto product to entity product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert proto product to entity product")
	}

	_, err = v.ValidateStruct(prdNew)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "validation add product request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation  add product request failed: %w", err).Error())
	}

	prd, err := s.repo.Products().AddProduct(ctx, prdNew)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't create a product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create a product")
	}

	pbPrd, err := dto.ConvertToPbProductFull(prd)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert entity product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity product to proto product")
	}

	return &pb_admin.AddProductResponse{
		Product: pbPrd,
	}, nil
}

func (s *Server) AddProductMeasurement(ctx context.Context, req *pb_admin.AddProductMeasurementRequest) (*pb_admin.AddProductMeasurementResponse, error) {

	value, err := decimal.NewFromString(req.MeasurementValue.String())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert measurement value to decimal",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert measurement value to decimal")
	}

	s.repo.Products().AddProductMeasurement(ctx, int(req.ProductId), int(req.SizeId), int(req.MeasurementNameId), value)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add product measurement",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product measurement")
	}
	return &pb_admin.AddProductMeasurementResponse{}, nil
}

func (s *Server) AddProductMedia(ctx context.Context, req *pb_admin.AddProductMediaRequest) (*pb_admin.AddProductMediaResponse, error) {
	err := s.repo.Products().AddProductMedia(ctx, int(req.ProductId), req.FullSize, req.Thumbnail, req.Compressed)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add product media",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product media")
	}
	return &pb_admin.AddProductMediaResponse{}, nil
}

func (s *Server) AddProductTag(ctx context.Context, req *pb_admin.AddProductTagRequest) (*pb_admin.AddProductTagResponse, error) {
	err := s.repo.Products().AddProductTag(ctx, int(req.ProductId), req.Tag)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add product tag",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add product tag")
	}
	return &pb_admin.AddProductTagResponse{}, nil
}

func (s *Server) DeleteProductByID(ctx context.Context, req *pb_admin.DeleteProductByIDRequest) (*pb_admin.DeleteProductByIDResponse, error) {
	err := s.repo.Products().DeleteProductById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product")
	}
	return &pb_admin.DeleteProductByIDResponse{}, nil
}

func (s *Server) DeleteProductMeasurement(ctx context.Context, req *pb_admin.DeleteProductMeasurementRequest) (*pb_admin.DeleteProductMeasurementResponse, error) {
	err := s.repo.Products().DeleteProductMeasurement(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete product measurement",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product measurement")
	}
	return &pb_admin.DeleteProductMeasurementResponse{}, nil
}

func (s *Server) DeleteProductMedia(ctx context.Context, req *pb_admin.DeleteProductMediaRequest) (*pb_admin.DeleteProductMediaResponse, error) {
	err := s.repo.Products().DeleteProductMedia(ctx, int(req.ProductMediaId))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete product media",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product media")
	}
	return &pb_admin.DeleteProductMediaResponse{}, nil
}

func (s *Server) DeleteProductTag(ctx context.Context, req *pb_admin.DeleteProductTagRequest) (*pb_admin.DeleteProductTagResponse, error) {
	err := s.repo.Products().DeleteProductTag(ctx, int(req.ProductId), req.Tag)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete product tag",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete product tag")
	}
	return &pb_admin.DeleteProductTagResponse{}, nil
}

func (s *Server) GetProductByID(ctx context.Context, req *pb_admin.GetProductByIDRequest) (*pb_admin.GetProductByIDResponse, error) {

	pf, err := s.repo.Products().GetProductById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get product by id")
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert dto product to proto product",
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
	if req.Limit <= 0 || req.Offset <= 0 {
		req.Limit, req.Offset = 15, 0
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

	prds, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, req.ShowHidden)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_common.Product, 0, len(prds))
	for _, prd := range prds {
		pbPrd, err := dto.ConvertEntityProductToPb(&prd)
		if err != nil {
			slog.Default().ErrorCtx(ctx, "can't convert dto product to proto product",
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

func (s *Server) HideProductByID(ctx context.Context, req *pb_admin.HideProductByIDRequest) (*pb_admin.HideProductByIDResponse, error) {
	err := s.repo.Products().HideProductById(ctx, int(req.Id), req.Hide)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't hide product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't hide product by id")
	}
	return &pb_admin.HideProductByIDResponse{}, nil
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
		slog.Default().ErrorCtx(ctx, "can't reduce stock for product sizes",
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
		slog.Default().ErrorCtx(ctx, "can't restore stock for product sizes",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't restore stock for product sizes")
	}
	return &pb_admin.RestoreStockForProductSizesResponse{}, nil
}

func (s *Server) SetSaleByID(ctx context.Context, req *pb_admin.SetSaleByIDRequest) (*pb_admin.SetSaleByIDResponse, error) {

	sale, err := decimal.NewFromString(req.SalePercent)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert sale to decimal",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert sale to decimal")
	}

	if sale.GreaterThan(decimal.NewFromInt(100)) || sale.LessThan(decimal.NewFromInt(0)) {
		return nil, status.Errorf(codes.InvalidArgument, "sale must be between 0 and 100")
	}

	err = s.repo.Products().SetSaleById(ctx, int(req.Id), sale)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't set sale by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't set sale by id")
	}
	return &pb_admin.SetSaleByIDResponse{}, nil
}

func (s *Server) UpdateProductBrand(ctx context.Context, req *pb_admin.UpdateProductBrandRequest) (*pb_admin.UpdateProductBrandResponse, error) {
	if req.Brand == "" {
		return &pb_admin.UpdateProductBrandResponse{}, fmt.Errorf("brand is empty")
	}
	err := s.repo.Products().UpdateProductBrand(ctx, int(req.ProductId), req.Brand)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product brand",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product brand")
	}
	return &pb_admin.UpdateProductBrandResponse{}, nil
}

func (s *Server) UpdateProductCategory(ctx context.Context, req *pb_admin.UpdateProductCategoryRequest) (*pb_admin.UpdateProductCategoryResponse, error) {
	err := s.repo.Products().UpdateProductCategory(ctx, int(req.ProductId), int(req.CategoryId))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product category",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product category")
	}
	return &pb_admin.UpdateProductCategoryResponse{}, nil
}

func (s *Server) UpdateProductColorAndColorHex(ctx context.Context, req *pb_admin.UpdateProductColorAndColorHexRequest) (*pb_admin.UpdateProductColorAndColorHexResponse, error) {
	if req.Color == "" {
		return &pb_admin.UpdateProductColorAndColorHexResponse{}, fmt.Errorf("color is empty")
	}

	if v.IsHexcolor(req.ColorHex) {
		return &pb_admin.UpdateProductColorAndColorHexResponse{}, fmt.Errorf("color hex is not valid")
	}

	err := s.repo.Products().UpdateProductColorAndColorHex(ctx, int(req.ProductId), req.Color, req.ColorHex)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product color and color hex",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product color and color hex")
	}
	return &pb_admin.UpdateProductColorAndColorHexResponse{}, nil
}

func (s *Server) UpdateProductCountryOfOrigin(ctx context.Context, req *pb_admin.UpdateProductCountryOfOriginRequest) (*pb_admin.UpdateProductCountryOfOriginResponse, error) {
	err := s.repo.Products().UpdateProductCountryOfOrigin(ctx, int(req.ProductId), req.CountryOfOrigin)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product country of origin",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product country of origin")
	}
	return &pb_admin.UpdateProductCountryOfOriginResponse{}, nil
}

func (s *Server) UpdateProductDescription(ctx context.Context, req *pb_admin.UpdateProductDescriptionRequest) (*pb_admin.UpdateProductDescriptionResponse, error) {
	if req.Description == "" {
		return &pb_admin.UpdateProductDescriptionResponse{}, fmt.Errorf("description is empty")
	}

	err := s.repo.Products().UpdateProductDescription(ctx, int(req.ProductId), req.Description)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product description",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product description")
	}
	return &pb_admin.UpdateProductDescriptionResponse{}, nil
}

func (s *Server) UpdateProductName(ctx context.Context, req *pb_admin.UpdateProductNameRequest) (*pb_admin.UpdateProductNameResponse, error) {
	if req.Name == "" {
		return &pb_admin.UpdateProductNameResponse{}, fmt.Errorf("name is empty")
	}

	err := s.repo.Products().UpdateProductName(ctx, int(req.ProductId), req.Name)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product name",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product name")
	}
	return &pb_admin.UpdateProductNameResponse{}, nil
}

func (s *Server) UpdateProductPreorder(ctx context.Context, req *pb_admin.UpdateProductPreorderRequest) (*pb_admin.UpdateProductPreorderResponse, error) {
	err := s.repo.Products().UpdateProductPreorder(ctx, int(req.ProductId), req.Preorder)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product preorder",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product preorder")
	}
	return &pb_admin.UpdateProductPreorderResponse{}, nil
}

func (s *Server) UpdateProductPrice(ctx context.Context, req *pb_admin.UpdateProductPriceRequest) (*pb_admin.UpdateProductPriceResponse, error) {

	price, err := decimal.NewFromString(req.Price)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert price to decimal",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert price to decimal")
	}

	err = s.repo.Products().UpdateProductPrice(ctx, int(req.ProductId), price)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product price",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product price")
	}
	return &pb_admin.UpdateProductPriceResponse{}, nil
}

func (s *Server) UpdateProductSKU(ctx context.Context, req *pb_admin.UpdateProductSKURequest) (*pb_admin.UpdateProductSKUResponse, error) {
	if req.Sku == "" {
		return &pb_admin.UpdateProductSKUResponse{}, fmt.Errorf("sku is empty")
	}

	err := s.repo.Products().UpdateProductSKU(ctx, int(req.ProductId), req.Sku)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product sku",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product sku")
	}
	return &pb_admin.UpdateProductSKUResponse{}, nil
}

func (s *Server) UpdateProductSale(ctx context.Context, req *pb_admin.UpdateProductSaleRequest) (*pb_admin.UpdateProductSaleResponse, error) {

	sale, err := decimal.NewFromString(req.Sale)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert sale to decimal",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert sale to decimal")
	}

	err = s.repo.Products().UpdateProductSale(ctx, int(req.ProductId), sale)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product sale",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product sale")
	}
	return &pb_admin.UpdateProductSaleResponse{}, nil

}

func (s *Server) UpdateProductSizeStock(ctx context.Context, req *pb_admin.UpdateProductSizeStockRequest) (*pb_admin.UpdateProductSizeStockResponse, error) {
	err := s.repo.Products().UpdateProductSizeStock(ctx, int(req.ProductId), int(req.SizeId), int(req.Quantity))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product size stock",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product size stock")
	}
	return &pb_admin.UpdateProductSizeStockResponse{}, nil
}

func (s *Server) UpdateProductTargetGender(ctx context.Context, req *pb_admin.UpdateProductTargetGenderRequest) (*pb_admin.UpdateProductTargetGenderResponse, error) {

	tg, err := dto.ConvertPbGenderEnumToEntityGenderEnum(req.Gender)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert gender",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert gender")
	}

	err = s.repo.Products().UpdateProductTargetGender(ctx, int(req.ProductId), tg)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product target gender",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product target gender")
	}
	return &pb_admin.UpdateProductTargetGenderResponse{}, nil
}

func (s *Server) UpdateProductThumbnail(ctx context.Context, req *pb_admin.UpdateProductThumbnailRequest) (*pb_admin.UpdateProductThumbnailResponse, error) {
	if req.Thumbnail == "" {
		return &pb_admin.UpdateProductThumbnailResponse{}, fmt.Errorf("thumbnail is empty")
	}
	err := s.repo.Products().UpdateProductThumbnail(ctx, int(req.ProductId), req.Thumbnail)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update product thumbnail",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update product thumbnail")
	}
	return &pb_admin.UpdateProductThumbnailResponse{}, nil
}

// PROMO MANAGER

func (s *Server) AddPromo(ctx context.Context, req *pb_admin.AddPromoRequest) (*pb_admin.AddPromoResponse, error) {

	pi, err := dto.ConvertPbCommonPromoToEntity(req.Promo)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert pb promo to entity promo",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb promo to entity promo")
	}

	err = s.repo.Promo().AddPromo(ctx, pi)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add promo",
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
		slog.Default().ErrorCtx(ctx, "can't delete promo code",
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
		slog.Default().ErrorCtx(ctx, "can't disable promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't disable promo code")
	}
	return &pb_admin.DisablePromoCodeResponse{}, nil
}

func (s *Server) ListPromos(ctx context.Context, req *pb_admin.ListPromosRequest) (*pb_admin.ListPromosResponse, error) {

	promos, err := s.repo.Promo().ListPromos(ctx)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't list promos",
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
	}, nil
}
