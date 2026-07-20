package jpk

import (
	"encoding/xml"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Deklaracja is the VAT-7(22) declaration embedded in a JPK_V7M filing. Amounts are whole złoty
// (the declaration rounds; only the evidence rows keep grosze). Fields are emitted only when
// non-zero except the mandatory totals (P_38, P_48) and the settlement line (P_51 payable OR P_53
// refund), so the file matches how a VAT-7 is actually filled in.
type Deklaracja struct {
	XMLName  xml.Name     `xml:"Deklaracja"`
	Naglowek DeklNaglowek `xml:"Naglowek"`
	Pozycje  DeklPozycje  `xml:"PozycjeSzczegolowe"`
	// Pouczenia = the taxpayer's acknowledgement of the statutory caution; always "1".
	Pouczenia int `xml:"Pouczenia"`
}

type DeklNaglowek struct {
	KodFormularza KodFormularza `xml:"KodFormularzaDekl"`
	Wariant       int           `xml:"WariantFormularzaDekl"`
}

// KodFormularza carries the two schema attributes plus the form code as text.
type KodFormularza struct {
	KodSystemowy string `xml:"kodSystemowy,attr"`
	WersjaSchemy string `xml:"wersjaSchemy,attr"`
	Value        string `xml:",chardata"`
}

// DeklPozycje are the VAT-7 boxes we can populate from the ledger. GRBPWR's regimes touch: domestic
// 23% (P_19/P_20), intra-community supply / WDT (P_11), export (P_12), intra-community acquisition /
// WNT self-charge (P_23/P_24), import of goods (P_25), and deductible input on other purchases
// (P_42/P_43). OSS and every UK figure are filed on their own returns and never appear here.
type DeklPozycje struct {
	P_11 *int64 `xml:"P_11,omitempty"` // WDT (intra-community supply) — net
	P_12 *int64 `xml:"P_12,omitempty"` // export of goods — net
	P_19 *int64 `xml:"P_19,omitempty"` // domestic supply taxed 23% — net
	P_20 *int64 `xml:"P_20,omitempty"` // domestic supply taxed 23% — VAT
	P_23 *int64 `xml:"P_23,omitempty"` // WNT (intra-community acquisition) — net
	P_24 *int64 `xml:"P_24,omitempty"` // WNT — self-charged output VAT
	P_25 *int64 `xml:"P_25,omitempty"` // import of goods (art. 33a) — net
	P_26 *int64 `xml:"P_26,omitempty"` // import of goods (art. 33a) — self-charged output VAT
	P_38 int64  `xml:"P_38"`           // total output VAT (mandatory)
	P_42 *int64 `xml:"P_42,omitempty"` // input — other purchases — net
	P_43 *int64 `xml:"P_43,omitempty"` // input — other purchases — VAT
	P_48 int64  `xml:"P_48"`           // total deductible input VAT (mandatory)
	P_51 *int64 `xml:"P_51,omitempty"` // amount payable to the tax office (P_38 − P_48, if ≥ 0)
	P_53 *int64 `xml:"P_53,omitempty"` // excess input to carry forward / refund (P_48 − P_38, if > 0)
}

// whole rounds a base-currency amount to whole złoty (declaration granularity) and clamps a small
// negative (a refund-heavy month) to zero for a base field — a negative base is declared via the
// paired refund/carry-forward line, not a negative in the box.
func whole(d decimal.Decimal) int64 {
	if d.IsNegative() {
		return 0
	}
	return d.Round(0).IntPart()
}

func ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// BuildDeclaration maps the month's VAT-return aggregates onto the VAT-7 boxes. It balances by
// construction: P_38 (output) − P_48 (input) resolves to P_51 (payable) or P_53 (refund), matching
// AcctVatReturnPL.NetPayable. WNT/import self-charge is net-zero (output VAT = deductible input), so it
// nets out of the settlement exactly as the return's NetPayable already models it.
func BuildDeclaration(ret *entity.AcctVatReturnPL) Deklaracja {
	// Output side.
	pDomesticNet := whole(ret.NetDomestic)
	pDomesticVat := whole(ret.OutputDomestic)
	pWnt := whole(ret.NetWnt)
	pWntVat := whole(ret.InputWnt) // self-charge output VAT equals the reclaimable input VAT
	pImport := whole(ret.NetImport)
	pImportVat := whole(ret.InputImport)

	totalOutputVat := pDomesticVat + pWntVat + pImportVat

	// Input side: all deductible input on "other purchases" (domestic + the self-charged WNT/import
	// reclaim). Fixed-asset input (P_40/P_41) is not separated by the ledger, so it folds in here.
	inputNet := whole(ret.NetInputDomestic) + pWnt + pImport
	inputVat := whole(ret.InputDomestic) + pWntVat + pImportVat

	settlement := totalOutputVat - inputVat
	var p51, p53 *int64
	if settlement >= 0 {
		p51 = ptr(settlement)
	} else {
		p53 = ptr(-settlement)
	}

	return Deklaracja{
		Naglowek: DeklNaglowek{
			KodFormularza: KodFormularza{KodSystemowy: "VAT-7 (22)", WersjaSchemy: "1-0E", Value: "VAT-7"},
			Wariant:       22,
		},
		Pozycje: DeklPozycje{
			P_11: ptr(whole(ret.NetWdt)),
			P_12: ptr(whole(ret.NetExport)),
			P_19: ptr(pDomesticNet),
			P_20: ptr(pDomesticVat),
			P_23: ptr(pWnt),
			P_24: ptr(pWntVat),
			P_25: ptr(pImport),
			P_26: ptr(pImportVat),
			P_38: totalOutputVat,
			P_42: ptr(inputNet),
			P_43: ptr(inputVat),
			P_48: inputVat,
			P_51: p51,
			P_53: p53,
		},
		Pouczenia: 1,
	}
}
