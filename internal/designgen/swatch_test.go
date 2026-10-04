package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ STEP 3: СВОТЧ — ПЛИТКА ИЗ ЗАЯВЛЕННОГО ЦВЕТА, С 0–1 РЕФЕРЕНСОМ ФАКТУРЫ ══════════════════════

// TestTheSwatchCraftBUILDS_FROM_THE_STATED_COLOUR.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: писать свотчу абзац фотографии («reconstruct the print … crumpled» — модель
// ищет мятую ткань, которой нет); не отделить фактуру от цвета (референс красит плитку в свой
// цвет); не сказать «без мотива» (гладкий твил возвращается принтом); потерять стык и исключения,
// общие для обоих режимов.
func TestTheSwatchCraftBUILDS_FROM_THE_STATED_COLOUR(t *testing.T) {
	for _, tc := range []struct {
		name     string
		p        patternParams
		pictures int
		contains []string
		absent   []string
	}{
		{
			name:     "swatch with a texture reference",
			p:        patternParams{Mode: entity.DesignPatternModeSwatch},
			pictures: 1,
			contains: []string{
				"fabric swatch", "match the stated colour value exactly across the whole tile",
				"texture reference only", "nothing of its colour, its lighting, its crop",
				"draw no motif, print, stripe or check unless the cloth words above name one",
				"natural scale",
			},
			absent: []string{"no picture is attached", "reconstruct the print", "crumpled",
				"lighting of the source photograph", "choose the size of the repeat yourself"},
		},
		{
			name:     "swatch from the colour alone",
			p:        patternParams{Mode: entity.DesignPatternModeSwatch},
			pictures: 0,
			contains: []string{
				"match the stated colour value exactly across the whole tile",
				"no picture is attached", "plain cloth of that material in that colour",
				"believable weave or knit at natural scale", "draw no motif",
			},
			absent: []string{"texture reference only", "reconstruct the print"},
		},
		{
			name:     "swatch with a stated repeat keeps the repeat branch",
			p:        patternParams{Mode: entity.DesignPatternModeSwatch, RepeatMM: 80},
			pictures: 0,
			contains: []string{"draw the motif at the scale of a 80 mm repeat"},
			absent:   []string{"show the cloth at natural scale"},
		},
		{
			name:     "image mode is untouched",
			p:        patternParams{Mode: entity.DesignPatternModeImage},
			pictures: 1,
			contains: []string{"reconstruct the print", "crumpled", "lighting of the source photograph",
				"choose the size of the repeat yourself"},
			absent: []string{"fabric swatch", "match the stated colour value", "texture reference only"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			low := strings.ToLower(patternCraft(tc.p, tc.pictures))
			for _, must := range tc.contains {
				require.Containsf(t, low, must, "the tile craft must say %q", must)
			}
			for _, never := range tc.absent {
				require.NotContainsf(t, low, never, "the tile craft must not say %q here", never)
			}
			// СТЫК, ИСКЛЮЧЕНИЯ, РОВНОЕ ПОЛЕ И РОВНЫЙ СВЕТ — В ОБОИХ РЕЖИМАХ: свотч раскладывается
			// на рендере ровно как плитка.
			for _, kept := range []string{
				"right edge", "left edge", "bottom edge", "top edge", "border", "vignette",
				"watermark", "no single focal", "light the tile flatly and evenly",
			} {
				require.Containsf(t, low, kept, "the shared half of the craft is missing %q", kept)
			}
		})
	}
}

// TestASwatchIsZERO_OR_ONE_TEXTURE_AND_AN_IMAGE_TILE_STILL_EXACTLY_ONE.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: не различать режим на денежной границе — свотч из одного цвета
// отказывался бы («exactly one picture»), а свотч с двумя фактурами уходил бы платным вызовом.
func TestASwatchIsZERO_OR_ONE_TEXTURE_AND_AN_IMAGE_TILE_STILL_EXACTLY_ONE(t *testing.T) {
	for _, refs := range [][]string{nil, {"https://cdn/weave.png"}} {
		calls, err := imageCalls(Job{
			Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeSwatch,
			Prompt: "swatch", References: refs,
		})
		require.NoErrorf(t, err, "%d texture pictures", len(refs))
		require.Len(t, calls, 1, "одна плитка — один платный вызов")
		require.Equal(t, 1, calls[0].n)
		require.Len(t, calls[0].refs, len(refs))
	}
	_, err := imageCalls(Job{
		Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeSwatch,
		References: []string{"a", "b"},
	})
	require.Error(t, err, "две фактуры смешались бы в третью")
	require.False(t, classify(err).Retryable)

	for _, mode := range []string{"", entity.DesignPatternModeImage} {
		_, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, PatternMode: mode})
		require.Errorf(t, err, "mode %q with no source must still refuse", mode)
	}
	require.Equal(t, "opaque", backgroundFor(entity.DesignRunKindPattern), "у свотча дыр нет так же, как у плитки")
}

// TestTheSwatchModeREACHES_THE_JOB_AND_THE_PROMPT.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: не прочесть `mode` из снимка (или прочесть его под lowerCamelCase) —
// режим молча становится image, и свотч из одного цвета уходит абзацем «reconstruct the print»
// либо отказывается на денежной границе.
func TestTheSwatchModeREACHES_THE_JOB_AND_THE_PROMPT(t *testing.T) {
	run := entity.DesignRun{
		Id: 7, TechCardId: 41, Kind: entity.DesignRunKindPattern,
		Params: rawJSON(t, map[string]any{
			"extra_input_media_ids": []int{77},
			"pattern": map[string]any{
				"name": "black · outer", "mode": entity.DesignPatternModeSwatch, "bom_item_id": 902,
			},
			"colour": map[string]any{
				"hex": "#C8102E", "words": "Pantone 18-1664 TCX Fiery Red · outer · 100% cotton twill",
			},
		}),
		Inputs: rawJSON(t, map[string]any{}),
	}
	job, err := buildJob(context.Background(), media(77), nil, run, "medium")
	require.NoError(t, err)
	require.Equal(t, entity.DesignPatternModeSwatch, job.PatternMode)
	require.Len(t, job.References, 1)
	low := strings.ToLower(job.Prompt)
	require.Contains(t, low, "#c8102e", "цвет обязан доехать до модели значением")
	require.Contains(t, low, "fiery red", "и словами")
	require.Contains(t, low, "texture reference only")

	// ФАКТУРА, НЕ ПЕРЕЖИВШАЯ РЕЗОЛВ, ДЕЛАЕТ ВЫЗОВ ГЛАДКОЙ ТКАНЬЮ, А НЕ ЛОЖЬЮ ПРО КАРТИНКУ.
	job, err = buildJob(context.Background(), media(), nil, run, "medium")
	require.NoError(t, err)
	require.Empty(t, job.References)
	require.Contains(t, strings.ToLower(job.Prompt), "no picture is attached")
}

// TestTheHardwareCraftIS_A_PRODUCT_SHOT_NOT_A_TILE: mode hardware writes its own craft (one item,
// white ground, colour matched if stated, refs = shape/material) and none of the tile half.
func TestTheHardwareCraftIS_A_PRODUCT_SHOT_NOT_A_TILE(t *testing.T) {
	for _, pictures := range []int{0, 3} {
		low := strings.ToLower(patternCraft(patternParams{Mode: entity.DesignPatternModeHardware, RepeatMM: 80}, pictures))
		for _, must := range []string{"single garment trim item", "exactly one of it",
			"plain, seamless pure white background", "soft, even studio light", "any hand",
			"match the stated colour value exactly", "if no colour is stated"} {
			require.Containsf(t, low, must, "%d pictures: must say %q", pictures, must)
		}
		for _, never := range []string{"repeating tile", "right edge", "repeat", "fill the frame edge to edge",
			"natural scale", "reconstruct the print"} {
			require.NotContainsf(t, low, never, "%d pictures: must not say %q", pictures, never)
		}
		if pictures > 0 {
			require.Contains(t, low, "not necessarily for the colour")
		} else {
			require.Contains(t, low, "no picture is attached")
		}
	}
}

// TestAHardwarePictureTAKES_UP_TO_FOUR_REFERENCES_IN_ONE_CALL.
func TestAHardwarePictureTAKES_UP_TO_FOUR_REFERENCES_IN_ONE_CALL(t *testing.T) {
	for _, refs := range [][]string{nil, {"a"}, {"a", "b", "c", "d"}} {
		calls, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeHardware,
			Prompt: "hw", References: refs})
		require.NoErrorf(t, err, "%d refs", len(refs))
		require.Len(t, calls, 1)
		require.Equal(t, 1, calls[0].n)
		require.Len(t, calls[0].refs, len(refs))
	}
	_, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeHardware,
		References: []string{"a", "b", "c", "d", "e"}})
	require.Error(t, err)
	require.False(t, classify(err).Retryable)
}

// TestTheLabelCraftREPRODUCES_THE_LOGO_IT_IS_GIVEN: mode label writes labelCraft, not hardwareCraft —
// the one picture is the LOGO ARTWORK to reproduce (hardware would say «not necessarily for the
// colour» and exclude any logo), and with no picture the label is blank, no invented wordmark.
func TestTheLabelCraftREPRODUCES_THE_LOGO_IT_IS_GIVEN(t *testing.T) {
	for _, pictures := range []int{0, 1} {
		got := patternCraft(patternParams{Mode: entity.DesignPatternModeLabel, RepeatMM: 80}, pictures)
		low := strings.ToLower(got)
		for _, must := range []string{"garment label:", "plain, seamless pure white background",
			"soft, even studio light", "context only", "never draw the garment"} {
			require.Containsf(t, low, must, "%d pictures: must say %q", pictures, must)
		}
		for _, never := range []string{"hardware item:", "not necessarily for the colour", "logo that the words",
			"repeating tile", "repeat"} {
			require.NotContainsf(t, low, never, "%d pictures: must not say %q", pictures, never)
		}
		if pictures > 0 {
			require.Contains(t, got, "LOGO ARTWORK")
			require.NotContains(t, low, "no logo is given")
		} else {
			require.Contains(t, low, "no logo is given")
			require.NotContains(t, got, "LOGO ARTWORK")
		}
	}
}

// TestALabelPictureTAKES_AT_MOST_ONE_PICTURE: the logo, in one call; two refuse at the money boundary.
func TestALabelPictureTAKES_AT_MOST_ONE_PICTURE(t *testing.T) {
	for _, refs := range [][]string{nil, {"logo"}} {
		calls, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeLabel,
			Prompt: "label", References: refs})
		require.NoErrorf(t, err, "%d refs", len(refs))
		require.Len(t, calls, 1)
		require.Equal(t, 1, calls[0].n)
		require.Len(t, calls[0].refs, len(refs))
	}
	_, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, PatternMode: entity.DesignPatternModeLabel,
		References: []string{"a", "b"}})
	require.Error(t, err)
	require.False(t, classify(err).Retryable)
}
