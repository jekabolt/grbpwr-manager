package jpk

import (
	"encoding/xml"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// SprzedazWiersz is one row of the JPK_V7M sales register. Amounts keep grosze (2 dp) and are emitted
// only when non-zero. GRBPWR's sales land in three columns: K_19/K_20 (domestic 23% net/VAT), K_21
// (intra-community supply / WDT net) and K_22 (export net).
type SprzedazWiersz struct {
	XMLName          xml.Name `xml:"SprzedazWiersz"`
	LpSprzedazy      int      `xml:"LpSprzedazy"`
	NrKontrahenta    string   `xml:"NrKontrahenta"`
	NazwaKontrahenta string   `xml:"NazwaKontrahenta"`
	DowodSprzedazy   string   `xml:"DowodSprzedazy"`
	DataWystawienia  string   `xml:"DataWystawienia"`
	DataSprzedazy    string   `xml:"DataSprzedazy,omitempty"`
	TypDokumentu     string   `xml:"TypDokumentu,omitempty"`
	K_19             string   `xml:"K_19,omitempty"`
	K_20             string   `xml:"K_20,omitempty"`
	K_21             string   `xml:"K_21,omitempty"`
	K_22             string   `xml:"K_22,omitempty"`
}

// SprzedazCtrl is the sales-register control block: the row count and the total output VAT, which must
// tie to the declaration's P_38.
type SprzedazCtrl struct {
	LiczbaWierszySprzedazy int    `xml:"LiczbaWierszySprzedazy"`
	PodatekNalezny         string `xml:"PodatekNalezny"`
}

// amt renders a grosze amount, dropping a zero (so omitempty elides the element).
func amt(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.StringFixed(2)
}

// applyRegimeAmounts places a row's net/VAT into the correct JPK sales columns for its regime.
func applyRegimeAmounts(w *SprzedazWiersz, regime string, net, vat decimal.Decimal) {
	switch entity.VatRegime(regime) {
	case entity.VatRegimePLDomestic:
		w.K_19 = amt(net)
		w.K_20 = amt(vat)
	case entity.VatRegimeWDT:
		w.K_21 = amt(net)
	case entity.VatRegimeExport:
		w.K_22 = amt(net)
	}
}

// BuildSalesEvidence turns the month's per-order sales rows into JPK sales-register rows. Orders with a
// buyer NIP become individual invoice rows; the rest (B2C) are aggregated into one internal-document
// (WEW) row per regime, dated the last day of the month — the standard way to report un-invoiced
// retail without listing every consumer order. Rows are numbered sequentially and the control block
// carries the row count + total output VAT.
func BuildSalesEvidence(rows []entity.AcctVatSalesRow, period time.Time) ([]SprzedazWiersz, SprzedazCtrl) {
	type bucket struct{ net, vat decimal.Decimal }
	b2c := map[string]*bucket{}
	var individual []entity.AcctVatSalesRow

	for _, r := range rows {
		if strings.TrimSpace(r.BuyerVatID.String) == "" {
			b := b2c[r.Regime]
			if b == nil {
				b = &bucket{}
				b2c[r.Regime] = b
			}
			b.net = b.net.Add(r.Net)
			b.vat = b.vat.Add(r.Vat)
			continue
		}
		individual = append(individual, r)
	}

	out := make([]SprzedazWiersz, 0, len(individual)+len(b2c))
	lp := 0
	totalVat := decimal.Zero

	for _, r := range individual {
		lp++
		w := SprzedazWiersz{
			LpSprzedazy:      lp,
			NrKontrahenta:    strings.TrimSpace(r.BuyerVatID.String),
			NazwaKontrahenta: "-",
			DowodSprzedazy:   r.UUID,
			DataWystawienia:  r.Placed.Format("2006-01-02"),
			DataSprzedazy:    r.Placed.Format("2006-01-02"),
		}
		applyRegimeAmounts(&w, r.Regime, r.Net, r.Vat)
		totalVat = totalVat.Add(r.Vat)
		out = append(out, w)
	}

	lastDay := period.AddDate(0, 1, -1).Format("2006-01-02")
	monthTag := period.Format("2006-01")
	// Deterministic regime order so re-runs of the same month are byte-identical.
	for _, regime := range []string{"pl_domestic", "wdt", "export"} {
		b := b2c[regime]
		if b == nil || (b.net.IsZero() && b.vat.IsZero()) {
			continue
		}
		lp++
		w := SprzedazWiersz{
			LpSprzedazy:      lp,
			NrKontrahenta:    "BRAK",
			NazwaKontrahenta: "Sprzedaż detaliczna",
			DowodSprzedazy:   "WEW_" + monthTag + "_" + regime,
			DataWystawienia:  lastDay,
			DataSprzedazy:    lastDay,
			TypDokumentu:     "WEW",
		}
		applyRegimeAmounts(&w, regime, b.net, b.vat)
		totalVat = totalVat.Add(b.vat)
		out = append(out, w)
	}

	return out, SprzedazCtrl{LiczbaWierszySprzedazy: len(out), PodatekNalezny: totalVat.StringFixed(2)}
}
