// Package communication implements mail queue and subscriber management.
package communication

import (
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.Mail and dependency.Subscribers.
type Store struct {
	storeutil.Base
}

// New creates a new communication store.
func New(base storeutil.Base) *Store {
	return &Store{Base: base}
}
