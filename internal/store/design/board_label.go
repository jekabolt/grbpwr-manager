package design

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─── Board labels (101-MOODBOARD-ROLES, wave 11) ───
//
// A design_reference row is the server's label on a moodboard picture. Three writers touch it and
// ONE rule keeps them apart: a row whose label_source is a person's ('' of a legacy row, human, quiz)
// is never written by a model path — every model write below carries that guard in its WHERE, in the
// same statement, so a tap that lands while the model is thinking always wins.

// ListReferences — the card's design_reference rows (every state), ordinal then id.
func (s *Store) ListReferences(ctx context.Context, cardID int) ([]entity.DesignReference, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, s.DB,
		`SELECT * FROM design_reference WHERE tech_card_id = :card ORDER BY ordinal, id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design references: %w", err)
	}
	return rows, nil
}

// designModelSources — the SQL list of label sources a model owns (entity.IsDesignLabelByModel).
const designModelSources = `('` + entity.DesignLabelSourceModelCheap + `', '` + entity.DesignLabelSourceModelStrong + `')`

// BeginBoardLabel claims one board picture for a model call. true = the caller owns a pending row
// and must finish it (FinishBoardLabel); false = nothing to do (a person's label, a settled model
// label, a task already in flight, or a lost task that used up its attempts — now `failed`).
//
//	no row                           → insert pending (attempt 1)
//	a person's row                   → false, never touched
//	model row, Relabel               → reset to pending (attempt 1): the purpose changed under it
//	model row, pending, stale        → attempts < max: re-arm (attempt + 1); else → failed
//	anything else                    → false
func (s *Store) BeginBoardLabel(ctx context.Context, req entity.DesignBoardLabelBegin) (bool, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return false, err
	}
	if req.MediaId <= 0 {
		return false, fmt.Errorf("%w: a board label needs a media id", entity.ErrDesignInvalidArgument)
	}
	claimed := false
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		claimed = false
		db := rep.DB()
		rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE tech_card_id = :card AND media_id = :media FOR UPDATE`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to read the board label: %w", err)
		}
		if len(rows) == 0 {
			ord, err := storeutil.QueryCountNamed(ctx, db,
				`SELECT COALESCE(MAX(ordinal), 0) FROM design_reference WHERE tech_card_id = :card`,
				map[string]any{"card": req.TechCardId})
			if err != nil {
				return fmt.Errorf("failed to read the reference tail: %w", err)
			}
			// INSERT IGNORE: a concurrent claim of the same picture (two saves at once) inserts once;
			// the loser sees 0 rows and does not call the model.
			n, err := storeutil.ExecNamedRows(ctx, db, `
				INSERT IGNORE INTO design_reference
					(tech_card_id, media_id, role, ordinal, set_by, set_at,
					 label_source, label_state, label_attempts, labelled_at)
				VALUES (:card, :media, '', :ord, :who, UTC_TIMESTAMP(6),
					:src, :pending, 1, UTC_TIMESTAMP(6))`,
				map[string]any{
					"card": req.TechCardId, "media": req.MediaId, "ord": ord + 1, "who": req.Actor,
					"src": entity.DesignLabelSourceModelCheap, "pending": entity.DesignLabelStatePending,
				})
			if err != nil {
				return fmt.Errorf("failed to claim the board label: %w", err)
			}
			claimed = n > 0
			return nil
		}
		r := rows[0]
		if !entity.IsDesignLabelByModel(r.LabelSource) {
			return nil
		}
		state := entity.DesignLabelStateOrOk(r.LabelState)
		switch {
		case req.Relabel:
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_reference
				SET role = '', detail_slot_id = NULL, label_source = :src, label_state = :pending,
					proposed_purpose = '', model_caption = NULL, label_model = '',
					label_attempts = 1, labelled_at = UTC_TIMESTAMP(6)
				WHERE id = :id AND label_source IN `+designModelSources,
				map[string]any{"id": r.Id, "src": entity.DesignLabelSourceModelCheap, "pending": entity.DesignLabelStatePending}); err != nil {
				return fmt.Errorf("failed to re-arm the board label: %w", err)
			}
			claimed = true
		case state == entity.DesignLabelStatePending && (!r.LabelledAt.Valid || r.LabelledAt.Time.Before(req.StaleBefore)):
			if r.LabelAttempts >= entity.DesignBoardLabelMaxAttempts {
				if err := storeutil.ExecNamed(ctx, db, `
					UPDATE design_reference SET label_state = :failed, labelled_at = UTC_TIMESTAMP(6)
					WHERE id = :id AND label_source IN `+designModelSources,
					map[string]any{"id": r.Id, "failed": entity.DesignLabelStateFailed}); err != nil {
					return fmt.Errorf("failed to fail the board label: %w", err)
				}
				return nil
			}
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_reference SET label_attempts = label_attempts + 1, labelled_at = UTC_TIMESTAMP(6)
				WHERE id = :id AND label_source IN `+designModelSources,
				map[string]any{"id": r.Id}); err != nil {
				return fmt.Errorf("failed to re-arm the board label: %w", err)
			}
			claimed = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// FinishBoardLabel writes a model's settled answer over the picture's PENDING model row. A row a
// person labelled meanwhile, or one that left the pending state, is not written (nil, nil).
//
// A detail with no slot (NewDetailName) mints a model slot of the flat bench (colourway-less, empty —
// it holds the detail's drawing to come) in the SAME transaction as the label, so a slot never exists
// without the photo that asked for it. Two guards against duplicates live here, not in the caller: the
// model's slot id must be a detail slot of this card, and a name equal (case/space-folded) to an
// existing detail slot's joins that slot instead of minting «(2)».
func (s *Store) FinishBoardLabel(ctx context.Context, req entity.DesignBoardLabel) (*entity.DesignReference, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.Role != "" && !entity.IsDesignReferenceRole(req.Role) {
		return nil, fmt.Errorf("%w: unknown board label role %q", entity.ErrDesignInvalidArgument, req.Role)
	}
	var out *entity.DesignReference
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()
		rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, db, `
			SELECT * FROM design_reference WHERE tech_card_id = :card AND media_id = :media FOR UPDATE`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to read the board label: %w", err)
		}
		if len(rows) == 0 || !entity.IsDesignLabelByModel(rows[0].LabelSource) ||
			entity.DesignLabelStateOrOk(rows[0].LabelState) != entity.DesignLabelStatePending {
			return nil
		}
		slot := any(nil)
		if req.Role == entity.DesignViewDetail {
			id, err := boardLabelDetailSlot(ctx, db, req)
			if err != nil {
				return err
			}
			if id > 0 {
				slot = id
			}
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE design_reference
			SET role = :role, detail_slot_id = :slot, label_source = :src, label_state = :state,
				proposed_purpose = :purpose, model_caption = :caption, label_model = :model,
				labelled_at = UTC_TIMESTAMP(6)
			WHERE id = :id AND label_source IN `+designModelSources,
			map[string]any{
				"id": rows[0].Id, "role": req.Role, "slot": slot, "src": req.Source, "state": req.State,
				"purpose": req.ProposedPurpose, "caption": nullStr(strings.TrimSpace(req.ModelCaption)),
				"model": truncateRunes(req.LabelModel, 96),
			}); err != nil {
			return fmt.Errorf("failed to write the board label: %w", err)
		}
		after, err := storeutil.QueryNamedOne[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE id = :id`, map[string]any{"id": rows[0].Id})
		if err != nil {
			return fmt.Errorf("failed to re-read the board label: %w", err)
		}
		out = &after
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// boardLabelDetailSlot resolves the detail slot a model's detail label points at: the named slot when
// it is a detail slot of this card; else an existing detail slot with the same name; else a NEW slot
// marked made_by_model. 0 = no slot (no id, no name).
func boardLabelDetailSlot(ctx context.Context, db dependency.DB, req entity.DesignBoardLabel) (int, error) {
	slots, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db, `
		SELECT * FROM design_bench_slot WHERE tech_card_id = :card AND view_key = :detail ORDER BY id`,
		map[string]any{"card": req.TechCardId, "detail": entity.DesignViewDetail})
	if err != nil {
		return 0, fmt.Errorf("failed to read the detail slots: %w", err)
	}
	if req.DetailSlotId > 0 {
		for _, sl := range slots {
			if sl.Id == req.DetailSlotId {
				return sl.Id, nil
			}
		}
	}
	key := entity.DesignDetailNameKey(req.NewDetailName)
	if key == "" {
		return 0, nil
	}
	for _, sl := range slots {
		if entity.DesignDetailNameKey(sl.DetailName.String) == key {
			return sl.Id, nil
		}
	}
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO design_bench_slot
			(tech_card_id, view_key, kind, colorway_id, exclusive_key, detail_name, picture_id, slot_rev,
			 set_by, set_at, made_by_model)
		VALUES
			(:card, :view, :kind, NULL, :excl, :name, NULL, 1, :who, UTC_TIMESTAMP(6), 1)`,
		map[string]any{
			"card": req.TechCardId, "view": entity.DesignViewDetail, "kind": entity.DesignPictureKindFlat,
			"excl": "detail:" + uuid.NewString(), "name": strings.TrimSpace(req.NewDetailName),
			"who": "model",
		})
	if err != nil {
		return 0, fmt.Errorf("failed to mint the model's detail slot: %w", err)
	}
	return id, nil
}

// DropBoardLabels deletes the MODEL rows of the named pictures (they left the board, or their purpose
// no longer takes a label), then the model-made detail slots left with no picture, no plate and no
// other label. A person's row and a person's slot are never touched. Returns how many slots went.
func (s *Store) DropBoardLabels(ctx context.Context, cardID int, mediaIDs []int) (int, error) {
	if err := requireCard(cardID); err != nil {
		return 0, err
	}
	if len(mediaIDs) == 0 {
		return 0, nil
	}
	dropped := 0
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		dropped = 0
		db := rep.DB()
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE FROM design_reference
			WHERE tech_card_id = :card AND media_id IN (:media) AND label_source IN `+designModelSources,
			map[string]any{"card": cardID, "media": mediaIDs}); err != nil {
			return fmt.Errorf("failed to drop the board labels: %w", err)
		}
		n, err := storeutil.ExecNamedRows(ctx, db, `
			DELETE s FROM design_bench_slot s
			WHERE s.tech_card_id = :card AND s.view_key = :detail AND s.made_by_model = 1
			  AND s.picture_id IS NULL
			  AND NOT EXISTS (SELECT 1 FROM design_reference r WHERE r.detail_slot_id = s.id)`,
			map[string]any{"card": cardID, "detail": entity.DesignViewDetail})
		if err != nil {
			return fmt.Errorf("failed to drop the empty model detail slots: %w", err)
		}
		dropped = int(n)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return dropped, nil
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
