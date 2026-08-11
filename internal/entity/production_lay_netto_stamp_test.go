package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// САМОПРОВЕРКА ШТАМПА NETTO (0296, правки ревью T7в2 MAJOR 4): штампу верят только вместе с фактом,
// который он делил. Сценарий, ради которого тест существует: откат DO — старый бинарь правит
// actual_qty/actual_uom, не зная про netto-колонки; после повторного выката новый код обязан
// УВИДЕТЬ расхождение базиса с фактом и отбросить штамп с причиной, а не поделить новый факт на
// чужой знаменатель.
func TestTrustedNettoStamp(t *testing.T) {
	d := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	stamped := func() ProductionRunLay {
		var l ProductionRunLay
		l.ActualQty = d("14.4")
		l.ActualUom = sql.NullString{String: "m", Valid: true}
		l.NettoQty = d("12")
		l.NettoBasisQty = d("14.4")
		l.NettoBasisUom = sql.NullString{String: "m", Valid: true}
		return l
	}

	t.Run("базис совпал — штамп доверен", func(t *testing.T) {
		stamp, reason := stamped().TrustedNettoStamp()
		require.Empty(t, reason)
		require.True(t, stamp.Valid)
		require.Equal(t, "12", stamp.Decimal.String())
	})

	t.Run("«14.400» против «14.4» и «м» против \"m\" — тот же факт, не правка", func(t *testing.T) {
		l := stamped()
		l.ActualQty = d("14.400")
		l.ActualUom = sql.NullString{String: "м", Valid: true}
		stamp, reason := l.TrustedNettoStamp()
		require.Empty(t, reason, "сравнение величиной и перечнем, не строкой")
		require.True(t, stamp.Valid)
	})

	t.Run("старый бинарь поправил величину факта — штамп отброшен с причиной", func(t *testing.T) {
		l := stamped()
		l.ActualQty = d("16.0")
		stamp, reason := l.TrustedNettoStamp()
		require.False(t, stamp.Valid, "новый факт нельзя делить на знаменатель, посчитанный для старого")
		require.Contains(t, reason, "мимо штампа")
		require.Contains(t, reason, "14.4", "причина называет факт, который штамп делил, — иначе она неотличима от бага")
	})

	t.Run("старый бинарь поправил единицу факта — штамп отброшен", func(t *testing.T) {
		l := stamped()
		l.ActualUom = sql.NullString{String: "kg", Valid: true}
		stamp, reason := l.TrustedNettoStamp()
		require.False(t, stamp.Valid)
		require.NotEmpty(t, reason)
	})

	t.Run("штамп без базиса — отброшен: источник записи неизвестен", func(t *testing.T) {
		l := stamped()
		l.NettoBasisQty, l.NettoBasisUom = decimal.NullDecimal{}, sql.NullString{}
		stamp, reason := l.TrustedNettoStamp()
		require.False(t, stamp.Valid)
		require.Contains(t, reason, "без базиса")
	})

	t.Run("штампа нет — не отказ, а отсутствие: причина пуста", func(t *testing.T) {
		var l ProductionRunLay
		l.ActualQty = d("14.4")
		stamp, reason := l.TrustedNettoStamp()
		require.False(t, stamp.Valid)
		require.Empty(t, reason, "отсутствие штампа — штатный путь к живому пересчёту, не поломка")
	})
}
