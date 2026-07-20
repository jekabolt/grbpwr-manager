// Package oss builds the quarterly OSS (One Stop Shop) Union-scheme VAT return from the accounting
// ledger — in Poland the VIU-DO filing. It reuses the JPK taxpayer identity for the header.
//
// The exact e-Deklaracje VIU-DO schema (namespaces / element names) is not reproduced verbatim here:
// this is a structured, taxpayer-headed OSS return — one row per member state of consumption with its
// rate, taxable base and VAT — that the accountant validates against the official schema (or transcribes
// into the OSS portal) before submission. Same honesty as the JPK export: the numbers come straight
// from the ledger; the wrapper is a draft pending schema validation.
package oss

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
)

// OssReturn is the quarterly OSS Union-scheme return envelope.
type OssReturn struct {
	XMLName    xml.Name  `xml:"OSSReturn"`
	Naglowek   Naglowek  `xml:"Naglowek"`
	Podatnik   Podatnik  `xml:"Podatnik"`
	Sprzedaz   []KrajRow `xml:"SprzedazWgKraju>Kraj"`
	SumaNetto  string    `xml:"Podsumowanie>SumaNetto"`
	SumaVat    string    `xml:"Podsumowanie>SumaVAT"`
	LiczbaKraj int       `xml:"Podsumowanie>LiczbaKrajow"`
}

type Naglowek struct {
	Rodzaj     string `xml:"RodzajDeklaracji"` // "VIU-DO" (Union scheme)
	Rok        int    `xml:"Rok"`
	Kwartal    int    `xml:"Kwartal"`     // 1..4
	Cel        int    `xml:"CelZlozenia"` // 1 = original
	Wytworzono string `xml:"DataWytworzenia"`
}

type Podatnik struct {
	NIP        string `xml:"NIP"`
	PelnaNazwa string `xml:"PelnaNazwa"`
	Email      string `xml:"Email"`
}

// KrajRow is one member state of consumption: destination country, applied rate, taxable base and VAT.
type KrajRow struct {
	KodKraju              string `xml:"KodKraju"` // ISO alpha-2 consumption country
	StawkaVAT             string `xml:"StawkaVAT"`
	PodstawaOpodatkowania string `xml:"PodstawaOpodatkowania"` // net taxable base
	KwotaVAT              string `xml:"KwotaVAT"`
}

// Generate builds the OSS return XML for a quarter from the per-country aggregate. It reuses (and
// validates) the JPK taxpayer identity, so a missing/mistyped NIP is caught before a filing is produced.
func Generate(taxpayer jpk.Taxpayer, ret *entity.AcctOssReturn, generatedAt time.Time) ([]byte, error) {
	if err := taxpayer.Validate(); err != nil {
		return nil, err
	}
	if ret == nil {
		return nil, fmt.Errorf("oss: nil return")
	}

	rows := make([]KrajRow, 0, len(ret.Rows))
	for _, r := range ret.Rows {
		rows = append(rows, KrajRow{
			KodKraju:              r.Country,
			StawkaVAT:             r.RatePct.StringFixed(2),
			PodstawaOpodatkowania: r.Net.StringFixed(2),
			KwotaVAT:              r.Vat.StringFixed(2),
		})
	}

	q := int((ret.QuarterStart.Month()-1)/3) + 1
	doc := OssReturn{
		Naglowek: Naglowek{
			Rodzaj:     "VIU-DO",
			Rok:        ret.QuarterStart.Year(),
			Kwartal:    q,
			Cel:        1,
			Wytworzono: generatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		},
		Podatnik: Podatnik{
			NIP:        taxpayer.NIP,
			PelnaNazwa: taxpayer.FullName,
			Email:      taxpayer.Email,
		},
		Sprzedaz:   rows,
		SumaNetto:  ret.TotalNet.StringFixed(2),
		SumaVat:    ret.TotalVat.StringFixed(2),
		LiczbaKraj: len(rows),
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("oss: encode: %w", err)
	}
	return buf.Bytes(), nil
}
