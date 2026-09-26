package designgen

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/require"
)

// ═══ B-09: 3D ИЗ НАЗВАННОЙ КАРТИНКИ И ОПЦИИ СБОРКИ — ПРОБЫ ВОРКЕРА ══════════════════════════════

// referenceRun — прогон 3D, у которого есть И полный верстак, И названные картинки. Верстак здесь
// нарочно: проба обязана показать, что режим референса его НЕ читает.
func referenceRun(id int) entity.DesignRun {
	r := testRun(id, entity.DesignRunKindThreed)
	r.Params = entity.RawJSON(`{
	  "extra_input_media_ids": [77],
	  "threed": {"reference_media_ids": [55, 56, 57], "texture": "off", "pbr": "", "quality": "detailed"}
	}`)
	r.Inputs = entity.RawJSON(`{
	  "refs": [{"media_id": 90, "role": "silhouette"}],
	  "slots": [
	    {"view_key": "front", "media_id": 1},
	    {"view_key": "back",  "media_id": 2}
	  ]
	}`)
	return r
}

// TestReferenceModeSendsTheNamedPicturesInTheirOrder — ids в порядке человека, виды по позиции,
// подписи «reference view N»; ни плит верстака, ни референсов карточки, ни extra.
//
// МУТАЦИЯ, КОТОРАЯ КРАСИТ: threedPicturesOf, читающий `in.Slots` (т.е. `return threedPictures(list,
// in)` безусловно) — вместо 55/56/57 уезжают плиты 1/2.
func TestReferenceModeSendsTheNamedPicturesInTheirOrder(t *testing.T) {
	run := referenceRun(1)
	p, in := parseParams(run.Params), parseInputs(run.Inputs)
	list := referenceList(run.Kind, p, in)
	require.NotEmpty(t, list, "положительный контроль: у снимка есть плиты и референсы, которые можно было бы взять")

	got := threedPicturesOf(list, in, p)
	require.Equal(t, []refCaption{
		{MediaID: 55, Caption: "reference view 1", View: entity.DesignViewFront},
		{MediaID: 56, Caption: "reference view 2", View: entity.DesignViewBack},
		{MediaID: 57, Caption: "reference view 3", View: entity.DesignViewSideL},
	}, got)
}

// TestAFourthReferenceIsTheRightSide — полный набор ставит четвёртую картинку правой стороной, и
// fal-маршрут раскладывает все четыре по именам без отказа.
func TestAFourthReferenceIsTheRightSide(t *testing.T) {
	got := threedReferencePictures([]int{5, 6, 7, 8})
	require.Equal(t, entity.DesignViewSideR, got[3].View)

	job := Job{}
	for _, rc := range got {
		job.References = append(job.References, "https://cdn.example/m/x.png")
		job.ReferenceViews = append(job.ReferenceViews, rc.View)
	}
	_, err := falViews(job)
	require.NoError(t, err, "четыре позиции — четыре разных стороны")
}

// TestAFifthReferenceIsNeverSilentlyTrimmed — пятая позиция (дверь её не пускает, но замороженный
// снимок мог доехать) уезжает БЕЗ вида, и fal-маршрут отказывает ей до денег.
func TestAFifthReferenceIsNeverSilentlyTrimmed(t *testing.T) {
	got := threedReferencePictures([]int{5, 6, 7, 8, 9})
	require.Len(t, got, 5, "хвост не отрезается молча")
	require.Empty(t, got[4].View)
	job := Job{}
	for _, rc := range got {
		job.References = append(job.References, "https://cdn.example/m/x.png")
		job.ReferenceViews = append(job.ReferenceViews, rc.View)
	}
	_, err := falViews(job)
	require.ErrorIs(t, err, fal.ErrNoFrontView)
}

// TestBenchModeIsTodaysSelection — без названных картинок диспетчер отвечает РОВНО тем, что отвечал
// threedPictures, на каждом из старых снимков.
func TestBenchModeIsTodaysSelection(t *testing.T) {
	for _, run := range []entity.DesignRun{threedRun(), steerRun(2)} {
		p, in := parseParams(run.Params), parseInputs(run.Inputs)
		list := referenceList(run.Kind, p, in)
		require.Equal(t, threedPictures(list, in), threedPicturesOf(list, in, p))
		require.NotEmpty(t, threedPicturesOf(list, in, p), "положительный контроль: плиты есть")
	}
}

// TestTheOptionsAreReadOffTheFrozenParams — snake_case теги B-02 плюс чтение в threedOptions.
func TestTheOptionsAreReadOffTheFrozenParams(t *testing.T) {
	o := threedOptionsOf(parseParams(referenceRun(3).Params))
	require.Equal(t, threedOptions{Texture: "off", Quality: "detailed"}, o)
	require.True(t, o.untextured())
	require.Equal(t, threedOptions{}, threedOptionsOf(parseParams(steerRun(4).Params)),
		"прогон верстака, замороженный до полей, читает нули — то есть сегодняшние константы")
}

// ─────────────────── байт в байт: 3D верстака не сдвинулся ───────────────────

// TestBenchPlate3DBodyIsByteIdentical — настоящий проход воркера над сегодняшним прогоном верстака
// (steerRun) отправляет fal-маршруту ТО ЖЕ тело, что до B-09: те же ключи, в том же порядке, с теми
// же константами, и ни одного нового ключа.
//
// ЭТАЛОН СОБРАН РУКАМИ В ФОРМЕ meshySubmitBody на 03985d5, а стир берётся из записанной истории
// (он — не предмет этой пробы; его сторожат пробы стира). МУТАЦИИ, КОТОРЫЕ КРАСЯТ: умолчание
// texture → off; geometry_resolution без omitempty; execute, подставляющий опции не из задания
// (например, `quality: detailed` по умолчанию).
func TestBenchPlate3DBodyIsByteIdentical(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, "meshy/v7/multi-image-to-3d"))
	_ = w.execute(context.Background(), steerRun(36), "tok")

	var raw string
	select {
	case raw = <-stand.body:
	default:
		t.Fatal("сабмита не было")
	}
	require.Len(t, st.recordedPrompts, 1)
	steer, err := json.Marshal(st.recordedPrompts[0])
	require.NoError(t, err)
	require.NotEqual(t, `""`, string(steer), "положительный контроль: стир у прогона есть")
	want := `{"image_urls":["https://cdn.example/m/21.png","https://cdn.example/m/22.png"],` +
		`"should_texture":true,"enable_pbr":false,"enable_safety_checker":true,` +
		`"texture_prompt":` + string(steer) + `}`
	require.Equal(t, want, raw)
}

// TestBenchPlate3DMeshyBodyIsByteIdentical — то же для прямого маршрута Meshy.
func TestBenchPlate3DMeshyBodyIsByteIdentical(t *testing.T) {
	stand := newThreedSteerStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, newThreedSteerProvider(t, stand.srv.URL))
	_ = w.execute(context.Background(), steerRun(37), "tok")

	var raw string
	select {
	case raw = <-stand.body:
	default:
		t.Fatal("сабмита не было")
	}
	require.Len(t, st.recordedPrompts, 1)
	steer, err := json.Marshal(st.recordedPrompts[0])
	require.NoError(t, err)
	want := `{"image_urls":["https://cdn.example/m/21.png","https://cdn.example/m/22.png"],` +
		`"target_formats":["glb"],"should_texture":true,"enable_pbr":false,` +
		`"texture_prompt":` + string(steer) + `}`
	require.Equal(t, want, raw)
}

// ─────────────────── опции доезжают до тела ───────────────────

func refJob() Job {
	return Job{
		RunID:          9,
		Kind:           entity.DesignRunKindThreed,
		References:     []string{"https://cdn.example/m/55.png"},
		ReferenceViews: []string{entity.DesignViewFront},
		SurfaceSteer:   "colourway BLK",
	}
}

// TestTheRunsOptionsReachBothRoutes — маршрут передаёт опции транспорту дословно и не отдаёт слов
// сборке без текстуры. МУТАЦИЯ: execute, не копирующий opts в запрос (→ should_texture true, красно).
func TestTheRunsOptionsReachBothRoutes(t *testing.T) {
	opts := threedOptions{Texture: "off", Quality: "detailed"}

	falStand := newFalSubmitStand(t)
	fp := falRoute(t, falStand.srv.URL, "meshy/v7/multi-image-to-3d").(falThreedProvider)
	_, err := fp.execute(context.Background(), refJob(), opts)
	require.NoError(t, err)
	var fb map[string]any
	require.NoError(t, json.Unmarshal([]byte(<-falStand.body), &fb))
	require.Equal(t, false, fb["should_texture"])
	require.Equal(t, "2k", fb["geometry_resolution"])
	require.NotContains(t, fb, "texture_prompt")

	mStand := newThreedSteerStand(t)
	mp := newThreedSteerProvider(t, mStand.srv.URL).(threedProvider)
	_, err = mp.execute(context.Background(), refJob(), threedOptions{PBR: "on", Quality: "detailed"})
	require.NoError(t, err)
	var mb map[string]any
	require.NoError(t, json.Unmarshal([]byte(<-mStand.body), &mb))
	require.Equal(t, true, mb["enable_pbr"])
	require.Equal(t, "2k", mb["geometry_resolution"])
	require.Equal(t, "colourway BLK", mb["texture_prompt"])
}

// TestAnUntexturedBuildSendsAndRecordsNoWords — steerFor и есть то, что уезжает и что пишется.
func TestAnUntexturedBuildSendsAndRecordsNoWords(t *testing.T) {
	require.Equal(t, "", steerFor("colourway BLK", threedOptions{Texture: "off"}))
	require.Equal(t, "colourway BLK", steerFor("colourway BLK", threedOptions{}))
	require.Equal(t, "colourway BLK", steerFor("colourway BLK", threedOptions{Texture: "on", PBR: "on"}))
}
