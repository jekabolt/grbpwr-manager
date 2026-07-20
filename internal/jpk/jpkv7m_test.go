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

	out, err := Generate(tp, ret, rows, period, gen)
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
		"<P_48>0</P_48>",
		"<LiczbaWierszyZakupow>0</LiczbaWierszyZakupow>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated XML missing %q", want)
		}
	}

	// A bad taxpayer must be rejected, not silently produce a file.
	if _, err := Generate(Taxpayer{NIP: "bad"}, ret, rows, period, gen); err == nil {
		t.Error("Generate accepted an invalid taxpayer")
	}
}
