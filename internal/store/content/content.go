// Package content implements hero, archive, and media management.
package content

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// RepFunc returns the repository bound to the current execution context — the
// root store outside a transaction, or the tx-scoped store inside one. It lets
// read paths (e.g. archive item resolution) reach sibling stores (products,
// media) on the active connection instead of opening a nested transaction.
type RepFunc func() dependency.Repository

// Store implements dependency.Hero, dependency.Archive, and dependency.Media.
type Store struct {
	storeutil.Base
	txFunc  TxFunc
	repFunc RepFunc
}

// New creates a new content store.
func New(base storeutil.Base, txFunc TxFunc, repFunc RepFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, repFunc: repFunc}
}
