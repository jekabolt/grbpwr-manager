package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"

	"github.com/shopspring/decimal"
)

// The tri-state is the whole design of the patch: absent must not be confused with cleared, or a
// client that predates a setting wipes it on every save.
func TestWorkshopSettingsPatchFromPbTriState(t *testing.T) {
	t.Run("absent field leaves the setting alone", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.CuttingTableLengthCm != nil {
			t.Fatalf("absent field must map to nil, got %+v", *p.CuttingTableLengthCm)
		}
		if !p.IsEmpty() {
			t.Error("a request naming nothing must produce an empty patch")
		}
	})

	t.Run("present but empty clears the setting", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			CuttingTableLengthCm: &pb_decimal.Decimal{},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.CuttingTableLengthCm == nil {
			t.Fatal("an explicitly present empty decimal must be a CLEAR, not an absence")
		}
		if p.CuttingTableLengthCm.Valid {
			t.Error("a clear must carry an invalid NullDecimal")
		}
		if p.IsEmpty() {
			t.Error("clearing is a real write, so the patch is not empty")
		}
	})

	t.Run("present with a number sets it", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			CuttingTableLengthCm: &pb_decimal.Decimal{Value: "612.50"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.CuttingTableLengthCm == nil || !p.CuttingTableLengthCm.Valid {
			t.Fatal("expected a valid value")
		}
		if !p.CuttingTableLengthCm.Decimal.Equal(decimal.RequireFromString("612.50")) {
			t.Errorf("value = %s, want 612.50", p.CuttingTableLengthCm.Decimal)
		}
	})

	t.Run("garbage is a field-tagged rejection", func(t *testing.T) {
		_, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			CuttingTableLengthCm: &pb_decimal.Decimal{Value: "six metres"},
		})
		ve, ok := err.(*entity.ValidationError)
		if !ok {
			t.Fatalf("expected *entity.ValidationError, got %T (%v)", err, err)
		}
		if ve.Field != "cutting_table_length_cm" {
			t.Errorf("field = %q", ve.Field)
		}
	})
}

// An unset setting must travel as an ABSENT field, never as a zero — a consumer that sees 0 would
// tell a workshop that has configured nothing that every раскладка is too long.
func TestWorkshopSettingsToPbLeavesUnsetAbsent(t *testing.T) {
	pb := WorkshopSettingsToPb(&entity.WorkshopSettings{})
	if pb.GetCuttingTableLengthCm() != nil {
		t.Errorf("unset must be absent on the wire, got %+v", pb.GetCuttingTableLengthCm())
	}
	if pb.GetUpdatedAt() != nil {
		t.Error("a zero timestamp must be absent, not 0001-01-01")
	}

	now := time.Now().UTC().Truncate(time.Second)
	pb = WorkshopSettingsToPb(&entity.WorkshopSettings{
		CuttingTableLengthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("600"), Valid: true},
		UpdatedBy:            "operator",
		UpdatedAt:            now,
	})
	if pb.GetCuttingTableLengthCm().GetValue() != "600" {
		t.Errorf("value = %q, want 600", pb.GetCuttingTableLengthCm().GetValue())
	}
	if pb.GetUpdatedBy() != "operator" {
		t.Errorf("updated_by = %q", pb.GetUpdatedBy())
	}
	if !pb.GetUpdatedAt().AsTime().Equal(now) {
		t.Errorf("updated_at = %v, want %v", pb.GetUpdatedAt().AsTime(), now)
	}

	if WorkshopSettingsToPb(nil) == nil {
		t.Error("a nil entity must still produce an empty message, not nil")
	}
}

// Ф4.8's tenant gets the same three states proved separately rather than by inspection: it is the
// fourth field to go through presentNullDecimal, and the one whose collapse would be least visible —
// nobody watches the предел стопки until a настил is being laid.
func TestWorkshopSettingsMaxStackHeightTriState(t *testing.T) {
	t.Run("absent leaves the limit alone", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			// A save of a NEIGHBOURING setting — exactly what a workshop tab holding a bundle from
			// before Ф4.8 sends on every edit of the table length.
			CuttingTableLengthCm: &pb_decimal.Decimal{Value: "600"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.MaxStackHeightCm != nil {
			t.Fatalf("an unmentioned limit must map to nil, got %+v", *p.MaxStackHeightCm)
		}
	})

	t.Run("present but empty clears the limit", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			MaxStackHeightCm: &pb_decimal.Decimal{},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.MaxStackHeightCm == nil {
			t.Fatal("an explicitly present empty decimal is a CLEAR, not an absence")
		}
		if p.MaxStackHeightCm.Valid {
			t.Error("a clear must carry an invalid NullDecimal")
		}
		// Clearing is how «у нас нет предела» is said — a real write, so the patch is not empty and
		// must not be refused with «name at least one setting».
		if p.IsEmpty() {
			t.Error("clearing the limit is a real write")
		}
	})

	t.Run("present with a number sets it", func(t *testing.T) {
		p, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			MaxStackHeightCm: &pb_decimal.Decimal{Value: "15.50"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.MaxStackHeightCm == nil || !p.MaxStackHeightCm.Valid {
			t.Fatal("expected a valid value")
		}
		if !p.MaxStackHeightCm.Decimal.Equal(decimal.RequireFromString("15.50")) {
			t.Errorf("value = %s, want 15.50", p.MaxStackHeightCm.Decimal)
		}
	})

	t.Run("garbage is a field-tagged rejection", func(t *testing.T) {
		_, err := WorkshopSettingsPatchFromPb(&pb_admin.UpdateWorkshopSettingsRequest{
			MaxStackHeightCm: &pb_decimal.Decimal{Value: "по колено"},
		})
		ve, ok := err.(*entity.ValidationError)
		if !ok {
			t.Fatalf("expected *entity.ValidationError, got %T (%v)", err, err)
		}
		if ve.Field != "max_stack_height_cm" {
			t.Errorf("field = %q", ve.Field)
		}
	})
}

// An unconfigured limit must travel as an ABSENT field. A "0" would reach the lay path as a real
// limit of zero centimetres and fail every настил in the shop — the mirror image of the table
// length's failure, and the reason this house writes presence rather than values.
func TestWorkshopSettingsToPbLeavesTheStackLimitAbsent(t *testing.T) {
	pb := WorkshopSettingsToPb(&entity.WorkshopSettings{})
	if pb.GetMaxStackHeightCm() != nil {
		t.Errorf("an unconfigured limit must be absent on the wire, got %+v", pb.GetMaxStackHeightCm())
	}

	pb = WorkshopSettingsToPb(&entity.WorkshopSettings{
		MaxStackHeightCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("15"), Valid: true},
	})
	if pb.GetMaxStackHeightCm().GetValue() != "15" {
		t.Errorf("value = %q, want 15", pb.GetMaxStackHeightCm().GetValue())
	}
}
