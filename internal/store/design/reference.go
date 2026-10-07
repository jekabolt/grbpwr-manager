package design

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SetReferenceRole states WHICH SIDE of the garment a reference image is about, and in what order
// it is fed to the model.
//
// AN EMPTY ROLE CLEARS IT — the row is deleted and the response carries no reference. «No side
// stated» is a real answer and must not need a second verb, and a row that exists only to say
// «nothing» would then have to be told apart from a row that was never written.
//
// THE ROLE LIVES IN THE BAND, NOT ON THE CARD'S MEDIA ROW, and that is forced: tech_card_media has
// no row key at all — it is rewritten whole by every card save — so there would be nothing to
// carry the attribute onto the resent row.
func (s *Store) SetReferenceRole(ctx context.Context, req entity.DesignReferenceRole) (*entity.DesignReference, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.MediaId <= 0 {
		return nil, fmt.Errorf("%w: a reference role needs a media id", entity.ErrDesignInvalidArgument)
	}
	if req.Role != "" && !entity.IsDesignReferenceRole(req.Role) {
		return nil, fmt.Errorf("%w: unknown reference role %q", entity.ErrDesignInvalidArgument, req.Role)
	}
	var out *entity.DesignReference
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		// T64 (05.10): владелец — медиатека общая, foreign_media больше не отказ; refuseForeignMedia
		// теперь всегда пропускает (вызов оставлен одной точкой правки).
		if err := refuseForeignMedia(ctx, db, req.TechCardId, req.MediaId); err != nil {
			return err
		}
		// A DISPLAY-ONLY PICTURE GETS NO ROLE (0361, D-24). A role is a promise to feed the picture
		// to every run that reads the card's references; the flag says the file is for showing
		// only. Refused on a NON-EMPTY role only — clearing a role is always legal — and read in
		// this transaction, as every guard here is. Asked of the media, not of the card: the flag is
		// a statement about the file, and the money door downstream asks the same question the same
		// way (MediaHeldDisplayOnly).
		if req.Role != "" {
			held, err := storeutil.QueryCountNamed(ctx, db,
				`SELECT COUNT(*) FROM design_picture WHERE media_id = :media AND display_only = 1`,
				map[string]any{"media": req.MediaId})
			if err != nil {
				return fmt.Errorf("failed to check whether media %d is display-only: %w", req.MediaId, err)
			}
			if held > 0 {
				return fmt.Errorf("%w: media %d is a display-only picture of the card and takes no reference role",
					entity.ErrDesignDisplayOnly, req.MediaId)
			}
		}

		// ГРАНИЦА ДЕТАЛИ — РЯДОМ С ГРАНИЦЕЙ МЕДИА, В ТОЙ ЖЕ ТРАНЗАКЦИИ И ПО ТОМУ ЖЕ ДОВОДУ (0360).
		//
		// FK один этого не закрывает: он проверяет лишь СУЩЕСТВОВАНИЕ строки design_bench_slot, а
		// слоты всех карточек живут в одной таблице. Без проверки ниже референс карточки A мог бы
		// указать на деталь карточки B — и клиент, который РИСУЕТ ИМЯ по этому id, напечатал бы
		// человеку чужое слово. Отказ здесь — единственное место, где это ещё вопрос, а не факт.
		//
		// ⚠ ПРОВЕРЯЕТСЯ ТОЛЬКО НАЗВАННЫЙ НЕНУЛЕВОЙ ID. Ноль — это «про слот ничего не сказано»
		// (см. DesignReferenceRole.DetailSlotId), и превращать молчание в отказ значило бы сломать
		// каждого писателя, который редактирует записку и о детали не думает вовсе.
		// ⚠ ОТРИЦАТЕЛЬНЫЙ ИДЕНТИФИКАТОР — ЧЕТВЁРТЫЙ, НЕЗАДУМАННЫЙ ЧЛЕН ПРАВИЛА, И ОН ЗАКРЫВАЕТСЯ
		// ЗДЕСЬ. `keepSlot` ловит ровно ноль, ветка записи — строго положительное, поэтому
		// отрицательное значение проваливалось мимо обоих и доезжало до VALUES(detail_slot_id) =
		// NULL, то есть МОЛЧА СТИРАЛО связь. Ни одна дверь его не проверяла. Отказ, а не
		// «считать за ноль»: минус не приходит от человека, он приходит от сломанного писателя, и
		// принятое молчком стирание — ровно тот класс, от которого волна отказалась везде.
		if req.DetailSlotId < 0 {
			return fmt.Errorf("%w: detail_slot_id must not be negative, got %d",
				entity.ErrDesignInvalidArgument, req.DetailSlotId)
		}
		if req.DetailSlotId > 0 && req.Role == entity.DesignViewDetail {
			ok, err := storeutil.QueryCountNamed(ctx, db, `
				SELECT COUNT(*) FROM design_bench_slot
				WHERE id = :slot AND tech_card_id = :card AND view_key = :detail`,
				map[string]any{
					"slot": req.DetailSlotId, "card": req.TechCardId,
					"detail": entity.DesignViewDetail,
				})
			if err != nil {
				return fmt.Errorf("failed to check detail slot %d of tech card %d: %w",
					req.DetailSlotId, req.TechCardId, err)
			}
			if ok == 0 {
				return fmt.Errorf(
					"%w: detail slot %d is not a detail slot of tech card %d",
					entity.ErrDesignInvalidArgument, req.DetailSlotId, req.TechCardId)
			}
		}

		// AN EMPTY ROLE NO LONGER DELETES THE ROW (101, wave 11). The row is now also the server's label
		// on a board picture, and a deleted row reads as «never labelled» — the next save would hand the
		// picture straight back to the model and undo the person's «no view». So an empty role is written
		// as a PERSON's empty label: it never travels (entity.DesignReferenceTravels), no model touches it
		// again, and the response still carries no reference, as before.
		// THE NOTE IS WRITTEN BY THIS UPSERT AND BY NO OTHER (0348, W-3). It lives on this row, so
		// a verb of its own would be a second write over the same key that could half-succeed —
		// leaving a role stated with somebody else's words next to it.
		//
		// AN EMPTY NOTE CLEARS IT — the column goes to NULL. That is not the rule `role` follows,
		// and the asymmetry is deliberate: a note is text, and empty text is a real answer for it,
		// while an empty role deletes the row above (see the branch before this one).
		// СВЯЗЬ С ДЕТАЛЬЮ — ТРИ СОСТОЯНИЯ, И ОНИ СЧИТАЮТСЯ ЗДЕСЬ, А НЕ В SQL (0360, J-9):
		//
		//	роль не detail          — slot = NULL и keep = false, то есть колонка ОЧИЩАЕТСЯ. Референс,
		//	                          переставший быть деталью, не может продолжать указывать на неё;
		//	роль detail, id  > 0    — slot = id, keep = false, колонка ПИШЕТСЯ;
		//	роль detail, id == 0    — keep = true, колонка ОСТАЁТСЯ КАК БЫЛА.
		//
		// ⚠ ТРЕТЬЕ СОСТОЯНИЕ — НЕ УКРАШЕНИЕ. proto3 не отличает незаполненный int32 от нуля,
		// поэтому вкладка, переписывающая ЗАПИСКУ, присылает здесь ноль — и без `keep` стёрла бы
		// связь с деталью без единого жеста человека. Ровно эта беда уже случалась с самой
		// запиской (0348), и лечится она тем же приёмом.
		keepSlot := req.Role == entity.DesignViewDetail && req.DetailSlotId == 0
		slot := any(nil)
		if req.Role == entity.DesignViewDetail && req.DetailSlotId > 0 {
			slot = req.DetailSlotId
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO design_reference
				(tech_card_id, media_id, role, note, detail_slot_id, ordinal, set_by, set_at,
				 label_source, label_state, labelled_at)
			VALUES (:card, :media, :role, :note, :slot, :ord, :who, UTC_TIMESTAMP(6),
				:human, :ok, UTC_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE
				role = VALUES(role),
				-- A PERSON'S WRITE SETTLES THE LABEL (101). Any write through this door is a person's tap,
				-- so the row leaves the model's hands for good and its state is ok.
				label_source = VALUES(label_source), label_state = VALUES(label_state),
				labelled_at = VALUES(labelled_at),
				-- IF, А НЕ VALUES(detail_slot_id) — по тому же доводу, что у записки строкой ниже,
				-- и с тем же запретом на двоеточие внутри комментария именованного запроса.
				detail_slot_id = IF(:slot_keep, detail_slot_id, VALUES(detail_slot_id)),
				-- IF, А НЕ VALUES(note) — когда вызывающий про записку ничего не сказал, колонка
				-- обязана остаться КАК БЫЛА. Ветвиться в Go двумя разными запросами здесь нельзя,
				-- это два писателя одной строки, и они разойдутся на первой же правке одного.
				--
				-- ⚠ В КОММЕНТАРИИ ВНУТРИ ИМЕНОВАННОГО ЗАПРОСА НЕ СТАВИТЬ ДВОЕТОЧИЕ. sqlx разбирает
				-- текст ДО того, как MySQL увидит комментарий, и «двоеточие плюс пробел» читает как
				-- параметр с ПУСТЫМ именем. Запрос падает на связывании с «could not find name»,
				-- то есть не на синтаксисе SQL, а там, где искать не станешь.
				note = IF(:note_omitted, note, VALUES(note)),
				ordinal = VALUES(ordinal),
				set_by = VALUES(set_by), set_at = VALUES(set_at)`,
			map[string]any{
				"card": req.TechCardId, "media": req.MediaId, "role": req.Role,
				// На ВСТАВКЕ omitted даёт NULL, и это верно: у новорождённой строки записки нет.
				"note":         nullStr(strings.TrimSpace(req.Note)),
				"note_omitted": req.NoteOmitted,
				"slot":         slot, "slot_keep": keepSlot,
				"ord": req.Ordinal, "who": req.Actor,
				"human": entity.DesignLabelSourceHuman, "ok": entity.DesignLabelStateOk,
			}); err != nil {
			return fmt.Errorf("failed to set design reference role: %w", err)
		}
		// A person moving a photo off a model's detail (to another slot, a view or «no view») may leave
		// that slot with nothing: it goes with its last photo (101 §2.6, §4.2).
		if _, err := dropOrphanModelSlots(ctx, db, req.TechCardId); err != nil {
			return err
		}
		if req.Role == "" {
			return nil
		}
		rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE tech_card_id = :card AND media_id = :media`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to read design reference: %w", err)
		}
		if len(rows) > 0 {
			r := rows[0]
			out = &r
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetReferenceHeld takes a labelled board picture OUT OF THE PROMPT, or puts it back (109 §4,
// «remove from prompt»). The label — the view, the detail slot — and the picture on the board stay:
// only label_state moves between ok and held, and only ok travels (entity.DesignReferenceTravels).
//
//	held = true   a row with a role, ok or held      → held (idempotent); a model detail left with no
//	                                                    photo that still travels loses its slot
//	              anything else (no row, no role,     → ErrDesignNothingToHold: that picture is not in
//	              pending / unsure / failed)            the prompt, there is nothing to take out
//	held = false  a held row whose detail slot is gone (the hold dropped the model's slot):
//	                a model's row                      → pending, labelled_at NULL, attempts 0 — the
//	                                                    next sync reads it again and mints anew (the
//	                                                    caller kicks the sync)
//	                a person's row                     → ok; the client asks «detail ?» (no slot)
//	              any other held row                   → ok
//	              a row that is not held               → unchanged (idempotent)
//
// The label source is never changed: a model's guess put back is still the model's (grey), a
// person's label is still the person's. Every write here is under the row's FOR UPDATE, so a model
// answer landing at the same moment (FinishBoardLabel writes only a PENDING model row) cannot
// overwrite a hold.
func (s *Store) SetReferenceHeld(ctx context.Context, req entity.DesignReferenceHold) (*entity.DesignReference, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.MediaId <= 0 {
		return nil, fmt.Errorf("%w: a hold needs a media id", entity.ErrDesignInvalidArgument)
	}
	var out *entity.DesignReference
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()
		rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE tech_card_id = :card AND media_id = :media FOR UPDATE`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to read the reference to hold: %w", err)
		}
		var r entity.DesignReference
		if len(rows) > 0 {
			r = rows[0]
		}
		state := entity.DesignLabelStateOrOk(r.LabelState)
		role := strings.TrimSpace(r.Role)
		if req.Held {
			if len(rows) == 0 || role == "" ||
				(state != entity.DesignLabelStateOk && state != entity.DesignLabelStateHeld) {
				return fmt.Errorf("%w: media %d of tech card %d has no settled label to take out of the prompt",
					entity.ErrDesignNothingToHold, req.MediaId, req.TechCardId)
			}
			if state == entity.DesignLabelStateOk {
				if err := storeutil.ExecNamed(ctx, db,
					`UPDATE design_reference SET label_state = :held WHERE id = :id`,
					map[string]any{"id": r.Id, "held": entity.DesignLabelStateHeld}); err != nil {
					return fmt.Errorf("failed to hold the reference: %w", err)
				}
				// A model's detail whose last travelling photo was just held goes with it (109 Q3); a
				// slot a person named or renamed (made_by_model = 0) stays whatever happens.
				if _, err := dropOrphanModelSlots(ctx, db, req.TechCardId); err != nil {
					return err
				}
			}
		} else if len(rows) > 0 && state == entity.DesignLabelStateHeld {
			lostSlot := role == entity.DesignViewDetail && !r.DetailSlotId.Valid
			switch {
			case lostSlot && entity.IsDesignLabelByModel(r.LabelSource):
				// The model's slot went with the hold: read the photo again (it joins a slot or mints
				// one). labelled_at NULL makes the pending row due at once — the sync's plan and
				// BeginBoardLabel both read a NULL labelled_at as a lost task, and attempts 0 gives it
				// the full two tries.
				if err := storeutil.ExecNamed(ctx, db, `
					UPDATE design_reference
					SET role = '', detail_slot_id = NULL, label_state = :pending,
						proposed_purpose = '', model_caption = NULL, label_model = '',
						label_attempts = 0, labelled_at = NULL
					WHERE id = :id AND label_source IN `+designModelSources,
					map[string]any{"id": r.Id, "pending": entity.DesignLabelStatePending}); err != nil {
					return fmt.Errorf("failed to re-arm the held reference: %w", err)
				}
			default:
				if err := storeutil.ExecNamed(ctx, db,
					`UPDATE design_reference SET label_state = :ok WHERE id = :id`,
					map[string]any{"id": r.Id, "ok": entity.DesignLabelStateOk}); err != nil {
					return fmt.Errorf("failed to put the reference back: %w", err)
				}
			}
		}
		if len(rows) == 0 {
			return nil
		}
		after, err := storeutil.QueryNamedOne[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE id = :id`, map[string]any{"id": r.Id})
		if err != nil {
			return fmt.Errorf("failed to re-read the reference: %w", err)
		}
		out = &after
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
