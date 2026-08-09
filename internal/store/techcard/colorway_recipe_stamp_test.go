package techcard

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Правило Ф6.8: отметка применения нормы двигается ТОЛЬКО когда пара (источник, раскладка) сменилась.
// Проверяется без базы — функция чистая, а цена ошибки в ней высокая: сдвинувшаяся отметка гасит
// индикатор «раскладку перемеряли после применения», и погасший индикатор неотличим от починенного
// расхождения.
func TestStampAppliedAtMovesOnlyWhenTheMarkerChanges(t *testing.T) {
	applied := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	at := func(ti time.Time) sql.NullTime { return sql.NullTime{Time: ti, Valid: true} }
	id := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	marker := func(m sql.NullInt64, a sql.NullTime) usageProvenance {
		return usageProvenance{source: entity.ConsumptionSourceMarker, markerID: m, appliedAt: a}
	}
	manual := usageProvenance{source: entity.ConsumptionSourceManual}

	t.Run("та же раскладка — отметка переносится дословно", func(t *testing.T) {
		got := stampAppliedAt(marker(id(5), sql.NullTime{}), []usageProvenance{marker(id(5), at(applied))}, now)
		require.True(t, got.appliedAt.Valid)
		require.Equal(t, applied, got.appliedAt.Time,
			"правка соседнего поля не имеет права обновлять отметку — иначе она обгонит updated_at раскладки")
	})

	t.Run("другая раскладка — отметка сейчас", func(t *testing.T) {
		got := stampAppliedAt(marker(id(7), sql.NullTime{}), []usageProvenance{marker(id(5), at(applied))}, now)
		require.Equal(t, now, got.appliedAt.Time)
	})

	t.Run("штамп сняли — отметки нет вовсе", func(t *testing.T) {
		got := stampAppliedAt(manual, []usageProvenance{marker(id(5), at(applied))}, now)
		require.False(t, got.appliedAt.Valid, "отметка без раскладки не отвечает ни на один вопрос")
	})

	t.Run("прошлого нет — отметка сейчас", func(t *testing.T) {
		got := stampAppliedAt(marker(id(5), sql.NullTime{}), nil, now)
		require.Equal(t, now, got.appliedAt.Time)
	})

	// СЛОТ, СТРОКИ КОТОРОГО НЕ СОГЛАСНЫ. Повторяющиеся строки на одном слоте законны, и одна из них
	// может быть марочной, а другая ручной. Согласия тут нет ни по какому определению — но пара
	// (marker, #5) в слоте БЫЛА, и её отметку обязано перенести. Если брать отметку только из
	// «согласованного прошлого», здесь встанет now, и индикатор погаснет у раскладки, перемеренной
	// вчера, — молча, на сохранении соседнего поля.
	t.Run("слот с разногласием всё равно отдаёт свою отметку", func(t *testing.T) {
		priors := []usageProvenance{manual, marker(id(5), at(applied))}
		got := stampAppliedAt(marker(id(5), sql.NullTime{}), priors, now)
		require.True(t, got.appliedAt.Valid)
		require.Equal(t, applied, got.appliedAt.Time)
	})

	// Из подходящих берётся САМАЯ РАННЯЯ: ранняя может лишь показать расхождение, которое поздняя
	// спрятала бы, а прятать — единственная непоправимая из двух ошибок.
	t.Run("несколько подходящих — берётся самая ранняя", func(t *testing.T) {
		later := applied.Add(72 * time.Hour)
		priors := []usageProvenance{marker(id(5), at(later)), marker(id(5), at(applied))}
		got := stampAppliedAt(marker(id(5), sql.NullTime{}), priors, now)
		require.Equal(t, applied, got.appliedAt.Time)
	})

	// Источник — часть пары. Ручная строка со случайно уцелевшим id не имеет права одолжить свою
	// отметку марочной: normalized() такой id и не пропустит, но правило проверяется на паре целиком.
	t.Run("совпал id, но не источник — отметка сейчас", func(t *testing.T) {
		stray := usageProvenance{source: entity.ConsumptionSourceManual, markerID: id(5), appliedAt: at(applied)}
		got := stampAppliedAt(marker(id(5), sql.NullTime{}), []usageProvenance{stray}, now)
		require.Equal(t, now, got.appliedAt.Time)
	})
}

// НОРМА С ВЫКРОЕК В ПРОВЕНАНСЕ (0294). Хелперы чистые, а цена ошибки в них — молчаливая: строка,
// сохранённая как 'manual' вместо 'dxf', выглядит на экране «введённой руками», то есть фича
// читается отключённой, а не сломанной. База здесь не нужна.
func TestDxfProvenanceCarriesAndNeverStamps(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	dxf := usageProvenance{source: entity.ConsumptionSourceDxf}

	t.Run("normalized оставляет источник и чистит всё марочное", func(t *testing.T) {
		got := usageProvenance{
			source:   entity.ConsumptionSourceDxf,
			selvedge: decimal.NewNullDecimal(decimal.RequireFromString("2")),
			cut:      decimal.NewNullDecimal(decimal.RequireFromString("20")),
			markerID: sql.NullInt64{Int64: 5, Valid: true},
			appliedAt: sql.NullTime{
				Time: now, Valid: true},
		}.normalized()
		require.Equal(t, entity.ConsumptionSourceDxf, got.source, "источник не демотируется нормализацией")
		require.False(t, got.selvedge.Valid, "разложение отходов описывает раскладку, которой не было")
		require.False(t, got.cut.Valid)
		require.False(t, got.markerID.Valid, "id раскладки — заявление о настиле, которого не случалось")
		require.False(t, got.appliedAt.Valid)
	})

	t.Run("штамп никогда не ставится и не переносится", func(t *testing.T) {
		// Ф2 (следующая фаза) при желании заведёт свежесть dxf-нормы отдельно; СЕГОДНЯ отметка у неё
		// пуста, и это не забывчивость — отметка без раскладки ни на один вопрос не отвечает.
		priors := []usageProvenance{{
			source:    entity.ConsumptionSourceDxf,
			appliedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
		}}
		got := stampAppliedAt(dxf.normalized(), priors, now)
		require.False(t, got.appliedAt.Valid)
	})

	t.Run("presence-less сохранение переносит согласованный dxf слота", func(t *testing.T) {
		agreed, ok := agreedSlotProvenance([]usageProvenance{dxf, dxf})
		require.True(t, ok, "две одинаковые dxf-строки слота согласны")
		require.Equal(t, entity.ConsumptionSourceDxf, agreed.source,
			"стейлый клиент не имеет права молча вернуть строку в ручной режим")
	})

	t.Run("dxf и marker на одном слоте согласия не дают", func(t *testing.T) {
		marker := usageProvenance{source: entity.ConsumptionSourceMarker}
		_, ok := agreedSlotProvenance([]usageProvenance{dxf, marker})
		require.False(t, ok)
	})

	t.Run("carriedSlotStamp по источнику dxf не находит ничего", func(t *testing.T) {
		// Даже если в строках слота лежит марочный штамп: он принадлежит другому источнику, и
		// перенести его на dxf значило бы приписать площади деталей чужую раскладку.
		priors := []usageProvenance{{
			source:   entity.ConsumptionSourceMarker,
			markerID: sql.NullInt64{Int64: 7, Valid: true},
		}}
		require.False(t, carriedSlotStamp(priors, entity.ConsumptionSourceDxf).Valid)
	})
}
