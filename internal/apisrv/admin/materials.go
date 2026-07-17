package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateMaterial adds a catalog material.
func (s *Server) CreateMaterial(ctx context.Context, req *pb_admin.CreateMaterialRequest) (*pb_admin.CreateMaterialResponse, error) {
	ins, err := dto.ConvertPbMaterialToEntityInsert(req.GetMaterial())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	actor := authsrv.GetAdminUsername(ctx)
	ins.CreatedBy, ins.UpdatedBy = actor, actor
	id, err := s.repo.TechCards().CreateMaterial(ctx, ins)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't create material", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create material")
	}
	return &pb_admin.CreateMaterialResponse{Id: int64(id)}, nil
}

// UpdateMaterial updates a catalog material's descriptive fields (not price history).
func (s *Server) UpdateMaterial(ctx context.Context, req *pb_admin.UpdateMaterialRequest) (*pb_admin.UpdateMaterialResponse, error) {
	m := req.GetMaterial()
	if m == nil || m.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material.id is required")
	}
	ins, err := dto.ConvertPbMaterialToEntityInsert(m)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ins.UpdatedBy = authsrv.GetAdminUsername(ctx)
	if err := s.repo.TechCards().UpdateMaterial(ctx, int(m.Id), ins, int(req.GetExpectedLockVersion())); err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't update material", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update material")
	}
	return &pb_admin.UpdateMaterialResponse{}, nil
}

// ArchiveMaterial toggles a material's archived flag.
func (s *Server) ArchiveMaterial(ctx context.Context, req *pb_admin.ArchiveMaterialRequest) (*pb_admin.ArchiveMaterialResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.TechCards().ArchiveMaterial(ctx, int(req.GetId()), req.GetArchived()); err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't archive material", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't archive material")
	}
	return &pb_admin.ArchiveMaterialResponse{}, nil
}

// GetMaterial returns a material with its current price.
func (s *Server) GetMaterial(ctx context.Context, req *pb_admin.GetMaterialRequest) (*pb_admin.GetMaterialResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	m, err := s.repo.TechCards().GetMaterial(ctx, int(req.GetId()))
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't get material", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get material")
	}
	pbM := dto.ConvertEntityMaterialToPb(*m)
	if read, _ := s.costingAccess(ctx); !read {
		stripMaterialCosting(pbM) // material price is confidential cost (task 19)
	}
	return &pb_admin.GetMaterialResponse{Material: pbM}, nil
}

// ListMaterials returns catalog materials (with current price), optionally filtered by section.
func (s *Server) ListMaterials(ctx context.Context, req *pb_admin.ListMaterialsRequest) (*pb_admin.ListMaterialsResponse, error) {
	materials, err := s.repo.TechCards().ListMaterials(ctx, req.GetSection(), req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list materials", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list materials")
	}
	read, _ := s.costingAccess(ctx)
	out := make([]*pb_common.Material, 0, len(materials))
	for _, m := range materials {
		pbM := dto.ConvertEntityMaterialToPb(m)
		if !read {
			stripMaterialCosting(pbM)
		}
		out = append(out, pbM)
	}
	return &pb_admin.ListMaterialsResponse{Materials: out}, nil
}

// AddMaterialPrice appends a point to a material's price history.
func (s *Server) AddMaterialPrice(ctx context.Context, req *pb_admin.AddMaterialPriceRequest) (*pb_admin.AddMaterialPriceResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set a material price")
	}
	price, err := dto.ConvertPbMaterialPriceToEntity(req.GetPrice())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.TechCards().AddMaterialPrice(ctx, price); err != nil {
		slog.Default().ErrorContext(ctx, "can't add material price", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add material price")
	}
	return &pb_admin.AddMaterialPriceResponse{}, nil
}

// ListMaterialPrices returns a material's full price history.
func (s *Server) ListMaterialPrices(ctx context.Context, req *pb_admin.ListMaterialPricesRequest) (*pb_admin.ListMaterialPricesResponse, error) {
	if req.GetMaterialId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material_id is required")
	}
	// The entire payload is confidential cost data; without costing:read there is nothing
	// non-cost to return, so shape it to an empty history (task 19).
	if read, _ := s.costingAccess(ctx); !read {
		return &pb_admin.ListMaterialPricesResponse{}, nil
	}
	prices, err := s.repo.TechCards().ListMaterialPrices(ctx, int(req.GetMaterialId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list material prices", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list material prices")
	}
	out := make([]*pb_common.MaterialPrice, 0, len(prices))
	for _, p := range prices {
		out = append(out, dto.ConvertEntityMaterialPriceToPb(p))
	}
	return &pb_admin.ListMaterialPricesResponse{Prices: out}, nil
}
