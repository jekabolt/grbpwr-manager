// Package storeutil provides shared types and helper functions for store sub-packages.
// It exists to break circular imports between the root store package and domain sub-packages.
package storeutil

import (
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// Base provides shared database access for all sub-stores.
// Domain sub-packages embed this struct to get DB access and time.
type Base struct {
	DB  dependency.DB
	Now func() time.Time
}
