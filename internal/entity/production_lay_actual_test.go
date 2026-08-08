package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

// Ф5б.1/Ф5б.2 — ЛОТ И ФАКТ РАСХОДА НА НАСТИЛЕ, the pure half. Everything here is a rule the
// migration also states in SQL; the point of the Go twin is that the refusal NAMES the field, and
// that the drift is a THREE-valued answer instead of a confident zero.

func layDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func layNullDec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// «Факт целиком или никак» (chk_prlay_actual_complete): a quantity implies a unit and a method, and
// the implication is ONE-WAY — a unit picked before the number was typed is a half-filled form.
func TestValidateProductionRunLayActual(t *testing.T) {
	cases := []struct {
		name      string
		in        ProductionRunLayActualInput
		wantField string
		wantWhy   string
	}{
		{
			name: "quantity without a unit is refused BY NAME",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("42.5"), Method: ProductionLayActualMethodRollBeforeAfter,
			},
			wantField: "lay.actual_uom", wantWhy: "required",
		},
		{
			name:      "quantity without a method is refused BY NAME",
			in:        ProductionRunLayActualInput{Qty: layNullDec("42.5"), Uom: MaterialUnitM},
			wantField: "lay.actual_method", wantWhy: "required",
		},
		{
			name: "a unit outside the vocabulary is refused",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("42.5"), Uom: MaterialUnit("погонных"), Method: ProductionLayActualMethodWeighed,
			},
			wantField: "lay.actual_uom", wantWhy: "unknown_unit",
		},
		{
			name: "a method outside the two is refused",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("42.5"), Uom: MaterialUnitM, Method: ProductionLayActualMethod("на глаз"),
			},
			wantField: "lay.actual_method", wantWhy: "unknown_method",
		},
		{
			name: "zero is not a measurement",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("0"), Uom: MaterialUnitM, Method: ProductionLayActualMethodRollBeforeAfter,
			},
			wantField: "lay.actual_qty", wantWhy: "out_of_range",
		},
		{
			name: "a whole fact passes",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("42.5"), Uom: MaterialUnitM, Method: ProductionLayActualMethodRollBeforeAfter,
			},
		},
		{
			name: "«м» is the vocabulary's «m» and passes",
			in: ProductionRunLayActualInput{
				Qty: layNullDec("42.5"), Uom: MaterialUnit("м"), Method: ProductionLayActualMethodWeighed,
			},
		},
		{
			name: "the unit chosen before the quantity is a half-filled form, not a lie",
			in:   ProductionRunLayActualInput{Uom: MaterialUnitKg},
		},
		{
			name: "withdrawing the fact needs nothing at all",
			in:   ProductionRunLayActualInput{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateProductionRunLayActual("lay", c.in)
			if c.wantField == "" {
				if err != nil {
					t.Fatalf("expected the fact to be accepted, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal naming %s", c.wantField)
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if ve.Field != c.wantField || ve.Reason != c.wantWhy {
				t.Fatalf("refusal is %q/%q, want %q/%q", ve.Field, ve.Reason, c.wantField, c.wantWhy)
			}
			if ve.HowToFix == "" {
				t.Fatal("a refusal a cutting room cannot act on is the MySQL 3819 this exists to replace")
			}
		})
	}
}

// The drift is computed from two numbers that already exist, and it is ABSENT — with a reason —
// wherever it cannot be earned. A zero would read as «план сошёлся», which is the one conclusion
// nobody has drawn.
func TestProductionRunLayDrift(t *testing.T) {
	lay := func(qty, uom string) ProductionRunLay {
		l := ProductionRunLay{}
		if qty != "" {
			l.ActualQty = layNullDec(qty)
		}
		if uom != "" {
			l.ActualUom = sql.NullString{String: uom, Valid: true}
		}
		return l
	}

	// План 4500 см = 45 м; ушло 47.25 м ⇒ +5 %.
	got := ProductionRunLayDrift(layDec("4500"), lay("47.25", "m"))
	if !got.Known {
		t.Fatalf("expected a known drift, got %q", got.Reason)
	}
	if !got.Drift.Equal(layDec("0.05")) {
		t.Fatalf("drift = %s, want 0.05", got.Drift)
	}
	if !got.PlannedInFactUnit.Equal(layDec("45")) {
		t.Fatalf("plan restated in the fact's unit = %s, want 45", got.PlannedInFactUnit)
	}

	// Меньше плана — дрейф отрицательный, а не «ноль, всё сошлось».
	if got := ProductionRunLayDrift(layDec("4500"), lay("42.75", "m")); !got.Known || !got.Drift.Equal(layDec("-0.05")) {
		t.Fatalf("undershoot drift = %v/%s, want a known -0.05", got.Known, got.Drift)
	}

	// Единица факта — из перечня Ф5а.3, и синоним читается как канон.
	if got := ProductionRunLayDrift(layDec("4500"), lay("4725", "см")); !got.Known || !got.Drift.Equal(layDec("0.05")) {
		t.Fatalf("cm drift = %v/%s, want a known 0.05", got.Known, got.Drift)
	}

	cases := []struct {
		name string
		plan decimal.Decimal
		lay  ProductionRunLay
		want string
	}{
		{
			name: "нет факта — нечего сравнивать, и это НЕ ноль",
			plan: layDec("4500"), lay: lay("", ""), want: LayDriftReasonNoActual,
		},
		{
			name: "факт во взвешенных килограммах против плана в сантиметрах",
			plan: layDec("4500"), lay: lay("18.4", "kg"), want: LayDriftReasonUnitNotLength,
		},
		{
			name: "единица, которой нет в словаре",
			plan: layDec("4500"), lay: lay("18.4", "локтей"), want: LayDriftReasonUnitUnknown,
		},
		{
			name: "плана нет — делить не на что",
			plan: decimal.Zero, lay: lay("47.25", "m"), want: LayDriftReasonNoPlan,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProductionRunLayDrift(c.plan, c.lay)
			if got.Known {
				t.Fatalf("expected no drift, got %s", got.Drift)
			}
			if got.Reason != c.want {
				t.Fatalf("reason = %q, want %q", got.Reason, c.want)
			}
			if !got.Drift.IsZero() {
				t.Fatal("the zero value must stay unread; Known is what a caller branches on")
			}
		})
	}
}

// SET NULL is paid for with the snapshot: a lay whose roll was deleted still NAMES it.
func TestProductionRunLayLotDetached(t *testing.T) {
	bound := ProductionRunLay{LotId: sql.NullInt64{Int64: 7, Valid: true}, LotCode: "R-1"}
	if bound.LotDetached() {
		t.Fatal("a bound lay is not detached")
	}
	orphan := ProductionRunLay{LotCode: "R-1"}
	if !orphan.LotDetached() {
		t.Fatal("a lay that remembers a code but holds no id lost its lot")
	}
	never := ProductionRunLay{}
	if never.LotDetached() {
		t.Fatal("a lay that never named a lot has not lost one — the two are different sentences")
	}
}

func TestProductionLayActualMethodMatchesSchema(t *testing.T) {
	// chk_prlay_actual_method (0285) closes the dictionary by spelling AND by case.
	for _, m := range []ProductionLayActualMethod{
		ProductionLayActualMethodRollBeforeAfter, ProductionLayActualMethodWeighed,
	} {
		if !IsValidProductionLayActualMethod(m) {
			t.Fatalf("%q must be storable", m)
		}
	}
	for _, m := range []ProductionLayActualMethod{"", "Weighed", "ROLL_BEFORE_AFTER", "guessed"} {
		if IsValidProductionLayActualMethod(m) {
			t.Fatalf("%q must not be storable", m)
		}
	}
	if ProductionLayActualMethodRollBeforeAfter != "roll_before_after" ||
		ProductionLayActualMethodWeighed != "weighed" {
		t.Fatal("the stored spellings are the CHECK's regexp; changing one without the other is a 3819 in production")
	}
}
