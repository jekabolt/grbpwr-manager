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
