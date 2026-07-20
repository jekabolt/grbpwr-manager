package jpk

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// schemaNamespace is the JPK_V7M(2) target namespace (the "2" variant in force since 2022).
const schemaNamespace = "http://crd.gov.pl/wzor/2021/12/27/11148/"

// nazwaSystemu identifies the producing system in the header — informational, not validated.
const nazwaSystemu = "GRBPWR"

// JPK is the JPK_V7M filing envelope: header, taxpayer, the VAT-7 declaration, and the sales/purchase
// evidence. GRBPWR files an output-side register (sales); the purchase register is left empty for the
// accountant to merge, so ZakupCtrl reports zero rows.
type JPK struct {
	XMLName    xml.Name   `xml:"JPK"`
	Xmlns      string     `xml:"xmlns,attr"`
	Naglowek   Naglowek   `xml:"Naglowek"`
	Podmiot1   Podmiot1   `xml:"Podmiot1"`
	Deklaracja Deklaracja `xml:"Deklaracja"`
	Ewidencja  Ewidencja  `xml:"Ewidencja"`
}

type Naglowek struct {
	KodFormularza      KodFormularza `xml:"KodFormularza"`
	WariantFormularza  int           `xml:"WariantFormularza"`
	DataWytworzeniaJPK string        `xml:"DataWytworzeniaJPK"`
	NazwaSystemu       string        `xml:"NazwaSystemu"`
	CelZlozenia        CelZlozenia   `xml:"CelZlozenia"`
	KodUrzedu          string        `xml:"KodUrzedu"`
	Rok                int           `xml:"Rok"`
	Miesiac            int           `xml:"Miesiac"`
}

// CelZlozenia carries the submission-purpose code (1 = original filing) with its schema position attr.
type CelZlozenia struct {
	Poz   string `xml:"poz,attr"`
	Value int    `xml:",chardata"`
}

type Podmiot1 struct {
	Rola             string           `xml:"rola,attr"`
	OsobaNiefizyczna OsobaNiefizyczna `xml:"OsobaNiefizyczna"`
}

// OsobaNiefizyczna is the legal-entity taxpayer block. Address is not part of Podmiot1 in JPK_V7M(2).
type OsobaNiefizyczna struct {
	NIP        string `xml:"NIP"`
	PelnaNazwa string `xml:"PelnaNazwa"`
	Email      string `xml:"Email"`
	Telefon    string `xml:"Telefon,omitempty"`
}

// Ewidencja holds the evidence rows and their control totals. The purchase side is intentionally empty
// (the accountant merges it); ZakupCtrl still reports zero rows / zero input VAT so the file is valid
// and internally consistent with the output-only declaration (P_48 = 0).
type Ewidencja struct {
	SprzedazWiersz []SprzedazWiersz `xml:"SprzedazWiersz"`
	SprzedazCtrl   SprzedazCtrl     `xml:"SprzedazCtrl"`
	ZakupCtrl      ZakupCtrl        `xml:"ZakupCtrl"`
}

// ZakupCtrl is the purchase-register control block. Zero here (output-only filing).
type ZakupCtrl struct {
	LiczbaWierszyZakupow int    `xml:"LiczbaWierszyZakupow"`
	PodatekNaliczony     string `xml:"PodatekNaliczony"`
}

// Generate builds a complete, output-side JPK_V7M XML document for the month. taxpayer is validated
// (a mistyped NIP is rejected here), the declaration comes from the VAT-return aggregates, and the
// sales rows form the evidence. generatedAt stamps DataWytworzeniaJPK. The purchase register is empty
// per the "accountant merges purchases" decision.
func Generate(taxpayer Taxpayer, ret *entity.AcctVatReturnPL, salesRows []entity.AcctVatSalesRow, period, generatedAt time.Time) ([]byte, error) {
	if err := taxpayer.Validate(); err != nil {
		return nil, err
	}
	if ret == nil {
		return nil, fmt.Errorf("jpk: nil vat return")
	}

	sales, salesCtrl := BuildSalesEvidence(salesRows, period)

	doc := JPK{
		Xmlns: schemaNamespace,
		Naglowek: Naglowek{
			KodFormularza:      KodFormularza{KodSystemowy: "JPK_V7M (2)", WersjaSchemy: "1-0E", Value: "JPK_VAT"},
			WariantFormularza:  2,
			DataWytworzeniaJPK: generatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			NazwaSystemu:       nazwaSystemu,
			CelZlozenia:        CelZlozenia{Poz: "P_7", Value: 1},
			KodUrzedu:          taxpayer.TaxOffice,
			Rok:                period.Year(),
			Miesiac:            int(period.Month()),
		},
		Podmiot1: Podmiot1{
			Rola: "Podatnik",
			OsobaNiefizyczna: OsobaNiefizyczna{
				NIP:        taxpayer.NIP,
				PelnaNazwa: taxpayer.FullName,
				Email:      taxpayer.Email,
				Telefon:    taxpayer.Phone,
			},
		},
		Deklaracja: BuildDeclaration(ret),
		Ewidencja: Ewidencja{
			SprzedazWiersz: sales,
			SprzedazCtrl:   salesCtrl,
			ZakupCtrl:      ZakupCtrl{LiczbaWierszyZakupow: 0, PodatekNaliczony: "0.00"},
		},
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header) // <?xml version="1.0" encoding="UTF-8"?>
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("jpk: encode: %w", err)
	}
	return buf.Bytes(), nil
}
