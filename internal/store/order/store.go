// Package order implements the dependency.Order interface for order management.
package order

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ErrOrderItemsUpdated is returned when order items were validated and updated
// (e.g. prices changed, items unavailable). Caller should NOT cancel the order.
var ErrOrderItemsUpdated = errors.New("order items are not valid and were updated")

// errPaymentRecordNotFound indicates the payment record for the order was not found (0 rows updated).
var errPaymentRecordNotFound = errors.New("payment record not found for order")

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// RepFunc returns the current repository (root store or tx store).
// Used when the order store needs cross-domain access (e.g. Products().ReduceStock).
type RepFunc func() dependency.Repository

// Store implements dependency.Order.
type Store struct {
	storeutil.Base
	txFunc  TxFunc
	repFunc RepFunc
}

// New creates a new order store.
func New(base storeutil.Base, txFunc TxFunc, repFunc RepFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, repFunc: repFunc}
}
