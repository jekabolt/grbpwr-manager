package design

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// THE CARD'S ASSET SHELVES (0354, V-11) — cloths, patterns and hardware — and the marks those
// assets leave on the flats.
//
// ONE TABLE WITH A `kind`, NOT THREE, and the whole argument lives in the head of
// 0354_design_asset.sql rather than here. What this file owns is the half of it Go has to enforce,
// because the schema deliberately cannot: `kind` carries no CHECK (a late ADD CONSTRAINT is a full
// table COPY under a hardcoded five-minute migration ceiling, i.e. a halted production start), the
// pattern-only fields carry no CHECK either, and «this asset and this picture are the SAME card's»
// is not expressible as a foreign key at all.
//
// EVERY ONE OF THOSE REFUSALS IS READ INSIDE THE WRITE TRANSACTION. It is already SERIALIZABLE
// (see the package header), so «read, check, write» is honest here — and a guard read outside it
// would be a TOCTOU with a nicer name.

// assetByID reads one shelf row inside the caller's transaction.
func assetByID(ctx context.Context, db dependency.DB, id int) (entity.DesignAsset, error) {
	a, err := storeutil.QueryNamedOne[entity.DesignAsset](ctx, db,
		`SELECT * FROM design_asset WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("%w: design asset %d", entity.ErrDesignNotFound, id)
		}
		return a, fmt.Errorf("failed to read design asset %d: %w", id, err)
	}
	return a, nil
}

// requireAssetOfCard reads the row and refuses one that belongs to a DIFFERENT card.
//
// ⚠ THERE IS NO «DO NOT CHECK» VALUE OF cardID, AND THAT ABSENCE IS THE POINT. This function used
// to skip the comparison on cardID <= 0, and the delete verbs used to hand it exactly that — which
// meant the one gesture in this file that CASCADES was also the one gesture with no card boundary
// at all. The argument for it was that a minted id already names its card, so asking the client to
// repeat it would be asking for a fact it can only get wrong. That reads the request wrongly: the
// card in a delete request is not a copy of the id's own property, it is the CALLER'S BELIEF about
// which shelf wall it is looking at, and the whole value of stating it is that the server can
// refuse when belief and fact disagree. Every caller now names a card, so a bad state is not
// checked for here — it cannot be spelled.
func requireAssetOfCard(ctx context.Context, db dependency.DB, cardID, assetID int) (entity.DesignAsset, error) {
	if err := requireCard(cardID); err != nil {
		return entity.DesignAsset{}, err
	}
	a, err := assetByID(ctx, db, assetID)
	if err != nil {
		return a, err
	}
	if a.TechCardId != cardID {
		return a, fmt.Errorf("%w: design asset %d belongs to tech card %d",
			entity.ErrDesignNotFound, a.Id, a.TechCardId)
	}
	return a, nil
}

// refuseFullShelf — потолок полок карточки, посчитанный В ТРАНЗАКЦИИ ВЫЗЫВАЮЩЕГО.
//
// THE CEILING IS COUNTED IN THIS TRANSACTION, not before it. Counted outside, two people adding the
// last cloth and the one after it at the same moment both see one free place.
//
// ⚠ ОТДЕЛЬНОЙ ФУНКЦИЕЙ, ПОТОМУ ЧТО ПИСАТЕЛЕЙ ПОЛКИ СТАЛО ДВА. Второй — посадка плитки при закрытии
// прогона паттерна (keepPatternTx, queue.go), и он приходит сюда через минуты после того, как
// дверь уже спросила то же самое у полосы. Одна проверка без другой была бы либо TOCTOU, либо
// платой за заведомо невозможную посадку; два написания одного счёта разошлись бы молча.
//
// freeing — сколько строк полки ЭТА ЖЕ транзакция гарантированно уберёт (B-m1): посадка прогона,
// которая заменяет на паре снимок фурнитуры, собираемый GC, не прибавляет полке строку, а меняет
// одну на другую, и отказать ей library_full на 120 значило бы выбросить оплаченный результат,
// который полку не переполнил бы. Все прочие звателя передают 0.
func refuseFullShelf(ctx context.Context, db dependency.DB, cardID, freeing int) error {
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM design_asset WHERE tech_card_id = :card`,
		map[string]any{"card": cardID})
	if err != nil {
		return fmt.Errorf("failed to count design assets: %w", err)
	}
	if shelfFull(n, freeing) {
		return fmt.Errorf("%w: tech card %d already holds %d shelf rows, the ceiling is %d",
			entity.ErrDesignAssetTooMany, cardID, n, entity.MaxDesignAssetsPerCard)
	}
	return nil
}

// shelfFull — чистый счёт потолка: n строк на полке, freeing из них уйдут в той же транзакции.
func shelfFull(n, freeing int) bool {
	if freeing < 0 {
		freeing = 0
	}
	return n-freeing >= entity.MaxDesignAssetsPerCard
}

// insertAssetTx — ОДНА строка полки, вставленная в транзакции вызывающего.
//
// ⚠ ОДИН INSERT НА ДВУХ ПИСАТЕЛЕЙ, И ЭТО НЕ ЭКОНОМИЯ СТРОК. Колонок у design_asset четырнадцать;
// второй список колонок рядом с первым — это место, где однажды забудут `created_by` или
// `ordinal`, и заметить это будет нечем: строка вставится, просто беднее. Именованные параметры
// ровно те же, что собирает UpsertAsset, поэтому у обоих писателей ОДИН набор обязательных полей.
func insertAssetTx(ctx context.Context, db dependency.DB, params map[string]any) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO design_asset
			(tech_card_id, kind, name, media_id, colour_code, colour_hex, note,
			 derived_from_asset_id, repeat_mm, rotation_deg, ordinal,
			 created_by, created_at, updated_at)
		VALUES
			(:card, :kind, :name, :media, :colour_code, :colour_hex, :note,
			 :parent, :repeat_mm, :rotation, :ord,
			 :who, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, params)
	if err != nil {
		return 0, fmt.Errorf("failed to create design asset: %w", err)
	}
	return id, nil
}

// stealColorwayTx — КРАЖА: колорвей снимается со всех прочих ассетов ЭТОЙ карточки.
//
// Скоуп — карточка, потому что дом факта — полка карточки, и колорвей принадлежит ей же (у
// SetAssetColorway это только что доказал assertColorwayOfCard). `id <> :id` оставляет строку-цель
// в покое: повторное назначение того же ассета тому же колорвею обязано быть идемпотентным, а не
// снять и вернуть.
//
// ⚠ ВТОРОЙ ЗВАТЕЛЬ — ПОСАДКА ПЛИТКИ БЕЗ СЛОТА (keepPatternTx), И ТАМ КРАЖА ОБЯЗАНА ИДТИ ДО
// ВСТАВКИ. uq_design_asset_colorway (tech_card_id, colorway_id) — настоящий UNIQUE: вставить нового
// носителя, пока прежний ещё носит, значит получить 1062 на уже оплаченном прогоне. У ассета,
// которого ещё нет, нет и id — отсюда keepID = 0, «не щадить никого»: строки с id 0 не бывает,
// поэтому условие `id <> 0` истинно для всех и означает ровно «снять со всех».
func stealColorwayTx(ctx context.Context, db dependency.DB, cardID, colorwayID, keepID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE design_asset SET colorway_id = NULL, updated_at = UTC_TIMESTAMP(6)
		WHERE tech_card_id = :card AND colorway_id = :cw AND id <> :id`,
		map[string]any{"card": cardID, "cw": colorwayID, "id": keepID}); err != nil {
		return fmt.Errorf("failed to clear colourway %d off the other assets of tech card %d: %w",
			colorwayID, cardID, err)
	}
	return nil
}

// keepPatternTx САЖАЕТ ГОТОВУЮ ПЛИТКУ НА ПОЛКУ КАРТОЧКИ — в той же транзакции, что закрывает
// прогон паттерна.
//
// ═══ ПОЧЕМУ ПОСАДКА ЖИВЁТ ЗДЕСЬ, А НЕ ВТОРЫМ ЖЕСТОМ ЧЕЛОВЕКА ════════════════════════════════════
//
// Владелец (J-12): «тут мы делаем только сам паттерн выбираем ему название и колорвей и все».
// «И всё» — это один жест. До круга 15 их было три: сделать плитку, нажать KEEP, назначить носку;
// две из трёх делались ПОСЛЕ денег и потому терялись — картинка оставалась в ленте, а полка карточки
// не знала о ней ничего. Имя и колорвей называются ДО денег (дверь отказывает `pattern_name_required`
// бесплатно), поэтому к моменту прилёта известно всё, что нужно строке полки.
//
// ⚠ И ЭТО ВТОРОЙ ПИСАТЕЛЬ design_asset.colorway_id, ЧТО НЕ ОТМЕНЯЕТ ПРАВИЛА, А НАЗЫВАЕТ ЕГО
// ГРАНИЦУ. Правило «пишет только SetDesignAssetColorway» защищает от ЗАТИРАНИЯ: Upsert — полная
// замена, и proto3-скаляр в нём приезжал бы нулём от всякого клиента, снимая ткань с колорвея
// молча. Здесь затирать нечего — строки ещё не существует, и колорвей у неё ровно тот, который
// прогон назвал до денег. Комментарий у поля в design.proto обновлён теми же словами.
//
// ⚠ КРАЖА ИДЁТ ДО ВСТАВКИ, И ЭТО НЕ ПОРЯДОК РАДИ ПОРЯДКА. uq_design_asset_colorway
// (tech_card_id, colorway_id) — настоящий UNIQUE; вставка второго носителя того же колорвея дала бы
// 1062 на прогоне, за который уже заплачено, и откатила бы вместе с собой ВСЮ выдачу.
//
// ⚠ ПОЛКА, ПЕРЕПОЛНИВШАЯСЯ ПОКА ПРОГОН ШЁЛ, — НЕ ОТКАЗ, А ЗАПИСЬ. Картинка куплена: провалить
// прилёт значило бы выбросить оплаченный результат и оставить байты в бакете ничьими. Поэтому кадр
// остаётся filed, строка закрывается `done`, а `error_code = 'library_full'` говорит человеку, что
// плитка есть, а места на полке для неё не нашлось. Дверь спрашивала то же самое ДО денег и в
// обычном случае этой ветки не бывает.
//
// ЧТО МОЛЧА НЕ ДЕЛАЕТСЯ. Прогон, замороженный до круга 15 (нет `params.pattern.name`), не сажает
// ничего: имени взять негде, а выдуманное приехало бы в следующий промпт словом «pattern». Такие
// плитки кладёт человек, как и раньше.
//
// STEP 3 (0368) ДОБАВИЛ ДВА ФАКТА: плитка запоминает код и hex заявленного цвета, а плитка,
// сделанная для пары (колорвей, слот), становится тканью этой пары — см. bindKeptPatternTx.
//
// ⚠ И У ПЛИТКИ СЛОТА ЛЕГАСИ-ЗАПИСИ НЕТ ВОВСЕ — ни кражи, ни UPDATE design_asset.colorway_id (ревью
// STEP 3). Свотч слота — ткань ПАРЫ («white → outer»), а не всего колорвея: записать его в колонку
// «ткань колорвея целиком» значило бы сказать неправду о подкладке того же цвета, а колонка —
// single-select, так что при свотче на каждый слот она прыгала бы на «самый свежий свотч любого
// слота» и отнимала колорвей у ткани, которую человек назначил руками. Колонка сегодня только
// пишется: ни экран шага, ни промпт ткань из неё не выводят (читают её лишь сторожа — вердикт
// удаления и перепривязка колорвея, N2 у Upsert), так что у слотового прогона ей нечего сообщать.
// Прогон БЕЗ слота (bom_item_id 0 — каждый image-прогон и каждый, замороженный до STEP 3) крадёт и
// пишет колорвей ровно как до 0368.
func keepPatternTx(ctx context.Context, db dependency.DB, run entity.DesignRun, p designRunParams, mediaID int) error {
	if p.Pattern == nil || mediaID <= 0 {
		return nil
	}
	name := strings.TrimSpace(p.Pattern.Name)
	if name == "" {
		return nil
	}
	// ⚠ ОБРЕЗКА, А НЕ ОТКАЗ, И ТОЛЬКО ЗДЕСЬ. Дверь уже держит 60 знаков (то же правило, что у
	// UpsertDesignAsset.name — колонка одна), так что этой ветки в честном пути не бывает. Но
	// колонка VARCHAR(60) в строгом режиме отвечает на переполнение ошибкой 1406, и она уронила бы
	// прилёт УЖЕ ОПЛАЧЕННОЙ картинки. Между «имя короче на хвост» и «выброшенный результат» выбор
	// не близкий.
	if r := []rune(name); len(r) > entity.MaxDesignAssetNameRunes {
		name = strings.TrimSpace(string(r[:entity.MaxDesignAssetNameRunes]))
	}
	// ⚠ ТОТ ЖЕ ПОЯС ДЛЯ РАППОРТА, И ПО ТОЙ ЖЕ ПРИЧИНЕ. Колонка `repeat_mm` — SMALLINT UNSIGNED
	// (0354): отрицательное или пятизначное число отдаёт 1264 в строгом режиме и уносит с собой
	// прилёт оплаченной картинки. Дверь держит ту же границу ДО денег
	// (entity.MaxDesignAssetRepeatMm), так что в честном пути эта ветка недостижима — она про
	// прогоны, замороженные раньше двери, и про клиентов, которых у нас нет.
	repeat := p.Pattern.RepeatMM
	if repeat < 0 {
		repeat = 0
	}
	if repeat > entity.MaxDesignAssetRepeatMm {
		repeat = entity.MaxDesignAssetRepeatMm
	}
	// ФУРНИТУРА (mode hardware) — НЕ ПЛИТКА: садится ассетом рода hardware, без раппорта и без
	// родословной, и привязывается к паре ровно как свотч. Колорвей целиком она не носит никогда
	// (SetAssetColorway ей отказывает), поэтому legacy-колонку не трогает и без слота.
	// Бирка (mode label) садится так же: тот же род hardware, та же пара.
	hardware := p.Pattern.Mode == entity.DesignPatternModeHardware || p.Pattern.Mode == entity.DesignPatternModeLabel ||
		p.Pattern.Mode == entity.DesignPatternModeArtwork
	kind := entity.DesignAssetKindPattern
	if hardware {
		kind = entity.DesignAssetKindHardware
		repeat = 0
	}

	// КОЛОРВЕЙ БЕРЁТСЯ ИЗ ЖИВОЙ КОЛОНКИ, А НЕ ИЗ ЗАМОРОЖЕННЫХ params, И РАЗНИЦА СОДЕРЖАТЕЛЬНАЯ:
	// колорвей законно удаляют между стартом и прилётом, FK гасит колонку в NULL, и посадка на
	// несуществующий id упала бы внешним ключом. Ноль здесь значит «плитка встаёт на полку ничьей»,
	// ровно то же, что случилось со строкой прогона.
	cw := entity.DesignColorwayOrNone(run.ColorwayId)
	// Плитка слота колорвей целиком не носит (см. шапку): ей нечего и красть.
	slot := p.Pattern.BomItemId > 0

	// ⚠ ПОТОЛОК СЧИТАЕТСЯ НЕТТО (B-m1). Перезаказ снимка фурнитуры/картинки на ту же пару
	// собирает прежний снимок в этой же транзакции (bindKeptPatternTx → dropSupersededHardwareTx),
	// так что полка на 120 после посадки останется на 120. Предикат — тот же, что у GC, с учётом
	// переезда меток (B-m2): см. landingFreesShelfRowTx.
	freeing := 0
	if cw > 0 && slot {
		frees, err := landingFreesShelfRowTx(ctx, db, run.TechCardId, cw, p.Pattern.BomItemId, hardware)
		if err != nil {
			return err
		}
		if frees {
			freeing = 1
		}
	}
	if err := refuseFullShelf(ctx, db, run.TechCardId, freeing); err != nil {
		if errors.Is(err, entity.ErrDesignAssetTooMany) {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE design_run SET error_code = :code WHERE id = :id`,
				map[string]any{"code": entity.DesignErrorCodeLibraryFull, "id": run.Id}); err != nil {
				return fmt.Errorf("failed to record that run %d had nowhere to file its tile: %w", run.Id, err)
			}
			return nil
		}
		return err
	}

	if cw > 0 && !slot && !hardware {
		if err := stealColorwayTx(ctx, db, run.TechCardId, cw, 0); err != nil {
			return err
		}
	}

	// РОДИТЕЛЬ ПРОВЕРЯЕТСЯ, А НЕ ПРИНИМАЕТСЯ НА ВЕРУ, и не найденный — это ПУСТО, а не отказ.
	// Дверь проверила принадлежность у говорящего; полку законно удаляют, пока прогон идёт, и
	// вставка с висящим id упала бы внешним ключом на оплаченном результате. Паттерн без
	// родословной — законное состояние: ровно в него его переводит ON DELETE SET NULL.
	//
	// ⚠ И ПОЛКА ПРОВЕРЯЕТСЯ ТОЖЕ, А НЕ ТОЛЬКО КАРТОЧКА. Контракт `source_asset_id` называет
	// `fabric|pattern`, а `derived_from_asset_id` — «паттерн, сделанный из ткани»; фурнитура
	// родителем принта не бывает ни в одном чтении, и строка «этот принт сделан из молнии»
	// пережила бы прогон навсегда. Дверь отказывает такому источнику ДО денег; здесь родословная
	// просто не пишется — провалить прилёт УЖЕ ОПЛАЧЕННОЙ плитки из-за поля, которое законно
	// пустует, было бы дороже правды, которую оно несёт.
	parent := 0
	if src := p.Pattern.SourceAssetID; src > 0 && !hardware {
		switch a, err := assetByID(ctx, db, src); {
		case err != nil && !errors.Is(err, entity.ErrDesignNotFound):
			return err
		case err == nil && a.TechCardId == run.TechCardId &&
			(a.Kind == entity.DesignAssetKindFabric || a.Kind == entity.DesignAssetKindPattern):
			parent = src
		}
	}

	// СВОТЧ ПОМНИТ СВОЙ ЦВЕТ (STEP 3). Код и hex заявленного цвета — те самые, из которых плитка
	// построена, — ложатся в колонки ассета, которые до этого у посаженной плитки пустовали. Без них
	// свотч «Pantone 18-1664» на полке был бы безымянным квадратом красного, и выбор ткани для слота
	// шёл бы на глаз. Пустое — NULL, как у UpsertAsset: «не сказано» не записывается пустой строкой.
	code, hex := "", ""
	if c := p.Colour; c != nil {
		code = keptColourFact(ctx, run.Id, "colour_code", c.Code, designAssetColourCodeMax)
		hex = keptColourFact(ctx, run.Id, "colour_hex", c.Hex, designAssetColourHexMax)
	}

	// ПРОСЬБА ПРОГОНА САДИТСЯ ЗАМЕТКОЙ АССЕТА (ROUND3 B1) — для свотча, фурнитуры и картинки
	// одинаково: «brass zip, 5 mm teeth» без неё теряется на полке. Обрезка, а не отказ, по той же
	// причине, что у имени: заметка не стоит оплаченной картинки. Пустое — NULL, как у UpsertAsset.
	note := strings.TrimSpace(run.Ask.String)
	if r := []rune(note); len(r) > entity.MaxDesignAssetNoteRunes {
		note = strings.TrimSpace(string(r[:entity.MaxDesignAssetNoteRunes]))
	}

	id, err := insertAssetTx(ctx, db, map[string]any{
		"card":        run.TechCardId,
		"kind":        kind,
		"name":        name,
		"media":       nullInt(mediaID),
		"colour_code": nullStr(code),
		"colour_hex":  nullStr(hex),
		"note":        nullStr(note),
		"parent":      nullInt(parent),
		"repeat_mm":   repeat,
		"rotation":    0,
		"ord":         0,
		"who":         run.Author,
	})
	if err != nil {
		return err
	}
	if cw == 0 {
		if slot {
			// СДЕЛАНА ДЛЯ СЛОТА, НО КОЛОРВЕЯ У ПРОГОНА БОЛЬШЕ НЕТ (FK погасил колонку, пока прогон
			// шёл) — пары, которую надо перепривязать, не существует. Плитка остаётся на полке.
			slog.WarnContext(ctx, "design: pattern tile landed unbound — its colourway is gone",
				slog.Int("run_id", run.Id), slog.Int("asset_id", id),
				slog.Int("bom_item_id", p.Pattern.BomItemId))
		}
		return nil
	}
	if slot {
		// ТКАНЬ ПАРЫ, А НЕ КОЛОРВЕЯ: legacy-колонка остаётся NULL, пишется только связка.
		return bindKeptPatternTx(ctx, db, run, cw, p.Pattern.BomItemId, id, hardware)
	}
	if hardware {
		// Без слота фурнитура остаётся на полке ничьей: носить колорвей целиком ей нельзя.
		return nil
	}
	// ⚠ НОСКА — ОТДЕЛЬНЫМ UPDATE, И ЭТО НАМЕРЕННО. Колонки colorway_id НЕТ в общем INSERT, которым
	// пользуется UpsertAsset, и её там не будет: держать её вне того оператора — это и есть
	// структурная гарантия «Upsert колорвей не несёт и не гасит». Оба оператора идут в ОДНОЙ
	// SERIALIZABLE-транзакции, поэтому окна, в котором ассет уже есть, а носка ещё нет, не
	// существует ни для одного читателя.
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE design_asset SET colorway_id = :cw, updated_at = UTC_TIMESTAMP(6)
		WHERE id = :id AND tech_card_id = :card`,
		map[string]any{"cw": cw, "id": id, "card": run.TechCardId}); err != nil {
		return fmt.Errorf("failed to give the kept pattern of run %d to colourway %d: %w", run.Id, cw, err)
	}
	return nil
}

// bindKeptPatternTx — ПЛИТКА, СДЕЛАННАЯ ДЛЯ ПАРЫ, СТАНОВИТСЯ ТКАНЬЮ ЭТОЙ ПАРЫ (STEP 3, 0368), в той
// же транзакции, что её посадила.
//
// ПЕРЕПРИВЯЗКА, А НЕ «ЕСЛИ ПУСТО». Самый свежий свотч — ровно то, о чём человек только что попросил
// и за что заплатил; прежние остаются на полке кандидатами и возвращаются руками
// (SetDesignAssetBinding). Запись — тот же upsert, что у глагола: писателей два, оператор один.
//
// ⚠ СЛОТ ПРОВЕРЯЕТСЯ ЗДЕСЬ ЖЕ, И ПРОМАХ — НЕ ОТКАЗ. Дверь отказала чужой строке BOM ДО денег, но
// между дверью и прилётом строку законно удаляют либо переносят, а вставка с висящим bom_item_id
// упала бы внешним ключом и откатила ВСЮ оплаченную выдачу. Поэтому строка перечитывается в этой
// SERIALIZABLE-транзакции (чтение ставит блокировку, и до коммита её не удалят), и пропавшая либо
// чужая строка означает «плитка на полке, пара не перепривязана» с записью в журнал — ровно то, что
// обещает контракт DesignPatternParams.bom_item_id. Ошибка же самой базы — не промах, а поломка, и
// она уходит наверх, как у каждого оператора этой транзакции: дедлок повторяется всем замыканием.
//
// ⚠ СЕМЬЯ СТРОКИ ПЕРЕСУЖИВАЕТСЯ ЗДЕСЬ ЖЕ. Дверь судила род против семьи ДО денег, но между дверью и
// прилётом строку законно переносят в другую секцию. Снимок фурнитуры (hardware) на строке, ставшей
// рулонной, и плитка (pattern) на строке, ставшей не рулонной, — тот же промах, что пропавшая
// строка: ассет остаётся на полке ничьим, прежняя связка пары НЕ трогается и НЕ собирается (GC
// только при настоящей замене), запись в журнал. Правило то же, что у SetAssetBinding.
func bindKeptPatternTx(ctx context.Context, db dependency.DB, run entity.DesignRun, cw, bomItemID, assetID int, hardware bool) error {
	if bomItemID <= 0 {
		return nil
	}
	section, ok, err := bomLineSectionOfCard(ctx, db, run.TechCardId, bomItemID)
	if err != nil {
		return err
	}
	if !ok {
		slog.WarnContext(ctx, "design: pattern tile landed unbound — its BOM line is not this card's any more",
			slog.Int("run_id", run.Id), slog.Int("asset_id", assetID),
			slog.Int("tech_card_id", run.TechCardId), slog.Int("bom_item_id", bomItemID))
		return nil
	}
	if roll := entity.IsRollGoodsSection(section); hardware == roll {
		slog.WarnContext(ctx, "design: pattern tile landed unbound — its BOM line changed family",
			slog.Int("run_id", run.Id), slog.Int("asset_id", assetID),
			slog.Int("tech_card_id", run.TechCardId), slog.Int("bom_item_id", bomItemID),
			slog.String("section", string(section)), slog.Bool("hardware", hardware))
		return nil
	}
	prev, err := pairAssetTx(ctx, db, cw, bomItemID)
	if err != nil {
		return err
	}
	// ⚠ МЕТКИ ПЕРЕЕЗЖАЮТ ДО GC (B-m2). Перезаказ картинки на ту же пару — это та же вещь в новой
	// версии: метки на флэте (design_asset_placement) принадлежат ПАРЕ, а не байтам снимка, и без
	// переезда GC прежнего снимка уносил бы их каскадом. Переезжают только с hardware-ассета, который
	// носит ровно эта пара: метки снимка, служащего ещё и другой паре, — её, и остаются с ней.
	// Решение принимается ДО upsert: после него связка пары уже указывает на новый ассет.
	move := false
	if prev > 0 && prev != assetID && hardware {
		if move, err = soleHardwareOfPairTx(ctx, db, prev); err != nil {
			return err
		}
	}
	if err := upsertAssetBindingTx(ctx, db, run.TechCardId, cw, bomItemID, assetID, run.Author); err != nil {
		return err
	}
	if prev != assetID {
		if move {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE design_asset_placement SET asset_id = :new WHERE asset_id = :prev`,
				map[string]any{"new": assetID, "prev": prev}); err != nil {
				return fmt.Errorf("failed to move the flat marks of superseded asset %d onto asset %d: %w", prev, assetID, err)
			}
		}
		return dropSupersededHardwareTx(ctx, db, prev)
	}
	return nil
}

// soleHardwareOfPairTx — prev рода hardware и связан РОВНО с одной парой (той, которую сейчас
// заменяют). Зовётся ДО upsert, пока связка пары ещё указывает на prev.
func soleHardwareOfPairTx(ctx context.Context, db dependency.DB, prev int) (bool, error) {
	a, err := assetByID(ctx, db, prev)
	if err != nil {
		if errors.Is(err, entity.ErrDesignNotFound) {
			return false, nil
		}
		return false, err
	}
	if a.Kind != entity.DesignAssetKindHardware {
		return false, nil
	}
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM design_asset_binding WHERE asset_id = :id`,
		map[string]any{"id": prev})
	if err != nil {
		return false, fmt.Errorf("failed to count the bindings of asset %d: %w", prev, err)
	}
	return n == 1, nil
}

// landingFreesShelfRowTx — уберёт ли посадка на пару (cw, bomItemID) прежний ассет пары (B-m1).
// Повторяет решения bindKeptPatternTx до записи: строка BOM карточки на месте, семья совпадает,
// у пары есть прежний ассет — и тот собирается dropSupersededHardwareTx ПОСЛЕ переезда меток.
func landingFreesShelfRowTx(ctx context.Context, db dependency.DB, cardID, cw, bomItemID int, hardware bool) (bool, error) {
	section, ok, err := bomLineSectionOfCard(ctx, db, cardID, bomItemID)
	if err != nil || !ok {
		return false, err
	}
	if entity.IsRollGoodsSection(section) == hardware {
		return false, nil
	}
	prev, err := pairAssetTx(ctx, db, cw, bomItemID)
	if err != nil || prev <= 0 {
		return false, err
	}
	a, err := assetByID(ctx, db, prev)
	if err != nil {
		if errors.Is(err, entity.ErrDesignNotFound) {
			return false, nil
		}
		return false, err
	}
	refs, err := storeutil.QueryNamedOne[struct {
		Bindings   int `db:"bindings"`
		Placements int `db:"placements"`
		Derived    int `db:"derived"`
	}](ctx, db, `
		SELECT
			(SELECT COUNT(*) FROM design_asset_binding   WHERE asset_id = :id) AS bindings,
			(SELECT COUNT(*) FROM design_asset_placement WHERE asset_id = :id) AS placements,
			(SELECT COUNT(*) FROM design_asset           WHERE derived_from_asset_id = :id) AS derived`,
		map[string]any{"id": prev})
	if err != nil {
		return false, fmt.Errorf("failed to count the references of asset %d: %w", prev, err)
	}
	return supersededCollectable(a.Kind, a.ColorwayId.Valid, hardware, refs.Bindings, refs.Placements, refs.Derived), nil
}

// supersededCollectable — чистый предикат GC прежнего ассета пары при посадке, с учётом переезда
// меток (B-m2). bindings считает и связку заменяемой пары: после upsert её у prev не останется.
// Метки не держат prev, если переедут, — а переезжают они, когда prev hardware, связан только с
// этой парой и садится hardware; иначе любая метка — повод оставить.
func supersededCollectable(kind string, colorwayHeld, landingHardware bool, bindings, placements, derived int) bool {
	if kind != entity.DesignAssetKindHardware || colorwayHeld {
		return false
	}
	if bindings != 1 || derived > 0 {
		return false
	}
	movesPlacements := landingHardware
	return placements == 0 || movesPlacements
}

// pairAssetTx — какой ассет сейчас носит пара (колорвей, слот); 0 = пара пуста. Читается в
// транзакции записи ДО неё: после upsert прежнего номера уже не узнать.
func pairAssetTx(ctx context.Context, db dependency.DB, cw, bomItemID int) (int, error) {
	ids, err := storeutil.QueryScalarListNamed[int](ctx, db, `
		SELECT asset_id FROM design_asset_binding WHERE colorway_id = :cw AND bom_item_id = :bom`,
		map[string]any{"cw": cw, "bom": bomItemID})
	if err != nil {
		return 0, fmt.Errorf("failed to read the asset of colourway %d on BOM line %d: %w", cw, bomItemID, err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// dropSupersededHardwareTx — СНИМОК ФУРНИТУРЫ, КОТОРЫЙ ПЕРЕСТАЛ БЫТЬ КАРТИНКОЙ ХОТЬ ОДНОЙ ПАРЫ,
// УДАЛЯЕТСЯ в той же транзакции, что его заменила, — ТОЛЬКО при посадке прогона (перезаказ снимка).
// SetAssetBinding не собирает никогда: снятие и замена откатываются UNDO. Снимок фурнитуры делается ДЛЯ пары и
// вне пары не значит ничего — на полке он был бы мусором, который копится с каждым перезаказом.
//
// ⚠ ТОЛЬКО kind = hardware. Ткани и паттерны — библиотека, которой человек управляет сам: они
// переживают снятие с любой пары. И только сирота: ни связки, ни метки на флэте, ни носки колорвея
// (colorway_id), ни паттерна, сделанного из него (derived_from_asset_id). Любая из этих ссылок —
// повод оставить. Проверки и DELETE идут отдельными операторами: MySQL не даёт подзапросу DELETE
// читать ту же таблицу (1093), а SERIALIZABLE держит прочитанное до коммита.
func dropSupersededHardwareTx(ctx context.Context, db dependency.DB, assetID int) error {
	if assetID <= 0 {
		return nil
	}
	a, err := assetByID(ctx, db, assetID)
	if err != nil {
		if errors.Is(err, entity.ErrDesignNotFound) {
			return nil
		}
		return err
	}
	if a.Kind != entity.DesignAssetKindHardware || a.ColorwayId.Valid {
		return nil
	}
	refs, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT
			(SELECT COUNT(*) FROM design_asset_binding   WHERE asset_id = :id) +
			(SELECT COUNT(*) FROM design_asset_placement WHERE asset_id = :id) +
			(SELECT COUNT(*) FROM design_asset           WHERE derived_from_asset_id = :id)`,
		map[string]any{"id": assetID})
	if err != nil {
		return fmt.Errorf("failed to count the references of superseded hardware asset %d: %w", assetID, err)
	}
	if refs > 0 {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM design_asset WHERE id = :id AND kind = :kind`,
		map[string]any{"id": assetID, "kind": entity.DesignAssetKindHardware}); err != nil {
		return fmt.Errorf("failed to drop superseded hardware asset %d: %w", assetID, err)
	}
	slog.InfoContext(ctx, "design: superseded hardware asset dropped",
		slog.Int("tech_card_id", a.TechCardId), slog.Int("asset_id", assetID))
	return nil
}

// bomLineSectionOfCard — секция строки BOM этой карточки; ok=false, если строки у карточки нет.
func bomLineSectionOfCard(ctx context.Context, db dependency.DB, cardID, bomItemID int) (entity.TechCardBomSection, bool, error) {
	secs, err := storeutil.QueryScalarListNamed[string](ctx, db,
		`SELECT section FROM tech_card_bom_item WHERE id = :bom AND tech_card_id = :card`,
		map[string]any{"bom": bomItemID, "card": cardID})
	if err != nil {
		return "", false, fmt.Errorf("failed to read BOM line %d of tech card %d: %w", bomItemID, cardID, err)
	}
	if len(secs) == 0 {
		return "", false, nil
	}
	return entity.TechCardBomSection(secs[0]), true, nil
}

// Ширины colour_code / colour_hex из 0354. Посадка не отказывает — она пишет оплаченный результат,
// — поэтому значение, которое колонка не вмещает, НЕ пишется вовсе: строгий режим ответил бы 1406 и
// уронил прилёт, а обрезанный код или hex — это уже другой цвет, то есть ложь на полке.
const (
	designAssetColourCodeMax = 32
	designAssetColourHexMax  = 9
)

// keptColourFact — одна грань заявленного цвета, готовая лечь в колонку ассета: обрезанная по
// краям, пустая при отсутствии и пустая же (с записью в журнал) при переполнении колонки.
func keptColourFact(ctx context.Context, runID int, column, v string, maxRunes int) string {
	v = strings.TrimSpace(v)
	if n := len([]rune(v)); n > maxRunes {
		slog.WarnContext(ctx, "design: stated colour does not fit the asset column, left empty",
			slog.Int("run_id", runID), slog.String("column", column), slog.Int("runes", n))
		return ""
	}
	return v
}

// bomLineGone — нет ли строки BOM с этим id НИ У ОДНОЙ карточки. Нужна только снятию связки:
// «пропала» (снимать нечего, OK) и «чужая» (foreign_bom_line) bomLineSectionOfCard не различает.
func bomLineGone(ctx context.Context, db dependency.DB, bomItemID int) (bool, error) {
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM tech_card_bom_item WHERE id = :bom`, map[string]any{"bom": bomItemID})
	if err != nil {
		return false, fmt.Errorf("failed to read BOM line %d: %w", bomItemID, err)
	}
	return n == 0, nil
}

// assetBindingUpsert — ЕДИНСТВЕННЫЙ оператор записи связки, на обоих писателях.
//
// UPSERT ПО КЛЮЧУ ПАРЫ, И ЭТО И ЕСТЬ SINGLE-SELECT. uq_design_asset_binding (colorway_id,
// bom_item_id) делает «две ткани у одной пары» невыразимым, а ON DUPLICATE KEY UPDATE превращает
// повторный выбор в замену, а не в 1062. Чтения перед записью нет намеренно: «SELECT, строки нет,
// INSERT» — это гонка двух выборов на 1062, которую клиент откатить не умеет.
//
// tech_card_id В ВЕТКЕ ДУБЛИКАТА НЕ ПЕРЕПИСЫВАЕТСЯ: пара уже принадлежит ровно одной карточке
// (колорвей и строка BOM проверены на неё обоими писателями), переписывать нечего.
const assetBindingUpsert = `
	INSERT INTO design_asset_binding (tech_card_id, colorway_id, bom_item_id, asset_id, set_by, set_at)
	VALUES (:card, :cw, :bom, :asset, :who, CURRENT_TIMESTAMP)
	ON DUPLICATE KEY UPDATE
		asset_id = VALUES(asset_id),
		set_by   = VALUES(set_by),
		set_at   = CURRENT_TIMESTAMP`

func upsertAssetBindingTx(ctx context.Context, db dependency.DB, cardID, cw, bomItemID, assetID int, who string) error {
	if err := storeutil.ExecNamed(ctx, db, assetBindingUpsert, map[string]any{
		"card": cardID, "cw": cw, "bom": bomItemID, "asset": assetID, "who": who,
	}); err != nil {
		return fmt.Errorf("failed to bind asset %d to colourway %d on BOM line %d: %w",
			assetID, cw, bomItemID, err)
	}
	return nil
}

// SetAssetBinding says WHICH ASSET IS THE FABRIC OF ONE (COLOURWAY, SLOT) (0368); AssetId 0 takes
// the fabric off the pair, and unbinding a pair that wears nothing is a success that changes
// nothing — the state after the call is exactly the one asked for. The same holds for a pair whose
// BOM line has since been DELETED (its binding went with it by cascade): the unbind answers OK
// rather than foreign_bom_line. A line that still exists on ANOTHER card is refused either way.
//
// EVERY ONE OF THE THREE IDS IS CHECKED AGAINST THE CARD, in this transaction and in this order:
// the asset (NotFound for another card's; any kind binds — a hardware asset is the picture of a
// hardware slot), the colourway (foreign_colorway) and the BOM line (foreign_bom_line). None of
// the three is expressible in the schema: the four foreign keys are each satisfied by a row of ANY
// card. The asset's KIND is judged against the line's family on a bind: hardware only on a line
// that is not roll goods (hardware_on_cloth_line), fabric|pattern only on a roll-goods line
// (cloth_on_trim_line) — entity.IsRollGoodsSection is the one reading of «roll goods».
//
// IT NEVER DROPS AN ASSET, not on a rebind and not on an unbind: the client offers UNDO after a
// clear or a replace by re-binding the previous asset id, so that asset must survive. A superseded
// hardware picture is collected only when a run lands over it (bindKeptPatternTx).
//
// IT DOES NOT TOUCH design_asset.colorway_id: that is the legacy whole-colourway fabric and has its
// own verb (SetAssetColorway).
func (s *Store) SetAssetBinding(ctx context.Context, req entity.DesignAssetBindingSet) (*entity.DesignAssetBinding, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.ColorwayId <= 0 {
		return nil, fmt.Errorf("%w: a binding names the colourway it dresses", entity.ErrDesignInvalidArgument)
	}
	if req.BomItemId <= 0 {
		return nil, fmt.Errorf("%w: a binding names the BOM line (the slot) it dresses", entity.ErrDesignInvalidArgument)
	}
	if req.AssetId < 0 {
		return nil, fmt.Errorf("%w: asset_id must not be negative", entity.ErrDesignInvalidArgument)
	}
	var out *entity.DesignAssetBinding
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()
		var asset entity.DesignAsset
		if req.AssetId > 0 {
			// ФУРНИТУРА ТОЖЕ БИНДИТСЯ (fabrics and hardware bench): связка — это «картинка этой
			// пары», а пара фурнитуры — строка BOM пуговиц или молнии. Род ассета судится против
			// семьи строки ниже; legacy-колонку колорвея (SetAssetColorway) фурнитура по-прежнему
			// не носит.
			var err error
			if asset, err = requireAssetOfCard(ctx, db, req.TechCardId, req.AssetId); err != nil {
				return err
			}
		}
		if err := assertColorwayOfCard(ctx, db, req.TechCardId, req.ColorwayId); err != nil {
			return err
		}
		section, ok, err := bomLineSectionOfCard(ctx, db, req.TechCardId, req.BomItemId)
		if err != nil {
			return err
		}
		if ok && req.AssetId > 0 {
			// РОД ПРОТИВ СЕМЬИ: фурнитура — только на не-рулонную строку, ткань и паттерн — только
			// на рулонную. Тот же токен, что у денежной двери прогона паттерна.
			roll := entity.IsRollGoodsSection(section)
			if asset.Kind == entity.DesignAssetKindHardware && roll {
				return fmt.Errorf("%w: asset %d is hardware and BOM line %d is a %s line (roll goods)",
					entity.ErrDesignHardwareOnClothLine, req.AssetId, req.BomItemId, section)
			}
			if asset.Kind != entity.DesignAssetKindHardware && !roll {
				return fmt.Errorf("%w: asset %d is a %s and BOM line %d is a %s line, not roll goods",
					entity.ErrDesignClothOnTrimLine, req.AssetId, asset.Kind, req.BomItemId, section)
			}
		}
		if !ok {
			// СНЯТИЕ С ПРОПАВШЕЙ СТРОКИ — НЕ ОТКАЗ (ревью STEP 3). Строку BOM законно удаляют, пока
			// экран открыт, и её пары уходят каскадом: состояние, о котором просит снятие, уже
			// наступило, а foreign_bom_line сказал бы человеку «чужая строка» о строке, которой нет
			// ни у кого. Поэтому пропавшая строка на снятии — тот же DELETE ниже (ничего не
			// находит) и OK. Строка, которая ЕСТЬ, но у ДРУГОЙ карточки, отказывает и на снятии:
			// адрес чужой пары — ошибка клиента, а не устаревший экран.
			gone := false
			if req.AssetId == 0 {
				if gone, err = bomLineGone(ctx, db, req.BomItemId); err != nil {
					return err
				}
			}
			if !gone {
				return fmt.Errorf("%w: BOM line %d is not a line of tech card %d",
					entity.ErrDesignForeignBomLine, req.BomItemId, req.TechCardId)
			}
		}
		pair := map[string]any{"cw": req.ColorwayId, "bom": req.BomItemId, "card": req.TechCardId}
		// ⚠ БЕЗ СБОРКИ МУСОРА: снятый либо заменённый ассет остаётся на полке — клиент предлагает
		// UNDO, перепривязывая прежний id. GC только при посадке прогона (bindKeptPatternTx).
		if req.AssetId == 0 {
			if err := storeutil.ExecNamed(ctx, db, `
				DELETE FROM design_asset_binding
				WHERE colorway_id = :cw AND bom_item_id = :bom AND tech_card_id = :card`, pair); err != nil {
				return fmt.Errorf("failed to unbind colourway %d on BOM line %d: %w",
					req.ColorwayId, req.BomItemId, err)
			}
			return nil
		}
		if err := upsertAssetBindingTx(ctx, db, req.TechCardId, req.ColorwayId, req.BomItemId,
			req.AssetId, req.SetBy); err != nil {
			return err
		}
		saved, err := storeutil.QueryNamedOne[entity.DesignAssetBinding](ctx, db, `
			SELECT * FROM design_asset_binding
			WHERE colorway_id = :cw AND bom_item_id = :bom AND tech_card_id = :card`, pair)
		if err != nil {
			return fmt.Errorf("failed to read the binding of colourway %d on BOM line %d back: %w",
				req.ColorwayId, req.BomItemId, err)
		}
		out = &saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpsertAsset writes ONE shelf row — creating it when AssetId is 0, replacing it otherwise.
//
// ONE VERB FOR BOTH GESTURES, because the screen has one: a shelf tile is filled in and saved. A
// separate create and update would be a second place to forget the ordinal or the parentage.
//
// WHAT IS CHECKED WHERE. The seven rules that need nothing but the request are in
// entity.DesignAssetUpsert.Validate — they are words of the contract, and a rule that can only be
// exercised against a live database is a rule nobody exercises. The three that need a row are
// here, inside the transaction: the parent exists and is this card's, the media is not another
// card's, and the shelves have not hit their ceiling.
func (s *Store) UpsertAsset(ctx context.Context, req entity.DesignAssetUpsert) (*entity.DesignAsset, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.AssetId < 0 {
		return nil, fmt.Errorf("%w: asset id %d", entity.ErrDesignInvalidArgument, req.AssetId)
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	note := strings.TrimSpace(req.Note)

	var out *entity.DesignAsset
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		// THE SAME NEGATIVE BOUNDARY THE REST OF THE BAND USES — «not another card's», never «this
		// card's». A file freshly uploaded through the media door belongs to no card at all and is
		// a perfectly legal texture; a positive rule would refuse it and force a human to save the
		// whole card before naming a cloth. See refuseForeignMedia for the full argument.
		if req.MediaId != 0 {
			if err := refuseForeignMedia(ctx, db, req.TechCardId, req.MediaId); err != nil {
				return err
			}
		}
		// THE PARENT IS READ, NOT ASSUMED. The foreign key says «some design_asset row», never
		// «one of THIS card's» — so without this read a pattern could be hung off a cloth of a
		// different style and the schema would accept it silently.
		if req.DerivedFromAssetId != 0 {
			if _, err := requireAssetOfCard(ctx, db, req.TechCardId, req.DerivedFromAssetId); err != nil {
				return err
			}
		}

		id := req.AssetId
		params := map[string]any{
			"card":        req.TechCardId,
			"kind":        req.Kind,
			"name":        name,
			"media":       nullInt(req.MediaId),
			"colour_code": nullStr(strings.TrimSpace(req.ColourCode)),
			"colour_hex":  nullStr(strings.TrimSpace(req.ColourHex)),
			"note":        nullStr(note),
			"parent":      nullInt(req.DerivedFromAssetId),
			"repeat_mm":   req.RepeatMm,
			"rotation":    req.RotationDeg,
			"ord":         req.Ordinal,
			"who":         req.Actor,
		}

		if id == 0 {
			if err := refuseFullShelf(ctx, db, req.TechCardId, 0); err != nil {
				return err
			}
			newID, err := insertAssetTx(ctx, db, params)
			if err != nil {
				return err
			}
			id = newID
		} else {
			// THE ROW IS READ BEFORE IT IS WRITTEN, and the bare UPDATE below could not replace
			// this read. `WHERE id = :id AND tech_card_id = :card` affecting zero rows is
			// ambiguous — «no such asset», «somebody else's asset» and «you saved the tile
			// unchanged» all look identical — and answering the third with NotFound would tell a
			// person their shelf vanished for pressing Save twice.
			before, err := requireAssetOfCard(ctx, db, req.TechCardId, id)
			if err != nil {
				return err
			}
			// ─── UPSERT НЕ ЧЁРНЫЙ ХОД В «ФУРНИТУРУ С КОЛОРВЕЕМ» (N2) ───
			//
			// SetAssetColorway отказывает назначить колорвей фурнитуре; но Upsert меняет РОД,
			// сохраняя колонку, — и тот же запретный конец достигался с другой стороны: назначь
			// ткань X колорвею 5, потом сохрани X как hardware, и строка станет фурнитурой,
			// носящей колорвей. Состояние, которое выделенный глагол называет невыразимым, обязано
			// быть невыразимым ЧЕРЕЗ ВСЕ ДВЕРИ, иначе запрет — это не правило, а привычка одной
			// двери.
			//
			// ОТКАЗ, А НЕ ТИХОЕ СНЯТИЕ, и выбор здесь тот же, что у всей волны. Снять назначение
			// молча значит исполнить не то, о чём просили: человек редактировал ПОЛКУ, а сервер
			// заодно и без единого слова снял бы ткань с цвета — потерю, которую видно только
			// на другом экране и только потом. Отказ же чинится одним понятным шагом («сними
			// ткань с колорвея, потом меняй род»), и он называет оба факта.
			if req.Kind == entity.DesignAssetKindHardware &&
				before.Kind != entity.DesignAssetKindHardware &&
				entity.DesignColorwayOrNone(before.ColorwayId) > 0 {
				return fmt.Errorf("%w: asset %d is the fabric of colourway %d and cannot become %s; "+
					"take it off the colourway first",
					entity.ErrDesignColorwayForbidden, id,
					entity.DesignColorwayOrNone(before.ColorwayId), entity.DesignAssetKindHardware)
			}
			params["id"] = id
			// created_by / created_at ARE NOT IN THE SET LIST. Who put the cloth on the shelf is
			// not rewritten by whoever edits its colour later; the editor's name would then be the
			// only name the row carries, and the byline would lie about a row nobody created twice.
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_asset SET
					kind = :kind, name = :name, media_id = :media,
					colour_code = :colour_code, colour_hex = :colour_hex, note = :note,
					derived_from_asset_id = :parent, repeat_mm = :repeat_mm,
					rotation_deg = :rotation, ordinal = :ord,
					updated_at = UTC_TIMESTAMP(6)
				WHERE id = :id AND tech_card_id = :card`, params); err != nil {
				return fmt.Errorf("failed to update design asset %d: %w", id, err)
			}
		}

		saved, err := assetByID(ctx, db, id)
		if err != nil {
			return err
		}
		// The response carries the file, not only its id: the tile that comes back is the tile the
		// screen redraws, and a bare media_id would blank the swatch it just saved.
		//
		// ⚠ THE ROW GOES THROUGH A SLICE AND COMES BACK OUT OF IT. attachAssetMedia fills its
		// argument IN PLACE, so handing it a fresh one-element literal and then returning `saved`
		// would return the copy that was never touched — a silent «no file» on every save.
		one := []entity.DesignAsset{saved}
		if err := attachAssetMedia(ctx, rep, one); err != nil {
			return err
		}
		out = &one[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetAssetColorway is the ONLY writer of design_asset.colorway_id (0357): «the fabric of colourway
// N is this asset», and colorwayID 0 takes the assignment off.
//
// SINGLE-SELECT, AND THE STEAL IS PART OF THE SAME TRANSACTION. A colourway wears ONE fabric, so
// assigning X to N first clears N off every other asset of this card. Doing it in a second call
// would leave a window in which the card claims two fabrics for one colourway — and doing it with
// a UNIQUE key instead would refuse the click outright, which is wrong twice over: the key would
// not constrain the unassigned majority at all (MySQL treats NULL as distinct), and pressing the
// neighbouring chip IS the intent «the fabric of N is now this one», not an accident to refuse.
//
// KIND GUARD: hardware has no fabric role — a zip is not what a colourway is made of — so naming
// it is `colorway_forbidden`, refused rather than silently ignored. fabric AND pattern are both
// allowed: the owner's «colour OR pattern» is about CONTENT, and a fabric asset with a photograph
// is the material case of the same sentence.
//
// The card boundary is read in THIS transaction (requireAssetOfCard, assertColorwayOfCard) for the
// reason every guard in this package is: one read outside it is a TOCTOU with a nicer name.
func (s *Store) SetAssetColorway(ctx context.Context, req entity.DesignAssetColorwaySet) (*entity.DesignAsset, error) {
	if req.ColorwayId < 0 {
		return nil, fmt.Errorf("%w: colorway_id must not be negative", entity.ErrDesignInvalidArgument)
	}
	var out *entity.DesignAsset
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		asset, err := requireAssetOfCard(ctx, db, req.TechCardId, req.AssetId)
		if err != nil {
			return err
		}
		if req.ColorwayId > 0 {
			if asset.Kind == entity.DesignAssetKindHardware {
				return fmt.Errorf("%w: a %s asset cannot be the fabric of a colourway",
					entity.ErrDesignColorwayForbidden, asset.Kind)
			}
			if err := assertColorwayOfCard(ctx, db, req.TechCardId, req.ColorwayId); err != nil {
				return err
			}
			if err := stealColorwayTx(ctx, db, req.TechCardId, req.ColorwayId, req.AssetId); err != nil {
				return err
			}
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE design_asset SET colorway_id = :cw, updated_at = UTC_TIMESTAMP(6)
			WHERE id = :id AND tech_card_id = :card`,
			map[string]any{"cw": nullInt(req.ColorwayId), "id": req.AssetId, "card": req.TechCardId}); err != nil {
			return fmt.Errorf("failed to set the colourway of design asset %d: %w", req.AssetId, err)
		}

		saved, err := assetByID(ctx, db, req.AssetId)
		if err != nil {
			return err
		}
		// Файл едет вместе со строкой по тому же доводу, что у UpsertAsset: экран перерисовывает
		// вернувшуюся плитку, и голый media_id стёр бы свотч, который только что показывали.
		// attachAssetMedia заполняет аргумент НА МЕСТЕ — отсюда срез и возврат из него.
		one := []entity.DesignAsset{saved}
		if err := attachAssetMedia(ctx, rep, one); err != nil {
			return err
		}
		out = &one[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAsset removes ONE shelf row and reports how many marks on flats went with it. Every
// (colourway, slot) it was the fabric of (0368) goes with it by the same cascade; those are counted
// in the same transaction, before the delete, and logged — the wire answer carries marks only.
//
// THE COUNT IS TAKEN BEFORE THE DELETE, and it has to be: the marks go with the row by
// ON DELETE CASCADE, so after the statement there is nothing left to count. The number is not
// decoration — the screen states it before it asks and repeats it after, because a delete that
// silently erased eight markings is a delete nobody could have predicted from what they were
// looking at.
//
// A PATTERN BUILT FROM THIS ASSET SURVIVES, its parentage cleared by the FK's SET NULL. That is
// the schema's decision and it is right: a pattern with a picture and a repeat is a usable
// instruction to a factory after its swatch is gone.
//
// ⚠ THE CARD IS REQUIRED, NOT OPTIONAL. A delete that cascades is the last verb in this file that
// may be addressed by a bare id: see requireAssetOfCard.
func (s *Store) DeleteAsset(ctx context.Context, techCardID, assetID int) (int, error) {
	if err := requireCard(techCardID); err != nil {
		return 0, err
	}
	if assetID <= 0 {
		return 0, fmt.Errorf("%w: asset id is required", entity.ErrDesignInvalidArgument)
	}
	removed, unbound := 0, 0
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		removed, unbound = 0, 0
		db := rep.DB()
		asset, err := requireAssetOfCard(ctx, db, techCardID, assetID)
		if err != nil {
			return err
		}
		n, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM design_asset_placement WHERE asset_id = :asset`,
			map[string]any{"asset": asset.Id})
		if err != nil {
			return fmt.Errorf("failed to count the marks of design asset %d: %w", asset.Id, err)
		}
		// ПАРЫ (КОЛОРВЕЙ, СЛОТ), ЧЬЕЙ ТКАНЬЮ ОН БЫЛ (0368), уходят тем же каскадом и считаются тем же
		// способом — ДО оператора, после которого считать нечего.
		bound, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM design_asset_binding WHERE asset_id = :asset`,
			map[string]any{"asset": asset.Id})
		if err != nil {
			return fmt.Errorf("failed to count the slot bindings of design asset %d: %w", asset.Id, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM design_asset WHERE id = :id`, map[string]any{"id": asset.Id}); err != nil {
			return fmt.Errorf("failed to delete design asset %d: %w", asset.Id, err)
		}
		removed, unbound = n, bound
		return nil
	})
	if err != nil {
		return 0, err
	}
	if unbound > 0 {
		// На проводе у ответа одно число — метки (removed_placements), и контракт этой волны его не
		// расширял: клиент видит связки ассета в самой полосе (asset_bindings) и называет их ДО
		// вопроса. Здесь число остаётся в журнале — «слоты остались без ткани» не должно быть
		// событием, о котором сервер промолчал.
		slog.InfoContext(ctx, "design: asset deleted together with the slot fabrics it was",
			slog.Int("tech_card_id", techCardID), slog.Int("asset_id", assetID),
			slog.Int("removed_placements", removed), slog.Int("removed_bindings", unbound))
	}
	return removed, nil
}

// SetAssetPlacement puts ONE mark on ONE flat, or moves an existing one.
//
// BOTH ENDS ARE CHECKED AGAINST THE SAME CARD, in this transaction, and neither check is
// expressible in the schema: design_asset_placement deliberately carries no tech_card_id (a second
// home for one fact drifts from the first at the first move), so the two foreign keys can each be
// satisfied by a row of a DIFFERENT style and the database would see nothing wrong.
func (s *Store) SetAssetPlacement(ctx context.Context, req entity.DesignAssetPlacementSet) (*entity.DesignAssetPlacement, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.PlacementId < 0 {
		return nil, fmt.Errorf("%w: placement id %d", entity.ErrDesignInvalidArgument, req.PlacementId)
	}
	if req.AssetId <= 0 {
		return nil, fmt.Errorf("%w: a placement names the asset it places", entity.ErrDesignInvalidArgument)
	}
	if req.PictureId <= 0 {
		return nil, fmt.Errorf("%w: a placement names the flat it is drawn on", entity.ErrDesignInvalidArgument)
	}
	// AN EMPTY ANNOTATION IS REFUSED RATHER THAN STORED. The column is NOT NULL and the row means
	// «this asset is HERE»; a mark with no geometry is a row that says «here» about nowhere, and
	// the screen would draw nothing while the shelf claimed the flat was marked. JSON `null` is
	// the same emptiness spelled a second way, so it is refused with it.
	ann := bytes.TrimSpace(req.Annotation)
	if len(ann) == 0 || string(ann) == "null" {
		return nil, fmt.Errorf("%w: a placement is a mark on a drawing and needs its geometry",
			entity.ErrDesignInvalidArgument)
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > entity.MaxDesignAssetNoteRunes {
		return nil, fmt.Errorf("%w: a placement note is at most %d characters",
			entity.ErrDesignInvalidArgument, entity.MaxDesignAssetNoteRunes)
	}

	var out *entity.DesignAssetPlacement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		asset, err := requireAssetOfCard(ctx, db, req.TechCardId, req.AssetId)
		if err != nil {
			return err
		}
		pic, err := pictureByID(ctx, db, req.PictureId)
		if err != nil {
			return err
		}
		// BOTH HALVES OF «MAY THIS PICTURE CARRY A MARK» — the card AND the kind — are asked of
		// entity.DesignAssetPlacementSet.RefusePicture, and the refusals it raises are the SAME
		// ones the bench raises for the same two facts (foreign_card_plate, wrong_kind). One fact
		// must not grow two machine tokens; the client already knows how to act on both.
		//
		// ⚠ THE RULE LIVES IN entity BECAUSE ITS KIND HALF IS TESTABLE THERE WITHOUT A DATABASE,
		// and the half that lived here alone is exactly the half that was complete: the card was
		// checked, the kind was not checked anywhere at all, and a mark on a render came back
		// from the band calling itself a mark on a flat.
		if err := req.RefusePicture(pic); err != nil {
			return err
		}

		params := map[string]any{
			"asset": asset.Id,
			"pic":   pic.Id,
			"ann":   []byte(ann),
			"note":  nullStr(note),
			"who":   req.Actor,
		}
		id := req.PlacementId
		if id == 0 {
			newID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_asset_placement
					(asset_id, picture_id, annotation, note, set_by, set_at)
				VALUES (:asset, :pic, :ann, :note, :who, UTC_TIMESTAMP(6))`, params)
			if err != nil {
				return fmt.Errorf("failed to place design asset %d: %w", asset.Id, err)
			}
			id = newID
		} else {
			if _, err := placementOfCard(ctx, db, req.TechCardId, id); err != nil {
				return err
			}
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_asset_placement SET
					asset_id = :asset, picture_id = :pic, annotation = :ann, note = :note,
					set_by = :who, set_at = UTC_TIMESTAMP(6)
				WHERE id = :id`, params); err != nil {
				return fmt.Errorf("failed to move design asset placement %d: %w", id, err)
			}
		}
		saved, err := placementByID(ctx, db, id)
		if err != nil {
			return err
		}
		out = &saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAssetPlacement takes ONE mark off a flat. The asset stays on its shelf: unmarking and
// removing are different acts, exactly as emptying a bench slot is not deleting the plate.
func (s *Store) DeleteAssetPlacement(ctx context.Context, techCardID, placementID int) error {
	if err := requireCard(techCardID); err != nil {
		return err
	}
	if placementID <= 0 {
		return fmt.Errorf("%w: placement id is required", entity.ErrDesignInvalidArgument)
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		pl, err := placementOfCard(ctx, db, techCardID, placementID)
		if err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM design_asset_placement WHERE id = :id`,
			map[string]any{"id": pl.Id}); err != nil {
			return fmt.Errorf("failed to delete design asset placement %d: %w", pl.Id, err)
		}
		return nil
	})
}

// placementByID reads one mark inside the caller's transaction.
func placementByID(ctx context.Context, db dependency.DB, id int) (entity.DesignAssetPlacement, error) {
	p, err := storeutil.QueryNamedOne[entity.DesignAssetPlacement](ctx, db,
		`SELECT * FROM design_asset_placement WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("%w: design asset placement %d", entity.ErrDesignNotFound, id)
		}
		return p, fmt.Errorf("failed to read design asset placement %d: %w", id, err)
	}
	return p, nil
}

// placementOfCard reads one mark THROUGH its asset, which is the only way to scope it by card —
// design_asset_placement carries no tech_card_id by design (0354).
//
// ⚠ AND THEREFORE THERE IS NO UNSCOPED BRANCH HERE EITHER. It used to fall back to placementByID
// on cardID <= 0, which did not merely skip a comparison — it skipped THE JOIN, and the join is the
// scope. See requireAssetOfCard for why the delete verbs no longer ask for that.
func placementOfCard(ctx context.Context, db dependency.DB, cardID, id int) (entity.DesignAssetPlacement, error) {
	if err := requireCard(cardID); err != nil {
		return entity.DesignAssetPlacement{}, err
	}
	rows, err := storeutil.QueryListNamed[entity.DesignAssetPlacement](ctx, db, `
		SELECT p.* FROM design_asset_placement p
		JOIN design_asset a ON a.id = p.asset_id
		WHERE p.id = :id AND a.tech_card_id = :card`,
		map[string]any{"id": id, "card": cardID})
	if err != nil {
		return entity.DesignAssetPlacement{}, fmt.Errorf("failed to read design asset placement %d: %w", id, err)
	}
	if len(rows) == 0 {
		return entity.DesignAssetPlacement{},
			fmt.Errorf("%w: design asset placement %d on tech card %d", entity.ErrDesignNotFound, id, cardID)
	}
	return rows[0], nil
}

// listAssets reads the whole shelf wall of a card, ordered the way the wall is drawn: shelf by
// shelf, then by the position a person gave the tile, then by birth order so that two tiles left
// at ordinal 0 keep a stable sequence instead of swapping on every read. The ordering matches
// idx_design_asset_card (tech_card_id, kind, ordinal, id) column for column.
func listAssets(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignAsset, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignAsset](ctx, db, `
		SELECT * FROM design_asset WHERE tech_card_id = :card ORDER BY kind, ordinal, id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design assets: %w", err)
	}
	return rows, nil
}

// listAssetPlacements reads every mark those assets left on this card's flats.
//
// ⚠ THE JOIN IS THE SCOPE, not an ornament: design_asset_placement has no tech_card_id at all
// (0354 says why — a second home for one fact diverges from the first), so «this card's marks» is
// reachable only through the asset. Drop the join and the band of every card serves the marks of
// every other.
func listAssetPlacements(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignAssetPlacement, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignAssetPlacement](ctx, db, `
		SELECT p.* FROM design_asset_placement p
		JOIN design_asset a ON a.id = p.asset_id
		WHERE a.tech_card_id = :card
		ORDER BY p.picture_id, p.id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design asset placements: %w", err)
	}
	return rows, nil
}

// loadPlacementPictures reads the pictures the marks sit on, by id, in ONE query, with their media
// resolved through the same funnel every outgoing picture takes (resolveMedia). Placements are few
// (bounded by the shelves), so this is a short IN list. A picture row that is gone simply has no key.
func loadPlacementPictures(ctx context.Context, rep dependency.Repository, pls []entity.DesignAssetPlacement) (map[int]entity.DesignPicture, error) {
	out := map[int]entity.DesignPicture{}
	ids := make([]int, 0, len(pls))
	seen := map[int]struct{}{}
	for _, p := range pls {
		if p.PictureId == 0 {
			continue
		}
		if _, ok := seen[p.PictureId]; ok {
			continue
		}
		seen[p.PictureId] = struct{}{}
		ids = append(ids, p.PictureId)
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, rep.DB(), `
		SELECT * FROM design_picture WHERE id IN (:ids)`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to load the pictures of design asset placements: %w", err)
	}
	ptrs := make([]*entity.DesignPicture, 0, len(rows))
	for i := range rows {
		ptrs = append(ptrs, &rows[i])
	}
	if err := resolveMedia(ctx, rep, ptrs); err != nil {
		return nil, err
	}
	for _, p := range rows {
		out[p.Id] = p
	}
	return out, nil
}

// listAssetBindings reads the fabric of every (colourway, slot) of this card (0368), never nil: an
// empty card answers [] so the wire can say «nothing bound yet» rather than «this binary does not
// know bindings». Ordered by colourway, then slot — the order the pattern step draws them in.
func listAssetBindings(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignAssetBinding, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignAssetBinding](ctx, db, `
		SELECT * FROM design_asset_binding WHERE tech_card_id = :card ORDER BY colorway_id, bom_item_id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design asset bindings: %w", err)
	}
	if rows == nil {
		rows = []entity.DesignAssetBinding{}
	}
	return rows, nil
}

// attachAssetMedia resolves the file of every asset in ONE batch read, inside the caller's
// transaction. A missing media row leaves Media nil rather than dropping the asset: «the file
// disappeared» is a fact the shelf must be able to show, exactly as it is for a picture.
func attachAssetMedia(ctx context.Context, rep dependency.Repository, assets []entity.DesignAsset) error {
	ids := make([]int, 0, len(assets))
	for _, a := range assets {
		if a.MediaId.Valid && a.MediaId.Int32 > 0 {
			ids = append(ids, int(a.MediaId.Int32))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byID, err := resolveMediaIDs(ctx, rep, ids)
	if err != nil {
		return fmt.Errorf("failed to resolve design asset media: %w", err)
	}
	for i := range assets {
		if !assets[i].MediaId.Valid {
			continue
		}
		if m, ok := byID[int(assets[i].MediaId.Int32)]; ok {
			mm := m
			assets[i].Media = &mm
		}
	}
	return nil
}
