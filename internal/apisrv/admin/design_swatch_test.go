package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══ STEP 3: СВОТЧ У ДВЕРИ — ЦВЕТ ОБЯЗАТЕЛЕН, КАРТИНОК 0–1, СЛОТ — ЭТОЙ КАРТОЧКИ ═══════════════

// ОТКАЗЫ РЕЖИМА СВОТЧА — БЕСПЛАТНЫЕ И СВОИМИ СЛОВАМИ; РЕЖИМ ФОТОГРАФИИ НЕ ТРОНУТ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: не различать режим (свотч из одного цвета получает one_source_picture);
// принять свотч без цвета (модель вернёт ткань случайного оттенка за те же деньги); принять две
// фактуры; прочесть неизвестный режим как image (дверь и воркер разошлись бы в том, что куплено).
func TestTheSwatchDoorREFUSES_BEFORE_MONEY(t *testing.T) {
	swatch := func(colour *pb_common.DesignColourRecipe, refs ...int32) *pb_common.DesignRunParams {
		return &pb_common.DesignRunParams{
			ExtraInputMediaIds: refs,
			Colour:             colour,
			Pattern:            &pb_common.DesignPatternParams{Name: "black · outer", Mode: entity.DesignPatternModeSwatch},
		}
	}
	image := func(mode string, refs ...int32) *pb_common.DesignRunParams {
		return &pb_common.DesignRunParams{
			ExtraInputMediaIds: refs,
			Pattern:            &pb_common.DesignPatternParams{Name: "fabric 1", Mode: mode},
		}
	}
	hex := &pb_common.DesignColourRecipe{Hex: "#C8102E"}
	for _, tc := range []struct {
		name   string
		params *pb_common.DesignRunParams
		reason string // "" = accepted
	}{
		{"swatch without a colour", swatch(nil), entity.DesignErrorCodeNoColour},
		{"swatch with a blank colour", swatch(&pb_common.DesignColourRecipe{Hex: " ", Words: "  "}),
			entity.DesignErrorCodeNoColour},
		{"swatch from a hex alone", swatch(hex), ""},
		{"swatch from a code alone", swatch(&pb_common.DesignColourRecipe{Code: "18-1664 TCX"}), ""},
		{"swatch from words alone", swatch(&pb_common.DesignColourRecipe{Words: "Pantone Fiery Red"}), ""},
		{"swatch with one texture", swatch(hex, 11), ""},
		{"swatch with two textures", swatch(hex, 11, 12), entity.DesignErrorCodeOneTexturePicture},
		{"image with no picture", image(entity.DesignPatternModeImage), "one_source_picture"},
		{"legacy empty mode with two pictures", image("", 11, 12), "one_source_picture"},
		{"image with one picture", image(entity.DesignPatternModeImage, 11), ""},
		{"legacy empty mode with one picture", image("", 11), ""},
		// НЕИЗВЕСТНЫЙ РЕЖИМ — ОТКАЗ СО СВОИМ СЛОВОМ, а не «читай как image»: дверь и воркер
		// разошлись бы в том, что куплено.
		{"an unknown mode is refused, not read as image", image("photo", 11),
			entity.DesignErrorCodeUnknownPatternMode},
		{"an unknown mode is refused even with no picture", image("photo"),
			entity.DesignErrorCodeUnknownPatternMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseUnworkableSources(entity.DesignRunKindPattern, "", tc.params)
			if tc.reason == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Equal(t, tc.reason, ffReason(t, err))
		})
	}

	t.Run("hardware: slot required, colour optional, 0..4 references", func(t *testing.T) {
		hw := func(cw int32, bom int32, colour *pb_common.DesignColourRecipe, refs ...int32) *pb_common.DesignRunParams {
			return &pb_common.DesignRunParams{
				ColorwayId:         cw,
				ExtraInputMediaIds: refs,
				Colour:             colour,
				Pattern: &pb_common.DesignPatternParams{Name: "button · horn", Mode: entity.DesignPatternModeHardware,
					BomItemId: bom},
			}
		}
		for _, tc := range []struct {
			name   string
			params *pb_common.DesignRunParams
			reason string
		}{
			{"no colour, no refs", hw(13, 904, nil), ""},
			{"colour stated", hw(13, 904, hex), ""},
			{"four refs", hw(13, 904, nil, 1, 2, 3, 4), ""},
			{"five refs", hw(13, 904, nil, 1, 2, 3, 4, 5), entity.DesignErrorCodeTooManyReferences},
			{"no bom line", hw(13, 0, hex), entity.DesignErrorCodeHardwareNeedsSlot},
			{"no colourway", hw(0, 904, hex), entity.DesignErrorCodeHardwareNeedsSlot},
		} {
			err := designRefuseUnworkableSources(entity.DesignRunKindPattern, "", tc.params)
			if tc.reason == "" {
				require.NoErrorf(t, err, tc.name)
				continue
			}
			require.Equalf(t, codes.InvalidArgument, status.Code(err), tc.name)
			require.Equalf(t, tc.reason, ffReason(t, err), tc.name)
		}
		p := hw(13, 904, nil)
		p.Pattern.Name = ""
		require.Equal(t, "pattern_name_required",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindPattern, "", p)))
	})

	t.Run("label: slot required, colour optional, 0..1 picture (the logo)", func(t *testing.T) {
		lb := func(cw int32, bom int32, refs ...int32) *pb_common.DesignRunParams {
			return &pb_common.DesignRunParams{
				ColorwayId:         cw,
				ExtraInputMediaIds: refs,
				Pattern: &pb_common.DesignPatternParams{Name: "brand label", Mode: entity.DesignPatternModeLabel,
					BomItemId: bom},
			}
		}
		for _, tc := range []struct {
			name   string
			params *pb_common.DesignRunParams
			reason string
		}{
			{"blank label", lb(13, 904), ""},
			{"one logo", lb(13, 904, 1), ""},
			{"two pictures", lb(13, 904, 1, 2), entity.DesignErrorCodeTooManyReferences},
			{"no bom line", lb(13, 0), entity.DesignErrorCodeHardwareNeedsSlot},
			{"no colourway", lb(0, 904), entity.DesignErrorCodeHardwareNeedsSlot},
		} {
			err := designRefuseUnworkableSources(entity.DesignRunKindPattern, "", tc.params)
			if tc.reason == "" {
				require.NoErrorf(t, err, tc.name)
				continue
			}
			require.Equalf(t, codes.InvalidArgument, status.Code(err), tc.name)
			require.Equalf(t, tc.reason, ffReason(t, err), tc.name)
		}
	})

	t.Run("a swatch still needs its name", func(t *testing.T) {
		p := swatch(hex)
		p.Pattern.Name = " "
		require.Equal(t, "pattern_name_required",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindPattern, "", p)))
	})
}

// СЛОТ ПЛИТКИ — СТРОКА BOM ЭТОЙ КАРТОЧКИ, И ОТКАЗ ЧУЖОЙ — FailedPrecondition ДО РЕЗЕРВА.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать designRefuseForeignBomLine из StartDesignRun — прогон заплатит за
// свотч, который при посадке не сможет стать тканью ни одной пары этой карточки. И вторая: перестать
// судить СЕМЬЮ строки — свотч на строке фурнитуры или ниток заплатил бы за ткань, которая не может
// быть картинкой пуговицы (cloth_on_trim_line).
func TestTheSwatchDoorREFUSES_A_FOREIGN_BOM_LINE(t *testing.T) {
	card := designMoodCard()
	card.BomItems = []entity.TechCardBomItem{
		{Id: 902, Section: entity.BomSectionFabric},
		{Id: 903, Section: entity.BomSectionLining},
		{Id: 904, Section: entity.BomSectionHardware},
		{Id: 905, Section: entity.BomSectionThread},
	}
	for _, tc := range []struct {
		name    string
		bom     int32
		code    codes.Code
		reason  string
		refused bool
	}{
		{"a line of this card", 902, codes.OK, "", false},
		{"a lining line of this card", 903, codes.OK, "", false},
		{"not made for a slot", 0, codes.OK, "", false},
		{"a hardware line of this card — a swatch is cloth", 904, codes.FailedPrecondition,
			entity.DesignErrorCodeClothOnTrimLine, true},
		{"a thread line of this card — a swatch is cloth", 905, codes.FailedPrecondition,
			entity.DesignErrorCodeClothOnTrimLine, true},
		{"a line of another card", 7777, codes.FailedPrecondition, entity.DesignErrorCodeForeignBomLine, true},
		{"a negative id", -3, codes.InvalidArgument, entity.DesignErrorCodeBadBomLineID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignRunRig(t, card, designBandWith(true))
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).
				Return(nil).Maybe()
			req := designStartRequest(entity.DesignRunKindPattern)
			req.Params = &pb_common.DesignRunParams{
				Colour: &pb_common.DesignColourRecipe{Hex: "#C8102E", Words: "Pantone Fiery Red · outer"},
				Pattern: &pb_common.DesignPatternParams{
					Name: "black · outer", Mode: entity.DesignPatternModeSwatch, BomItemId: tc.bom,
				},
			}
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			if !tc.refused {
				require.NoError(t, err)
				require.NotNil(t, rig.sent, "положительный контроль: свотч без картинки дошёл до стора")
				return
			}
			require.Error(t, err)
			require.Equal(t, tc.code, status.Code(err))
			require.Equal(t, tc.reason, ffReason(t, err), "каждый отказ двери называет себя словом")
			require.Nil(t, rig.sent, "отказ обязан стоять ДО резерва")
		})
	}
}

// СЕМЬЯ СТРОКИ ПО РЕЖИМУ: фурнитура — только на не-рулонную строку, свотч — только на рулонную,
// фотография (image) семью не судит. Отказы — FailedPrecondition со своими токенами.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: перепутать IsRollGoodsSection местами в двух ветках; судить image-режим.
func TestTheBomLineDoorJUDGES_THE_FAMILY_BY_MODE(t *testing.T) {
	bom := []entity.TechCardBomItem{
		{Id: 902, Section: entity.BomSectionFabric},
		{Id: 906, Section: entity.BomSectionInsulation},
		{Id: 904, Section: entity.BomSectionHardware},
		{Id: 905, Section: entity.BomSectionThread},
	}
	for _, tc := range []struct {
		name   string
		mode   string
		bom    int32
		reason string
	}{
		{"hardware on a hardware line", entity.DesignPatternModeHardware, 904, ""},
		{"hardware on a thread line", entity.DesignPatternModeHardware, 905, ""},
		{"hardware on a fabric line", entity.DesignPatternModeHardware, 902, entity.DesignErrorCodeHardwareOnClothLine},
		{"hardware on an insulation line", entity.DesignPatternModeHardware, 906, entity.DesignErrorCodeHardwareOnClothLine},
		{"label on a hardware line", entity.DesignPatternModeLabel, 904, ""},
		{"label on a fabric line", entity.DesignPatternModeLabel, 902, entity.DesignErrorCodeHardwareOnClothLine},
		{"swatch on a fabric line", entity.DesignPatternModeSwatch, 902, ""},
		{"swatch on an insulation line", entity.DesignPatternModeSwatch, 906, ""},
		{"swatch on a hardware line", entity.DesignPatternModeSwatch, 904, entity.DesignErrorCodeClothOnTrimLine},
		{"image on a hardware line is not judged", entity.DesignPatternModeImage, 904, ""},
		{"legacy empty mode on a thread line is not judged", "", 905, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseForeignBomLine(41, &pb_common.DesignRunParams{
				Pattern: &pb_common.DesignPatternParams{Name: "x", Mode: tc.mode, BomItemId: tc.bom},
			}, bom)
			if tc.reason == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Equal(t, tc.reason, ffReason(t, err))
		})
	}
}
