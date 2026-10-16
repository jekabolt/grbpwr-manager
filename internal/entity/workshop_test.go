package entity

import (
	"database/sql"
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

func TestValidateMaxStackHeightCm(t *testing.T) {
	cases := []struct {
		name    string
		in      decimal.NullDecimal
		wantErr bool
		reason  string
	}{
		// Clearing is legal and is the ONLY way to say «предела нет» — see the zero case below.
		{name: "unset is accepted", in: decimal.NullDecimal{}},
		{name: "chiffon shop with a light blade", in: wsND("2")},
		{name: "typical straight knife", in: wsND("15")},
		{name: "the longest knives", in: wsND("30")},
		{name: "ceiling boundary", in: wsND("100")},
		{name: "two decimals", in: wsND("12.50")},

		// A 0 cm limit fails every настил ever laid. «У нас нет предела» is said by CLEARING the
		// setting — that withholds the verdict, where a zero manufactures a false one.
		{name: "zero refuses every lay", in: wsND("0"), wantErr: true, reason: "must_be_positive"},
		{name: "negative", in: wsND("-1"), wantErr: true, reason: "must_be_positive"},
		{name: "millimetres typed as centimetres", in: wsND("150"), wantErr: true, reason: "implausibly_tall"},
		{name: "stray zero", in: wsND("3000"), wantErr: true, reason: "implausibly_tall"},
		// DECIMAL(6,2) would silently round a third digit away on the round trip.
		{name: "over-precise", in: wsND("15.005"), wantErr: true, reason: "too_many_decimal_places"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMaxStackHeightCm(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a rejection, got nil")
				}
				ve, ok := err.(*ValidationError)
				if !ok {
					t.Fatalf("expected *ValidationError, got %T", err)
				}
				if ve.Field != "max_stack_height_cm" {
					t.Errorf("field = %q, want max_stack_height_cm", ve.Field)
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

// The plausibility band must stay inside the DB CHECK of migration 0283, or a value Go accepts would
// be refused by MySQL as a raw 500 instead of a readable field violation.
func TestMaxStackHeightBandFitsTheColumnCheck(t *testing.T) {
	if MaxStackHeightCm != 100 {
		t.Errorf("ceiling %d drifted from chk_workshop_settings_stack_height in 0283 (100); move both together",
			MaxStackHeightCm)
	}
}

// EffectiveMaxStackHeightCm is the ONE place «не настроено» is decided, and it must never hand back a
// number a caller could compare against: an unset limit means the verdict is withheld, not that every
// настил fails.
func TestEffectiveMaxStackHeightCmWithholdsRatherThanZeroes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    decimal.NullDecimal
		valid bool
	}{
		{name: "never configured", in: decimal.NullDecimal{}},
		{name: "zero is not a limit", in: wsND("0")},
		{name: "negative is not a limit", in: wsND("-5")},
		{name: "configured", in: wsND("15"), valid: true},
	} {
		s := &WorkshopSettings{MaxStackHeightCm: tc.in}
		if got := s.EffectiveMaxStackHeightCm(); got.Valid != tc.valid {
			t.Errorf("%s: valid = %v, want %v (%+v)", tc.name, got.Valid, tc.valid, got)
		}
	}
	if (&WorkshopSettings{}).EffectiveMaxStackHeightCm().Valid {
		t.Error("an empty settings row configures nothing")
	}
	if (*WorkshopSettings)(nil).EffectiveMaxStackHeightCm().Valid {
		t.Error("a nil settings row must read as «не настроено», not panic")
	}
}

func TestValidateCuttingTableWidthCm(t *testing.T) {
	cases := []struct {
		name   string
		in     decimal.NullDecimal
		reason string
	}{
		{name: "unset is accepted", in: decimal.NullDecimal{}},
		{name: "typical table", in: wsND("180")},
		{name: "floor boundary", in: wsND("50")},
		{name: "ceiling boundary", in: wsND("1000")},
		{name: "two decimals", in: wsND("182.50")},
		{name: "zero", in: wsND("0"), reason: "must_be_positive"},
		{name: "negative", in: wsND("-1"), reason: "must_be_positive"},
		{name: "metres typed as centimetres", in: wsND("1.8"), reason: "implausibly_narrow"},
		{name: "millimetres typed as centimetres", in: wsND("1800"), reason: "implausibly_wide"},
		{name: "over-precise", in: wsND("180.005"), reason: "too_many_decimal_places"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCuttingTableWidthCm(tc.in)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("unexpected rejection: %v", err)
				}
				return
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
			}
			if ve.Field != "cutting_table_width_cm" || ve.Reason != tc.reason {
				t.Errorf("got %s/%s, want cutting_table_width_cm/%s", ve.Field, ve.Reason, tc.reason)
			}
		})
	}
	// Must stay inside chk_workshop_settings_table_width in 0413.
	if MaxCuttingTableWidthCm != 1000 || MinCuttingTableWidthCm <= 0 {
		t.Error("width band drifted from chk_workshop_settings_table_width (0413); move both together")
	}
}

func TestValidateWorkshopCostingDefaults(t *testing.T) {
	reasonOf := func(err error) string {
		if err == nil {
			return ""
		}
		ve, ok := err.(*ValidationError)
		if !ok {
			t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
		}
		return ve.Reason
	}
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"currency unset", ValidateWorkshopCostingCurrency(sql.NullString{}), ""},
		{"currency EUR", ValidateWorkshopCostingCurrency(ns("EUR")), ""},
		{"currency USDT (expense-only)", ValidateWorkshopCostingCurrency(ns("USDT")), ""},
		{"currency lower-case", ValidateWorkshopCostingCurrency(ns("eur")), "unsupported_currency"},
		{"currency unknown", ValidateWorkshopCostingCurrency(ns("XYZ")), "unsupported_currency"},
		{"country unset", ValidateWorkshopCountryOfOrigin(sql.NullString{}), ""},
		{"country PT", ValidateWorkshopCountryOfOrigin(ns("PT")), ""},
		{"country alpha-3", ValidateWorkshopCountryOfOrigin(ns("PRT")), "unknown_country"},
		{"country lower-case", ValidateWorkshopCountryOfOrigin(ns("pt")), "unknown_country"},
		{"rate unset", ValidateLabourRatePerMin(decimal.NullDecimal{}), ""},
		{"rate typical", ValidateLabourRatePerMin(wsND("0.3500")), ""},
		{"rate zero", ValidateLabourRatePerMin(wsND("0")), "must_be_positive"},
		{"rate over-precise", ValidateLabourRatePerMin(wsND("0.12345")), "too_many_decimal_places"},
		{"rate stray zeros", ValidateLabourRatePerMin(wsND("1000000")), "implausibly_high"},
		{"pct zero is legal", ValidateWorkshopPercent("defect_percent", wsND("0"), MaxDefectPercent), ""},
		{"pct ceiling", ValidateWorkshopPercent("defect_percent", wsND("100"), MaxDefectPercent), ""},
		{"pct over ceiling", ValidateWorkshopPercent("defect_percent", wsND("100.01"), MaxDefectPercent), "out_of_range"},
		{"overhead over 100 legal", ValidateWorkshopPercent("overhead_pct", wsND("150"), MaxOverheadPct), ""},
		{"pct negative", ValidateWorkshopPercent("target_margin_pct", wsND("-1"), MaxTargetMarginPct), "must_not_be_negative"},
		{"pct over-precise", ValidateWorkshopPercent("overhead_pct", wsND("1.005"), MaxOverheadPct), "too_many_decimal_places"},
	}
	for _, tc := range cases {
		if got := reasonOf(tc.err); got != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.reason)
		}
	}
}
