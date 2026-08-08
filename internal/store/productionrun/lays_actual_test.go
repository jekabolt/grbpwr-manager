package productionrun

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// resolveLayActual decides TWO things, and the second one is the one that gets forgotten: what to
// store, and whether the who/when stamp is renewed. Every full-payload client echoes the fact back
// on every save — a note edit, a display-order nudge, a section swap — and a stamp renewed on those
// would record the last person to touch the настил as the person who measured the roll.
func TestResolveLayActualStamp(t *testing.T) {
	nd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	stored := func(qty, uom, method string) *layIdentity {
		id := &layIdentity{Id: 1}
		if qty != "" {
			id.ActualQty = nd(qty)
		}
		if uom != "" {
			id.ActualUom = ns(uom)
		}
		if method != "" {
			id.ActualMethod = ns(method)
		}
		return id
	}
	fact := func(qty, uom string, method entity.ProductionLayActualMethod) *entity.ProductionRunLayActualInput {
		in := &entity.ProductionRunLayActualInput{Uom: entity.MaterialUnit(uom), Method: method}
		if qty != "" {
			in.Qty = nd(qty)
		}
		return in
	}

	if got := resolveLayActual(nil, stored("42.5", "m", "weighed")); got != nil {
		t.Fatal("a silent payload must produce NO instruction — that silence is what protects a fact " +
			"from a client that predates the column")
	}

	// Создание с фактом: подписывать некому, кроме того, кто его принёс.
	if got := resolveLayActual(fact("42.5", "m", entity.ProductionLayActualMethodWeighed), nil); !got.Stamp {
		t.Fatal("a fact arriving with a brand-new lay must be stamped")
	}
	// Создание без факта: подписывать нечего.
	if got := resolveLayActual(fact("", "", ""), nil); got.Stamp {
		t.Fatal("an empty fact on a new lay signs nothing")
	}

	// ЭХО НЕИЗМЕНЁННОГО ФАКТА — снимка не касаемся, подписи тоже.
	echo := resolveLayActual(
		fact("42.500", "м", entity.ProductionLayActualMethodWeighed),
		stored("42.5", "m", "weighed"))
	if echo.Stamp {
		t.Fatal("an unchanged measurement — «42.500» against «42.5», «м» against «m» — must not be restamped")
	}
	if echo.Uom.String != "m" {
		t.Fatalf("the unit is stored canonically (chk_prlay_actual_uom is lower case only), got %q", echo.Uom.String)
	}

	cases := []struct {
		name string
		in   *entity.ProductionRunLayActualInput
		was  *layIdentity
	}{
		{
			name: "the number moved",
			in:   fact("44", "m", entity.ProductionLayActualMethodWeighed),
			was:  stored("42.5", "m", "weighed"),
		},
		{
			name: "the unit moved — the same digits mean something else",
			in:   fact("42.5", "kg", entity.ProductionLayActualMethodWeighed),
			was:  stored("42.5", "m", "weighed"),
		},
		{
			name: "the method moved — the number's provenance changed",
			in:   fact("42.5", "m", entity.ProductionLayActualMethodRollBeforeAfter),
			was:  stored("42.5", "m", "weighed"),
		},
		{
			name: "the fact was withdrawn — the signature must go with it",
			in:   fact("", "", ""),
			was:  stored("42.5", "m", "weighed"),
		},
		{
			name: "the first measurement of an existing lay",
			in:   fact("42.5", "m", entity.ProductionLayActualMethodWeighed),
			was:  stored("", "", ""),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveLayActual(c.in, c.was); !got.Stamp {
				t.Fatal("a moved measurement must renew who/when")
			}
		})
	}
}
