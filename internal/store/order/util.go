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

// getProductPrice resolves a product's price in the order currency. It enforces the order-time
// pricing invariant (PR5-D): the currency must be present AND its price must be positive. Product
// create/update already reject a missing or non-positive price (validateRequiredCurrencies), but
// that runs only at write time; re-checking here is defence in depth so a data anomaly (a stray
// zero price in the order currency) fails the order instead of silently selling at zero. The custom
// order path intentionally does not use this — it prices from admin-supplied amounts.
func getProductPrice(prd *entity.Product, currency string) (decimal.Decimal, error) {
	for _, price := range prd.Prices {
		// Stored currencies are uppercase; compare case-insensitively so a
		// lowercase/mixed-case client currency does not falsely miss the price.
		if strings.EqualFold(price.Currency, currency) {
			if price.Price.LessThanOrEqual(decimal.Zero) {
				return decimal.Zero, fmt.Errorf("product %d has a non-positive price in currency %s", prd.Id, currency)
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
