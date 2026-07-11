package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

func TestConvertEntityFulfillmentBoardToPb(t *testing.T) {
	placed := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	mkOrder := func(uuid string) entity.Order {
		return entity.Order{UUID: uuid, Currency: "USD", TotalPrice: decimal.NewFromInt(100), Placed: placed}
	}
	board := &entity.FulfillmentBoard{
		ToFulfill: []entity.FulfillmentCard{
			{Order: mkOrder("a"), Column: entity.FulfillmentColumnToFulfill, Assignee: "alice", ChecklistDone: 1, ChecklistTotal: 3, HasNotes: true},
			{Order: mkOrder("b"), Column: entity.FulfillmentColumnToFulfill},
		},
		Shipped: []entity.FulfillmentCard{
			{Order: mkOrder("c"), Column: entity.FulfillmentColumnShipped},
		},
		// Delivered intentionally empty.
	}

	cols, err := ConvertEntityFulfillmentBoardToPb(board)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All three columns are always emitted, in a stable order.
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}
	if cols[0].Column != pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_TO_FULFILL ||
		cols[1].Column != pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_SHIPPED ||
		cols[2].Column != pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_DELIVERED {
		t.Errorf("column order/labels mismatch: %v %v %v", cols[0].Column, cols[1].Column, cols[2].Column)
	}
	if len(cols[0].Cards) != 2 || len(cols[1].Cards) != 1 || len(cols[2].Cards) != 0 {
		t.Fatalf("card counts mismatch: %d %d %d", len(cols[0].Cards), len(cols[1].Cards), len(cols[2].Cards))
	}
	// Card order within a column is preserved (oldest-first from the store).
	c0 := cols[0].Cards[0]
	if c0.Order.Uuid != "a" || c0.Assignee != "alice" || c0.ChecklistDone != 1 || c0.ChecklistTotal != 3 || !c0.HasNotes {
		t.Errorf("first card fields mismatch: %+v", c0)
	}
	if cols[0].Cards[1].Order.Uuid != "b" {
		t.Errorf("card order not preserved: %q", cols[0].Cards[1].Order.Uuid)
	}
}

func TestConvertEntityOrderFulfillmentToPb(t *testing.T) {
	// Nil annotation still yields an object carrying the uuid.
	got := ConvertEntityOrderFulfillmentToPb("uX", nil)
	if got.OrderUuid != "uX" || got.Assignee != "" || got.Notes != "" || len(got.Checklist) != 0 {
		t.Errorf("nil annotation mismatch: %+v", got)
	}

	f := &entity.OrderFulfillment{
		OrderUuid: "uY",
		Assignee:  "bob",
		Notes:     sql.NullString{String: "handle with care", Valid: true},
		Checklist: []entity.FulfillmentChecklistItem{
			{Id: 1, Content: "picked", IsDone: true, Position: 0},
			{Id: 2, Content: "packed", IsDone: false, Position: 1},
		},
	}
	got = ConvertEntityOrderFulfillmentToPb("uY", f)
	if got.OrderUuid != "uY" || got.Assignee != "bob" || got.Notes != "handle with care" {
		t.Errorf("annotation mismatch: %+v", got)
	}
	if len(got.Checklist) != 2 || got.Checklist[0].Content != "picked" || !got.Checklist[0].IsDone ||
		got.Checklist[1].Content != "packed" || got.Checklist[1].IsDone {
		t.Errorf("checklist mismatch: %+v", got.Checklist)
	}
}

func TestValidateChecklistContent(t *testing.T) {
	if s, err := ValidateChecklistContent("  pick the item  "); err != nil || s != "pick the item" {
		t.Errorf("valid content: %q %v", s, err)
	}
	if _, err := ValidateChecklistContent("   "); err == nil {
		t.Errorf("empty content should error")
	}
	if _, err := ValidateChecklistContent(string(make([]byte, maxChecklistContent+1))); err == nil {
		t.Errorf("over-length content should error")
	}
}
