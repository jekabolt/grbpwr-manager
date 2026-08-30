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
const benchSlotUpsert = `
	INSERT INTO design_bench_slot
		(tech_card_id, view_key, kind, exclusive_key, detail_name, picture_id, slot_rev, set_by, set_at)
	VALUES
		(:card, :view, :kind, :excl, :name, :pic, 1, :who, UTC_TIMESTAMP(6))
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
	kind := entity.DesignKindOrFlat(req.Slot.Kind)
	if !entity.IsDesignPictureKind(kind) {
		return nil, fmt.Errorf("%w: unknown slot kind %q", entity.ErrDesignInvalidArgument, kind)
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
			same := (byID && h.Id == req.Slot.SlotId) ||
				(!byID && h.ExclusiveKey == req.Slot.ViewKey && entity.DesignKindOrFlat(h.Kind) == kind)
			if !same {
				return nil, fmt.Errorf("%w: picture %d already stands in slot %d",
					entity.ErrDesignPictureAlreadyInSlot, req.PictureId, h.Id)
			}
		}
	}

	switch {
	case byID:
		return casExistingSlot(ctx, db, req)
	case req.Slot.ViewKey == entity.DesignViewDetail:
		return createDetailSlot(ctx, db, req, kind)
	default:
		return upsertSilhouetteSlot(ctx, db, req, kind)
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
func upsertSilhouetteSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet, kind string) (*entity.DesignBenchSlot, error) {
	before, found, err := slotByKey(ctx, db, req.TechCardId, kind, req.Slot.ViewKey)
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
		"excl":         req.Slot.ViewKey,
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
			after, _, _ := slotByKey(ctx, db, req.TechCardId, kind, req.Slot.ViewKey)
			return &after, fmt.Errorf("%w: slot was born concurrently", entity.ErrDesignSlotRevMismatch)
		}
		return nil, fmt.Errorf("failed to set design bench slot: %w", err)
	}

	after, ok, err := slotByKey(ctx, db, req.TechCardId, kind, req.Slot.ViewKey)
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
func createDetailSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet, kind string) (*entity.DesignBenchSlot, error) {
	if req.NewDetailName == "" {
		return nil, fmt.Errorf("%w: a new detail slot needs a name", entity.ErrDesignDetailNameRequired)
	}
	if req.ExpectedSlotRev != 0 {
		return nil, fmt.Errorf("%w: a slot that does not exist yet is at rev 0", entity.ErrDesignSlotRevMismatch)
	}
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO design_bench_slot
			(tech_card_id, view_key, kind, exclusive_key, detail_name, picture_id, slot_rev, set_by, set_at)
		VALUES
			(:card, :view, :kind, :excl, :name, :pic, 1, :who, UTC_TIMESTAMP(6))`,
		map[string]any{
			"card": req.TechCardId,
			"view": entity.DesignViewDetail,
			"kind": kind,
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
func casExistingSlot(ctx context.Context, db dependency.DB, req entity.DesignBenchSlotSet) (*entity.DesignBenchSlot, error) {
	before, err := slotByID(ctx, db, req.Slot.SlotId)
	if err != nil {
		return nil, err
	}
	if before.TechCardId != req.TechCardId {
		return nil, fmt.Errorf("%w: slot %d belongs to tech card %d",
			entity.ErrDesignNotFound, before.Id, before.TechCardId)
	}
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
func slotByKey(ctx context.Context, db dependency.DB, cardID int, kind, exclusiveKey string) (entity.DesignBenchSlot, bool, error) {
	kind = entity.DesignKindOrFlat(kind)
	rows, err := storeutil.QueryListNamed[entity.DesignBenchSlot](ctx, db, `
		SELECT * FROM design_bench_slot
		WHERE tech_card_id = :card AND kind = :kind AND exclusive_key = :excl`,
		map[string]any{"card": cardID, "kind": kind, "excl": exclusiveKey})
	if err != nil {
		return entity.DesignBenchSlot{}, false, fmt.Errorf("failed to read design bench slot: %w", err)
	}
	if len(rows) == 0 {
		return entity.DesignBenchSlot{
			ViewKey: exclusiveKey, Kind: kind, ExclusiveKey: exclusiveKey, TechCardId: cardID,
		}, false, nil
	}
	return rows[0], true, nil
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

// DeleteDetailSlot removes an EMPTY detail slot that no version quotes.
//
// The version check cannot be a foreign key. design_sheet_version_plate.slot_id is ON DELETE SET
// NULL on purpose: both the slot and the version cascade from tech_card, so a RESTRICT here
// would make DeleteTechCard — one bare DELETE FROM tech_card — die on 1451 with nothing the
// caller could remove. So the refusal lives in Go, in the same transaction as the delete.
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
		versions, err := storeutil.QueryScalarListNamed[int](ctx, rep.DB(), `
			SELECT DISTINCT v.version_number
			FROM design_sheet_version_plate p
			JOIN design_sheet_version v ON v.id = p.version_id
			WHERE p.slot_id = :id
			ORDER BY v.version_number`,
			map[string]any{"id": slotID})
		if err != nil {
			return fmt.Errorf("failed to check versions quoting design slot %d: %w", slotID, err)
		}
		if len(versions) > 0 {
			return fmt.Errorf("%w: slot %d is quoted by versions %v",
				entity.ErrDesignSlotInVersion, slotID, versions)
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
