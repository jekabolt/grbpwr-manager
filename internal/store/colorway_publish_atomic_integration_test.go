package store

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// requiredPricesExcept returns the full required-currency price set minus `drop` — a per-price-valid
// but INCOMPLETE set (legal to write to a DRAFT, illegal to publish with).
func requiredPricesExcept(drop string) []entity.ColorwayPriceInsert {
	out := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		if c == drop {
			continue
		}
		out = append(out, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	return out
}

// TestPublishColorwayEnforcesRequiredCurrencyCompleteness is the deterministic half of the FIX-1
// (P1 MONEY) acceptance test: the →ACTIVE edge now runs mint + preconditions + the required-currency
// completeness check + the status flip inside ONE serializable transaction (transitionColorwayLifecycle
// → applyColorwayTransition on rep.DB()). This asserts the completeness gate still fires against the
// PERSISTED prices on that path: publishing a DRAFT that is missing a required currency (PLN) is
// rejected and the colourway stays DRAFT (published_at NULL); once the full set is written it publishes
// and goes ACTIVE.
func TestPublishColorwayEnforcesRequiredCurrencyCompleteness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, fullPrices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TPAC1", "SS", "SS26", 2026)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	// PLN is a required currency (0182). Create the DRAFT with a set that omits it — legal for a DRAFT.
	partial := requiredPricesExcept("PLN")
	require.NotEmpty(t, partial)
	prd := newColorwayInsert("BLK", "black", "TPAC1-BLK", mediaID, langID, partial)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, partial)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", colorwayID) })
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	readState := func() (uint8, sql.NullTime) {
		var st uint8
		var pub sql.NullTime
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT lifecycle_status, published_at FROM product WHERE id = ?`, colorwayID).Scan(&st, &pub))
		return st, pub
	}

	// Publish must be REFUSED for the incomplete price set, and the refusal must be complete: the
	// colourway is still DRAFT and was never stamped published_at.
	err = s.Products().PublishColorway(ctx, colorwayID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLN", "publish must name the missing required currency")
	st, pub := readState()
	require.Equal(t, uint8(entity.ColorwayStatusDraft), st, "a rejected publish must leave the colourway DRAFT")
	require.False(t, pub.Valid, "a rejected publish must not stamp published_at")

	// Write the complete required set (legal on a DRAFT) and publish again — now it activates.
	var lockV int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&lockV))
	upd := newColorwayInsert("BLK", "black", "TPAC1-BLK", mediaID, langID, fullPrices)
	_, err = s.Products().UpdateColorway(ctx, colorwayID, lockV, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, fullPrices)
	require.NoError(t, err)

	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID))
	st, pub = readState()
	require.Equal(t, uint8(entity.ColorwayStatusActive), st, "a complete colourway must publish to ACTIVE")
	require.True(t, pub.Valid, "publish must stamp published_at")
}

// TestPublishColorwayActivationIsAtomicUnderConcurrentPriceReduction is the concurrency half of the
// FIX-1 acceptance test. It races PublishColorway (DRAFT→ACTIVE) against an UpdateColorway that reduces
// the DRAFT's prices to an incomplete set, and asserts the invariant the fix guarantees:
//
//	an ACTIVE colourway ALWAYS has a complete required-currency price set.
//
// Before the fix, checkColorwayRequiredCurrencies and the status flip were separate autocommit
// statements, so a concurrent price reduction could land between them: the check saw a complete set,
// the reduction committed, then the flip set ACTIVE — publishing incomplete pricing. With the →ACTIVE
// edge now wrapped in one serializable transaction (and UpdateColorway's own non-DRAFT completeness
// guard), the two possible serial orders are the only outcomes: (a) reduce-then-publish → publish sees
// the partial set and is refused (stays DRAFT); (b) publish-then-reduce → the reduction hits an ACTIVE
// colourway and is refused by validateRequiredCurrenciesPresent. Either way an ACTIVE colourway is
// never left incomplete.
func TestPublishColorwayActivationIsAtomicUnderConcurrentPriceReduction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, fullPrices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TPAC2", "SS", "SS26", 2026)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	prd := newColorwayInsert("BLK", "black", "TPAC2-BLK", mediaID, langID, fullPrices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, fullPrices)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", colorwayID) })
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	// Publish once so the base + variant SKUs and the style's model_no are minted; then reset to DRAFT
	// for the race so every iteration starts from a fully-publishable state.
	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID))

	resetDraftWithFullPrices := func() {
		_, err := testDB.ExecContext(ctx,
			`UPDATE product SET lifecycle_status = ?, published_at = NULL WHERE id = ?`,
			uint8(entity.ColorwayStatusDraft), colorwayID)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `DELETE FROM product_price WHERE product_id = ?`, colorwayID)
		require.NoError(t, err)
		for _, c := range currency.RequiredCurrencies() {
			_, err = testDB.ExecContext(ctx,
				`INSERT INTO product_price (product_id, currency, price) VALUES (?, ?, ?)`, colorwayID, c, "10000.00")
			require.NoError(t, err)
		}
	}
	activeCount := 0
	partial := requiredPricesExcept("PLN")
	for i := 0; i < 20; i++ {
		resetDraftWithFullPrices()
		var lockV int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&lockV))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.Products().PublishColorway(ctx, colorwayID) // may legitimately fail if the reduction won the race
		}()
		go func() {
			defer wg.Done()
			upd := newColorwayInsert("BLK", "black", "TPAC2-BLK", mediaID, langID, partial)
			_, _ = s.Products().UpdateColorway(ctx, colorwayID, lockV, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, partial)
		}()
		wg.Wait()

		var st uint8
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&st))
		if st != uint8(entity.ColorwayStatusActive) {
			continue
		}
		activeCount++
		// The core invariant: an ACTIVE colourway must carry every required currency.
		rows, err := testDB.QueryContext(ctx, `SELECT currency FROM product_price WHERE product_id = ?`, colorwayID)
		require.NoError(t, err)
		provided := map[string]bool{}
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			provided[strings.ToUpper(c)] = true
		}
		require.NoError(t, rows.Err())
		rows.Close()
		require.Empty(t, currency.MissingRequired(provided),
			"iteration %d: an ACTIVE colourway must have a complete required-currency price set", i)
	}
	// Not an invariant (scheduling-dependent), just a signal the race was actually exercised.
	t.Logf("publish won the race and activated in %d/20 iterations", activeCount)
}
