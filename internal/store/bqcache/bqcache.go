// Package bqcache implements BigQuery precomputed analytics cache read/write operations.
package bqcache

import (
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.BQCacheStore by composing read and write sub-stores.
type Store struct {
	*ReadStore
	*WriteStore
}

// New creates a new BQ cache store with the given base.
func New(base storeutil.Base) *Store {
	return &Store{
		ReadStore:  &ReadStore{Base: base},
		WriteStore: &WriteStore{Base: base},
	}
}
