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

// whole rounds an amount to whole złoty (declaration granularity). Negatives are KEPT: in a
// refund-heavy month a regime's net/VAT box is legitimately negative (in-period corrections), and
// clamping it to zero both under-declared the reduction and broke the declaration↔evidence tie
// (the signed KOREKTA evidence rows still summed negative). P_51/P_53 derive from the signed
// payable, so the settlement side was always correct (review pass 1, M-1).
func whole(d decimal.Decimal) int64 {
	return d.Round(0).IntPart()
}

func ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// BuildDeclaration maps the month's VAT-return aggregates onto the VAT-7 boxes. This is an
// OUTPUT-SIDE declaration: sales and self-charged output VAT are declared, and the input/deduction
// side (P_42/P_43/P_48) is left at zero for the accountant to merge from their purchase register — the
// system does not capture every purchase invoice (nor the supplier NIP each deduction row needs), so
// the accountant's input side is authoritative. P_48 = 0 keeps the file internally consistent with an
// empty ZakupWiersz; P_51 therefore reports the output VAT before the accountant's input deduction.
//
// WNT/import self-charge is declared on BOTH sides normally, but with the input side deferred we
// declare only its output leg here (P_24/P_26) — the accountant claims the matching input.
func BuildDeclaration(ret *entity.AcctVatReturnPL) Deklaracja {
	pDomesticVat := whole(ret.OutputDomestic)

	// WNT/import self-charge is deliberately NOT declared here: the emitted registers carry no
	// K_23..K_26 rows for it (VatPurchaseEvidenceFiling filters domestic_pl only), and a
	// declaration box the file's own ewidencja cannot evidence fails the MF deklaracja↔ewidencja
	// cross-check. Its output and input legs are equal, so P_51 is unaffected; the filing caveats
	// name the amounts for the accountant to merge (review pass 1, H-1).
	totalOutputVat := pDomesticVat

	// Input side (statutory review 13): the system now captures register-backed input VAT —
	// domestic material receipts + documented OPEX invoices (ret.InputDomestic /
	// NetInputDomestic) and the WNT/import self-charge inputs — so the declaration deducts what
	// the emitted purchase register evidences. P_42/P_43 carry the combined "other purchases"
	// net/VAT; P_48 = P_43; P_51 = max(P_38 − P_48, 0) with any excess in P_53. This matches the
	// app's NetPayable formula exactly (output − all inputs). Undocumented opex input VAT is
	// deliberately NOT here (ret.InputUnregistered, caveated) so the declaration always
	// cross-checks with the register rows.
	inputVat := whole(ret.InputDomestic)
	inputNet := whole(ret.NetInputDomestic)
	payable := totalOutputVat - inputVat
	var p51, p53 int64
	if payable >= 0 {
		p51 = payable
	} else {
		p53 = -payable
	}

	return Deklaracja{
		Naglowek: DeklNaglowek{
			KodFormularza: KodFormularza{KodSystemowy: "VAT-7 (22)", WersjaSchemy: "1-0E", Value: "VAT-7"},
			Wariant:       22,
		},
		Pozycje: DeklPozycje{
			P_11: ptr(whole(ret.NetWdt)),
			P_12: ptr(whole(ret.NetExport)),
			P_19: ptr(whole(ret.NetDomestic)),
			P_20: ptr(pDomesticVat),
			P_38: totalOutputVat,
			P_42: ptr(inputNet),
			P_43: ptr(inputVat),
			P_48: inputVat,
			P_51: ptr(p51),
			P_53: ptr(p53),
		},
		Pouczenia: 1,
	}
}
