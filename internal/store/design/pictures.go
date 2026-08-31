package design

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// RegisterBatch files ONE upload gesture as one batch plus its pictures, and — when a target slot
// is given — places the first picture into that slot under the SAME compare-and-set as an
// ordinary placement. One gesture, two facts, one transaction.
//
// IDEMPOTENCY IS THE client_request_id, and it is not made redundant by SERIALIZABLE. The
// isolation level orders two concurrent writers; it has nothing to say about the SAME writer
// retrying after a network timeout, which is the case that files a second batch and a second set
// of pictures. The insert is INSERT … ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id) — the house
// idiom (storeutil.UpsertAdminSpecialty) — so a repeat resolves to the existing row instead of
// dying on 1062, and none of the batch's own columns are overwritten by the repeat.
func (s *Store) RegisterBatch(ctx context.Context, req entity.DesignBatchRegister) (*entity.DesignBatchResult, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("%w: an upload batch needs at least one item", entity.ErrDesignInvalidArgument)
	}
	if req.ClientRequestId == "" {
		return nil, fmt.Errorf("%w: client_request_id is required", entity.ErrDesignInvalidArgument)
	}
	for _, it := range req.Items {
		if it.MediaId <= 0 {
			return nil, fmt.Errorf("%w: an upload item needs a media id", entity.ErrDesignInvalidArgument)
		}
		if it.GhostView != "" && !entity.IsDesignGhostView(it.GhostView) {
			return nil, fmt.Errorf("%w: unknown ghost_view %q", entity.ErrDesignInvalidArgument, it.GhostView)
		}
		// РОД БЕРЁТСЯ СО ВХОДА. До волны 2 он был захардкожен в `flat` прямо в INSERT'е, и это
		// значило ровно одно: рендер и 3D нельзя было завести РУКАМИ вовсе — а ручная загрузка
		// (W-8) единственный путь, который работает всегда, в том числе когда генерации нет.
		// Пустое читается как flat, поэтому старый вызывающий не заметил перемены.
		if !entity.IsDesignPictureKind(entity.DesignKindOrFlat(it.Kind)) {
			return nil, fmt.Errorf("%w: unknown picture kind %q", entity.ErrDesignInvalidArgument, it.Kind)
		}
	}

	out := &entity.DesignBatchResult{}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// The callback is re-run on a deadlock, so each attempt starts from a blank verdict
		// instead of inheriting the one a rolled-back attempt reached.
		*out = entity.DesignBatchResult{}
		db := rep.DB()

		var size int64
		for _, it := range req.Items {
			size += it.SizeBytes
		}
		// Whether this gesture has already been filed is read BEFORE the write, in this
		// SERIALIZABLE transaction, so the answer is authoritative. Deriving it from the upsert's
		// RowsAffected would be guesswork: a repeat whose columns happen to match reports zero
		// changed rows, and so does a row that was merely re-stamped.
		prior, err := storeutil.QueryListNamed[entity.DesignBatch](ctx, db,
			`SELECT * FROM design_batch WHERE client_request_id = :req`,
			map[string]any{"req": req.ClientRequestId})
		if err != nil {
			return fmt.Errorf("failed to check design upload idempotency: %w", err)
		}
		out.Idempotent = len(prior) > 0
		batchID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_batch (tech_card_id, client_request_id, author, files_count, size_bytes)
			VALUES (:card, :req, :who, :n, :size)
			ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
			map[string]any{
				"card": req.TechCardId, "req": req.ClientRequestId, "who": req.Actor,
				"n": len(req.Items), "size": size,
			})
		if err != nil {
			return fmt.Errorf("failed to register design upload batch: %w", err)
		}

		batch, err := storeutil.QueryNamedOne[entity.DesignBatch](ctx, db,
			`SELECT * FROM design_batch WHERE id = :id`, map[string]any{"id": batchID})
		if err != nil {
			return fmt.Errorf("failed to read design upload batch %d: %w", batchID, err)
		}
		// A repeat that resolved onto somebody else's batch would be a client_request_id
		// collision across cards. Refusing beats filing pictures onto a foreign card.
		if batch.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: client_request_id belongs to tech card %d",
				entity.ErrDesignNotFound, batch.TechCardId)
		}
		// uq_design_picture_batch_ordinal makes each picture idempotent on its own, so a retry
		// that reached the batch insert but not the pictures still converges.
		for i, it := range req.Items {
			if _, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_picture
					(tech_card_id, media_id, batch_id, ordinal, kind, ghost_view, source_class, mixed_input)
				VALUES (:card, :media, :batch, :ord, :kind, :ghost, :src, 0)
				ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
				map[string]any{
					"card": req.TechCardId, "media": it.MediaId, "batch": batchID, "ord": i,
					"kind": entity.DesignKindOrFlat(it.Kind), "ghost": nullStr(it.GhostView),
					// mixed_input is 0 for an upload BY CONSTRUCTION: one gesture has one
					// provenance, so there is no mixture to launder. The flag becomes computable
					// only where inputs of several provenances meet — a fix run's output — and it
					// is computed there, at the moment the picture is born, never at mint.
					"src": entity.DesignSourceUploaded,
				}); err != nil {
				return fmt.Errorf("failed to file design upload item %d: %w", i, err)
			}
		}

		pics, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db,
			`SELECT * FROM design_picture WHERE batch_id = :b ORDER BY ordinal, id`,
			map[string]any{"b": batchID})
		if err != nil {
			return fmt.Errorf("failed to read design batch pictures: %w", err)
		}
		flat := make([]*entity.DesignPicture, 0, len(pics))
		for i := range pics {
			flat = append(flat, &pics[i])
		}
		if err := resolveMedia(ctx, rep, flat); err != nil {
			return err
		}
		batch.Pictures = pics
		out.Batch, out.Pictures = batch, pics

		if req.Target != nil && len(pics) > 0 {
			// РОД АДРЕСА ПО УМОЛЧАНИЮ — РОД САМОЙ КАРТИНКИ. Жест один: «положи ЭТОТ файл на ЭТУ
			// сторону», и рода в нём человек не называл вовсе. Подставить сюда `flat` значило бы
			// отказывать каждой ручной загрузке рендера с постановкой — то есть закрыть ровно ту
			// дверь, которую эта волна открывает. Названный вызывающим род имеет приоритет.
			target := *req.Target
			if target.Kind == "" {
				target.Kind = entity.DesignKindOrFlat(pics[0].Kind)
			}
			slot, err := setBenchSlotTx(ctx, rep, entity.DesignBenchSlotSet{
				TechCardId:      req.TechCardId,
				Slot:            target,
				PictureId:       pics[0].Id,
				ExpectedSlotRev: req.ExpectedSlotRev,
				Actor:           req.Actor,
			})
			if err != nil {
				return err
			}
			if err := attachSlotPictures(ctx, rep, []*entity.DesignBenchSlot{slot}); err != nil {
				return err
			}
			out.Slot = slot
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HidePicture is the ONLY persistent verb for picture invisibility, and it is reversible.
//
// EVERY GUARD READS IN THE SAME TRANSACTION AS THE UPDATE. A guard that reads first and writes
// afterwards is a TOCTOU: the plate is put into a slot between the two, and the sheet then quotes
// a picture the band draws as absent.
//
// Un-hiding is never guarded — making a picture visible again cannot break anything.
func (s *Store) HidePicture(ctx context.Context, pictureID int, hidden bool, actor string) (*entity.DesignPicture, error) {
	var out entity.DesignPicture
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		pic, err := pictureByID(ctx, db, pictureID)
		if err != nil {
			return err
		}
		if hidden {
			if err := hidePictureGuards(ctx, db, pic); err != nil {
				return err
			}
		}
		if hidden {
			err = storeutil.ExecNamed(ctx, db, `
				UPDATE design_picture SET hidden_at = UTC_TIMESTAMP(6), hidden_by = :who WHERE id = :id`,
				map[string]any{"id": pictureID, "who": actor})
		} else {
			err = storeutil.ExecNamed(ctx, db, `
				UPDATE design_picture SET hidden_at = NULL, hidden_by = NULL WHERE id = :id`,
				map[string]any{"id": pictureID})
		}
		if err != nil {
			return fmt.Errorf("failed to set design picture visibility: %w", err)
		}
		out, err = pictureByID(ctx, db, pictureID)
		if err != nil {
			return err
		}
		return resolveMedia(ctx, rep, []*entity.DesignPicture{&out})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetPictureSelected marks a picture as CHOSEN, and un-marks it (owner requirement W-12: «мы так
// же можем маркать 3д рендеры как выбранные»).
//
// IT IS NOT THE OTHER SIDE OF HidePicture, and folding the two would make one gesture silently
// undo the other: hidden says «do not show me this», selected says «this is the one». The two are
// independent — a chosen picture may later be hidden — and that is why this is a verb of its own
// rather than a second flag on the hide.
//
// NOTHING IS EXCLUSIVE AND NOTHING IS GUARDED. The owner speaks in the plural, so many pictures of
// a kind may be chosen at once (there is deliberately no UNIQUE on the column); and a mark that
// changes no visibility, no slot and no frozen version has nothing it could break, which is the
// whole reason HidePicture's four guards have no counterpart here.
func (s *Store) SetPictureSelected(ctx context.Context, pictureID int, selected bool, actor string) (*entity.DesignPicture, error) {
	var out entity.DesignPicture
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// THE ROW IS READ BEFORE IT IS WRITTEN so an unknown or foreign id is a NotFound rather
		// than a silent zero-row UPDATE that would answer OK about a picture that does not exist.
		if _, err := pictureByID(ctx, db, pictureID); err != nil {
			return err
		}
		// actor IS NOT STAMPED, AND THAT IS NOT AN OVERSIGHT: design_picture has no selected_by
		// column (0350 added the flag alone), and inventing one out of hidden_by would say that
		// the person who chose the frame is the person who hid it. It stays in the argument list
		// because every write of this band is called with it and a signature that quietly differs
		// is a signature somebody will forget to pass.
		_ = actor
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE design_picture SET selected = :selected WHERE id = :id`,
			map[string]any{"id": pictureID, "selected": selected}); err != nil {
			return fmt.Errorf("failed to set design picture selection: %w", err)
		}
		var err error
		out, err = pictureByID(ctx, db, pictureID)
		if err != nil {
			return err
		}
		return resolveMedia(ctx, rep, []*entity.DesignPicture{&out})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// hidePictureGuards is the four refusals of 10 §3, read inside the caller's transaction.
func hidePictureGuards(ctx context.Context, db dependency.DB, pic entity.DesignPicture) error {
	inSlot, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM design_bench_slot WHERE picture_id = :id`,
		map[string]any{"id": pic.Id})
	if err != nil {
		return fmt.Errorf("failed to check design slot use of picture %d: %w", pic.Id, err)
	}
	if inSlot > 0 {
		return fmt.Errorf("%w: picture %d stands in a bench slot", entity.ErrDesignInSlot, pic.Id)
	}

	// FEEDING A LIVE RUN. The snapshot is JSON, so this is a JSON predicate — bounded to the
	// card's own pending/running rows, which are units, not the organisation's whole history.
	// The paths are SNAKE_CASE; see entity.DesignInputsJSONSlotMedia for why that is load-bearing.
	liveInput, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT COUNT(*) FROM design_run r
		WHERE r.tech_card_id = :card AND r.status IN ('pending', 'running') AND (
			JSON_CONTAINS(COALESCE(JSON_EXTRACT(r.inputs, '$.slots[*].media_id'), JSON_ARRAY()), CAST(:media AS JSON))
			OR JSON_CONTAINS(COALESCE(JSON_EXTRACT(r.inputs, '$.refs[*].media_id'), JSON_ARRAY()), CAST(:media AS JSON))
			OR JSON_CONTAINS(COALESCE(JSON_EXTRACT(r.params, '$.extra_input_media_ids'), JSON_ARRAY()), CAST(:media AS JSON))
		)`,
		map[string]any{"card": pic.TechCardId, "media": pic.MediaId})
	if err != nil {
		return fmt.Errorf("failed to check live design runs using picture %d: %w", pic.Id, err)
	}
	if liveInput > 0 {
		return fmt.Errorf("%w: picture %d is an input of a run in flight", entity.ErrDesignLiveRunInput, pic.Id)
	}

	// PARENTING A LIVE CROP. Hiding the composite while its visible crops remain would leave the
	// crops with a parent nobody can look at, and «where did this come from» is the one question
	// a crop must always be able to answer.
	liveCrop, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM design_picture WHERE derived_from = :id AND hidden_at IS NULL`,
		map[string]any{"id": pic.Id})
	if err != nil {
		return fmt.Errorf("failed to check live crops of picture %d: %w", pic.Id, err)
	}
	if liveCrop > 0 {
		return fmt.Errorf("%w: picture %d is the parent of a visible crop", entity.ErrDesignLiveCropParent, pic.Id)
	}
	return nil
}

// ArchiveRun flips a PRESENTATIONAL, REVERSIBLE flag on a history row. It does NOT hide the row's
// pictures — picture invisibility has exactly one persistent verb and this is not it.
func (s *Store) ArchiveRun(ctx context.Context, runID int, archived bool, actor string) (*entity.DesignRun, error) {
	var out entity.DesignRun
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		var err error
		if archived {
			err = storeutil.ExecNamed(ctx, db, `
				UPDATE design_run SET archived_at = UTC_TIMESTAMP(6), archived_by = :who WHERE id = :id`,
				map[string]any{"id": runID, "who": actor})
		} else {
			err = storeutil.ExecNamed(ctx, db, `
				UPDATE design_run SET archived_at = NULL, archived_by = NULL WHERE id = :id`,
				map[string]any{"id": runID})
		}
		if err != nil {
			return fmt.Errorf("failed to archive design run %d: %w", runID, err)
		}
		out, err = runByID(ctx, db, runID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRun reads one history row with its attempts and pictures.
func (s *Store) GetRun(ctx context.Context, runID int) (*entity.DesignRun, error) {
	var out entity.DesignRun
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		r, err := runByID(ctx, db, runID)
		if err != nil {
			return err
		}
		if r.Attempts, err = storeutil.QueryListNamed[entity.DesignRunAttempt](ctx, db,
			`SELECT * FROM design_run_attempt WHERE run_id = :id ORDER BY attempt_no`,
			map[string]any{"id": runID}); err != nil {
			return fmt.Errorf("failed to read design run attempts: %w", err)
		}
		pics, err := loadPicturesByRuns(ctx, db, []int{runID})
		if err != nil {
			return err
		}
		r.Pictures = pics[runID]
		flat := make([]*entity.DesignPicture, 0, len(r.Pictures))
		for i := range r.Pictures {
			flat = append(flat, &r.Pictures[i])
		}
		if err := resolveMedia(ctx, rep, flat); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPicture reads one picture with its media resolved.
func (s *Store) GetPicture(ctx context.Context, pictureID int) (*entity.DesignPicture, error) {
	var out entity.DesignPicture
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		p, err := pictureByID(ctx, rep.DB(), pictureID)
		if err != nil {
			return err
		}
		out = p
		return resolveMedia(ctx, rep, []*entity.DesignPicture{&out})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func runByID(ctx context.Context, db dependency.DB, id int) (entity.DesignRun, error) {
	r, err := storeutil.QueryNamedOne[entity.DesignRun](ctx, db,
		`SELECT * FROM design_run WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("%w: design run %d", entity.ErrDesignNotFound, id)
		}
		return r, fmt.Errorf("failed to read design run %d: %w", id, err)
	}
	return r, nil
}

// SplitPicture files the crops of a composite. The BYTE WORK HAPPENS BEFORE THIS CALL — the
// handler reads the original object, cuts it and uploads each frame — so all that is left here is
// the derivation, in one transaction.
//
// ⚠ IDEMPOTENCY IS BY DERIVATION, NOT BY client_request_id, and that is forced rather than
// chosen: design_picture has no column for a request id and none can be added from here. So the
// rule is: if the parent already has crops that are still visible, THEY are returned and nothing
// is cut again — which is exactly what a retried split needs. A deliberate RE-split is available
// after the bad crops are hidden, so a wrong cut is not a dead end. The check and the insert are
// in one SERIALIZABLE transaction, so a concurrent duplicate cannot slip between them.
//
// Crops are SIBLINGS under the source's own row (same run_id, same batch_id): no money was spent
// on a crop, and it belongs where its parent hangs.
func (s *Store) SplitPicture(ctx context.Context, req entity.DesignSplitRequest) ([]entity.DesignPicture, error) {
	if len(req.Frames) == 0 {
		return nil, fmt.Errorf("%w: a split needs at least one frame", entity.ErrDesignInvalidArgument)
	}
	var out []entity.DesignPicture
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()
		parent, err := pictureByID(ctx, db, req.PictureId)
		if err != nil {
			return err
		}
		// No compositeness check here either — see the note in the handler. The column it read has
		// no writer in this wave, so the check refused every split that could ever reach it.

		existing, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db,
			`SELECT * FROM design_picture WHERE derived_from = :id AND hidden_at IS NULL ORDER BY ordinal, id`,
			map[string]any{"id": parent.Id})
		if err != nil {
			return fmt.Errorf("failed to read existing crops of picture %d: %w", parent.Id, err)
		}
		if len(existing) > 0 {
			out = existing
			flat := make([]*entity.DesignPicture, 0, len(out))
			for i := range out {
				flat = append(flat, &out[i])
			}
			return resolveMedia(ctx, rep, flat)
		}

		// Ordinals continue after whatever already hangs under the parent's row, so the crops sort
		// after the outputs they were cut from rather than colliding with them on
		// uq_design_picture_run_ordinal / uq_design_picture_batch_ordinal.
		base, err := nextSiblingOrdinal(ctx, db, parent)
		if err != nil {
			return err
		}
		for i, f := range req.Frames {
			if f.MediaId <= 0 {
				return fmt.Errorf("%w: crop %d has no media", entity.ErrDesignInvalidArgument, i)
			}
			if f.ViewKey != "" && !entity.IsDesignGhostView(f.ViewKey) {
				return fmt.Errorf("%w: unknown view_key %q on crop %d", entity.ErrDesignInvalidArgument, f.ViewKey, i)
			}
			if _, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_picture
					(tech_card_id, media_id, run_id, batch_id, ordinal, kind, ghost_view,
					 derived_from, source_class, mixed_input, layer_rev)
				VALUES (:card, :media, :run, :batch, :ord, :kind, :ghost, :parent, :src, :mixed, :layer)`,
				map[string]any{
					"card": parent.TechCardId, "media": f.MediaId,
					"run": nullInt32(parent.RunId), "batch": nullInt32(parent.BatchId),
					"ord": base + i, "kind": parent.Kind, "ghost": nullStr(f.ViewKey),
					"parent": parent.Id,
					// A crop INHERITS its parent's provenance and its mixed flag. Cutting a
					// picture up does not change where it came from, and a crop of a mixed
					// composite is still mixed — laundering the mixture by one more operation is
					// exactly what the flag exists to prevent.
					"src": parent.SourceClass, "mixed": parent.MixedInput, "layer": parent.LayerRev,
				}); err != nil {
				return fmt.Errorf("failed to file crop %d of picture %d: %w", i, parent.Id, err)
			}
		}

		// ─── ТОЛЬКО ЕСЛИ РАЗРЕЗ БЫЛ ЗАЯВЛЕН «ДЛЯ ПРОМПТА» ───
		//
		// R-11 круга 2 требовал обратного — кропы входили в промпт ВСЕГДА. **T-15 круга 4 это
		// отменил дословно:** «в INPUT — REFERENCES не должны уходить все флеты если мы их явно
		// туда сами не добавим». Разрез на верстаке — это раскладка видов по слотам, и он молча
		// набивал промпт теми самыми флэтами.
		//
		// ПОЧЕМУ ВОПРОС ЗАДАЁТСЯ ЗДЕСЬ, А НЕ ЧИНИТСЯ НА КЛИЕНТЕ. Клиент, которому эти строки не
		// нужны, может только удалить их следом — а между записью и удалением человек успевает
		// поставить на то же медиа СВОЮ роль и записку, и удаление их уничтожит. Закрыть это окно
		// с клиента нечем: очистка роли это голый DELETE по (карточка, медиа) без сверки с
		// ожидаемым значением. Значит единственное место, где ответ не гонится с человеком, —
		// до записи.
		// ⚠ РАННИЙ ВЫХОД ЗДЕСЬ БЫЛ ДЕФЕКТОМ, И ВОТ КАКИМ. Стояло `if !req.ForInput { return nil }`,
		// то есть выход из ВСЕЙ транзакции. Но `out` наполняется в самом хвосте этой же функции —
		// значит при `for_input=false`, а это ЗНАЧЕНИЕ ПО УМОЛЧАНИЮ и прямое правило владельца
		// (T-15), кропы записывались в базу, а вызывающему возвращался ПУСТОЙ СПИСОК. Клиент
		// получал «разрез не дал ничего», имея разрез в базе.
		//
		// Поймано контейнерным прогоном сторовых тестов: три пробы падали на `require.Len(crops, 1)`
		// с нулём. Из безопасных пакетов это не видно вовсе — там стор замокан.
		//
		// Гейт обязан огораживать РОВНО запись ролей, а не хвост чтения. Поэтому он стал условием
		// блока, а не выходом из функции.
		if req.ForInput {
			// ДЕТАЛИ СПЛИТА ВХОДЯТ В ПРОМПТ СРАЗУ, ПОМЕЧЕННЫЕ РОЛЬЮ СВОЕГО ВИДА (решение владельца,
			// R-11, СУЖЕННОЕ T-15 до разрезов, заявленных для промпта). Словарь ролей design_reference и словарь view_key — ОДИН и тот же
			// (entity.IsDesignGhostView проверяет оба: см. валидацию кадров выше и SetReferenceRole),
			// поэтому перенос без догадок. Кадр БЕЗ view_key роли НЕ получает: придуманная здесь роль
			// соврала бы модели о стороне изделия, и снять эту ложь человеку было бы негде — он её не
			// ставил.
			//
			// ИДЕМПОТЕНТНОСТЬ НЕ ЛОМАЕТСЯ: повтор сплита срезается ВЫШЕ, на живых кропах родителя, и до
			// этого места не доходит — второй набор ролей взяться неоткуда. Всё в одной SERIALIZABLE
			// транзакции с самими кропами: роль без кропа или кроп без роли не переживают отказ.
			//
			// ОРДИНАЛ — ХВОСТ за максимальным из уже стоящих, а не ноль: промпт читается
			// ORDER BY ordinal, и кроп с ordinal 0 встал бы ВПЕРЕДИ референсов мудборда, чей порядок
			// назначил человек.
			ord, err := storeutil.QueryCountNamed(ctx, db,
				`SELECT COALESCE(MAX(ordinal), 0) FROM design_reference WHERE tech_card_id = :card`,
				map[string]any{"card": parent.TechCardId})
			if err != nil {
				return fmt.Errorf("failed to read the prompt tail of tech card %d: %w", parent.TechCardId, err)
			}
			for _, f := range req.Frames {
				if f.ViewKey == "" {
					continue
				}
				ord++
				// Упсерт, а не голый INSERT — на случай, когда одно медиа названо двумя кадрами одного
				// запроса; записка (note) НЕ перечислена и потому не затирается, если строка уже была.
				if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO design_reference (tech_card_id, media_id, role, ordinal, set_by, set_at)
				VALUES (:card, :media, :role, :ord, :who, UTC_TIMESTAMP(6))
				ON DUPLICATE KEY UPDATE
					role = VALUES(role),
					ordinal = VALUES(ordinal),
					set_by = VALUES(set_by), set_at = VALUES(set_at)`,
					map[string]any{
						"card": parent.TechCardId, "media": f.MediaId,
						"role": f.ViewKey, "ord": ord, "who": req.Actor,
					}); err != nil {
					return fmt.Errorf("failed to file the prompt role of crop media %d: %w", f.MediaId, err)
				}
			}
		}

		out, err = storeutil.QueryListNamed[entity.DesignPicture](ctx, db,
			`SELECT * FROM design_picture WHERE derived_from = :id ORDER BY ordinal, id`,
			map[string]any{"id": parent.Id})
		if err != nil {
			return fmt.Errorf("failed to read crops of picture %d: %w", parent.Id, err)
		}
		flat := make([]*entity.DesignPicture, 0, len(out))
		for i := range out {
			flat = append(flat, &out[i])
		}
		return resolveMedia(ctx, rep, flat)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nextSiblingOrdinal is the first free ordinal under the parent's run or batch row.
func nextSiblingOrdinal(ctx context.Context, db dependency.DB, parent entity.DesignPicture) (int, error) {
	switch {
	case parent.RunId.Valid:
		n, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COALESCE(MAX(ordinal), -1) + 1 FROM design_picture WHERE run_id = :id`,
			map[string]any{"id": parent.RunId.Int32})
		if err != nil {
			return 0, fmt.Errorf("failed to compute next crop ordinal: %w", err)
		}
		return n, nil
	case parent.BatchId.Valid:
		n, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COALESCE(MAX(ordinal), -1) + 1 FROM design_picture WHERE batch_id = :id`,
			map[string]any{"id": parent.BatchId.Int32})
		if err != nil {
			return 0, fmt.Errorf("failed to compute next crop ordinal: %w", err)
		}
		return n, nil
	default:
		// A parent that hangs under neither a run nor a batch has no sibling series to continue,
		// and both unique keys tolerate NULL freely, so ordinals restart at zero.
		return 0, nil
	}
}

func nullInt32(v sql.NullInt32) any {
	if !v.Valid {
		return nil
	}
	return v.Int32
}
