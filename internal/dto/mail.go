package dto

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

type OrderConfirmed struct {
	Name            string
	OrderUUID       string
	OrderDate       time.Time
	TotalAmount     string
	PaymentMethod   string
	PaymentCurrency string
}

type OrderCancelled struct {
	Name             string
	OrderID          string
	CancellationDate string
	RefundAmount     float64
	PaymentMethod    string
	PaymentCurrency  string
}

type OrderShipment struct {
	Name           string
	OrderID        string
	ShippingDate   string
	TotalAmount    float64
	TrackingNumber string
	TrackingURL    string
}
type PromoCodeDetails struct {
	PromoCode       string
	HasFreeShipping bool
	DiscountAmount  int
	ExpirationDate  string
}

func ResendSendEmailRequestToEntity(mr *resend.SendEmailRequest, to string) *entity.SendEmailRequest {
	return &entity.SendEmailRequest{
		From:    mr.From,
		To:      to,
		Html:    *mr.Html,
		Subject: mr.Subject,
		ReplyTo: *mr.ReplyTo,
	}
}

func EntitySendEmailRequestToResend(mr *entity.SendEmailRequest) (*resend.SendEmailRequest, error) {
	if mr.To == "" {
		return nil, fmt.Errorf("mail req 'to' is empty")
	}
	return &resend.SendEmailRequest{
		From:    mr.From,
		To:      []string{mr.To},
		Html:    &mr.Html,
		Subject: mr.Subject,
		ReplyTo: &mr.ReplyTo,
	}, nil
}
