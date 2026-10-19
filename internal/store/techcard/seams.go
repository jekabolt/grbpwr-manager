package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CONFIRMED SEAMS (tech_card_seam) — the store half.
//
// Same shape as the piece areas (0297), different payload: the client resolves geometry, the server
// stores the decision and owns the one fact that could be forged in the dangerous direction — «this
// seam was decided on the card's CURRENT patterns». THE CLIENT NEVER SUPPLIES THE FINGERPRINT: the
// store computes it from its own sheets + block→piece links of the seam's pieces' fabric scopes, and
// recomputes it on every read to say `stale`.
//
// KEYED, PARTIAL UPSERT — never a replace of the card's list: a reviewer decides one seam at a time,
// and two reviewers on one card must not clobber each other's rows.
//
// NO lock_version BUMP. The card row is taken FOR UPDATE (serialises with the card save, which holds
// the same row through its own optimistic UPDATE and may delete pieces) but left unchanged, so the
// form's UpdateTechCard(expected_lock_version) keeps working while somebody confirms a seam.

// seamRow is one tech_card_seam row as scanned. The sides are scanned as bytes, never as
// json.RawMessage (not a Scanner).
type seamRow struct {
	TechCardId        int           `db:"tech_card_id"`
	SeamKey           string        `db:"seam_key"`
	Status            string        `db:"status"`
	Kind              string        `db:"kind"`
	Direction         string        `db:"direction"`
	Source            string        `db:"source"`
	SideA             []byte        `db:"side_a"`
	SideB             []byte        `db:"side_b"`
	SizeId            sql.NullInt64 `db:"size_id"`
	AnchoredSize      string        `db:"anchored_size"`
	Note              string        `db:"note"`
	SourceFingerprint string        `db:"source_fingerprint"`
	CreatedBy         string        `db:"created_by"`
	CreatedAt         time.Time     `db:"created_at"`
	UpdatedBy         string        `db:"updated_by"`
	UpdatedAt         time.Time     `db:"updated_at"`
}

// GetTechCardSeams returns a card's seam decisions, staleness resolved against today's sources.
// A card with no rows is an empty list, not an error: «nobody has reviewed the seams yet».
func (s *Store) GetTechCardSeams(ctx context.Context, techCardID int) ([]entity.TechCardSeam, error) {
	return listTechCardSeams(ctx, s.DB, techCardID)
}

func listTechCardSeams(ctx context.Context, db dependency.DB, techCardID int) ([]entity.TechCardSeam, error) {
	rows, err := storeutil.QueryListNamed[seamRow](ctx, db, `
		SELECT tech_card_id, seam_key, status, kind, direction, source, side_a, side_b, size_id,
		       anchored_size, note, source_fingerprint, created_by, created_at, updated_by, updated_at
		FROM tech_card_seam
		WHERE tech_card_id = :id
		ORDER BY id`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load seams of tech card %d: %w", techCardID, err)
	}
	if len(rows) == 0 {
		return []entity.TechCardSeam{}, nil
	}
	src, err := loadSeamSources(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	out := make([]entity.TechCardSeam, 0, len(rows))
	for _, r := range rows {
		seam := entity.TechCardSeam{
			TechCardSeamInput: entity.TechCardSeamInput{
				SeamKey:      r.SeamKey,
				Status:       entity.TechCardSeamStatus(r.Status),
				Kind:         entity.TechCardSeamKind(r.Kind),
				Direction:    entity.TechCardSeamDirection(r.Direction),
				Source:       entity.TechCardSeamSource(r.Source),
				AnchoredSize: r.AnchoredSize,
				Note:         r.Note,
			},
			TechCardId:        r.TechCardId,
			SizeId:            r.SizeId,
			SourceFingerprint: r.SourceFingerprint,
			CreatedBy:         r.CreatedBy,
			CreatedAt:         r.CreatedAt,
			UpdatedBy:         r.UpdatedBy,
			UpdatedAt:         r.UpdatedAt,
		}
		// A side that does not decode is a corrupted row, not a reason to fail the whole card read
		// (this runs inside GetTechCard). It comes back with an empty side and stale = true, so the
		// screen offers to re-confirm or delete it instead of applying nothing silently.
		errA := json.Unmarshal(r.SideA, &seam.SideA)
		errB := json.Unmarshal(r.SideB, &seam.SideB)
		seam.Stale = errA != nil || errB != nil ||
			src.fingerprint(seam.PieceLineKeys()) != r.SourceFingerprint
		out = append(out, seam)
	}
	return out, nil
}

// seamSources is a card's fabric-scope sources loaded ONCE per call, with fingerprints memoised per
// distinct piece set (a card has dozens of seams over a handful of piece sets).
type seamSources struct {
	sheets map[string][]entity.PatternSheetRef
	blocks map[string][]entity.PieceAreaBlockRef
	memo   map[string]string
}

func loadSeamSources(ctx context.Context, db dependency.DB, techCardID int) (*seamSources, error) {
	// ONE set of fabric lines for both halves — the same rule SaveTechCardPieceAreas follows: sheets
	// and links must resolve into one fabric identity, or a purpose edit between two reads would put
	// them under different keys inside one fingerprint.
	lines, err := loadRollGoodsLines(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	sheets, err := scopeSheetRefs(ctx, db, techCardID, lines)
	if err != nil {
		return nil, err
	}
	blocks, err := scopeBlockRefs(ctx, db, techCardID, lines)
	if err != nil {
		return nil, err
	}
	return &seamSources{sheets: sheets, blocks: blocks, memo: map[string]string{}}, nil
}

func (s *seamSources) fingerprint(pieceKeys []string) string {
	k := strings.Join(pieceKeys, "\x00")
	if fp, ok := s.memo[k]; ok {
		return fp
	}
	fp := entity.TechCardSeamSourceFingerprint(pieceKeys, s.sheets, s.blocks)
	s.memo[k] = fp
	return fp
}

// UpsertTechCardSeams inserts or replaces the named seams whole and returns the card's full list.
func (s *Store) UpsertTechCardSeams(ctx context.Context, in entity.TechCardSeamsWrite) ([]entity.TechCardSeam, int, error) {
	// Re-validated here and not only trusted from the handler: the store is the last gate before
	// the column's JSON format, and a second caller (seeder, import) must not bypass the shape rules.
	if err := entity.ValidateTechCardSeamsWrite(&in); err != nil {
		return nil, 0, err
	}
	var out []entity.TechCardSeam
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// THE CARD ROW LOCK — FIRST ACTION, BEFORE ANY READ. In REPEATABLE READ the first plain read
		// opens the snapshot; a read before the lock would judge the release state and the card's
		// pieces as they were BEFORE a concurrent card save that deletes a piece committed. Taken
		// FOR UPDATE and left unchanged: serialisation without a lock_version bump.
		if err := lockTechCardRow(ctx, db, in.TechCardId); err != nil {
			return err
		}
		// A released card is frozen content — the seams included, they feed the seam map print and
		// the doll.
		if err := storeutil.RequireMutableTechCard(ctx, db, in.TechCardId); err != nil {
			return err
		}
		// Every anchor must name a piece of THIS card. Matched case-insensitively (ULIDs; the column
		// collation is case-insensitive anyway).
		pieces, err := storeutil.QueryScalarListNamed[string](ctx, db, `
			SELECT line_key FROM tech_card_piece
			WHERE tech_card_id = :id AND line_key IS NOT NULL`, map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load piece keys of tech card %d: %w", in.TechCardId, err)
		}
		mine := make(map[string]bool, len(pieces))
		for _, p := range pieces {
			mine[strings.ToUpper(strings.TrimSpace(p))] = true
		}
		for i, seam := range in.Seams {
			for _, side := range []struct {
				name    string
				anchors []entity.TechCardSeamAnchor
			}{{"side_a", seam.SideA}, {"side_b", seam.SideB}} {
				for j, a := range side.anchors {
					if !mine[strings.ToUpper(a.PieceLineKey)] {
						return entity.NewFieldViolation(
							fmt.Sprintf("seams[%d].%s.parts[%d].piece_line_key", i, side.name, j),
							"piece_not_on_card", a.PieceLineKey,
							"the anchor names a piece this card does not have — reload the card; the piece may have been removed")
					}
				}
			}
		}
		// The per-card cap counts what the card WILL hold: existing rows not named here + this call.
		existing, err := storeutil.QueryScalarListNamed[string](ctx, db, `
			SELECT seam_key FROM tech_card_seam WHERE tech_card_id = :id`,
			map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load seam keys of tech card %d: %w", in.TechCardId, err)
		}
		named := make(map[string]bool, len(in.Seams))
		for _, seam := range in.Seams {
			named[seam.SeamKey] = true
		}
		total := len(in.Seams)
		for _, k := range existing {
			if !named[strings.ToUpper(k)] {
				total++
			}
		}
		if total > entity.TechCardSeamMaxRowsPerCard {
			return entity.NewFieldViolation("seams", "too_many", "",
				fmt.Sprintf("a card holds at most %d seams; delete orphan or obsolete rows first", entity.TechCardSeamMaxRowsPerCard))
		}
		src, err := loadSeamSources(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		for _, seam := range in.Seams {
			sideA, err := json.Marshal(seam.SideA)
			if err != nil {
				return fmt.Errorf("encode seam %s side_a: %w", seam.SeamKey, err)
			}
			sideB, err := json.Marshal(seam.SideB)
			if err != nil {
				return fmt.Errorf("encode seam %s side_b: %w", seam.SeamKey, err)
			}
			// size_id stays NULL (v1 has no per-size rows), so the key is (card, seam_key, 0).
			// created_* survive a replace; updated_* name the latest decision.
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_seam
					(tech_card_id, seam_key, status, kind, direction, source, side_a, side_b,
					 anchored_size, note, source_fingerprint, created_by, updated_by)
				VALUES
					(:tech_card_id, :seam_key, :status, :kind, :direction, :source, :side_a, :side_b,
					 :anchored_size, :note, :source_fingerprint, :by, :by)
				ON DUPLICATE KEY UPDATE
					status = VALUES(status), kind = VALUES(kind), direction = VALUES(direction),
					source = VALUES(source), side_a = VALUES(side_a), side_b = VALUES(side_b),
					anchored_size = VALUES(anchored_size), note = VALUES(note),
					source_fingerprint = VALUES(source_fingerprint), updated_by = VALUES(updated_by),
					updated_at = CURRENT_TIMESTAMP`,
				map[string]any{
					"tech_card_id":       in.TechCardId,
					"seam_key":           seam.SeamKey,
					"status":             string(seam.Status),
					"kind":               string(seam.Kind),
					"direction":          string(seam.Direction),
					"source":             string(seam.Source),
					"side_a":             string(sideA),
					"side_b":             string(sideB),
					"anchored_size":      seam.AnchoredSize,
					"note":               seam.Note,
					"source_fingerprint": src.fingerprint(seam.PieceLineKeys()),
					"by":                 in.By,
				}); err != nil {
				return fmt.Errorf("upsert seam %s of tech card %d: %w", seam.SeamKey, in.TechCardId, err)
			}
		}
		// The echo is read in the same transaction: the client re-syncs from what was committed,
		// not from its optimistic copy. Assigned, never appended — txFunc may retry this closure.
		list, err := listTechCardSeams(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, len(in.Seams), nil
}

// DeleteTechCardSeams removes seams by key and returns the card's full list and the removed count.
func (s *Store) DeleteTechCardSeams(ctx context.Context, techCardID int, seamKeys []string) ([]entity.TechCardSeam, int, error) {
	if techCardID <= 0 {
		return nil, 0, entity.NewFieldViolation("tech_card_id", "required", "", "")
	}
	if len(seamKeys) == 0 {
		return nil, 0, entity.NewFieldViolation("seam_keys", "empty", "", "name at least one seam_key")
	}
	if len(seamKeys) > entity.TechCardSeamMaxRowsPerCard {
		return nil, 0, entity.NewFieldViolation("seam_keys", "too_many", "",
			fmt.Sprintf("at most %d keys per call", entity.TechCardSeamMaxRowsPerCard))
	}
	keys := make([]string, 0, len(seamKeys))
	seen := map[string]bool{}
	for i, k := range seamKeys {
		k = entity.NormalizeTechCardSeamKey(k)
		if !entity.ValidTechCardSeamKey(k) {
			return nil, 0, entity.NewFieldViolation(fmt.Sprintf("seam_keys[%d]", i), "invalid", "",
				"a 26-char ULID")
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	var (
		out     []entity.TechCardSeam
		deleted int
	)
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := lockTechCardRow(ctx, db, techCardID); err != nil {
			return err
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, techCardID); err != nil {
			return err
		}
		n := 0
		for _, k := range keys {
			affected, err := storeutil.ExecNamedRows(ctx, db, `
				DELETE FROM tech_card_seam WHERE tech_card_id = :id AND seam_key = :k`,
				map[string]any{"id": techCardID, "k": k})
			if err != nil {
				return fmt.Errorf("delete seam %s of tech card %d: %w", k, techCardID, err)
			}
			n += int(affected)
		}
		list, err := listTechCardSeams(ctx, db, techCardID)
		if err != nil {
			return err
		}
		out, deleted = list, n
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, deleted, nil
}
