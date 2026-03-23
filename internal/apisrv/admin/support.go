package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		return nil, status.Errorf(codes.Internal, "can't get support tickets paged: %v", err)
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
