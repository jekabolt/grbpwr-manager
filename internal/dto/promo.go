package dto

import (
	"time"

	"github.com/shopspring/decimal"
)

type PromoCode struct {
	ID           int64
	Code         string
	FreeShipping bool
	Sale         decimal.Decimal
	Expiration   time.Time
	Allowed      bool
}
