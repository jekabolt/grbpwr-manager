package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardOperationIconFieldsRoundTrip проводит иконные поля 0436 через стор: seam_kind,
// work_variant, button_holes шага и button_holes пуговицы-артикула. Записал -> прочитал -> совпало;
// шаг без значений читается NULL, а не нулём и не пустой строкой. Ловит разъезд пяти списков
// (entity, ALTER, INSERT-map, SELECT, дайджест), который компилятору не виден.
//
// SAFE ONLY against a local container DSN (store tests DROP tables of their DSN).
func TestTechCardOperationIconFieldsRoundTrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	{
		di, derr := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, derr)
		hf, herr := s.Hero().GetHero(ctx)
		require.NoError(t, herr)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	bound := entity.TechCardOperation{
		OperationNumber: ni(10), OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"), Zone: entity.ZoneOuter,
		SeamClass: ns("bs_bound"), BindingStyle: ns("double_fold"), SeamKind: ns("bound_closed"),
	}
	button := entity.TechCardOperation{
		OperationNumber: ni(20), OperationType: entity.OpTypeMachine, MachineType: ns("button_attach"), Zone: entity.ZoneOuter,
		AttachPattern: ns("cross_x"), ButtonHoles: ni(4),
	}
	zip := entity.TechCardOperation{
		OperationNumber: ni(30), OperationType: entity.OpTypeMachine, MachineType: ns("zipper_setting"), Zone: entity.ZoneOuter,
		Work: ns("set_zip"), WorkVariant: ns("two_way"),
	}
	bare := entity.TechCardOperation{
		OperationNumber: ni(40), OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"), Zone: entity.ZoneOuter,
	}

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Operation Icon Fields Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("OPI-RT-1"), Purpose: entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SeasonCode: ns("SS"), SeasonYear: ni(2026),
		Operations: []entity.TechCardOperation{bound, button, zip, bare},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	got, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	byNum := map[int32]entity.TechCardOperation{}
	for _, o := range got.Operations {
		byNum[o.OperationNumber.Int32] = o
	}
	require.Equal(t, ns("bound_closed"), byNum[10].SeamKind)
	require.Equal(t, ni(4), byNum[20].ButtonHoles)
	require.Equal(t, ns("two_way"), byNum[30].WorkVariant)
	require.Equal(t, ns("set_zip"), byNum[30].Work)
	for _, n := range []int32{10, 20, 30, 40} {
		o := byNum[n]
		if n != 10 {
			require.False(t, o.SeamKind.Valid, "step %d: seam_kind must read NULL", n)
		}
		if n != 20 {
			require.False(t, o.ButtonHoles.Valid, "step %d: button_holes must read NULL", n)
		}
		if n != 30 {
			require.False(t, o.WorkVariant.Valid, "step %d: work_variant must read NULL", n)
		}
	}

	// Пуговица-артикул: отверстия в material_hardware_attr рядом с diameter_mm.
	matID, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Icon Fields Button", Section: "hardware", MaterialClass: "hardware",
		CreatedBy: "alice", UpdatedBy: "alice",
		HardwareAttr: &entity.MaterialHardwareAttr{Finish: ns("horn"), ButtonHoles: ni(2)},
	})
	require.NoError(t, err)
	m, err := T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.NotNil(t, m.HardwareAttr)
	require.Equal(t, ni(2), m.HardwareAttr.ButtonHoles)

	require.NoError(t, T.UpdateMaterial(ctx, matID, &entity.MaterialInsert{
		Name: "Icon Fields Button", Section: "hardware", MaterialClass: "hardware", UpdatedBy: "bob",
		HardwareAttr: &entity.MaterialHardwareAttr{Finish: ns("horn")},
	}, 0))
	m, err = T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.NotNil(t, m.HardwareAttr)
	require.False(t, m.HardwareAttr.ButtonHoles.Valid, "an unset hole count must read NULL")

	// Повтор 0436 поверх применённой схемы (падение посреди файла → следующий старт с верха):
	// каждый гейт обязан пропустить свою колонку, и уже записанные значения не трогаются.
	for i := range 2 {
		_, err := testDB.ExecContext(ctx, `DELETE FROM gorp_migrations WHERE id = '0436_operation_icon_fields.sql'`)
		require.NoError(t, err)
		require.NoError(t, Migrate(testDB), "re-applying 0436 over an applied schema (pass %d)", i+1)
	}
	again, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	for _, o := range again.Operations {
		switch o.OperationNumber.Int32 {
		case 10:
			require.Equal(t, ns("bound_closed"), o.SeamKind)
		case 20:
			require.Equal(t, ni(4), o.ButtonHoles)
		case 30:
			require.Equal(t, ns("two_way"), o.WorkVariant)
		}
	}
}
