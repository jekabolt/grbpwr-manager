package entity

import (
	"database/sql"
	"time"
)

// FulfillmentColumn is a lane on the orders-fulfillment board. Each column is
// bound 1:1 to a real order status — the board is a projection of orders, not a
// separate state machine.
type FulfillmentColumn string

const (
	FulfillmentColumnToFulfill FulfillmentColumn = "to_fulfill" // order status confirmed
	FulfillmentColumnShipped   FulfillmentColumn = "shipped"    // order status shipped
	FulfillmentColumnDelivered FulfillmentColumn = "delivered"  // order status delivered
)

// OrderStatusToFulfillmentColumn maps the order statuses that appear on the board
// to their column. Statuses not present here (placed, awaiting_payment, cancelled,
// refunds) are not shown on the fulfillment board.
var OrderStatusToFulfillmentColumn = map[OrderStatusName]FulfillmentColumn{
	Confirmed: FulfillmentColumnToFulfill,
	Shipped:   FulfillmentColumnShipped,
	Delivered: FulfillmentColumnDelivered,
}

// OrderFulfillment is the board-owned annotation overlaid on an order (assignee,
// internal notes, packing checklist). It carries NO order status — that lives on
// the order. 1:1 with an order via OrderUuid; lazily created on first edit.
type OrderFulfillment struct {
	Id        int                        `db:"id"`
	OrderUuid string                     `db:"order_uuid"`
	Assignee  string                     `db:"assignee"`
	Notes     sql.NullString             `db:"notes"`
	CreatedBy string                     `db:"created_by"`
	CreatedAt time.Time                  `db:"created_at"`
	UpdatedAt time.Time                  `db:"updated_at"`
	Checklist []FulfillmentChecklistItem `db:"-"`
}

// FulfillmentChecklistItem is one packing-checklist row on an order fulfillment.
type FulfillmentChecklistItem struct {
	Id                 int       `db:"id"`
	OrderFulfillmentId int       `db:"order_fulfillment_id"`
	Content            string    `db:"content"`
	IsDone             bool      `db:"is_done"`
	Position           int       `db:"position"`
	CreatedAt          time.Time `db:"created_at"`
}

// FulfillmentCard is a board tile: the compact order plus an annotation summary.
// Full order detail + full annotation come from a separate card fetch.
type FulfillmentCard struct {
	Order          Order
	Column         FulfillmentColumn
	Assignee       string
	ChecklistDone  int
	ChecklistTotal int
	HasNotes       bool
}

// FulfillmentBoard is the three-column projection returned to the board UI. The
// active columns (ToFulfill, Shipped) are oldest order first (longest-waiting
// picked first); the historical Delivered column is newest first and bounded.
type FulfillmentBoard struct {
	ToFulfill []FulfillmentCard
	Shipped   []FulfillmentCard
	Delivered []FulfillmentCard
}
