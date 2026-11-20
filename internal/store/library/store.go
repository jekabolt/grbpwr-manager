// Package library is the unified library store: media rows and library files listed as one, plus the
// shared folder tree (library_folder, 0482). Neither table moves; this package reads both and writes
// only folder pointers (media.lib_folder_id, library_file.folder_id) and folders.
//
// The files branch always ANDs fileslibrary's Viewer.Where; the media branch has no row ACL. Which
// branches run is decided by the handler (sections + team features) and passed in as
// entity.LibraryBranches; a branch that is off is left out of the SQL entirely.
package library

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Library.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new library store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

var _ dependency.Library = (*Store)(nil)
