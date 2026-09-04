package designgen

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ J-6(в): ПРОМПТ ПАТТЕРНА БОЛЬШЕ НЕ НЕСЁТ ОПИСАНИЕ ИЗДЕЛИЯ ═══════════════════════════════════
//
// ⚠ ГАРАНТИЯ ЖИВЁТ НА ДВОИХ, И ЭТО НАМЕРЕННО. composePrompt пишет `garment:` и `fit:` ПО НАЛИЧИЮ
// ЗНАЧЕНИЯ, а не по роду: он читает замороженный снимок и не имеет права решать за дверь, что тот
// «на самом деле» хотел сказать. Пустоту кладёт дверь (apisrv/admin: designKindReadsTheGarmentNote,
// проба TestASnapshotCarriesTheCardsReferencesONLY_FOR_THE_KINDS_THAT_READ_THE_CARD).
//
// ЭТА ПОЛОВИНА МЕРЯЕТ МЕХАНИЗМ: пустое поле снимка ГАСИТ блок целиком, а не печатает пустой
// заголовок. Без неё «дверь кладёт пустоту» ничего не доказывало бы — заголовок `garment:` с
// пустым телом всё равно был бы словом в промпте.
//
// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ — ТОТ ЖЕ ПРОГОН С НЕПУСТЫМ СНИМКОМ. Без него проба зеленела бы на
// композиторе, который не пишет ни одного блока вовсе.
func TestAnEmptyGarmentNoteWRITES_NO_BLOCK_AT_ALL(t *testing.T) {
	const patternParamsJSON = `{"extra_input_media_ids":[90],"pattern":{"repeat_mm":0,"name":"chevron"}}`

	for _, tc := range []struct {
		name   string
		inputs string
		want   bool
	}{
		{
			name:   "the snapshot a pattern run gets from the door",
			inputs: `{"refs":[{"media_id":90}]}`,
			want:   false,
		},
		{
			name:   "positive control: the same run with the card's words in its snapshot",
			inputs: `{"garment_note":"GARMENT-olive shirt","fit":"FIT-oversized","refs":[{"media_id":90}]}`,
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRun(1, entity.DesignRunKindPattern)
			r.Params = entity.RawJSON(patternParamsJSON)
			r.Inputs = entity.RawJSON(tc.inputs)
			p, in := parseParams(r.Params), parseInputs(r.Inputs)
			got := composePrompt(r, p, in, referenceList(r.Kind, p, in))

			// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ САМОГО ПРОМПТА: он собран и не пуст в обоих случаях.
			require.Contains(t, strings.ToLower(got), "repeating tile")

			if tc.want {
				require.Contains(t, got, "garment:\nGARMENT-olive shirt")
				require.Contains(t, got, "fit:\nFIT-oversized")
				return
			}
			require.NotContains(t, got, "garment:",
				"описание изделия в прогоне, который делает КУСОК ТКАНИ, — это деньги")
			require.NotContains(t, got, "fit:")
		})
	}
}

// ═══ J-12: РЕМЕСЛО ПЛИТКИ ══════════════════════════════════════════════════════════════════════

// ИСХОДНИК — ФОТОГРАФИЯ МЯТОЙ ТКАНИ, А НЕ ПЛИТКА, И ЭТО НАДО СКАЗАТЬ.
//
// Владелец описал вход дословно: «я могу дать картинку на которой может быть какая-то ткань
// например помятая или что-то еще с ней она не как прямо паттерн нам надо через аи ее превратить в
// реальный паттерн». Прежний абзац говорил только «возьми мотив и не бери кроп» — под такой
// инструкцией складки приезжают вместе с мотивом и замощаются в решётку складок.
//
// И ЧИСЛО РАППОРТА УШЛО С ЭКРАНА, А ВОПРОС ОСТАЛСЯ. При 0 модели говорится, ОТКУДА взять плотность
// и КАК ошибиться нельзя; при легаси-числе — прежняя фраза о масштабе, слово в слово.
func TestThePatternCraftRECONSTRUCTS_THE_PRINT_AND_ANSWERS_THE_SCALE(t *testing.T) {
	for _, tc := range []struct {
		name     string
		p        patternParams
		contains []string
		absent   []string
	}{
		{
			name: "no repeat stated — the ordinary case after round 15",
			p:    patternParams{},
			contains: []string{
				"reconstruct the print", "pressed flat", "crumpled",
				"choose the size of the repeat yourself",
			},
			absent: []string{"mm repeat on the finished garment"},
		},
		{
			name: "a legacy run that still states its number",
			p:    patternParams{RepeatMM: 120},
			contains: []string{
				"reconstruct the print", "crumpled",
				"draw the motif at the scale of a 120 mm repeat",
			},
			absent: []string{"choose the size of the repeat yourself"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			low := strings.ToLower(patternCraft(tc.p))
			for _, must := range tc.contains {
				require.Containsf(t, low, must, "the tile craft must say %q", must)
			}
			for _, never := range tc.absent {
				require.NotContainsf(t, low, never, "the tile craft must not say %q here", never)
			}
			// ⚠ ЧЕТЫРЕ ПЕРВЫХ АБЗАЦА НЕ ТРОНУТЫ. Стык, исключения, ровное поле и свет верны и
			// остаются словом в слово: волна меняла ПЯТЫЙ абзац и добавляла шестой.
			for _, kept := range []string{
				"right edge", "left edge", "border", "vignette", "no single focal object",
				"lighting of the source",
			} {
				require.Containsf(t, low, kept, "the wrap half of the craft must not have moved (%q)", kept)
			}
		})
	}
}

// ═══ J-12: ПЛИТКА НА РЕНДЕРЕ — ИМЕНЕМ И КАК ПЛИТКА ═════════════════════════════════════════════

// ⚠ ЭТО ДОКАЗАТЕЛЬСТВО «ПО ПОСТРОЕНИЮ», А НЕ УТВЕРЖДЕНИЕ О НЁМ.
//
// Новый абзац одноклоточного рендера включается ровно при `fabrics[0].kind == "pattern"`. Значит
// вопрос «сдвинутся ли шесть замороженных голденов» — это вопрос «несёт ли хоть один замороженный
// прогон поле kind», и на него можно ОТВЕТИТЬ, а не понадеяться: поле заведено этой волной, и ни
// одна замороженная строка params его не содержит. Проба читает те самые литералы, из которых
// собраны голдены.
func TestNoFrozenSingleClothRunCanREACH_THE_PATTERN_PARAGRAPH(t *testing.T) {
	for _, c := range singleClothRuns {
		require.NotContainsf(t, c.params, `"kind"`,
			"случай %q несёт kind — тогда новый абзац мог бы сдвинуть замороженный голден", c.name)
	}
	// И ОБРАТНОЕ: пустой kind читается как «ткань», а не как «неизвестно».
	require.False(t, clothIsAPattern(fabricUse{}))
	require.False(t, clothIsAPattern(fabricUse{Kind: "fabric"}))
	require.True(t, clothIsAPattern(fabricUse{Kind: "pattern"}))
}

// goldenOnePatternlessCloth — ПРОМПТ ОДНОКЛОТОЧНОГО РЕНДЕРА, СНЯТЫЙ С БАЗОВОГО ДЕРЕВА (170091f)
// И ВСТАВЛЕННЫЙ СЮДА ЛИТЕРАЛОМ.
//
// ⚠ ЛИТЕРАЛ, А НЕ ВТОРОЙ ВЫЗОВ ТОГО ЖЕ КОМПОЗИТОРА, И ЭТО ПОЧИНКА РЕВЬЮ. Первая редакция этой
// пробы сравнивала ДВА промпта одного и того же дерева — «с kind» и «без kind» — за вычетом двух
// известных вставок. Утверждение «ничего не сдвинулось» она при этом не проверяла ВОВСЕ: сдвинь
// эта же волна renderFabricParagraph, обе половины уехали бы вместе, и проба осталась бы зелёной.
// Ровно та форма «двух согласованных носителей», за которую этот репозиторий уже платил.
//
// Снято через `go test -overlay`, подменив семь файлов designgen блобами базового коммита (память
// `measure-base-tree-with-go-overlay`): в дерево при этом не записано ни байта, а значение здесь —
// то, что печатал композитор ДО волны. Дисциплина та же, что у шести голденов renderfabric_test.go,
// и по той же причине: собранный из production-констант голден переписывается той же правкой, что
// и промпт, и продолжает зеленеть.
const goldenOnePatternlessCloth = "ASK-the words of the person\n\ncolour:\ncolourway RED-01 — the exact value is #b1121a\n\nreferences:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view\n- image 3: fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here\n\nTurn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph.\n\nFabric. The cloth of this garment is stated in more than one way at once, and the statements may disagree. Resolve every disagreement in this fixed order of authority, the same way on every run:\n1. THE FABRIC PHOTOGRAPH (image 3) governs the MATERIAL of this garment: weave or knit structure, surface texture, pile, sheen, transparency, weight and the way the cloth drapes and folds. Read the cloth from that image and from nothing else.\n2. THE STATED COLOUR — the `colour` block above — governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph. Where the two disagree, keep the photograph's material and re-colour it: the garment is the stated colour even when the swatch photograph is another one.\n\nLayout: a single view — FRONT — the garment photographed once, isolated and centred on the canvas.\n\nStyle: photorealistic product photograph of the finished garment in real cloth, shown AS IF WORN — the fabric holds the volume of a body underneath it: chest, waist and hip shaping, sleeves and straps carrying their own weight, soft natural folds where the cloth falls, and the garment's own soft self-shadow inside those folds. There is no person, no body part, no mannequin, no dress form, no hanger and no visible support of any kind: the garment holds its shape in empty space. Even, soft, diffuse frontal studio light; no cast shadow on the background, no hot highlights, no vignette.\n\nThe material must read as real cloth: the weave or knit structure visible at close range, the fibre's own sheen or matte finish, sheer or open fabric slightly translucent where it overlaps itself, hems, bindings and edges finished the way a sewn edge is finished, seams and topstitching soft and pressed rather than drawn as lines.\n\nStrictly excluded: any human body or body part, skin, hair, face, hands, mannequin, dress form, bust, hanger, rail, stand or clip; background objects, props, furniture, floor, wall, horizon line or scenery of any kind; drop shadow or reflection on the background; text, labels, watermarks, logos, measurements, callouts, arrows, dimension lines or drawn outlines of any kind; any colour, print or pattern that neither the fabric photograph nor the stated colour calls for.\n\nOutput: high resolution, sharp focus across the whole garment, seamless pure white background, true colour, e-commerce product photography aesthetic."

// ОДНОКЛОТОЧНЫЙ РЕНДЕР ПЛИТКИ: старый абзац ПЕРВЫМ, слово в слово, новый — ВТОРЫМ.
//
// Порядок несущий: абзац, который говорит КАК кладут ткань, разрешает споры между предложениями о
// том, ЧТО она такое, — значит стоит после них (тот же довод, по которому ремесло стоит последним).
func TestAOneClothRenderOfAPatternADDS_A_PARAGRAPH_AND_MOVES_NOTHING(t *testing.T) {
	const plain = `{"views":["front"],"layout":"per_view","colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
		`"fabrics":[{"asset_id":4,"name":"floral","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a"}]}}`
	const tile = `{"views":["front"],"layout":"per_view","colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
		`"fabrics":[{"asset_id":4,"name":"floral","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a","kind":"pattern"}]}}`

	plainPrompt := renderPrompt(t, plain, renderSlots)
	tilePrompt := renderPrompt(t, tile, renderSlots)

	// ─── ПЕРВОЕ УТВЕРЖДЕНИЕ И ГЛАВНОЕ: ОБЫЧНАЯ ТКАНЬ ГОВОРИТ РОВНО ТО, ЧТО ГОВОРИЛА ДО ВОЛНЫ ───
	require.Equal(t, goldenOnePatternlessCloth, plainPrompt,
		"промпт одноклоточного рендера заморожен в истории и не смеет сдвинуться ни на байт")
	require.NotEqual(t, plainPrompt, tilePrompt, "положительный контроль: поле kind вообще читается")

	// ─── ВТОРОЕ: ПЛИТКА ОТЛИЧАЕТСЯ РОВНО ДВУМЯ ИМЕНОВАННЫМИ ВСТАВКАМИ И БОЛЬШЕ НИЧЕМ ───
	const oldCaption = "fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here"
	const newCaption = "pattern tile «floral» — a seamless repeat tile of the print this garment is made in: " +
		"read the motif and its colours from here; it is not a photograph of the garment's cloth"
	require.Contains(t, goldenOnePatternlessCloth, oldCaption)
	require.Contains(t, tilePrompt, newCaption)

	para := renderPatternClothParagraph(
		fabricUse{Name: "floral", MediaID: 9, Kind: entity.DesignAssetKindPattern},
		[]refCaption{{MediaID: 1}, {MediaID: 2}, {MediaID: 9}})
	require.Contains(t, tilePrompt, para)
	require.Contains(t, para, "Image 3 is its seamless repeat tile")
	require.Contains(t, para, "natural garment scale taken from the size of the motif")

	stripped := strings.Replace(tilePrompt, "\n\n"+para, "", 1)
	stripped = strings.Replace(stripped, newCaption, oldCaption, 1)
	require.Equal(t, goldenOnePatternlessCloth, stripped,
		"кроме подписи картинки и нового абзаца в одноклоточном рендере не смеет сдвинуться НИЧЕГО")

	// ЛЕГАСИ-ЧИСЛО ГОВОРИТСЯ ТЕМИ ЖЕ СЛОВАМИ, ЧТО У renderClothLine — один раппорт одной фразой.
	withRepeat := renderPatternClothParagraph(
		fabricUse{Name: "floral", MediaID: 9, Kind: entity.DesignAssetKindPattern, RepeatMM: 120},
		[]refCaption{{MediaID: 9}})
	require.Contains(t, withRepeat, "Its pattern repeats every 120 mm on the finished garment.")
	require.NotContains(t, withRepeat, "natural garment scale")

	// ПЛИТКА, ЧЬЯ КАРТИНКА НЕ ДОЕХАЛА, НЕ ПОЛУЧАЕТ НОМЕРА и не приглашает выдумать принт.
	lost := renderPatternClothParagraph(
		fabricUse{Name: "floral", MediaID: 9, Kind: entity.DesignAssetKindPattern}, nil)
	require.Contains(t, lost, "do not invent a print")
	require.NotContains(t, lost, "image ")
}

// МНОГОКЛОТОЧНАЯ СТРОКА ПЛИТКИ ГОВОРИТ «РАЗЛОЖИ», А НЕ «ПРОЧИТАЙ ДРАП».
func TestAPatternClothInAMultiClothRunIS_LAID_OUT_NOT_READ(t *testing.T) {
	attached := []refCaption{{MediaID: 9}, {MediaID: 10}}
	plain := renderClothLine(1, fabricUse{Name: "jersey", MediaID: 9}, attached, false, false)
	tile := renderClothLine(2, fabricUse{Name: "floral", MediaID: 10, Kind: entity.DesignAssetKindPattern}, attached, false, false)

	require.Contains(t, plain, "Its texture is image 1: read this cloth's weave")
	require.Contains(t, tile, "Its picture is image 2, a seamless repeat tile of its print")
	require.NotContains(t, tile, "read this cloth's weave")

	// И БЕЗ КАРТИНКИ МОЛЧАНИЕ СКАЗАНО, А НЕ ОСТАВЛЕНО ПУСТЫМ — иначе модель прочитает мотив с
	// фотографии соседа.
	mute := renderClothLine(2, fabricUse{Name: "floral", Kind: entity.DesignAssetKindPattern}, attached, false, false)
	require.Contains(t, mute, "No picture of this pattern was sent")
}

// ═══ J-31: ТКАНЬ ЕДЕТ ВТОРОЙ КАРТИНКОЙ В КАЖДЫЙ ВЫЗОВ ══════════════════════════════════════════

// TestARecolourIsONE_CALL_PER_PHOTOGRAPH_SHOWING_ITS_OWN_AND_THE_CLOTH.
//
// ⚠ ЧИСЛО ВЫЗОВОВ НЕ МЕНЯЕТСЯ, И ЭТО ДЕНЬГИ. Резерв и requested_outputs посчитаны у двери по числу
// ФОТОГРАФИЙ; ткань, попавшая в References, купила бы лишнюю картинку молча. Поэтому она едет
// отдельным списком и попадает ВТОРОЙ картинкой в каждый из тех же N вызовов.
func TestARecolourIsONE_CALL_PER_PHOTOGRAPH_SHOWING_ITS_OWN_AND_THE_CLOTH(t *testing.T) {
	photos := []string{"https://cdn/on-model-1.png", "https://cdn/on-model-2.png", "https://cdn/on-model-3.png"}
	for _, tc := range []struct {
		name   string
		cloths []string
		want   int
	}{
		{"no cloth — the route as it was before J-31", nil, 1},
		{"one cloth travels with every photograph", []string{"https://cdn/tile.png"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := imageCalls(Job{
				Kind: entity.DesignRunKindRecolor, Prompt: "recolour it",
				References: photos, ClothReferences: tc.cloths,
			})
			require.NoError(t, err)
			require.Len(t, calls, 3, "три фотографии — три платных правки, ткань вызова не заводит")
			for i, c := range calls {
				require.Equal(t, 1, c.n)
				require.Lenf(t, c.refs, tc.want, "call %d", i)
				require.Equal(t, photos[i], c.refs[0], "фотография этого вызова стоит первой")
				if tc.want > 1 {
					require.Equal(t, tc.cloths[0], c.refs[1])
				}
			}
			// СРЕЗЫ У ВЫЗОВОВ РАЗНЫЕ: общий префикс переписывался бы следующей итерацией.
			if len(calls) > 1 {
				calls[0].refs[0] = "MUTATED"
				require.Equal(t, photos[1], calls[1].refs[0])
			}
		})
	}
}

// ТКАНЬ ДОЕЗЖАЕТ ИЗ ЗАМОРОЖЕННОГО РЕЦЕПТА, А ПЛИТА ВЕРСТАКА И ССЫЛКА КАРТОЧКИ — ПО-ПРЕЖНЕМУ НЕТ.
//
// ⚠ ОТБОР ТКАНЕЙ ИДЁТ ДО СУЖЕНИЯ СПИСКА, и это единственный порядок, при котором он что-то
// находит: sourcePictures оставляет только названные фотографии, то есть выбрасывает и плитку.
// Мутация «сначала сузить» даёт пустой ClothReferences при зелёном всём остальном.
func TestARecolourSENDS_ITS_CLOTH_AND_STILL_NOTHING_FROM_THE_CARD(t *testing.T) {
	run := entity.DesignRun{
		Id: 5, TechCardId: 41, Kind: entity.DesignRunKindRecolor,
		Params: rawJSON(t, map[string]any{
			"extra_input_media_ids": []int{77, 78},
			"colour": map[string]any{
				"code": "OLV", "fabric_media_id": 9,
				"fabrics": []map[string]any{
					{"asset_id": 4, "name": "floral", "media_id": 9, "repeat_mm": 120, "kind": "pattern"},
				},
			},
		}),
		Inputs: rawJSON(t, map[string]any{
			"garment_note": "a shirt",
			"slots":        []map[string]any{{"view_key": "front", "media_id": 11}},
			"refs":         []map[string]any{{"media_id": 13, "note": "mood"}},
		}),
	}
	job, err := buildJob(context.Background(), media(9, 11, 13, 77, 78), run, "medium")
	require.NoError(t, err)

	require.Len(t, job.References, 2, "плита и настроение здесь по-прежнему ни при чём")
	require.Contains(t, job.References[0], "/77.")
	require.Contains(t, job.References[1], "/78.")
	require.Len(t, job.ClothReferences, 1, "ткань рецепта обязана доехать")
	require.Contains(t, job.ClothReferences[0], "/9.")
	for _, u := range append(append([]string{}, job.References...), job.ClothReferences...) {
		require.NotContains(t, u, "/11.", "плита верстака")
		require.NotContains(t, u, "/13.", "ссылка карточки")
	}
}

// TestARecolourWithAClothSAYS_RE_CLOTH_AND_NAMES_THE_REPEAT.
//
// Два ремесла, и они противоречат друг другу построчно: одно требует СОХРАНИТЬ мотив, другое —
// ЗАМЕНИТЬ ткань. Прогон, взявший оба, кончился бы тем, что написано ниже.
func TestARecolourWithAClothSAYS_RE_CLOTH_AND_NAMES_THE_REPEAT(t *testing.T) {
	withCloth := runParams{Colour: &colourRecipe{
		Fabrics: []fabricUse{{Name: "floral", MediaID: 9, RepeatMM: 120, Kind: entity.DesignAssetKindPattern}},
	}}
	// ⚠ ТКАНЬ БЕЗ КАРТИНКИ НЕ ПЕРЕКЛЮЧАЕТ РЕМЕСЛО: класть нечего, и «image 2» указывало бы в
	// пустоту. Дверь такой прогон отказывает (`cloth_without_picture`), но снимок мог быть
	// заморожен до неё.
	wordsOnly := runParams{Colour: &colourRecipe{Fabrics: []fabricUse{{Name: "floral", RepeatMM: 120}}}}

	got := recolorCraft(withCloth)
	low := strings.ToLower(got)
	require.Contains(t, low, "re-cloth, not re-photograph")
	require.Contains(t, low, "image 1")
	require.Contains(t, low, "image 2")
	require.Contains(t, got, "Its pattern repeats every 120 mm on the finished garment.")
	require.NotContains(t, low, "keep the pattern's motif",
		"«сохрани принт» прямо противоречит «положи новую ткань»")
	require.NotContains(t, low, "recolour, not re-photograph")
	// ВЕСЬ СПИСОК ЗАПРЕТОВ ПЕРЕЕХАЛ ЦЕЛИКОМ: это и есть содержание всей секции ON MODEL.
	for _, must := range []string{"pose", "background", "lighting", "crop", "strictly excluded", "seams"} {
		require.Containsf(t, low, must, "the re-cloth craft must pin %q too", must)
	}

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ — ветка без картинки ткани осталась прежней, слово в слово.
	old := recolorCraft(wordsOnly)
	require.Contains(t, strings.ToLower(old), "recolour, not re-photograph")
	require.Contains(t, strings.ToLower(old), "keep the pattern's motif")
	require.NotContains(t, strings.ToLower(old), "re-cloth")
	require.Equal(t, recolorCraft(runParams{}), strings.TrimSuffix(old,
		"\nThe garment carries a print or a woven pattern. Keep the pattern's motif, its "+
			"scale and its placement on the body exactly as they are; recolour it, do not redraw it."),
		"ветка без картинки ткани — прежний текст плюс прежнее предложение о принте, и ничего больше")
}

// TestARecolourCaptionsNUMBER_ONE_PHOTO_AND_THE_CLOTH_NOT_N_PHOTOS.
//
// ⚠ ЭТО НЕ КОСМЕТИКА ДАЖЕ БЕЗ ТКАНИ. При трёх снимках промпт говорил «- image 1 … - image 3», а
// КАЖДЫЙ вызов держал одну картинку: модель получала протокол нумерации, к которому нечего
// приложить. С тканью числа перестают быть украшением и начинают указывать — «the garment made of
// the cloth in image 2».
func TestARecolourCaptionsNUMBER_ONE_PHOTO_AND_THE_CLOTH_NOT_N_PHOTOS(t *testing.T) {
	mk := func(withCloth bool) string {
		colour := map[string]any{"code": "OLV"}
		ids := []int{77, 78, 79}
		resolvable := []int{77, 78, 79}
		if withCloth {
			colour["fabric_media_id"] = 9
			colour["fabrics"] = []map[string]any{{"name": "floral", "media_id": 9, "kind": "pattern"}}
			resolvable = append(resolvable, 9)
		}
		run := entity.DesignRun{
			Id: 5, TechCardId: 41, Kind: entity.DesignRunKindRecolor,
			Ask:    sql.NullString{String: "ASK", Valid: true},
			Params: rawJSON(t, map[string]any{"extra_input_media_ids": ids, "colour": colour}),
			Inputs: rawJSON(t, map[string]any{}),
		}
		job, err := buildJob(context.Background(), media(resolvable...), run, "medium")
		require.NoError(t, err)
		require.Len(t, job.References, 3, "три снимка — три платных вызова, что бы ни было в подписях")
		return job.Prompt
	}

	for _, tc := range []struct {
		name  string
		cloth bool
		lines int
	}{
		{"three photographs and no cloth", false, 1},
		{"three photographs and one cloth", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mk(tc.cloth)
			require.Equal(t, tc.lines, strings.Count(got, "- image "),
				"подписи описывают ОДИН вызов, а вызовов три одинаковых по форме")
			require.Contains(t, got, "- image 1: the photograph being recoloured")
			if tc.cloth {
				require.Contains(t, got, "- image 2: pattern tile")
			}
			require.NotContains(t, got, "- image 3:",
				"третьей картинки не бывает ни в одном вызове перекраса")
		})
	}
}
