package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф6.2 — THE STORE HALF OF «CREATE COLOURWAYS FROM ARCHIVE».
//
// The action itself lives in the API layer, and it has to: it creates PRODUCTS (through the same
// CreateColorway the panel uses), writes recipes (through UpdateColorwayRecipe) and reports what it
// could not do. Those three already exist, and re-implementing any of them down here would be a
// second opinion about what a colourway is.
//
// What does NOT exist anywhere is the last piece of the colourway — the piece→cloth mapping
// (tech_card_piece_material). Its only writer is the CARD SAVE, which full-replaces it from the
// payload of the whole card; there is no way to add one colourway's mapping without re-saving the
// card, which an import must not do. Hence this file, and hence exactly one write in it.
//
// The other method here is the report stamp. It is separate from the commit's own
// (archiveImportStampResultQuery, import.go) because they are different sentences: the commit
// stamps a report AND the card id it just created, in one statement, guarded by nothing; this one
// rewrites the report of an import that is already committed and MUST NOT be able to touch
// tech_card_id — a statement that could is a statement that will, on the day somebody reuses it.
// ─────────────────────────────────────────────────────────────────────────────

const (
	importColorwayPiecesQuery = `SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :card`

	importColorwayBomQuery = `SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :card`

	importColorwayOwnerQuery = `SELECT style_id FROM product WHERE id = :colorway`

	importColorwayPieceMaterialClearQuery = `
		DELETE FROM tech_card_piece_material
		WHERE colorway_id = :colorway
		  AND piece_id IN (SELECT id FROM tech_card_piece WHERE tech_card_id = :card)`

	importColorwayPieceMaterialInsertQuery = `
		INSERT INTO tech_card_piece_material
			(piece_id, colorway_id, bom_item_id, fusing_bom_item_id, note, display_order)
		VALUES (:piece_id, :colorway_id, :bom_item_id, :fusing_bom_item_id, :note, :display_order)`

	importColorwayReportStampQuery = `
		UPDATE tech_card_import
		SET report = :report
		WHERE import_id = :import_id AND status = :committed`
)

// ApplyImportedColorwayPieceMaterials writes ONE colourway's piece→cloth mapping onto ONE card,
// resolving every reference by the stable line keys the archive carried verbatim.
//
// FULL REPLACE FOR THIS COLOURWAY AND NOBODY ELSE. The clear is scoped by colorway_id AND by the
// card's own pieces, so re-running it cannot touch a neighbouring colour — and the card save's own
// clear (clearTechCardPieceMaterials, materials.go) is deliberately NOT reused: that one wipes the
// mapping of every colourway of the card, which is correct when the card save is about to re-write
// all of them and catastrophic here.
//
// A KEY THAT NAMES NOTHING IS AN ERROR, not a silently dropped row. The caller has the card's key
// sets in hand and reports what it filtered out; by the time a row reaches here it has been
// promised to exist, and a store that quietly wrote fewer rows than it was given would make that
// promise unfalsifiable.
//
// The colourway is verified to belong to THIS card first. Nothing upstream can pass another card's
// colourway today, but the mapping's own UNIQUE is (piece_id, colorway_id) — it would happily hold
// a stranger — and the FK is to product(id), which does not know about tech_card_id either.
func (s *Store) ApplyImportedColorwayPieceMaterials(ctx context.Context, techCardID, colorwayID int,
	rows []entity.TechCardArchivePieceMaterial) error {
	if techCardID <= 0 || colorwayID <= 0 {
		return fmt.Errorf("apply imported piece materials: tech card id and colourway id are required (got %d / %d)",
			techCardID, colorwayID)
	}

	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		owner, err := storeutil.QueryNamedOne[struct {
			StyleID int `db:"style_id"`
		}](ctx, rep.DB(), importColorwayOwnerQuery, map[string]any{"colorway": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d owner: %w", colorwayID, err) // sql.ErrNoRows -> NotFound upstream
		}
		if owner.StyleID != techCardID {
			return fmt.Errorf("colourway %d belongs to style %d, not to tech card %d",
				colorwayID, owner.StyleID, techCardID)
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), importColorwayPieceMaterialClearQuery,
			map[string]any{"colorway": colorwayID, "card": techCardID}); err != nil {
			return fmt.Errorf("clear colourway %d piece materials on card %d: %w", colorwayID, techCardID, err)
		}
		if len(rows) == 0 {
			return nil
		}

		pieceByKey, err := importColorwayKeyIndex(ctx, rep.DB(), importColorwayPiecesQuery, techCardID, "cut-piece")
		if err != nil {
			return err
		}
		bomByKey, err := importColorwayKeyIndex(ctx, rep.DB(), importColorwayBomQuery, techCardID, "BOM line")
		if err != nil {
			return err
		}

		for i := range rows {
			r := &rows[i]
			pieceID, ok := pieceByKey[r.PieceLineKey]
			if !ok {
				return fmt.Errorf("piece material %d: tech card %d has no cut-piece %q",
					i, techCardID, r.PieceLineKey)
			}
			bomID, err := importColorwayBomRef(bomByKey, r.BomLineKey, techCardID, i, "bom_line_key")
			if err != nil {
				return err
			}
			fusingID, err := importColorwayBomRef(bomByKey, r.FusingBomLineKey, techCardID, i, "fusing_bom_line_key")
			if err != nil {
				return err
			}
			// bom_item_index / fusing_bom_item_index are NOT written: they are the legacy positional
			// refs, they index the SUBMITTED bom_items of a card save, and this write has no such
			// list. A number carried over from another instance's payload would point at whatever
			// line happens to sit at that position here.
			if err := storeutil.ExecNamed(ctx, rep.DB(), importColorwayPieceMaterialInsertQuery,
				map[string]any{
					"piece_id":           pieceID,
					"colorway_id":        colorwayID,
					"bom_item_id":        bomID,
					"fusing_bom_item_id": fusingID,
					"note":               importColorwayNote(r.Note),
					"display_order":      i,
				}); err != nil {
				return fmt.Errorf("insert piece material %d of colourway %d: %w", i, colorwayID, err)
			}
		}
		return nil
	})
}

// importColorwayKeyIndex loads one child table's line_key → id map for a card. `what` names the
// table in the error, because both callers fail the same way and the message is the only thing that
// tells them apart.
func importColorwayKeyIndex(ctx context.Context, db dependency.DB, query string, techCardID int, what string) (map[string]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, query, map[string]any{"card": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load tech card %d %s keys: %w", techCardID, what, err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.LineKey == "" {
			continue
		}
		out[r.LineKey] = r.Id
	}
	return out, nil
}

// importColorwayBomRef resolves an OPTIONAL BOM reference: an empty key is SQL NULL (the mapping
// legitimately names no fusing, and a fabric-less row is a note about the piece), a key that names
// nothing is an error.
func importColorwayBomRef(byKey map[string]int, key string, techCardID, index int, field string) (sql.NullInt64, error) {
	if key == "" {
		return sql.NullInt64{}, nil
	}
	id, ok := byKey[key]
	if !ok {
		return sql.NullInt64{}, fmt.Errorf("piece material %d: tech card %d has no BOM line %q (%s)",
			index, techCardID, key, field)
	}
	return sql.NullInt64{Int64: int64(id), Valid: true}, nil
}

func importColorwayNote(note string) sql.NullString {
	return sql.NullString{String: note, Valid: note != ""}
}

// StampTechCardImportReport replaces the stored report of an import that has ALREADY been committed.
//
// Guarded on status='committed' and on nothing else: an 'uploaded' row has no card yet and no
// report worth revising, and an 'expired' one describes an archive whose bytes are gone. A guard
// that matched them too would let a second, unrelated screen rewrite the record of an import that
// never happened. No rows matched is not an error here — the caller read the row first, and the two
// reads racing is a lost update of a report, not a corruption of one.
//
// The bytes are checked for being JSON before the statement runs. The column is JSON and MySQL
// would otherwise answer with a bare 3140 from the middle of an UPDATE, naming neither the import
// nor the caller.
func (s *Store) StampTechCardImportReport(ctx context.Context, importID string, report []byte) error {
	if importID == "" {
		return fmt.Errorf("can't stamp a tech card import report: import id is required")
	}
	if len(report) == 0 {
		return fmt.Errorf("can't stamp tech card import %s: the report is empty", importID)
	}
	if !json.Valid(report) {
		return fmt.Errorf("can't stamp tech card import %s: the report is not JSON", importID)
	}
	if err := storeutil.ExecNamed(ctx, s.DB, importColorwayReportStampQuery, map[string]any{
		"import_id": importID,
		"report":    string(report),
		"committed": entity.TechCardImportStatusCommitted,
	}); err != nil {
		return fmt.Errorf("stamp tech card import %s report: %w", importID, err)
	}
	return nil
}
