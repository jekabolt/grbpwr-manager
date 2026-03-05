package store

import (
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// bqCacheStore combines read and write implementations for BQCacheStore interface.
type bqCacheStore struct {
	*bqCacheStoreRead
	*bqCacheStoreWrite
}

// BQCache returns an object implementing the BQCacheStore interface.
func (ms *MYSQLStore) BQCache() dependency.BQCacheStore {
	return &bqCacheStore{
		bqCacheStoreRead:  &bqCacheStoreRead{MYSQLStore: ms},
		bqCacheStoreWrite: &bqCacheStoreWrite{MYSQLStore: ms},
	}
}
