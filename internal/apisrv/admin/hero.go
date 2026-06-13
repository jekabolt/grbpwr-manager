package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddHero(ctx context.Context, req *pb_admin.AddHeroRequest) (*pb_admin.AddHeroResponse, error) {

	heroFull := dto.ConvertCommonHeroFullInsertToEntity(req.Hero)

	err := s.repo.Hero().SetHero(ctx, heroFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add hero")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Hero: true,
	})
	return &pb_admin.AddHeroResponse{}, nil
}
