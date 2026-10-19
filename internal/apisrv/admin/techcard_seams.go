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

// CONFIRMED SEAMS (tech_card_seam) — the handler half.
//
// The browser resolves seam geometry against the card's DXF (the server does not parse DXF); what
// crosses the wire is the technologist's DECISION plus the anchors it was made on. The server owns
// the staleness of the source: it stamps a fingerprint of the sheets + block→piece links of the
// seam's pieces' fabric scopes, and every read says `stale` when that moved.
//
// Keyed and partial; the card row is locked, a released card refuses, lock_version never moves,
// and no section digest sees the rows. Both RPCs echo the card's full list so the client re-syncs.

// UpsertTechCardSeams inserts or replaces seam decisions by seam_key.
func (s *Server) UpsertTechCardSeams(ctx context.Context, req *pb_admin.UpsertTechCardSeamsRequest) (*pb_admin.UpsertTechCardSeamsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	in, err := dto.TechCardSeamsWriteFromPb(int(req.GetTechCardId()), req.GetSeams(), authsrv.GetAdminUsername(ctx))
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	seams, written, err := s.repo.TechCards().UpsertTechCardSeams(ctx, in)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't write tech card seams",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't write tech card seams")
	}
	return &pb_admin.UpsertTechCardSeamsResponse{
		Seams:   dto.TechCardSeamsToPb(seams),
		Written: int32(written),
	}, nil
}

// DeleteTechCardSeams removes seam decisions by seam_key.
func (s *Server) DeleteTechCardSeams(ctx context.Context, req *pb_admin.DeleteTechCardSeamsRequest) (*pb_admin.DeleteTechCardSeamsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if len(req.GetSeamKeys()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "seam_keys: name at least one seam_key")
	}
	seams, deleted, err := s.repo.TechCards().DeleteTechCardSeams(ctx, int(req.GetTechCardId()), req.GetSeamKeys())
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't delete tech card seams",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete tech card seams")
	}
	return &pb_admin.DeleteTechCardSeamsResponse{
		Seams:   dto.TechCardSeamsToPb(seams),
		Deleted: int32(deleted),
	}, nil
}
