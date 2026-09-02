package design

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// benchSlotUpsert is the LAZY BIRTH of one of the four silhouette sides, and the compare-and-set
// of that side, in ONE statement.
//
// WHY NOT "SELECT, no row, INSERT". Two people putting a plate on `front` at the same time both
// see "no row", both insert, and the second gets 1062 — an error that is in no taxonomy and that
// the client will not roll back, because what it is waiting for is Aborted: slot_rev_mismatch.
// The precedent is verbatim: a second fitting on one sample died on 1062 in exactly this shape.
//
// ⚠ THE ORDER OF THE ASSIGNMENTS IS LOAD-BEARING, and the form printed in the plan (11 §1.3) has
// it wrong. MySQL evaluates ON DUPLICATE KEY UPDATE assignments LEFT TO RIGHT, and every later
// expression sees the value an earlier one just wrote. With slot_rev assigned second, the
// `slot_rev = :expected_rev` guard in set_by and set_at is compared against the ALREADY
// INCREMENTED revision, is false, and the two stamps are silently left at the previous author
// and the previous time — on a CAS that SUCCEEDED. slot_rev is assigned LAST here for exactly
// that reason. Moving it back up reintroduces a defect that no round trip would show, because
// the picture does land in the slot; only the byline lies.
//
// ⚠ `kind` УЧАСТВУЕТ В INSERT И НЕ УЧАСТВУЕТ В ON DUPLICATE KEY UPDATE, и это не пропуск. Род —
// ЧАСТЬ АДРЕСА (uq_design_bench_view = tech_card_id, kind, exclusive_key после 0349), а адрес
// строки не переписывается её же upsert'ом: строка, на которую он разрешился, УЖЕ имеет тот род,
// по которому её нашли. Присвоение `kind = VALUES(kind)` было бы тождеством в лучшем случае и
// переездом строки между осями в худшем.
//
// ИМЯ КЛЮЧА uq_design_bench_view СОХРАНЕНО 0349-й намеренно: mysqlDupKey разбирает 1062 ПО ИМЕНИ
// ключа, отличая «слот тронули» от «плита занята». Переименование ключа молча схлопнуло бы два
// разных отказа в один.
// ⚠ `colorway_id` — ТОЖЕ ЧАСТЬ АДРЕСА и тоже не участвует в ON DUPLICATE KEY UPDATE, по тому же
// доводу, что kind: он закодирован в exclusive_key (DesignBenchExclusiveKey), строка, на которую
// разрешился upsert, уже несёт тот колорвей, по которому её нашли, а колонка рядом — читаемая
// половина того же факта, записанная тем же INSERT из того же значения.
const benchSlotUpsert = `
	INSERT INTO design_bench_slot
		(tech_card_id, view_key, kind, colorway_id, exclusive_key, detail_name, picture_id, slot_rev, set_by, set_at)
	VALUES
		(:card, :view, :kind, :cw, :excl, :name, :pic, 1, :who, UTC_TIMESTAMP(6))
	ON DUPLICATE KEY UPDATE
		picture_id  = IF(slot_rev = :expected_rev, VALUES(picture_id), picture_id),
		detail_name = IF(slot_rev = :expected_rev, VALUES(detail_name), detail_name),
		set_by      = IF(slot_rev = :expected_rev, VALUES(set_by), set_by),
		set_at      = IF(slot_rev = :expected_rev, VALUES(set_at), set_at),
		slot_rev    = IF(slot_rev = :expected_rev, slot_rev + 1, slot_rev)`

// SetBenchSlot places, displaces or unmarks a plate under compare-and-set on slot_rev, and
// gives birth to one of the four silhouette slots on first touch.
//
// UNMARK IS PictureId == 0 — the slot is emptied, not deleted. Emptying a slot and deleting a
// detail slot are two different acts and stay two different verbs (DeleteDetailSlot).
//
// THE DISPLACED PLATE IS NOT RETURNED, and that is a decision rather than an omission: the
// response message carries one field, `slot`, and the displaced picture is still in the band
// under its own run row, which the caller already holds. Returning it would need a contract
// field that does not exist.
func (s *Store) SetBenchSlot(ctx context.Context, req entity.DesignBenchSlotSet) (*entity.DesignBenchSlot, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	var out entity.DesignBenchSlot
	var mismatch *entity.DesignBenchSlot
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		slot, err := setBenchSlotTx(ctx, rep, req)
		if err != nil {
			// The refusal carries the slot's CURRENT state, plate included, so the client can
			// show the person what actually stands there instead of only saying «reload».
			if slot != nil {
				_ = attachSlotPictures(ctx, rep, []*entity.DesignBenchSlot{slot})
				mismatch = slot
			}
			return err
		}
		if err := attachSlotPictures(ctx, rep, []*entity.DesignBenchSlot{slot}); err != nil {
			return err
		}
		out = *slot
		return nil
	})
	if err != nil {
		return mismatch, err
	}
	return &out, nil
}

// attachSlotPictures resolves the plate standing in each slot, with its media, in ONE batch.
//
// A slot's plate is routinely an OLD upload that sits outside the band's first page, so a bare
// picture_id leaves the slot with no thumbnail and no source_class — and source_class is exactly
// what the mixed-provenance warning is computed from. The join is not decoration.
func attachSlotPictures(ctx context.Context, rep dependency.Repository, slots []*entity.DesignBenchSlot) error {
	ids := make([]int, 0, len(slots))
	for _, sl := range slots {
		if sl != nil && sl.PictureId.Valid && sl.PictureId.Int32 > 0 {
			ids = append(ids, int(sl.PictureId.Int32))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, rep.DB(),
		`SELECT * FROM design_picture WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("failed to resolve design bench plates: %w", err)
	}
	byID := make(map[int]*entity.DesignPicture, len(rows))
	flat := make([]*entity.DesignPicture, 0, len(rows))
	for i := range rows {
		byID[rows[i].Id] = &rows[i]
		flat = append(flat, &rows[i])
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return err
	}
	for _, sl := range slots {
		if sl == nil || !sl.PictureId.Valid {
			continue
		}
		if p, ok := byID[int(sl.PictureId.Int32)]; ok {
			cp := *p
			sl.Picture = &cp
		}
	}
	return nil
}

// setBenchSlotTx is the body, split out because RegisterBatch performs the very same placement
// in ITS transaction (one gesture, two facts, one transaction). Two copies of a CAS is how the
// two of them drift apart.
func setBenchSlotTx(ctx context.Context, rep dependency.Repository, req entity.DesignBenchSlotSet) (*entity.DesignBenchSlot, error) {
	db := rep.DB()
	byID := req.Slot.SlotId != 0
	if !byID && req.Slot.ViewKey == "" {
		return nil, fmt.Errorf("%w: slot must be addressed by view_key or slot_id", entity.ErrDesignInvalidArgument)
	}
	if !byID && !entity.IsDesignGhostView(req.Slot.ViewKey) {
		return nil, fmt.Errorf("%w: unknown view_key %q", entity.ErrDesignInvalidArgument, req.Slot.ViewKey)
	}
	// РОД — ВТОРАЯ ПОЛОВИНА АДРЕСА (0349). Пустое читается как flat ровно как DEFAULT колонки,
	// поэтому всякий писатель, который род не именует, попадает туда же, куда попадал до
	// миграции; и ровно поэтому запрос про рендер фронта перестал разрешаться на флэт фронта.
	//
	// ⚠ СПРАШИВАЕТСЯ IsDesignBenchKind, А НЕ IsDesignPictureKind, И РАЗНИЦА ПОЯВИЛАСЬ ВМЕСТЕ С
	// `pattern`: плитка обоев — законный кадр карточки и НЕ состояние изделия ни с какой стороны.
	// Список принимаемых значений при этом не изменился ни на один член, так что ни одна уже
	// стоящая строка верстака смысла не поменяла.
	kind := entity.DesignKindOrFlat(req.Slot.Kind)
	if !entity.IsDesignBenchKind(kind) {
		return nil, fmt.Errorf("%w: unknown slot kind %q", entity.ErrDesignInvalidArgument, kind)
	}
	// ─── ВТОРАЯ ПОЛОВИНА ВТОРОЙ ОСИ: КОЛОРВЕЙ АДРЕСА (0356) ───
	//
	// Рендер-верстак живёт НА КОЛОРВЕЙ: front колорвея A и front колорвея B — два слота, и
	// различает их exclusive_key, куда колорвей закодирован (entity.DesignBenchExclusiveKey).
	// Флэтовому адресу колорвей ОТКАЗЫВАЕТСЯ, а не молча обнуляется: чертёж изделия один на все
	// цвета (L-4), и «флэт колорвея 5» не должен быть выразим ни через одну дверь записи.
	// ⚠ НОЛЬ У ЗАПРОСА — ЭТО «НЕ НАЗВАЛ», А НЕ «НАЗВАЛ БЕЗКОЛОРВЕЙНЫЙ» (entity.DesignColorwayRef).
	// Здесь, на прямой двери постановки, оба намерения дают один и тот же АДРЕС, и различать их
	// незачем; различает их вызывающий составного жеста (RegisterBatch) и адресация по id ниже.
	if !req.Slot.ColorwayId.Valid() {
		return nil, fmt.Errorf("%w: colorway_id %d is neither a colourway, 0 (not stated) nor %d (the colourway-less bench)",
			entity.ErrDesignInvalidArgument, int(req.Slot.ColorwayId), entity.DesignColorwayUnattributed)
	}
	cw := req.Slot.ColorwayId.Id()
	if !byID {
		if cw > 0 && !entity.DesignPictureKindTakesColorway(kind) {
			return nil, fmt.Errorf("%w: the %s bench has no colourway axis — a flat is one markup for the whole card",
				entity.ErrDesignColorwayForbidden, kind)
		}
		// ГРАНИЦА КАРТОЧКИ — В ЭТОЙ ЖЕ ТРАНЗАКЦИИ, как у плиты ниже: колорвей после 0151 — строка
		// product со style_id карточки, и чужой id замёрз бы в верстаке правдоподобной,
		// но ложной атрибуцией.
		if cw > 0 {
			if err := assertColorwayOfCard(ctx, db, req.TechCardId, cw); err != nil {
				return nil, err
			}
		}
	}

	// ⚠ АДРЕС ПО slot_id НАЗЫВАЕТ СВОЙ ВЕРСТАК САМ, и род запроса при нём ИГНОРИРУЕТСЯ — это
	// слово контракта («a minted id already names its bench, and a kind disagreeing with it would
	// be a contradiction nobody could adjudicate»), а не удобство.
	//
	// ПОЧЕМУ СТРОКА ЧИТАЕТСЯ ЗДЕСЬ, А НЕ В casExistingSlot. Ниже стоит единственный сторож,
	// который вообще сверяет род кадра с родом слота, и сравнивать ему при адресации по id не с
	// чем, кроме РОДА САМОЙ СТРОКИ. Взять род из запроса значило бы завести ровно два новых
	// молчаливых исхода: замена плиты в рендер-слоте по id без рода — ЛОЖНЫЙ ОТКАЗ (`render` кадр
	// против подставленного `flat`), а `slot_id` рендер-слота с родом `flat` — ФЛЭТ, ПРИНЯТЫЙ В
	// РЕНДЕР-СЛОТ, потому что сам casExistingSlot род не проверяет вовсе. Второй исход тише
	// первого и дороже: он портит верстак, с которого строится 3D.
	var existing *entity.DesignBenchSlot
	if byID {
		before, err := slotByID(ctx, db, req.Slot.SlotId)
		if err != nil {
			return nil, err
		}
		if before.TechCardId != req.TechCardId {
			return nil, fmt.Errorf("%w: slot %d belongs to tech card %d",
				entity.ErrDesignNotFound, before.Id, before.TechCardId)
		}
		existing = &before
		kind = entity.DesignKindOrFlat(before.Kind)
		// КОЛОРВЕЙ ПРИ АДРЕСАЦИИ ПО id БЕРЁТСЯ У СТРОКИ — иначе замена плиты в колорвейном слоте
		// по id (сегодняшний клиент колорвея при этом не шлёт) ловила бы ЛОЖНЫЙ mismatch.
		//
		// ⚠ НО НАЗВАННЫЙ И НЕСОГЛАСНЫЙ — ОТКАЗЫВАЕТСЯ, А НЕ ВЫБРАСЫВАЕТСЯ (D2). «Рассудить
		// некому» было неправдой: строка слота лежит перед нами, и она знает и свой род, и свой
		// колорвей. Тихо принять `slot_id` флэтового слота с `colorway_id: 5` значит ответить OK
		// на просьбу, которой никто не исполнил, — та же молчаливая потеря, от которой эта волна
		// отказалась на двери загрузки и на двери прогона. Два исхода, ровно как у двери по виду:
		// у безосного верстака положительный колорвей — colorway_forbidden, у осного несогласный —
		// colorway_mismatch (сюда же попадает названный сентинелом безколорвейный верстак против
		// именованного слота: `-1` против `cw:5` — противоречие, а не умолчание).
		rowCw := entity.DesignColorwayOrNone(before.ColorwayId)
		if req.Slot.ColorwayId.Stated() {
			if cw > 0 && !entity.DesignPictureKindTakesColorway(kind) {
				return nil, fmt.Errorf("%w: slot %d is a %s slot and has no colourway axis, but colourway %d was named",
					entity.ErrDesignColorwayForbidden, before.Id, kind, cw)
			}
			if cw != rowCw {
				return nil, fmt.Errorf("%w: slot %d stands on colourway %d and colourway %d was named (0 = none)",
					entity.ErrDesignColorwayMismatch, before.Id, rowCw, cw)
			}
		}
		cw = rowCw
	}

	// The plate, and the four refusals that belong to it. Every one of them is read in THIS
	// transaction: a guard read outside it is a TOCTOU with a nicer name.
	if req.PictureId != 0 {
		pic, err := pictureByID(ctx, db, req.PictureId)
		if err != nil {
			return nil, err
		}
		// SCHEMA CANNOT EXPRESS THIS. A composite FK (tech_card_id, picture_id) would, but its
		// ON DELETE would have to be CASCADE — both columns are NOT NULL — and a detail slot is
		// required to survive the disappearance of its plate. So Go checks it, here, in the same
		// transaction as the write.
		if pic.TechCardId != req.TechCardId {
			return nil, fmt.Errorf("%w: picture %d belongs to tech card %d",
				entity.ErrDesignForeignCardPlate, pic.Id, pic.TechCardId)
		}
		if len(pic.CompositeViews) > 0 && string(pic.CompositeViews) != "null" && string(pic.CompositeViews) != "[]" {
			return nil, fmt.Errorf("%w: picture %d is a composite and must be split first",
				entity.ErrDesignCompositePlate, pic.Id)
		}
		if pic.HiddenAt.Valid {
			return nil, fmt.Errorf("%w: picture %d is hidden", entity.ErrDesignHiddenPlate, pic.Id)
		}
		// РОД КАДРА ОБЯЗАН СОВПАСТЬ С РОДОМ СЛОТА, и это ЗАМЕНА прежнему «threed в слот не
		// встаёт» — не ослабление его, а обобщение. Пока ось была одна, единственным способом
		// сказать «этот кадр сюда не относится» было назвать один запретный род; но настоящая
		// беда была не в турнтейбле, а в РЕНДЕРЕ: он вставал на тот же адрес, что флэт, вытеснял
		// его, и минт печатал рендер на техническом листе. Теперь адресов два, и правило — одно.
		//
		// Для писателя, который род не именует (kind = flat), поведение НЕ ИЗМЕНИЛОСЬ: threed
		// по-прежнему отказывают, флэт по-прежнему принимают. Изменилось ровно то, что рендер
		// теперь отказывают во флэт-слоте вместо того, чтобы принять и испортить лист.
		if pic.Kind != kind {
			return nil, fmt.Errorf("%w: picture %d is %q and the slot is %q",
				entity.ErrDesignWrongKind, pic.Id, pic.Kind, kind)
		}
		// КОЛОРВЕЙ ПЛИТЫ ОБЯЗАН СОВПАСТЬ С КОЛОРВЕЕМ СЛОТА — вторая ось того же сторожа. Рендер
		// колорвея A в верстаке B печатал бы на листе B чужой цвет; и НЕатрибутированная плита в
		// именованном верстаке — тоже отказ, в обе стороны: постановка не выдумывает атрибуцию,
		// которой у кадра нет, и не стирает ту, которая есть. Легаси-плиты (colorway NULL) при
		// этом остаются полноценными жителями своего, безколорвейного верстака.
		if picCw := entity.DesignColorwayOrNone(pic.ColorwayId); picCw != cw {
			return nil, fmt.Errorf("%w: picture %d belongs to colourway %d and the slot to colourway %d (0 = none)",
				entity.ErrDesignColorwayMismatch, pic.Id, picCw, cw)
		}
	}

	// WHERE THE PLATE ALREADY STANDS — and this check is REQUIRED FOR CORRECTNESS, not for a
	// nicer sentence. design_bench_slot carries TWO unique keys, and INSERT … ON DUPLICATE KEY
	// UPDATE updates WHICHEVER ROW COLLIDED. If plate P already sits in `back` and somebody
	// inserts it into `front`, the insert collides on uq_design_bench_picture — on the `back`
	// row — and the ON DUPLICATE branch would mutate `back` instead of `front`. Refusing here
	// keeps the upsert reachable only through uq_design_bench_view.
	if req.PictureId != 0 {
		holder, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db, `
			SELECT * FROM design_bench_slot WHERE tech_card_id = :card AND picture_id = :pic`,
			map[string]any{"card": req.TechCardId, "pic": req.PictureId})
		if err != nil {
			return nil, fmt.Errorf("failed to check where design plate %d stands: %w", req.PictureId, err)
		}
		for _, h := range holder {
			// Тот же адрес — та же ТРОЙКА (род, эксклюзивный ключ), где ключ уже несёт колорвей:
			// сравнение с голым view_key считало бы слот колорвея «другим» слотом самого себя.
			same := (byID && h.Id == req.Slot.SlotId) ||
				(!byID && h.ExclusiveKey == entity.DesignBenchExclusiveKey(req.Slot.ViewKey, cw) &&
					entity.DesignKindOrFlat(h.Kind) == kind)
			if !same {
				return nil, fmt.Errorf("%w: picture %d already stands in slot %d",
					entity.ErrDesignPictureAlreadyInSlot, req.PictureId, h.Id)
			}
		}
	}

	switch {
	case byID:
		return casExistingSlot(ctx, db, req, *existing)
	case req.Slot.ViewKey == entity.DesignViewDetail:
		return createDetailSlot(ctx, db, req, kind, cw)
	default:
		return upsertSilhouetteSlot(ctx, db, req, kind, cw)
	}
}

// upsertSilhouetteSlot handles one of the four sides: lazy birth plus CAS in one statement.
//
// The verdict is NOT taken by comparing the re-read revision against expected+1. That comparison
// is ambiguous: a slot already sitting at rev 4 while the caller echoes 3 would satisfy
// "rev == expected+1" without a single byte having been written, and the caller would be told
// its stale placement succeeded. The pre-read inside this SERIALIZABLE transaction is
// authoritative, so the base revision is known, and the verdict is "the row moved from the base
// I actually read".
func upsertSilhouetteSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet, kind string, cw int) (*entity.DesignBenchSlot, error) {
	before, found, err := slotByKey(ctx, db, req.TechCardId, kind, cw, req.Slot.ViewKey)
	if err != nil {
		return nil, err
	}
	baseRev := 0
	if found {
		baseRev = before.SlotRev
	}
	if baseRev != req.ExpectedSlotRev {
		return &before, revMismatch(before, found, req.ExpectedSlotRev)
	}

	name := any(nil)
	if req.NewDetailName != "" {
		name = req.NewDetailName
	} else if found && before.DetailName.Valid {
		name = before.DetailName.String
	}

	_, err = storeutil.ExecNamedRows(ctx, db, benchSlotUpsert, map[string]any{
		"card":         req.TechCardId,
		"view":         req.Slot.ViewKey,
		"kind":         kind,
		"cw":           nullInt(cw),
		"excl":         entity.DesignBenchExclusiveKey(req.Slot.ViewKey, cw),
		"name":         name,
		"pic":          nullInt(req.PictureId),
		"who":          req.Actor,
		"expected_rev": req.ExpectedSlotRev,
	})
	if err != nil {
		// The residual 1062 is a BELT, not the mechanism. uq_design_bench_picture is checked in
		// Go above and can only fire here on a concurrent placement; uq_design_bench_view can
		// only fire if the ON DUPLICATE branch is ever removed. Both map to a refusal the client
		// already knows how to undo.
		if key, dup := mysqlDupKey(err); dup {
			if key == "uq_design_bench_picture" {
				return nil, fmt.Errorf("%w: picture %d was placed elsewhere concurrently",
					entity.ErrDesignPictureAlreadyInSlot, req.PictureId)
			}
			after, _, _ := slotByKey(ctx, db, req.TechCardId, kind, cw, req.Slot.ViewKey)
			return &after, fmt.Errorf("%w: slot was born concurrently", entity.ErrDesignSlotRevMismatch)
		}
		return nil, fmt.Errorf("failed to set design bench slot: %w", err)
	}

	after, ok, err := slotByKey(ctx, db, req.TechCardId, kind, cw, req.Slot.ViewKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("failed to re-read design bench slot after upsert")
	}
	if after.SlotRev != baseRev+1 {
		return &after, revMismatch(after, true, req.ExpectedSlotRev)
	}
	return &after, nil
}

// createDetailSlot mints a NEW detail slot.
//
// ITS exclusive_key IS A MINTED UUID, never the name. "Detail 1 / detail 2" as a key would move
// a plate on rename, and two details a human called the same thing must still be two slots — so
// the lazy-birth upsert deliberately does not apply here: two people naming a detail at the same
// moment legitimately create two slots, and collapsing them would be the bug.
func createDetailSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet, kind string, cw int) (*entity.DesignBenchSlot, error) {
	if req.NewDetailName == "" {
		return nil, fmt.Errorf("%w: a new detail slot needs a name", entity.ErrDesignDetailNameRequired)
	}
	if req.ExpectedSlotRev != 0 {
		return nil, fmt.Errorf("%w: a slot that does not exist yet is at rev 0", entity.ErrDesignSlotRevMismatch)
	}
	// Ключ детали остаётся минтованным uuid — он эксклюзивен сам по себе, и колорвей ему для
	// единственности не нужен; колонка при этом записывает, ЧЬЕМУ верстаку деталь принадлежит.
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO design_bench_slot
			(tech_card_id, view_key, kind, colorway_id, exclusive_key, detail_name, picture_id, slot_rev, set_by, set_at)
		VALUES
			(:card, :view, :kind, :cw, :excl, :name, :pic, 1, :who, UTC_TIMESTAMP(6))`,
		map[string]any{
			"card": req.TechCardId,
			"view": entity.DesignViewDetail,
			"kind": kind,
			"cw":   nullInt(cw),
			"excl": "detail:" + uuid.NewString(),
			"name": req.NewDetailName,
			"pic":  nullInt(req.PictureId),
			"who":  req.Actor,
		})
	if err != nil {
		if key, dup := mysqlDupKey(err); dup && key == "uq_design_bench_picture" {
			return nil, fmt.Errorf("%w: picture %d already stands in a slot",
				entity.ErrDesignPictureAlreadyInSlot, req.PictureId)
		}
		return nil, fmt.Errorf("failed to create design detail slot: %w", err)
	}
	slot, err := slotByID(ctx, db, id)
	if err != nil {
		return nil, err
	}
	return &slot, nil
}

// casExistingSlot updates a slot addressed by its own id under compare-and-set. RowsAffected
// answers unambiguously here — the DSN carries no clientFoundRows, so a row that did not change
// counts zero — and the re-read is only for the payload.
//
// `before` ПРИХОДИТ ПАРАМЕТРОМ, а не читается второй раз: setBenchSlotTx уже прочитал эту строку —
// род слота ему нужен ДО сторожа рода кадра, — и второе чтение той же строки в той же транзакции
// было бы вторым источником правды о её роде.
func casExistingSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet, before entity.DesignBenchSlot) (*entity.DesignBenchSlot, error) {
	if before.SlotRev != req.ExpectedSlotRev {
		return &before, revMismatch(before, true, req.ExpectedSlotRev)
	}
	name := any(nil)
	if req.NewDetailName != "" {
		name = req.NewDetailName
	} else if before.DetailName.Valid {
		name = before.DetailName.String
	}
	n, err := storeutil.ExecNamedRows(ctx, db, `
		UPDATE design_bench_slot
		SET picture_id = :pic, detail_name = :name, set_by = :who, set_at = UTC_TIMESTAMP(6),
			slot_rev = slot_rev + 1
		WHERE id = :id AND slot_rev = :expected_rev`,
		map[string]any{
			"pic": nullInt(req.PictureId), "name": name, "who": req.Actor,
			"id": req.Slot.SlotId, "expected_rev": req.ExpectedSlotRev,
		})
	if err != nil {
		if key, dup := mysqlDupKey(err); dup && key == "uq_design_bench_picture" {
			return nil, fmt.Errorf("%w: picture %d already stands in a slot",
				entity.ErrDesignPictureAlreadyInSlot, req.PictureId)
		}
		return nil, fmt.Errorf("failed to update design bench slot %d: %w", req.Slot.SlotId, err)
	}
	if n == 0 {
		after, rerr := slotByID(ctx, db, req.Slot.SlotId)
		if rerr != nil {
			return nil, rerr
		}
		return &after, revMismatch(after, true, req.ExpectedSlotRev)
	}
	after, err := slotByID(ctx, db, req.Slot.SlotId)
	if err != nil {
		return nil, err
	}
	return &after, nil
}

// revMismatch names the refusal the client knows how to undo, and carries the slot's CURRENT
// state so the handler can put it in the status details — reloading the whole band to learn one
// revision is a round trip nobody needs.
func revMismatch(slot entity.DesignBenchSlot, found bool, expected int) error {
	if !found {
		return fmt.Errorf("%w: slot does not exist yet but rev %d was echoed",
			entity.ErrDesignSlotRevMismatch, expected)
	}
	return fmt.Errorf("%w: slot is at rev %d, %d was echoed",
		entity.ErrDesignSlotRevMismatch, slot.SlotRev, expected)
}

// slotByKey reads ONE slot by its FULL address — the card, the KIND and the exclusive key.
//
// ⚠ THE KIND IS PART OF THE PREDICATE, NOT OF THE RESULT. Before 0349 the address was
// (card, exclusive_key) and the set was one-element by UNIQUE; after 0349 it is
// (card, kind, exclusive_key), and dropping `kind` from the WHERE would silently make this a
// LIST — `rows[0]` would then answer «the render of front» with the FLAT of front, or the other
// way round, depending on insertion order. Nothing would fail; the wrong plate would just be
// read, compared and CAS-ed. The comparison is made against the normalised kind so that rows
// written before the migration (kind = 'flat' by DEFAULT) stay reachable by a caller that names
// no kind at all.
// Колорвей — ТРЕТЬЯ ЧАСТЬ ТОГО ЖЕ АДРЕСА (0356), и в предикат он входит не отдельной колонкой, а
// внутри exclusive_key: DesignBenchExclusiveKey возвращает голый вид при colorwayID == 0 — байт в
// байт легаси-ключ, — поэтому каждый слот, рождённый до оси, остаётся достижим тем же адресом.
func slotByKey(ctx context.Context, db dependency.DB, cardID int, kind string, colorwayID int, viewKey string) (entity.DesignBenchSlot, bool, error) {
	kind = entity.DesignKindOrFlat(kind)
	excl := entity.DesignBenchExclusiveKey(viewKey, colorwayID)
	rows, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db, `
		SELECT * FROM design_bench_slot
		WHERE tech_card_id = :card AND kind = :kind AND exclusive_key = :excl`,
		map[string]any{"card": cardID, "kind": kind, "excl": excl})
	if err != nil {
		return entity.DesignBenchSlot{}, false, fmt.Errorf("failed to read design bench slot: %w", err)
	}
	if len(rows) == 0 {
		phantom := entity.DesignBenchSlot{
			ViewKey: viewKey, Kind: kind, ExclusiveKey: excl, TechCardId: cardID,
		}
		if colorwayID > 0 {
			phantom.ColorwayId = sql.NullInt32{Int32: int32(colorwayID), Valid: true}
		}
		return phantom, false, nil
	}
	return rows[0], true, nil
}

// assertColorwayOfCard — граница карточки для названного колорвея, в ТОЙ ЖЕ транзакции, что
// запись. Колорвей после слияния доменов (0151) — строка product, а принадлежность стилю несёт
// `style_id` (0138: «every product belongs to a style», FK RESTRICT) — ровно та колонка, по
// которой ходит CRUD колорвеев (colorway_write.go). НЕ primary_tech_card_id: то — старое
// SET NULL-зеркало для экономики, и оно законно пусто у строк, заведённых до зеркала.
// ⚠ АРХИВНЫЙ КОЛОРВЕЙ ПРИНИМАЕТСЯ, И ЭТО РЕШЕНИЕ (F5). Предиката по lifecycle_status/deleted_at
// здесь НЕТ намеренно: `DeleteProductById` — МЯГКОЕ удаление, архив это состояние ВИТРИНЫ, а не
// исчезновение строки, и полоса DESIGN живёт до витрины, а не после неё. Рендерить и хранить
// кадры архивного цвета законно ровно потому, что архив и заводят, чтобы перестать ПРОДАВАТЬ, —
// а не чтобы перестать проектировать: цвет, снятый с продажи, регулярно возвращается следующим
// сезоном, и запрет на его рендер сделал бы возврат дороже, чем заведение нового колорвея.
// Обратное правило (отказывать архивным) стоило бы дороже и молча: замороженные params рерана
// называют id, и прогон становился бы неповторимым в тот день, когда цвет архивируют.
// Единственное состояние, которое здесь ОТКАЗЫВАЕТСЯ, — чужая карточка: атрибуция принадлежит
// стилю, а не витрине.
func assertColorwayOfCard(ctx context.Context, db dependency.DB, cardID, colorwayID int) error {
	owner, exists, err := colorwayOwnerCard(ctx, db, colorwayID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: colourway %d does not exist", entity.ErrDesignForeignColorway, colorwayID)
	}
	if owner != cardID {
		return fmt.Errorf("%w: colourway %d does not belong to tech card %d",
			entity.ErrDesignForeignColorway, colorwayID, cardID)
	}
	return nil
}

// colorwayOwnerCard — ЧЬЯ ЭТО СТРОКА И ЕСТЬ ЛИ ОНА ВООБЩЕ, двумя РАЗЛИЧИМЫМИ ответами.
//
// Различие нужно ровно одному вызывающему — старту прогона (F2): «колорвея нет вовсе» и «колорвей
// чужой» требуют РАЗНЫХ исходов у УНАСЛЕДОВАННОГО параметра, и слепив их в один отказ, мы делали
// реран невозможным навсегда. Всем остальным дверям различие не нужно, и они ходят через
// assertColorwayOfCard.
//
// owner = 0 у строки без style_id (законно у продуктов, заведённых до 0138), и он не совпадёт ни
// с одной карточкой: requireCard не пускает нулевой id.
func colorwayOwnerCard(ctx context.Context, db dependency.DB, colorwayID int) (owner int, exists bool, err error) {
	rows, err := storeutil.QueryListNamed[struct {
		Card sql.NullInt32 `db:"style_id"`
	}](ctx, db, `SELECT style_id FROM product WHERE id = :id`,
		map[string]any{"id": colorwayID})
	if err != nil {
		return 0, false, fmt.Errorf("failed to resolve colourway %d: %w", colorwayID, err)
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	return int(rows[0].Card.Int32), true, nil
}

func slotByID(ctx context.Context, db dependency.DB, id int) (entity.DesignBenchSlot, error) {
	slot, err := storeutil.QueryNamedOne[entity.DesignBenchSlot](ctx, db,
		`SELECT * FROM design_bench_slot WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return slot, fmt.Errorf("%w: design bench slot %d", entity.ErrDesignNotFound, id)
		}
		return slot, fmt.Errorf("failed to read design bench slot %d: %w", id, err)
	}
	return slot, nil
}

// DeleteDetailSlot removes an EMPTY detail slot.
//
// Both refusals live in Go rather than in the schema, and in the SAME transaction as the delete:
// «is this a detail» is not expressible as a constraint at all, and «is it empty» read outside the
// transaction would be a TOCTOU against a concurrent placement.
func (s *Store) DeleteDetailSlot(ctx context.Context, slotID int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		slot, err := slotByID(ctx, rep.DB(), slotID)
		if err != nil {
			return err
		}
		// A silhouette side is an address of the garment, not a thing a person made; deleting it
		// would mean the next placement silently re-creates it at rev 1 and every open client's
		// CAS token becomes wrong. The plan does not name this refusal — it is added here rather
		// than left as a silent success.
		if slot.ViewKey != entity.DesignViewDetail {
			return fmt.Errorf("%w: slot %d is the %s side of the garment",
				entity.ErrDesignNotADetailSlot, slot.Id, slot.ViewKey)
		}
		if slot.PictureId.Valid {
			return fmt.Errorf("%w: slot %d still holds a plate", entity.ErrDesignSlotFilled, slot.Id)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM design_bench_slot WHERE id = :id`, map[string]any{"id": slotID}); err != nil {
			return fmt.Errorf("failed to delete design detail slot %d: %w", slotID, err)
		}
		return nil
	})
}

// listBenchSlots reads the whole bench of a card: the sides that have been touched plus every
// detail slot. Untouched sides are simply absent — they are born on first placement.
func listBenchSlots(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignBenchSlot, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db, `
		SELECT * FROM design_bench_slot WHERE tech_card_id = :card ORDER BY kind, view_key, id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design bench slots: %w", err)
	}
	return rows, nil
}
