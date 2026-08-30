package designgen

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ───────── ОДИН ПРЕДПОЛЁТ НА ДВОИХ: ДВЕРЬ И ПРОХОД ─────────
//
// ЧТО ЗДЕСЬ ДОКАЗЫВАЕТСЯ И ПОЧЕМУ ЭТО НЕ ОЧЕВИДНО. Хендлер обязан отказывать роду, который всё
// равно не доедет, ДО того как заведёт строку и зарезервирует деньги дня. Соблазн — написать в
// хендлере список «vector и threed сейчас не работают». Такой список разошёлся бы с реальностью
// молча в обе стороны: он продолжал бы отказывать после того, как хранилище научилось типу, и
// продолжал бы пропускать после того, как маршрут стал возвращать новый.
//
// Поэтому ответ СЧИТАЕТСЯ: Produces() маршрута × Accepts() приёмника. Пробы ниже меряют именно
// это свойство — они меняют ТОЛЬКО возможности (что маршрут отдаёт, что приёмник умеет) и
// смотрят, как меняется вердикт при ОДНОМ И ТОМ ЖЕ роде.

// TestTheGateIsComputedFromCapabilitiesNotFromTheKindName.
//
// Род один и тот же — vector. Меняется только приёмник. Если бы ответ брался из списка родов, обе
// половины таблицы дали бы один и тот же вердикт.
func TestTheGateIsComputedFromCapabilitiesNotFromTheKindName(t *testing.T) {
	vec := &fakeProvider{name: "recraft_vector", produces: []string{ContentTypeSVG}}
	providers := Providers{Vector: vec}

	blind := newWorker(nil, nil, nil, newFakeSink(ContentTypePNG), providers)
	require.Error(t, blind.PreflightKind(entity.DesignRunKindVector),
		"приёмник, не умеющий SVG, обязан закрыть вектор")

	able := newWorker(nil, nil, nil, newFakeSink(ContentTypePNG, ContentTypeSVG), providers)
	require.NoError(t, able.PreflightKind(entity.DesignRunKindVector),
		"тот же род, тот же маршрут, другой приёмник — и отказ обязан ИСЧЕЗНУТЬ САМ, без правки")

	// И симметрично: приёмник тот же, меняется маршрут.
	raster := &fakeProvider{name: "recraft_vector", produces: []string{ContentTypePNG}}
	require.NoError(t,
		newWorker(nil, nil, nil, newFakeSink(ContentTypePNG), Providers{Vector: raster}).
			PreflightKind(entity.DesignRunKindVector))
	weird := &fakeProvider{name: "recraft_vector", produces: []string{ContentTypePNG, "application/pdf"}}
	require.Error(t,
		newWorker(nil, nil, nil, newFakeSink(ContentTypePNG, ContentTypeSVG), Providers{Vector: weird}).
			PreflightKind(entity.DesignRunKindVector),
		"новый тип на выходе закрывает род сам, без единой правки в списках")
}

// TestEveryArtifactTypeIsCheckedNotJustTheFirst. 3D отдаёт ДВЕ вещи — модель и растровую плитку.
// Проверка первой из них зеленела бы ровно на том маршруте, который сложнее всего.
func TestEveryArtifactTypeIsCheckedNotJustTheFirst(t *testing.T) {
	thd := &fakeProvider{name: "meshy", produces: []string{ContentTypeGLB, ContentTypePNG}}
	w := newWorker(nil, nil, nil, newFakeSink(ContentTypeGLB), Providers{Threed: thd})
	err := w.PreflightKind(entity.DesignRunKindThreed)
	require.Error(t, err, "PNG-плитку тоже некуда класть — род закрыт")
	require.Contains(t, err.Error(), ContentTypePNG)

	ok := newWorker(nil, nil, nil, newFakeSink(ContentTypeGLB, ContentTypePNG), Providers{Threed: thd})
	require.NoError(t, ok.PreflightKind(entity.DesignRunKindThreed))
}

// TestTheDoorAndThePassGiveTheSameAnswer — одно выражение, спрошенное дважды.
//
// Дверь спрашивает при создании прогона, проход — при исполнении. Разойдясь, они дают ровно две
// плохие вещи: дверь строже прохода — рабочий род закрыт навсегда; проход строже двери — оплаченный
// резерв под прогон, который гарантированно провалится.
func TestTheDoorAndThePassGiveTheSameAnswer(t *testing.T) {
	for _, c := range []struct {
		name     string
		produces []string
		accepts  []string
		off      bool
	}{
		{"storable", []string{ContentTypeSVG}, []string{ContentTypeSVG}, false},
		{"not storable", []string{ContentTypeSVG}, []string{ContentTypePNG}, false},
		{"one of two storable", []string{ContentTypeGLB, ContentTypePNG}, []string{ContentTypeGLB}, false},
		{"no credentials", []string{ContentTypePNG}, []string{ContentTypePNG}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			prov := &fakeProvider{name: "vector", produces: c.produces, off: c.off, out: okOutcome(1, 0.08)}
			st := &fakeStore{}
			sink := newFakeSink(c.accepts...)
			w := testWorker(st, nil, sink, Providers{Vector: prov})

			doorRefused := w.PreflightKind(entity.DesignRunKindVector) != nil

			require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindVector), "tok"))
			passRefused := len(st.failed) == 1 && len(st.started) == 0

			require.Equal(t, doorRefused, passRefused,
				"дверь и проход обязаны отвечать одинаково: они и есть один вызов")
			if doorRefused {
				require.Empty(t, prov.calls, "отказ обязан случиться ДО платного вызова")
			}
		})
	}
}

// TestTheRefusalCarriesTheSameWordTheHistoryRowWouldHave. Клиент разбирает машинную причину, а не
// английскую прозу; строка истории — тот же словарь. Два разных слова про одно событие — это два
// разных события в глазах читателя.
func TestTheRefusalCarriesTheSameWordTheHistoryRowWouldHave(t *testing.T) {
	unstorable := newWorker(nil, nil, nil, newFakeSink(ContentTypePNG),
		Providers{Vector: &fakeProvider{name: "recraft_vector", produces: []string{ContentTypeSVG}}})
	err := unstorable.PreflightKind(entity.DesignRunKindVector)
	require.Error(t, err)
	var refusal *KindRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, CodeOutputNotStorable, refusal.RefusalReason())
	require.Equal(t, entity.DesignRunKindVector, refusal.Kind)
	require.ErrorIs(t, err, errSinkUnsupported, "сентинел обязан пережить обёртку: по нему классифицируют")
	require.Equal(t, CodeOutputNotStorable, classify(err).Code)

	missing := newWorker(nil, nil, nil, newFakeSink(ContentTypePNG), Providers{})
	err = missing.PreflightKind(entity.DesignRunKindVector)
	require.Error(t, err)
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, CodeKindNotAvailable, refusal.RefusalReason())

	disabled := newWorker(nil, nil, nil, newFakeSink(ContentTypePNG),
		Providers{Vector: &fakeProvider{name: "recraft_vector", produces: []string{ContentTypePNG}, off: true}})
	require.Error(t, disabled.PreflightKind(entity.DesignRunKindVector))
}

// TestASinklessGateRefusesRatherThanGuesses. «Я не могу проверить, где это хранить» не должно
// читаться как «есть где».
func TestASinklessGateRefusesRatherThanGuesses(t *testing.T) {
	w := newWorker(nil, nil, nil, nil,
		Providers{Image: &fakeProvider{name: "image", produces: []string{ContentTypePNG}}})
	require.Error(t, w.PreflightKind(entity.DesignRunKindFlat))
}

// TestTheRealRoutesOutputsAllHaveSomewhereToLive — ПРОБА, КОТОРАЯ ПОЙМАЛА БЫ ВЕСЬ ДЕФЕКТ.
//
// Она берёт НАСТОЯЩИЕ маршруты и НАСТОЯЩИЙ приёмник — не подделки — и спрашивает ровно то, о чём
// молчали все остальные: умеет ли бакет хранить то, что эти маршруты отдают. Пока приёмник знал
// только растр, эта проба краснела бы на vector и на threed — то есть с первого дня, а не после
// первого оплаченного клика.
//
// Списка типов здесь нет: он читается у самих маршрутов.
func TestTheRealRoutesOutputsAllHaveSomewhereToLive(t *testing.T) {
	sink := &bucketSink{}
	for _, prov := range []Provider{
		NewImageProvider(nil), NewVectorProvider(nil), NewThreedProvider(nil),
	} {
		produces := prov.Produces()
		require.NotEmpty(t, produces, "маршрут, который ничего не обещает, делает пробу пустой")
		for _, ct := range produces {
			require.Truef(t, sink.Accepts(ct),
				"маршрут %s отдаёт %s, а приёмнику его некуда класть: этот прогон был бы оплачен и провален",
				prov.Name(), ct)
		}
	}
}
