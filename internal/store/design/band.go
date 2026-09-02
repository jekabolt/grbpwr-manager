package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// THE FOUR HEADER AGGREGATES, AS NAMED CONSTANTS.
//
// They are constants rather than inline strings so that «counted over the whole card» is a
// property a test can CITE and a mutation can break. Every one of them is scoped by
// tech_card_id and by nothing else: no LIMIT, no cursor, no join to the page. Counting the loaded
// page instead would truncate the header by exactly the amount that is not on the screen — a card
// with forty runs would caption itself «12», and the number would look plausible.
const (
	designCountRuns         = `SELECT COUNT(*) FROM design_run WHERE tech_card_id = :card`
	designCountArchivedRuns = `SELECT COUNT(*) FROM design_run WHERE tech_card_id = :card AND archived_at IS NOT NULL`
	designMaxRrev           = `SELECT COALESCE(MAX(rrev), 0) FROM design_run WHERE tech_card_id = :card`
	designCountBatches      = `SELECT COUNT(*) FROM design_batch WHERE tech_card_id = :card`
	// designCountFabricRenders — «ЕСТЬ ЛИ У КАРТОЧКИ ФАБРИК-РЕНДЕРЫ ВООБЩЕ» (W-13). Считается по
	// ВСЕЙ карточке и только по НЕСПРЯТАННЫМ кадрам: спрятанный рендер человек уже отверг.
	//
	// ⚠ ЭТО БОЛЬШЕ НЕ ТО, ЧТО ОТКРЫВАЕТ 3D, и прежняя формулировка («открывать им 3D значило бы
	// обещать дверь, за которую сервер откажет») эту волну не пережила: дверь открывает
	// designRenderBenchColorways — ЗАНЯТЫЕ РЕНДЕР-СЛОТЫ, потому что именно из них прогон собирает
	// входы. Два ответа законно расходятся: загруженный, но не поставленный рендер даёт здесь
	// единицу и оставляет то множество пустым. Оставлено как подсказка пустого состояния
	// («рендеров нет» против «рендеры есть, разложи их»), см. DesignBand.HasFabricRender.
	designCountFabricRenders = `SELECT COUNT(*) FROM design_picture
		WHERE tech_card_id = :card AND kind = 'render' AND hidden_at IS NULL`
	// designRenderBenchColorways — ВОРОТА 3D, И ОНИ СПРАШИВАЮТ ВЕРСТАК, А НЕ ПОЛОСУ (D5).
	//
	// Гейт обязан задавать РОВНО ТОТ ЖЕ ВОПРОС, что и отбор входов. 3D читает не картинки
	// карточки, а ЗАНЯТЫЕ РЕНДЕР-СЛОТЫ своего колорвея (designSelectBench: род слота = render,
	// колорвей слота = колорвей прогона, у слота есть плита с медиа). Прежний счёт по
	// design_picture отвечал на другой вопрос — «есть ли на карточке такой файл», — и расходился
	// с отбором ровно в главном случае: загруженный, но НЕ ПОСТАВЛЕННЫЙ рендер открывал дверь,
	// деньги дня резервировались, прогон уходил в работу с ПУСТЫМ набором плит. Оплаченный
	// прогон без входов — не редкость, а нормальный порядок работы: файл загружают раньше, чем
	// решают, на какую сторону его положить.
	//
	// JOIN на design_picture повторяет две последние проверки отбора (`slot.Picture != nil`,
	// `MediaId > 0`).
	//
	// ⚠ ТРЕТИЙ ПРЕДИКАТ, `hidden_at IS NULL`, У ОТБОРА ОТСУТСТВУЕТ, И ЭТО НАМЕРЕННАЯ
	// НЕСИММЕТРИЯ, а не «тот же вопрос» (F8). Отбор плит спрятанность не смотрит вовсе, а
	// attachSlotPictures прикрепляет спрятанную плиту наравне с прочими. То есть гейт СТРОЖЕ
	// отбора: множество занятых верстаков ⊆ множество верстаков, из которых отбор что-нибудь
	// возьмёт.
	//
	// НЕСИММЕТРИЯ ВЫБРАНА В СТОРОНУ ДЕНЕГ И ПРОВЕРЕНА ПО ОБОИМ ИСХОДАМ:
	//   * ложный ОТКАЗ (все плиты колорвея спрятаны, гейт закрыт) стоит одного клика «показать»;
	//   * ложное РАЗРЕШЕНИЕ (гейт открыт, отбор пуст) стоит оплаченного прогона без входов —
	//     ровно того, что D5 и закрывает.
	// Убрать `hidden_at` отсюда значило бы открывать дверь по спрятанной плите; добавить его в
	// отбор — вторая правка, меняющая ПОВЕДЕНИЕ уже уехавшего 3D (сегодня спрятанная плита в
	// слоте кормит прогон), и она этой волне не принадлежит.
	//
	// Остаточный перекос: у колорвея front спрятан, back виден — гейт открывает back, а прогон
	// возьмёт ОБА, включая спрятанный front. Это не потеря денег, но и не то, чего человек ждёт
	// от «спрятать». Долг записан здесь; чинится он в designSelectBench, вместе с решением, что
	// вообще значит спрятанная плита в занятом слоте (сегодня hidePictureGuards её не создаёт —
	// прятать стоящую в слоте отказано, — так что состояние достижимо только постановкой
	// спрятанной плиты либо прямой правкой базы).
	//
	// NULL схлопывается в 0: неатрибутированный легаси-верстак открывает дверь только
	// безколорвейному 3D. Скоуп — ВСЯ карточка, никакой страницы, ровно как у счётчика выше.
	designRenderBenchColorways = `SELECT DISTINCT COALESCE(s.colorway_id, 0) AS cw
		FROM design_bench_slot s
		JOIN design_picture p ON p.id = s.picture_id
		WHERE s.tech_card_id = :card AND s.kind = 'render'
		  AND p.media_id > 0 AND p.hidden_at IS NULL
		ORDER BY cw`
	// designListLayers — ПРОЕКЦИЯ СЛОЁВ ДЛЯ ПОЛОСЫ, И ОНА ИМЕНОВАННАЯ, ПОЭТОМУ КАЖДАЯ НОВАЯ
	// КОЛОНКА ПОПАДАЕТ СЮДА РУКАМИ. Пропуск не падает и ничего не логирует — полоса просто
	// сервирует поле нулём, и это ровно та форма, которую читают экраны: три колонки 0350 без
	// строки здесь сделали бы каждый слой «drawn» без файла за ним, а `raster_media_id` (0355) —
	// каждый закрашенный слой неотличимым от пустого до открытия редактора.
	//
	// `strokes` ЗДЕСЬ НЕТ НАМЕРЕННО, и это не противоречие: 512 KB на слой, слоёв на карточке
	// несколько, и список миниатюр не обязан возить их все. Растр — голый id, а не килобайты.
	designListLayers = `
		SELECT id, tech_card_id, base_media_id, rev, origin, source_media_id,
		       source_picture_id, raster_media_id, updated_by, updated_at
		FROM design_edit_layer WHERE tech_card_id = :card ORDER BY id`
)

// GetBand reads the whole band in ONE read transaction.
//
// THE AGGREGATES ARE COUNTED IN THAT SAME TRANSACTION, over the WHOLE card, never over the page
// that happens to have been loaded. total_runs, archived_runs, MAX(rrev), the colour-history
// chips and hidden_by_run are all "how much of this exists", and computing them from the loaded
// slice would silently truncate the header and the chips to whatever fitted in the page. That is
// not an optimisation question; a header that says «12 runs» when there are forty is wrong.
//
// ARCHIVED ROWS ARE IN THE PAGE, carrying their flag. The contract is explicit that hidden and
// archived travel WITH their flags and the client filters (Д1) — the server never lies about
// what exists.
func (s *Store) GetBand(ctx context.Context, cardID, runLimit int) (*entity.DesignBand, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	band := &entity.DesignBand{HiddenByRun: map[int]int{}, HiddenByBatch: map[int]int{}}
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		var err error

		if band.Bench, err = listBenchSlots(ctx, db, cardID); err != nil {
			return err
		}
		benchPtrs := make([]*entity.DesignBenchSlot, 0, len(band.Bench))
		for i := range band.Bench {
			benchPtrs = append(benchPtrs, &band.Bench[i])
		}
		if err = attachSlotPictures(ctx, rep, benchPtrs); err != nil {
			return err
		}
		if band.Budget, err = loadBudget(ctx, db, s.Now()); err != nil {
			return err
		}
		if band.References, err = storeutil.QueryListNamed[entity.DesignReference](ctx, db, `
			SELECT * FROM design_reference WHERE tech_card_id = :card ORDER BY ordinal, id`,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to list design references: %w", err)
		}
		// THE SHELF WALL AND ITS MARKS (0354), IN THIS SAME SNAPSHOT. The studio draws bench,
		// references and shelves in one frame; read separately they could disagree about which
		// instant of the card is on screen. Neither list is paged and neither needs to be — the
		// shelves are capped on the WRITE side (entity.MaxDesignAssetsPerCard), so «all of them»
		// is a bounded answer rather than an unbounded one, and the count on the wall is the whole
		// truth instead of «as much as fitted».
		if band.Assets, err = listAssets(ctx, db, cardID); err != nil {
			return err
		}
		// The file of each asset, in ONE batch. Without it a shelf tile has an id and no swatch,
		// which is the same defect a bench slot without its plate had.
		if err = attachAssetMedia(ctx, rep, band.Assets); err != nil {
			return err
		}
		if band.AssetPlacements, err = listAssetPlacements(ctx, db, cardID); err != nil {
			return err
		}
		// Layers WITHOUT their strokes: 512 KB is the cap per LAYER and a card may hold several,
		// so shipping them all would make every open of the tab cost megabytes to draw a list.
		//
		// ⚠ THE COLUMN LIST IS NAMED, SO EVERY NEW COLUMN MUST BE ADDED TO IT BY HAND, and the
		// three of 0350 are here for that reason. A projection that omits `origin` and its two
		// source ids does not fail — the band simply serves every layer as `drawn` with no file
		// behind it, which is precisely the shape the mixed-provenance warning reads.
		//
		// `raster_media_id` (0355) IS HERE AND `strokes` IS STILL NOT, and the two are not an
		// inconsistency: strokes are up to 512 KB per layer, the raster is a bare id. Omitting it
		// would not fail either — the band would simply serve every painted layer as unpainted, so
		// the tab could not tell a canvas with brushwork on it from an empty one until the editor
		// was opened.
		if band.Layers, err = storeutil.QueryListNamed[entity.DesignEditLayer](ctx, db,
			designListLayers, map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to list design edit layers: %w", err)
		}

		if band.TotalRuns, err = storeutil.QueryCountNamed(ctx, db, designCountRuns,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count design runs: %w", err)
		}
		if band.ArchivedRuns, err = storeutil.QueryCountNamed(ctx, db, designCountArchivedRuns,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count archived design runs: %w", err)
		}
		if band.MaxRrev, err = storeutil.QueryCountNamed(ctx, db, designMaxRrev,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to read design max rrev: %w", err)
		}
		if band.ColourRecipes, err = loadColourRecipes(ctx, db, cardID); err != nil {
			return err
		}
		if band.TotalBatches, err = storeutil.QueryCountNamed(ctx, db, designCountBatches,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to count design batches: %w", err)
		}
		renders, err := storeutil.QueryCountNamed(ctx, db, designCountFabricRenders,
			map[string]any{"card": cardID})
		if err != nil {
			return fmt.Errorf("failed to count design fabric renders: %w", err)
		}
		band.HasFabricRender = renders > 0
		cwRows, err := storeutil.QueryListNamed[struct {
			Cw int `db:"cw"`
		}](ctx, db, designRenderBenchColorways, map[string]any{"card": cardID})
		if err != nil {
			return fmt.Errorf("failed to list design render bench colourways: %w", err)
		}
		band.RenderBenchColorways = make([]int, 0, len(cwRows))
		for _, r := range cwRows {
			band.RenderBenchColorways = append(band.RenderBenchColorways, r.Cw)
		}

		if band.HiddenByRun, err = loadHiddenCounts(ctx, db, cardID, "run_id"); err != nil {
			return err
		}
		if band.HiddenByBatch, err = loadHiddenCounts(ctx, db, cardID, "batch_id"); err != nil {
			return err
		}

		// THE BAND'S OWN PAGE IS TAKEN WITH IncludeArchived TRUE, and the token it mints carries
		// that flag. A cursor born over the unfiltered list and then continued with
		// include_archived=false would change the row set MID-PAGINATION and skip rows in
		// silence; making the flag part of the token turns that from a hope about the client into
		// a property of the server.
		page, err := listRunsTx(ctx, rep, entity.DesignRunPage{
			TechCardId: cardID, Limit: runLimit, IncludeArchived: true,
		})
		if err != nil {
			return err
		}
		band.Runs, band.NextCursor = page.Runs, page.NextCursor
		band.Batches, band.NextBatchCursor = page.Batches, page.NextBatchCursor
		return nil
	})
	if err != nil {
		return nil, err
	}
	return band, nil
}

// ListRuns returns one page of the history WITH the pictures of that page. A flat picture list
// beside the rows would ship 120 MediaFull for a card with 40 runs of 3 outputs.
func (s *Store) ListRuns(ctx context.Context, p entity.DesignRunPage) (*entity.DesignRunPageResult, error) {
	if err := requireCard(p.TechCardId); err != nil {
		return nil, err
	}
	var out entity.DesignRunPageResult
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := listRunsTx(ctx, rep, p)
		if err != nil {
			return err
		}
		out = *res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// listRunsTx is the keyset page. A CURSOR, NOT AN OFFSET: rows are born at the HEAD of this list,
// and an offset page would duplicate and skip rows exactly while somebody is generating. The
// cursor is the id of the last row returned, and `id < cursor` under `ORDER BY id DESC` is a
// stable keyset because design_run.id is a monotone AUTO_INCREMENT.
//
// A cursor minted by GetBand (which includes archived rows) stays valid for a ListRuns call that
// excludes them: the predicate narrows, the keyset does not move.
func listRunsTx(ctx context.Context, rep dependency.Repository, p entity.DesignRunPage) (*entity.DesignRunPageResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultRunPageLimit
	}
	if limit > MaxRunPageLimit {
		limit = MaxRunPageLimit
	}
	db := rep.DB()
	where := "tech_card_id = :card"
	params := map[string]any{"card": p.TechCardId, "limit": limit + 1}
	if !p.IncludeArchived {
		where += " AND archived_at IS NULL"
	}
	if p.Cursor > 0 {
		where += " AND id < :cursor"
		params["cursor"] = p.Cursor
	}
	runs, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db,
		`SELECT * FROM design_run WHERE `+where+` ORDER BY id DESC LIMIT :limit`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list design runs: %w", err)
	}
	res := &entity.DesignRunPageResult{}
	if len(runs) > limit {
		runs = runs[:limit]
		res.NextCursor = runs[len(runs)-1].Id
	}
	// The upload shelves ride in the same page and carry their OWN keyset. With the generative
	// machine cut from this wave they are not a secondary branch — they are the only source of
	// pictures, and a batch picture hangs under no history row by construction.
	if res.Batches, res.NextBatchCursor, err = loadBatches(ctx, rep, p.TechCardId, limit, p.BatchCursor); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return res, nil
	}

	ids := make([]int, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.Id)
	}
	pics, err := loadPicturesByRuns(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	attempts, err := storeutil.QueryListNamed[entity.DesignRunAttempt](ctx, db,
		`SELECT * FROM design_run_attempt WHERE run_id IN (:ids) ORDER BY run_id, attempt_no`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to load design run attempts: %w", err)
	}
	byRun := map[int][]entity.DesignRunAttempt{}
	for _, a := range attempts {
		byRun[a.RunId] = append(byRun[a.RunId], a)
	}

	var flat []*entity.DesignPicture
	for i := range runs {
		runs[i].Attempts = byRun[runs[i].Id]
		runs[i].Pictures = pics[runs[i].Id]
		for j := range runs[i].Pictures {
			flat = append(flat, &runs[i].Pictures[j])
		}
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return nil, err
	}
	res.Runs = runs
	return res, nil
}

// loadColourRecipes builds the chips of the colour history.
//
// FORMAT, since the plan does not fix one: the RAW JSON of design_run.params->'$.colour' of the
// card's render runs, newest first, de-duplicated by the encoded recipe, capped at
// MaxColourRecipes. The chip restores a RECIPE and never a picture — the recipe migrates, the
// pixels do not — so the store hands the recipe object through untouched rather than inventing a
// flattened shape the contract would then have to mirror.
//
// The JSON PATH IS SNAKE_CASE because the writer stores protojson with UseProtoNames: true. If
// wave 2's StartRun ever writes default protojson (lowerCamelCase), this query returns nothing
// and the chips vanish with no error anywhere. See entity.DesignRunJSONFieldColour.
func loadColourRecipes(ctx context.Context, db dependency.DB, cardID int) ([]json.RawMessage, error) {
	raw, err := storeutil.QueryScalarListNamed[[]byte](ctx, db, `
		SELECT JSON_EXTRACT(params, '$.colour')
		FROM design_run
		WHERE tech_card_id = :card AND kind = 'render'
			AND params IS NOT NULL AND JSON_EXTRACT(params, '$.colour') IS NOT NULL
		ORDER BY id DESC
		LIMIT :scan`,
		map[string]any{"card": cardID, "scan": colourRecipeScanRuns})
	if err != nil {
		return nil, fmt.Errorf("failed to read design colour recipes: %w", err)
	}
	out := make([]json.RawMessage, 0, len(raw))
	seen := map[string]struct{}{}
	for _, r := range raw {
		if len(r) == 0 || string(r) == "null" {
			continue
		}
		if _, ok := seen[string(r)]; ok {
			continue
		}
		seen[string(r)] = struct{}{}
		out = append(out, json.RawMessage(append([]byte(nil), r...)))
		if len(out) >= MaxColourRecipes {
			break
		}
	}
	return out, nil
}

// loadHiddenCounts counts, over the WHOLE card, how many pictures of each run (or of each batch)
// are hidden.
//
// FORMAT, since the plan does not fix one: owner id → count, and an owner with nothing hidden is
// ABSENT rather than present with a zero — «· 2 hidden» is a badge, and a zero badge is noise.
// Rows whose owner column is NULL are excluded: a key of 0 would read as «no owner», and the
// badge belongs to the collapsed row, not to the orphan.
//
// THE AGGREGATE EXISTS BECAUSE BOTH LISTS ARE PAGED. A run or a shelf that is off the page has
// no pictures in the response, so the client has nothing to count — the header would silently
// lose exactly the part that is not on screen.
//
// The column name is interpolated, NOT bound: it is one of two literals chosen right here, never
// caller input. sqlx would bind it as a string value and the query would compare a column to the
// text "run_id".
func loadHiddenCounts(ctx context.Context, db dependency.DB, cardID int, ownerCol string) (map[int]int, error) {
	if ownerCol != "run_id" && ownerCol != "batch_id" {
		return nil, fmt.Errorf("unsupported design hidden-count column %q", ownerCol)
	}
	type row struct {
		Owner int `db:"owner"`
		N     int `db:"n"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, db, `
		SELECT `+ownerCol+` AS owner, COUNT(*) AS n
		FROM design_picture
		WHERE tech_card_id = :card AND hidden_at IS NOT NULL AND `+ownerCol+` IS NOT NULL
		GROUP BY `+ownerCol,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to count hidden design pictures: %w", err)
	}
	out := make(map[int]int, len(rows))
	for _, r := range rows {
		out[r.Owner] = r.N
	}
	return out, nil
}

// loadBatches reads the card's upload shelves WITH their pictures, newest first.
//
// THIS IS THE MAIN READ OF THE WAVE, not a secondary branch. The generative machine is cut from
// it entirely, so on beta there will be no runs at all and every picture will arrive through a
// batch. And a batch picture hangs under NO history row by construction — design_picture.run_id
// is NULL for a manual upload and design_run has no row to express the gesture — so without this
// the upload shelf is empty forever after the first tab reload.
//
// THE CEILING IS MaxBandBatches, declared in the same shape as the contract's other limits
// (10 §6). What happens after it: those batches are NOT shipped and get no badge either, which
// is honest — they are not on the screen. TotalBatches makes the overflow measurable, and a card
// that exceeds the ceiling needs a paged batch read that does not exist yet.
func loadBatches(ctx context.Context, rep dependency.Repository, cardID, limit, cursor int) ([]entity.DesignBatch, int, error) {
	db := rep.DB()
	if limit <= 0 || limit > MaxBandBatches {
		limit = MaxBandBatches
	}
	where := "tech_card_id = :card"
	params := map[string]any{"card": cardID, "limit": limit + 1}
	if cursor > 0 {
		where += " AND id < :cursor"
		params["cursor"] = cursor
	}
	batches, err := storeutil.QueryListNamed[entity.DesignBatch](ctx, db,
		`SELECT * FROM design_batch WHERE `+where+` ORDER BY id DESC LIMIT :limit`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list design batches: %w", err)
	}
	next := 0
	if len(batches) > limit {
		batches = batches[:limit]
		next = batches[len(batches)-1].Id
	}
	if len(batches) == 0 {
		return batches, next, nil
	}
	ids := make([]int, 0, len(batches))
	for _, b := range batches {
		ids = append(ids, b.Id)
	}
	pics, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
		SELECT * FROM design_picture WHERE batch_id IN (:ids) ORDER BY batch_id, ordinal, id`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load design batch pictures: %w", err)
	}
	byBatch := map[int][]entity.DesignPicture{}
	for _, p := range pics {
		if !p.BatchId.Valid {
			continue
		}
		byBatch[int(p.BatchId.Int32)] = append(byBatch[int(p.BatchId.Int32)], p)
	}
	var flat []*entity.DesignPicture
	for i := range batches {
		batches[i].Pictures = byBatch[batches[i].Id]
		for j := range batches[i].Pictures {
			flat = append(flat, &batches[i].Pictures[j])
		}
	}
	if err := resolveMedia(ctx, rep, flat); err != nil {
		return nil, 0, err
	}
	return batches, next, nil
}

// GetBudget reports today's money bar. The DAY KEY IS COMPUTED IN GO, in the organisation's
// timezone — the MySQL session's day is a property of whichever server answered, not an answer
// of the organisation.
func (s *Store) GetBudget(ctx context.Context) (entity.DesignBudget, error) {
	var b entity.DesignBudget
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		b, err = loadBudget(ctx, rep.DB(), s.Now())
		return err
	})
	return b, err
}

// GetSettings reads the singleton row that IS the band's whole configuration.
func (s *Store) GetSettings(ctx context.Context) (entity.DesignSettings, error) {
	var out entity.DesignSettings
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		out, err = loadSettings(ctx, rep.DB())
		return err
	})
	return out, err
}

func loadSettings(ctx context.Context, db dependency.DB) (entity.DesignSettings, error) {
	s, err := storeutil.QueryNamedOne[entity.DesignSettings](ctx, db, `
		SELECT currency, budget_timezone, updated_by, updated_at
		FROM design_settings WHERE id = 1`, map[string]any{})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 0344 seeds the row with INSERT IGNORE, so this is only reachable if somebody
			// deleted it. Falling back to the schema defaults keeps the band readable instead of
			// making a missing configuration row look like a broken card.
			// ⚠ ЗДЕСЬ ЖИЛА ЛОВУШКА, И ОНА УМЕРЛА ВМЕСТЕ С КОЛОНКОЙ (0358). Фолбэк отдавал
			// DailyBudget = 0, а ноль значил «сегодня не запускаем», — то есть инсталляция, у
			// которой строку синглтона кто-то удалил, была ЗАКРЫТА НАВСЕГДА, и сказано это было
			// бы теми же словами «потолок исчерпан», что и обычное исчерпание. Теперь отсутствие
			// строки не может закрыть полосу: закрывать нечем.
			return entity.DesignSettings{
				Currency:       "USD",
				BudgetTimezone: "Europe/Warsaw",
			}, nil
		}
		return s, fmt.Errorf("failed to read design settings: %w", err)
	}
	return s, nil
}

// DesignBudgetDayKey is the day key of an instant in the organisation's timezone. Exported
// because wave 2's StartRun reserves against exactly this key and the two must not compute it
// differently.
func DesignBudgetDayKey(now time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		// An unloadable zone name must not silently become the server's own local day, which
		// would move the reset by hours without telling anyone. UTC is the neutral fallback and
		// it is the one the column's own default day would agree with.
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}

func loadBudget(ctx context.Context, db dependency.DB, now time.Time) (entity.DesignBudget, error) {
	set, err := loadSettings(ctx, db)
	if err != nil {
		return entity.DesignBudget{}, err
	}
	day := DesignBudgetDayKey(now, set.BudgetTimezone)
	b := entity.DesignBudget{
		Day:      day,
		Spent:    decimal.Zero,
		Reserved: decimal.Zero,
		Currency: set.Currency,
		Timezone: set.BudgetTimezone,
	}
	type row struct {
		Reserved decimal.Decimal `db:"reserved"`
		Spent    decimal.Decimal `db:"spent"`
		Currency string          `db:"currency"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, db,
		`SELECT reserved, spent, currency FROM design_budget_day WHERE day = :day`,
		map[string]any{"day": day})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No row for today is not an error — it is a day on which nothing has been spent.
			return b, nil
		}
		return b, fmt.Errorf("failed to read design budget day: %w", err)
	}
	b.Reserved, b.Spent = r.Reserved, r.Spent
	if r.Currency != "" {
		b.Currency = r.Currency
	}
	return b, nil
}
