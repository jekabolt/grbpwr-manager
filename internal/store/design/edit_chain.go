package design

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// designEditChainLinks — ЗВЕНЬЯ ЦЕПОЧЕК ПРАВОК карточек (T28 v2): всякий кадр с replaced_by (корень либо
// середина цепочки) и всякий флэттен (только флэттен становится преемником — FlattenEditLayer с
// ReplacePictureId). Узкие колонки по индексу idx_design_picture_card.
const designEditChainLinks = `
	SELECT ` + entity.DesignEditChainColumns + ` FROM design_picture
	WHERE tech_card_id IN (:cards) AND (replaced_by IS NOT NULL OR derivation = :flatten)`

func designEditChainParams(cards []int) map[string]any {
	return map[string]any{"cards": cards, "flatten": entity.DesignDerivationFlatten}
}

// annotateEditChains — УГЛЫ undo/redo каждого кадра (entity.DesignEditChainControls), посчитанные по
// ВСЕЙ цепочке его карточки, а не по странице: плита слота из строки, ушедшей за страницу, обязана
// сохранить углы. Зовётся из resolveMedia — единственной воронки, через которую кадр уходит наружу.
func annotateEditChains(ctx context.Context, db dependency.DB, pics []*entity.DesignPicture) error {
	seen := map[int]bool{}
	var cards []int
	for _, p := range pics {
		if p == nil || p.TechCardId <= 0 || seen[p.TechCardId] {
			continue
		}
		seen[p.TechCardId] = true
		cards = append(cards, p.TechCardId)
	}
	if len(cards) == 0 {
		return nil
	}
	links, err := storeutil.QueryListNamed[entity.DesignChainLink](ctx, db, designEditChainLinks, designEditChainParams(cards))
	if err != nil {
		return fmt.Errorf("failed to read design edit chains: %w", err)
	}
	controls := entity.DesignEditChainControls(links)
	for _, p := range pics {
		if p == nil {
			continue
		}
		c := controls[p.Id]
		p.CanUndo, p.CanRedo = c.CanUndo, c.CanRedo
	}
	return nil
}

// UndoEdit — ОТМЕНИТЬ ТЕКУЩУЮ ВЕРСИЮ ЦЕПОЧКИ (UndoDesignEdit): undone_at на неё, слоты, державшие её,
// переезжают на предыдущее звено. Одна транзакция, решение — entity.DesignUndoStep.
func (s *Store) UndoEdit(ctx context.Context, req entity.DesignEditChainStepRequest) (*entity.DesignEditChainResult, error) {
	return s.editChainStep(ctx, req, true)
}

// RedoEdit — ВЕРНУТЬ ОТМЕНЁННОЕ ЗВЕНО за текущей версией (RedoDesignEdit): undone_at снимается, слоты
// текущей версии переезжают на него. Одна транзакция, решение — entity.DesignRedoStep.
func (s *Store) RedoEdit(ctx context.Context, req entity.DesignEditChainStepRequest) (*entity.DesignEditChainResult, error) {
	return s.editChainStep(ctx, req, false)
}

// editChainStep — ТЕЛО обоих глаголов, в одной SERIALIZABLE-транзакции:
//
//  1. звенья цепочек карточки читаются FOR UPDATE — две вкладки, жмущие undo/redo одной цепочки, идут
//     друг за другом, и вторая видит исход первой;
//  2. CAS: текущая версия = expected_current_id, иначе stale_chain (повтор того же жеста — успех без
//     записи, entity.DesignUndoStep / DesignRedoStep);
//  3. метка undone_at ставится (undo) либо снимается (redo) — UPDATE с охраной на прежнее значение;
//  4. каждый слот, державший прежнюю текущую версию, переезжает на новую через setBenchSlotTx (все
//     сторожа постановки, slot_rev + 1).
//
// Ответ — цепочка после шага (кадры с файлами и углами) и слоты новой текущей версии.
func (s *Store) editChainStep(ctx context.Context, req entity.DesignEditChainStepRequest, undo bool) (*entity.DesignEditChainResult, error) {
	if req.PictureId <= 0 || req.ExpectedCurrentId <= 0 {
		return nil, fmt.Errorf("%w: picture_id and expected_current_id are required", entity.ErrDesignInvalidArgument)
	}
	var out entity.DesignEditChainResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = entity.DesignEditChainResult{}
		db := rep.DB()
		pic, err := pictureByID(ctx, db, req.PictureId)
		if err != nil {
			return err
		}
		links, err := storeutil.QueryListNamed[entity.DesignChainLink](ctx, db,
			designEditChainLinks+` FOR UPDATE`, designEditChainParams([]int{pic.TechCardId}))
		if err != nil {
			return fmt.Errorf("failed to lock the edit chain of design picture %d: %w", pic.Id, err)
		}
		if !hasChainLink(links, pic.Id) {
			// Кадр вне всякой цепочки (не флэттен и не заменён) — цепочка из него одного.
			links = append(links, entity.DesignChainLink{Id: pic.Id, TechCardId: pic.TechCardId, ReplacedBy: pic.ReplacedBy, UndoneAt: pic.UndoneAt})
		}
		chain, err := entity.DesignEditChain(links, pic.Id)
		if err != nil {
			return err
		}

		var step entity.DesignEditStep
		if undo {
			crops := 0
			if cur := entity.DesignEditChainCurrent(chain); cur > 0 && chain[cur].Id == req.ExpectedCurrentId {
				standing, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db,
					designSheetCropsOf, designSheetCropsParams(chain[cur].Id))
				if err != nil {
					return fmt.Errorf("failed to read the visible pieces of design picture %d: %w", chain[cur].Id, err)
				}
				crops = len(standing)
			}
			step, err = entity.DesignUndoStep(chain, req.ExpectedCurrentId, crops)
		} else {
			step, err = entity.DesignRedoStep(chain, req.ExpectedCurrentId)
		}
		if err != nil {
			return err
		}

		if !step.Replay {
			q := `UPDATE design_picture SET undone_at = UTC_TIMESTAMP(6) WHERE id = :id AND undone_at IS NULL`
			if !undo {
				q = `UPDATE design_picture SET undone_at = NULL WHERE id = :id AND undone_at IS NOT NULL`
			}
			n, err := storeutil.ExecNamedRows(ctx, db, q, map[string]any{"id": step.Mark})
			if err != nil {
				return fmt.Errorf("failed to mark design picture %d undone=%v: %w", step.Mark, undo, err)
			}
			if n != 1 {
				// Под замком этого не бывает; второй пояс к CAS, а не молчаливый успех.
				return fmt.Errorf("%w: picture %d changed under the chain lock", entity.ErrDesignStaleChain, step.Mark)
			}
			if _, err := moveBenchSlots(ctx, rep, pic.TechCardId, step.From, step.To, req.Actor); err != nil {
				return err
			}
			slog.Default().InfoContext(ctx, "design edit chain step",
				slog.Bool("undo", undo), slog.Int("from", step.From), slog.Int("to", step.To),
				slog.String("actor", req.Actor), slog.String("idempotency_key", req.IdempotencyKey))
		}

		ids := make([]int, 0, len(chain))
		for _, l := range chain {
			ids = append(ids, l.Id)
		}
		out.CurrentPictureId = step.To
		if out.Pictures, err = storeutil.QueryListNamed[entity.DesignPicture](ctx, db,
			`SELECT * FROM design_picture WHERE id IN (:ids) ORDER BY id`, map[string]any{"ids": ids}); err != nil {
			return fmt.Errorf("failed to read the edit chain of design picture %d: %w", pic.Id, err)
		}
		flat := make([]*entity.DesignPicture, 0, len(out.Pictures))
		for i := range out.Pictures {
			flat = append(flat, &out.Pictures[i])
		}
		if err := resolveMedia(ctx, rep, flat); err != nil {
			return err
		}
		if out.Slots, err = storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db,
			`SELECT * FROM design_bench_slot WHERE tech_card_id = :card AND picture_id = :pic`,
			map[string]any{"card": pic.TechCardId, "pic": step.To}); err != nil {
			return fmt.Errorf("failed to read the bench slots of design picture %d: %w", step.To, err)
		}
		ptrs := make([]*entity.DesignBenchSlot, 0, len(out.Slots))
		for i := range out.Slots {
			ptrs = append(ptrs, &out.Slots[i])
		}
		return attachSlotPictures(ctx, rep, ptrs)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func hasChainLink(links []entity.DesignChainLink, id int) bool {
	for _, l := range links {
		if l.Id == id {
			return true
		}
	}
	return false
}

// moveBenchSlots — КАЖДЫЙ СЛОТ, ДЕРЖАЩИЙ from, ПЕРЕЕЗЖАЕТ НА to — через setBenchSlotTx, со всеми сторожами
// постановки и slot_rev + 1. Ожидаемая ревизия — из этой же транзакции: постановку решает сервер по
// строке, прочитанной под замком, а вкладка со старым экраном узнает о переезде по slot_rev_mismatch.
// Слот, держащий from, — не больше одного (uq_design_bench_picture); нет слота — переезжать нечему.
// Общий для перезаписи (flattenTakeThePlaceOf) и для undo/redo.
func moveBenchSlots(ctx context.Context, rep dependency.Repository, cardID, from, to int, actor string) ([]*entity.DesignBenchSlot, error) {
	holders, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, rep.DB(), `
		SELECT * FROM design_bench_slot WHERE tech_card_id = :card AND picture_id = :pic`,
		map[string]any{"card": cardID, "pic": from})
	if err != nil {
		return nil, fmt.Errorf("failed to find the bench slot of design picture %d: %w", from, err)
	}
	moved := make([]*entity.DesignBenchSlot, 0, len(holders))
	for _, h := range holders {
		slot, err := setBenchSlotTx(ctx, rep, entity.DesignBenchSlotSet{
			TechCardId:      cardID,
			Slot:            entity.DesignSlotRef{SlotId: h.Id},
			PictureId:       to,
			ExpectedSlotRev: h.SlotRev,
			Actor:           actor,
		})
		if err != nil {
			return nil, err
		}
		moved = append(moved, slot)
	}
	return moved, nil
}
