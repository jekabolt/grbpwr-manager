package design

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetEditLayer reads ONE layer WITH its strokes. It exists as its own method because GetBand
// deliberately lists layers without them: 512 KB is the cap per LAYER, a card may hold several,
// and shipping them all would make opening the tab cost megabytes to draw a list of thumbnails.
func (s *Store) GetEditLayer(ctx context.Context, cardID, layerID int) (*entity.DesignEditLayer, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	var out entity.DesignEditLayer
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		l, err := layerByID(ctx, rep.DB(), layerID)
		if err != nil {
			return err
		}
		if l.TechCardId != cardID {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, layerID, l.TechCardId)
		}
		out = l
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SaveEditLayer stores a layer under compare-and-set on its rev. layer_id = 0 creates one;
// with base_media_id = 0 that is the clean vector base of the «draw it» door, and a card may hold
// several of those — uq_design_edit_layer_base tolerates repeated NULLs.
//
// CAS IS NOT MADE REDUNDANT BY SERIALIZABLE. The isolation level orders two writers; it cannot
// tell that the second one was looking at r3 while r4 already existed.
//
// ⚠ IT WRITES BOTH CHANNELS OF THE LAYER (0355) UNDER ONE COMPARE-AND-SET. Since the layer grew a
// pixel channel, `strokes` and `raster_media_id` move together under the same `rev = :expected`
// predicate. That is X-9's «the revision must not drift between the two channels»: losing a
// person's brushwork to a stale writer is exactly as expensive as losing their strokes.
//
// ⚠ WHAT ACTUALLY CARRIES THAT GUARANTEE IS THE TRANSACTION, NOT THE COUNT OF STATEMENTS, and the
// difference was MEASURED rather than assumed. A raster written by a SECOND statement inside this
// same closure is redundant but harmless: a CAS miss returns an error, the closure rolls back, and
// the stray write goes with it — the probe stays green under exactly that mutation, correctly. The
// hazard is a raster written under a SECOND VERB (its own transaction, no expected_rev), and there
// the probe goes red: the stale writer's pixels commit, the strokes are refused, and the person
// whose painting was overwritten learns it from the picture rather than from a refusal.
//
// So the rule this function keeps is: THE PIXELS NEVER MOVE OUTSIDE THIS TRANSACTION, and every
// refusal inside it is an error rather than a quiet `return nil`. A second rev column would break
// it the same way a second verb does.
func (s *Store) SaveEditLayer(ctx context.Context, req entity.DesignEditLayerSave) (*entity.DesignEditLayer, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if len(req.Strokes) > MaxStrokesBytes {
		return nil, fmt.Errorf("%w: %d bytes of strokes, the ceiling is %d",
			entity.ErrDesignStrokesTooLarge, len(req.Strokes), MaxStrokesBytes)
	}
	// NAMING A RASTER AND ASKING TO CLEAR ONE IS A CONTRADICTION, not a precedence puzzle. Picking
	// a winner would make one half of the request silently unread, and the half that loses is the
	// half somebody's editor believed it had sent.
	if req.RasterMediaId > 0 && req.ClearRaster {
		return nil, fmt.Errorf("%w: a save cannot both set raster media %d and clear the raster",
			entity.ErrDesignInvalidArgument, req.RasterMediaId)
	}
	var out entity.DesignEditLayer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		// ─── ГРАНИЦА КАРТОЧКИ ДЛЯ ПИКСЕЛЬНОГО КАНАЛА ───
		//
		// ⚠ ГЕЙТ ОТВЕЧАЕТ ОШИБКОЙ, А НЕ `return nil`, И ЭТО НЕ МЕЛОЧЬ. Ранний выход из этого
		// замыкания оставил бы `out` нулевым, а вызывающий вернул бы «слой» с нулевым id как
		// успех — тот самый дефект, который вчера нашёлся в соседней функции пакета.
		//
		// ПОЧЕМУ ПРОВЕРКА ВООБЩЕ НУЖНА. Растр отдаётся клиенту как ссылка и рисуется им на
		// холсте: непроверенное поле означает, что слой карточки A показывает и сплющивает
		// картинку карточки B — дословно та же беда, которую ImportVector закрыл для
		// source_media_id и base_media_id. Правило ОТРИЦАТЕЛЬНОЕ («не принадлежит чужой
		// карточке»), поэтому свежезагруженный ничейный PNG — обычный случай — проходит одним
		// чтением; см. шапку refuseForeignMedia.
		//
		// СУЩЕСТВОВАНИЕ СПРАШИВАЕТСЯ ЗДЕСЬ, А НЕ У ВНЕШНЕГО КЛЮЧА: 1452 назвал бы человеку имя
		// ограничения, а этот отказ называет id, который он прислал.
		if req.RasterMediaId > 0 {
			media, err := resolveMediaIDs(ctx, rep, []int{req.RasterMediaId})
			if err != nil {
				return fmt.Errorf("failed to resolve the raster media of the layer: %w", err)
			}
			if _, ok := media[req.RasterMediaId]; !ok {
				return fmt.Errorf("%w: media %d does not exist",
					entity.ErrDesignInvalidArgument, req.RasterMediaId)
			}
			if err := refuseForeignMedia(ctx, db, req.TechCardId, req.RasterMediaId); err != nil {
				return err
			}
		}

		if req.LayerId == 0 {
			if req.ExpectedRev != 0 {
				return fmt.Errorf("%w: a layer that does not exist yet is at rev 0",
					entity.ErrDesignLayerRevMismatch)
			}
			id, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_edit_layer
					(tech_card_id, base_media_id, rev, strokes, raster_media_id, updated_by)
				VALUES (:card, :base, 1, :strokes, :raster, :who)`,
				map[string]any{
					"card": req.TechCardId, "base": nullInt(req.BaseMediaId),
					"strokes": jsonOrNil(req.Strokes), "raster": nullInt(req.RasterMediaId),
					"who": req.Actor,
				})
			if err != nil {
				// uq_design_edit_layer_base means a layer over THIS base already exists on this
				// card. That is not a duplicate to swallow: the caller believed it was creating
				// one, so the honest answer is the CAS refusal that makes it reload and continue
				// the existing layer.
				if isDupKey(err) {
					return fmt.Errorf("%w: a layer over this base already exists",
						entity.ErrDesignLayerRevMismatch)
				}
				return fmt.Errorf("failed to create design edit layer: %w", err)
			}
			out, err = layerByID(ctx, db, id)
			return err
		}

		before, err := layerByID(ctx, db, req.LayerId)
		if err != nil {
			return err
		}
		if before.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, req.LayerId, before.TechCardId)
		}
		// THE RASTER JOINS THE SET LIST ONLY WHEN THE REQUEST SAID SOMETHING ABOUT IT, and that is
		// how «silence keeps» is implemented — by never writing the column, rather than by reading
		// the old value and writing it back. A read-modify-write would be correct under this
		// transaction too, but it would put a second reader of the same fact into the function and
		// make the guarantee depend on that reader staying in step.
		rasterStated := req.RasterMediaId > 0 || req.ClearRaster
		args := map[string]any{
			"strokes": jsonOrNil(req.Strokes), "who": req.Actor,
			"id": req.LayerId, "expected": req.ExpectedRev,
		}
		if rasterStated {
			args["raster"] = nullInt(req.RasterMediaId)
		}
		n, err := storeutil.ExecNamedRows(ctx, db, layerSaveUpdate(rasterStated), args)
		if err != nil {
			return fmt.Errorf("failed to save design edit layer %d: %w", req.LayerId, err)
		}
		if n == 0 {
			after, rerr := layerByID(ctx, db, req.LayerId)
			if rerr != nil {
				return rerr
			}
			return fmt.Errorf("%w: layer is at rev %d, %d was echoed",
				entity.ErrDesignLayerRevMismatch, after.Rev, req.ExpectedRev)
		}
		out, err = layerByID(ctx, db, req.LayerId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ImportVector files an ALREADY-UPLOADED vector file into the band as an edit layer: the media row
// keeps the authoritative SVG, the layer keeps the editable projection of it, and
// design_edit_layer.source_media_id is the edge between them.
//
// IT SPENDS NOTHING, AND THAT IS THE LINE BETWEEN IT AND GENERATION. Vectorising BY MACHINE is a
// paid provider call and goes through StartRun with kind = vector; this files a file that already
// exists. Two doors for the money would be two budget checks, and one of them would eventually be
// the one nobody updated.
//
// THE CLIENT PARSES, THE STORE RECORDS THE PROVENANCE — the same division of labour
// FlattenEditLayer already draws, and for the same reason: there is no SVG parser and no vector
// renderer anywhere in this repository, so the only honest producer of strokes is the canvas that
// is about to draw them. `strokes` may legitimately be empty and then means «file the file»: the
// layer holds the vector without an editable form of it yet.
//
// ⚠ IDEMPOTENCY IS BY client_request_id — THE KEY THE CONTRACT DECLARES — AND THE FILE IS A SECOND
// BELT ON THE SAME PROMISE.
//
// This comment used to say the opposite, and it argued from the table: design_edit_layer had no
// request-id column, so the pair (tech_card_id, source_media_id) was made the key instead. That was
// a rationale outliving its reason the moment 0351 added the column — and while it stood, the verb
// answered backwards to what it promised: the SAME request naming a DIFFERENT file filed a second
// layer, while the field the contract calls the key was carried all the way here and never read.
//
// The two readings are ONE rule with two ways of recognising a retry, not two rules — both return
// the existing layer, and only the mismatch is new:
//
//   - the request id catches the ordinary retry, and a request id that turns up naming a different
//     card or a different file is REFUSED rather than silently handed somebody else's layer;
//   - the file still catches a client that reissued its request id, because the promise the
//     contract writes down is about the FILE: «a retry after a lost response must not file the same
//     SVG as a second layer».
//
// Both re-reads run INSIDE the SERIALIZABLE transaction, where an ordinary SELECT already blocks,
// so two concurrent retries cannot both insert; the guarantee is a lock, not a hope, and the unique
// index behind it is the belt for anything that reaches the table without passing through here.
func (s *Store) ImportVector(ctx context.Context, req entity.DesignVectorImport) (*entity.DesignEditLayer, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.SourceMediaId <= 0 {
		return nil, fmt.Errorf("%w: an import needs the vector file it is filing",
			entity.ErrDesignInvalidArgument)
	}
	// `drawn` IS REFUSED, and it is not pedantry: a layer drawn from nothing is born by
	// SaveEditLayer and has no file to import at all. Accepting it would file a layer claiming a
	// provenance its own source_media_id contradicts.
	if !entity.IsDesignImportableLayerOrigin(req.Origin) {
		return nil, fmt.Errorf("%w: origin %q is not imported | vectorised",
			entity.ErrDesignInvalidArgument, req.Origin)
	}
	if len(req.Strokes) > MaxStrokesBytes {
		return nil, fmt.Errorf("%w: %d bytes of strokes, the ceiling is %d",
			entity.ErrDesignStrokesTooLarge, len(req.Strokes), MaxStrokesBytes)
	}
	var out entity.DesignEditLayer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		// ─── 1. ИДЕМПОТЕНТНОСТЬ ПО ТОМУ КЛЮЧУ, КОТОРЫЙ ОБЪЯВЛЕН КОНТРАКТОМ ───
		//
		// `client_request_id` — вот он, и до 0351 колонки под него не было вовсе: поле требовалось
		// хендлером, доезжало сюда в запросной структуре и НЕ ЧИТАЛОСЬ. Дедупликация шла по паре
		// (карточка, файл), то есть по другому правилу, расходящемуся с обещанным в обе стороны.
		//
		// Ответ на повтор — СУЩЕСТВУЮЩИЙ слой, нетронутым: второй вызов это ретрай первого, а
		// ретрай, переписавший штрихи, выбросил бы всю правку, случившуюся между ними.
		if prior, ok, err := layerByRequestID(ctx, db, req.ClientRequestId); err != nil {
			return err
		} else if ok {
			// ⚠ ТОТ ЖЕ ЗАПРОС, НАЗЫВАЮЩИЙ ДРУГОЕ, — ЭТО ОШИБКА, А НЕ ПОВТОР, и молчаливо вернуть
			// ему чужой ответ значит соврать дважды: клиент считает, что подшил ЭТОТ файл, а
			// подшит другой. Тот же приём и та же формулировка, что у StartRun с чужой карточкой.
			if prior.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: client_request_id %q already filed a layer on tech card %d",
					entity.ErrDesignInvalidArgument, req.ClientRequestId, prior.TechCardId)
			}
			if int(prior.SourceMediaId.Int32) != req.SourceMediaId {
				return fmt.Errorf("%w: client_request_id %q already filed media %d, not %d",
					entity.ErrDesignInvalidArgument, req.ClientRequestId,
					prior.SourceMediaId.Int32, req.SourceMediaId)
			}
			out = prior
			return nil
		}

		// ─── 2. ТОТ ЖЕ ФАЙЛ, ПОТЕРЯННЫЙ ЗАПРОС — ВТОРОЙ ПОЯС, А НЕ ВТОРОЕ ПРАВИЛО ───
		//
		// Контракт формулирует обещание про ФАЙЛ дословно: «a retry after a lost response must not
		// file the same SVG as a second layer». Клиент, перевыпустивший идентификатор запроса
		// (а таких клиентов пишут), прошёл бы проверку выше и подшил бы тот же SVG вторым слоем —
		// то есть ровно то, что обещание запрещает. Оба чтения дают ОДИН ответ (существующий
		// слой) и разойтись не могут: они отличаются только тем, по чему узнают повтор.
		existing, err := storeutil.QueryListNamed[entity.DesignEditLayer](ctx, db, `
			SELECT * FROM design_edit_layer
			WHERE tech_card_id = :card AND source_media_id = :src ORDER BY id LIMIT 1`,
			map[string]any{"card": req.TechCardId, "src": req.SourceMediaId})
		if err != nil {
			return fmt.Errorf("failed to look for an already-imported vector: %w", err)
		}
		if len(existing) > 0 {
			out = existing[0]
			return nil
		}

		// ─── the file must exist, and the raster must belong to THIS card ───
		//
		// The FK would catch a missing media with a raw 1452 naming a constraint; caught here it
		// is an InvalidArgument that names the id the caller sent. The picture check the schema
		// cannot make at all: design_picture(id) is a valid target no matter whose card it is on,
		// and a lineage pointing at somebody else's flat is a lie the band would then draw.
		media, err := resolveMediaIDs(ctx, rep, []int{req.SourceMediaId, req.BaseMediaId})
		if err != nil {
			return fmt.Errorf("failed to resolve the vector source media: %w", err)
		}
		if _, ok := media[req.SourceMediaId]; !ok {
			return fmt.Errorf("%w: media %d does not exist",
				entity.ErrDesignInvalidArgument, req.SourceMediaId)
		}
		if req.BaseMediaId > 0 {
			if _, ok := media[req.BaseMediaId]; !ok {
				return fmt.Errorf("%w: base media %d does not exist",
					entity.ErrDesignInvalidArgument, req.BaseMediaId)
			}
		}
		if req.SourcePictureId > 0 {
			pic, err := pictureByID(ctx, db, req.SourcePictureId)
			if err != nil {
				return err
			}
			if pic.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: picture %d belongs to tech card %d",
					entity.ErrDesignNotFound, req.SourcePictureId, pic.TechCardId)
			}
		}
		// ⚠ И ТА ЖЕ ПРОВЕРКА ДЛЯ ДВУХ МЕДИА, КОТОРЫЕ ЕЁ НЕ ИМЕЛИ ВОВСЕ. Строкой выше картинка
		// проверяется на принадлежность карточке с 0350; файл и подложка не проверялись ничем,
		// кроме существования строки media, — то есть картинка ЧУЖОЙ карточки становилась
		// векторным слоем этой, и полоса рисовала её как свою, а «скачать SVG» отдавало чужой файл.
		if err := refuseForeignMedia(ctx, db, req.TechCardId, req.SourceMediaId, req.BaseMediaId); err != nil {
			return err
		}

		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_edit_layer
				(tech_card_id, base_media_id, rev, strokes, origin, source_media_id,
				 source_picture_id, client_request_id, updated_by)
			VALUES (:card, :base, 1, :strokes, :origin, :src, :pic, :req, :who)`,
			map[string]any{
				"card": req.TechCardId, "base": nullInt(req.BaseMediaId),
				"strokes": jsonOrNil(req.Strokes), "origin": req.Origin,
				"src": req.SourceMediaId, "pic": nullInt(req.SourcePictureId),
				"req": nullStr(req.ClientRequestId), "who": req.Actor,
			})
		if err != nil {
			if isDupKey(err) {
				// ДВА РАЗНЫХ УНИКАЛЬНЫХ КЛЮЧА, И ОТВЕТЫ У НИХ РАЗНЫЕ.
				//
				// uq_design_edit_layer_client_request (0351) — это ГОНКА ДВУХ ПОВТОРОВ одного
				// запроса: чтение выше их обоих пропустило, вставку выиграл один. Проигравшему
				// причитается ТОТ ЖЕ ОТВЕТ, что и победителю, — иначе идемпотентность держалась бы
				// на том, кто успел первым. Остаточный 1062 здесь пояс, а не механизм.
				if prior, ok, rerr := layerByRequestID(ctx, db, req.ClientRequestId); rerr != nil {
					return rerr
				} else if ok {
					out = prior
					return nil
				}
				// uq_design_edit_layer_base: a layer over THIS base already exists on this card.
				// Same answer as SaveEditLayer gives, and for the same reason — the caller believed
				// it was creating one, so the honest reply is the CAS refusal that makes it reload
				// and continue the layer that is already there.
				return fmt.Errorf("%w: a layer over this base already exists",
					entity.ErrDesignLayerRevMismatch)
			}
			return fmt.Errorf("failed to import the design vector: %w", err)
		}
		out, err = layerByID(ctx, db, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// designFlattenSourceClass — ПРОВЕНАНС ФЛЭТТЕНА, И ОН ЧИТАЕТ origin СЛОЯ (0350).
//
// ЧТО БЫЛО. Решение принималось ТОЛЬКО по наличию базовой картинки: есть база → ai_edits, нет →
// drawn. Колонка origin не читалась вовсе, и это ОТМЫВАЛО машинное происхождение в человеческое:
// слой `vectorised` — платная перерисовка растра моделью — записывался «нарисован от руки», а
// чужой импортированный файл `imported` — тоже. Предупреждение о смеси провенансов считается по
// design_picture.source_class (runInputsAreMixed), поэтому оно молчало ровно там, где обязано было
// звучать: ИИ-кадр и человеческий кадр становились неразличимы.
//
// ПОЧЕМУ origin СИЛЬНЕЕ ДОГАДКИ ПО БАЗЕ. База отвечает на вопрос «поверх чего», origin — на вопрос
// «откуда сам вектор», и провенанс — это второе. Импортированный SVG остаётся чужим файлом и
// лёжа поверх нашего растра; машинная перерисовка остаётся машинной и без базы.
//
// entity.DesignSourceImportedSVG получает здесь СВОЕГО ПЕРВОГО ПИСАТЕЛЯ: до этой правки значение
// стояло в словаре провода и не записывалось ни одной строкой кода.
func designFlattenSourceClass(origin string, hasParent bool) string {
	switch entity.DesignLayerOriginOrDrawn(origin) {
	case entity.DesignLayerOriginImported:
		return entity.DesignSourceImportedSVG
	case entity.DesignLayerOriginVectorised:
		// С базой это правка ИМЕННО ТОГО кадра машиной (ai_edits); без базы — просто машинный
		// вектор (ai). Ни одно из двух не `drawn`, и в этом вся разница.
		if hasParent {
			return entity.DesignSourceAIEdits
		}
		return entity.DesignSourceAI
	default:
		// origin = drawn (в том числе пустая колонка): рука человека. Поверх кадра это правка
		// того кадра, на чистом листе — рисунок.
		if hasParent {
			return entity.DesignSourceAIEdits
		}
		return entity.DesignSourceDrawn
	}
}

// layerSaveUpdate — ОДИН ОПЕРАТОР НА ОБА КАНАЛА СЛОЯ, ПОД ОДНИМ ПРЕДИКАТОМ CAS.
//
// Пиксели присваиваются В ТОМ ЖЕ `UPDATE … WHERE rev = :expected`, что и штрихи, поэтому
// устаревший писатель теряет ОБА канала либо ни одного.
//
// ⚠ ЧЕСТНО ПРО СИЛУ ЭТОЙ ФОРМЫ, ПОТОМУ ЧТО ОНА ЗАМЕРЕНА. Один оператор здесь — это ЯСНОСТЬ, а не
// сам замок: замок — транзакция. Растр, вынесенный во второй оператор ВНУТРИ того же замыкания,
// откатился бы вместе с промахом CAS, и проба на это (правильно) не краснеет. Краснеет она на том,
// что действительно опасно, — на растре, уехавшем во ВТОРОЙ ГЛАГОЛ со своей транзакцией и без
// expected_rev. Формулировать «мутация — второй оператор» значило бы держать в коде довод,
// который проба не подтверждает.
//
// ПУСТОЙ ХВОСТ ЗНАЧИТ «ПРО РАСТР НИЧЕГО НЕ СКАЗАНО»: колонка не пишется вовсе, поэтому хранимое
// переживает сохранение, тронувшее одни штрихи. Клиент, приславший растр или явную очистку, попадает
// в первую ветку, и nullInt превращает ноль в NULL — то есть «очистить» и «не задано» на проводе
// различает флаг, а в SQL — присутствие самого присваивания.
func layerSaveUpdate(rasterStated bool) string {
	raster := ""
	if rasterStated {
		raster = ", raster_media_id = :raster"
	}
	return `
		UPDATE design_edit_layer
		SET strokes = :strokes` + raster + `, rev = rev + 1, updated_by = :who
		WHERE id = :id AND rev = :expected`
}

// designLayerIsEmpty — «СПЛЮЩИВАТЬ НЕЧЕГО», И ЭТО ТЕПЕРЬ ВОПРОС ПРО ДВА КАНАЛА, А НЕ ПРО ОДИН.
//
// ЧТО БЫЛО И ПОЧЕМУ ЭТО СЛОМАЛОСЬ БЫ МОЛЧА. Гейт флэттена звучал «нет штрихов — пусто», и до 0355
// это было верно тождественно: штрихи были ЕДИНСТВЕННЫМ каналом слоя. С появлением пикселей то же
// условие стало ложью ровно про тот случай, ради которого круг 6 и затевался: человек взял кисть,
// закрасил, стёр ластиком дырку в фотографии, пера не касался — и получил бы FailedPrecondition
// «у слоя нет штрихов» на полностью законченной работе. Отказ был бы не косметическим: сплющивание
// — единственная дверь, через которую правка попадает в полосу как картинка.
//
// ОБРАТНОЕ ТОЖЕ ДЕРЖИТСЯ: слой с одними штрихами и без растра сплющивается как и раньше.
//
// ПУСТАЯ КОЛОНКА JSON У MySQL ТРЁХЛИКА — отсутствие, литерал `null` и пустой массив, — и все три
// значат одно; проверка перечисляет их явно, потому что ни одна из трёх форм не сводится к другой
// на стороне драйвера.
func designLayerIsEmpty(l entity.DesignEditLayer) bool {
	if l.RasterMediaId.Valid && l.RasterMediaId.Int32 > 0 {
		return false
	}
	s := string(l.Strokes)
	return len(l.Strokes) == 0 || s == "null" || s == "[]"
}

// FlattenEditLayer files an ALREADY-RASTERISED image as a picture of the band, carrying
// derived_from, source_class and layer_rev. The server does not rasterise (Р-2): the client
// produced the raster from base + layer and uploaded it, and the server records the provenance.
//
// expected_rev IS REQUIRED and is not a convenience. Without it a colleague's newer save gets
// materialised under somebody else's intention: the person looked at r3, the file they uploaded
// depicts r3, and the row would claim r4.
//
// THE FLATTEN DOES NOT BUMP THE LAYER'S REV. It materialises a revision, it does not edit one —
// bumping would invalidate every open editor's CAS token for a write that changed no stroke.
func (s *Store) FlattenEditLayer(ctx context.Context, req entity.DesignEditLayerFlatten) (*entity.DesignPicture, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.MediaId <= 0 {
		return nil, fmt.Errorf("%w: a flatten needs the rasterised media", entity.ErrDesignInvalidArgument)
	}
	var out entity.DesignPicture
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		layer, err := layerByID(ctx, db, req.LayerId)
		if err != nil {
			return err
		}
		if layer.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, req.LayerId, layer.TechCardId)
		}
		if layer.Rev != req.ExpectedRev {
			return fmt.Errorf("%w: layer is at rev %d, %d was echoed",
				entity.ErrDesignLayerRevMismatch, layer.Rev, req.ExpectedRev)
		}
		if designLayerIsEmpty(layer) {
			return fmt.Errorf("%w: layer %d holds neither pixels nor strokes",
				entity.ErrDesignEmptyLayer, req.LayerId)
		}

		// The base picture, when the layer has one, is both the derivation parent and the row the
		// flatten hangs under: a flatten is a SIBLING of what it was traced over, not a run of its
		// own, because no money was spent on it.
		// ─── КТО РОДИТЕЛЬ: СНАЧАЛА СПРОСИТЬ СЛОЙ, ПОТОМ ИСКАТЬ ПО ФАЙЛУ (D7) ───
		//
		// У слоя ЕСТЬ колонка source_picture_id — та самая плита, поверх которой рисовали, — и
		// она игнорировалась: родителя искали по паре (карточка, base_media_id) и брали ПЕРВУЮ
		// строку по id. Пока картинка была одна на файл, это совпадало; с осью колорвея один и
		// тот же файл законно регистрируется дважды (тот же мультивью, залитый на другой цвет), и
		// «первая по id» стала БРОСКОМ МОНЕТЫ: флэттен наследовал колорвей произвольной плиты и
		// уезжал в чужой верстак — молча, потому что у результата и у слота колорвеи совпадали
		// по построению.
		//
		// Порядок теперь: названный слоем родитель → иначе поиск по файлу → и если файл называет
		// НЕСКОЛЬКО колорвеев, ОТКАЗ вместо догадки.
		//
		// ⚠ ЗАПИСАННЫЙ ДОЛГ, А НЕ ЗАКРЫТЫЙ ДЕФЕКТ: сужение защищает ТОЛЬКО колорвей. Две
		// регистрации одного файла ПОД ОДНИМ колорвеем по-прежнему разрешаются «первой по id», и
		// вместе с ней флэттен наследует род, прогон, пачку, provenance и derived_from — то есть
		// исходный порок «произвольный родитель» жив, просто перестал менять ВЕРСТАК. Наблюдаемое
		// следствие: перезалив того же мультивью под тем же цветом меняет смысл флэттена в
		// зависимости от порядка вставки. Чинится это не здесь, а решением о том, обязан ли слой
		// ВСЕГДА называть source_picture_id (сегодня его пишет только ImportVector, а
		// SaveEditLayer — никогда); расширять отказ на весь наследуемый набор без этого решения
		// значило бы закрыть флэттен там, где он сегодня работает и никого не обманывает.
		var parent *entity.DesignPicture
		switch {
		case layer.SourcePictureId.Valid && layer.SourcePictureId.Int32 > 0:
			pic, err := pictureByID(ctx, db, int(layer.SourcePictureId.Int32))
			if err != nil {
				return fmt.Errorf("failed to resolve the source picture of layer %d: %w", req.LayerId, err)
			}
			// Граница карточки перепроверяется здесь, а не берётся на веру у записи слоя:
			// чтение и запись разделены временем, и картинка живёт дольше слоя.
			if pic.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: source picture %d of layer %d belongs to tech card %d",
					entity.ErrDesignNotFound, pic.Id, req.LayerId, pic.TechCardId)
			}
			parent = &pic
		case layer.BaseMediaId.Valid && layer.BaseMediaId.Int32 > 0:
			rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
				SELECT * FROM design_picture
				WHERE tech_card_id = :card AND media_id = :media ORDER BY id`,
				map[string]any{"card": req.TechCardId, "media": layer.BaseMediaId.Int32})
			if err != nil {
				return fmt.Errorf("failed to resolve the base picture of layer %d: %w", req.LayerId, err)
			}
			if len(rows) > 0 {
				for _, r := range rows[1:] {
					if entity.DesignColorwayOrNone(r.ColorwayId) != entity.DesignColorwayOrNone(rows[0].ColorwayId) {
						return fmt.Errorf(
							"%w: media %d is registered on tech card %d as pictures %d (colourway %d) and %d "+
								"(colourway %d), and layer %d does not say which one it was drawn over",
							entity.ErrDesignAmbiguousFlattenBase, layer.BaseMediaId.Int32, req.TechCardId,
							rows[0].Id, entity.DesignColorwayOrNone(rows[0].ColorwayId),
							r.Id, entity.DesignColorwayOrNone(r.ColorwayId), req.LayerId)
					}
				}
				parent = &rows[0]
			}
		}

		// PROVENANCE. Both strings come from the wire vocabulary of DesignPicture.source_class —
		// see entity.DesignSourceAIEdits for why the wire wins over the migration's prose.
		src := designFlattenSourceClass(layer.Origin, parent != nil)
		ghost, kind := any(nil), entity.DesignPictureKindFlat
		mixed := false
		runID, batchID, derived := any(nil), any(nil), any(nil)
		cw := any(nil)
		// ONLY-FOR-SHOWING IS INHERITED FROM THE BASE (0361, D-24): a layer drawn over a display-only
		// picture flattens into a display-only picture. An edit does not turn a file the person
		// brought in «for the artifacts only» into a prompt input; a layer drawn from nothing has no
		// base and is an ordinary picture.
		displayOnly := false
		if parent != nil {
			kind = parent.Kind
			mixed = parent.MixedInput
			displayOnly = parent.DisplayOnly
			runID, batchID, derived = nullInt32(parent.RunId), nullInt32(parent.BatchId), parent.Id
			// Флэттен — СИБЛИНГ подложки и наследует её колорвей (0356): перекрашенный слоем
			// рендер колорвея A остаётся кадром колорвея A, иначе он выпал бы из своего верстака.
			cw = nullInt32(parent.ColorwayId)
			if parent.GhostView.Valid {
				ghost = parent.GhostView.String
			}
		}
		ord := 0
		if parent != nil {
			if ord, err = nextSiblingOrdinal(ctx, db, *parent); err != nil {
				return err
			}
		}
		// ГЛАГОЛ ЗАПИСЫВАЕТСЯ ЗДЕСЬ, А НЕ ВЫВОДИТСЯ ЧИТАТЕЛЕМ (0359, J-1).
		//
		// БЕЗ БАЗЫ ГЛАГОЛА НЕТ ВОВСЕ, и пустая строка тут не «неизвестно», а КОРЕНЬ: слой,
		// нарисованный с чистого листа, ни от чего не производен, `derived` рядом тоже NULL, и
		// пара (derived_from, derivation) читается однозначно.
		//
		// ⚠ ПОЧЕМУ НЕЛЬЗЯ ОСТАВИТЬ КЛИЕНТУ ДОГАДКУ ПО `layer_rev`, которая пишется строкой ниже:
		// кроп КОПИРУЕТ ревизию родителя (pictures.go), поэтому «правка» и «разрез правки»
		// приходят с одинаковой ненулевой ревизией; а «правь → сохрани → правь снова» даёт два
		// флэттена, чьи ревизии могут совпасть. Обещание контракта «layer_rev 0 = not flattened»
		// сломано первым из этих двух и было сломано до появления колонки.
		derivation := entity.DesignDerivationNone
		if parent != nil {
			derivation = entity.DesignDerivationFlatten
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_picture
				(tech_card_id, media_id, run_id, batch_id, ordinal, kind, ghost_view,
				 colorway_id, derived_from, derivation, source_class, mixed_input, layer_rev,
				 display_only)
			VALUES (:card, :media, :run, :batch, :ord, :kind, :ghost, :cw, :parent, :derivation,
			        :src, :mixed, :layer, :display_only)`,
			map[string]any{
				"card": req.TechCardId, "media": req.MediaId, "run": runID, "batch": batchID,
				"ord": ord, "kind": kind, "ghost": ghost, "cw": cw, "parent": derived,
				"derivation": derivation,
				"src":        src, "mixed": mixed, "layer": layer.Rev,
				"display_only": displayOnly,
			})
		if err != nil {
			return fmt.Errorf("failed to file the flattened design layer: %w", err)
		}
		out, err = pictureByID(ctx, db, id)
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

// layerByRequestID reads the layer a given client_request_id already filed, if any.
//
// AN EMPTY REQUEST ID MATCHES NOTHING, deliberately: the column is NULL for every layer born of
// SaveEditLayer, and a blank key that matched them would make the first hand-drawn layer on a card
// the idempotent answer to every import.
func layerByRequestID(ctx context.Context, db dependency.DB, requestID string) (entity.DesignEditLayer, bool, error) {
	var out entity.DesignEditLayer
	if strings.TrimSpace(requestID) == "" {
		return out, false, nil
	}
	rows, err := storeutil.QueryListNamed[entity.DesignEditLayer](ctx, db,
		`SELECT * FROM design_edit_layer WHERE client_request_id = :req LIMIT 1`,
		map[string]any{"req": requestID})
	if err != nil {
		return out, false, fmt.Errorf("failed to look for an already-filed vector import: %w", err)
	}
	if len(rows) == 0 {
		return out, false, nil
	}
	return rows[0], true, nil
}

// AssertMediaNotForeign — та же граница, вынесенная НАРУЖУ, для двери.
//
// ⚠ ЗАЧЕМ ГЛАГОЛ, А НЕ КОПИЯ ПРАВИЛА В ХЕНДЛЕРЕ. Дверь обязана отказать ДО резерва денег: прогон с
// чужой картинкой не должен даже открываться. Но правило «чьё это медиа» знает только база, и
// хендлер, отвечавший на него по-своему (через реестр ссылок media), был ВТОРЫМ мнением о том же
// вопросе — а два мнения расходятся в тот день, когда правят одно. Здесь оно одно, и спрашивают его
// оба: дверь снаружи транзакции, ImportVector — внутри своей.
//
// ЧИТАЮЩАЯ ТРАНЗАКЦИЯ, а не пишущая: это вопрос, а не изменение, и SERIALIZABLE-писатель ради
// одного COUNT держал бы блокировки на чужих строках.
func (s *Store) AssertMediaNotForeign(ctx context.Context, techCardID int, mediaIDs []int) error {
	if err := requireCard(techCardID); err != nil {
		return err
	}
	if len(mediaIDs) == 0 {
		return nil
	}
	return s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		return refuseForeignMedia(ctx, rep.DB(), techCardID, mediaIDs...)
	})
}

// refuseForeignMedia — ГРАНИЦА КАРТОЧКИ ДЛЯ ИДЕНТИФИКАТОРА МЕДИА, ПРИШЕДШЕГО С ПРОВОДА.
//
// ⚠ ПРАВИЛО ОТРИЦАТЕЛЬНОЕ («не принадлежит ДРУГОЙ карточке»), А НЕ ПОЛОЖИТЕЛЬНОЕ («принадлежит
// этой»), И ЭТО РЕШЕНИЕ, А НЕ СЛАБОСТЬ. Положительное правило — то, что стоит у SetReferenceRole
// («медиа обязано лежать в tech_card_media этой карточки»), — здесь ЛОЖНО ОТКАЗЫВАЛО БЫ на
// законном жесте: файл, только что загруженный через UploadContentImage, не принадлежит ещё ни
// одной карточке (контракт ImportDesignVector говорит это про source_media_id дословно), а
// картинка полосы живёт в design_picture и в tech_card_media не попадает вовсе — ту таблицу
// целиком переписывает сейв карточки.
//
// ДЕРЖАТЕЛЕЙ РОВНО ДВА, И ЭТО НЕ ПРОИЗВОЛ: tech_card_media — картинки, которые карточка держит
// сама, design_picture — картинки её полосы. Больше НИ ОДНА таблица не отвечает на вопрос «чья это
// карточка» (реестр ссылок media знает ещё продукты, архивы и примерки, но они про другую
// принадлежность). Медиа, за которым не стоит ни одна карточка, — ничейное, и оно проходит.
//
// ⚠ design_asset.media_id ЗДЕСЬ НЕТ НАМЕРЕННО, И ЭТО РЕШЕНИЕ, КОТОРОЕ УЖЕ ОСПАРИВАЛИ. Полки (0354)
// — третья пер-карточная таблица со ссылкой на media, и «третий держатель, который забыли» выглядит
// очевидным выводом. Он неверен, и вот чем.
//
// ДВА ДЕРЖАТЕЛЯ ВЫШЕ — ЭТО КАРТИНКИ САМОГО ИЗДЕЛИЯ: его флэты, его референсы, его рендеры. Такая
// картинка по построению принадлежит ОДНОМУ стилю, поэтому правило «не чужая» никогда не мешает
// работе. Ассет — это картинка МАТЕРИАЛА: лоскут ткани, плитка паттерна, снимок фурнитуры. Один и
// тот же джерси законно шьётся в десяти стилях, и дверь загрузки ассета — это ПИКЕР БИБЛИОТЕКИ
// (клиент: MediaSlot на полке), то есть выбрать тот же файл на второй карточке — один клик.
//
// ЧТО БЫ ДАЛО ДОБАВЛЕНИЕ. Оба запроса ниже симметричны, и таблица, попавшая в них, попадает в оба:
// первая карточка, положившая лоскут на полку, стала бы его ЕДИНСТВЕННЫМ держателем (`others = 1`,
// `mine = 0`), и всякая следующая получала бы отказ «media belongs to another tech card». Полки,
// заведённые ради того, чтобы называть ткани изделия, начали бы запрещать называть ту же ткань во
// втором изделии — то есть правило границы съело бы саму функцию.
//
// А ГДЕ ВРЕД ОТ ОТСУТСТВИЯ, ТАМ ОН ЗАКРЫТ ДРУГИМ ПРАВИЛОМ. Настоящая опасность звучит не «чужой
// лоскут», а «чужая ПОЛКА»: прогон, замораживающий в своей истории `fabrics[*].asset_id` другой
// карточки. Это проверяется по имени — designRefuseForeignClothAssets у двери прогона, — и
// отмывания через ассет тоже не выходит: чтобы положить на полку карточки B чужую картинку, надо
// сначала пройти ЭТУ функцию, а флэт или референс карточки A она держит.
//
// НОЛЬ И ОТРИЦАТЕЛЬНОЕ МОЛЧА ПРОПУСКАЮТСЯ: «не задано» — законное состояние обоих полей, и
// отказывать за отсутствие значения обязан тот, кто его требует, а не эта функция.
func refuseForeignMedia(ctx context.Context, db dependency.DB, cardID int, mediaIDs ...int) error {
	seen := make(map[int]struct{}, len(mediaIDs))
	for _, id := range mediaIDs {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		// СНАЧАЛА СПРАШИВАЕТСЯ «ЧУЖОЕ ЛИ», и обычный ответ — ноль, на котором второй запрос не
		// нужен вовсе: ничейный свежий файл проходит одним чтением.
		others, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COUNT(*) FROM (
				SELECT tech_card_id FROM tech_card_media WHERE media_id = :media
				UNION ALL
				SELECT tech_card_id FROM design_picture WHERE media_id = :media
			) h WHERE h.tech_card_id <> :card`,
			map[string]any{"media": id, "card": cardID})
		if err != nil {
			return fmt.Errorf("failed to check who media %d belongs to: %w", id, err)
		}
		if others == 0 {
			continue
		}
		// ОДИН ФАЙЛ В ДВУХ КАРТОЧКАХ — ОБЫЧНОЕ ДЕЛО (та же ткань, тот же референс), и отказывать
		// за это нельзя: правило про то, что картинка НЕ ЧУЖАЯ, а не про то, что она больше нигде
		// не встречается.
		mine, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COUNT(*) FROM (
				SELECT tech_card_id FROM tech_card_media WHERE media_id = :media
				UNION ALL
				SELECT tech_card_id FROM design_picture WHERE media_id = :media
			) h WHERE h.tech_card_id = :card`,
			map[string]any{"media": id, "card": cardID})
		if err != nil {
			return fmt.Errorf("failed to check whether media %d belongs here: %w", id, err)
		}
		if mine == 0 {
			return fmt.Errorf("%w: media %d belongs to another tech card, not to %d",
				entity.ErrDesignForeignMedia, id, cardID)
		}
	}
	return nil
}

func layerByID(ctx context.Context, db dependency.DB, id int) (entity.DesignEditLayer, error) {
	l, err := storeutil.QueryNamedOne[entity.DesignEditLayer](ctx, db,
		`SELECT * FROM design_edit_layer WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return l, fmt.Errorf("%w: design edit layer %d", entity.ErrDesignNotFound, id)
		}
		return l, fmt.Errorf("failed to read design edit layer %d: %w", id, err)
	}
	return l, nil
}
