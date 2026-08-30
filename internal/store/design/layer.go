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

// SaveEditLayer stores a vector layer under compare-and-set on its rev. layer_id = 0 creates one;
// with base_media_id = 0 that is the clean vector base of the «draw it» door, and a card may hold
// several of those — uq_design_edit_layer_base tolerates repeated NULLs.
//
// CAS IS NOT MADE REDUNDANT BY SERIALIZABLE. The isolation level orders two writers; it cannot
// tell that the second one was looking at r3 while r4 already existed.
func (s *Store) SaveEditLayer(ctx context.Context, req entity.DesignEditLayerSave) (*entity.DesignEditLayer, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if len(req.Strokes) > MaxStrokesBytes {
		return nil, fmt.Errorf("%w: %d bytes of strokes, the ceiling is %d",
			entity.ErrDesignStrokesTooLarge, len(req.Strokes), MaxStrokesBytes)
	}
	var out entity.DesignEditLayer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if req.LayerId == 0 {
			if req.ExpectedRev != 0 {
				return fmt.Errorf("%w: a layer that does not exist yet is at rev 0",
					entity.ErrDesignLayerRevMismatch)
			}
			id, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_edit_layer (tech_card_id, base_media_id, rev, strokes, updated_by)
				VALUES (:card, :base, 1, :strokes, :who)`,
				map[string]any{
					"card": req.TechCardId, "base": nullInt(req.BaseMediaId),
					"strokes": jsonOrNil(req.Strokes), "who": req.Actor,
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
		n, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_edit_layer
			SET strokes = :strokes, rev = rev + 1, updated_by = :who
			WHERE id = :id AND rev = :expected`,
			map[string]any{
				"strokes": jsonOrNil(req.Strokes), "who": req.Actor,
				"id": req.LayerId, "expected": req.ExpectedRev,
			})
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
		if len(layer.Strokes) == 0 || string(layer.Strokes) == "null" || string(layer.Strokes) == "[]" {
			return fmt.Errorf("%w: layer %d has no strokes", entity.ErrDesignEmptyLayer, req.LayerId)
		}

		// The base picture, when the layer has one, is both the derivation parent and the row the
		// flatten hangs under: a flatten is a SIBLING of what it was traced over, not a run of its
		// own, because no money was spent on it.
		var parent *entity.DesignPicture
		if layer.BaseMediaId.Valid && layer.BaseMediaId.Int32 > 0 {
			rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
				SELECT * FROM design_picture
				WHERE tech_card_id = :card AND media_id = :media ORDER BY id LIMIT 1`,
				map[string]any{"card": req.TechCardId, "media": layer.BaseMediaId.Int32})
			if err != nil {
				return fmt.Errorf("failed to resolve the base picture of layer %d: %w", req.LayerId, err)
			}
			if len(rows) > 0 {
				parent = &rows[0]
			}
		}

		// PROVENANCE. Both strings come from the wire vocabulary of DesignPicture.source_class —
		// see entity.DesignSourceAIEdits for why the wire wins over the migration's prose.
		src := designFlattenSourceClass(layer.Origin, parent != nil)
		ghost, kind := any(nil), entity.DesignPictureKindFlat
		mixed := false
		runID, batchID, derived := any(nil), any(nil), any(nil)
		if parent != nil {
			kind = parent.Kind
			mixed = parent.MixedInput
			runID, batchID, derived = nullInt32(parent.RunId), nullInt32(parent.BatchId), parent.Id
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
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_picture
				(tech_card_id, media_id, run_id, batch_id, ordinal, kind, ghost_view,
				 derived_from, source_class, mixed_input, layer_rev)
			VALUES (:card, :media, :run, :batch, :ord, :kind, :ghost, :parent, :src, :mixed, :layer)`,
			map[string]any{
				"card": req.TechCardId, "media": req.MediaId, "run": runID, "batch": batchID,
				"ord": ord, "kind": kind, "ghost": ghost, "parent": derived,
				"src": src, "mixed": mixed, "layer": layer.Rev,
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
