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
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestTechCardOperationKindsRoundTrip проводит все 32 колонки волны 0324 через стор: записал ->
// прочитал -> значения совпали. Проверяется ровно то, что четыре списка (ALTER миграции, named-map
// INSERT'а, SELECT операций, поля entity.TechCardOperation) сошлись: разъезд между ними компилятору
// не виден и молчит до первого сохранения.
//
// SAFE ONLY against a local container DSN — see mysql_test.go / project memory
// (store-tests-drop-prod-db: the non-CI TestMain talks to the configured prod DB and DROPs tables).
func TestTechCardOperationKindsRoundTrip(t *testing.T) {
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
	nd := func(v string) decimal.NullDecimal {
		return decimal.NewNullDecimal(decimal.RequireFromString(v))
	}
	// Децимал обязан приехать СТРОКОЙ. Сравнение идёт по .String(), а не по .Equal и не по float:
	// половина значений ниже (6.4, 1.15, 12.3, 35.7, 4.3, 6.7, 3.1, 18.3, 12.7, 85.3) в двоичной
	// плавающей точке непредставима, поэтому маршрут через float64 разъедется здесь же, а не в цеху.
	eqDec := func(t *testing.T, want string, got decimal.NullDecimal, what string) {
		t.Helper()
		require.True(t, got.Valid, "%s must round-trip as set", what)
		require.Equal(t, want, got.Decimal.String(), "%s must round-trip as an exact decimal string", what)
	}

	// Шаг со ВСЕМИ 32 заполненными. Применимость (какой глагол какое поле имеет право нести) — второй
	// эшелон, Go-валидация в internal/dto; стор её не выполняет, а CHECK'и схемы одноколоночные, так
	// что заполненными законно оказываются все 32 сразу. Именно это здесь и нужно: цель — провод, а
	// не правила.
	full := entity.TechCardOperation{
		OperationNumber: ni(10), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
		SMV: nd("1.2"), Note: ns("шаг со всеми 32 полями волны"),

		NeedleCount:   ni(4),
		NeedleGaugeMm: nd("6.4"),
		SeamSecuring:  ns("backtack"),
		RowSpacingMm:  nd("12.3"),
		FullnessRatio: nd("1.15"),

		PlacementCount: ni(6),
		PitchMm:        nd("85.3"),

		AttachMethod:     ns("threaded"),
		HolePrep:         ns("punch"),
		Reinforcement:    ns("fusible_patch"),
		FoldbackMm:       nd("35.7"),
		CycleStitchCount: ni(42),

		PrintMethod:    ns("heat_transfer"),
		PeelMode:       ns("warm"),
		SecondPressSec: ni(8),
		PressureScale:  ns("firm"),

		AirTemperatureC: ni(480),
		FeedSpeedMMin:   nd("4.3"),

		TrimAction:          ns("notch_convex"),
		ResidualAllowanceMm: nd("6.7"),

		ResidualTailMaxMm: nd("3.1"),

		CleaningKind:   ns("adhesive_removal"),
		CoverageMode:   ns("sample_per_bundle"),
		WetProcessKind: ns("garment_dye"),

		ButtonholeStyle:       ns("round_end"),
		CutLengthMm:           nd("18.3"),
		ButtonholeOrientation: ns("horizontal"),
		BartackLengthMm:       nd("12.7"),
		AttachPattern:         ns("cross_x"),
		ZipperApplication:     ns("in_seam_pocket"),
		BindingStyle:          ns("double_fold"),
		LabelAttachStitch:     ns("two_sides_top_bottom"),
	}

	// Шаг без единого нового поля — старая карточка, какой её пишет прод сегодня. Все 32 обязаны
	// прочитаться Valid=false, а не нулями и не пустыми строками.
	bare := entity.TechCardOperation{
		OperationNumber: ni(20), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
		SMV: nd("0.4"), Note: ns("шаг без единого поля волны"),
	}

	card := &entity.TechCardInsert{
		Name: "Operation Kinds Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("OPK-RT-1"), Purpose: entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SeasonCode: ns("SS"), SeasonYear: ni(2026),
		Operations: []entity.TechCardOperation{full, bare},
	}

	tcID, err := T.AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	got, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	byNumber := make(map[int32]entity.TechCardOperation, len(got.Operations))
	for _, o := range got.Operations {
		byNumber[o.OperationNumber.Int32] = o
	}
	require.Len(t, byNumber, 2)

	// --- A. round-trip всех 32 ----------------------------------------------------------------------
	f := byNumber[10]
	require.Equal(t, int32(4), f.NeedleCount.Int32)
	require.True(t, f.NeedleCount.Valid)
	eqDec(t, "6.4", f.NeedleGaugeMm, "needle_gauge_mm")
	require.Equal(t, "backtack", f.SeamSecuring.String)
	eqDec(t, "12.3", f.RowSpacingMm, "row_spacing_mm")
	eqDec(t, "1.15", f.FullnessRatio, "fullness_ratio")

	require.Equal(t, int32(6), f.PlacementCount.Int32)
	eqDec(t, "85.3", f.PitchMm, "pitch_mm")

	require.Equal(t, "threaded", f.AttachMethod.String)
	require.Equal(t, "punch", f.HolePrep.String)
	require.Equal(t, "fusible_patch", f.Reinforcement.String)
	eqDec(t, "35.7", f.FoldbackMm, "foldback_mm")
	require.Equal(t, int32(42), f.CycleStitchCount.Int32)

	require.Equal(t, "heat_transfer", f.PrintMethod.String)
	require.Equal(t, "warm", f.PeelMode.String)
	require.Equal(t, int32(8), f.SecondPressSec.Int32)
	require.Equal(t, "firm", f.PressureScale.String)

	require.Equal(t, int32(480), f.AirTemperatureC.Int32)
	eqDec(t, "4.3", f.FeedSpeedMMin, "feed_speed_m_min")

	require.Equal(t, "notch_convex", f.TrimAction.String)
	eqDec(t, "6.7", f.ResidualAllowanceMm, "residual_allowance_mm")

	eqDec(t, "3.1", f.ResidualTailMaxMm, "residual_tail_max_mm")

	require.Equal(t, "adhesive_removal", f.CleaningKind.String)
	require.Equal(t, "sample_per_bundle", f.CoverageMode.String)
	require.Equal(t, "garment_dye", f.WetProcessKind.String)

	require.Equal(t, "round_end", f.ButtonholeStyle.String)
	eqDec(t, "18.3", f.CutLengthMm, "cut_length_mm")
	require.Equal(t, "horizontal", f.ButtonholeOrientation.String)
	eqDec(t, "12.7", f.BartackLengthMm, "bartack_length_mm")
	require.Equal(t, "cross_x", f.AttachPattern.String)
	require.Equal(t, "in_seam_pocket", f.ZipperApplication.String)
	require.Equal(t, "double_fold", f.BindingStyle.String)
	require.Equal(t, "two_sides_top_bottom", f.LabelAttachStitch.String)

	// Ни одна строка не обрезана шириной колонки: два самых длинных токена волны сидят ровно в край
	// (two_sides_top_bottom = 20 при VARCHAR(24), sample_per_bundle = 17 при VARCHAR(20)).
	require.Len(t, f.LabelAttachStitch.String, 20)
	require.Len(t, f.CoverageMode.String, 17)

	// --- B. шаг без единого нового поля: 32 NULL читаются как Valid=false ----------------------------
	assertAllUnset := func(t *testing.T, o entity.TechCardOperation, what string) {
		t.Helper()
		strs := map[string]sql.NullString{
			"seam_securing": o.SeamSecuring, "attach_method": o.AttachMethod,
			"hole_prep": o.HolePrep, "reinforcement": o.Reinforcement,
			"print_method": o.PrintMethod, "peel_mode": o.PeelMode,
			"pressure_scale": o.PressureScale, "trim_action": o.TrimAction,
			"cleaning_kind": o.CleaningKind, "coverage_mode": o.CoverageMode,
			"wet_process_kind": o.WetProcessKind, "buttonhole_style": o.ButtonholeStyle,
			"buttonhole_orientation": o.ButtonholeOrientation, "attach_pattern": o.AttachPattern,
			"zipper_application": o.ZipperApplication, "binding_style": o.BindingStyle,
			"label_attach_stitch": o.LabelAttachStitch,
		}
		ints := map[string]sql.NullInt32{
			"needle_count": o.NeedleCount, "placement_count": o.PlacementCount,
			"cycle_stitch_count": o.CycleStitchCount, "second_press_sec": o.SecondPressSec,
			"air_temperature_c": o.AirTemperatureC,
		}
		decs := map[string]decimal.NullDecimal{
			"needle_gauge_mm": o.NeedleGaugeMm, "row_spacing_mm": o.RowSpacingMm,
			"fullness_ratio": o.FullnessRatio, "pitch_mm": o.PitchMm,
			"foldback_mm": o.FoldbackMm, "feed_speed_m_min": o.FeedSpeedMMin,
			"residual_allowance_mm": o.ResidualAllowanceMm,
			"residual_tail_max_mm":  o.ResidualTailMaxMm, "cut_length_mm": o.CutLengthMm,
			"bartack_length_mm": o.BartackLengthMm,
		}
		require.Equal(t, 32, len(strs)+len(ints)+len(decs), "все 32 колонки волны обязаны быть перечислены здесь")
		for name, v := range strs {
			require.False(t, v.Valid, "%s: %s должен остаться НЕ УКАЗАН, а не пустой строкой", what, name)
			require.Empty(t, v.String, "%s: %s", what, name)
		}
		for name, v := range ints {
			require.False(t, v.Valid, "%s: %s должен остаться НЕ УКАЗАН, а не нулём", what, name)
			require.Zero(t, v.Int32, "%s: %s", what, name)
		}
		for name, v := range decs {
			require.False(t, v.Valid, "%s: %s должен остаться НЕ УКАЗАН, а не нулём", what, name)
		}
	}
	assertAllUnset(t, byNumber[20], "шаг без полей волны")

	// --- C. НАСТОЯЩАЯ старая строка: писана мимо стора, о новых колонках не упоминает вовсе -----------
	// Отличается от B тем, что B пишет 32 явных NULL через стор, а здесь колонки не названы в INSERT'е
	// ни разу — так выглядит строка, записанная бинарём до этой волны. Разбор обязан пережить и это.
	var legacyOpID int64
	{
		res, execErr := testDB.ExecContext(ctx, `
			INSERT INTO tech_card_operation (tech_card_id, operation_number, operation_type, zone, display_order, note)
			VALUES (?, ?, ?, ?, ?, ?)`, tcID, 30, string(entity.OpTypeMachine), string(entity.ZoneOuter), 99, "строка до волны 0324")
		require.NoError(t, execErr)
		legacyOpID, err = res.LastInsertId()
		require.NoError(t, err)
		require.NotZero(t, legacyOpID)
	}
	var nullCount int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT (needle_count IS NULL) + (needle_gauge_mm IS NULL) + (seam_securing IS NULL)
		     + (row_spacing_mm IS NULL) + (fullness_ratio IS NULL) + (placement_count IS NULL)
		     + (pitch_mm IS NULL) + (attach_method IS NULL) + (hole_prep IS NULL)
		     + (reinforcement IS NULL) + (foldback_mm IS NULL) + (cycle_stitch_count IS NULL)
		     + (print_method IS NULL) + (peel_mode IS NULL) + (second_press_sec IS NULL)
		     + (pressure_scale IS NULL) + (air_temperature_c IS NULL) + (feed_speed_m_min IS NULL)
		     + (trim_action IS NULL) + (residual_allowance_mm IS NULL) + (residual_tail_max_mm IS NULL)
		     + (cleaning_kind IS NULL) + (coverage_mode IS NULL) + (wet_process_kind IS NULL)
		     + (buttonhole_style IS NULL) + (cut_length_mm IS NULL) + (buttonhole_orientation IS NULL)
		     + (bartack_length_mm IS NULL) + (attach_pattern IS NULL) + (zipper_application IS NULL)
		     + (binding_style IS NULL) + (label_attach_stitch IS NULL)
		FROM tech_card_operation WHERE id = ?`, legacyOpID).Scan(&nullCount))
	require.Equal(t, 32, nullCount, "строка, писанная мимо волны, обязана нести 32 NULL — без DEFAULT'ов")

	reread, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	var legacy *entity.TechCardOperation
	for i := range reread.Operations {
		if reread.Operations[i].OperationNumber.Int32 == 30 {
			legacy = &reread.Operations[i]
		}
	}
	require.NotNil(t, legacy, "старая строка обязана прочитаться, а не уронить разбор")
	require.Equal(t, "строка до волны 0324", legacy.Note.String)
	assertAllUnset(t, *legacy, "строка до волны 0324")

	// --- D. полная замена списка операций не теряет 32 колонки ---------------------------------------
	require.NoError(t, T.UpdateTechCard(ctx, tcID, &entity.TechCardInsert{
		Name: "Operation Kinds Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("OPK-RT-1"), Purpose: entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SeasonCode: ns("SS"), SeasonYear: ni(2026),
		Operations: []entity.TechCardOperation{full},
	}, got.LockVersion))

	after, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, after.Operations, 1)
	a := after.Operations[0]
	eqDec(t, "1.15", a.FullnessRatio, "fullness_ratio после полной замены")
	eqDec(t, "85.3", a.PitchMm, "pitch_mm после полной замены")
	require.Equal(t, "two_sides_top_bottom", a.LabelAttachStitch.String)
	require.Equal(t, int32(480), a.AirTemperatureC.Int32)
}
