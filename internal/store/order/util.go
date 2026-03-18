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

func getProductPrice(prd *entity.Product, currency string) (decimal.Decimal, error) {
	for _, price := range prd.Prices {
		if price.Currency == currency {
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

func generateOrderReference() string {
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
			panic(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return prefix + string(b)
}
