package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fulfillmentColumnEntityToPb = map[entity.FulfillmentColumn]pb_common.FulfillmentColumn{
	entity.FulfillmentColumnToFulfill: pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_TO_FULFILL,
	entity.FulfillmentColumnShipped:   pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_SHIPPED,
	entity.FulfillmentColumnDelivered: pb_common.FulfillmentColumn_FULFILLMENT_COLUMN_DELIVERED,
}

// ConvertEntityFulfillmentCardToPb converts a board tile (compact order +
// annotation summary) to proto. Returns an error if the order money conversion
// fails.
func ConvertEntityFulfillmentCardToPb(c *entity.FulfillmentCard) (*pb_common.FulfillmentCard, error) {
	pbOrder, err := ConvertEntityOrderToPbCommonOrder(c.Order)
	if err != nil {
		return nil, err
	}
	return &pb_common.FulfillmentCard{
		Order:          pbOrder,
		Column:         fulfillmentColumnEntityToPb[c.Column],
		Assignee:       c.Assignee,
		ChecklistDone:  int32(c.ChecklistDone),
		ChecklistTotal: int32(c.ChecklistTotal),
		HasNotes:       c.HasNotes,
	}, nil
}

// ConvertEntityFulfillmentBoardToPb converts the three-column board to proto,
// always emitting all three columns (even when empty) in a stable order.
func ConvertEntityFulfillmentBoardToPb(b *entity.FulfillmentBoard) ([]*pb_common.FulfillmentColumnCards, error) {
	cols := []struct {
		col   entity.FulfillmentColumn
		cards []entity.FulfillmentCard
	}{
		{entity.FulfillmentColumnToFulfill, b.ToFulfill},
		{entity.FulfillmentColumnShipped, b.Shipped},
		{entity.FulfillmentColumnDelivered, b.Delivered},
	}
	out := make([]*pb_common.FulfillmentColumnCards, 0, len(cols))
	for _, c := range cols {
		pbCards := make([]*pb_common.FulfillmentCard, 0, len(c.cards))
		for i := range c.cards {
			pbCard, err := ConvertEntityFulfillmentCardToPb(&c.cards[i])
			if err != nil {
				return nil, err
			}
			pbCards = append(pbCards, pbCard)
		}
		out = append(out, &pb_common.FulfillmentColumnCards{
			Column: fulfillmentColumnEntityToPb[c.col],
			Cards:  pbCards,
		})
	}
	return out, nil
}

// ConvertEntityFulfillmentChecklistToPb converts fulfillment checklist items.
func ConvertEntityFulfillmentChecklistToPb(items []entity.FulfillmentChecklistItem) []*pb_common.FulfillmentChecklistItem {
	out := make([]*pb_common.FulfillmentChecklistItem, 0, len(items))
	for i := range items {
		out = append(out, &pb_common.FulfillmentChecklistItem{
			Id:        int32(items[i].Id),
			Content:   items[i].Content,
			IsDone:    items[i].IsDone,
			Position:  int32(items[i].Position),
			CreatedAt: timestamppb.New(items[i].CreatedAt),
		})
	}
	return out
}

// ConvertEntityOrderFulfillmentToPb converts the full annotation (assignee, notes,
// checklist) to proto. A nil annotation yields an annotation with just the uuid
// (the frontend still gets an object to render an empty card).
func ConvertEntityOrderFulfillmentToPb(orderUUID string, f *entity.OrderFulfillment) *pb_common.FulfillmentAnnotation {
	if f == nil {
		return &pb_common.FulfillmentAnnotation{OrderUuid: orderUUID}
	}
	return &pb_common.FulfillmentAnnotation{
		OrderUuid: f.OrderUuid,
		Assignee:  f.Assignee,
		Notes:     f.Notes.String,
		Checklist: ConvertEntityFulfillmentChecklistToPb(f.Checklist),
	}
}
