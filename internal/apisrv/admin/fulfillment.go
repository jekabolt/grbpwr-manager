package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxFulfillmentAssignee = 255
	maxFulfillmentNotes    = 60000
)

// GetFulfillmentBoard returns the three-column projection of orders (with each
// card's annotation summary).
func (s *Server) GetFulfillmentBoard(ctx context.Context, req *pb_admin.GetFulfillmentBoardRequest) (*pb_admin.GetFulfillmentBoardResponse, error) {
	board, err := s.repo.Fulfillment().GetFulfillmentBoard(ctx, int(req.DeliveredLimit))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get fulfillment board", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get fulfillment board")
	}
	cols, err := dto.ConvertEntityFulfillmentBoardToPb(board)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert fulfillment board", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get fulfillment board")
	}
	return &pb_admin.GetFulfillmentBoardResponse{Columns: cols}, nil
}

// GetFulfillmentCard returns full order detail plus the annotation for one order.
func (s *Server) GetFulfillmentCard(ctx context.Context, req *pb_admin.GetFulfillmentCardRequest) (*pb_admin.GetFulfillmentCardResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order for fulfillment card", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get fulfillment card")
	}
	pbOrder, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert order full", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get fulfillment card")
	}
	ann, err := s.repo.Fulfillment().GetOrderFulfillment(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order fulfillment", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get fulfillment card")
	}
	return &pb_admin.GetFulfillmentCardResponse{
		Order:         pbOrder,
		Annotation:    dto.ConvertEntityOrderFulfillmentToPb(req.OrderUuid, ann),
		StripeDetails: dto.ConvertToOrderStripeDetails(orderFull),
	}, nil
}

// SetFulfillmentAssignee sets (or clears with "") the order's fulfillment assignee.
func (s *Server) SetFulfillmentAssignee(ctx context.Context, req *pb_admin.SetFulfillmentAssigneeRequest) (*pb_admin.SetFulfillmentAssigneeResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	assignee := strings.TrimSpace(req.Assignee)
	if len(assignee) > maxFulfillmentAssignee {
		return nil, status.Errorf(codes.InvalidArgument, "assignee must be at most %d characters", maxFulfillmentAssignee)
	}
	if err := s.repo.Fulfillment().SetFulfillmentAssignee(ctx, req.OrderUuid, assignee, authsrv.GetAdminUsername(ctx)); err != nil {
		slog.Default().ErrorContext(ctx, "can't set fulfillment assignee", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't set fulfillment assignee")
	}
	return &pb_admin.SetFulfillmentAssigneeResponse{}, nil
}

// SetFulfillmentNotes sets the order's internal packing notes.
func (s *Server) SetFulfillmentNotes(ctx context.Context, req *pb_admin.SetFulfillmentNotesRequest) (*pb_admin.SetFulfillmentNotesResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	if len(req.Notes) > maxFulfillmentNotes {
		return nil, status.Errorf(codes.InvalidArgument, "notes must be at most %d characters", maxFulfillmentNotes)
	}
	if err := s.repo.Fulfillment().SetFulfillmentNotes(ctx, req.OrderUuid, req.Notes, authsrv.GetAdminUsername(ctx)); err != nil {
		slog.Default().ErrorContext(ctx, "can't set fulfillment notes", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't set fulfillment notes")
	}
	return &pb_admin.SetFulfillmentNotesResponse{}, nil
}

// AddFulfillmentChecklistItem appends a packing-checklist item to an order.
func (s *Server) AddFulfillmentChecklistItem(ctx context.Context, req *pb_admin.AddFulfillmentChecklistItemRequest) (*pb_admin.AddFulfillmentChecklistItemResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	content, err := dto.ValidateChecklistContent(req.Content)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	id, err := s.repo.Fulfillment().AddFulfillmentChecklistItem(ctx, req.OrderUuid, content, authsrv.GetAdminUsername(ctx))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add fulfillment checklist item", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add fulfillment checklist item")
	}
	return &pb_admin.AddFulfillmentChecklistItemResponse{Id: int32(id)}, nil
}

// SetFulfillmentChecklistItemDone sets a packing-checklist item's done flag.
func (s *Server) SetFulfillmentChecklistItemDone(ctx context.Context, req *pb_admin.SetFulfillmentChecklistItemDoneRequest) (*pb_admin.SetFulfillmentChecklistItemDoneResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "checklist item id is required")
	}
	if err := s.repo.Fulfillment().SetFulfillmentChecklistItemDone(ctx, int(req.Id), req.IsDone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "checklist item not found")
		}
		slog.Default().ErrorContext(ctx, "can't set fulfillment checklist item done", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't set fulfillment checklist item done")
	}
	return &pb_admin.SetFulfillmentChecklistItemDoneResponse{}, nil
}

// DeleteFulfillmentChecklistItem removes a packing-checklist item.
func (s *Server) DeleteFulfillmentChecklistItem(ctx context.Context, req *pb_admin.DeleteFulfillmentChecklistItemRequest) (*pb_admin.DeleteFulfillmentChecklistItemResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "checklist item id is required")
	}
	if err := s.repo.Fulfillment().DeleteFulfillmentChecklistItem(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete fulfillment checklist item", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't delete fulfillment checklist item")
	}
	return &pb_admin.DeleteFulfillmentChecklistItemResponse{}, nil
}

// ShipFulfillmentOrder records the tracking code (the real shipped transition +
// shipped email), gated by fulfillment perms so a warehouse role can ship without
// full orders:write.
func (s *Server) ShipFulfillmentOrder(ctx context.Context, req *pb_admin.ShipFulfillmentOrderRequest) (*pb_admin.ShipFulfillmentOrderResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	trackingCode := strings.TrimSpace(req.TrackingCode)
	if trackingCode == "" {
		return nil, status.Error(codes.InvalidArgument, "tracking code is required")
	}
	if err := s.shipOrder(ctx, req.OrderUuid, trackingCode); err != nil {
		slog.Default().ErrorContext(ctx, "can't ship fulfillment order", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't ship order")
	}
	return &pb_admin.ShipFulfillmentOrderResponse{}, nil
}

// MarkFulfillmentDelivered performs the real delivered transition for an order.
func (s *Server) MarkFulfillmentDelivered(ctx context.Context, req *pb_admin.MarkFulfillmentDeliveredRequest) (*pb_admin.MarkFulfillmentDeliveredResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	if err := s.deliverOrder(ctx, req.OrderUuid); err != nil {
		slog.Default().ErrorContext(ctx, "can't mark fulfillment order delivered", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't mark order delivered")
	}
	return &pb_admin.MarkFulfillmentDeliveredResponse{}, nil
}
