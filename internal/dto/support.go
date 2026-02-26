package dto

import (
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertEntitySupportTicketToPb(ticket entity.SupportTicket) *pb_common.SupportTicket {
	return &pb_common.SupportTicket{
		Id:                  int32(ticket.Id),
		CreatedAt:           timestamppb.New(ticket.CreatedAt),
		UpdatedAt:           timestamppb.New(ticket.UpdatedAt),
		Status:              ConvertEntitySupportTicketStatusToPb(ticket.Status),
		ResolvedAt:          timestamppb.New(ticket.ResolvedAt.Time),
		SupportTicketInsert: ConvertEntitySupportTicketInsertToPb(ticket.SupportTicketInsert),
		CaseNumber:          ticket.CaseNumber,
		Category:            ticket.Category,
		Priority:            ConvertEntitySupportTicketPriorityToPb(ticket.Priority),
		InternalNotes:       ticket.InternalNotes,
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
		Category:       ticket.Category,
		Priority:       ConvertEntitySupportTicketPriorityToPb(ticket.Priority),
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
		Category:       ticket.Category,
		Priority:       ConvertPbSupportTicketPriorityToEntity(ticket.Priority),
	}
}

func ConvertEntitySupportTicketStatusToPb(status entity.SupportTicketStatus) pb_common.SupportTicketStatus {
	switch status {
	case entity.SupportStatusSubmitted:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_SUBMITTED
	case entity.SupportStatusInProgress:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_IN_PROGRESS
	case entity.SupportStatusWaitingCustomer:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_WAITING_CUSTOMER
	case entity.SupportStatusResolved:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_RESOLVED
	case entity.SupportStatusClosed:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_CLOSED
	default:
		return pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_UNKNOWN
	}
}

func ConvertPbSupportTicketStatusToEntity(status pb_common.SupportTicketStatus) entity.SupportTicketStatus {
	switch status {
	case pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_SUBMITTED:
		return entity.SupportStatusSubmitted
	case pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_IN_PROGRESS:
		return entity.SupportStatusInProgress
	case pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_WAITING_CUSTOMER:
		return entity.SupportStatusWaitingCustomer
	case pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_RESOLVED:
		return entity.SupportStatusResolved
	case pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_CLOSED:
		return entity.SupportStatusClosed
	default:
		return entity.SupportStatusSubmitted
	}
}

func ConvertEntitySupportTicketPriorityToPb(priority entity.SupportTicketPriority) pb_common.SupportTicketPriority {
	switch priority {
	case entity.PriorityLow:
		return pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_LOW
	case entity.PriorityMedium:
		return pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_MEDIUM
	case entity.PriorityHigh:
		return pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_HIGH
	case entity.PriorityUrgent:
		return pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_URGENT
	default:
		return pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_MEDIUM
	}
}

func ConvertPbSupportTicketPriorityToEntity(priority pb_common.SupportTicketPriority) entity.SupportTicketPriority {
	switch priority {
	case pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_LOW:
		return entity.PriorityLow
	case pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_MEDIUM:
		return entity.PriorityMedium
	case pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_HIGH:
		return entity.PriorityHigh
	case pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_URGENT:
		return entity.PriorityUrgent
	default:
		return entity.PriorityMedium
	}
}

func ConvertPbSupportTicketFiltersToEntity(
	status *pb_common.SupportTicketStatus,
	email, orderRef, topic, category string,
	priority *pb_common.SupportTicketPriority,
	dateFrom, dateTo string,
) entity.SupportTicketFilters {
	filters := entity.SupportTicketFilters{
		Email:          email,
		OrderReference: orderRef,
		Topic:          topic,
		Category:       category,
	}

	if status != nil && *status != pb_common.SupportTicketStatus_SUPPORT_TICKET_STATUS_UNKNOWN {
		entityStatus := ConvertPbSupportTicketStatusToEntity(*status)
		filters.Status = &entityStatus
	}

	if priority != nil && *priority != pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_UNKNOWN {
		entityPriority := ConvertPbSupportTicketPriorityToEntity(*priority)
		filters.Priority = &entityPriority
	}

	if dateFrom != "" {
		if t, err := time.Parse(time.RFC3339, dateFrom); err == nil {
			filters.DateFrom = &t
		}
	}

	if dateTo != "" {
		if t, err := time.Parse(time.RFC3339, dateTo); err == nil {
			filters.DateTo = &t
		}
	}

	return filters
}
