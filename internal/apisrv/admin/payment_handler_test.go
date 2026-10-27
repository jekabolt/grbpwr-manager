package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetPaymentHandlerRefusesDisabledStripeProcessor(t *testing.T) {
	_, err := (&Server{}).getPaymentHandler(context.Background(), entity.CARD)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status = %v, want %v (err=%v)", status.Code(err), codes.FailedPrecondition, err)
	}
}
