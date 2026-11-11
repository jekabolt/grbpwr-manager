package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PIECE FOLDS (tech_card_piece_fold, 0443) — the handler half. The seams' contract: the browser
// resolves the line on each size, the server stores it and owns the staleness of the source.
// Keyed and partial; the card row is locked, a released card refuses, lock_version never moves, and
// no section digest sees the rows. Both RPCs echo the card's full list so the client re-syncs.

// UpsertTechCardPieceFolds inserts or replaces fold lines by fold_key.
func (s *Server) UpsertTechCardPieceFolds(ctx context.Context, req *pb_admin.UpsertTechCardPieceFoldsRequest) (*pb_admin.UpsertTechCardPieceFoldsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if len(req.GetFolds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "folds: name at least one fold")
	}
	in, err := dto.TechCardPieceFoldsWriteFromPb(int(req.GetTechCardId()), req.GetFolds(), authsrv.GetAdminUsername(ctx))
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	folds, written, err := s.repo.TechCards().UpsertTechCardPieceFolds(ctx, in)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't write tech card piece folds",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't write tech card piece folds")
	}
	return &pb_admin.UpsertTechCardPieceFoldsResponse{
		Folds:   dto.TechCardPieceFoldsToPb(folds),
		Written: int32(written),
	}, nil
}

// DeleteTechCardPieceFolds removes fold lines by fold_key.
func (s *Server) DeleteTechCardPieceFolds(ctx context.Context, req *pb_admin.DeleteTechCardPieceFoldsRequest) (*pb_admin.DeleteTechCardPieceFoldsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if len(req.GetFoldKeys()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "fold_keys: name at least one fold_key")
	}
	folds, deleted, err := s.repo.TechCards().DeleteTechCardPieceFolds(ctx, int(req.GetTechCardId()), req.GetFoldKeys())
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't delete tech card piece folds",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete tech card piece folds")
	}
	return &pb_admin.DeleteTechCardPieceFoldsResponse{
		Folds:   dto.TechCardPieceFoldsToPb(folds),
		Deleted: int32(deleted),
	}, nil
}

// ReplaceTechCardPieceFolds deletes and upserts fold lines in one transaction.
func (s *Server) ReplaceTechCardPieceFolds(ctx context.Context, req *pb_admin.ReplaceTechCardPieceFoldsRequest) (*pb_admin.ReplaceTechCardPieceFoldsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	in, err := dto.TechCardPieceFoldsReplaceFromPb(
		int(req.GetTechCardId()), req.GetDeleteFoldKeys(), req.GetFolds(), authsrv.GetAdminUsername(ctx))
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	folds, written, deleted, err := s.repo.TechCards().ReplaceTechCardPieceFolds(ctx, in)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't replace tech card piece folds",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't replace tech card piece folds")
	}
	return &pb_admin.ReplaceTechCardPieceFoldsResponse{
		Folds:   dto.TechCardPieceFoldsToPb(folds),
		Written: int32(written),
		Deleted: int32(deleted),
	}, nil
}
