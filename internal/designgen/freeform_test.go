package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"

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
}

func (f *fakeObjects) GetManagedObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.asked = append(f.asked, key)
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
	require.Contains(t, got[0].caption, "image 1 with area A in RED outlined")
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
	require.Contains(t, job.Prompt, "an outline is a hint about WHERE, not a mask")
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
