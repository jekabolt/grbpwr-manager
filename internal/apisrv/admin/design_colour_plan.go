package admin

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────── ЦВЕТОВОЙ ПЛАН (Feature A, 0364) ───────────────────────────
//
// ОДНА ДВЕРЬ И ОДНО ЧТЕНИЕ: план приезжает в GetDesignBand вместе с верстаком и полками (одно
// мгновение карточки на весь экран) и пишется целиком под CAS.
//
// ⚠ ВТОРОЙ ДВЕРИ — «УДАЛИТЬ» — ЗДЕСЬ БОЛЬШЕ НЕТ, И ЭТО ПОЧИНКА, А НЕ УПРОЩЕНИЕ. DeleteDesignColourPlan
// нёс ОДИН tech_card_id: ревизия не сверялась, ошибки не было, строка исчезала. Сценарий, который
// это стоило: A открыл карточку на rev 3, B двадцать минут красил и сохранил rev 5, устаревшая
// вкладка A жмёт «очистить» — и покраска B снесена молча, а PNG осиротели. Шапка SetColourPlan в
// сторе сама пишет, что двадцать минут покраски — ровно та работа, которую нельзя потерять молча.
// Отдельный глагол для этого и не нужен: «очистить» — это SetDesignColourPlan{expected_rev,
// maps:[], cloths:[]}, состояние, которое контракт называет законным («painted, then cleared»), и
// оно проходит тот же CAS, что всякая другая запись. Добавлять `expected_rev` на удаление значило
// бы завести ВТОРОЙ глагол с теми же правилами и той же ценой ошибки.
//
// ⚠ ГРАНИЦЫ ЖИВУТ В СТОРЕ, В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО И ЗАПИСЬ, И ТЕПЕРЬ ВСЕ ТРИ. Карточка, медиа и
// ПОЛКА проверяются там (refuseUnknownCard / refuseMissingPlanMedia / refuseForeignMedia /
// refuseForeignPlanAssets). Полка проверялась здесь и читала полосу ОТДЕЛЬНЫМ GetBand до открытия
// транзакции — единственная граница фичи, стоявшая не там, где пишут; гонка с DeleteDesignAsset
// пропускала план, называющий снесённую строку.

// SetDesignColourPlan replaces the card's whole colour plan under compare-and-set on its rev.
func (s *Server) SetDesignColourPlan(ctx context.Context, req *pb_admin.SetDesignColourPlanRequest) (*pb_admin.SetDesignColourPlanResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if rev := req.GetExpectedRev(); rev < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "expected_rev %d is not a revision", rev)
	}
	maps := designColourMapsFromPb(req.GetMaps())
	cloths := designColourClothsFromPb(req.GetCloths())

	plan, err := s.repo.Design().SetColourPlan(ctx, entity.DesignColourPlanSave{
		TechCardId:  cardID,
		ExpectedRev: int(req.GetExpectedRev()),
		Maps:        maps,
		Cloths:      cloths,
		Actor:       designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to save the design colour plan", err,
			map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	// ⚠ ОТВЕТ СОХРАНЕНИЯ ПРОХОДИТ ТОТ ЖЕ JOIN, ЧТО И ПОЛОСА, И ЭТО НЕ УКРАШЕНИЕ. Клиент,
	// сохранивший план, обязан получить РОВНО ТО ЖЕ, что он получил бы, перезагрузив страницу, —
	// иначе «после сохранения карта видна, после перезагрузки нет» (или наоборот), и разница
	// читается как потеря данных. Ровно поэтому конвертер — метод: собрать DesignColourPlan, не
	// соединив картинки, отсюда физически нельзя, а значит пустой `media` на проводе всегда
	// значит «картинки нет», а не «здесь join забыли позвать».
	return &pb_admin.SetDesignColourPlanResponse{Plan: s.designColourPlanToPb(ctx, plan)}, nil
}

/* ─────────────────────────── the wire ─────────────────────────── */

func designColourMapsFromPb(in []*pb_common.DesignColourMap) []entity.DesignColourMap {
	out := make([]entity.DesignColourMap, 0, len(in))
	for _, m := range in {
		if m == nil {
			continue
		}
		swatches := make([]entity.DesignColourSwatch, 0, len(m.GetPalette()))
		for _, sw := range m.GetPalette() {
			if sw == nil {
				continue
			}
			swatches = append(swatches, entity.DesignColourSwatch{
				Hex: sw.GetHex(), Px: int(sw.GetPx()),
			})
		}
		out = append(out, entity.DesignColourMap{
			MediaId:     int(m.GetMediaId()),
			View:        m.GetView(),
			BaseMediaId: int(m.GetBaseMediaId()),
			Palette:     swatches,
		})
	}
	return out
}

func designColourClothsFromPb(in []*pb_common.DesignColourCloth) []entity.DesignColourCloth {
	out := make([]entity.DesignColourCloth, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, entity.DesignColourCloth{
			Hex:       c.GetHex(),
			AssetId:   int(c.GetAssetId()),
			ColourHex: c.GetColourHex(),
			Words:     c.GetWords(),
			Parts:     c.GetParts(),
		})
	}
	return out
}

// designColourPlanToPb — nil ОСТАЁТСЯ nil. Полоса объявляет «на этой карточке плана нет» пустым
// полем, а не пустым документом с rev 0: клиент, получивший план-пустышку, echo'нул бы её rev и
// разошёлся бы с сервером на первом же сохранении.
//
// ⚠ ЭТО МЕТОД, А НЕ ФУНКЦИЯ, И ПРИЧИНА РОВНО ОДНА: СОЕДИНЕНИЕ КАРТИНОК. Пока конвертер был
// свободной функцией, план можно было собрать где угодно и отдать без картинок — а тогда пустой
// `media` на проводе означал бы два разных состояния («файла нет» и «здесь join не звали»), и
// клиенту пришлось бы гадать, какое. Метод делает соединение НЕИЗБЕЖНЫМ: у producers плана нет
// другого способа получить сообщение.
func (s *Server) designColourPlanToPb(ctx context.Context, p *entity.DesignColourPlan) *pb_common.DesignColourPlan {
	if p == nil {
		return nil
	}
	out := &pb_common.DesignColourPlan{
		TechCardId: int32(p.TechCardId),
		Rev:        int32(p.Rev),
		Maps:       designColourMapsToPb(p.Maps),
		Cloths:     designColourClothsToPb(p.Cloths),
		UpdatedBy:  p.UpdatedBy,
		UpdatedAt:  timestamppb.New(p.UpdatedAt),
	}
	s.joinDesignColourPlanMedia(ctx, out)
	return out
}

// joinDesignColourPlanMedia резолвит ЖИВОПИСЬ, которую план помнит одним номером (Feature A).
//
// ЧТО БЫЛО СЛОМАНО. PNG карты — это СОБСТВЕННАЯ загрузка: он не DesignPicture, не плита верстака и
// не строка полки, поэтому во всём ответе полосы не было НИ ОДНОГО места, откуда клиент мог бы
// взять его url. Сохранённая карта уезжала голым числом, и после перезагрузки страница не могла ни
// показать её, ни открыть на правку: плитка откатывалась к флэту, «paint ▸» открывал чистый холст
// поверх него, а следующее сохранение записывало эту чистоту поверх двадцати минут покраски.
// Палитра и назначения при этом переживали перезагрузку целыми — то есть экран выглядел рабочим и
// молча терял ровно ту половину, ради которой фича существует.
//
// ТОТ ЖЕ ПРИЁМ, ЧТО У joinDesignRunInputMedia И joinDesignLayerRasterMedia, А НЕ ЧЕТВЁРТЫЙ. Живое
// медиа доезжает картинкой, ИСЧЕЗНУВШЕЕ помечается `deleted`, запрос ОДИН на весь план. Номер при
// этом не стирается никогда: «какая живопись пропала» отвечается только по нему.
//
// ⚠ ОТКАЗ ЗАПРОСА — ЭТО «МЫ НЕ ЗНАЕМ», А НЕ «ЕГО НЕТ», и это дословно правило соседа. Ставить
// `deleted` по неудавшемуся чтению значило бы сказать человеку, что его покраска удалена, когда
// она, вероятно, жива, — и он перекрасил бы вид заново поверх целого файла. Ронять же весь ответ
// нельзя тем более: кроме картинок в плане есть палитра, назначения и rev, и все они правда.
//
// ПОТОЛОК ОТВЕТА НАЗВАН И ЗАМЕРЕН, А НЕ ОЦЕНЁН НА ГЛАЗ: карт в плане не больше
// MaxDesignColourMaps (6), картинка у карты одна, значит соединение добавляет к полосе не более
// ШЕСТИ MediaFull. Полностью заполненный MediaFull (три url, размеры, blurhash, sha256) кодируется
// в 390 байт, то есть худший случай — 2340 байт против grpcMaxSendMsgSize в 50 МиБ (0,0045%).
// Это и есть ответ на находку 4 ревью: документ плана уже ограничен 64 КБ, и путь ЧТЕНИЯ не
// открывает ему нового способа расти. Подложка (`base_media_id`) сюда НЕ входит и это не пропуск:
// флэт у клиента уже есть — он на верстаке и в выходах карточки, — а второй join удвоил бы цену
// ради url, который в том же ответе лежит рядом.
func (s *Server) joinDesignColourPlanMedia(ctx context.Context, p *pb_common.DesignColourPlan) {
	if p == nil {
		return
	}
	seen := make(map[int]struct{}, len(p.GetMaps()))
	for _, m := range p.GetMaps() {
		if id := int(m.GetMediaId()); id > 0 {
			seen[id] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	found, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design: failed to join the painted media into the colour plan",
			slog.String("err", err.Error()))
		return
	}
	for _, m := range p.GetMaps() {
		id := int(m.GetMediaId())
		if id <= 0 {
			// НУЛЯ ЗДЕСЬ НЕ БЫВАЕТ У ЖИВОГО ПЛАНА — entity.Validate требует картинку у каждой
			// карты, — но если он всё же приехал, это «карта без картинки», а не «картинку
			// удалили»: флаг о пропаже принадлежит только тому, у кого номер есть.
			continue
		}
		md, ok := found[id]
		if !ok {
			m.Media, m.Deleted = nil, true
			continue
		}
		m.Media, m.Deleted = designMediaToPb(&md), false
	}
}

func designColourMapsToPb(in []entity.DesignColourMap) []*pb_common.DesignColourMap {
	out := make([]*pb_common.DesignColourMap, 0, len(in))
	for _, m := range in {
		palette := make([]*pb_common.DesignColourSwatch, 0, len(m.Palette))
		for _, sw := range m.Palette {
			palette = append(palette, &pb_common.DesignColourSwatch{
				Hex: sw.Hex, Px: int32(sw.Px),
			})
		}
		out = append(out, &pb_common.DesignColourMap{
			MediaId:     int32(m.MediaId),
			View:        m.View,
			BaseMediaId: int32(m.BaseMediaId),
			Palette:     palette,
		})
	}
	return out
}

func designColourClothsToPb(in []entity.DesignColourCloth) []*pb_common.DesignColourCloth {
	out := make([]*pb_common.DesignColourCloth, 0, len(in))
	for _, c := range in {
		out = append(out, &pb_common.DesignColourCloth{
			Hex:       c.Hex,
			AssetId:   int32(c.AssetId),
			ColourHex: c.ColourHex,
			Words:     c.Words,
			Parts:     c.Parts,
		})
	}
	return out
}
