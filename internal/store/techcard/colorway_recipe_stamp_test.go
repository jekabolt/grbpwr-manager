package techcard

import (
	"database/sql"
	"testing"
	"time"

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
