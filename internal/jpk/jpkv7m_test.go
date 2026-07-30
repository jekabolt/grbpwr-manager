package jpk

import (
	"database/sql"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestGenerate(t *testing.T) {
	period := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	tp := Taxpayer{NIP: "1234563218", FullName: "GRBPWR sp. z o.o.", Email: "vat@grbpwr.com", TaxOffice: "1471"}
	ret := &entity.AcctVatReturnPL{NetDomestic: d("1000"), OutputDomestic: d("230"), NetWdt: d("500")}
	rows := []entity.AcctVatSalesRow{
		{UUID: "ORD-A", Placed: period, BuyerVatID: sql.NullString{}, Regime: "pl_domestic", Net: d("1000"), Vat: d("230")},
	}

	out, err := Generate(tp, ret, rows, nil, period, gen)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Must be well-formed and round-trip parse.
	var back JPK
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("generated XML does not parse: %v", err)
	}

	s := string(out)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		schemaNamespace,
		"<NIP>1234563218</NIP>",
		"<KodUrzedu>1471</KodUrzedu>",
		"<Rok>2026</Rok>",
		"<Miesiac>7</Miesiac>",
		"<P_38>230</P_38>",
		"<LiczbaWierszyZakupow>0</LiczbaWierszyZakupow>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated XML missing %q", want)
		}
	}

	// A bad taxpayer must be rejected, not silently produce a file.
	if _, err := Generate(Taxpayer{NIP: "bad"}, ret, rows, nil, period, gen); err == nil {
		t.Error("Generate accepted an invalid taxpayer")
	}
}

// A month with a WNT acquisition must come out internally consistent: the declaration's P_23/P_24 are
// present, SprzedazCtrl ties to P_38 and ZakupCtrl to P_48. Filing a JPK that omitted the WNT while
// the VAT-UE statement (built from the same movements) declared it was an automatic MF cross-check
// mismatch.
func TestGenerateWntTiesDeclarationToEvidence(t *testing.T) {
	period := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	tp := Taxpayer{NIP: "1234563218", FullName: "GRBPWR sp. z o.o.", Email: "vat@grbpwr.com", TaxOffice: "1471"}
	ret := &entity.AcctVatReturnPL{
		NetDomestic: d("1000"), OutputDomestic: d("230"),
		NetWnt: d("400"), InputWnt: d("92"), OutputWntSelfCharge: d("92"),
	}
	salesRows := []entity.AcctVatSalesRow{
		{UUID: "ORD-A", Placed: period, TaxPointAt: period, Regime: "pl_domestic", Net: d("1000"), Vat: d("230")},
		{UUID: "INV-DE-1", Placed: period, TaxPointAt: period, BuyerVatID: sql.NullString{String: "DE811234567", Valid: true},
			Regime: "wnt", Net: d("400"), Vat: d("92")},
	}
	purchaseRows := []entity.AcctVatPurchaseRow{
		{DocNumber: "INV-DE-1", DocDate: period, SupplierVatId: "DE811234567", SupplierName: "Stoff GmbH", Net: d("400"), Vat: d("92")},
	}

	out, err := Generate(tp, ret, salesRows, purchaseRows, period, gen)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var back JPK
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("generated XML does not parse: %v", err)
	}

	p := back.Deklaracja.Pozycje
	if p.P_23 == nil || *p.P_23 != 400 || p.P_24 == nil || *p.P_24 != 92 {
		t.Errorf("WNT boxes = P_23:%v P_24:%v, want 400 / 92", p.P_23, p.P_24)
	}
	if p.P_38 != 322 {
		t.Errorf("P_38 = %d, want 322 (230 + 92)", p.P_38)
	}
	if got := back.Ewidencja.SprzedazCtrl.PodatekNalezny; got != "322.00" {
		t.Errorf("SprzedazCtrl.PodatekNalezny = %q, want 322.00 (must tie to P_38)", got)
	}
	if p.P_48 != 92 {
		t.Errorf("P_48 = %d, want 92 (the WNT input leg)", p.P_48)
	}
	if got := back.Ewidencja.ZakupCtrl.PodatekNaliczony; got != "92.00" {
		t.Errorf("ZakupCtrl.PodatekNaliczony = %q, want 92.00 (must tie to P_48)", got)
	}
	// Net-zero reverse charge: the payable is the domestic figure, untouched by the WNT.
	if p.P_51 == nil || *p.P_51 != 230 {
		t.Errorf("P_51 = %v, want 230 (self-charge legs cancel)", p.P_51)
	}
	if !strings.Contains(string(out), "<K_23>400.00</K_23>") {
		t.Error("sales register is missing the K_23 WNT row")
	}
}
