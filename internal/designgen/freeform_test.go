package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// fakeObjects — бакет из памяти: ключ → байты. Ключ тот же, что построит
// bucket.ObjectKeyFromStoredURL из url медиа, поэтому проба проверяет и разбор адреса.
type fakeObjects struct {
	byKey map[string][]byte
	asked []string
	// failAfter makes every read past the n-th fail, which is how the SECOND read of one picture is
	// made to fail while the first succeeds — the window's composite reads the original a second
	// time, after the money.
	failAfter int
}

func (f *fakeObjects) GetManagedObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.asked = append(f.asked, key)
	if f.failAfter > 0 && len(f.asked) > f.failAfter {
		return nil, 0, errBoom
	}
	raw, ok := f.byKey[key]
	if !ok {
		return nil, 0, errBoom
	}
	return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
}

// fixturePNG — 100×100 PNG, у которого ЛЕВАЯ ПОЛОВИНА ПРОЗРАЧНА, а правая — сплошной цвет.
// Прозрачность здесь не украшение: она и есть то, что кроп обязан довезти.
func fixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			if x < 50 {
				img.Set(x, y, color.NRGBA{})
				continue
			}
			img.Set(x, y, color.NRGBA{R: 20, G: 200, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func freeformRun(paramsJSON string) entity.DesignRun {
	r := testRun(1, entity.DesignRunKindFreeform)
	r.Params = entity.RawJSON(paramsJSON)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)
	return r
}

// point — одна доля координаты в том виде, в каком её пишет protojson: ОБЪЕКТ со строкой внутри.
func point(x, y string) string {
	return `{"x":{"value":"` + x + `"},"y":{"value":"` + y + `"}}`
}

// TestDeriveFreeformMAKES_TWO_PICTURES_OUT_OF_ONE_MARKED_AREA.
//
// Область — не маска: у платного маршрута поля маски нет вовсе. Она превращается в ДВЕ картинки —
// обведённую копию и кроп — и в подписи к ним, и это единственный способ, которым разметка
// человека вообще доезжает до модели. Проба держит обе половины: и число картинок, и то, что
// кроп PNG-исходника остаётся PNG С АЛЬФОЙ.
func TestDeriveFreeformMAKES_TWO_PICTURES_OUT_OF_ONE_MARKED_AREA(t *testing.T) {
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": fixturePNG(t)}}
	p := parseParams(entity.RawJSON(`{"freeform":{"preset":"free","items":[
	  {"media_id":11,"role":"subject","texts":["the pocket"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.6", "0.6") + `,` + point("0.9", "0.6") + `,` + point("0.9", "0.9") + `,` + point("0.6", "0.9") +
		`]}]}]}}`))
	require.NotNil(t, p.Freeform)
	require.Len(t, p.Freeform.Items, 1)

	attached := []refCaption{{MediaID: 11, Caption: "the picture being worked on"}}
	urls := []string{"https://cdn.example/m/11.png"}

	got, err := deriveFreeform(context.Background(), objs, p, attached, urls)
	require.NoError(t, err)
	require.Len(t, got, 2, "one marked area is one outlined copy plus one crop")
	require.Equal(t, []string{"m/11.png"}, objs.asked, "the key comes out of the stored url")

	// ─── ОБВЕДЁННАЯ КОПИЯ ───
	require.True(t, strings.HasPrefix(got[0].dataURI, "data:image/jpeg;base64,"), got[0].dataURI[:40])
	require.Contains(t, got[0].caption, "image 1 with area A outlined in RED and lettered A",
		"обе половины метки названы словами: цвет — то, что на картинке, буква — то, чем область "+
			"зовётся в разговоре")
	require.Contains(t, got[0].caption, "not part of the garment",
		"without this half the model draws the outline onto the answer")

	// ─── КРОП ОБЛАСТИ ───
	require.True(t, strings.HasPrefix(got[1].dataURI, "data:image/png;base64,"),
		"a crop of a PNG source stays PNG: this is where alpha would die")
	require.Contains(t, got[1].caption, "a close crop of area A of image 1")
	require.Contains(t, got[1].caption, "the pocket", "the words of that area travel with its crop")

	// ⚠ АЛЬФА ПРОВЕРЯЕТСЯ ПИКСЕЛЯМИ, А НЕ ТИПОМ ФАЙЛА. «PNG» не значит «с прозрачностью»: белая
	// подложка под кропом дала бы ровно такой же content type и молча убила бы вырез.
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got[1].dataURI, "data:image/png;base64,"))
	require.NoError(t, err)
	crop, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	require.False(t, crop.Bounds().Empty())
}

// TestFreeformCropOfAnOpaqueSourceIsJPEG — альфы нет, значит и PNG не за что платить весом.
func TestFreeformCropOfAnOpaqueSourceIsJPEG(t *testing.T) {
	img := image.NewYCbCr(image.Rect(0, 0, 80, 80), image.YCbCrSubsampleRatio420)
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	objs := &fakeObjects{byKey: map[string][]byte{"m/12.png": buf.Bytes()}}

	p := parseParams(entity.RawJSON(`{"freeform":{"preset":"free","items":[
	  {"media_id":12,"regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.1", "0.1") + `,` + point("0.4", "0.1") + `,` + point("0.4", "0.4") + `]}]}]}}`))
	got, err := deriveFreeform(context.Background(), objs, p,
		[]refCaption{{MediaID: 12}}, []string{"https://cdn.example/m/12.png"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, strings.HasPrefix(got[1].dataURI, "data:image/jpeg;base64,"))
}

// TestAFreeformRunWithNoObjectStoreREFUSES_RATHER_THAN_DROPPING_THE_MARKUP.
//
// Молчаливая деградация здесь стоила бы полной цены кадра, в котором разметка человека не
// участвовала, — и отличить такой кадр от честного в истории было бы нечем.
func TestAFreeformRunWithNoObjectStoreREFUSES_RATHER_THAN_DROPPING_THE_MARKUP(t *testing.T) {
	p := parseParams(entity.RawJSON(`{"freeform":{"preset":"free","items":[
	  {"media_id":11,"regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.1", "0.1") + `,` + point("0.4", "0.1") + `,` + point("0.4", "0.4") + `]}]}]}}`))
	_, err := deriveFreeform(context.Background(), nil, p,
		[]refCaption{{MediaID: 11}}, []string{"https://cdn.example/m/11.png"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no object store")

	// А БЕЗ ЕДИНОЙ ОБЛАСТИ ХРАНИЛИЩЕ НЕ НУЖНО ВОВСЕ: производить нечего.
	bare := parseParams(entity.RawJSON(`{"freeform":{"preset":"free","items":[{"media_id":11}]}}`))
	got, err := deriveFreeform(context.Background(), nil, bare,
		[]refCaption{{MediaID: 11}}, []string{"https://cdn.example/m/11.png"})
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAFreeformJobIsONE_CALL_OF_ONE_PICTURE — слово владельца, исполненное в коде.
//
// Дверь посчитала цену по тому же числу (designRequestedOutputs), поэтому всякая другая форма
// здесь — либо покупка того, за что не резервировали, либо плитка-плейсхолдер, которую никто не
// заполнит.
func TestAFreeformJobIsONE_CALL_OF_ONE_PICTURE(t *testing.T) {
	job := Job{
		Kind:       entity.DesignRunKindFreeform,
		Prompt:     "do the thing",
		References: []string{"https://a", "https://b", "data:image/jpeg;base64,zz"},
	}
	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Len(t, calls, 1, "many pictures are still ONE ask")
	require.Equal(t, 1, calls[0].n)
	require.Equal(t, job.References, calls[0].refs)
	require.Equal(t, "", backgroundFor(entity.DesignRunKindFreeform),
		"the background of the answer must be the background of the person's own picture")

	_, err = imageCalls(Job{Kind: entity.DesignRunKindFreeform, Prompt: "x"})
	require.Error(t, err, "nothing resolved is a terminal refusal, not a paid call")
}

// TestAFreeformPromptPutsTheAskFirstAndTheCraftLast.
//
// Порядок несущий: слова ближе к концу промпта — те, которым модель подчиняется, поэтому ремесло
// стоит после всего человеческого. А блока «garment:» здесь нет вовсе — карточку этот род не
// читает, и снимок у него пустой по построению (дверь).
func TestAFreeformPromptPutsTheAskFirstAndTheCraftLast(t *testing.T) {
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": fixturePNG(t)}}
	r := freeformRun(`{"freeform":{"preset":"free","items":[
	  {"media_id":11,"role":"subject","texts":["make the collar wider"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.2", "0.2") + `,` + point("0.5", "0.2") + `,` + point("0.5", "0.5") + `]}]}]}}`)
	r.Ask = sql.NullString{String: "widen the collar", Valid: true}
	r.Inputs = entity.RawJSON(`{"garment_note":"","fit":""}`)

	job, err := buildJob(context.Background(), media(11), objs, r, "medium")
	require.NoError(t, err)

	require.Len(t, job.References, 3, "the picture, its outlined copy and the crop of area A")
	require.True(t, strings.HasPrefix(job.References[0], "https://"))
	require.True(t, strings.HasPrefix(job.References[1], "data:image/"))
	require.True(t, strings.HasPrefix(job.References[2], "data:image/"))
	require.Len(t, job.ReferenceViews, 3, "views stay positional with the references")

	require.True(t, strings.HasPrefix(job.Prompt, "widen the collar"), job.Prompt)
	require.NotContains(t, job.Prompt, "garment:")
	require.NotContains(t, job.Prompt, "fit:")
	require.Contains(t, job.Prompt, "- image 1:")
	require.Contains(t, job.Prompt, "- image 2:")
	require.Contains(t, job.Prompt, "- image 3:")
	require.Contains(t, job.Prompt, "an outline and its letter are a hint about WHERE, not a mask")
	require.NotContains(t, job.Prompt, "change nothing else",
		"the soft mask does not keep that promise, and a promise we cannot keep reads as our bug")

	craft := strings.Index(job.Prompt, "Work from the words above")
	refs := strings.Index(job.Prompt, "references:")
	require.Greater(t, craft, refs, "the craft speaks after every human word")
}

// TestTheFrozenFreeformParamsAreREAD_BY_THE_NAMES_THE_DOOR_WRITES.
//
// ⚠ ЭТОТ ЧИТАТЕЛЬ УЗКИЙ И РУЧНОЙ, ЗНАЧИТ ОН МОЖЕТ РАЗОЙТИСЬ С ПИСАТЕЛЕМ МОЛЧА. `media_id`,
// прочитанный как `mediaId`, — это НОЛЬ: картинка не уехала, ошибки нет, прогон успешен. Здесь
// сообщение маршалится ТЕМ ЖЕ protojson с UseProtoNames, которым дверь пишет колонку `params`, и
// разбирается настоящим parseParams: переименование поля в контракте краснит эту пробу вместо
// того, чтобы через месяц дать пустой прогон за полную цену.
func TestTheFrozenFreeformParamsAreREAD_BY_THE_NAMES_THE_DOOR_WRITES(t *testing.T) {
	written := &pb_common.DesignRunParams{
		Freeform: &pb_common.DesignFreeformParams{
			Preset: entity.DesignFreeformPresetAddHardware,
			Items: []*pb_common.DesignFreeformItem{{
				MediaId: 11,
				Role:    entity.DesignFreeformRoleSubject,
				Texts:   []string{"the buckle goes here", "a jacket on a hanger"},
				Regions: []*pb_common.TechCardAnnotation{{
					Kind: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
					Points: []*pb_common.TechCardAnnotationPoint{
						{X: &pb_decimal.Decimal{Value: "0.10"}, Y: &pb_decimal.Decimal{Value: "0.20"}},
						{X: &pb_decimal.Decimal{Value: "0.40"}, Y: &pb_decimal.Decimal{Value: "0.20"}},
						{X: &pb_decimal.Decimal{Value: "0.40"}, Y: &pb_decimal.Decimal{Value: "0.50"}},
					},
				}},
			}},
		},
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(written)
	require.NoError(t, err)

	p := parseParams(entity.RawJSON(raw))
	require.NotNil(t, p.Freeform, "the whole ask fell on the floor")
	require.Equal(t, entity.DesignFreeformPresetAddHardware, p.Freeform.Preset)
	require.Len(t, p.Freeform.Items, 1)
	it := p.Freeform.Items[0]
	require.Equal(t, 11, it.MediaID, "a media id read as zero is a picture that never travels")
	require.Equal(t, entity.DesignFreeformRoleSubject, it.Role)
	require.Equal(t, []string{"the buckle goes here", "a jacket on a hanger"}, it.Texts)
	require.Len(t, it.Regions, 1)
	require.Len(t, it.Regions[0].Points, 3)
	// КООРДИНАТА ПРИЕЗЖАЕТ ОБЪЕКТОМ СО СТРОКОЙ ВНУТРИ, а не числом: прочитанная числом, она была бы
	// нулём, то есть областью в левом верхнем углу — правдоподобной и всегда не той.
	require.InDelta(t, 0.10, it.Regions[0].Points[0].X.f(), 1e-9)
	require.InDelta(t, 0.20, it.Regions[0].Points[0].Y.f(), 1e-9)
}

// TestEveryPhase2ParamsFieldIsREAD_BY_ITS_SNAKE_CASE_NAME [Codex 3].
//
// The reader drops decode mistakes silently, so a mistyped tag is a field that reads zero on a paid
// run. Every phase-2 field is written non-zero through the door's own marshaller and read back.
// The second half marshals with the protojson DEFAULT (camelCase) and requires every multi-word
// field to read zero: proof that the snake_case tags, not luck, carry the value. Single-word keys
// (model, quality, framing, …) are spelled the same both ways and cannot show that.
func TestEveryPhase2ParamsFieldIsREAD_BY_ITS_SNAKE_CASE_NAME(t *testing.T) {
	written := &pb_common.DesignRunParams{
		Image: &pb_common.DesignImageOptions{
			Model:       "openai/gpt-image-2",
			Quality:     "high",
			AspectRatio: "3:4",
			Background:  "transparent",
		},
		Freeform: &pb_common.DesignFreeformParams{
			Preset: entity.DesignFreeformPresetTryon,
			Options: &pb_common.DesignWorkflowOptions{
				Framing:           entity.DesignFramingUpperBody,
				Angle:             entity.DesignAngleLowAngle,
				SceneMode:         entity.DesignSceneModeReference,
				SceneText:         "a concrete stairwell",
				ModelId:           41,
				ProductColorwayId: 42,
				LogoSize:          entity.DesignLogoSizeLarge,
				Creativity:        3,
			},
		},
		Threed: &pb_common.DesignThreedParams{
			ReferenceMediaIds: []int32{71, 72, 73, 74},
			Texture:           "off",
			Pbr:               "on",
			Quality:           "detailed",
			Follow:            "shape",
			SurfaceHint:       "brushed wool, matte",
		},
	}

	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(written)
	require.NoError(t, err)
	p := parseParams(entity.RawJSON(raw))

	require.NotNil(t, p.Image, "the per-run engine fell on the floor")
	require.Equal(t, imageOptions{
		Model: "openai/gpt-image-2", Quality: "high", AspectRatio: "3:4", Background: "transparent",
	}, *p.Image)

	require.NotNil(t, p.Freeform)
	require.NotNil(t, p.Freeform.Options, "the preset's options fell on the floor")
	require.Equal(t, workflowOptions{
		Framing: "upper_body", Angle: "low_angle", SceneMode: "reference", SceneText: "a concrete stairwell",
		ModelID: 41, ProductColorwayID: 42, LogoSize: "large", Creativity: 3,
	}, *p.Freeform.Options)

	require.NotNil(t, p.Threed)
	require.Equal(t, []int{71, 72, 73, 74}, p.Threed.ReferenceMediaIDs, "order is front, back, left, right")
	require.Equal(t, "off", p.Threed.Texture)
	require.Equal(t, "on", p.Threed.PBR)
	require.Equal(t, "detailed", p.Threed.Quality)
	require.Equal(t, "shape", p.Threed.Follow)
	require.Equal(t, "brushed wool, matte", p.Threed.SurfaceHint)

	// The camelCase half: a writer switched to the protojson default must turn every multi-word
	// field into zero here — if one still reads, its tag is not the snake_case name.
	camel, err := protojson.Marshal(written)
	require.NoError(t, err)
	require.Contains(t, string(camel), `"aspectRatio"`, "the fixture must really be camelCase")
	c := parseParams(entity.RawJSON(camel))
	require.NotNil(t, c.Image)
	require.Empty(t, c.Image.AspectRatio)
	require.NotNil(t, c.Freeform)
	require.NotNil(t, c.Freeform.Options)
	require.Empty(t, c.Freeform.Options.SceneMode)
	require.Empty(t, c.Freeform.Options.SceneText)
	require.Zero(t, c.Freeform.Options.ModelID)
	require.Zero(t, c.Freeform.Options.ProductColorwayID)
	require.Empty(t, c.Freeform.Options.LogoSize)
	require.NotNil(t, c.Threed)
	require.Empty(t, c.Threed.ReferenceMediaIDs)
	require.Empty(t, c.Threed.SurfaceHint)
}

// TestAPlaygroundJobCarriesItsPRESET — imageCalls (the money boundary) reads the preset off the
// job, so it must survive buildJob; a non-freeform run carries none.
func TestAPlaygroundJobCarriesItsPRESET(t *testing.T) {
	r := freeformRun(`{"freeform":{"preset":"tryon","items":[{"media_id":11,"role":"model"},{"media_id":12,"role":"product"}]}}`)
	job, err := buildJob(context.Background(), media(11, 12), &fakeObjects{byKey: map[string][]byte{}}, r, "medium")
	require.NoError(t, err)
	require.Equal(t, entity.DesignFreeformPresetTryon, job.FreeformPreset)

	flat := testRun(2, entity.DesignRunKindFlat)
	job, err = buildJob(context.Background(), media(), nil, flat, "medium")
	require.NoError(t, err)
	require.Empty(t, job.FreeformPreset)
}

// TestTheSurfaceSteerCarriesTHE_SURFACE_HINT — the person's own surface words reach the texturing
// stage, after the presentation, inside the same bound; blank words add nothing.
func TestTheSurfaceSteerCarriesTHE_SURFACE_HINT(t *testing.T) {
	ctx := context.Background()
	steer := surfaceSteer(ctx, runParams{Threed: &threedParams{Presentation: "air", SurfaceHint: "  brushed\nwool  "}})
	require.Equal(t, "presentation air; surface: brushed wool", steer)

	require.Equal(t, "", surfaceSteer(ctx, runParams{Threed: &threedParams{SurfaceHint: "   "}}))

	long := strings.Repeat("wool ", 200)
	require.LessOrEqual(t, len([]rune(surfaceSteer(ctx, runParams{Threed: &threedParams{SurfaceHint: long}}))),
		steerCeiling, "the hint is bounded like every other part")
}

// TestFreeformReferencesAreTheItemsAndNothingElse.
//
// Ни плит верстака, ни ссылок карточки, ни тканей рецепта: снимок такого прогона их не носит, но
// проба кормит сборщик снимком, который их НЕСЁТ, — иначе она проверяла бы пустоту, а не правило.
func TestFreeformReferencesAreTheItemsAndNothingElse(t *testing.T) {
	p := parseParams(entity.RawJSON(`{"extra_input_media_ids":[77],"freeform":{"preset":"free",
	  "items":[{"media_id":11,"role":"hardware","texts":["this buckle"]},{"media_id":12}]}}`))
	in := parseInputs(entity.RawJSON(`{"refs":[{"media_id":90,"role":"silhouette"}],
	  "slots":[{"view_key":"front","media_id":91}]}`))

	list := referenceList(entity.DesignRunKindFreeform, p, in)
	require.Len(t, list, 2)
	require.Equal(t, 11, list[0].MediaID)
	require.Equal(t, 12, list[1].MediaID)
	require.Contains(t, list[0].Caption, "the hardware")
	require.Contains(t, list[0].Caption, "this buckle")
	require.Equal(t, "reference image", list[1].Caption,
		"a picture nobody described still gets words, or every caption after it shifts")

	for _, rc := range list {
		require.NotEqual(t, 90, rc.MediaID)
		require.NotEqual(t, 91, rc.MediaID)
		require.NotEqual(t, 77, rc.MediaID)
	}
}

// TestTheAddHardwareCraftNamesBOTH_PICTURES_BY_NUMBER.
//
// «Возьми фурнитуру с той картинки» модель исполнить не может: у неё нет наших слов для «той».
// Номер — единственное, что у нас с ней общее, и он берётся из списка ДОЕХАВШИХ картинок.
func TestTheAddHardwareCraftNamesBOTH_PICTURES_BY_NUMBER(t *testing.T) {
	p := parseParams(entity.RawJSON(`{"freeform":{"preset":"add_hardware","items":[
	  {"media_id":11,"role":"subject"},{"media_id":12,"role":"hardware"}]}}`))
	attached := []refCaption{{MediaID: 11}, {MediaID: 12}}

	craft := freeformCraft(p, attached)
	require.Contains(t, craft, "the hardware shown in image 2")
	require.Contains(t, craft, "onto image 1")
	require.Contains(t, craft, "inside the outlined area")
	require.Contains(t, craft, "Return ONE picture")
	require.NotContains(t, craft, "change nothing else")

	// КАРТИНКА, НЕ ПЕРЕЖИВШАЯ РЕЗОЛВ, НЕ ПОЛУЧАЕТ НОМЕРА: указание в пустоту хуже, чем слова.
	craft = freeformCraft(p, []refCaption{{MediaID: 11}})
	require.NotContains(t, craft, "image 2")
	require.Contains(t, craft, "the picture of the hardware")
}

// TestTheRepaintCraftSaysWholeGarmentWhenNothingIsMarked.
//
// «Перекрась только обведённое» там, где обведённого нет, — инструкция, которую невозможно
// исполнить, и модель ответит на неё чем угодно.
func TestTheRepaintCraftSaysWholeGarmentWhenNothingIsMarked(t *testing.T) {
	bare := parseParams(entity.RawJSON(`{"freeform":{"preset":"repaint_parts","items":[
	  {"media_id":11,"role":"subject"},{"media_id":12,"role":"cloth"}]}}`))
	craft := freeformCraft(bare, []refCaption{{MediaID: 11}, {MediaID: 12}})
	require.Contains(t, craft, "Repaint the whole garment in image 1")
	require.Contains(t, craft, "the cloth shown in image 2")

	marked := parseParams(entity.RawJSON(`{"freeform":{"preset":"repaint_parts","items":[
	  {"media_id":11,"role":"subject","regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON",
	   "points":[` + point("0.1", "0.1") + `,` + point("0.4", "0.1") + `,` + point("0.4", "0.4") + `]}]}]}}`))
	craft = freeformCraft(marked, []refCaption{{MediaID: 11}})
	require.Contains(t, craft, "Repaint ONLY the outlined areas of image 1")
	require.Contains(t, craft, "the colour described above")
}

// ═══════════ БУКВА ОБЛАСТИ ЖИВЁТ В ПИКСЕЛЯХ, А НЕ В ПОДПИСИ ═══════════

// diamondRegion — ромб вокруг центра. Ромб, а не прямоугольник, НАМЕРЕННО: у прямоугольника угол
// bbox лежит ПРЯМО НА КОНТУРЕ, и «в углу есть пиксели цвета» было бы правдой и без всякой буквы —
// проба измеряла бы обводку и была бы зелена при снятой плашке. У ромба угол bbox отстоит от
// ближайшей линии на сотню пикселей: всё, что там найдено, нарисовано плашкой и ничем больше.
func diamondRegion(cx, cy, rx, ry string) freeformRegion {
	f := func(s string) freeformDecimal { return freeformDecimal{Value: s} }
	sum := func(a, b string, sign float64) freeformDecimal {
		av, _ := strconv.ParseFloat(a, 64)
		bv, _ := strconv.ParseFloat(b, 64)
		return freeformDecimal{Value: strconv.FormatFloat(av+sign*bv, 'f', 4, 64)}
	}
	return freeformRegion{
		Kind: "TECH_CARD_ANNOTATION_KIND_POLYGON",
		Points: []freeformPoint{
			{X: f(cx), Y: sum(cy, ry, -1)},
			{X: sum(cx, rx, +1), Y: f(cy)},
			{X: f(cx), Y: sum(cy, ry, +1)},
			{X: sum(cx, rx, -1), Y: f(cy)},
		},
	}
}

func isNear(c color.Color, want color.RGBA, tol int) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, w uint8) int {
		v := int(a>>8) - int(w)
		if v < 0 {
			return -v
		}
		return v
	}
	return d(r, want.R) <= tol && d(g, want.G) <= tol && d(b, want.B) <= tol
}

func isWhitish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r>>8 > 200 && g>>8 > 200 && b>>8 > 200
}

// TestTheAreaLetterIsPRINTED_INTO_THE_PIXELS.
//
// ⚠ ЭТО ОБЯЗАТЕЛЬНАЯ ПОЛОВИНА SET-OF-MARK, А НЕ УКРАШЕНИЕ. Приём (arXiv 2310.11441) состоит из
// границы И ВИДИМОГО ЯРЛЫКА; без ярлыка просьба «сделай это в области B» указывает в никуда —
// модель видит два одинаково обведённых места и слово, которого на картинке нет. Сопоставить «B» с
// синим ей неоткуда, кроме нашей же подписи, то есть текста, спорящего с изображением.
//
// ПРОБА СМОТРИТ НА ПИКСЕЛИ, А НЕ НА ПОДПИСЬ, потому что подпись зелена и при пустой картинке.
// Три утверждения, и каждое проверяет свой отказ:
//
//  1. В углу bbox есть ПЛАШКА цвета контура — снятая плашка красит эту строку.
//  2. ВНУТРИ плашки есть БЕЛЫЕ пиксели — сплошной квадрат без глифа красит эту.
//  3. Верхняя левая клетка глифа у A и B РАЗНАЯ (у «A» она пустая, у «B» залитая) — буква,
//     нарисованная одна и та же для всех областей, красит эту.
func TestTheAreaLetterIsPRINTED_INTO_THE_PIXELS(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1600, 1200))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	regions := []freeformRegion{
		diamondRegion("0.30", "0.20", "0.12", "0.12"),
		diamondRegion("0.70", "0.65", "0.12", "0.12"),
	}
	uri, err := freeformOutlined(src, regions, freeformMaxSide)
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/jpeg;base64,"))
	require.NoError(t, err)
	marked, err := jpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	mb := marked.Bounds()

	for _, c := range []struct {
		colour  color.RGBA
		minX    float64
		minY    float64
		corner  bool // true when the glyph's top-left cell is FILLED ('B'), false when empty ('A')
		letter  string
		nearTol int
	}{
		{freeformOutlineColours[0].rgba, 0.18, 0.08, false, "A", 60},
		{freeformOutlineColours[1].rgba, 0.58, 0.53, true, "B", 60},
	} {
		t.Run(c.letter, func(t *testing.T) {
			x0 := mb.Min.X + int(math.Round(c.minX*float64(mb.Dx()-1)))
			y0 := mb.Min.Y + int(math.Round(c.minY*float64(mb.Dy()-1)))
			win := image.Rect(x0, y0, x0+50, y0+60).Intersect(mb)

			// ─── 1. ПЛАШКА. Её границы находятся сканированием, а не арифметикой из констант:
			// проба обязана увидеть то, что нарисовано, а не пересчитать то, что задумано.
			plate := image.Rectangle{Min: image.Pt(win.Max.X, win.Max.Y), Max: image.Pt(win.Min.X, win.Min.Y)}
			painted := 0
			for y := win.Min.Y; y < win.Max.Y; y++ {
				for x := win.Min.X; x < win.Max.X; x++ {
					if !isNear(marked.At(x, y), c.colour, c.nearTol) {
						continue
					}
					painted++
					plate = plate.Union(image.Rect(x, y, x+1, y+1))
				}
			}
			require.Greater(t, painted, 200,
				"в углу bbox нет плашки цвета области: ромб отстоит от своего угла на сотню "+
					"пикселей, значит красить там больше нечему")

			// ─── 2. БЕЛЫЕ ПИКСЕЛИ ВНУТРИ ПЛАШКИ — это и есть глиф. Отступ в три пикселя от
			// границы плашки снимает звон JPEG на резком крае, а не подгоняет результат: буква
			// стоит в середине, на своём поле шириной в клетку.
			inner := plate.Inset(3)
			require.False(t, inner.Empty())
			white := 0
			for y := inner.Min.Y; y < inner.Max.Y; y++ {
				for x := inner.Min.X; x < inner.Max.X; x++ {
					if isWhitish(marked.At(x, y)) {
						white++
					}
				}
			}
			require.Greater(t, white, 80, "плашка без белых пикселей внутри — это квадрат, а не буква")

			// ─── 3. ЧТО ИМЕННО ЗА БУКВА. Верхняя левая клетка глифа: у «A» пустая (цвет плашки),
			// у «B» залитая (белая). Одна буква на все области прошла бы обе проверки выше.
			scale := plate.Dx() / (freeformGlyphCols + 2)
			require.GreaterOrEqual(t, scale, 2, "масштаб глифа не может быть меньше двух пикселей на клетку")
			cx := plate.Min.X + scale + scale/2
			cy := plate.Min.Y + scale + scale/2
			if c.corner {
				require.True(t, isWhitish(marked.At(cx, cy)),
					"у «B» верхняя левая клетка залита, а здесь она пустая — нарисована не та буква")
			} else {
				require.True(t, isNear(marked.At(cx, cy), c.colour, c.nearTol),
					"у «A» верхняя левая клетка пустая, а здесь она залита — нарисована не та буква")
			}
		})
	}
}

// ═══════════ БАЙТЫ СЧИТАЮТСЯ ДО ДЕНЕГ ═══════════

// TestTheDerivedLadderStepsDownBeforeItRefuses.
//
// Лестница и отказ — ОДНА конструкция с двумя концами, и без каждого из них она вредна. Без ступени
// вниз честный кадр 4000×4000 убивал бы прогон там, где хватило бы 1024. Без отказа в конце
// лестница уводила бы качество вниз молча, и человек платил бы полную цену за кадр, собранный из
// миниатюр.
func TestTheDerivedLadderStepsDownBeforeItRefuses(t *testing.T) {
	fat := strings.Repeat("x", freeformMaxDerivedBytes+1)
	thin := strings.Repeat("x", 32)

	var tried []int
	got, err := freeformWithinBudget("the crop", func(side int) (string, error) {
		tried = append(tried, side)
		if side == freeformMaxSide {
			return fat, nil
		}
		return thin, nil
	})
	require.NoError(t, err)
	require.Equal(t, thin, got)
	require.Equal(t, []int{freeformMaxSide, freeformFallbackSide}, tried,
		"первый размер пробуется первым, ступень вниз — только когда он не влез")

	tried = nil
	_, err = freeformWithinBudget("the crop", func(side int) (string, error) {
		tried = append(tried, side)
		return fat, nil
	})
	require.ErrorIs(t, err, errFreeformJobTooLarge)
	require.Contains(t, err.Error(), "1024", "отказ называет размер, на котором сдался")
	require.Equal(t, []int{freeformMaxSide, freeformFallbackSide}, tried)

	// ─── А ЭТО ВТОРОЙ ПОТОЛОК, И ОН НЕ ВЫВОДИТСЯ ИЗ ПЕРВОГО. Шестнадцать производных, каждая
	// честно под своим потолком, дают двадцать четыре мегабайта в теле одного запроса.
	var b jobBudget
	require.NoError(t, b.add(strings.Repeat("x", freeformMaxJobBytes-1)))
	err = b.add("xx")
	require.ErrorIs(t, err, errFreeformJobTooLarge)
	require.Contains(t, err.Error(), "mark fewer areas")
}

// noisePNG — САМЫЙ ТЯЖЁЛЫЙ PNG, КОТОРЫЙ БЫВАЕТ: шум не сжимается ничем, поэтому его вес это его
// площадь. Один пиксель полупрозрачен, и это не мелочь — из-за него кроп остаётся PNG (альфу надо
// довезти), то есть попадает в самый дорогой из путей.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(2463534242)
	for i := 0; i < len(img.Pix); i += 4 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		img.Pix[i] = uint8(seed)
		img.Pix[i+1] = uint8(seed >> 8)
		img.Pix[i+2] = uint8(seed >> 16)
		img.Pix[i+3] = 0xff
	}
	img.SetNRGBA(0, 0, color.NRGBA{A: 0x80})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestAnOversizedPlaygroundRunREFUSES_BEFORE_ANY_MONEY.
//
// ⚠ ЭТО ПРО ПАМЯТЬ ПРОЦЕССА, А НЕ ПРО ВЕЖЛИВОСТЬ К ПРОВАЙДЕРУ. Производная едет base64 внутри
// JSON-тела: тело собирается целиком, кодируется целиком и держится до ответа — в процессе, у
// которого пол-гигабайта на всё. Воркер, съевший память на своей картинке, уносит с собой чужой
// ОПЛАЧЕННЫЙ прогон из соседней горутины.
//
// Проба держит обе половины отказа: он терминальный (повтор соберёт то же задание из того же
// замороженного снимка) и он БЕСПЛАТНЫЙ — попытка не открыта, провайдер не позван, платить не за
// что.
func TestAnOversizedPlaygroundRunREFUSES_BEFORE_ANY_MONEY(t *testing.T) {
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": noisePNG(t, 4000, 4000)}}
	r := freeformRun(`{"freeform":{"preset":"free","items":[
	  {"media_id":11,"role":"subject","texts":["this pocket"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.05", "0.05") + `,` + point("0.95", "0.05") + `,` + point("0.95", "0.95") + `,` +
		point("0.05", "0.95") + `]}]}]}}`)

	prov := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	st := &fakeStore{}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{Image: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(), r, "tok"))

	require.Empty(t, prov.calls, "ни одного платного вызова: отказ вынесен при сборке задания")
	require.Empty(t, st.started, "и ни одной открытой попытки — StartAttempt резервирует бюджет")
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeJobTooLarge, st.failed[0].ErrorCode)
	require.False(t, st.failed[0].Retryable,
		"снимок заморожен: следующий проход соберёт то же задание и упрётся в тот же потолок")
	require.Contains(t, st.failed[0].LastError, "1024",
		"отказ называет размер, на котором сдался, — человеку иначе нечего уменьшать")
}

// TestAGigapixelSourceIsREFUSED_FROM_ITS_HEADER_BEFORE_ANY_MONEY.
//
// ⚠ БАЙТОВЫЙ ПОТОЛОК ЭТОГО НЕ ЛОВИЛ, И В ЭТОМ ВЕСЬ ДЕФЕКТ. `freeformMaxSourceBytes` меряет СЖАТОЕ
// (64 MiB), а в память ложится РАЗВЁРНУТОЕ: PNG в полторы сотни байт законно объявляет канву в
// гигапиксель, и `png.Decode` попросил бы под неё десятки гигабайт в процессе, у которого
// пол-гигабайта на всё. Умирал бы не этот прогон, а весь воркер вместе с чужими оплаченными
// прогонами в соседних горутинах.
//
// ПРОБА РАЗЛИЧАЮЩАЯ ПО ПОСТРОЕНИЮ: у фикстуры нет растра под объявленную канву (pngHeaderSays), то
// есть `png.Decode` на ней ПАДАЕТ. Значит `source_too_large` невозможно получить, если байты всё
// же декодировали — декодировавшая редакция отдала бы обычную ошибку чтения и другой код.
func TestAGigapixelSourceIsREFUSED_FROM_ITS_HEADER_BEFORE_ANY_MONEY(t *testing.T) {
	bomb := pngHeaderSays(t, 40000, 40000)
	require.Less(t, len(bomb), 4096, "бомба обязана быть маленькой — потолок байтов её не видит")

	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": bomb}}
	r := freeformRun(`{"freeform":{"preset":"free","items":[
	  {"media_id":11,"role":"subject","texts":["this pocket"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.05", "0.05") + `,` + point("0.95", "0.05") + `,` + point("0.95", "0.95") + `,` +
		point("0.05", "0.95") + `]}]}]}}`)

	prov := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	st := &fakeStore{}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{Image: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(), r, "tok"))

	require.Empty(t, prov.calls, "ни одного платного вызова: отказ вынесен при сборке задания")
	require.Empty(t, st.started, "и ни одной открытой попытки — StartAttempt резервирует бюджет")
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeSourceTooLarge, st.failed[0].ErrorCode)
	require.Equal(t, "source_too_large", CodeSourceTooLarge)
	require.False(t, st.failed[0].Retryable,
		"строка медиа неизменна: следующий проход прочитает тот же заголовок")
	require.Contains(t, st.failed[0].LastError, "40000×40000",
		"отказ называет объявленный размер — человеку иначе нечем опознать виновную картинку")

	// ⚠ И ЭТО НЕ job_too_large. Совет у них разный: там «снимите область или уменьшите набор»,
	// здесь «замените вот этот кадр»; одно слово на два совета послало бы человека чинить не то.
	require.NotEqual(t, CodeJobTooLarge, st.failed[0].ErrorCode)
}

// TestAddHardwareWithoutItsHardwareIsREFUSED_NOT_BOUGHT_AS_BEST_EFFORT.
//
// ⚠ ДВЕРЬ ЭТО НЕ ЛОВИЛА, И ПОЙМАТЬ НЕ МОГЛА. Она спрашивает ПАРАМЕТРЫ («названа ли картинка
// фурнитуры»), а между дверью и проходом стоит время: строку медиа законно удаляют, и резолв
// пропускает пропавшую МОЛЧА. `add_hardware`, потерявший фотографию фурнитуры, уезжал провайдеру
// абзацем «возьми фурнитуру с картинки …» БЕЗ ЕДИНОЙ картинки фурнитуры — модель отвечает
// правдоподобным кадром, деньги списаны, а в истории такой прогон неотличим от честного.
//
// Проба ставит ровно этот разрыв: снимок называет 11 (изделие с областью) и 12 (фурнитура), а
// медиа знает только 11.
func TestAddHardwareWithoutItsHardwareIsREFUSED_NOT_BOUGHT_AS_BEST_EFFORT(t *testing.T) {
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": fixturePNG(t), "m/12.png": fixturePNG(t)}}
	r := freeformRun(`{"freeform":{"preset":"add_hardware","items":[
	  {"media_id":11,"role":"subject","texts":["right here"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.6", "0.6") + `,` + point("0.9", "0.6") + `,` + point("0.9", "0.9") + `]}]},
	  {"media_id":12,"role":"hardware"}]}}`)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11},{"media_id":12}]}`)

	prov := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	st := &fakeStore{}
	// МЕДИА ЗНАЕТ ТОЛЬКО 11: строку 12 удалили между снимком и проходом.
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{Image: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(), r, "tok"))

	require.Empty(t, prov.calls, "предпосылка не исполнима — платить не за что")
	require.Empty(t, st.started, "и попытку открывать не за что: StartAttempt резервирует бюджет")
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeSourceGone, st.failed[0].ErrorCode)
	require.Equal(t, "source_gone", CodeSourceGone)
	require.False(t, st.failed[0].Retryable, "удалённая строка медиа не возвращается")
	require.Contains(t, st.failed[0].LastError, "role=hardware",
		"отказ называет ИМЕННО ту половину, которой не стало")

	// ─── КОНТРОЛЬ: тот же прогон с ОБЕИМИ живыми картинками покупается как обычно. Без него проба
	// была бы зелена и у сторожа, отказывающего всякому add_hardware.
	ok := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	okStore := &fakeStore{}
	w2 := testWorker(okStore, media(11, 12), newFakeSink(ContentTypePNG), Providers{Image: ok})
	w2.objects = objs
	require.NoError(t, w2.execute(context.Background(), r, "tok"))
	require.Len(t, ok.calls, 1, "исполнимый add_hardware обязан по-прежнему уезжать провайдеру")
}

// TestAPlaygroundRunWhoseEveryPictureVanishedIsREFUSED — иначе это платная просьба «нарисуй по
// словам», которую дверь отклонила бы как `no_source_picture`.
func TestAPlaygroundRunWhoseEveryPictureVanishedIsREFUSED(t *testing.T) {
	r := freeformRun(`{"freeform":{"preset":"free","items":[{"media_id":11},{"media_id":12}]}}`)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11},{"media_id":12}]}`)

	prov := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	st := &fakeStore{}
	w := testWorker(st, media(), newFakeSink(ContentTypePNG), Providers{Image: prov})
	w.objects = &fakeObjects{byKey: map[string][]byte{}}

	require.NoError(t, w.execute(context.Background(), r, "tok"))
	require.Empty(t, prov.calls)
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeSourceGone, st.failed[0].ErrorCode)

	// ⚠ А ПОТЕРЯ ОДНОЙ ИЗ ДВУХ У `free` — НЕ ОТКАЗ, И ЭТО НАМЕРЕННО. Ссылки там не связаны, и
	// ронять исполнимый прогон за честную деградацию было бы сторожем дороже дыры.
	ok := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	okStore := &fakeStore{}
	w2 := testWorker(okStore, media(11), newFakeSink(ContentTypePNG), Providers{Image: ok})
	w2.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": fixturePNG(t)}}
	require.NoError(t, w2.execute(context.Background(), r, "tok"))
	require.Len(t, ok.calls, 1)
	require.Empty(t, okStore.failed)
}

// TestTheSourceCeilingIsTHE_SAME_NUMBER_EVERYWHERE — потолок исходника плейграунда, потолок
// проверки альфы выреза и потолок бакета это ОДНО число.
//
// Копия разошлась бы молча и в ту сторону, в которую дороже: картинка, которую бакет отказался бы
// хранить, была бы развёрнута в память ради подготовки платного вызова.
func TestTheSourceCeilingIsTHE_SAME_NUMBER_EVERYWHERE(t *testing.T) {
	side, _ := bucket.ImageBudgetCeilings()

	_, err := freeformDecode(pngHeaderSays(t, side+1, 4))
	require.ErrorIs(t, err, errFreeformSourceTooLarge)

	// ЧЕСТНАЯ КАРТИНКА В БЮДЖЕТЕ ЧИТАЕТСЯ КАК ПРЕЖДЕ — иначе потолок был бы заглушкой на весь род.
	img, err := freeformDecode(fixturePNG(t))
	require.NoError(t, err)
	require.Equal(t, 100, img.Bounds().Dx())

	v := classify(err2(errFreeformSourceTooLarge))
	require.False(t, v.Retryable)
	require.Equal(t, CodeSourceTooLarge, v.Code)
	require.Equal(t, entity.DesignAttemptFailed, v.State,
		"денег не потрачено: сборка задания идёт до StartAttempt")
}

// err2 оборачивает сентинел так же, как это делает боевой код (%w плюс цифры): классификатор обязан
// узнавать его СКВОЗЬ обёртку, иначе ветка зелена на голом сентинеле и мертва на том единственном
// значении, которое реально приезжает с маршрута.
func err2(sentinel error) error {
	return fmt.Errorf("%w: its header declares 40000×40000 pixels", sentinel)
}

// TestAlphaIsMEASURED_NOT_ASSUMED_FROM_THE_TYPE.
//
// Тип отвечает на вопрос «МОЖЕТ ли эта картинка нести альфу», и ответ «да» у любого фотоснимка,
// загруженного PNG — то есть у большинства. Кроп такого снимка уезжал PNG'ом: втрое-впятеро больше
// байтов при побайтово одинаковом содержании, и платит за них потолок тела запроса и память
// процесса. При этом НАСТОЯЩАЯ прозрачность обязана остаться PNG — иначе вырезка, положенная в
// плейграунд, теряет ровно то, ради чего её делали.
func TestAlphaIsMEASURED_NOT_ASSUMED_FROM_THE_TYPE(t *testing.T) {
	opaque := image.NewNRGBA(image.Rect(0, 0, 120, 120))
	draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.NRGBA{R: 9, G: 40, B: 200, A: 255}),
		image.Point{}, draw.Src)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, opaque))

	region := diamondRegion("0.5", "0.5", "0.2", "0.2")
	uri, err := freeformCrop(mustDecode(t, buf.Bytes()), region,
		freeformSourceHasAlpha(mustDecode(t, buf.Bytes())), freeformMaxSide)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(uri, "data:image/jpeg;base64,"),
		"PNG без единого прозрачного пикселя — это фотография, и PNG ей не нужен")

	withAlpha := mustDecode(t, fixturePNG(t))
	require.True(t, freeformSourceHasAlpha(withAlpha), "настоящая прозрачность остаётся прозрачностью")
}

func mustDecode(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := freeformDecode(raw)
	require.NoError(t, err)
	return img
}
