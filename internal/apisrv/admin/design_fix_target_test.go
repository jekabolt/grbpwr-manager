package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ДВА ЧИТАТЕЛЯ ОДНОГО ПОЛЯ — ОДНО ПРАВИЛО.
//
// ЧТО БЫЛО. Контракт называет одно правило: `fix_targets`, когда список непуст, иначе скаляр
// `fix_target`. Промпт (designgen/snapshot.go) его и исполнял. А отбор плит верстака строил
// ОБЪЕДИНЕНИЕ списка и скаляра — то есть второе правило рядом с первым. Запрос с
// fix_target="front" и fix_targets=["back"] отдавал модели плиты front И back, а текстом просил
// «правь back»: оплаченный кадр собирался не из того, о чём его просили, и ни один из читателей
// при этом не был «сломан» — они отвечали на разные вопросы.

// ПЛИТЫ ОТБИРАЮТСЯ ПО СПИСКУ, КОГДА ОН ЕСТЬ. Скаляр при непустом списке не добавляет ничего.
//
// МУТАЦИЯ: вернуть объединение (класть скаляр в ту же карту безусловно) — front возвращается в
// выборку, и она перестаёт совпадать с промптом.
func TestDesignInputSlotsFollowTheListWhenItIsNotEmpty(t *testing.T) {
	got := designInputSlots(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: designTwoSidedBench(),
		Params: &pb_common.DesignRunParams{
			FixTarget:  entity.DesignViewFront,
			FixTargets: []string{entity.DesignViewBack},
		},
	})
	require.Len(t, got, 1, "объединение отдало бы модели обе стороны, а текст просил бы одну")
	require.Equal(t, entity.DesignViewBack, got[0].GetViewKey())
}

// СКАЛЯР ЧИТАЕТСЯ, КОГДА СПИСОК ПУСТ: старое написание обязано остаться понятным навсегда —
// снимки прежних прогонов заморожены и несут именно его.
//
// МУТАЦИЯ: убрать ветку скаляра — прогоны, замороженные до появления списка, перестают сужать
// выборку вовсе и уезжают к модели ВСЕМ верстаком.
func TestDesignInputSlotsFallBackToTheScalarWhenTheListIsEmpty(t *testing.T) {
	got := designInputSlots(designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Bench:  designTwoSidedBench(),
		Params: &pb_common.DesignRunParams{FixTarget: entity.DesignViewFront},
	})
	require.Len(t, got, 1)
	require.Equal(t, entity.DesignViewFront, got[0].GetViewKey())
}

// ПРОТИВОРЕЧИВЫЙ ВХОД ОТВЕРГАЕТСЯ У ДВЕРИ, А НЕ РАЗРЕШАЕТСЯ МОЛЧА.
//
// Выбросить `front` по правилу «список сильнее» значило бы снова тихо отрезать часть просьбы —
// тот же класс, что молчаливая обрезка входов у поставщика. Сервер не угадывает, какое из двух
// написаний человек имел в виду.
//
// МУТАЦИЯ: убрать проверку — противоречие проезжает и замерзает в снимке навсегда.
func TestDesignEffectiveParamsRefusesContradictorySpellings(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		FixTarget:  entity.DesignViewFront,
		FixTargets: []string{entity.DesignViewBack},
	}, nil)
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "contradictory_fix_target", md["reason"])
}

// СОГЛАСОВАННЫЕ НАПИСАНИЯ ЗАКОННЫ. Клиент, шлющий оба для совместимости, ничему не противоречит —
// и при таком входе ОБА мыслимых правила, объединение и падение, дают ОДИН ответ. Без этой пробы
// проверка выше выполнима заглушкой «отвергать всё, где заданы оба».
func TestDesignEffectiveParamsAcceptsAgreeingSpellings(t *testing.T) {
	params, err := designEffectiveParams(&pb_common.DesignRunParams{
		FixTarget:  entity.DesignViewFront,
		FixTargets: []string{entity.DesignViewFront, entity.DesignViewBack},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{entity.DesignViewFront, entity.DesignViewBack}, params.GetFixTargets())
	require.Equal(t, entity.DesignViewFront, params.GetFixTarget(),
		"согласованный скаляр не стирается: старому читателю он по-прежнему отвечает верно")
}

// designTwoSidedBench — верстак с флэтами front и back, каждый со своей плитой.
func designTwoSidedBench() []entity.DesignBenchSlot {
	slot := func(id int, view string, pic int) entity.DesignBenchSlot {
		return entity.DesignBenchSlot{
			Id: id, TechCardId: designRunCardID, ViewKey: view,
			Kind:      entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: int32(pic), Valid: true},
			Picture: &entity.DesignPicture{
				Id: pic, TechCardId: designRunCardID, MediaId: 300 + pic,
				Kind: entity.DesignPictureKindFlat,
			},
		}
	}
	return []entity.DesignBenchSlot{
		slot(1, entity.DesignViewFront, 11),
		slot(2, entity.DesignViewBack, 12),
	}
}
