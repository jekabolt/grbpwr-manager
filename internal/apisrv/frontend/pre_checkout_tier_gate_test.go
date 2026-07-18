package frontend

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireTierLockedField asserts err is an InvalidArgument carrying an items/tier_locked field violation.
func requireTierLockedField(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "pre-checkout tier block must be InvalidArgument")
	st, ok := status.FromError(err)
	require.True(t, ok)
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, fv := range br.FieldViolations {
				if fv.Field == "items" && strings.Contains(fv.Description, "tier_locked") {
					return
				}
			}
		}
	}
	t.Fatalf("expected an items/tier_locked BadRequest field violation, got: %v", err)
}

// TestPreCheckoutTierGate proves the pre-checkout mirror of the purchase block (enforceBuyerTierAccess,
// order_pre_checkout.go) rejects a cart containing a min_tier>=1 item for a non-qualifying buyer BEFORE
// any PaymentIntent is created. In ValidateOrderItemsInsert the gate is invoked before the Stripe
// PaymentIntent block, so an error here means the handler returns without ever issuing a client_secret —
// an ineligible buyer can never obtain one for a locked item. A qualifying buyer (or a normal-only cart)
// passes the gate, letting checkout proceed.
func TestPreCheckoutTierGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	st, db := tierGateBackends(t)

	token := fmt.Sprintf("%d%04d", time.Now().UnixNano(), rand.Intn(10000))
	mediaID := seedTestMedia(ctx, t, st)
	sizeID := seedTestSize(ctx, t, db)

	// P1: gated (min_tier=1). P0: normal (min_tier=0). ProductIds feed enforceBuyerTierAccess directly
	// (in the real flow they arrive resolved post stock-validation).
	p1ID, _, _ := seedTierProduct(ctx, t, db, mediaID, sizeID, "PG1-"+token, 1, false, "")
	p0ID, _, _ := seedTierProduct(ctx, t, db, mediaID, sizeID, "PG0-"+token, 0, false, "")

	// enforceBuyerTierAccess only touches s.repo; the other Server deps are unused here.
	s := &Server{repo: st}

	item := func(pid int) entity.OrderItem {
		return entity.OrderItem{OrderItemInsert: entity.OrderItemInsert{ProductId: pid, Quantity: decimal.NewFromInt(1)}}
	}

	// GUEST cart with a min_tier=1 item → rejected pre-checkout; no client_secret is ever reached.
	err := s.enforceBuyerTierAccess(ctx, []entity.OrderItem{item(p1ID)}, entity.TierCodeMember)
	requireTierLockedField(t, err)

	// MIXED guest cart (gated + normal) → rejected as a whole (the gated line blocks the cart).
	err = s.enforceBuyerTierAccess(ctx, []entity.OrderItem{item(p0ID), item(p1ID)}, entity.TierCodeMember)
	requireTierLockedField(t, err)

	// Qualifying buyer (plus=1) → gate passes, so checkout may proceed to a PaymentIntent.
	require.NoError(t, s.enforceBuyerTierAccess(ctx, []entity.OrderItem{item(p1ID)}, entity.TierCodePlus),
		"a qualifying buyer must pass the pre-checkout gate")

	// Normal-only cart for a guest → gate passes.
	require.NoError(t, s.enforceBuyerTierAccess(ctx, []entity.OrderItem{item(p0ID)}, entity.TierCodeMember),
		"a non-gated cart must pass the pre-checkout gate")
}
