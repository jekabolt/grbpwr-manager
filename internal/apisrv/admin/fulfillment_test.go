package admin

import (
	"context"
	"database/sql"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetFulfillmentBoard always returns three columns (even when empty).
func TestGetFulfillmentBoardReturnsThreeColumns(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	ff := mocks.NewMockFulfillment(t)
	repo.EXPECT().Fulfillment().Return(ff)
	ff.EXPECT().GetFulfillmentBoard(mock.Anything, 0).Return(&entity.FulfillmentBoard{}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetFulfillmentBoard(context.Background(), &pb_admin.GetFulfillmentBoardRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Columns) != 3 {
		t.Errorf("expected 3 columns, got %d", len(resp.Columns))
	}
}

// GetFulfillmentCard requires an order_uuid and maps a missing order to NotFound.
func TestGetFulfillmentCardValidationAndNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.GetFulfillmentCard(context.Background(), &pb_admin.GetFulfillmentCardRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing uuid: want InvalidArgument, got %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	repo2.EXPECT().Order().Return(order)
	order.EXPECT().GetOrderFullByUUID(mock.Anything, "u1").Return(nil, sql.ErrNoRows)
	s2 := &Server{repo: repo2}
	if _, err := s2.GetFulfillmentCard(context.Background(), &pb_admin.GetFulfillmentCardRequest{OrderUuid: "u1"}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// SetFulfillmentAssignee trims the assignee and stamps created_by from the JWT.
func TestSetFulfillmentAssigneeStampsCreatedBy(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	ff := mocks.NewMockFulfillment(t)
	repo.EXPECT().Fulfillment().Return(ff)
	ff.EXPECT().SetFulfillmentAssignee(mock.Anything, "u1", "alice", "max").Return(nil)

	s := &Server{repo: repo}
	ctx := authsrv.PutAdminUsername(context.Background(), "max")
	if _, err := s.SetFulfillmentAssignee(ctx, &pb_admin.SetFulfillmentAssigneeRequest{OrderUuid: "u1", Assignee: "  alice  "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// order_uuid required
	repo2 := mocks.NewMockRepository(t)
	s2 := &Server{repo: repo2}
	if _, err := s2.SetFulfillmentAssignee(context.Background(), &pb_admin.SetFulfillmentAssigneeRequest{Assignee: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("want InvalidArgument, got %v", err)
	}
}

// AddFulfillmentChecklistItem validates content and stamps created_by from the JWT.
func TestAddFulfillmentChecklistItem(t *testing.T) {
	// empty content
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.AddFulfillmentChecklistItem(context.Background(), &pb_admin.AddFulfillmentChecklistItemRequest{OrderUuid: "u1", Content: "   "}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty content: want InvalidArgument, got %v", err)
	}

	// success
	repo2 := mocks.NewMockRepository(t)
	ff := mocks.NewMockFulfillment(t)
	repo2.EXPECT().Fulfillment().Return(ff)
	ff.EXPECT().AddFulfillmentChecklistItem(mock.Anything, "u1", "label printed", "max").Return(4, nil)
	s2 := &Server{repo: repo2}
	ctx := authsrv.PutAdminUsername(context.Background(), "max")
	resp, err := s2.AddFulfillmentChecklistItem(ctx, &pb_admin.AddFulfillmentChecklistItemRequest{OrderUuid: "u1", Content: "  label printed  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Id != 4 {
		t.Errorf("id mismatch: %d", resp.Id)
	}
}

// SetFulfillmentChecklistItemDone maps a missing item to NotFound.
func TestSetFulfillmentChecklistItemDoneNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	ff := mocks.NewMockFulfillment(t)
	repo.EXPECT().Fulfillment().Return(ff)
	ff.EXPECT().SetFulfillmentChecklistItemDone(mock.Anything, 7, true).Return(sql.ErrNoRows)
	s := &Server{repo: repo}
	if _, err := s.SetFulfillmentChecklistItemDone(context.Background(), &pb_admin.SetFulfillmentChecklistItemDoneRequest{Id: 7, IsDone: true}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// ShipFulfillmentOrder requires both an order_uuid and a non-empty tracking code
// before touching the order store.
func TestShipFulfillmentOrderRequiresTracking(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.ShipFulfillmentOrder(context.Background(), &pb_admin.ShipFulfillmentOrderRequest{OrderUuid: "u1", TrackingCode: "   "}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("blank tracking: want InvalidArgument, got %v", err)
	}
	if _, err := s.ShipFulfillmentOrder(context.Background(), &pb_admin.ShipFulfillmentOrderRequest{TrackingCode: "TRK1"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing uuid: want InvalidArgument, got %v", err)
	}
}

// MarkFulfillmentDelivered requires an order_uuid and delegates to the order store.
func TestMarkFulfillmentDelivered(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.MarkFulfillmentDelivered(context.Background(), &pb_admin.MarkFulfillmentDeliveredRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing uuid: want InvalidArgument, got %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	repo2.EXPECT().Order().Return(order)
	order.EXPECT().DeliveredOrder(mock.Anything, "u2").Return(nil)
	s2 := &Server{repo: repo2}
	if _, err := s2.MarkFulfillmentDelivered(context.Background(), &pb_admin.MarkFulfillmentDeliveredRequest{OrderUuid: "u2"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
