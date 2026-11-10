package techcard

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func threadBirthArticle(unit, length string) entity.MaterialWithPrice {
	m := entity.MaterialWithPrice{Material: entity.Material{MaterialInsert: entity.MaterialInsert{
		Unit: sql.NullString{String: unit, Valid: unit != ""},
	}}}
	if length != "" {
		m.ThreadAttr = &entity.MaterialThreadAttr{
			LengthPerConeM: decimal.NewNullDecimal(decimal.RequireFromString(length)),
		}
	}
	return m
}

func TestNormalizeNewThreadBomLine(t *testing.T) {
	for _, tc := range []struct {
		name, articleUnit, length, lineUnit, price, wantUnit, wantPrice string
		priced                                                          bool
	}{
		{"cone", "cone", "5000", "cone", "5", "m", "0.001", true},
		{"centimetres", "cm", "", "cm", "0.02", "m", "2", true},
		{"missing package length", "spool", "", "spool", "5", "m", "", false},
		{"missing package length already metres", "spool", "", "m", "5", "m", "", false},
		{"mass stays mass", "kg", "5000", "kg", "5", "kg", "5", true},
		{"already metre is not divided twice", "cone", "5000", "m", "0.001", "m", "0.001", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := entity.TechCardBomItem{
				Section: entity.BomSectionThread,
				Unit:    sql.NullString{String: tc.lineUnit, Valid: tc.lineUnit != ""},
				UnitPrice: decimal.NullDecimal{
					Decimal: decimal.RequireFromString(tc.price), Valid: tc.price != "",
				},
				Currency: sql.NullString{String: "EUR", Valid: true},
			}
			normalizeNewThreadBomLine(&line, threadBirthArticle(tc.articleUnit, tc.length))
			require.Equal(t, tc.wantUnit, line.Unit.String)
			require.Equal(t, tc.priced, line.UnitPrice.Valid)
			if tc.priced {
				require.Equal(t, tc.wantPrice, line.UnitPrice.Decimal.String())
			} else {
				require.False(t, line.Currency.Valid)
			}
		})
	}
}

func TestTechCardSQLDoesNotResolveBomUnitArticleFirst(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NotContains(t, string(body), "COALESCE(NULLIF(m.unit",
			"%s resolves a BOM unit article-first instead of entity.ResolveBomLineUnit", name)
	}
}
