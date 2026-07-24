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
		// Filing rows stamp the TAX-POINT date (payment for sales, refund date for korekta) so a
		// row's date always falls inside the filing period — the placed date was a different day
		// and could sit outside the month (statutory review 13, §1.4). Legacy rows without
		// TaxPointAt fall back to Placed. Buyer name comes from the billing company / person;
		// "-" only when nothing is known. A refund of a B2B invoice is its own corrective row
		// (negative amounts) marked in DowodSprzedazy — the system has no credit-note register
		// yet, so the order reference + KOREKTA marker is the document identity (caveated in the
		// statutory review; the accountant maps it to the actual credit note).
		day := r.TaxPointAt
		if day.IsZero() {
			day = r.Placed
		}
		name := strings.TrimSpace(r.BuyerName.String)
		if name == "" {
			name = "-"
		}
		doc := r.UUID
		if r.IsRefund {
			doc = r.UUID + "-KOREKTA"
		}
		w := SprzedazWiersz{
			LpSprzedazy:      lp,
			NrKontrahenta:    strings.TrimSpace(r.BuyerVatID.String),
			NazwaKontrahenta: name,
			DowodSprzedazy:   doc,
			DataWystawienia:  day.Format("2006-01-02"),
			DataSprzedazy:    day.Format("2006-01-02"),
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

// ZakupWiersz is one purchase-register row (ewidencja zakupu): the supplier invoice identity plus
// the net (K_42) and input VAT (K_43) of "other purchases". Emitted only for register-backed
// deductions — domestic material receipts and documented OPEX invoices — so the declaration's
// P_42/P_43 always cross-check with these rows.
type ZakupWiersz struct {
	XMLName        xml.Name `xml:"ZakupWiersz"`
	LpZakupu       int      `xml:"LpZakupu"`
	NrDostawcy     string   `xml:"NrDostawcy"`
	NazwaDostawcy  string   `xml:"NazwaDostawcy"`
	DowodZakupu    string   `xml:"DowodZakupu"`
	DataZakupu     string   `xml:"DataZakupu"`
	K_42           string   `xml:"K_42,omitempty"`
	K_43           string   `xml:"K_43,omitempty"`
}

// BuildPurchaseEvidence turns the month's register-backed purchase rows into ZakupWiersz rows plus
// the control totals.
func BuildPurchaseEvidence(rows []entity.AcctVatPurchaseRow) ([]ZakupWiersz, ZakupCtrl) {
	out := make([]ZakupWiersz, 0, len(rows))
	totalVat := decimal.Zero
	for i, r := range rows {
		supplier := strings.TrimSpace(r.SupplierVatId)
		if supplier == "" {
			supplier = "BRAK"
		}
		name := strings.TrimSpace(r.SupplierName)
		if name == "" {
			name = "-"
		}
		out = append(out, ZakupWiersz{
			LpZakupu:      i + 1,
			NrDostawcy:    supplier,
			NazwaDostawcy: name,
			DowodZakupu:   r.DocNumber,
			DataZakupu:    r.DocDate.Format("2006-01-02"),
			K_42:          r.Net.StringFixed(2),
			K_43:          r.Vat.StringFixed(2),
		})
		totalVat = totalVat.Add(r.Vat)
	}
	return out, ZakupCtrl{LiczbaWierszyZakupow: len(out), PodatekNaliczony: totalVat.StringFixed(2)}
}
