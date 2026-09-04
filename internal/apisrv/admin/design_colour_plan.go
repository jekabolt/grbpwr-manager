package admin

import (
	"context"
	"fmt"
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
// ДВЕ ДВЕРИ И ОДНО ЧТЕНИЕ: план приезжает в GetDesignBand вместе с верстаком и полками (одно
// мгновение карточки на весь экран), пишется целиком под CAS и удаляется целиком.
//
// ⚠ ГРАНИЦА КАРТОЧКИ ЖИВЁТ В СТОРЕ, В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО И ЗАПИСЬ — сюда она не переносится и
// не дублируется. Здесь спрашивается только то, чего стор спросить не может: принадлежит ли
// названная полка ЭТОЙ карточке. Полки приезжают в полосе целиком (потолок MaxDesignAssetsPerCard),
// поэтому ответ читается одним чтением полосы и не требует второго запроса.

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

	// ⚠ АДРЕС ПОЛКИ ПРОВЕРЯЕТСЯ ЗДЕСЬ, И ЭТО ТОТ ЖЕ ДОВОД, ЧТО У designRefuseForeignClothAssets НА
	// ДВЕРИ ПРОГОНА. Ложный `asset_id` не всплывёт ошибкой никогда: план просто навсегда утверждает,
	// что цвет метит полку, которой у этой карточки нет, — и утверждение это уедет в рецепт
	// прогона, где оно уже заморожено. Чтение полосы здесь одно и оно же отвечает на вопрос.
	if err := s.designRefusePlanForeignAssets(ctx, cardID, cloths); err != nil {
		return nil, err
	}

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

// DeleteDesignColourPlan removes the card's colour plan. The map pictures are ordinary media and
// are NOT deleted — they may already be frozen into a run's recipe.
func (s *Server) DeleteDesignColourPlan(ctx context.Context, req *pb_admin.DeleteDesignColourPlanRequest) (*pb_admin.DeleteDesignColourPlanResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if err := s.repo.Design().DeleteColourPlan(ctx, cardID); err != nil {
		return nil, designError(ctx, "failed to delete the design colour plan", err,
			map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	return &pb_admin.DeleteDesignColourPlanResponse{}, nil
}

// designRefusePlanForeignAssets держит карточную половину адреса полки, ровно как
// designRefuseForeignClothAssets держит её для тканей прогона.
//
// НОЛЬ ПРОПУСКАЕТСЯ МОЛЧА: цвет, названный плоским цветом или словами, полки не имеет вовсе, и это
// законный, полный ответ на вопрос «из чего эта деталь».
func (s *Server) designRefusePlanForeignAssets(ctx context.Context, cardID int, cloths []entity.DesignColourCloth) error {
	need := false
	for _, c := range cloths {
		if c.AssetId > 0 {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return designError(ctx, "failed to read the card's shelves", err, nil)
	}
	shelf := make(map[int]struct{}, len(band.Assets))
	for _, a := range band.Assets {
		if a.TechCardId == cardID {
			shelf[a.Id] = struct{}{}
		}
	}
	for i, c := range cloths {
		if c.AssetId <= 0 {
			continue
		}
		if _, ok := shelf[c.AssetId]; !ok {
			return designError(ctx, "a colour plan named a foreign shelf row",
				fmt.Errorf("%w: cloths.%d.asset_id %d is not a shelf row of tech card %d",
					entity.ErrDesignInvalidArgument, i, c.AssetId, cardID), nil)
		}
	}
	return nil
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
