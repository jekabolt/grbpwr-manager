package oss

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestGenerateOss(t *testing.T) {
	tp := jpk.Taxpayer{NIP: "1234563218", FullName: "GRBPWR sp. z o.o.", Email: "vat@grbpwr.com", TaxOffice: "1471"}
	ret := &entity.AcctOssReturn{
		QuarterStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), // Q3
		Rows: []entity.AcctOssRow{
			{Country: "DE", RatePct: dec("19.00"), Net: dec("1000.00"), Vat: dec("190.00")},
			{Country: "FR", RatePct: dec("20.00"), Net: dec("500.00"), Vat: dec("100.00")},
		},
		TotalNet: dec("1500.00"),
		TotalVat: dec("290.00"),
	}

	out, err := Generate(tp, ret, time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var back OssReturn
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("OSS XML does not parse: %v", err)
	}
	if back.Naglowek.Kwartal != 3 || back.Naglowek.Rok != 2026 {
		t.Errorf("quarter/year = Q%d %d, want Q3 2026", back.Naglowek.Kwartal, back.Naglowek.Rok)
	}
	if len(back.Sprzedaz) != 2 {
		t.Fatalf("country rows = %d, want 2", len(back.Sprzedaz))
	}
	if back.LiczbaKraj != 2 || back.SumaVat != "290.00" || back.SumaNetto != "1500.00" {
		t.Errorf("totals wrong: %+v", back)
	}
	s := string(out)
	for _, want := range []string{"VIU-DO", "<NIP>1234563218</NIP>", "<KodKraju>DE</KodKraju>", "<KwotaVAT>190.00</KwotaVAT>"} {
		if !strings.Contains(s, want) {
			t.Errorf("OSS XML missing %q", want)
		}
	}

	if _, err := Generate(jpk.Taxpayer{NIP: "bad"}, ret, time.Now().UTC()); err == nil {
		t.Error("Generate accepted an invalid taxpayer")
	}
}
