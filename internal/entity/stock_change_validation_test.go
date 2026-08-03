package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

func reasonOf(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// TestValidateStockChangeAcceptsEveryCombinationTheCodebaseWrites pins the source/reason/sign pairs
// the live journal writers actually produce. Enforcement only makes sense if this list is complete —
// a new writer that is not represented here will fail its write, loudly, on the first attempt.
func TestValidateStockChangeAcceptsEveryCombinationTheCodebaseWrites(t *testing.T) {
	cases := []struct {
		name   string
		source StockChangeSource
		reason string
		delta  string
	}{
		{"create colourway stock", StockChangeSourceAdminNewProduct, string(StockChangeReasonInitialStock), "5"},
		{"create colourway zero size", StockChangeSourceAdminNewProduct, string(StockChangeReasonInitialStock), "0"},
		{"update colourway reconcile up", StockChangeSourceManualAdjustment, string(StockChangeReasonCorrection), "3"},
		{"update colourway reconcile down", StockChangeSourceManualAdjustment, string(StockChangeReasonCorrection), "-3"},
		{"admin stock count", StockChangeSourceManualAdjustment, string(StockChangeReasonStockCount), "-2"},
		{"admin damage", StockChangeSourceManualAdjustment, string(StockChangeReasonDamage), "-1"},
		{"admin loss", StockChangeSourceManualAdjustment, string(StockChangeReasonLoss), "-1"},
		{"admin found", StockChangeSourceManualAdjustment, string(StockChangeReasonFound), "1"},
		{"admin reserved release", StockChangeSourceManualAdjustment, string(StockChangeReasonReservedRelease), "1"},
		{"admin other", StockChangeSourceManualAdjustment, string(StockChangeReasonOther), "-4"},
		{"paid order reduces", StockChangeSourceOrderPaid, string(StockChangeReasonOrder), "-1"},
		{"custom order reduces", StockChangeSourceOrderCustom, string(StockChangeReasonCustomOrder), "-1"},
		{"return restores", StockChangeSourceOrderReturned, string(StockChangeReasonReturnToStock), "1"},
		{"cancel restores", StockChangeSourceOrderCancelled, string(StockChangeReasonOrderCancelled), "1"},
		{"production receipt has no reason", StockChangeSourceProductionReceived, "", "50"},
		{"shipping row carries no movement", StockChangeSourceOrderPaid, string(StockChangeReasonOrder), "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateStockChange(string(c.source), reasonOf(c.reason), decimal.RequireFromString(c.delta)); err != nil {
				t.Fatalf("a combination the codebase writes was rejected: %v", err)
			}
		})
	}
}

func TestValidateStockChangeRejectsInconsistentRows(t *testing.T) {
	cases := []struct {
		name   string
		source string
		reason string
		delta  string
	}{
		{"unknown source", "warehouse_magic", "", "1"},
		{"reason from another source", string(StockChangeSourceOrderPaid), string(StockChangeReasonDamage), "-1"},
		{"a sale that adds stock", string(StockChangeSourceOrderPaid), string(StockChangeReasonOrder), "1"},
		{"a receipt that removes stock", string(StockChangeSourceProductionReceived), "", "-5"},
		{"a receipt with a borrowed reason", string(StockChangeSourceProductionReceived), string(StockChangeReasonFound), "5"},
		{"a cancel that removes stock", string(StockChangeSourceOrderCancelled), string(StockChangeReasonOrderCancelled), "-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateStockChange(c.source, reasonOf(c.reason), decimal.RequireFromString(c.delta)); err == nil {
				t.Fatalf("expected rejection, got nil")
			}
		})
	}
}

// TestStockChangeVocabularyMapsAgree: a source known to one map but not the other is a hole — the
// sign map decides whether a source is known at all, the reason map decides what it may carry.
func TestStockChangeVocabularyMapsAgree(t *testing.T) {
	for src := range AllowedSignForSource {
		if _, ok := ValidReasonsForSource[src]; !ok {
			t.Errorf("source %q has a sign rule but no reason list", src)
		}
	}
	for src := range ValidReasonsForSource {
		if _, ok := AllowedSignForSource[src]; !ok {
			t.Errorf("source %q has a reason list but no sign rule", src)
		}
	}
}
