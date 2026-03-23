package frontend

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) SubmitSupportTicket(ctx context.Context, req *pb_frontend.SubmitSupportTicketRequest) (*pb_frontend.SubmitSupportTicketResponse, error) {
	clientIP := middleware.GetClientIP(ctx)

	ticket := dto.ConvertPbSupportTicketInsertToEntity(req.Ticket)

	if err := entity.ValidateSupportTicketInsert(&ticket); err != nil {
		slog.Default().WarnContext(ctx, "invalid support ticket",
			slog.String("err", err.Error()),
			slog.String("email", ticket.Email),
		)
		return nil, status.Errorf(codes.InvalidArgument, "invalid ticket: %s", err.Error())
	}

	if err := s.rateLimiter.CheckSupportTicket(clientIP, ticket.Email); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for support ticket",
			slog.String("ip", clientIP),
			slog.String("email", ticket.Email),
		)
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
	}

	caseNumber, err := s.repo.Support().SubmitTicket(ctx, ticket)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create support ticket",
			slog.String("err", err.Error()),
			slog.String("email", ticket.Email),
		)
		return nil, status.Errorf(codes.Internal, "can't create support ticket")
	}

	slog.Default().InfoContext(ctx, "support ticket created",
		slog.String("case_number", caseNumber),
		slog.String("email", ticket.Email),
	)

	return &pb_frontend.SubmitSupportTicketResponse{}, nil
}
