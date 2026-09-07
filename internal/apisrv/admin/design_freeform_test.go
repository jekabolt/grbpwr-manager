package admin

import (
	"context"
	"strconv"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
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
		require.Equal(t, "no_source_picture", ffReason(t,
			designRefuseUnworkableSources(entity.DesignRunKindFreeform, "", ffParams("free"))))
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
			entity.DesignRunKindThreed, entity.DesignRunKindVector,
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
		Return(&entity.DesignBand{}, nil).Twice()

	// ГЕНЕРАЦИЯ ВЫКЛЮЧЕНА: поле есть, список пуст и НЕ nil.
	off := &Server{repo: repo}
	resp, err := off.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
	require.NoError(t, err)
	require.NotNil(t, resp.GetFreeformPresets())
	require.Empty(t, resp.GetFreeformPresets())

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
}
