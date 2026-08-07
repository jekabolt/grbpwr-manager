// Package workshop implements the workshop_settings store (0272) — «дом настроек цеха», the
// singleton row of shop-floor constants (cutting table length, припуск по умолчанию, режим гейта
// готовности and предел высоты стопки today; минимальный зазор as it lands).
package workshop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// singletonID is the only legal workshop_settings.id; the table's chk_workshop_settings_singleton
// CHECK refuses anything else.
const singletonID = 1

// TxFunc is the transaction runner injected by the parent store.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Workshop.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new workshop settings store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

const selectSettings = `SELECT cutting_table_length_cm, default_seam_allowance_cm, run_readiness_blocking,
	       max_stack_height_cm, updated_by, updated_at
	FROM workshop_settings WHERE id = :id`

// GetSettings returns the workshop configuration. A MISSING singleton row is not an error: it reads
// as "nothing configured yet", the same answer the seeded-but-empty row gives. Anything else would
// make every consumer of a shop-floor default fail closed on a row that the migration seeds and the
// update path recreates anyway.
func (s *Store) GetSettings(ctx context.Context) (*entity.WorkshopSettings, error) {
	return getSettings(ctx, s.DB)
}

func getSettings(ctx context.Context, db dependency.DB) (*entity.WorkshopSettings, error) {
	row, err := storeutil.QueryNamedOne[entity.WorkshopSettings](ctx, db, selectSettings,
		map[string]any{"id": singletonID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &entity.WorkshopSettings{}, nil
		}
		return nil, fmt.Errorf("failed to get workshop settings: %w", err)
	}
	return &row, nil
}

// UpdateSettings applies a partial patch and returns the resulting configuration, so a caller never
// needs a follow-up read to learn what it actually wrote.
//
// Partiality is expressed in the SQL itself with IF(:x_omitted, col, :x) (the 0265 idiom) rather
// than by assembling a SET list: the statement stays static and every future tenant is one more
// line that cannot get the mask plumbing subtly wrong.
//
// The INSERT IGNORE ahead of the UPDATE makes the write self-healing on a database whose singleton
// row went missing. It also removes any temptation to read meaning out of RowsAffected here: MySQL
// reports 0 rows for an UPDATE that sets a column to the value it already holds, so a "0 rows means
// the row is gone" check would reject the perfectly ordinary re-save of an unchanged value.
func (s *Store) UpdateSettings(ctx context.Context, patch entity.WorkshopSettingsPatch, updatedBy string) (*entity.WorkshopSettings, error) {
	if patch.IsEmpty() {
		return nil, entity.NewFieldViolation("patch", "no_settings_named", "",
			"name at least one setting to write")
	}
	if patch.CuttingTableLengthCm != nil {
		if err := entity.ValidateCuttingTableLengthCm(*patch.CuttingTableLengthCm); err != nil {
			return nil, err
		}
	}
	// Ф3.2's tenant validates through a DIFFERENT function on purpose: a 0 cm table is nonsense and is
	// rejected, a 0 cm allowance is a real setting («our выкройки carry the cut line») and is accepted.
	// One shared validator could not have held both floors — which is exactly the argument 0272 made
	// for a typed column per setting rather than a shared key/value table.
	if patch.DefaultSeamAllowanceCm != nil {
		if err := entity.ValidateSeamAllowanceStandardCm("default_seam_allowance_cm",
			*patch.DefaultSeamAllowanceCm); err != nil {
			return nil, err
		}
	}
	// Ф4.8's tenant sides with the TABLE LENGTH on the zero question, not with the allowance: a 0 cm
	// stack limit would fail every настил in the shop, so «у нас нет предела» is said by clearing the
	// setting (no verdict) rather than by entering a number that refuses everything.
	if patch.MaxStackHeightCm != nil {
		if err := entity.ValidateMaxStackHeightCm(*patch.MaxStackHeightCm); err != nil {
			return nil, err
		}
	}

	params := map[string]any{
		"id":         singletonID,
		"updated_by": updatedBy,
		// Presence, not value: an omitted setting keeps its stored column (see the IF() above).
		"cutting_table_length_omitted": patch.CuttingTableLengthCm == nil,
		"cutting_table_length_cm":      nullDecimalParam(patch.CuttingTableLengthCm),
		"seam_allowance_omitted":       patch.DefaultSeamAllowanceCm == nil,
		"default_seam_allowance_cm":    nullDecimalParam(patch.DefaultSeamAllowanceCm),
		// Ф6.9 — the same presence mask as its neighbours, and NO validator: a bool has no
		// plausibility band, and the only thing that could be wrong about it is being set at the wrong
		// TIME (before кампания Д3 has re-measured the norms), which no server-side check can know.
		// What protects that is the gate's report-only default plus the fact that flipping it is a
		// deliberate, single, auditable command — not a validator that would have to guess.
		"run_readiness_blocking_omitted": patch.RunReadinessBlocking == nil,
		"run_readiness_blocking":         boolParam(patch.RunReadinessBlocking),
		// Ф4.8 — one more line, and that is the whole point of the static IF() statement: a fourth
		// tenant cannot get its half of the mask plumbing subtly wrong the way an assembled SET list
		// would let it.
		"max_stack_height_omitted": patch.MaxStackHeightCm == nil,
		"max_stack_height_cm":      nullDecimalParam(patch.MaxStackHeightCm),
	}

	var out *entity.WorkshopSettings
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`INSERT IGNORE INTO workshop_settings (id) VALUES (:id)`,
			map[string]any{"id": singletonID}); err != nil {
			return fmt.Errorf("failed to ensure workshop settings row: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE workshop_settings SET
				cutting_table_length_cm = IF(:cutting_table_length_omitted, cutting_table_length_cm, :cutting_table_length_cm),
				default_seam_allowance_cm = IF(:seam_allowance_omitted, default_seam_allowance_cm, :default_seam_allowance_cm),
				run_readiness_blocking = IF(:run_readiness_blocking_omitted, run_readiness_blocking, :run_readiness_blocking),
				max_stack_height_cm = IF(:max_stack_height_omitted, max_stack_height_cm, :max_stack_height_cm),
				updated_by = :updated_by
			WHERE id = :id`, params); err != nil {
			return fmt.Errorf("failed to update workshop settings: %w", err)
		}
		got, err := getSettings(ctx, rep.DB())
		if err != nil {
			return err
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nullDecimalParam binds a tri-state patch field. nil (the setting was not named) binds NULL, which
// the IF() discards anyway; a cleared setting binds NULL for real.
func nullDecimalParam(v *decimal.NullDecimal) any {
	if v == nil || !v.Valid {
		return nil
	}
	return v.Decimal
}

// boolParam binds the two-state bool patch. nil (the setting was not named) binds NULL, which the
// IF() discards anyway — there is no «clear it» state to express here, because unset and false
// behave identically (see entity.WorkshopSettings.RunReadinessBlocking).
func boolParam(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}
