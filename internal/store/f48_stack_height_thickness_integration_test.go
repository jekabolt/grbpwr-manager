package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// f48Guard skips outside CI unless the DSN points at a local container. A bare local
// `go test ./internal/store/...` uses config.toml's PROD DSN, this suite runs Automigrate and its
// TestMain DROPS ALL TABLES on cleanup (mysql_test.go / project memory). The guard is copied per
// file rather than shared on purpose: a helper in another file is one rename away from silently
// not being called here.
func f48Guard(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

// TestMaterialFabricThicknessOmittedIsNotCleared is Ф4.8's write-presence acceptance test against
// the real UPDATE statement, and it is the same defect Ф5а.2 already caught once on the column next
// door: a save that does NOT carry the field must leave the stored value alone, while a save that
// carries an explicitly empty one must clear it.
//
// It has to be an integration test, because the whole defect lives in the SQL. A unit test on the
// DTO can only prove the flag is computed, not that the statement honours it — and here the silent
// failure is worse than a lost number: an erased thickness turns a live stack-height check back into
// UNKNOWN for every настил cut from that article, with nothing on any screen saying the measurement
// used to exist.
func TestMaterialFabricThicknessOmittedIsNotCleared(t *testing.T) {
	f48Guard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	base := func() *entity.MaterialInsert {
		return &entity.MaterialInsert{Name: "F4.8 Thickness Poplin", Section: "fabric", Unit: ns("m")}
	}

	id, err := tc.CreateMaterial(ctx, base())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", id)
	})

	// ПРИЁМОЧНАЯ ПРОБА СПЕКИ: an article nobody has measured must read back as UNKNOWN, not as 0.
	// «0 см, влезает» is the exact wrong answer — a verdict manufactured out of missing data.
	fresh, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.False(t, fresh.FabricThicknessMm.Valid,
		"an unmeasured article must read as UNSET, never as 0 mm, got %v", fresh.FabricThicknessMm)
	require.False(t, fresh.EffectiveFabricThicknessMm().Valid,
		"and the effective reading must withhold too — a 0 here would make every stack «0 см, влезает»")

	// The operator measures the cloth: 0.3 mm поплин, straight out of the field's hint text.
	ins := base()
	ins.FabricThicknessMm = nd("0.300")
	require.NoError(t, tc.UpdateMaterial(ctx, id, ins, 0))
	m, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.True(t, m.FabricThicknessMm.Valid && m.FabricThicknessMm.Decimal.Equal(decimal.RequireFromString("0.300")),
		"the measurement is stored, got %v", m.FabricThicknessMm)

	// THE LOAD-BEARING CASE. A save from a bundle that does not know the field — the stale tab, or any
	// client in the window between the backend and client deploys. It must not erase the measurement.
	omitted := base()
	omitted.Name = "F4.8 Thickness Poplin v2"
	omitted.FabricThicknessMmOmitted = true
	require.NoError(t, tc.UpdateMaterial(ctx, id, omitted, m.LockVersion))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "F4.8 Thickness Poplin v2", m.Name, "the rest of the save still lands")
	require.True(t, m.FabricThicknessMm.Valid && m.FabricThicknessMm.Decimal.Equal(decimal.RequireFromString("0.300")),
		"an ABSENT fabric_thickness_mm must not clear the stored one, got %v", m.FabricThicknessMm)

	// The two Ф4.8/Ф5а fields must be independently omissible — a shared mask would let one save
	// carry the coefficient and silently wipe the thickness with it.
	withCoefficient := base()
	withCoefficient.CuttingCoefficient = nd("1.06")
	withCoefficient.FabricThicknessMmOmitted = true
	require.NoError(t, tc.UpdateMaterial(ctx, id, withCoefficient, m.LockVersion))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.True(t, m.CuttingCoefficient.Valid && m.CuttingCoefficient.Decimal.Equal(decimal.RequireFromString("1.0600")),
		"the coefficient landed, got %v", m.CuttingCoefficient)
	require.True(t, m.FabricThicknessMm.Valid && m.FabricThicknessMm.Decimal.Equal(decimal.RequireFromString("0.300")),
		"writing the NEIGHBOURING dial must not disturb the thickness, got %v", m.FabricThicknessMm)

	// An explicit clear (the field present with an empty value) still works — otherwise an operator
	// who mistyped a thickness could never withdraw it, and a wrong measurement produces confident
	// wrong verdicts where an absent one produces none.
	cleared := base()
	cleared.FabricThicknessMm = decimal.NullDecimal{}
	require.NoError(t, tc.UpdateMaterial(ctx, id, cleared, m.LockVersion))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.False(t, m.FabricThicknessMm.Valid, "an explicit clear must store NULL, got %v", m.FabricThicknessMm)

	// And a create never consults the flag: absent and empty are the same NULL on INSERT.
	otherID, err := tc.CreateMaterial(ctx, func() *entity.MaterialInsert {
		i := base()
		i.Name = "F4.8 Thickness Poplin fresh"
		i.FabricThicknessMmOmitted = true
		return i
	}())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", otherID)
	})
	other, err := tc.GetMaterial(ctx, otherID)
	require.NoError(t, err)
	require.False(t, other.FabricThicknessMm.Valid, "a fresh material has no thickness, got %v", other.FabricThicknessMm)

	// A create that DOES carry a thickness must persist it — the INSERT column list is hand-written
	// and a field left out of it fails exactly this way and no other.
	measuredID, err := tc.CreateMaterial(ctx, func() *entity.MaterialInsert {
		i := base()
		i.Name = "F4.8 Thickness Melton"
		i.FabricThicknessMm = nd("2.500")
		return i
	}())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", measuredID)
	})
	measured, err := tc.GetMaterial(ctx, measuredID)
	require.NoError(t, err)
	require.True(t, measured.FabricThicknessMm.Valid &&
		measured.FabricThicknessMm.Decimal.Equal(decimal.RequireFromString("2.500")),
		"a thickness given at CREATE must survive the INSERT, got %v", measured.FabricThicknessMm)
}

// TestWorkshopMaxStackHeightOmittedIsNotCleared is the settings-side half of the same law. The
// workshop screen sends only the settings an edit actually touched, so every save of the table
// length arrives with the stack limit ABSENT — and a full-replace UPDATE would wipe it every time.
func TestWorkshopMaxStackHeightOmittedIsNotCleared(t *testing.T) {
	f48Guard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	ws := s.Workshop()

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	// Start from «не настроено», which is what the migration seeds.
	_, err = testDB.ExecContext(ctx, `UPDATE workshop_settings SET max_stack_height_cm = NULL WHERE id = 1`)
	require.NoError(t, err)
	got, err := ws.GetSettings(ctx)
	require.NoError(t, err)
	require.False(t, got.MaxStackHeightCm.Valid, "an unconfigured limit must read as unset, not as 0")
	require.False(t, got.EffectiveMaxStackHeightCm().Valid,
		"and the effective reading must withhold — a 0 here would fail every настил in the shop")

	// Configure it.
	limit := nd("15.00")
	got, err = ws.UpdateSettings(ctx, entity.WorkshopSettingsPatch{MaxStackHeightCm: &limit}, "cutter")
	require.NoError(t, err)
	require.True(t, got.MaxStackHeightCm.Valid && got.MaxStackHeightCm.Decimal.Equal(decimal.RequireFromString("15.00")),
		"the limit is stored, got %v", got.MaxStackHeightCm)

	// THE LOAD-BEARING CASE: an ordinary edit of a NEIGHBOURING setting, which is every save the
	// workshop screen makes when the operator only touched the table length.
	table := nd("600")
	got, err = ws.UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &table}, "cutter")
	require.NoError(t, err)
	require.True(t, got.MaxStackHeightCm.Valid && got.MaxStackHeightCm.Decimal.Equal(decimal.RequireFromString("15.00")),
		"a patch that does not name the limit must leave it standing, got %v", got.MaxStackHeightCm)

	// Clearing is explicit and must still work: a shop that typed 3 for 30 has to be able to return
	// the setting to «не настроено» rather than leave a wrong limit failing honest настилы.
	cleared := decimal.NullDecimal{}
	got, err = ws.UpdateSettings(ctx, entity.WorkshopSettingsPatch{MaxStackHeightCm: &cleared}, "cutter")
	require.NoError(t, err)
	require.False(t, got.MaxStackHeightCm.Valid, "clearing must return the limit to unset, got %v", got.MaxStackHeightCm)
	require.True(t, got.CuttingTableLengthCm.Valid, "clearing one setting must not touch its neighbour")

	// A patch naming ONLY the limit is a real write and must not be refused as empty — the trap
	// entity.WorkshopSettingsPatch.IsEmpty names, proved here against the live statement.
	again := nd("30")
	got, err = ws.UpdateSettings(ctx, entity.WorkshopSettingsPatch{MaxStackHeightCm: &again}, "cutter")
	require.NoError(t, err)
	require.True(t, got.MaxStackHeightCm.Valid && got.MaxStackHeightCm.Decimal.Equal(decimal.RequireFromString("30.00")),
		"a patch naming only the limit must be accepted, got %v", got.MaxStackHeightCm)

	// ZERO IS REFUSED before MySQL's named CHECK can turn it into a raw 500, and it is refused with a
	// field-tagged violation the screen can pin on the control. A 0 cm limit fails every настил ever
	// laid; «предела нет» is the CLEAR above, which withholds the verdict instead of failing it.
	zero := nd("0")
	_, err = ws.UpdateSettings(ctx, entity.WorkshopSettingsPatch{MaxStackHeightCm: &zero}, "cutter")
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "max_stack_height_cm", ve.Field)

	after, err := ws.GetSettings(ctx)
	require.NoError(t, err)
	require.True(t, after.MaxStackHeightCm.Decimal.Equal(decimal.RequireFromString("30.00")),
		"a refused write must not have disturbed the stored limit, got %v", after.MaxStackHeightCm)
}
