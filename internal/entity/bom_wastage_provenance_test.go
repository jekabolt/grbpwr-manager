package entity

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПРОВЕНАНС ПРОЦЕНТА РАСКРОЯ (0296) — §9 + правки ревью T7в2: правило о СУТИ (смена значения
// сбрасывает источник, что бы клиент ни прислал — MAJOR 3), самопроверка бейджа по applied_percent
// (MAJOR 4), verbatim-протокол присутствия на full-replace, серверный штамп applied_at.
func TestResolveBomWastageProvenance(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	then := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	d := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	// applied — живой бейдж: applied_percent совпадает с числом percent (самопроверка проходит).
	applied := func(n int64, at time.Time, percent string) BomWastageProvenance {
		return BomWastageProvenance{
			Source:         BomWastageSourceLays,
			LayCount:       sql.NullInt64{Int64: n, Valid: true},
			AppliedAt:      sql.NullTime{Time: at, Valid: true},
			AppliedPercent: d(percent),
		}
	}
	claim := func(n int64) BomWastageProvenance {
		return BomWastageProvenance{Source: BomWastageSourceLays, LayCount: sql.NullInt64{Int64: n, Valid: true}}
	}

	t.Run("старый клиент, значение не тронуто — бейдж переживает full-replace verbatim", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(5, then, "22.00"), d("22.00"), d("22.00"),
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, applied(5, then, "22.00"), got,
			"отсутствие пары на проводе — молчание, не инструкция: иначе первый же сейв старого бандла стёр бы аудит")
	})

	t.Run("«22.5» против «22.50» — то же значение, не правка", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(5, then, "22.50"), d("22.50"), d("22.5"),
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, BomWastageSourceLays, got.Source, "сравнение по величине, не по представлению")
	})

	t.Run("старый клиент правит значение — сброс в manual, штамп гаснет", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(5, then, "22.00"), d("22.00"), d("25.00"),
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, BomWastageSourceManual, got.Source,
			"ручное число не должно донашивать бейдж «медиана по 5 раскроям»")
		require.False(t, got.LayCount.Valid)
		require.False(t, got.AppliedAt.Valid)
	})

	t.Run("очистка значения старым клиентом — тоже правка", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(5, then, "22.00"), d("22.00"), decimal.NullDecimal{},
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, BomWastageSourceManual, got.Source)
		require.False(t, got.AppliedAt.Valid)
	})

	// MAJOR 3 — сам эксплойт, дословно: full-replace клиент меняет процент и ЭХО-ШЛЁТ источник
	// «lays, 4 настила» обратно. Правило о сути: значение изменилось — manual, что бы клиент ни
	// прислал, и штамп даты не движется на новое время.
	t.Run("эхо источника при смене числа — manual, что бы клиент ни прислал", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("35.00"),
			claim(4), true, false, now)
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual}, got,
			"сервер не имеет права хранить «медиана по 4 раскроям» на числе, которого калибровка не давала")
	})

	t.Run("свежая заявка без подтверждения сервера — manual (fail-closed)", func(t *testing.T) {
		got := ResolveBomWastageProvenance(BomWastageProvenance{}, decimal.NullDecimal{}, d("22.00"),
			claim(4), true, false, now)
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual}, got,
			"единственная дверь к бейджу — верификация обработчика; прямой вызов стора её открыть не может")
	})

	t.Run("подтверждённое применение предложения — сервер штампует сейчас и запоминает число", func(t *testing.T) {
		got := ResolveBomWastageProvenance(BomWastageProvenance{}, decimal.NullDecimal{}, d("22.00"),
			claim(4), true, true, now)
		require.Equal(t, applied(4, now, "22.00"), got,
			"applied_percent — самопроверка бейджа: без неё откат DO делал бы бейдж недетектируемо устаревшим")
	})

	t.Run("эхо той же пары над тем же числом — verbatim, штамп НЕ освежается", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("22.00"),
			claim(4), true, false, now)
		require.Equal(t, then, got.AppliedAt.Time,
			"освежённый штамп заявил бы более свежую калибровку, чем была сделана")
		require.Equal(t, applied(4, then, "22.00"), got)
	})

	t.Run("новая медиана (другой счётчик) при том же числе — только с подтверждением", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("22.00"),
			claim(7), true, true, now)
		require.Equal(t, now, got.AppliedAt.Time)
		require.EqualValues(t, 7, got.LayCount.Int64)

		got = ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("22.00"),
			claim(7), true, false, now)
		require.Equal(t, BomWastageSourceManual, got.Source,
			"неподтверждённый новый счётчик — то же чужое утверждение, что и новое число")
	})

	t.Run("явный manual — счётчик и штамп не переживают источник", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("22.00"),
			BomWastageProvenance{Source: BomWastageSourceManual}, true, false, now)
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual}, got)
	})

	t.Run("пустой источник на проводе нормализуется в manual", func(t *testing.T) {
		got := ResolveBomWastageProvenance(applied(4, then, "22.00"), d("22.00"), d("22.00"),
			BomWastageProvenance{}, true, false, now)
		require.Equal(t, BomWastageSourceManual, got.Source)
	})

	t.Run("строка старше 0296 (пустой stored source) читается как manual", func(t *testing.T) {
		got := ResolveBomWastageProvenance(BomWastageProvenance{}, d("8.00"), d("8.00"),
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, BomWastageSourceManual, got.Source)
	})

	// MAJOR 4 — рассинхронизированный бейдж (старый бинарь после отката DO переписал
	// wastage_percent, не тронув провенанс): verbatim-перенос НЕ воскрешает его, эхо — тоже.
	t.Run("бейдж, чей applied_percent разошёлся с числом, мёртв и для verbatim, и для эха", func(t *testing.T) {
		desynced := applied(4, then, "22.00") // …а в колонке процента уже 30.00
		got := ResolveBomWastageProvenance(desynced, d("30.00"), d("30.00"),
			BomWastageProvenance{}, false, false, now)
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual}, got,
			"молчание старого клиента не переносит бейдж, отброшенный самопроверкой")

		got = ResolveBomWastageProvenance(desynced, d("30.00"), d("30.00"),
			claim(4), true, false, now)
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual}, got,
			"эхо не воскрешает то, что чтение уже отбросило")
	})
}

// Самопроверка бейджа (MAJOR 4) — читатель хранимой строки.
func TestEffectiveBomWastageProvenance(t *testing.T) {
	d := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	at := sql.NullTime{Time: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), Valid: true}
	live := BomWastageProvenance{Source: BomWastageSourceLays,
		LayCount: sql.NullInt64{Int64: 4, Valid: true}, AppliedAt: at, AppliedPercent: d("22.00")}

	t.Run("совпавший applied_percent — бейдж жив целиком", func(t *testing.T) {
		require.Equal(t, live, EffectiveBomWastageProvenance(live, d("22.00")))
	})
	t.Run("значение правили мимо провенанса — бейдж читается как manual", func(t *testing.T) {
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual},
			EffectiveBomWastageProvenance(live, d("30.00")),
			"откат DO: старый бинарь переписал wastage_percent, не зная про провенанс, — бейдж обязан погаснуть, не поверить")
	})
	t.Run("бейдж без applied_percent — не бейдж", func(t *testing.T) {
		noAnchor := live
		noAnchor.AppliedPercent = decimal.NullDecimal{}
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual},
			EffectiveBomWastageProvenance(noAnchor, d("22.00")))
	})
	t.Run("manual и пустой источник проходят как manual без якоря", func(t *testing.T) {
		require.Equal(t, BomWastageProvenance{Source: BomWastageSourceManual},
			EffectiveBomWastageProvenance(BomWastageProvenance{}, d("8.00")))
	})
}
