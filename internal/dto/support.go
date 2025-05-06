package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertEntitySupportTicketToPb(ticket entity.SupportTicket) *pb_common.SupportTicket {
	return &pb_common.SupportTicket{
		Id:                  int32(ticket.Id),
		CreatedAt:           timestamppb.New(ticket.CreatedAt),
		UpdatedAt:           timestamppb.New(ticket.UpdatedAt),
		Status:              ticket.Status,
		ResolvedAt:          timestamppb.New(ticket.ResolvedAt.Time),
		SupportTicketInsert: ConvertEntitySupportTicketInsertToPb(ticket.SupportTicketInsert),
	}
}

func ConvertEntitySupportTicketInsertToPb(ticket entity.SupportTicketInsert) *pb_common.SupportTicketInsert {
	return &pb_common.SupportTicketInsert{
		Topic:          ticket.Topic,
		Subject:        ticket.Subject,
		Civility:       ticket.Civility,
		Email:          ticket.Email,
		FirstName:      ticket.FirstName,
		LastName:       ticket.LastName,
		OrderReference: ticket.OrderReference,
		Notes:          ticket.Notes,
	}
}

func ConvertEntitySupportTicketsToPb(tickets []entity.SupportTicket) []*pb_common.SupportTicket {
	pbTickets := make([]*pb_common.SupportTicket, len(tickets))
	for i, ticket := range tickets {
		pbTickets[i] = ConvertEntitySupportTicketToPb(ticket)
	}
	return pbTickets
}

func ConvertPbSupportTicketInsertToEntity(ticket *pb_common.SupportTicketInsert) entity.SupportTicketInsert {
	return entity.SupportTicketInsert{
		Topic:          ticket.Topic,
		Subject:        ticket.Subject,
		Civility:       ticket.Civility,
		Email:          ticket.Email,
		FirstName:      ticket.FirstName,
		LastName:       ticket.LastName,
		OrderReference: ticket.OrderReference,
		Notes:          ticket.Notes,
	}
}
