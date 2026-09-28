package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestBomLineLabelPart покрывает ЧАСТЬ СОСТАВНИКА (label_part) на строке BOM: значение доезжает до
// колонки и обратно, «не прислали» оставляет его как лежит, явный UNSPECIFIED (NULL, флаг снят)
// очищает, а правка соседа его не трогает. Карточка сохраняется ЦЕЛИКОМ, поэтому колонка, забытая
// в UPDATE или защищённая не тем флагом, стирается у ВСЕХ строк на первом же сейве вкладки со старым
// бандлом — бесследно (поле вне подписи, NULL неотличим от «авто»).
//
// ⚠️ ЗАПУСК ТОЛЬКО В ОДНОРАЗОВОМ КОНТЕЙНЕРЕ (CI=1 + MYSQL_*), никогда голым `go test` — TestMain
// этого пакета без CI идёт в базу из config.toml и дропает таблицы.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ПРОБА ПРОВЕРЕНА (каждая прогнана и откачена):
//  1. убрать `bi.label_part` из SELECT чтения — краснеет первое же чтение;
//  2. убрать `label_part` из bomItemInsertQuery — краснеет первое же чтение;
//  3. заменить `label_part=IF(:label_part_omitted, label_part, :label_part)` на голое
//     `label_part=:label_part` — краснеет ветка «старый бандл не шлёт поля»: значение стирается.
func TestBomLineLabelPart(t *testing.T) {
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
		slotShell  = "01LPSHELL00000000000000L1"
		slotSleeve = "01LPSLEEVE0000000000000L2"
		slotZip    = "01LPZIPPER0000000000000L3"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

	bom := []entity.TechCardBomItem{
		{LineKey: slotShell, Section: entity.BomSectionFabric, Name: "main fabric", LabelPart: ns("shell")},
		{LineKey: slotSleeve, Section: entity.BomSectionLining, Name: "sleeve lining", LabelPart: ns("sleeve_lining")},
		// Строка без выбора читается NULL — «авто», дефолт выводит клиент.
		{LineKey: slotZip, Section: entity.BomSectionHardware, Name: "front zip"},
	}

	mk := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "BOM LABEL PART", StyleNumber: ns("LP-1"),
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
	require.Equal(t, ns("shell"), got[slotShell].LabelPart, "INSERT + SELECT доносят значение")
	require.Equal(t, ns("sleeve_lining"), got[slotSleeve].LabelPart)
	require.False(t, got[slotZip].LabelPart.Valid, "невыбранная часть обязана читаться NULL («авто»)")

	// ВКЛАДКА СО СТАРЫМ БАНДЛОМ: поля на проводе нет (LabelPartOmitted), правится сосед.
	stale := make([]entity.TechCardBomItem, len(bom))
	copy(stale, bom)
	for i := range stale {
		stale[i].LabelPart = sql.NullString{}
		stale[i].LabelPartOmitted = true
	}
	stale[2].Name = "front zip (YKK)"
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(stale), lockVersion()))

	got = byKey()
	require.Equal(t, ns("shell"), got[slotShell].LabelPart,
		"отсутствие поля на проводе означает «не трогай», а не «очисти»")
	require.Equal(t, ns("sleeve_lining"), got[slotSleeve].LabelPart)
	require.Equal(t, "front zip (YKK)", got[slotZip].Name, "правка соседа обязана сохраниться")

	// ЯВНОЕ «АВТО» = ОЧИСТИТЬ (флаг снят, NULL), и правка значения доезжает через UPDATE.
	edited := make([]entity.TechCardBomItem, len(bom))
	copy(edited, bom)
	edited[0].LabelPart = sql.NullString{}
	edited[1].LabelPart = ns("body_lining")
	edited[2].Name = "front zip (YKK)"
	edited[2].LabelPart = ns("not_on_label")
	require.NoError(t, T.UpdateTechCard(ctx, tcID, mk(edited), lockVersion()))

	got = byKey()
	require.False(t, got[slotShell].LabelPart.Valid, "явный UNSPECIFIED обязан очистить колонку")
	require.Equal(t, ns("body_lining"), got[slotSleeve].LabelPart, "правка значения доезжает")
	require.Equal(t, ns("not_on_label"), got[slotZip].LabelPart, "часть законна на любой секции")
}
