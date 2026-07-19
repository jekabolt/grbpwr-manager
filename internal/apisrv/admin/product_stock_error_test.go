package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestColorwayTransitionErrorClassification locks A2: the →ACTIVE completeness gates (missing required
// currency, no thumbnail, unmet publish preconditions) are client-fixable and must surface as
// FailedPrecondition (HTTP 400), NOT Internal (HTTP 500). They are typed by entity.ErrColorwayNotSellable.
func TestColorwayTransitionErrorClassification(t *testing.T) {
	ctx := context.Background()

	// The store wraps every →ACTIVE completeness failure as `%w: <detail>` of ErrColorwayNotSellable.
	notSellable := fmt.Errorf("%w: cannot activate colourway 5: missing required currencies: PLN", entity.ErrColorwayNotSellable)

	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"missing required currency (not sellable)", notSellable, codes.FailedPrecondition},
		{"no thumbnail (not sellable)", fmt.Errorf("%w: cannot activate colourway 5: no thumbnail is set", entity.ErrColorwayNotSellable), codes.FailedPrecondition},
		{"not a draft", entity.ErrColorwayNotDraft, codes.FailedPrecondition},
		{"frozen siblings", entity.ErrStyleFrozenSiblings, codes.FailedPrecondition},
		{"invalid lifecycle transition", errors.New("lifecycle transition \"publish\" is not allowed from active"), codes.FailedPrecondition},
		{"missing colourway", sql.ErrNoRows, codes.NotFound},
		{"infrastructure failure", errors.New("dial tcp: connection reset"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.Code(colorwayTransitionError(ctx, "publish", 5, tc.err))
			require.Equal(t, tc.want, got)
		})
	}

	// The client-facing message must retain the actionable detail (which currency is missing).
	msg := status.Convert(colorwayTransitionError(ctx, "publish", 5, notSellable)).Message()
	require.Contains(t, msg, "missing required currencies: PLN")
}

// TestUpdateVariantStockUnpublishedColorway locks C9: setting stock on a colourway whose base SKU is
// not yet minted (i.e. still a DRAFT) returns InvalidArgument with an actionable "publish first"
// message, instead of a bare Internal 500. The store signals this as an *entity.ValidationError.
func TestUpdateVariantStockUnpublishedColorway(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	repo.EXPECT().Products().Return(products)

	products.EXPECT().GetVariantByID(mock.Anything, 5).
		Return(entity.Variant{Id: 5, ProductId: 10, SizeId: 2, Status: uint8(entity.VariantStatusActive)}, nil)

	products.EXPECT().
		UpdateProductSizeStockWithHistory(mock.Anything, 10, 2, entity.StockUpdateModeSet, 3, "stock_count", "").
		Return(decimal.Zero, decimal.Zero, &entity.ValidationError{
			Message:  "colourway 10 is not published yet — its base SKU is assigned on publish, so stock can only be set after publishing",
			Field:    "product_id",
			Reason:   "colorway_not_published",
			HowToFix: "publish the colourway first (PublishColorway), then set variant stock",
		})

	s := &Server{repo: repo}
	_, err := s.UpdateVariantStock(context.Background(), &pb_admin.UpdateVariantStockRequest{
		VariantId: 5,
		Quantity:  3,
		Mode:      pb_common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET,
		Reason:    pb_common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
	})

	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, strings.ToLower(status.Convert(err).Message()), "not published")
}
