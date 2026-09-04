package admin

import (
	"context"
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
	return &pb_admin.SetDesignColourPlanResponse{Plan: designColourPlanToPb(plan)}, nil
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
func designColourPlanToPb(p *entity.DesignColourPlan) *pb_common.DesignColourPlan {
	if p == nil {
		return nil
	}
	return &pb_common.DesignColourPlan{
		TechCardId: int32(p.TechCardId),
		Rev:        int32(p.Rev),
		Maps:       designColourMapsToPb(p.Maps),
		Cloths:     designColourClothsToPb(p.Cloths),
		UpdatedBy:  p.UpdatedBy,
		UpdatedAt:  timestamppb.New(p.UpdatedAt),
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
