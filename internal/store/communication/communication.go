// Package communication implements mail queue and subscriber management.
package communication

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc runs f inside a transaction, handing it the full repository.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Mail and dependency.Subscribers.
type Store struct {
	storeutil.Base
	// txFunc runs the outbox claim (SELECT … FOR UPDATE SKIP LOCKED, then the lease UPDATE) in one
	// transaction.
	txFunc TxFunc
}

// New creates a new communication store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}
