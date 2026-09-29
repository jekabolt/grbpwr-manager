package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/status"
)

// ─────────────────────── фикстуры формы ───────────────────────

func ffPoint(x, y string) *pb_common.TechCardAnnotationPoint {
	return &pb_common.TechCardAnnotationPoint{
		X: &pb_decimal.Decimal{Value: x},
		Y: &pb_decimal.Decimal{Value: y},
	}
}

func ffRegion(points ...*pb_common.TechCardAnnotationPoint) *pb_common.TechCardAnnotation {
	if len(points) == 0 {
		points = []*pb_common.TechCardAnnotationPoint{
			ffPoint("0.1", "0.1"), ffPoint("0.4", "0.1"), ffPoint("0.4", "0.4"),
		}
	}
	return &pb_common.TechCardAnnotation{
		Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
		Points: points,
	}
}

func ffParams(preset string, items ...*pb_common.DesignFreeformItem) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{
		Freeform: &pb_common.DesignFreeformParams{Preset: preset, Items: items},
	}
}

// ffReason — машинная причина отказа, ради которой их и заводят: клиент разбирает слово, а не
// английскую фразу.
func ffReason(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "a door refusal is always a status: %v", err)
	for _, d := range st.Details() {
		if info, ok := d.(interface{ GetReason() string }); ok {
			return info.GetReason()
		}
	}
	return ""
}

// TestThePlaygroundKindsAreLIVE_AND_NEVER_FALL_INTO_FLAT.
//
// ⚠ `DesignPictureKindOfRun` РОНЯЕТ НЕЗНАКОМЫЙ РОД ВО `flat` СВОИМ `default:` — молча и
// правдоподобно. Кадр плейграунда, названный флэтом, попадает в список, из которого кадр ставят в
// СЛОТ ВЕРСТАКА: «перёд изделия» становится вольной картинкой или вырезкой с прозрачным фоном.
// Названный рендером — открывает ворота «3D только после fabric render» на карточке, где вещь
// никто не рисовал. Проба держит оба «не».
func TestThePlaygroundKindsAreLIVE_AND_NEVER_FALL_INTO_FLAT(t *testing.T) {
	for _, kind := range []string{entity.DesignRunKindFreeform, entity.DesignRunKindCutout} {
		require.Truef(t, entity.IsDesignRunKind(kind), "kind %q is not in the vocabulary", kind)
		got := entity.DesignPictureKindOfRun(kind)
		require.Equalf(t, kind, got, "the run kind and its picture kind share the word")
		require.NotEqualf(t, entity.DesignPictureKindFlat, got, "kind %q fell into flat", kind)
		require.NotEqualf(t, entity.DesignPictureKindRender, got, "kind %q would open the 3D gate", kind)
		require.Truef(t, entity.IsDesignPictureKind(got), "picture kind %q is not in the vocabulary", got)
		// ⚠ И НИ ОДИН ИЗ ДВУХ НЕ РОД ВЕРСТАКА: верстак держит СОСТОЯНИЕ ИЗДЕЛИЯ по сторонам.
		require.Falsef(t, entity.IsDesignBenchKind(got), "kind %q must not be a bench axis", got)
		// ОСИ КОЛОРВЕЯ У НИХ НЕТ: клиент шлёт colorway_id=0, стор отказывает всему прочему.
		require.Falsef(t, entity.DesignRunKindTakesColorway(kind), "kind %q has no colourway axis", kind)
		require.Falsef(t, entity.DesignPictureKindTakesColorway(got), "picture %q has no colourway axis", got)
		// КАРТОЧКУ НЕ ЧИТАЮТ ВОВСЕ — ни верстака и ссылок, ни описания изделия.
		require.Falsef(t, designKindReadsTheCard(kind), "kind %q must not read the card", kind)
		require.Falsef(t, designKindReadsTheGarmentNote(kind), "kind %q must not read the garment note", kind)
		// ⚠ ОДИН КАДР НА ПРОГОН — СЛОВО ВЛАДЕЛЬЦА, И СПРАШИВАЕТСЯ ОНО НА ПАРАМЕТРАХ, КОТОРЫЕ У
		// ЛЮБОГО ДРУГОГО РОДА ДАЛИ БЫ ДВА. `per_view` с двумя видами — это два платных вызова у
		// рендера; у плейграунда вызов один, потому что все его картинки про ОДНУ просьбу.
		perView := ffParams("free")
		perView.Layout = designLayoutPerView
		perView.Views = []string{entity.DesignViewFront, entity.DesignViewBack}
		require.Equalf(t, 1, designRequestedOutputs(kind, perView), "kind %q asks for one picture", kind)
		require.Equal(t, 2, designRequestedOutputs(entity.DesignRunKindRender, perView),
			"the same params on a render are two: this is what makes the line above a claim")
	}
}

// TestEveryPlaygroundRefusalHappensBEFORE_ANY_MONEY.
//
// Каждый из этих отказов — форма запроса, а не состояние системы, и все они стоят у двери до
// StartRun, который резервирует бюджет той же транзакцией, что вставляет строку. Проба
// перечисляет их по машинным словам: именно их разбирает клиент.
func TestEveryPlaygroundRefusalHappensBEFORE_ANY_MONEY(t *testing.T) {
	subject := &pb_common.DesignFreeformItem{MediaId: 11, Role: entity.DesignFreeformRoleSubject}

	t.Run("freeform on a foreign kind", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindRender, ffParams("free", subject))
		require.Equal(t, "freeform_forbidden", ffReason(t, err))
	})
	t.Run("unknown preset", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform, ffParams("make_it_pop", subject))
		require.Equal(t, "unknown_preset", ffReason(t, err))
	})
	t.Run("unknown role", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11, Role: "muse"}))
		require.Equal(t, "unknown_role", ffReason(t, err))
	})
	t.Run("a region that is not a polygon", func(t *testing.T) {
		pin := &pb_common.TechCardAnnotation{
			Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
			Points: []*pb_common.TechCardAnnotationPoint{ffPoint("0.2", "0.2")},
		}
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{pin}}))
		require.Equal(t, "region_not_a_polygon", ffReason(t, err))
	})
	t.Run("a coordinate outside the picture", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.1", "0.1"), ffPoint("1.4", "0.1"), ffPoint("0.4", "0.4"))}}))
		require.Error(t, err)
		require.Contains(t, err.Error(), "fraction of the picture from 0 to 1")
	})
	// ⚠ ТРИ ТОЧКИ — ЕЩЁ НЕ МНОГОУГОЛЬНИК, И СЧЁТА ТОЧЕК ЗДЕСЬ БЫЛО НЕДОСТАТОЧНО. Совпавшие и
	// лежащие на одной прямой точки проходили ВСЮ проверку формы: их ровно три, каждая в 0..1, вид
	// POLYGON. Дальше они работали как настоящая область — открывали ворота `add_hardware`
	// («картинка размечена»), брали окно генерации, давали кроп нулевой ширины, растянутый до
	// 1024 px, — и за это платил человек.
	t.Run("three identical points", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.2", "0.2"), ffPoint("0.2", "0.2"), ffPoint("0.2", "0.2"))}}))
		require.Equal(t, "region_degenerate", ffReason(t, err))
		require.Contains(t, err.Error(), "repeats point",
			"совпавшая вершина чинится удалением точки, а не растягиванием области — и отказ говорит именно это")
	})
	t.Run("three collinear points", func(t *testing.T) {
		// Ни одна пара не совпадает, поэтому ловит это ТОЛЬКО площадь.
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.1", "0.1"), ffPoint("0.3", "0.3"), ffPoint("0.5", "0.5"))}}))
		require.Equal(t, "region_degenerate", ffReason(t, err))
		require.Contains(t, err.Error(), "one straight line")
	})
	t.Run("a quadrilateral with one dead vertex", func(t *testing.T) {
		// Площадь настоящая — ловит только проверка повтора соседей.
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.1", "0.1"), ffPoint("0.5", "0.1"),
					ffPoint("0.5", "0.1"), ffPoint("0.5", "0.5"))}}))
		require.Equal(t, "region_degenerate", ffReason(t, err))
	})
	t.Run("a sliver thinner than the threshold", func(t *testing.T) {
		// Полоска 0.4 × 0.00001 = 4e-6 долей², то есть вдвое с лишним ниже порога.
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.1", "0.1"), ffPoint("0.5", "0.1"),
					ffPoint("0.5", "0.10001"), ffPoint("0.1", "0.10001"))}}))
		require.Equal(t, "region_degenerate", ffReason(t, err))
	})
	t.Run("an ordinary area is STILL LEGAL", func(t *testing.T) {
		// ⚠ БЕЗ ЭТОЙ ПОЛОВИНЫ ПРОБА БЫЛА БЫ ЗЕЛЕНА И У СТОРОЖА, ОТКАЗЫВАЮЩЕГО ВСЕМУ.
		require.NoError(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion()}})),
			"треугольник 0.3×0.3 — обычная разметка пальцем")

		// И САМАЯ МАЛЕНЬКАЯ ЗАКОННАЯ: квадрат ровно в порог. Порог назван литералом там, где он
		// объявлен, поэтому проба меряет ЧИСЛО, а не «что-нибудь ненулевое».
		require.Equal(t, "0.00001", designFreeformMinRegionArea,
			"порог решает, какую разметку человек может нарисовать, — он решение, а не дрейф")
		require.NoError(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform,
			ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion(
					ffPoint("0.1", "0.1"), ffPoint("0.11", "0.1"),
					ffPoint("0.11", "0.101"), ffPoint("0.1", "0.101"))}})),
			"0.01 × 0.001 = 1e-5 — ровно порог, и он включительный")
	})
	t.Run("two lists for one fact", func(t *testing.T) {
		p := ffParams("free", subject)
		p.ExtraInputMediaIds = []int32{99}
		require.Equal(t, "one_list_per_fact",
			ffReason(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform, p)))
	})
	t.Run("too many pictures for one call", func(t *testing.T) {
		p := ffParams("free")
		// Восемь картинок, у каждой по две области: 8 + 16 + 8 = 32 при потолке 16.
		for i := 0; i < entity.MaxDesignFreeformItems; i++ {
			p.Freeform.Items = append(p.Freeform.Items, &pb_common.DesignFreeformItem{
				MediaId: int32(20 + i),
				Regions: []*pb_common.TechCardAnnotation{ffRegion(), ffRegion()},
			})
		}
		require.NoError(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform, p),
			"the shape itself is legal: eight pictures with two areas each")
		err := designRefuseFreeformOverflow(entity.DesignRunKindFreeform, p)
		require.Equal(t, "too_many_pictures", ffReason(t, err))
		require.Contains(t, err.Error(), strconv.Itoa(orimages.MaxInputReferences),
			"the refusal names the provider's own ceiling, not a copy of it")
	})
	t.Run("nine pictures", func(t *testing.T) {
		p := ffParams("free")
		for i := 0; i <= entity.MaxDesignFreeformItems; i++ {
			p.Freeform.Items = append(p.Freeform.Items, &pb_common.DesignFreeformItem{MediaId: int32(20 + i)})
		}
		require.Error(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform, p))
	})
	t.Run("the same picture twice", func(t *testing.T) {
		// ⚠ ЭТО ОБХОД ПОТОЛКА ОБЛАСТЕЙ, А НЕ НЕОПРЯТНОСТЬ ЗАПРОСА. Четыре области объявлены НА
		// КАРТИНКУ; две записи по четыре дали бы восемь — и восемь производных картинок в платном
		// вызове, у которого свой потолок.
		p := ffParams("free",
			&pb_common.DesignFreeformItem{MediaId: 11, Regions: []*pb_common.TechCardAnnotation{ffRegion()}},
			&pb_common.DesignFreeformItem{MediaId: 11, Regions: []*pb_common.TechCardAnnotation{ffRegion()}})
		require.Equal(t, "duplicate_picture",
			ffReason(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform, p)))
	})
	t.Run("no source picture", func(t *testing.T) {
		// Phase 2: `free` with no picture is text → image, legal with words; without words the
		// refusal names what fixes it. The first-wave presets keep `no_source_picture`.
		require.Equal(t, entity.DesignErrorCodeWordsRequired, ffReason(t,
			designRefuseUnworkableSources(entity.DesignRunKindFreeform, "", ffParams("free"))))
		require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindFreeform, "a red coat", ffParams("free")))
		require.Equal(t, "no_source_picture", ffReason(t,
			designRefuseUnworkableSources(entity.DesignRunKindFreeform, "x",
				ffParams(entity.DesignFreeformPresetRepaintParts))))
	})
	t.Run("add_hardware without the hardware", func(t *testing.T) {
		p := ffParams(entity.DesignFreeformPresetAddHardware,
			&pb_common.DesignFreeformItem{MediaId: 11, Role: entity.DesignFreeformRoleSubject,
				Regions: []*pb_common.TechCardAnnotation{ffRegion()}})
		require.Equal(t, "hardware_picture_required",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindFreeform, "", p)))
	})
	t.Run("add_hardware without the place", func(t *testing.T) {
		p := ffParams(entity.DesignFreeformPresetAddHardware,
			&pb_common.DesignFreeformItem{MediaId: 11, Role: entity.DesignFreeformRoleSubject},
			&pb_common.DesignFreeformItem{MediaId: 12, Role: entity.DesignFreeformRoleHardware})
		require.Equal(t, "mark_the_area",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindFreeform, "", p)))
	})
	t.Run("repaint_parts with nothing marked is LEGAL", func(t *testing.T) {
		p := ffParams(entity.DesignFreeformPresetRepaintParts,
			&pb_common.DesignFreeformItem{MediaId: 11, Role: entity.DesignFreeformRoleSubject})
		require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindFreeform, "", p),
			"«repaint the whole garment» is an ordinary ask and the craft knows it")
	})
	t.Run("cutout takes exactly one picture", func(t *testing.T) {
		two := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11, 12}}
		require.Equal(t, "one_source_picture",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindCutout, "", two)))
		none := &pb_common.DesignRunParams{}
		require.Equal(t, "one_source_picture",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindCutout, "", none)))
	})
	t.Run("cutout takes no words", func(t *testing.T) {
		one := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11}}
		require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindCutout, "", one))
		require.Equal(t, "cutout_takes_no_words",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindCutout, "make it pretty", one)))
		// ⚠ ВТОРАЯ ПОЛОВИНА ЭТОЙ ПРОВЕРКИ У ЖИВОЙ ДВЕРИ НЕДОСТИЖИМА, И ЭТО НАЗВАНО ВСЛУХ.
		// Непустой `freeform` на роде «не freeform» ловится РАНЬШЕ, в
		// designRefuseMalformedFreeform, словом `freeform_forbidden`; обе фразы верны и обе
		// показывают на одно и то же поле. Сторож здесь остаётся вторым замком: он держит форму
		// на случай, если порядок у двери когда-нибудь переставят.
		wordy := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11},
			Freeform: &pb_common.DesignFreeformParams{Preset: "free"}}
		require.Equal(t, "cutout_takes_no_words",
			ffReason(t, designRefuseUnworkableSources(entity.DesignRunKindCutout, "", wordy)))
		require.Equal(t, "freeform_forbidden",
			ffReason(t, designRefuseMalformedFreeform(entity.DesignRunKindCutout, wordy)),
			"the door refuses this shape one step earlier, and it names the same field")
	})
	t.Run("and the gate is silent for every other kind", func(t *testing.T) {
		for _, kind := range []string{
			entity.DesignRunKindFlat, entity.DesignRunKindRender,
			entity.DesignRunKindThreed,
		} {
			require.NoErrorf(t, designRefuseMalformedFreeform(kind, &pb_common.DesignRunParams{}), "kind %s", kind)
			require.NoErrorf(t, designRefuseFreeformOverflow(kind, ffParams("free")), "kind %s", kind)
		}
	})
}

// TestThePlaygroundsPicturesAreGUARDED_LIKE_EVERY_OTHER_INPUT.
//
// Шестой источник в designRunInputMediaRefs — это не украшение списка: на нём стоят ДВА сторожа
// («вход не картинка» и «кадр только для показа»), и оба они спрашивают именно эту функцию.
// Картинка плейграунда, не попавшая сюда, обошла бы оба — за деньги.
func TestThePlaygroundsPicturesAreGUARDED_LIKE_EVERY_OTHER_INPUT(t *testing.T) {
	p := ffParams("free",
		&pb_common.DesignFreeformItem{MediaId: 11},
		&pb_common.DesignFreeformItem{MediaId: 12})
	refs := designRunInputMediaRefs(p, &pb_common.DesignInputSnapshot{})
	require.Len(t, refs, 2)
	require.Equal(t, 11, refs[0].ID)
	require.Equal(t, "params.freeform.items.0.media_id", refs[0].Where,
		"the refusal must name the field the person can fix")
	require.Equal(t, 12, refs[1].ID)
	require.Equal(t, []int{11, 12}, designFreeformItemMediaIDs(p),
		"the card boundary asks the same list")
}

// TestTheFreeformSnapshotFREEZES_THE_AREAS_AS_CALLOUTS.
//
// Снимок — это ответ истории на вопрос «что этот прогон показал модели», и другого ответа не
// будет. Область, замороженная парой параллельных списков, разъехалась бы при первом же чтении
// вполглаза; здесь она едет ОДНИМ сообщением, тем же, которым карточка возит свои выноски.
func TestTheFreeformSnapshotFREEZES_THE_AREAS_AS_CALLOUTS(t *testing.T) {
	card := &entity.TechCard{}
	card.GarmentDescription.String, card.GarmentDescription.Valid = "olive shirt, spread collar", true
	card.Fit.String, card.Fit.Valid = "oversized", true

	p := ffParams(entity.DesignFreeformPresetAddHardware,
		&pb_common.DesignFreeformItem{MediaId: 11, Role: entity.DesignFreeformRoleSubject,
			Regions: []*pb_common.TechCardAnnotation{ffRegion()},
			Texts:   []string{"the buckle goes here", "a jacket on a hanger"}},
		&pb_common.DesignFreeformItem{MediaId: 12, Role: entity.DesignFreeformRoleHardware})

	snap, err := designAssembleInputs(designInputSources{
		Kind:   entity.DesignRunKindFreeform,
		Card:   card,
		Params: p,
		Refs:   []entity.DesignReference{{MediaId: 90, Role: "silhouette"}},
		Bench: []entity.DesignBenchSlot{{Id: 1, ViewKey: entity.DesignViewFront,
			Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	require.Len(t, snap.GetRefs(), 2, "the items, and nothing from the card")
	require.Empty(t, snap.GetSlots(), "the playground never reads the bench")
	require.Empty(t, snap.GetGarmentNote(), "«olive shirt» describes the card's garment, not this ask")
	require.Empty(t, snap.GetFit())

	first := snap.GetRefs()[0]
	require.Equal(t, int32(11), first.GetMediaId())
	require.Equal(t, entity.DesignFreeformRoleSubject, first.GetRole())
	require.Len(t, first.GetCallouts(), 1)
	require.Equal(t, "the buckle goes here", first.GetCallouts()[0].GetText())
	require.NotNil(t, first.GetCallouts()[0].GetAnnotation(), "the shape travels WITH the words")
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
		first.GetCallouts()[0].GetAnnotation().GetKind())
	require.Equal(t, "a jacket on a hanger", first.GetNote(),
		"a text with no area describes the whole picture")

	for _, r := range snap.GetRefs() {
		require.NotEqual(t, int32(90), r.GetMediaId(), "a card reference is not an input of this run")
	}
}

// TestARerunOfAPlaygroundRunREBUILDS_ITS_REFS_FROM_ITS_OWN_PARAMS.
//
// ⚠ СУЖЕНИЕ ЗДЕСЬ БЫЛО БЫ ЛОЖЬЮ ПРО ОПЛАЧЕННЫЙ ПРОГОН. Реран вправе разметить те же картинки
// иначе, а воркер собирает ссылки из `params.freeform.items`; снимок, сохранивший вчерашние
// области, утверждал бы про новый прогон то, чего в нём не было, — навсегда.
func TestARerunOfAPlaygroundRunREBUILDS_ITS_REFS_FROM_ITS_OWN_PARAMS(t *testing.T) {
	parent := &entity.DesignRun{Id: 900, Kind: entity.DesignRunKindFreeform}
	parent.Inputs = entity.RawJSON(`{"refs":[
	  {"media_id":11,"callouts":[{"media_id":11,"text":"yesterday"}]},
	  {"media_id":88}],"garment_note":"olive shirt"}`)

	p := ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11, Texts: []string{"today"}})
	snap, _, err := (&Server{}).designRunInputs(context.Background(),
		designInputSources{Kind: entity.DesignRunKindFreeform, Params: p}, parent)
	require.NoError(t, err)
	require.Len(t, snap.GetRefs(), 1, "media 88 is not in this run's items")
	require.Equal(t, int32(11), snap.GetRefs()[0].GetMediaId())
	require.Equal(t, "today", snap.GetRefs()[0].GetNote(),
		"the rerun's own words, not the parent's")
	require.Empty(t, snap.GetRefs()[0].GetCallouts(), "no area was marked this time")
	require.Empty(t, snap.GetGarmentNote(), "the kind reads no garment note, on a rerun either")
}

// TestARerunOfAPlaygroundRunMAY_NOT_SWAP_ITS_PICTURES.
//
// ⚠ ВТОРАЯ ПОЛОВИНА ПРАВИЛА, БЕЗ КОТОРОЙ ПЕРВАЯ БЫЛА ДЫРОЙ. Пересборка ссылок из `params` (проба
// выше) существует ради ОБЛАСТЕЙ: реран вправе разметить те же картинки иначе. Про КАРТИНКИ она не
// говорила ничего — и реран прогона над снимком 11 с `items=[88]` уезжал с картинкой 88, сохраняя
// `rerun_of`. Строка истории показывала на родителя, с которым у неё нет ни одного общего входа:
// провенанс, доказывающий неправду, хуже отсутствующего.
func TestARerunOfAPlaygroundRunMAY_NOT_SWAP_ITS_PICTURES(t *testing.T) {
	parentParams := []byte(`{"freeform":{"preset":"free","items":[{"media_id":11},{"media_id":12}]}}`)

	t.Run("a different picture is refused", func(t *testing.T) {
		swap := ffParams("free", &pb_common.DesignFreeformItem{MediaId: 88})
		err := designRefuseFreeformRerunPictureSwap(entity.DesignRunKindFreeform, swap, 900, parentParams)
		require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
		require.Contains(t, err.Error(), "900", "человеку надо знать, какой прогон он якобы повторяет")
		require.Contains(t, err.Error(), "88")
	})
	t.Run("dropping one of the parent's pictures is refused too", func(t *testing.T) {
		// ⚠ УБАВЛЕНИЕ — ТА ЖЕ ПОДМЕНА. Прогон над одной картинкой из двух показывает модели другое
		// множество, значит повтором родителя не является.
		fewer := ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11})
		require.Equal(t, "rerun_changes_pictures",
			ffReason(t, designRefuseFreeformRerunPictureSwap(
				entity.DesignRunKindFreeform, fewer, 900, parentParams)))
	})
	t.Run("the same pictures reordered and re-marked are LEGAL", func(t *testing.T) {
		// Ровно та правка, ради которой реран вообще принимает params: те же картинки, другой
		// порядок, другие области, другие слова.
		same := ffParams("free",
			&pb_common.DesignFreeformItem{MediaId: 12, Texts: []string{"try it on the cuff"}},
			&pb_common.DesignFreeformItem{MediaId: 11,
				Regions: []*pb_common.TechCardAnnotation{ffRegion()}})
		require.NoError(t, designRefuseFreeformRerunPictureSwap(
			entity.DesignRunKindFreeform, same, 900, parentParams))
	})
	t.Run("a silent rerun is not asked anything", func(t *testing.T) {
		// Молчащий реран наследует параметры родителя целиком — множества совпадают по построению.
		require.NoError(t, designRefuseFreeformRerunPictureSwap(
			entity.DesignRunKindFreeform, nil, 900, parentParams))
	})
	t.Run("a fresh run is not asked anything", func(t *testing.T) {
		swap := ffParams("free", &pb_common.DesignFreeformItem{MediaId: 88})
		require.NoError(t, designRefuseFreeformRerunPictureSwap(
			entity.DesignRunKindFreeform, swap, 0, nil))
	})
	t.Run("an empty list is somebody else's refusal", func(t *testing.T) {
		// «Ни одной картинки» — вопрос работоспособности (`no_source_picture`), и ответить на него
		// словом про подмену значило бы послать человека чинить не то.
		require.NoError(t, designRefuseFreeformRerunPictureSwap(
			entity.DesignRunKindFreeform, &pb_common.DesignRunParams{}, 900, parentParams))
	})
	t.Run("and no other kind is asked at all", func(t *testing.T) {
		for _, kind := range []string{
			entity.DesignRunKindRecolor, entity.DesignRunKindPattern, entity.DesignRunKindCutout,
		} {
			require.NoErrorf(t, designRefuseFreeformRerunPictureSwap(kind,
				ffParams("free", &pb_common.DesignFreeformItem{MediaId: 88}), 900, parentParams),
				"kind %s", kind)
		}
	})
}

// TestStartDesignRunRefusesAPlaygroundRerunThatSWAPS_ITS_PICTURES — ТА ЖЕ ПРОВЕРКА У ЖИВОЙ ДВЕРИ.
//
// ⚠ БЕЗ ЭТОЙ ПОЛОВИНЫ СТОРОЖ МОГ БЫ БЫТЬ МЁРТВЫМ КОДОМ. Пробы выше зовут функцию напрямую и зелены
// даже тогда, когда её никто не зовёт; здесь просьба идёт через StartDesignRun, и предмет проверки
// — что до стора она НЕ ДОЕХАЛА, то есть ни резерва, ни строки, ни цента.
func TestStartDesignRunRefusesAPlaygroundRerunThatSWAPS_ITS_PICTURES(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFreeform,
		Params: entity.RawJSON(`{"freeform":{"preset":"free","items":[{"media_id":11}]}}`),
	}, nil).Maybe()

	req := designStartRequest(entity.DesignRunKindFreeform)
	req.RerunOfRunId = 12
	req.Params = ffParams("free", &pb_common.DesignFreeformItem{MediaId: 88})

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
	require.Nil(t, rig.sent, "отказ до резерва: строка не заведена и бюджет дня не тронут")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: тот же реран с той же картинкой и НОВОЙ областью проходит до стора.
	ok := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	ok.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFreeform,
		Params: entity.RawJSON(`{"freeform":{"preset":"free","items":[{"media_id":11}]}}`),
	}, nil).Maybe()
	ok.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).
		Return(nil).Maybe()
	same := designStartRequest(entity.DesignRunKindFreeform)
	same.RerunOfRunId = 12
	same.Params = ffParams("free", &pb_common.DesignFreeformItem{MediaId: 11,
		Regions: []*pb_common.TechCardAnnotation{ffRegion()}, Texts: []string{"here instead"}})
	_, err = ok.srv.StartDesignRun(designRunCtx(), same)
	require.NoError(t, err)
	require.NotNil(t, ok.sent, "переразметить те же картинки — законная правка просьбы")
	require.Equal(t, 12, ok.sent.RerunOf, "и она остаётся повтором того же прогона")
}

// TestGetDesignBandALWAYS_ANSWERS_ABOUT_THE_PLAYGROUND.
//
// Пустой список значит «сервер про плейграунд знает и говорит, что сейчас нельзя»; ОТСУТСТВИЕ
// поля значило бы «сервер старый». Клиент читает присутствие — и гасит ячейку на рельсе, а не
// пускает человека в отказ.
func TestGetDesignBandALWAYS_ANSWERS_ABOUT_THE_PLAYGROUND(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design).Maybe()
	design.EXPECT().GetBand(mock.Anything, mock.Anything, mock.Anything).
		Return(&entity.DesignBand{}, nil).Times(3)

	// ГЕНЕРАЦИЯ ВЫКЛЮЧЕНА: поле есть, список пуст и НЕ nil.
	off := &Server{repo: repo}
	off.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	resp, err := off.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
	require.NoError(t, err)
	require.NotNil(t, resp.GetFreeformPresets())
	require.Empty(t, resp.GetFreeformPresets())
	// Phase 2 (fields 28–30): present and empty on a closed server, never nil.
	require.NotNil(t, resp.GetPlaygroundWorkflows())
	require.Empty(t, resp.GetPlaygroundWorkflows())
	require.NotNil(t, resp.GetImageModels())
	require.Empty(t, resp.GetImageModels())
	require.NotNil(t, resp.GetThreedOptions())
	require.Empty(t, resp.GetThreedOptions())

	// ГЕНЕРАЦИЯ ВКЛЮЧЕНА, МАРШРУТ ВЫРЕЗА НЕ ПОДКЛЮЧЁН: три пресета, четвёртой кнопки нет.
	on := &Server{repo: repo}
	on.SetDesignGenerationEnabled(true)
	on.SetDesignKindGate(func(kind string) error {
		if kind == entity.DesignRunKindCutout {
			return status.Error(2, "no cutout route is wired")
		}
		return nil
	})
	resp, err = on.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Equal(t, entity.FreeformPresets(), resp.GetFreeformPresets())
	require.NotContains(t, resp.GetFreeformPresets(), entity.DesignRunKindCutout,
		"a button whose route is not wired is a button that leads to a refusal")

	// ⚠ И СЛОВАРЬ ЗДЕСЬ — ТОТ ЖЕ, ЧТО У ДВЕРИ. Кнопка, которой дверь не знает, — отказ по клику.
	for _, preset := range entity.FreeformPresets() {
		require.Truef(t, entity.IsFreeformPreset(preset), "preset %q is served but not accepted", preset)
	}
	// The tiles in grid order, without the closed cutout route and never extend_image; no engine
	// table wired on this server → no picker.
	want := []string{}
	for _, w := range entity.PlaygroundWorkflows() {
		if w != entity.DesignWorkflowRemoveBackground && w != entity.DesignWorkflowExtendImage {
			want = append(want, w)
		}
	}
	require.Equal(t, want, resp.GetPlaygroundWorkflows())
	require.NotNil(t, resp.GetImageModels())
	require.Empty(t, resp.GetImageModels())
	// No 3D route wired on this server → no build option advertised (G-02: threed_options is read off
	// the configured route; see TestTheThreedOptionsFOLLOW_THE_CONFIGURED_ROUTE).
	require.NotNil(t, resp.GetThreedOptions())
	require.Empty(t, resp.GetThreedOptions())

	// EVERY ROUTE OPEN: band 26 is STILL the old three + cutout [Codex 10] — an old client draws
	// every key of it as a chip — and the engines come with exactly one default.
	all := &Server{repo: repo}
	all.SetDesignGenerationEnabled(true)
	all.SetDesignKindGate(func(string) error { return nil })
	all.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	resp, err = all.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Equal(t, append(entity.FreeformPresets(), entity.DesignRunKindCutout), resp.GetFreeformPresets())
	require.NotContains(t, resp.GetPlaygroundWorkflows(), entity.DesignWorkflowExtendImage)
	require.Len(t, resp.GetPlaygroundWorkflows(), len(entity.PlaygroundWorkflows())-1)
	defaults := 0
	for _, m := range resp.GetImageModels() {
		require.NotEmpty(t, m.GetQualities())
		require.Contains(t, m.GetAspectRatios(), "auto")
		if m.GetIsDefault() {
			defaults++
		}
	}
	require.Len(t, resp.GetImageModels(), len(designgen.EngineTable("")))
	require.Equal(t, 1, defaults, "exactly one engine is the deployment's default")
	for _, w := range resp.GetPlaygroundWorkflows() {
		require.Truef(t, entity.IsDesignWorkflow(w), "%q is not a workflow key", w)
	}
}

// ─────────────────────── PLAYGROUND phase 2: the preset doors ───────────────────────

func ffItem(id int32, role string, regions int, texts ...string) *pb_common.DesignFreeformItem {
	it := &pb_common.DesignFreeformItem{MediaId: id, Role: role, Texts: texts}
	for i := 0; i < regions; i++ {
		it.Regions = append(it.Regions, ffRegion())
	}
	return it
}

func ffWithOptions(p *pb_common.DesignRunParams, o *pb_common.DesignWorkflowOptions) *pb_common.DesignRunParams {
	p.Freeform.Options = o
	return p
}

// TestEveryPresetHasITS_SHAPE_TABLE — one row per (preset, shape) → the machine word, all of them
// before money. The interim state after B-01 (nine presets admitted, three with a shape) is closed
// when every preset the door accepts has a role row here.
func TestEveryPresetHasITS_SHAPE_TABLE(t *testing.T) {
	for _, preset := range entity.FreeformPresetsAll() {
		_, ok := designPresetRoles[preset]
		require.Truef(t, ok, "preset %q is accepted by the door and has no role table", preset)
	}

	const (
		model   = entity.DesignFreeformRoleModel
		product = entity.DesignFreeformRoleProduct
		scene   = entity.DesignFreeformRoleScene
		logo    = entity.DesignFreeformRoleLogo
	)
	P := ffParams
	O := ffWithOptions
	for _, c := range []struct {
		name   string
		ask    string
		params *pb_common.DesignRunParams
		want   string // '' = legal
	}{
		{"free: words, no picture", "a red coat", P(entity.DesignFreeformPresetFree), ""},
		{"free: nothing at all", "", P(entity.DesignFreeformPresetFree), entity.DesignErrorCodeWordsRequired},
		{"free: a tryon role", "x", P(entity.DesignFreeformPresetFree, ffItem(11, model, 0)), "unknown_role"},

		{"tryon: model + product", "", P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0)), ""},
		{"tryon: no model", "", P(entity.DesignFreeformPresetTryon, ffItem(12, product, 0)), entity.DesignErrorCodeRoleRequired},
		{"tryon: no product", "", P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0)), entity.DesignErrorCodeRoleRequired},
		{"tryon: an unroled picture", "", P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0), ffItem(13, "", 0)), "unknown_role"},
		{"tryon: five products", "", P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0),
			ffItem(12, product, 0), ffItem(13, product, 0), ffItem(14, product, 0), ffItem(15, product, 0), ffItem(16, product, 0)),
			"too_many_pictures"},
		{"tryon: reference scene missing", "", O(P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0)),
			&pb_common.DesignWorkflowOptions{SceneMode: entity.DesignSceneModeReference}), entity.DesignErrorCodeRoleRequired},
		{"tryon: reference scene given", "", O(P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0), ffItem(13, scene, 0)),
			&pb_common.DesignWorkflowOptions{SceneMode: entity.DesignSceneModeReference, Framing: entity.DesignFramingFullBody}), ""},
		{"tryon: scene picture without reference mode", "", P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0), ffItem(13, scene, 0)),
			entity.DesignErrorCodeOptionNotRead},

		{"fabric_extract: one picture", "", P(entity.DesignFreeformPresetFabricExtract, ffItem(11, "", 0)), ""},
		{"fabric_extract: two pictures", "", P(entity.DesignFreeformPresetFabricExtract, ffItem(11, "", 0), ffItem(12, "", 0)), entity.DesignErrorCodeOneSourcePicture},
		{"ghost_mannequin: none", "x", P(entity.DesignFreeformPresetGhostMannequin), entity.DesignErrorCodeOneSourcePicture},
		{"ghost_mannequin: a marked area", "", P(entity.DesignFreeformPresetGhostMannequin, ffItem(11, "", 1)), entity.DesignErrorCodeOneRegion},
		{"variations: creativity read", "", O(P(entity.DesignFreeformPresetVariations, ffItem(11, entity.DesignFreeformRoleSubject, 0)),
			&pb_common.DesignWorkflowOptions{Creativity: 2}), ""},

		{"add_logo: garment + logo", "", O(P(entity.DesignFreeformPresetAddLogo, ffItem(11, "", 0), ffItem(12, logo, 0)),
			&pb_common.DesignWorkflowOptions{LogoSize: entity.DesignLogoSizeSmall}), ""},
		{"add_logo: no logo", "", P(entity.DesignFreeformPresetAddLogo, ffItem(11, "", 0)), entity.DesignErrorCodeRoleRequired},
		{"add_logo: two logos", "", P(entity.DesignFreeformPresetAddLogo, ffItem(11, "", 0), ffItem(12, logo, 0), ffItem(13, logo, 0)), entity.DesignErrorCodeOneSourcePicture},
		{"add_logo: two garments", "", P(entity.DesignFreeformPresetAddLogo, ffItem(11, "", 0), ffItem(12, "", 0), ffItem(13, logo, 0)), entity.DesignErrorCodeOneSourcePicture},
		{"add_logo: a hardware picture", "", P(entity.DesignFreeformPresetAddLogo, ffItem(11, entity.DesignFreeformRoleHardware, 0), ffItem(12, logo, 0)), "unknown_role"},

		{"retouch: one area with words", "", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 1, "remove the stain")), ""},
		{"retouch: words in the ask", "remove the stain", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 1)), ""},
		{"retouch: no words", "", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 1)), entity.DesignErrorCodeWordsRequired},
		{"retouch: two areas", "x", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 2)), entity.DesignErrorCodeOneRegion},
		{"retouch: no area", "x", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 0)), entity.DesignErrorCodeOneRegion},
		{"retouch: two pictures", "x", P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 1), ffItem(12, "", 0)), entity.DesignErrorCodeOneSourcePicture},
		{"retouch: a cloth picture", "x", P(entity.DesignFreeformPresetRetouch, ffItem(11, entity.DesignFreeformRoleCloth, 1)), "unknown_role"},

		{"option_not_read: logo_size on free", "x", O(P(entity.DesignFreeformPresetFree, ffItem(11, "", 0)),
			&pb_common.DesignWorkflowOptions{LogoSize: entity.DesignLogoSizeLarge}), entity.DesignErrorCodeOptionNotRead},
		{"option_not_read: creativity on tryon", "", O(P(entity.DesignFreeformPresetTryon, ffItem(11, model, 0), ffItem(12, product, 0)),
			&pb_common.DesignWorkflowOptions{Creativity: 1}), entity.DesignErrorCodeOptionNotRead},
		{"option_not_read: framing on retouch", "x", O(P(entity.DesignFreeformPresetRetouch, ffItem(11, "", 1)),
			&pb_common.DesignWorkflowOptions{Framing: entity.DesignFramingAuto}), entity.DesignErrorCodeOptionNotRead},

		{"add_hardware as before", "", P(entity.DesignFreeformPresetAddHardware, ffItem(11, "", 0)), "hardware_picture_required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := designRefuseUnworkableSources(entity.DesignRunKindFreeform, c.ask, c.params)
			if c.want == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, c.want, ffReason(t, err))
		})
	}
}

// TestTheOptionsVOCABULARY_IS_ASKED_OF_THE_SPEAKER — unknown words are refused by the spoken door.
func TestTheOptionsVOCABULARY_IS_ASKED_OF_THE_SPEAKER(t *testing.T) {
	tryon := func(o *pb_common.DesignWorkflowOptions) *pb_common.DesignRunParams {
		return ffWithOptions(ffParams(entity.DesignFreeformPresetTryon,
			ffItem(11, entity.DesignFreeformRoleModel, 0), ffItem(12, entity.DesignFreeformRoleProduct, 0)), o)
	}
	for field, o := range map[string]*pb_common.DesignWorkflowOptions{
		"framing":             {Framing: "cowboy_shot"},
		"angle":               {Angle: "dutch"},
		"scene_mode":          {SceneMode: "replace"},
		"logo_size":           {LogoSize: "huge"},
		"creativity":          {Creativity: entity.MaxDesignCreativity + 1},
		"model_id":            {ModelId: -1},
		"product_colorway_id": {ProductColorwayId: -3},
	} {
		t.Run(field, func(t *testing.T) {
			err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform, tryon(o))
			require.Equal(t, entity.DesignErrorCodeUnknownOption, ffReason(t, err))
		})
	}
	require.NoError(t, designRefuseMalformedFreeform(entity.DesignRunKindFreeform, tryon(
		&pb_common.DesignWorkflowOptions{Framing: entity.DesignFramingPortrait, Angle: entity.DesignAngleLowAngle,
			SceneMode: entity.DesignSceneModeEdit, Creativity: entity.MaxDesignCreativity})))

	t.Run("options on a foreign kind", func(t *testing.T) {
		p := &pb_common.DesignRunParams{Freeform: &pb_common.DesignFreeformParams{
			Options: &pb_common.DesignWorkflowOptions{LogoSize: "small"}}}
		require.Equal(t, "freeform_forbidden", ffReason(t, designRefuseMalformedFreeform(entity.DesignRunKindRender, p)))
	})
	t.Run("unknown_preset lists the nine", func(t *testing.T) {
		err := designRefuseMalformedFreeform(entity.DesignRunKindFreeform, ffParams("make_it_pop"))
		require.Equal(t, "unknown_preset", ffReason(t, err))
		for _, p := range entity.FreeformPresetsAll() {
			require.Contains(t, err.Error(), p)
		}
	})
}

// TestARerunSTAYS_ON_ITS_TILE [Codex 2].
func TestARerunSTAYS_ON_ITS_TILE(t *testing.T) {
	marshal := func(p *pb_common.DesignRunParams) []byte {
		b, err := designMarshalJSON(p)
		require.NoError(t, err)
		return b
	}
	free := ffParams(entity.DesignFreeformPresetFree, ffItem(11, "", 0), ffItem(12, "", 0))

	t.Run("free → tryon", func(t *testing.T) {
		spoken := ffParams(entity.DesignFreeformPresetTryon,
			ffItem(11, entity.DesignFreeformRoleModel, 0), ffItem(12, entity.DesignFreeformRoleProduct, 0))
		err := designRefuseRerunChangesWorkflow(entity.DesignRunKindFreeform, spoken, 900, marshal(free))
		require.Equal(t, entity.DesignErrorCodeRerunChangesWorkflow, ffReason(t, err))
	})
	t.Run("same preset, items reordered", func(t *testing.T) {
		spoken := ffParams(entity.DesignFreeformPresetFree, ffItem(12, "", 1, "here"), ffItem(11, "", 0))
		require.NoError(t, designRefuseRerunChangesWorkflow(entity.DesignRunKindFreeform, spoken, 900, marshal(free)))
	})
	t.Run("add_hardware → repaint_parts stays create_edit", func(t *testing.T) {
		spoken := ffParams(entity.DesignFreeformPresetRepaintParts, ffItem(11, "", 0))
		parent := ffParams(entity.DesignFreeformPresetAddHardware, ffItem(11, "", 1))
		require.NoError(t, designRefuseRerunChangesWorkflow(entity.DesignRunKindFreeform, spoken, 900, marshal(parent)))
	})
	t.Run("recolour gains a cloth picture", func(t *testing.T) {
		parent := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11},
			Colour: &pb_common.DesignColourRecipe{Hex: "#112233"}}
		spoken := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11},
			Colour: &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{{MediaId: 70}}, FabricMediaId: 70}}
		err := designRefuseRerunChangesWorkflow(entity.DesignRunKindRecolor, spoken, 900, marshal(parent))
		require.Equal(t, entity.DesignErrorCodeRerunChangesWorkflow, ffReason(t, err))
	})
	t.Run("a silent rerun and a fresh run are not asked", func(t *testing.T) {
		require.NoError(t, designRefuseRerunChangesWorkflow(entity.DesignRunKindFreeform, nil, 900, marshal(free)))
		require.NoError(t, designRefuseRerunChangesWorkflow(entity.DesignRunKindFreeform,
			ffParams(entity.DesignFreeformPresetTryon), 0, nil))
	})
	t.Run("words-only rerun of a pictured run swaps its pictures", func(t *testing.T) {
		err := designRefuseFreeformRerunPictureSwap(entity.DesignRunKindFreeform,
			ffParams(entity.DesignFreeformPresetFree), 900, marshal(free))
		require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
		require.Contains(t, err.Error(), "names none")
		wordsOnly := marshal(ffParams(entity.DesignFreeformPresetFree))
		require.NoError(t, designRefuseFreeformRerunPictureSwap(entity.DesignRunKindFreeform,
			ffParams(entity.DesignFreeformPresetFree), 900, wordsOnly))
		require.Equal(t, "rerun_changes_pictures", ffReason(t, designRefuseFreeformRerunPictureSwap(
			entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetFree, ffItem(88, "", 0)), 900, wordsOnly)))
	})
}

// TestStartDesignRunRefusesARerunThatCHANGES_ITS_TILE — the guard is wired at the live door.
func TestStartDesignRunRefusesARerunThatCHANGES_ITS_TILE(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFreeform,
		Params: entity.RawJSON(`{"freeform":{"preset":"free","items":[{"media_id":11},{"media_id":12}]}}`),
	}, nil).Maybe()
	req := designStartRequest(entity.DesignRunKindFreeform)
	req.RerunOfRunId = 12
	req.Params = ffParams(entity.DesignFreeformPresetTryon,
		ffItem(11, entity.DesignFreeformRoleModel, 0), ffItem(12, entity.DesignFreeformRoleProduct, 0))
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Equal(t, entity.DesignErrorCodeRerunChangesWorkflow, ffReason(t, err))
	require.Nil(t, rig.sent)
}

// ─────────────────────── B-11: model-photo provenance ───────────────────────

func tryonWithModel(modelID int32, modelMedia int32) *pb_common.DesignRunParams {
	return ffWithOptions(ffParams(entity.DesignFreeformPresetTryon,
		ffItem(modelMedia, entity.DesignFreeformRoleModel, 0), ffItem(12, entity.DesignFreeformRoleProduct, 0)),
		&pb_common.DesignWorkflowOptions{ModelId: modelID})
}

// TestATryonDRESSES_A_PHOTO_OF_THE_NAMED_MODEL — thumbnail and gallery pass, anything else is a
// mismatch, an unknown profile is named, and no model id means no read at all.
func TestATryonDRESSES_A_PHOTO_OF_THE_NAMED_MODEL(t *testing.T) {
	server := func(t *testing.T) (*Server, *mocks.MockModels) {
		repo := mocks.NewMockRepository(t)
		models := mocks.NewMockModels(t)
		repo.EXPECT().Models().Return(models).Maybe()
		return &Server{repo: repo}, models
	}
	profile := &entity.Model{Id: 5}
	profile.ThumbnailId = sql.NullInt32{Int32: 70, Valid: true}
	profile.MediaIds = []int{71, 72}

	for _, c := range []struct {
		name  string
		media int32
		want  string
	}{
		{"the thumbnail", 70, ""},
		{"a gallery photo", 72, ""},
		{"somebody else's photo", 99, entity.DesignErrorCodeModelPhotoMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, models := server(t)
			models.EXPECT().GetModelById(mock.Anything, 5).Return(profile, nil).Once()
			err := s.designRefuseModelPhotoMismatch(designRunCtx(), entity.DesignRunKindFreeform, tryonWithModel(5, c.media))
			if c.want == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, c.want, ffReason(t, err))
		})
	}
	t.Run("an unknown profile", func(t *testing.T) {
		s, models := server(t)
		models.EXPECT().GetModelById(mock.Anything, 6).Return(nil, fmt.Errorf("get: %w", sql.ErrNoRows)).Once()
		err := s.designRefuseModelPhotoMismatch(designRunCtx(), entity.DesignRunKindFreeform, tryonWithModel(6, 70))
		require.Equal(t, entity.DesignErrorCodeModelNotFound, ffReason(t, err))
	})
	t.Run("no model id, or another preset: no read", func(t *testing.T) {
		s, models := server(t)
		require.NoError(t, s.designRefuseModelPhotoMismatch(designRunCtx(), entity.DesignRunKindFreeform, tryonWithModel(0, 99)))
		other := ffWithOptions(ffParams(entity.DesignFreeformPresetFree, ffItem(99, "", 0)),
			&pb_common.DesignWorkflowOptions{ModelId: 5})
		require.NoError(t, s.designRefuseModelPhotoMismatch(designRunCtx(), entity.DesignRunKindFreeform, other),
			"another preset refuses model_id elsewhere (option_not_read), without a read")
		models.AssertNotCalled(t, "GetModelById", mock.Anything, mock.Anything)
	})
}

// TestStartDesignRunRefusesATryonOnAFOREIGN_MODEL_PHOTO — the check is wired at the live door,
// before the store.
func TestStartDesignRunRefusesATryonOnAFOREIGN_MODEL_PHOTO(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	models := mocks.NewMockModels(t)
	rig.repo.EXPECT().Models().Return(models).Maybe()
	profile := &entity.Model{Id: 5}
	profile.MediaIds = []int{71}
	models.EXPECT().GetModelById(mock.Anything, 5).Return(profile, nil).Once()
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()

	req := designStartRequest(entity.DesignRunKindFreeform)
	req.Params = tryonWithModel(5, 99)
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Equal(t, entity.DesignErrorCodeModelPhotoMismatch, ffReason(t, err))
	require.Nil(t, rig.sent)
}
