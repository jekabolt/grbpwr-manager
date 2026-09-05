package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestBomLineEstUsage покрывает 0365: ОЦЕНКА РАСХОДА НА ИЗДЕЛИЕ доезжает до колонки и обратно,
// «не прислали» оставляет её как лежит, явная пустота её очищает, а правка соседа её не трогает.
//
// ПОЧЕМУ ЭТИ ЧЕТЫРЕ, А НЕ «ещё немного покрытия». Карточка сохраняется ЦЕЛИКОМ: админка шлёт весь
// BOM на каждую правку, поэтому колонка, забытая в UPDATE, стирается у ВСЕХ строк карточки при
// первом же сохранении соседней ячейки — и притом бесследно, потому что оценки нет в подписи и
// NULL неотличим от «ещё не оценили». Колонка, забытая в SELECT, читается нулём и на круговом
// рейсе (прочитал → сохранил без правок) стирает сама себя.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ПРОБА ПРОВЕРЕНА (каждая прогнана и откачена):
//  1. убрать `bi.est_usage` из SELECT чтения — краснеет первое же чтение;
//  2. убрать `est_usage` из bomItemInsertQuery — краснеет первое же чтение;
//  3. заменить `est_usage=IF(:est_usage_omitted, est_usage, :est_usage)` на голое
//     `est_usage=:est_usage` — краснеет ветка «старый бандл не шлёт поля»: значение стирается.
func TestBomLineEstUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	const (
		slotCloth  = "01EUCLOTH00000000000000E1"
		slotThread = "01EUTHREAD0000000000000E2"
		slotZip    = "01EUZIPPER0000000000000E3"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	dec := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	bom := []entity.TechCardBomItem{
		{LineKey: slotCloth, Section: entity.BomSectionFabric, Name: "main fabric",
			Unit: ns("m"), EstUsage: dec("1.6")},
		// Нитка — мерная строка нерулонной секции: оценка законна и здесь (у неё нет семьи).
		{LineKey: slotThread, Section: entity.BomSectionThread, Name: "sewing thread",
			Unit: ns("m"), EstUsage: dec("150")},
		// Слот без оценки читается честно пустым, а не нулём: «не оценено» ≠ «нисколько».
		{LineKey: slotZip, Section: entity.BomSectionHardware, Name: "front zip", Unit: ns("pcs")},
	}

	mk := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "BOM EST USAGE", StyleNumber: ns("EU-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        items,
		}
	}

	tcID, err := T.AddTechCard(ctx, mk(bom))
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), tcID) })

	byKey := func() map[string]entity.TechCardBomItem {
		t.Helper()
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		m := make(map[string]entity.TechCardBomItem, len(tc.BomItems))
		for _, b := range tc.BomItems {
			m[b.LineKey] = b
		}
		require.Len(t, m, len(bom))
		return m
	}
	lockVersion := func() int {
		t.Helper()
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return tc.LockVersion
	}

	got := byKey()
	require.True(t, got[slotCloth].EstUsage.Valid)
	require.Equal(t, "1.6", got[slotCloth].EstUsage.Decimal.String())
	require.Equal(t, "150", got[slotThread].EstUsage.Decimal.String())
	require.False(t, got[slotZip].EstUsage.Valid, "неоценённая строка обязана читаться пустой, а не нулём")

	// ВКЛАДКА СО СТАРЫМ БАНДЛОМ: поля на проводе нет вовсе (EstUsageOmitted), правится сосед.
	// Колонка обязана остаться как лежит — иначе один сейв стирает оценку у всей карточки.
	stale := make([]entity.TechCardBomItem, len(bom))
	copy(stale, bom)
	for i := range stale {
		stale[i].EstUsage = decimal.NullDecimal{}
		stale[i].EstUsageOmitted = true
	}
	stale[2].Name = "front zip (YKK)"
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(stale), lockVersion()))

	got = byKey()
	require.Equal(t, "1.6", got[slotCloth].EstUsage.Decimal.String(),
		"отсутствие поля на проводе означает «не трогай», а не «очисти»")
	require.Equal(t, "150", got[slotThread].EstUsage.Decimal.String())
	require.Equal(t, "front zip (YKK)", got[slotZip].Name, "правка соседа обязана сохраниться")
	require.False(t, got[slotZip].EstUsage.Valid)

	// ЯВНАЯ ПУСТОТА = ОЧИСТИТЬ (омитед-флаг снят). Единственная дверь, через которую оценка
	// снимается со строки; без неё поле стало бы неочищаемым под upsert'ом.
	cleared := make([]entity.TechCardBomItem, len(bom))
	copy(cleared, bom)
	cleared[0].EstUsage = decimal.NullDecimal{}
	cleared[0].EstUsageOmitted = false
	cleared[1].EstUsage = dec("120.5")
	cleared[2].Name = "front zip (YKK)"
	cleared[2].EstUsage = dec("1")
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(cleared), lockVersion()))

	got = byKey()
	require.False(t, got[slotCloth].EstUsage.Valid, "явная пустота обязана очистить колонку")
	require.Equal(t, "120.5", got[slotThread].EstUsage.Decimal.String(), "правка значения доезжает")
	require.Equal(t, "1", got[slotZip].EstUsage.Decimal.String(), "оценка на счётной секции законна")

	// Три знака после точки — ровно столько хранит DECIMAL(12,3); четвёртый отвергает DTO, а не
	// колонка, поэтому здесь проверяется, что третий ВЫЖИВАЕТ (а не округляется до сотых).
	precise := make([]entity.TechCardBomItem, len(bom))
	copy(precise, bom)
	precise[0].EstUsage = dec("0.125")
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(precise), lockVersion()))
	require.Equal(t, "0.125", byKey()[slotCloth].EstUsage.Decimal.String())
}
