package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE UPLOAD HALF OF tech_card_import (migration 0336).
//
// An import is TWO CALLS, not one: the archive is uploaded and read (this row), a human looks at
// the report, and only then does a second call create the card. The row is what carries the state
// between them, because there are several instances of this service and the second call legally
// lands on a different one than the first — state in process memory would mean "upload it again",
// unpredictably and only under load.
//
// This file writes ONLY the first call's half: the columns known at upload. tech_card_id, report,
// status transitions and the timestamps of the commit belong to the write path (Ф3.2, import.go),
// which is why they are absent here rather than defaulted to something reassuring.
// ─────────────────────────────────────────────────────────────────────────────

// CreateTechCardImportRow records one uploaded archive: where its bytes are, what its manifest said,
// and the colourway payload a much later step will need after the bucket object is long gone.
//
// archiveManifest MUST BE THE ARCHIVE'S OWN BYTES — manifest.json verbatim, never a re-marshal of a
// parsed struct. An archive of a newer 1.x MINOR carries fields this server has no member for;
// encoding/json drops them without a word, and the row would then show a SHORTER manifest than the
// one that arrived, under the column comment "что было в ZIP на загрузке". The journal would be
// lying quietly and permanently, so the store refuses empty bytes and checks that what it is handed
// is at least JSON — the column is JSON and MySQL would otherwise answer with a bare 3140.
//
// colorwaysPayload is nil when the archive carried no colorways.json, and NULL is the honest value
// for that: colourways are products, an import creates none, and "no colourways travelled" must not
// read as "an empty list travelled".
func (s *Store) CreateTechCardImportRow(ctx context.Context, importID, objectKey string, archiveManifest, colorwaysPayload []byte, importedBy string) error {
	if importID == "" {
		return fmt.Errorf("can't record a tech card import: import id is required")
	}
	if objectKey == "" {
		return fmt.Errorf("can't record tech card import %s: object key is required", importID)
	}
	if len(archiveManifest) == 0 {
		return fmt.Errorf("can't record tech card import %s: the archive manifest is required", importID)
	}
	if !json.Valid(archiveManifest) {
		return fmt.Errorf("can't record tech card import %s: the archive manifest is not JSON", importID)
	}
	// A colourway payload that does not parse is not written as NULL and not written as text: the
	// column is JSON, and a silent downgrade to "no colourways travelled" is exactly the lie the
	// later apply step would inherit.
	var colorways any
	if len(colorwaysPayload) > 0 {
		if !json.Valid(colorwaysPayload) {
			return fmt.Errorf("can't record tech card import %s: the colourway payload is not JSON", importID)
		}
		colorways = string(colorwaysPayload)
	}

	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO tech_card_import
			(import_id, object_key, archive_manifest, colorways_payload, status, imported_by)
		VALUES
			(:import_id, :object_key, :archive_manifest, :colorways_payload, :status, :imported_by)`,
		map[string]any{
			"import_id":         importID,
			"object_key":        objectKey,
			"archive_manifest":  string(archiveManifest),
			"colorways_payload": colorways,
			// entity.TechCardImportStatusUploaded, not a literal: the commit path CLAIMS this row
			// by matching on that exact word, and two spellings of one status is a row nobody can
			// pick up. The dictionary lives in Go because 0336 deliberately declined a CHECK.
			"status":      entity.TechCardImportStatusUploaded,
			"imported_by": importedBy,
		}); err != nil {
		return fmt.Errorf("record tech card import %s: %w", importID, err)
	}
	return nil
}

// expireStaleTechCardImportsQuery is named rather than inlined so the guard in import_row_test.go
// asserts against THE statement that runs, not a copy of it that can drift away from it.
const expireStaleTechCardImportsQuery = `
		UPDATE tech_card_import
		SET status = :expired
		WHERE status = :uploaded AND created_at < :olderThan`

// ExpireStaleTechCardImports marks every still-uncommitted upload older than olderThan as expired
// and returns how many rows it moved. The background cleanup (Ф5.1) calls it on the same tick that
// deletes the aged-out bucket objects, with the SAME cutoff, because the two halves describe one
// fact: after the retention window the archive's bytes are gone, so the row can no longer become a
// card and must stop claiming it might.
//
// THE CUTOFF ARRIVES AS AN ARGUMENT RATHER THAN AS `NOW() - INTERVAL 7 DAY` ON PURPOSE. The
// retention period is one number (bucket.ArchiveRetention, an owner decision) and it already
// governs the object half; spelling it a second time in SQL would let the two halves drift apart
// silently — and the direction they would drift matters, because a row that still says "uploaded"
// after its object is deleted is an operator pressing commit onto a 404. Passing the instant down
// is also the house convention for age-based sweeps (see GetStuckPlacedOrders).
//
// ONLY 'uploaded' MOVES. 'committed' rows are a card's provenance and are read long after the
// object expires; 'failed' rows are the record of a commit that did not survive its transaction,
// and repainting either as "expired" would erase what actually happened. The status is matched by
// the entity constant rather than a literal for the same reason the insert does: two spellings of
// one status is a row nobody picks up.
func (s *Store) ExpireStaleTechCardImports(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := storeutil.ExecNamedRows(ctx, s.DB, expireStaleTechCardImportsQuery,
		map[string]any{
			"expired":   entity.TechCardImportStatusExpired,
			"uploaded":  entity.TechCardImportStatusUploaded,
			"olderThan": olderThan,
		})
	if err != nil {
		return 0, fmt.Errorf("expire stale tech card imports older than %s: %w", olderThan, err)
	}
	return n, nil
}
