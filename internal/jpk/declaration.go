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
// WNT self-charge (P_23/P_24), import of goods art. 33a (P_25/P_26), and deductible input on other purchases
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

// BuildDeclaration maps the month's VAT-return aggregates onto the VAT-7 boxes. Every declared box is
// backed by the evidence rows the same file carries: the output side from the sales register, the
// deduction side (P_42/P_43/P_48) from the register-backed purchases only — domestic material
// receipts, documented OPEX invoices and the WNT/import self-charge inputs. Input VAT the system
// cannot evidence with a register row (ret.InputUnregistered) is deliberately left out and caveated,
// so the declaration never claims more than the ewidencja proves.
//
// WNT/import self-charge is declared on BOTH sides: P_23/P_24 and P_25/P_26 for the self-charged
// output, and its deductible leg inside P_42/P_43.
func BuildDeclaration(ret *entity.AcctVatReturnPL) Deklaracja {
	pDomesticVat := whole(ret.OutputDomestic)

	// WNT / import (art. 33a) self-charge IS declared: the file's own ewidencja now evidences it on
	// both sides — VatSalesEvidenceFiling emits the K_23/K_24 and K_25/K_26 output rows and
	// VatPurchaseEvidenceFiling the matching K_42/K_43 input rows — so the MF deklaracja↔ewidencja
	// cross-check ties, and the JPK no longer contradicts the VAT-UE recapitulative statement,
	// which declares the same WNT acquisitions (review pass 3; supersedes review pass 1 H-1, which
	// dropped the boxes while the registers were still output-only).
	//
	// The self-charge posting is Dr 2080 / Cr 2070 with ONE amount (rule M1, internal/accounting/
	// material.go), so each regime's self-charged output VAT equals its reclaimable input — which is
	// why the output boxes are read off the input figures.
	pWntVat := whole(ret.InputWnt)
	pImportVat := whole(ret.InputImport)

	// P_38 is the sum of the DECLARED boxes (the MF arithmetic check on the declaration), so it adds
	// the already-rounded boxes instead of rounding the total.
	totalOutputVat := pDomesticVat + pWntVat + pImportVat

	// Input side (statutory review 13): the system now captures register-backed input VAT —
	// domestic material receipts + documented OPEX invoices (ret.InputDomestic /
	// NetInputDomestic) and the WNT/import self-charge inputs — so the declaration deducts what
	// the emitted purchase register evidences. P_42/P_43 carry the combined "other purchases"
	// net/VAT; P_48 = P_43; P_51 = max(P_38 − P_48, 0) with any excess in P_53. This matches the
	// app's NetPayable formula exactly (output − all inputs). Undocumented opex input VAT is
	// deliberately NOT here (ret.InputUnregistered, caveated) so the declaration always
	// cross-checks with the register rows.
	//
	// Each component is rounded separately (rather than the combined figure) so the self-charge's two
	// legs cancel EXACTLY: P_51 = whole(OutputDomestic) − whole(InputDomestic), the domestic payable,
	// regardless of the WNT/import amounts. Rounding the sum instead could drift the payable by up to
	// a złoty purely because a net-zero reverse charge happened in the month.
	inputVat := whole(ret.InputDomestic) + pWntVat + pImportVat
	inputNet := whole(ret.NetInputDomestic) + whole(ret.NetWnt) + whole(ret.NetImport)
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
			P_23: ptr(whole(ret.NetWnt)),
			P_24: ptr(pWntVat),
			P_25: ptr(whole(ret.NetImport)),
			P_26: ptr(pImportVat),
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
