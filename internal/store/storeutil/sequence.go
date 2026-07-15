package storeutil

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// AllocateModelNo mints the next model number from the shared model_no_seq counter (SKU redesign
// task 03). A single sequence hands numbers to both styles (tech_card) and standalone products so
// their namespaces can never collide. refType/refID are provenance only ("tech_card"|"product").
// Must run inside the caller's transaction so the allocation commits or rolls back with the row it
// numbers. The returned number is the counter's AUTO_INCREMENT id.
func AllocateModelNo(ctx context.Context, conn dependency.DB, refType string, refID int) (int, error) {
	n, err := ExecNamedLastId(ctx, conn,
		`INSERT INTO model_no_seq (ref_type, ref_id) VALUES (:t, :id)`,
		map[string]any{"t": refType, "id": refID})
	if err != nil {
		return 0, fmt.Errorf("allocate model_no: %w", err)
	}
	return n, nil
}
