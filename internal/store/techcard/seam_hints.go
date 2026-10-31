package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SEAM HINTS (0440, unit-seams line F): the stored AI hint runs, the owner's overrides and the
// owner's code dictionary. NOT A LEG OF UpdateTechCard and not card content: no card row lock, no
// lock_version, no released-card refusal, and clone / archive / restore do not carry them. The
// shapes and vocabularies are validated by the caller (internal/techcardseamhints).

const seamHintRunColumns = `id, tech_card_id, input_digest, schema_version, model, hints, created_by, created_at`

// requireTechCard returns sql.ErrNoRows (wrapped) when the card does not exist.
func (s *Store) requireTechCard(ctx context.Context, cardID int) error {
	if _, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, s.DB, `SELECT id FROM tech_card WHERE id = :id`, map[string]any{"id": cardID}); err != nil {
		return fmt.Errorf("tech card %d: %w", cardID, err)
	}
	return nil
}

// TechCardTechnicalMediaIDs returns which of ids are on the card's TECHNICAL sheet
// (tech_card_media, category = 'technical': flats / sketches, never the moodboard). An unknown card
// is sql.ErrNoRows. The caller refuses any id not returned: a global media id must never let a
// picture of another card reach an external model.
func (s *Store) TechCardTechnicalMediaIDs(ctx context.Context, cardID int, ids []int) ([]int, error) {
	if err := s.requireTechCard(ctx, cardID); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	got, err := storeutil.QueryScalarListNamed[int](ctx, s.DB, `
		SELECT DISTINCT media_id FROM tech_card_media
		WHERE tech_card_id = :card AND category = :technical AND media_id IN (:ids)`,
		map[string]any{"card": cardID, "technical": "technical", "ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to read the technical media of tech card %d: %w", cardID, err)
	}
	return got, nil
}

// GetTechCardSeamHintRun reads the run of exactly (card, digest, schema version); found=false when
// there is none.
func (s *Store) GetTechCardSeamHintRun(ctx context.Context, cardID int, digest string, schemaVersion int) (entity.TechCardSeamHintRun, bool, error) {
	run, err := storeutil.QueryNamedOne[entity.TechCardSeamHintRun](ctx, s.DB, `
		SELECT `+seamHintRunColumns+` FROM tech_card_seam_hint_run
		WHERE tech_card_id = :card AND input_digest = :digest AND schema_version = :v`,
		map[string]any{"card": cardID, "digest": digest, "v": schemaVersion})
	if errors.Is(err, sql.ErrNoRows) {
		return run, false, nil
	}
	if err != nil {
		return run, false, fmt.Errorf("failed to read the seam hint run of tech card %d: %w", cardID, err)
	}
	return run, true, nil
}

// LatestTechCardSeamHintRun reads the card's newest run of the schema version, whatever its digest;
// found=false when the card has none.
func (s *Store) LatestTechCardSeamHintRun(ctx context.Context, cardID int, schemaVersion int) (entity.TechCardSeamHintRun, bool, error) {
	run, err := storeutil.QueryNamedOne[entity.TechCardSeamHintRun](ctx, s.DB, `
		SELECT `+seamHintRunColumns+` FROM tech_card_seam_hint_run
		WHERE tech_card_id = :card AND schema_version = :v
		ORDER BY created_at DESC, id DESC LIMIT 1`,
		map[string]any{"card": cardID, "v": schemaVersion})
	if errors.Is(err, sql.ErrNoRows) {
		return run, false, nil
	}
	if err != nil {
		return run, false, fmt.Errorf("failed to read the latest seam hint run of tech card %d: %w", cardID, err)
	}
	return run, true, nil
}

// SaveTechCardSeamHintRun stores a run: a new (card, digest, schema version) is inserted, an
// existing one (a forced rerun) is replaced in place (model, hints, created_by, created_at = now).
// Overrides are never touched. Returns the stored row. An unknown card fails the FK; the caller
// maps it to NotFound.
func (s *Store) SaveTechCardSeamHintRun(ctx context.Context, run entity.TechCardSeamHintRun) (entity.TechCardSeamHintRun, error) {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO tech_card_seam_hint_run (tech_card_id, input_digest, schema_version, model, hints, created_by)
		VALUES (:card, :digest, :v, :model, :hints, :by)
		ON DUPLICATE KEY UPDATE model = VALUES(model), hints = VALUES(hints),
			created_by = VALUES(created_by), created_at = CURRENT_TIMESTAMP(3)`,
		map[string]any{"card": run.TechCardId, "digest": run.InputDigest, "v": run.SchemaVersion,
			"model": run.Model, "hints": run.Hints, "by": run.CreatedBy}); err != nil {
		return run, fmt.Errorf("failed to save the seam hint run of tech card %d: %w", run.TechCardId, err)
	}
	saved, ok, err := s.GetTechCardSeamHintRun(ctx, run.TechCardId, run.InputDigest, run.SchemaVersion)
	if err != nil {
		return run, err
	}
	if !ok {
		return run, fmt.Errorf("seam hint run of tech card %d vanished after save", run.TechCardId)
	}
	return saved, nil
}

// ListTechCardSeamHintOverrides reads the card's overrides ordered by kind, hint key.
func (s *Store) ListTechCardSeamHintOverrides(ctx context.Context, cardID int) ([]entity.TechCardSeamHintOverride, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardSeamHintOverride](ctx, s.DB, `
		SELECT tech_card_id, kind, hint_key, value, state, updated_at, updated_by
		FROM tech_card_seam_hint_override WHERE tech_card_id = :card ORDER BY kind, hint_key`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to read the seam hint overrides of tech card %d: %w", cardID, err)
	}
	return rows, nil
}

// SetTechCardSeamHintOverride upserts one override (state ok / fixed / off), or deletes it when
// o.State is empty. Returns the card's full list afterwards. An unknown card is sql.ErrNoRows.
func (s *Store) SetTechCardSeamHintOverride(ctx context.Context, o entity.TechCardSeamHintOverride) ([]entity.TechCardSeamHintOverride, error) {
	if err := s.requireTechCard(ctx, o.TechCardId); err != nil {
		return nil, err
	}
	params := map[string]any{"card": o.TechCardId, "kind": o.Kind, "key": o.HintKey}
	if o.State == "" {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			DELETE FROM tech_card_seam_hint_override WHERE tech_card_id = :card AND kind = :kind AND hint_key = :key`,
			params); err != nil {
			return nil, fmt.Errorf("failed to delete a seam hint override of tech card %d: %w", o.TechCardId, err)
		}
	} else {
		var value any // NULL when none (state off)
		if o.Value.Valid {
			value = o.Value.String
		}
		params["value"], params["state"], params["by"] = value, o.State, o.UpdatedBy
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO tech_card_seam_hint_override (tech_card_id, kind, hint_key, value, state, updated_by)
			VALUES (:card, :kind, :key, :value, :state, :by)
			ON DUPLICATE KEY UPDATE value = VALUES(value), state = VALUES(state), updated_by = VALUES(updated_by),
				updated_at = CURRENT_TIMESTAMP(3)`,
			params); err != nil {
			return nil, fmt.Errorf("failed to save a seam hint override of tech card %d: %w", o.TechCardId, err)
		}
	}
	return s.ListTechCardSeamHintOverrides(ctx, o.TechCardId)
}

// ListPieceCodeDictionary reads the owner's code dictionary ordered by code.
func (s *Store) ListPieceCodeDictionary(ctx context.Context) ([]entity.PieceCodeDictionaryEntry, error) {
	rows, err := storeutil.QueryListNamed[entity.PieceCodeDictionaryEntry](ctx, s.DB, `
		SELECT code, role, hand, updated_at, updated_by FROM piece_code_dictionary ORDER BY code`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read the piece code dictionary: %w", err)
	}
	return rows, nil
}

// SetPieceCodeDictionaryEntry upserts one dictionary row (e.Role non-empty) or deletes it (e.Role
// empty). Returns the stored row and found=false after a delete (or when deleting a missing code).
func (s *Store) SetPieceCodeDictionaryEntry(ctx context.Context, e entity.PieceCodeDictionaryEntry) (entity.PieceCodeDictionaryEntry, bool, error) {
	if e.Role == "" {
		if err := storeutil.ExecNamed(ctx, s.DB, `DELETE FROM piece_code_dictionary WHERE code = :code`,
			map[string]any{"code": e.Code}); err != nil {
			return e, false, fmt.Errorf("failed to delete piece code %s: %w", e.Code, err)
		}
		return e, false, nil
	}
	var hand any
	if e.Hand.Valid && e.Hand.String != "" {
		hand = e.Hand.String
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO piece_code_dictionary (code, role, hand, updated_by) VALUES (:code, :role, :hand, :by)
		ON DUPLICATE KEY UPDATE role = VALUES(role), hand = VALUES(hand), updated_by = VALUES(updated_by),
			updated_at = CURRENT_TIMESTAMP(3)`,
		map[string]any{"code": e.Code, "role": e.Role, "hand": hand, "by": e.UpdatedBy}); err != nil {
		return e, false, fmt.Errorf("failed to save piece code %s: %w", e.Code, err)
	}
	got, err := storeutil.QueryNamedOne[entity.PieceCodeDictionaryEntry](ctx, s.DB, `
		SELECT code, role, hand, updated_at, updated_by FROM piece_code_dictionary WHERE code = :code`,
		map[string]any{"code": e.Code})
	if err != nil {
		return e, false, fmt.Errorf("failed to read piece code %s back: %w", e.Code, err)
	}
	return got, true, nil
}
