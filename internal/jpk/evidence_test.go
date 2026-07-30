package jpk

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func vatID(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func TestBuildSalesEvidence(t *testing.T) {
	period := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	placed := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	rows := []entity.AcctVatSalesRow{
		{UUID: "ORD-A", Placed: placed, BuyerVatID: vatID("DE123456789"), Regime: "pl_domestic", Net: d("100"), Vat: d("23")},
		{UUID: "ORD-B", Placed: placed, BuyerVatID: vatID(""), Regime: "pl_domestic", Net: d("200"), Vat: d("46")},
		{UUID: "ORD-C", Placed: placed, BuyerVatID: vatID(""), Regime: "pl_domestic", Net: d("50"), Vat: d("11.50")},
		{UUID: "ORD-D", Placed: placed, BuyerVatID: vatID("FR99999999"), Regime: "wdt", Net: d("500"), Vat: d("0")},
	}

	got, ctrl := BuildSalesEvidence(rows, period)

	// Two individual invoice rows (the two B2B orders) + one B2C aggregate = 3.
	if len(got) != 3 {
		t.Fatalf("row count = %d, want 3 (2 B2B + 1 B2C aggregate)", len(got))
	}
	if ctrl.LiczbaWierszySprzedazy != 3 {
		t.Errorf("ctrl count = %d, want 3", ctrl.LiczbaWierszySprzedazy)
	}
	// Total output VAT: 23 (B2B domestic) + 0 (wdt) + 57.50 (B2C aggregate) = 80.50.
	if ctrl.PodatekNalezny != "80.50" {
		t.Errorf("PodatekNalezny = %q, want 80.50", ctrl.PodatekNalezny)
	}

	byDoc := map[string]SprzedazWiersz{}
	for _, w := range got {
		byDoc[w.DowodSprzedazy] = w
	}
	if a := byDoc["ORD-A"]; a.K_19 != "100.00" || a.K_20 != "23.00" || a.NrKontrahenta != "DE123456789" {
		t.Errorf("B2B domestic row wrong: %+v", a)
	}
	if dd := byDoc["ORD-D"]; dd.K_21 != "500.00" || dd.K_20 != "" {
		t.Errorf("wdt row should have K_21 only: %+v", dd)
	}
	agg, ok := byDoc["WEW_2026-07_pl_domestic"]
	if !ok {
		t.Fatalf("missing B2C aggregate row; got docs %v", byDoc)
	}
	if agg.TypDokumentu != "WEW" || agg.NrKontrahenta != "BRAK" || agg.K_19 != "250.00" || agg.K_20 != "57.50" {
		t.Errorf("B2C aggregate wrong: %+v", agg)
	}
	if agg.DataWystawienia != "2026-07-31" {
		t.Errorf("B2C aggregate should be dated month-end, got %s", agg.DataWystawienia)
	}
}

// The reverse-charge self-assessments must land in the SALES register (K_23/K_24 for WNT, K_25/K_26
// for import art. 33a), always as their own row against the supplier — never folded into the retail
// aggregate and never dropped for want of a VAT id. Their VAT counts into the control total, which is
// what ties the register to the declaration's P_38.
func TestBuildSalesEvidenceSelfCharge(t *testing.T) {
	period := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	taxPoint := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	rows := []entity.AcctVatSalesRow{
		{UUID: "ORD-A", Placed: taxPoint, TaxPointAt: taxPoint, BuyerVatID: vatID(""), Regime: "pl_domestic", Net: d("100"), Vat: d("23")},
		{UUID: "INV-DE-1", Placed: taxPoint, TaxPointAt: taxPoint, BuyerVatID: vatID("DE811234567"), BuyerName: vatID("Stoff GmbH"), Regime: "wnt", Net: d("400"), Vat: d("92")},
		{UUID: "MOV-77", Placed: taxPoint, TaxPointAt: taxPoint, BuyerVatID: vatID(""), Regime: "import", Net: d("200"), Vat: d("46")},
	}

	got, ctrl := BuildSalesEvidence(rows, period)

	byDoc := map[string]SprzedazWiersz{}
	for _, w := range got {
		byDoc[w.DowodSprzedazy] = w
	}
	wnt, ok := byDoc["INV-DE-1"]
	if !ok {
		t.Fatalf("WNT self-charge row missing; got docs %v", byDoc)
	}
	if wnt.K_23 != "400.00" || wnt.K_24 != "92.00" || wnt.NrKontrahenta != "DE811234567" || wnt.NazwaKontrahenta != "Stoff GmbH" {
		t.Errorf("WNT row wrong: %+v", wnt)
	}
	if wnt.K_19 != "" || wnt.K_20 != "" || wnt.TypDokumentu != "" {
		t.Errorf("WNT row must not touch the domestic sale columns: %+v", wnt)
	}
	imp, ok := byDoc["MOV-77"]
	if !ok {
		t.Fatalf("import self-charge row missing; got docs %v", byDoc)
	}
	if imp.K_25 != "200.00" || imp.K_26 != "46.00" {
		t.Errorf("import row wrong: %+v", imp)
	}
	// No supplier VAT id on file → BRAK, but the row (and its VAT) still ships.
	if imp.NrKontrahenta != "BRAK" {
		t.Errorf("import row counterparty = %q, want BRAK", imp.NrKontrahenta)
	}
	// 1 retail aggregate + 2 self-charge rows; total output VAT 23 + 92 + 46.
	if len(got) != 3 || ctrl.LiczbaWierszySprzedazy != 3 {
		t.Errorf("row count = %d (ctrl %d), want 3", len(got), ctrl.LiczbaWierszySprzedazy)
	}
	if ctrl.PodatekNalezny != "161.00" {
		t.Errorf("PodatekNalezny = %q, want 161.00 (self-charged VAT included)", ctrl.PodatekNalezny)
	}
}
