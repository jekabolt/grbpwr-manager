// Package materialcode composes self-describing article codes for catalog materials.
package materialcode

import (
	"database/sql"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

var finishAbbreviation = map[string]string{
	"matte":    "MAT",
	"satin":    "SAT",
	"gloss":    "GLS",
	"glossy":   "GLS",
	"polished": "POL",
	"brushed":  "BRU",
	"antique":  "ANT",
}

// ComposeArticle builds the base article code used when an operator leaves a new material's code
// blank. Tokens whose source is unset or zero are omitted.
func ComposeArticle(m *entity.MaterialInsert) string {
	if m == nil {
		return "OTH"
	}

	tokens := make([]string, 0, 6)
	if supplier := abbreviation(nullString(m.Supplier)); supplier != "" {
		tokens = append(tokens, supplier)
	}

	switch strings.ToLower(strings.TrimSpace(m.MaterialClass)) {
	case string(entity.MaterialClassFabric):
		tokens = append(tokens, "FAB")
		fibre := firstFibre(m.CompositionEntries)
		gsm := roundedDecimal(preferredDecimal(m.FabricAttr, m.FabricWeightGsm, func(a *entity.MaterialFabricAttr) decimal.NullDecimal {
			return a.WeightGsm
		}))
		if fibre != "" || gsm != "" {
			tokens = append(tokens, fibre+gsm)
		}
		if width := roundedDecimal(preferredDecimal(m.FabricAttr, m.FabricWidth, func(a *entity.MaterialFabricAttr) decimal.NullDecimal {
			return a.WidthCm
		})); width != "" {
			tokens = append(tokens, "W"+width)
		}
	case string(entity.MaterialClassHardware):
		tokens = append(tokens, "HW")
		if m.HardwareAttr != nil {
			if base := abbreviation(nullString(m.HardwareAttr.BaseMaterial)); base != "" {
				tokens = append(tokens, base)
			}
			if diameter := roundedDecimal(m.HardwareAttr.DiameterMm); diameter != "" {
				tokens = append(tokens, diameter)
			}
			if finish := strings.TrimSpace(nullString(m.HardwareAttr.Finish)); finish != "" {
				token := finishAbbreviation[strings.ToLower(finish)]
				if token == "" {
					token = abbreviation(finish)
				}
				if token != "" {
					tokens = append(tokens, token)
				}
			}
		}
	case string(entity.MaterialClassThread):
		tokens = append(tokens, "THR")
		if fibre := firstFibre(m.CompositionEntries); fibre != "" {
			tokens = append(tokens, fibre)
		}
	case string(entity.MaterialClassPackaging):
		tokens = append(tokens, "PKG")
		if m.PackagingAttr != nil {
			if gsm := roundedDecimal(m.PackagingAttr.Gsm); gsm != "" {
				tokens = append(tokens, "G"+gsm)
			}
		}
	default:
		tokens = append(tokens, "OTH")
	}

	if colour := abbreviation(nullString(m.Color)); colour != "" {
		tokens = append(tokens, colour)
	}
	return strings.Join(tokens, "·")
}

func firstFibre(entries []entity.CompositionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	return abbreviation(entries[0].FiberCode)
}

func preferredDecimal(
	typed *entity.MaterialFabricAttr,
	legacy decimal.NullDecimal,
	value func(*entity.MaterialFabricAttr) decimal.NullDecimal,
) decimal.NullDecimal {
	if typed != nil {
		if v := value(typed); v.Valid && !v.Decimal.IsZero() {
			return v
		}
	}
	return legacy
}

func nullString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func roundedDecimal(value decimal.NullDecimal) string {
	if !value.Valid || value.Decimal.IsZero() {
		return ""
	}
	return value.Decimal.Round(0).String()
}

// abbreviation keeps the first three ASCII letters and upper-cases them. Article-code tokens are
// deliberately restricted to A-Z even when the descriptive source contains punctuation or digits.
func abbreviation(value string) string {
	var out strings.Builder
	out.Grow(3)
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r)
		case r >= 'a' && r <= 'z':
			out.WriteRune(r - ('a' - 'A'))
		default:
			continue
		}
		if out.Len() == 3 {
			break
		}
	}
	return out.String()
}
