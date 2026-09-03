package design

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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
// fortieth and forty-first cloth at the same moment both see 39.
//
// ⚠ ОТДЕЛЬНОЙ ФУНКЦИЕЙ, ПОТОМУ ЧТО ПИСАТЕЛЕЙ ПОЛКИ СТАЛО ДВА. Второй — посадка плитки при закрытии
// прогона паттерна (keepPatternTx, queue.go), и он приходит сюда через минуты после того, как
// дверь уже спросила то же самое у полосы. Одна проверка без другой была бы либо TOCTOU, либо
// платой за заведомо невозможную посадку; два написания одного счёта разошлись бы молча.
func refuseFullShelf(ctx context.Context, db dependency.DB, cardID int) error {
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM design_asset WHERE tech_card_id = :card`,
		map[string]any{"card": cardID})
	if err != nil {
		return fmt.Errorf("failed to count design assets: %w", err)
	}
	if n >= entity.MaxDesignAssetsPerCard {
		return fmt.Errorf("%w: tech card %d already holds %d shelf rows, the ceiling is %d",
			entity.ErrDesignAssetTooMany, cardID, n, entity.MaxDesignAssetsPerCard)
	}
	return nil
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
// ⚠ ВТОРОЙ ЗВАТЕЛЬ — ПОСАДКА ПЛИТКИ (keepPatternTx), И ТАМ КРАЖА ОБЯЗАНА ИДТИ ДО ВСТАВКИ.
// uq_design_asset_colorway (tech_card_id, colorway_id) — настоящий UNIQUE: вставить нового
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

	if err := refuseFullShelf(ctx, db, run.TechCardId); err != nil {
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

	// КОЛОРВЕЙ БЕРЁТСЯ ИЗ ЖИВОЙ КОЛОНКИ, А НЕ ИЗ ЗАМОРОЖЕННЫХ params, И РАЗНИЦА СОДЕРЖАТЕЛЬНАЯ:
	// колорвей законно удаляют между стартом и прилётом, FK гасит колонку в NULL, и посадка на
	// несуществующий id упала бы внешним ключом. Ноль здесь значит «плитка встаёт на полку ничьей»,
	// ровно то же, что случилось со строкой прогона.
	cw := entity.DesignColorwayOrNone(run.ColorwayId)
	if cw > 0 {
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
	if src := p.Pattern.SourceAssetID; src > 0 {
		switch a, err := assetByID(ctx, db, src); {
		case err != nil && !errors.Is(err, entity.ErrDesignNotFound):
			return err
		case err == nil && a.TechCardId == run.TechCardId &&
			(a.Kind == entity.DesignAssetKindFabric || a.Kind == entity.DesignAssetKindPattern):
			parent = src
		}
	}

	id, err := insertAssetTx(ctx, db, map[string]any{
		"card":        run.TechCardId,
		"kind":        entity.DesignAssetKindPattern,
		"name":        name,
		"media":       nullInt(mediaID),
		"colour_code": nil,
		"colour_hex":  nil,
		"note":        nil,
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
			if err := refuseFullShelf(ctx, db, req.TechCardId); err != nil {
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

// DeleteAsset removes ONE shelf row and reports how many marks on flats went with it.
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
	removed := 0
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		removed = 0
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
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM design_asset WHERE id = :id`, map[string]any{"id": asset.Id}); err != nil {
			return fmt.Errorf("failed to delete design asset %d: %w", asset.Id, err)
		}
		removed = n
		return nil
	})
	if err != nil {
		return 0, err
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
