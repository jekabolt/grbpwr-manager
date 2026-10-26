package app

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	stripepayment "github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
)

func TestNewStripeProcessorAllowsExplicitlyDisabledLiveProcessor(t *testing.T) {
	processor, err := newStripeProcessor(context.Background(), &stripepayment.Config{Enabled: false}, nil, nil, entity.CARD)
	if err != nil {
		t.Fatalf("disabled processor returned error: %v", err)
	}
	if processor != nil {
		t.Fatalf("disabled processor = %#v, want nil", processor)
	}
}

func TestNewStripeProcessorStillRequiresKeyWhenEnabled(t *testing.T) {
	processor, err := newStripeProcessor(context.Background(), &stripepayment.Config{Enabled: true}, nil, nil, entity.CARD)
	if err == nil {
		t.Fatalf("enabled processor without key unexpectedly succeeded: %#v", processor)
	}
}
