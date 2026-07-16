package order

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// requirePositivePrice enforces the order-time positive-price invariant (PR5-D / problem 044): every
// order line — standard (priced from the catalogue) or custom (priced from admin-supplied amounts) —
// must have a strictly positive unit price. Zero and negative are both rejected: a silent free/comp
// or negative sale is not supported here (a genuine comp/gift order needs a separate typed flow with
// reason, audit and permission). It returns a *entity.ValidationError so the API layer maps it to
// InvalidArgument. This is the single shared invariant the standard and custom paths both call.
func requirePositivePrice(productID int, price decimal.Decimal) *entity.ValidationError {
	if price.LessThanOrEqual(decimal.Zero) {
		return &entity.ValidationError{Message: fmt.Sprintf("product %d: item price must be positive (got %s)", productID, price.String())}
	}
	return nil
}

// getProductPrice resolves a product's price in the order currency. It enforces the order-time
// pricing invariant (PR5-D): the currency must be present AND its price must be positive (via the
// shared requirePositivePrice helper). Product create/update already reject a missing or non-positive
// price (validateRequiredCurrencies), but that runs only at write time; re-checking here is defence
// in depth so a data anomaly (a stray zero price in the order currency) fails the order instead of
// silently selling at zero. The custom order path prices from admin-supplied amounts, but shares the
// same positivity invariant (requirePositivePrice) in validateOrderItemsStockForCustomOrder.
func getProductPrice(prd *entity.Colorway, currency string) (decimal.Decimal, error) {
	for _, price := range prd.Prices {
		// Stored currencies are uppercase; compare case-insensitively so a
		// lowercase/mixed-case client currency does not falsely miss the price.
		if strings.EqualFold(price.Currency, currency) {
			if verr := requirePositivePrice(prd.Id, price.Price); verr != nil {
				return decimal.Zero, verr
			}
			return price.Price, nil
		}
	}
	return decimal.Zero, fmt.Errorf("product %d does not have a price in currency %s", prd.Id, currency)
}

func getProductIdsFromItems(items []entity.OrderItemInsert) []int {
	seen := make(map[int]struct{}, len(items))
	ids := make([]int, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.ProductId]; !ok {
			seen[item.ProductId] = struct{}{}
			ids = append(ids, item.ProductId)
		}
	}
	return ids
}

func sanitizePhone(phone string) (string, error) {
	var builder strings.Builder
	for _, r := range phone {
		if unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	sanitized := builder.String()

	if len(sanitized) < 7 || len(sanitized) > 15 {
		return "", fmt.Errorf("phone number must be between 7 and 15 digits after sanitization, got %d digits", len(sanitized))
	}

	return sanitized, nil
}

func generateOrderReference() (string, error) {
	const (
		prefix   = "ORD-"
		length   = 7
		alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		base     = int64(len(alphabet))
	)
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(base))
		if err != nil {
			return "", fmt.Errorf("can't generate order reference: %w", err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return prefix + string(b), nil
}
