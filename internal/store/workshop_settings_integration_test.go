package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Ф2.5 «дом настроек цеха» (0272). The load-bearing property under test is that UNSET and ZERO stay
// distinguishable end to end: a workshop that has not configured a cutting table must read back as
// "no length", never as 0 cm, or the length verdict turns into "everything is too long".
func TestWorkshopSettingsUnsetIsNotZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The migration seeds the singleton with everything unset.
	_, err = testDB.ExecContext(ctx, `UPDATE workshop_settings SET cutting_table_length_cm = NULL WHERE id = 1`)
	require.NoError(t, err)

	got, err := s.Workshop().GetSettings(ctx)
	require.NoError(t, err)
	require.False(t, got.CuttingTableLengthCm.Valid, "an unconfigured table must read as unset, not as 0")

	// Setting it.
	set := decimal.NullDecimal{Decimal: decimal.RequireFromString("612.50"), Valid: true}
	got, err = s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &set}, "tester")
	require.NoError(t, err)
	require.True(t, got.CuttingTableLengthCm.Valid)
	require.True(t, got.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString("612.50")),
		"got %s", got.CuttingTableLengthCm.Decimal)
	require.Equal(t, "tester", got.UpdatedBy)
	require.False(t, got.UpdatedAt.IsZero())

	// Clearing it — an explicitly invalid value in the patch, not an absent one.
	cleared := decimal.NullDecimal{}
	got, err = s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &cleared}, "tester2")
	require.NoError(t, err)
	require.False(t, got.CuttingTableLengthCm.Valid, "clearing must return the setting to unset")

	reread, err := s.Workshop().GetSettings(ctx)
	require.NoError(t, err)
	require.False(t, reread.CuttingTableLengthCm.Valid)
}

// A patch that names no setting must leave every stored value alone. This is what protects a
// workshop screen shipped before a new tenant landed from wiping settings it has never heard of.
func TestWorkshopSettingsPatchIsPartial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	set := decimal.NullDecimal{Decimal: decimal.RequireFromString("800"), Valid: true}
	_, err = s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &set}, "setter")
	require.NoError(t, err)

	// An empty patch is refused outright rather than executed as a no-op write: it would stamp
	// updated_by/updated_at and put a fake edit in the audit trail.
	_, err = s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{}, "nobody")
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)

	after, err := s.Workshop().GetSettings(ctx)
	require.NoError(t, err)
	require.True(t, after.CuttingTableLengthCm.Valid, "a refused empty patch must not have wiped the value")
	require.True(t, after.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString("800")))
	require.Equal(t, "setter", after.UpdatedBy, "a refused patch must not restamp the audit fields")
}

// The store repeats the plausibility band before touching the DB, so the operator gets a readable
// field violation instead of a raw MySQL CHECK failure.
func TestWorkshopSettingsRejectsImplausibleTableLength(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	for _, bad := range []string{"0", "-5", "6", "60000"} {
		v := decimal.NullDecimal{Decimal: decimal.RequireFromString(bad), Valid: true}
		_, err := s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &v}, "tester")
		require.Error(t, err, "value %s must be refused", bad)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve, "value %s must be a field violation, not a raw DB error", bad)
		require.Equal(t, "cutting_table_length_cm", ve.Field)
	}
}

// The partial UPDATE writes through IF(:omitted, col, :val), which mixes a DECIMAL column with a
// bound value MySQL sees as a string. Pin the round trip at the band boundaries and at full scale so
// a silent coercion or a rounded-away digit cannot slip in.
func TestWorkshopSettingsRoundTripIsExact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	for _, want := range []string{"50", "5000", "4999.99", "612.34"} {
		v := decimal.NullDecimal{Decimal: decimal.RequireFromString(want), Valid: true}
		got, err := s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &v}, "tester")
		require.NoError(t, err, "value %s must be accepted at the band boundary", want)
		require.True(t, got.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString(want)),
			"wrote %s, read back %s", want, got.CuttingTableLengthCm.Decimal)

		reread, err := s.Workshop().GetSettings(ctx)
		require.NoError(t, err)
		require.True(t, reread.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString(want)),
			"wrote %s, re-read %s", want, reread.CuttingTableLengthCm.Decimal)
	}
}

// The schema, not just Go, has to refuse a zero or an absurd length — a manual UPDATE at the mysql
// prompt is exactly when the Go validator is not running.
func TestWorkshopSettingsColumnCheckRefusesZeroAndAbsurd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	for _, bad := range []string{"0", "-1", "5000.01"} {
		_, err := testDB.ExecContext(ctx,
			`UPDATE workshop_settings SET cutting_table_length_cm = ? WHERE id = 1`, bad)
		require.Error(t, err, "chk_workshop_settings_table_length must refuse %s", bad)
	}
	// NULL and a plausible value both pass.
	_, err = testDB.ExecContext(ctx, `UPDATE workshop_settings SET cutting_table_length_cm = NULL WHERE id = 1`)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE workshop_settings SET cutting_table_length_cm = 5000 WHERE id = 1`)
	require.NoError(t, err)

	// The singleton CHECK keeps a second configuration from appearing.
	_, err = testDB.ExecContext(ctx, `INSERT INTO workshop_settings (id) VALUES (2)`)
	require.Error(t, err, "chk_workshop_settings_singleton must refuse a second row")
}

// The row is recreated if it goes missing, so a database that lost the seed still accepts a write
// instead of silently updating nothing.
func TestWorkshopSettingsUpdateSelfHealsMissingRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	_, err = testDB.ExecContext(ctx, `DELETE FROM workshop_settings WHERE id = 1`)
	require.NoError(t, err)

	// A read of a missing singleton is "nothing configured", not an error.
	got, err := s.Workshop().GetSettings(ctx)
	require.NoError(t, err)
	require.False(t, got.CuttingTableLengthCm.Valid)

	set := decimal.NullDecimal{Decimal: decimal.RequireFromString("450"), Valid: true}
	got, err = s.Workshop().UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &set}, "healer")
	require.NoError(t, err)
	require.True(t, got.CuttingTableLengthCm.Valid)
	require.True(t, got.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString("450")))
}
