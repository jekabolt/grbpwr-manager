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
	if req.Role != "" && !entity.IsDesignGhostView(req.Role) {
		return nil, fmt.Errorf("%w: unknown reference role %q", entity.ErrDesignInvalidArgument, req.Role)
	}
	var out *entity.DesignReference
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		// ГРАНИЦА ЗДЕСЬ ОТРИЦАТЕЛЬНАЯ — «медиа не принадлежит ДРУГОЙ карточке», refuseForeignMedia,
		// та же и в той же транзакции, что у ImportVector. Положительное правило, стоявшее тут
		// раньше («лежит в tech_card_media ЭТОЙ карточки»), ЛОЖНО ОТКАЗЫВАЛО на двух законных
		// жестах (R-10, R-11): картинка входа, которую клиент положил только в НЕСОХРАНЁННУЮ форму
		// (tech_card_media переписывается лишь сейвом всей карточки, поэтому между «добавил» и
		// «нажал Save» любой выбор роли был обречён), и кроп сплита, который живёт в design_picture
		// и в tech_card_media не попадает никогда.
		//
		// Ослабление обязано остаться УЗКИМ: держателей у границы ровно два (tech_card_media ∪
		// design_picture), и медиа, которое держит ЧУЖАЯ карточка и не держит эта, получает отказ
		// по-прежнему — иначе роль скармливала бы модели чужую картинку. Это закреплено отдельной
		// пробой (TestDesignDBReferenceRoleStillRefusesForeignCard); убрать вызов ниже — она
		// краснеет первой.
		if err := refuseForeignMedia(ctx, db, req.TechCardId, req.MediaId); err != nil {
			return err
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

		if req.Role == "" {
			if err := storeutil.ExecNamed(ctx, db,
				`DELETE FROM design_reference WHERE tech_card_id = :card AND media_id = :media`,
				map[string]any{"card": req.TechCardId, "media": req.MediaId}); err != nil {
				return fmt.Errorf("failed to clear design reference role: %w", err)
			}
			return nil
		}

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
				(tech_card_id, media_id, role, note, detail_slot_id, ordinal, set_by, set_at)
			VALUES (:card, :media, :role, :note, :slot, :ord, :who, UTC_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE
				role = VALUES(role),
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
			}); err != nil {
			return fmt.Errorf("failed to set design reference role: %w", err)
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
