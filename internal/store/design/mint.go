package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// АТОМАРНЫЙ МИНТ ВЕРСИИ ЛИСТА — САМОЕ РИСКОВАННОЕ МЕСТО ВОЛНЫ, и вся его ценность в слове «одна».
//
// ПОЧЕМУ НЕ «СОХРАНИ, ПОТОМ МИНТИ». H1: первая выноска на черновике листа РОЖДАЕТ v1, а инвариант
// прототипа — «rev ≥ 1 ⇔ был акт минта; ни одной выноски при rev = 0». Двухшаговый путь оставляет
// на полу ровно запрещённое состояние — документ с выноской и без версии, видимый другим и
// неотличимый от осознанного черновика, — плюс окно между шагами, в которое влезает чужой
// UpdateTechCard и уносит состав из-под минта.
//
// ПОЧЕМУ ЭТО НЕ ВТОРОЙ ПИСАТЕЛЬ ВЫНОСОК. Документ пишет techcard.UpdateTechCardTx — ТА ЖЕ функция,
// что и у обычного сейва, в ЭТОЙ транзакции. Второго пути записи не появляется; появляется вторая
// точка входа в существующий. Всё, что хендлер сейва делает ДО стора (гейты, конверсия, минт
// номеров выносок, carryOmitted*, отпечатки подписей), делает и хендлер минта — это П-Д, и без него
// первая выноска черновика замерзает в версии с номером 0, а свежая подпись рождается протухшей.

// mintPlate — одна плита состава: слот, картинка в нём и всё, что версия обязана ЗАПОМНИТЬ, а не
// вывести заново. Читается ОДНИМ запросом на весь состав, не по строке.
type mintPlate struct {
	slot        entity.DesignBenchSlot
	PictureId   int            `db:"picture_id"`
	MediaId     int            `db:"media_id"`
	RunId       sql.NullInt32  `db:"run_id"`
	BatchId     sql.NullInt32  `db:"batch_id"`
	SourceClass string         `db:"source_class"`
	LayerRev    int            `db:"layer_rev"`
	MixedInput  bool           `db:"mixed_input"`
	ContentHash sql.NullString `db:"content_hash"`
	FitAtLaunch sql.NullString `db:"fit_at_launch"`
}

// MintSheetVersion пишет документ и рождает замороженную версию в ОДНОЙ транзакции.
func (s *Store) MintSheetVersion(ctx context.Context, req entity.DesignSheetMint) (*entity.DesignSheetVersionFull, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ClientRequestId) == "" {
		return nil, fmt.Errorf("%w: client_request_id is required — without it a lost response mints a phantom version",
			entity.ErrDesignInvalidArgument)
	}
	if req.TechCard == nil {
		return nil, fmt.Errorf("%w: the mint carries the whole card document", entity.ErrDesignInvalidArgument)
	}
	if !entity.IsDesignMintedVia(req.MintedVia) {
		return nil, fmt.Errorf("%w: minted_via %q is not callout|print|release|share",
			entity.ErrDesignInvalidArgument, req.MintedVia)
	}
	// СЛОВАРЬ ОБНОВЛЯЕТСЯ ДО ТРАНЗАКЦИИ, ровно как у обычного сейва: это чтение общего
	// in-memory кэша, и втащить его внутрь SERIALIZABLE-записи значило бы положить словарные
	// таблицы в набор блокировок каждого минта задаром.
	if err := s.cards.EnsureDictionaryFresh(ctx, "design sheet mint"); err != nil {
		return nil, err
	}

	var out entity.DesignSheetVersionFull
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// txFunc умеет перезапустить callback после дедлока: ничего с оборванной попытки не несём.
		out = entity.DesignSheetVersionFull{}
		db := rep.DB()

		// ─── 1. ИДЕМПОТЕНТНОСТЬ — ПЕРВЫМ ДЕЛОМ, И ЭТО ПОРЯДОК, А НЕ СТИЛЬ ───
		//
		// Повтор с тем же client_request_id обязан вернуть УЖЕ СОЗДАННУЮ версию, а не родить
		// фантомную vN+1. Проверка стоит ДО документной записи намеренно: та бампает lock_version,
		// и повтор, дошедший до неё, получил бы Aborted:lock_version_mismatch — то есть человек
		// увидел бы конфликт вместо своего же успеха, а сеть, потерявшая ответ, стала бы
		// неотличима от чужой правки.
		if existing, ok, err := versionByRequestID(ctx, db, req.ClientRequestId); err != nil {
			return err
		} else if ok {
			if existing.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: client_request_id %q already minted a version of tech card %d",
					entity.ErrDesignInvalidArgument, req.ClientRequestId, existing.TechCardId)
			}
			full, err := loadSheetVersion(ctx, rep, req.TechCardId, existing.VersionNumber)
			if err != nil {
				return err
			}
			out = *full
			out.Idempotent = true
			return nil
		}

		// ─── 2. CAS ПО ВЕРСТАКУ ───
		slots, err := listBenchSlots(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if err := casExpectedPlates(slots, req.ExpectedPlates); err != nil {
			return err
		}

		// ─── 3. СОСТАВ ───
		plates, err := composeMintPlates(ctx, db, slots)
		if err != nil {
			return err
		}
		cardFit, err := storeutil.QueryNamedOne[struct {
			Fit sql.NullString `db:"fit"`
		}](ctx, db, `SELECT fit FROM tech_card WHERE id = :card`, map[string]any{"card": req.TechCardId})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: tech card %d", entity.ErrDesignNotFound, req.TechCardId)
			}
			return fmt.Errorf("failed to read the card fit before minting: %w", err)
		}
		if err := mintGates(plates, cardFit.Fit.String, req); err != nil {
			return err
		}

		// ─── 4. ПОЯС П-А ───
		//
		// Документ, который эта транзакция сейчас заморозит, ОБЯЗАН содержать плиты верстака как
		// technical-медиа. Механизм «деталь кроя ↔ выноска» читает ровно tc.Media с
		// category='technical' (store/techcard/materials.go), и плита вне этого множества делает
		// КАЖДУЮ деталь на листовой выноске detached — тех-пак печатает пустой эскиз, а дайджест
		// DESIGN не видит чертежа. Вкладывает плиты хендлер (там же, где собирается весь остальной
		// документ); пояс стоит ЗДЕСЬ, потому что это единственное место, которое видит верстак и
		// документ одновременно, и потому что отказ дешевле молчаливой порчи.
		if err := requirePlatesInDocument(plates, req.TechCard); err != nil {
			return err
		}

		// ─── 5. ДОКУМЕНТ — ТЕМ ЖЕ КОДОМ ───
		orphaned, err := s.cards.UpdateTechCardTx(ctx, rep, req.TechCardId, req.TechCard, req.ExpectedLockVersion)
		if err != nil {
			return err
		}

		// ─── 6. ВЫНОСКИ — ИЗ ТОГО ДОКУМЕНТА, КОТОРЫЙ ЭТА ЖЕ ТРАНЗАКЦИЯ ТОЛЬКО ЧТО ЗАПИСАЛА ───
		//
		// Не из payload'а: номера присваивает хендлер, а перенос геометрии и ключа строки правит
		// payload дальше — читать надо КОЛОНКИ, тогда «замороженное» и «сохранённое» это по
		// построению одно и то же, а не два похожих списка.
		stored, err := storedCallouts(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		prevPlateMedia, err := previousPlateMedia(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		frozen, err := freezeCallouts(stored, plates, prevPlateMedia)
		if err != nil {
			return err
		}

		// ─── 7. ВЕРСИЯ ───
		versionNumber, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COALESCE(MAX(version_number), 0) + 1 FROM design_sheet_version
			WHERE tech_card_id = :card`, map[string]any{"card": req.TechCardId})
		if err != nil {
			return fmt.Errorf("failed to take the next design sheet version number: %w", err)
		}
		versionID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_sheet_version
				(tech_card_id, version_number, client_request_id, mixed_consent, minted_via, minted_by)
			VALUES (:card, :n, :req, :consent, :via, :who)`,
			map[string]any{
				"card": req.TechCardId, "n": versionNumber, "req": req.ClientRequestId,
				"consent": req.MixedConsent, "via": req.MintedVia, "who": req.Actor,
			})
		if err != nil {
			// Остаточный 1062 на uq_design_sheet_version — это ДВА параллельных минта, которые
			// сериализация развела, а не повтор одного: повтор поймала бы проверка выше. Второй
			// увидит тот же допустимый верстак и возьмёт следующий номер, поэтому ему честнее
			// сказать «верстак под тобой уже сминчен», чем отдать сырой 1062, которого нет в
			// таксономии и который клиент не откатит.
			if isDupKey(err) {
				return fmt.Errorf("%w: another mint of this card committed first", entity.ErrDesignBenchMoved)
			}
			return fmt.Errorf("failed to mint the design sheet version: %w", err)
		}
		if err := insertVersionPlates(ctx, db, int(versionID), plates, cardFit.Fit.String); err != nil {
			return err
		}
		if err := insertVersionCallouts(ctx, db, int(versionID), frozen); err != nil {
			return err
		}
		// Журнальная строка `minted` рождается ЗДЕСЬ и только здесь: client_request_id у неё NULL
		// намеренно (несколько NULL в UNIQUE не конфликтуют), потому что ключ идемпотентности
		// этого акта живёт на самой версии.
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO design_sheet_issue (version_id, action, actor) VALUES (:v, :action, :who)`,
			map[string]any{"v": versionID, "action": entity.DesignIssueMinted, "who": req.Actor}); err != nil {
			return fmt.Errorf("failed to journal the design sheet mint: %w", err)
		}

		full, err := loadSheetVersion(ctx, rep, req.TechCardId, versionNumber)
		if err != nil {
			return err
		}
		out = *full
		out.OrphanedPatternURLs = orphaned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// versionByRequestID ищет уже сминченную версию по ключу запроса.
func versionByRequestID(ctx context.Context, db dependency.DB, requestID string) (entity.DesignSheetVersion, bool, error) {
	v, err := storeutil.QueryNamedOne[entity.DesignSheetVersion](ctx, db,
		`SELECT * FROM design_sheet_version WHERE client_request_id = :req`,
		map[string]any{"req": requestID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.DesignSheetVersion{}, false, nil
		}
		return entity.DesignSheetVersion{}, false, fmt.Errorf("failed to look up the design sheet version by request id: %w", err)
	}
	return v, true, nil
}

// casExpectedPlates сверяет верстак с тем, каким его видел минтующий.
//
// ПУСТОЙ СПИСОК ЗНАЧИТ «НЕ ПРОВЕРЯТЬ», как и говорит контракт, и это честно только для серверного
// вызова: UI всегда шлёт полный набор. Отказ называет ИМЕННО ТОТ слот — «верстак изменился» без
// имени слота это новость, а не действие.
func casExpectedPlates(slots []entity.DesignBenchSlot, expected []entity.DesignExpectedPlate) error {
	if len(expected) == 0 {
		return nil
	}
	byID := make(map[int]entity.DesignBenchSlot, len(slots))
	byView := make(map[string]entity.DesignBenchSlot, len(slots))
	for _, sl := range slots {
		byID[sl.Id] = sl
		if entity.IsDesignSilhouetteView(sl.ViewKey) {
			byView[sl.ViewKey] = sl
		}
	}
	for _, e := range expected {
		var (
			cur   entity.DesignBenchSlot
			found bool
			name  string
		)
		switch {
		case e.Slot.SlotId > 0:
			cur, found = byID[e.Slot.SlotId]
			name = "slot " + strconv.Itoa(e.Slot.SlotId)
		case e.Slot.ViewKey != "":
			cur, found = byView[e.Slot.ViewKey]
			name = e.Slot.ViewKey
		default:
			return fmt.Errorf("%w: an expected plate names neither a view nor a slot id",
				entity.ErrDesignInvalidArgument)
		}
		// СЛОТА НЕТ — ЭТО ТОЖЕ РАСХОЖДЕНИЕ, а не «нечего сверять»: слот детали могли удалить
		// ровно между экраном и минтом, и заморозить состав, которого уже нет, нельзя.
		if !found {
			return &entity.DesignMintRefusal{
				Err: fmt.Errorf("%w: %s is gone from the bench", entity.ErrDesignBenchMoved, name),
				Metadata: map[string]string{
					"slot": name, "expected_slot_rev": strconv.Itoa(e.SlotRev), "slot_gone": "true",
				},
			}
		}
		if cur.SlotRev != e.SlotRev {
			return &entity.DesignMintRefusal{
				Err: fmt.Errorf("%w: %s is at rev %d, the mint was composed at rev %d",
					entity.ErrDesignBenchMoved, name, cur.SlotRev, e.SlotRev),
				Metadata: map[string]string{
					"slot": name, "slot_id": strconv.Itoa(cur.Id),
					"slot_rev": strconv.Itoa(cur.SlotRev), "expected_slot_rev": strconv.Itoa(e.SlotRev),
				},
			}
		}
	}
	return nil
}

// composeMintPlates собирает состав: четыре стороны в каноническом порядке, затем детали по
// возрастанию id слота. Пустые слоты в состав не входят — плита это КАРТИНКА, а не адрес.
func composeMintPlates(ctx context.Context, db dependency.DB, slots []entity.DesignBenchSlot) ([]mintPlate, error) {
	ordered := make([]entity.DesignBenchSlot, 0, len(slots))
	for _, view := range entity.DesignSilhouetteViews {
		for _, sl := range slots {
			if sl.ViewKey == view && sl.PictureId.Valid {
				ordered = append(ordered, sl)
			}
		}
	}
	details := make([]entity.DesignBenchSlot, 0, len(slots))
	for _, sl := range slots {
		if !entity.IsDesignSilhouetteView(sl.ViewKey) && sl.PictureId.Valid {
			details = append(details, sl)
		}
	}
	sort.Slice(details, func(i, j int) bool { return details[i].Id < details[j].Id })
	ordered = append(ordered, details...)
	if len(ordered) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(ordered))
	for _, sl := range ordered {
		ids = append(ids, int(sl.PictureId.Int32))
	}
	// ОДИН ЗАПРОС НА ВЕСЬ СОСТАВ. content_hash и fit_at_launch приезжают джойнами, потому что
	// версия обязана ЗАПОМНИТЬ их, а не выводить при чтении: media может переехать, прогон —
	// заархивироваться, а бумага печатается через год.
	rows, err := storeutil.QueryListNamed[mintPlate](ctx, db, `
		SELECT p.id AS picture_id, p.media_id, p.run_id, p.batch_id, p.source_class,
		       p.layer_rev, p.mixed_input, m.content_hash, r.fit_at_launch
		FROM design_picture p
		JOIN media m ON m.id = p.media_id
		LEFT JOIN design_run r ON r.id = p.run_id
		WHERE p.id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to read the design plates being minted: %w", err)
	}
	byPicture := make(map[int]mintPlate, len(rows))
	for _, r := range rows {
		byPicture[r.PictureId] = r
	}
	out := make([]mintPlate, 0, len(ordered))
	for _, sl := range ordered {
		p, ok := byPicture[int(sl.PictureId.Int32)]
		if !ok {
			return nil, fmt.Errorf("%w: picture %d of slot %d", entity.ErrDesignNotFound,
				sl.PictureId.Int32, sl.Id)
		}
		p.slot = sl
		out = append(out, p)
	}
	return out, nil
}

// mintGates — четыре ворот прототипа (mintAnalysis), дословно и в том же порядке: минимум листа,
// посадка, руки, смесь провенансов. Два первых НЕ снимаются согласием, два вторых — снимаются.
func mintGates(plates []mintPlate, cardFit string, req entity.DesignSheetMint) error {
	filled := make(map[string]bool, len(plates))
	for _, p := range plates {
		filled[p.slot.ViewKey] = true
	}
	missing := make([]string, 0, len(entity.DesignSheetMinViews))
	for _, v := range entity.DesignSheetMinViews {
		if !filled[v] {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return &entity.DesignMintRefusal{
			Err: fmt.Errorf("%w: the sheet needs %s — the slot is empty",
				entity.ErrDesignSheetMinUnmet, strings.Join(missing, " and ")),
			Metadata: map[string]string{"missing": strings.Join(missing, ",")},
		}
	}
	// ПОСАДКА СОГЛАСИЕМ НЕ СНИМАЕТСЯ: плита нарисована под одну посадку, карточка утверждает
	// другую, и одно из двух утверждений неверно. Лист гадать не имеет права.
	for _, p := range plates {
		at := strings.TrimSpace(p.FitAtLaunch.String)
		if at != "" && at != cardFit {
			return &entity.DesignMintRefusal{
				Err: fmt.Errorf("%w: %s was drawn at %q, the card now says %q",
					entity.ErrDesignFitMismatch, p.slot.ViewKey, at, cardFit),
				Metadata: map[string]string{"view": p.slot.ViewKey, "fit": at, "card_fit": cardFit},
			}
		}
	}
	// РУКИ ПОСАДКИ НЕ ЗАЯВЛЯЮТ ВОВСЕ — её заявляет прогон. Значит за загруженную плиту посадку
	// подтверждает человек, а не сервер подставляет карточкину молча.
	hand := 0
	for _, p := range plates {
		if p.BatchId.Valid {
			hand++
		}
	}
	if hand > 0 && !req.UploadedFitConfirm {
		return fmt.Errorf("%w: %d uploaded plate(s) state no fit of their own", entity.ErrDesignUploadedFitUnconfirmed, hand)
	}
	// СМЕСЬ ПРОВЕНАНСОВ считается ПО ЧЕТЫРЁМ СТОРОНАМ, как в прототипе: две и более разных
	// генерации в силуэте — красное, и человек обязан это принять явно. Плита, сама собранная из
	// разных входов (mixed_input), из счёта исключена — она уже не «одна генерация».
	runs := map[int]bool{}
	for _, p := range plates {
		if !entity.IsDesignSilhouetteView(p.slot.ViewKey) || p.MixedInput || !p.RunId.Valid {
			continue
		}
		runs[int(p.RunId.Int32)] = true
	}
	if len(runs) >= 2 && !req.MixedConsent {
		return fmt.Errorf("%w: the composition mixes %d generations", entity.ErrDesignMixedNeedsConsent, len(runs))
	}
	return nil
}

// requirePlatesInDocument — пояс П-А. См. довод на месте вызова.
func requirePlatesInDocument(plates []mintPlate, tc *entity.TechCardInsert) error {
	technical := entity.TechCardTechnicalMedia(tc.Media)
	missing := make([]string, 0, len(plates))
	for _, p := range plates {
		if !technical[p.MediaId] {
			missing = append(missing, p.slot.ViewKey+"="+strconv.Itoa(p.MediaId))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: the document being frozen does not list these plates as technical media: %s",
			entity.ErrDesignPlatesNotInDocument, strings.Join(missing, ", "))
	}
	return nil
}

// frozenCallout — выноска, как она ляжет на бумагу.
type frozenCallout struct {
	Number     int
	MediaId    int
	Annotation []byte
	Text       sql.NullString
}

// storedCallouts читает выноски карточки ПОСЛЕ записи документа — сущностью, а не своей копией
// колонок: геометрия замораживается тем же конвертером, которым говорит провод, и своя структура
// разошлась бы с ним на первом же новом поле.
func storedCallouts(ctx context.Context, db dependency.DB, cardID int) ([]entity.TechCardCallout, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardCallout](ctx, db, `
		SELECT callout_number, part, description, dimensions, media_id, pos_x, pos_y,
		       kind, color, dashed, filled, points, parts
		FROM tech_card_callout WHERE tech_card_id = :card ORDER BY callout_number, id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to read the callouts being frozen: %w", err)
	}
	for i := range rows {
		// Якоря лежат JSON-колонкой, а конвертер геометрии ждёт разобранный список — тот же разбор,
		// что делает чтение карточки (store/techcard/enrich.go). Испорченный JSON не роняет минт:
		// выноска замерзает без якорей, что честнее, чем отказ выпустить лист из-за одной строки.
		if len(rows[i].PointsRaw) > 0 {
			_ = json.Unmarshal(rows[i].PointsRaw, &rows[i].Points)
		}
		// PARTS РАЗБИРАЕТСЯ ТОЖЕ, И БЕЗ ЭТОГО ВЕТКА «ВСЕ ДЕТАЛИ» БЫЛА МЕРТВА. Печатная строка
		// (entity.TechCardCalloutPrintedLine) перечисляет все детали, о которых указание; без
		// разбора здесь `Parts` всегда nil, композитор падает на фолбэк `Part`, и указание о двух
		// деталях замерзает с одной — необратимо, в артефакте, который печатают через год. Чтение
		// карточки разбирает оба поля (internal/store/techcard/enrich.go), и это место обязано
		// совпадать с ним, а не почти совпадать.
		if len(rows[i].PartsRaw) > 0 {
			_ = json.Unmarshal(rows[i].PartsRaw, &rows[i].Parts)
		}
	}
	return rows, nil
}

// previousPlateMedia — медиа плит ПОСЛЕДНЕЙ сминченной версии. Нужны ровно для одного вопроса: не
// осталась ли выноска висеть на картинке, которую этот состав ЗАМЕНИЛ.
func previousPlateMedia(ctx context.Context, db dependency.DB, cardID int) (map[int]bool, error) {
	ids, err := storeutil.QueryScalarListNamed[int](ctx, db, `
		SELECT pl.media_id
		FROM design_sheet_version_plate pl
		JOIN design_sheet_version v ON v.id = pl.version_id
		WHERE v.tech_card_id = :card
		  AND v.version_number = (SELECT MAX(version_number) FROM design_sheet_version WHERE tech_card_id = :card)`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to read the previous version plates: %w", err)
	}
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// freezeCallouts — П-Е, СОСТАВ ЗАМОРОЗКИ, целиком.
//
// МОРОЗЯТСЯ ВЫНОСКИ НА МЕДИА ПЛИТ. Всё остальное на бумагу не едет: мудбордная заметка — не лист
// швеи (К-14), легаси-эскиз вне верстака — не этот выпуск, незапиненная выноска не показывает НА
// ЧТО.
//
// НЕЗАПИНЕННАЯ ВЫНОСКА ПРИ ЭТОМ НОМЕР БЕРЁТ (entity.CalloutTakesSheetNumber), и она его тратит:
// номер израсходован, а на бумаге его нет — в нумерации листа остаётся дыра. Это НЕ рассогласование
// двух правил, а осознанная пара: номер — АДРЕС строки, по нему на неё ссылаются деталь кроя и
// «отказ ведёт в место», и не выдать его значило бы оставить строку без адреса ради красоты бумаги.
// Дыра безобидна по тому же доводу, по которому счётчик карточки только растёт: повтор номера —
// порча, пропуск номера — нет. Если однажды дыры перестанут быть безобидными, менять надо ЗДЕСЬ, а
// не в предикате: на бумагу решает попадать этот файл.
//
// ОТКАЗ УЖЕ, ЧЕМ ЗАМОРОЗКА, И В ЭТОМ ВСЯ СУТЬ. Отказывает не «выноска вне плит», а «выноска на
// ЗАМЕНЁННОМ медиа»: та, что стояла на плите ПРОШЛОЙ версии, чью плиту этот состав вытеснил.
// Только у неё есть потерянный адрес — она указывала на картинку, которой на новой бумаге нет.
// Отказывать по всем выноскам вне плит значило бы сделать минт недостижимым на КАЖДОЙ карточке с
// мудбордом, то есть на всех.
func freezeCallouts(stored []entity.TechCardCallout, plates []mintPlate, prevPlateMedia map[int]bool) ([]frozenCallout, error) {
	plateMedia := make(map[int]bool, len(plates))
	for _, p := range plates {
		plateMedia[p.MediaId] = true
	}
	out := make([]frozenCallout, 0, len(stored))
	unrepinned := make([]string, 0)
	for _, c := range stored {
		if !c.MediaId.Valid || c.MediaId.Int32 == 0 {
			continue
		}
		media := int(c.MediaId.Int32)
		if plateMedia[media] {
			ann, err := dto.TechCardCalloutAnnotationJSON(c)
			if err != nil {
				return nil, fmt.Errorf("failed to freeze the geometry of callout %d: %w", c.Number, err)
			}
			out = append(out, frozenCallout{
				Number:     c.Number,
				MediaId:    media,
				Annotation: ann,
				// СОСТАВНАЯ строка, а не одно `description`: контракт `DesignSheetCallout.text`
				// требует именно её, и указание-мерка без неё замерзало бы вообще без текста.
				Text: sql.NullString{String: entity.TechCardCalloutPrintedLine(c), Valid: true},
			})
			continue
		}
		if prevPlateMedia[media] {
			unrepinned = append(unrepinned, strconv.Itoa(c.Number))
		}
	}
	if len(unrepinned) > 0 {
		return nil, &entity.DesignMintRefusal{
			Err: fmt.Errorf("%w: callouts %s stand on pictures this composition replaced — re-pin or drop them",
				entity.ErrDesignUnrepinnedCallouts, strings.Join(unrepinned, ", ")),
			Metadata: map[string]string{"numbers": strings.Join(unrepinned, ",")},
		}
	}
	return out, nil
}

func insertVersionPlates(ctx context.Context, db dependency.DB, versionID int, plates []mintPlate, cardFit string) error {
	for i, p := range plates {
		// ШТАМП ПОСАДКИ. Прогон заявил свою — берём её; не заявил (руки, кроп, флэттен) — берём
		// карточкину, ту самую, которую человек подтвердил галкой uploaded_fit_confirmed. Пусто
		// значит «посадка не заявлена никем», и это тоже честный ответ, а не пропуск.
		fit := strings.TrimSpace(p.FitAtLaunch.String)
		if fit == "" {
			fit = cardFit
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO design_sheet_version_plate
				(version_id, ordinal, view_key, slot_id, detail_name, media_id, content_hash,
				 layer_rev, source_class, run_id, fit_stamp, mixed_input)
			VALUES (:v, :ord, :view, :slot, :detail, :media, :hash, :rev, :src, :run, :fit, :mixed)`,
			map[string]any{
				"v": versionID, "ord": i, "view": p.slot.ViewKey, "slot": p.slot.Id,
				"detail": p.slot.DetailName, "media": p.MediaId, "hash": p.ContentHash,
				"rev": p.LayerRev, "src": p.SourceClass, "run": nullInt32(p.RunId),
				"fit": nullStr(fit), "mixed": p.MixedInput,
			}); err != nil {
			return fmt.Errorf("failed to freeze design sheet plate %d: %w", i, err)
		}
	}
	return nil
}

func insertVersionCallouts(ctx context.Context, db dependency.DB, versionID int, callouts []frozenCallout) error {
	for _, c := range callouts {
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO design_sheet_version_callout (version_id, number, media_id, annotation, text)
			VALUES (:v, :n, :media, :ann, :text)`,
			map[string]any{
				"v": versionID, "n": c.Number, "media": c.MediaId,
				"ann": string(c.Annotation), "text": c.Text,
			}); err != nil {
			return fmt.Errorf("failed to freeze design sheet callout %d: %w", c.Number, err)
		}
	}
	return nil
}
