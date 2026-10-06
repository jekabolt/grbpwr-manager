package design

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SetDetailKept marks a STALE flat detail kept (req.Keep) or takes the mark off (0400,
// 82-INPUT-REDESIGN §5). The mark is written against the CURRENT views run and the CURRENT detail
// plate, read under the card and bench locks, so it can never name a state the bench did not have
// at the moment of the write; ApplyDesignDetailStaleness reads it only while both are still current.
//
// THE SLOT REV IS NOT BUMPED: the mark is an acknowledgement, not a placement, and bumping the CAS
// token would make every open tab's next placement on this slot fail with slot_rev_mismatch for a
// change that moved no plate.
//
// Returns the slot with Stale / Kept recomputed over the whole bench.
func (s *Store) SetDetailKept(ctx context.Context, req entity.DesignDetailKeptSet) (*entity.DesignBenchSlot, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.SlotId <= 0 {
		return nil, fmt.Errorf("%w: slot id is required", entity.ErrDesignInvalidArgument)
	}
	var out entity.DesignBenchSlot
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// The general lock order (locks.go): card → pictures → bench. No picture is written here.
		if err := lockDesignCard(ctx, db, req.TechCardId); err != nil {
			return err
		}
		if err := lockDesignBench(ctx, db, req.TechCardId); err != nil {
			return err
		}
		slot, err := slotByID(ctx, db, req.SlotId)
		if err != nil {
			return err
		}
		if slot.TechCardId != req.TechCardId {
			// Another card's slot is «no such slot on this card», never a confirmation it exists.
			return fmt.Errorf("%w: design bench slot %d on tech card %d", entity.ErrDesignNotFound, req.SlotId, req.TechCardId)
		}
		if !entity.IsDesignFlatDetailSlot(slot) {
			return fmt.Errorf("%w: slot %d is %s/%s", entity.ErrDesignNotAFlatDetail, slot.Id,
				entity.DesignKindOrFlat(slot.Kind), slot.ViewKey)
		}
		bench, err := listBenchSlots(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		ptrs := make([]*entity.DesignBenchSlot, 0, len(bench))
		for i := range bench {
			ptrs = append(ptrs, &bench[i])
		}
		if err := attachSlotPictures(ctx, rep, ptrs); err != nil {
			return err
		}
		entity.ApplyDesignDetailStaleness(bench)
		at := -1
		for i := range bench {
			if bench[i].Id == slot.Id {
				at = i
				break
			}
		}
		if at < 0 {
			return fmt.Errorf("%w: design bench slot %d", entity.ErrDesignNotFound, slot.Id)
		}
		cur := bench[at]

		// CAS on the views run for BOTH directions: an old tab's unkeep must not wipe a mark made
		// against newer views.
		if req.AgainstRunId > 0 && req.AgainstRunId != cur.StaleAgainstRunId {
			return fmt.Errorf("%w: the views are run %d now, not %d", entity.ErrDesignViewsChanged,
				cur.StaleAgainstRunId, req.AgainstRunId)
		}
		if req.Keep {
			if cur.Picture == nil || !cur.PictureId.Valid || cur.PictureId.Int32 <= 0 {
				return fmt.Errorf("%w: slot %d holds no plate", entity.ErrDesignDetailEmpty, slot.Id)
			}
			if !cur.Stale {
				return fmt.Errorf("%w: slot %d is not older than the views", entity.ErrDesignDetailNotStale, slot.Id)
			}
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_bench_slot
				SET kept_run_id = :run, kept_picture_id = :pic, kept_by = :who, kept_at = UTC_TIMESTAMP(6)
				WHERE id = :id`, map[string]any{
				"run": cur.StaleAgainstRunId, "pic": cur.PictureId.Int32, "who": req.Actor, "id": slot.Id,
			}); err != nil {
				return fmt.Errorf("failed to keep design detail slot %d: %w", slot.Id, err)
			}
		} else if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_bench_slot
				SET kept_run_id = NULL, kept_picture_id = NULL, kept_by = '', kept_at = NULL
				WHERE id = :id`, map[string]any{"id": slot.Id}); err != nil {
			return fmt.Errorf("failed to unkeep design detail slot %d: %w", slot.Id, err)
		}

		// Re-read the row (its stamps) and recompute against the bench already in hand.
		fresh, err := slotByID(ctx, db, slot.Id)
		if err != nil {
			return err
		}
		fresh.Picture, fresh.RunKind, fresh.RunRrev = cur.Picture, cur.RunKind, cur.RunRrev
		bench[at] = fresh
		entity.ApplyDesignDetailStaleness(bench)
		out = bench[at]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
