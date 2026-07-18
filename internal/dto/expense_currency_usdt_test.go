package dto

import (
	"testing"
	"time"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestExpenseValidatorsAcceptUSDT is the P0 assertion for the corrected currency model: USDT is an
// EXPENSE/accounting currency, so every expense validator must ACCEPT it (this is what feature #67's
// hard "3-letter ISO only" check wrongly forbade), while still rejecting an unknown/unsupported code
// such as CHF. The parallel selling-side rejection (a USDT product price) is asserted in
// store/product/colorway_price_validation_test.go.
func TestExpenseValidatorsAcceptUSDT(t *testing.T) {
	validFrom := timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("material price", func(t *testing.T) {
		got, err := ConvertPbMaterialPriceToEntity(&pb_common.MaterialPrice{
			MaterialId: 1, Price: dec("12.50"), Currency: "USDT", ValidFrom: validFrom,
		})
		require.NoError(t, err, "material price must accept USDT")
		require.Equal(t, "USDT", got.Currency)
		require.Equal(t, "12.5", got.Price.String())

		_, err = ConvertPbMaterialPriceToEntity(&pb_common.MaterialPrice{
			MaterialId: 1, Price: dec("12.50"), Currency: "CHF", ValidFrom: validFrom,
		})
		require.Error(t, err, "an unsupported expense currency (CHF) must still be rejected")
	})

	t.Run("dev expense", func(t *testing.T) {
		got, err := ConvertPbDevExpenseInsertToEntity(&pb_common.TechCardDevExpenseInsert{
			TechCardId: 1, Kind: "materials", Amount: dec("100"), Currency: "usdt", // lowercase → normalised
		})
		require.NoError(t, err, "dev expense must accept USDT")
		require.Equal(t, "USDT", got.Currency, "currency upper-cased")

		_, err = ConvertPbDevExpenseInsertToEntity(&pb_common.TechCardDevExpenseInsert{
			TechCardId: 1, Kind: "materials", Amount: dec("100"), Currency: "CHF",
		})
		require.Error(t, err, "an unsupported dev-expense currency (CHF) must still be rejected")
	})

	t.Run("production run cost", func(t *testing.T) {
		got, err := convertPbProductionRunCosts([]*pb_common.ProductionRunCost{{
			Kind:     pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_MATERIALS,
			Amount:   dec("100"),
			Currency: "USDT",
		}})
		require.NoError(t, err, "production run cost must accept USDT")
		require.Len(t, got, 1)
		require.Equal(t, "USDT", got[0].Currency)

		_, err = convertPbProductionRunCosts([]*pb_common.ProductionRunCost{{
			Kind:     pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_MATERIALS,
			Amount:   dec("100"),
			Currency: "CHF",
		}})
		require.Error(t, err, "an unsupported production-cost currency (CHF) must still be rejected")
	})

	t.Run("opex — employee default currency", func(t *testing.T) {
		got, err := ConvertPbEmployeeToEntity(&pb_admin.EmployeeInsert{
			FullName: "Мария", DefaultCurrency: "USDT",
		})
		require.NoError(t, err, "employee default_currency must accept USDT")
		require.True(t, got.DefaultCurrency.Valid && got.DefaultCurrency.String == "USDT")

		_, err = ConvertPbEmployeeToEntity(&pb_admin.EmployeeInsert{
			FullName: "x", DefaultCurrency: "CHF",
		})
		require.Error(t, err, "an unsupported employee currency (CHF) must still be rejected")
	})

	t.Run("opex line", func(t *testing.T) {
		got, err := ConvertPbOpexLinesToEntity([]*pb_admin.OpexLineInsert{{
			Month: "2026-01-01", Category: "rent", Label: "warehouse", Amount: dec("500"), Currency: "USDT",
		}})
		require.NoError(t, err, "opex line must accept USDT")
		require.Len(t, got, 1)
		require.Equal(t, "USDT", got[0].Currency)
	})

	t.Run("material lot receipt", func(t *testing.T) {
		got, err := ConvertPbReceiveMaterialStock(&pb_admin.ReceiveMaterialStockRequest{
			MaterialId: 1, Quantity: dec("5"), UnitCost: dec("10"), Currency: "USDT",
		})
		require.NoError(t, err, "material lot receipt must accept USDT")
		require.Equal(t, "USDT", got.Currency)

		_, err = ConvertPbReceiveMaterialStock(&pb_admin.ReceiveMaterialStockRequest{
			MaterialId: 1, Quantity: dec("5"), UnitCost: dec("10"), Currency: "CHF",
		})
		require.Error(t, err, "an unsupported material-lot currency (CHF) must still be rejected")
	})

	t.Run("bom line", func(t *testing.T) {
		got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{{
			Section:   pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
			Name:      "wool",
			Currency:  "USDT",
			UnitPrice: dec("8.00"),
		}})
		require.NoError(t, err, "bom line must accept USDT")
		require.Len(t, got, 1)
		require.Equal(t, "USDT", got[0].Currency.String)

		_, err = parseTechCardBomItems([]*pb_common.TechCardBomItem{{
			Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "wool", Currency: "CHF",
		}})
		require.Error(t, err, "an unsupported bom currency (CHF) must still be rejected")
	})

	t.Run("tech-card costing currency", func(t *testing.T) {
		got, err := parseTechCardCosting(&pb_common.TechCardCosting{Currency: "USDT"})
		require.NoError(t, err, "tech-card costing must accept USDT")
		require.NotNil(t, got)
		require.Equal(t, "USDT", got.Currency.String)

		_, err = parseTechCardCosting(&pb_common.TechCardCosting{Currency: "CHF"})
		require.Error(t, err, "an unsupported costing currency (CHF) must still be rejected")
	})
}
