package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func wsND(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

func TestValidateCuttingTableLengthCm(t *testing.T) {
	cases := []struct {
		name    string
		in      decimal.NullDecimal
		wantErr bool
		reason  string
	}{
		// Clearing the setting is legal — only a value being SET has to be plausible.
		{name: "unset is accepted", in: decimal.NullDecimal{}},
		{name: "typical atelier table", in: wsND("600")},
		{name: "floor boundary", in: wsND("50")},
		{name: "ceiling boundary", in: wsND("5000")},
		{name: "two decimals", in: wsND("612.50")},

		{name: "zero is not a table", in: wsND("0"), wantErr: true, reason: "must_be_positive"},
		{name: "negative", in: wsND("-1"), wantErr: true, reason: "must_be_positive"},
		// The load-bearing case: metres typed into a field labelled centimetres. A bare "> 0"
		// rule accepts this and then declares every раскладка too long.
		{name: "metres typed as centimetres", in: wsND("6"), wantErr: true, reason: "implausibly_short"},
		{name: "just under the floor", in: wsND("49.99"), wantErr: true, reason: "implausibly_short"},
		{name: "stray zero", in: wsND("60000"), wantErr: true, reason: "implausibly_long"},
		{name: "millimetres typed as centimetres", in: wsND("6000"), wantErr: true, reason: "implausibly_long"},
		// DECIMAL(10,2) would silently round a third digit away on the round trip.
		{name: "over-precise", in: wsND("600.005"), wantErr: true, reason: "too_many_decimal_places"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCuttingTableLengthCm(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a rejection, got nil")
				}
				ve, ok := err.(*ValidationError)
				if !ok {
					t.Fatalf("expected *ValidationError, got %T", err)
				}
				if ve.Field != "cutting_table_length_cm" {
					t.Errorf("field = %q, want cutting_table_length_cm", ve.Field)
				}
				if ve.Reason != tc.reason {
					t.Errorf("reason = %q, want %q", ve.Reason, tc.reason)
				}
				if ve.HowToFix == "" {
					t.Error("a rejection must tell the operator how to fix it")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

// The plausibility band must stay inside the DB CHECK of migration 0272, or a value Go accepts
// would be refused by MySQL as a raw 500 instead of a readable field violation.
func TestCuttingTableLengthBandFitsTheColumnCheck(t *testing.T) {
	if MinCuttingTableLengthCm <= 0 {
		t.Errorf("floor %d must be positive (chk_workshop_settings_table_length requires > 0)", MinCuttingTableLengthCm)
	}
	if MaxCuttingTableLengthCm != 5000 {
		t.Errorf("ceiling %d drifted from chk_workshop_settings_table_length in 0272 (5000); move both together",
			MaxCuttingTableLengthCm)
	}
	if MinCuttingTableLengthCm >= MaxCuttingTableLengthCm {
		t.Error("floor must be below the ceiling")
	}
}

func TestWorkshopSettingsPatchIsEmpty(t *testing.T) {
	if !(WorkshopSettingsPatch{}).IsEmpty() {
		t.Error("a patch naming nothing must report empty")
	}
	// A patch that CLEARS a setting names it, so it is not empty — clearing is a real write.
	cleared := decimal.NullDecimal{}
	if (WorkshopSettingsPatch{CuttingTableLengthCm: &cleared}).IsEmpty() {
		t.Error("a patch that clears a setting is not empty")
	}
	set := wsND("600")
	if (WorkshopSettingsPatch{CuttingTableLengthCm: &set}).IsEmpty() {
		t.Error("a patch that sets a setting is not empty")
	}
}
