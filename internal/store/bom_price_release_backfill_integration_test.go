package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestBomPriceReleaseBackfill pins the release-time price backfill end-to-end against a real
// schema: a linked BOM line whose catalog price appeared only AFTER the material was linked
// (unit_price NULL) takes the current catalog price at the moment the card goes RELEASED, inside
// the release transaction — stamped price_source='catalog'. Invariants under test: an agreed
// price is NEVER overwritten (even when the catalog disagrees), a free-text line is never
// touched, a linked material with no resolvable price leaves its line NULL without blocking the
// release, the save's own lock_version bump is the only bump, and the post-release consistent
// read (what the release snapshot marshals) already carries the filled price — which is then
// proven on the FROZEN DOCUMENT itself: the persisted tech_card_release.snapshot blob, produced
// by the same consistent-read → marshal → save sequence the apisrv release snapshot runs, must
// hold the backfilled price. The create-as-released path gets the same backfill.
func TestBomPriceReleaseBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Material A gets its price only AFTER it is linked (the prod defect). Material B has a
	// catalog price that DISAGREES with the line's agreed price. Material C never has a price.
	matA, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "RB late-priced lining", Section: "lining",
	})
	require.NoError(t, err)
	matB, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "RB agreed-price fabric", Section: "fabric",
	})
	require.NoError(t, err)
	require.NoError(t, s.TechCards().AddMaterialPrice(ctx, entity.MaterialPrice{
		MaterialId: matB, Price: decimal.RequireFromString("7.77"), Currency: "EUR",
		ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: "manual",
	}))
	matC, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "RB never-priced trim", Section: "trim",
	})
	require.NoError(t, err)

	const lateKey = "RBLATEPRICEDLINE0000000001"
	const agreedKey = "RBAGREEDPRICELINE000000001"
	const noPriceKey = "RBNEVERPRICEDLINE000000001"
	const freeKey = "RBFREETEXTLINE000000000001"
	card := &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "RB-BACKFILL", Valid: true}, Name: "RB-BACKFILL",
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		Costing:         &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
		BomItems: []entity.TechCardBomItem{
			{
				LineKey: lateKey, Section: entity.BomSectionLining, Name: "RB late-priced lining",
				MaterialId: sql.NullInt64{Int64: int64(matA), Valid: true},
			},
			{
				LineKey: agreedKey, Section: entity.BomSectionFabric, Name: "RB agreed-price fabric",
				MaterialId: sql.NullInt64{Int64: int64(matB), Valid: true},
				UnitPrice:  decimal.NullDecimal{Decimal: decimal.RequireFromString("5.00"), Valid: true},
				Currency:   sql.NullString{String: "EUR", Valid: true},
			},
			{
				LineKey: noPriceKey, Section: entity.BomSectionTrim, Name: "RB never-priced trim",
				MaterialId: sql.NullInt64{Int64: int64(matC), Valid: true},
			},
			{
				LineKey: freeKey, Section: entity.BomSectionPackaging, Name: "RB free-text box",
			},
		},
	}
	tcID, err := s.TechCards().AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	readLine := func(cardID int, lineKey string) (price sql.NullString, cur, src string, at sql.NullTime) {
		var curN, srcN sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT CAST(unit_price AS CHAR), currency, price_source, price_snapshot_at
			FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?`, cardID, lineKey).
			Scan(&price, &curN, &srcN, &at))
		return price, curN.String, srcN.String, at
	}

	// The price arrives in the catalog only now — after the material was linked. The line's
	// frozen unit_price stays NULL (that is the diagnosed defect this backfill closes at release).
	require.NoError(t, s.TechCards().AddMaterialPrice(ctx, entity.MaterialPrice{
		MaterialId: matA, Price: decimal.RequireFromString("9.99"), Currency: "EUR",
		ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: "manual",
	}))
	price, _, src, _ := readLine(tcID, lateKey)
	require.False(t, price.Valid, "a catalog price added after linking must not reach the line before release")
	require.Empty(t, src)
	_, _, agreedSrc, agreedAt := readLine(tcID, agreedKey)
	require.Equal(t, entity.BomPriceSourceManual, agreedSrc)
	require.True(t, agreedAt.Valid)

	// Release the card through the regular save. The backfill runs inside this transaction.
	stored, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	rel := stored.TechCardInsert
	rel.ApprovalState = entity.TechCardApprovalReleased
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, tcID, &rel, stored.LockVersion))

	// The save's own header UPDATE is the only lock_version bump — the backfill must not add one.
	var lockAfter int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT lock_version FROM tech_card WHERE id = ?", tcID).Scan(&lockAfter))
	require.Equal(t, stored.LockVersion+1, lockAfter,
		"release backfill must not bump lock_version beyond the save's own bump")

	// The NULL-priced linked line took the catalog price, stamped 'catalog'.
	price, cur, src, at := readLine(tcID, lateKey)
	require.True(t, price.Valid)
	require.Equal(t, "9.9900", price.String)
	require.Equal(t, "EUR", cur)
	require.Equal(t, entity.BomPriceSourceCatalog, src)
	require.True(t, at.Valid)

	// The agreed price is untouched — catalog 7.77 must NOT overwrite the agreed 5.00, and the
	// round-tripped provenance (manual, original stamp) survives.
	price, cur, src, at = readLine(tcID, agreedKey)
	require.Equal(t, "5.0000", price.String)
	require.Equal(t, "EUR", cur)
	require.Equal(t, entity.BomPriceSourceManual, src)
	require.Equal(t, agreedAt.Time.Unix(), at.Time.Unix())

	// A linked material with no resolvable price stays NULL — and did not block the release.
	price, _, src, at = readLine(tcID, noPriceKey)
	require.False(t, price.Valid)
	require.Empty(t, src)
	require.False(t, at.Valid)

	// The free-text line is never touched.
	price, _, src, _ = readLine(tcID, freeKey)
	require.False(t, price.Valid)
	require.Empty(t, src)

	// What the post-commit release snapshot marshals is the consistent read — it must already
	// see the filled price in the columns.
	released, err := s.TechCards().GetTechCardByIdConsistent(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardApprovalReleased, released.ApprovalState)
	var snapPrice decimal.NullDecimal
	for i := range released.BomItems {
		if released.BomItems[i].LineKey == lateKey {
			snapPrice = released.BomItems[i].UnitPrice
		}
	}
	require.True(t, snapPrice.Valid, "the release snapshot input must carry the backfilled price")
	require.True(t, snapPrice.Decimal.Equal(decimal.RequireFromString("9.99")))

	// The frozen release document itself — the whole reason the backfill exists. The apisrv
	// snapshotReleaseIfReleased is not constructible from this package (unexported Server fields),
	// so this reproduces its exact post-commit sequence with the SAME production code — the
	// consistent reload above → ConvertEntityTechCardToPb → protojson.Marshal →
	// SaveTechCardRelease — and then asserts on the PERSISTED tech_card_release.snapshot, parsed
	// the way GetTechCardRelease parses it. If the backfill ever drifts out of the release
	// transaction (runs after the snapshot, or in a later tx), the consistent reload sees NULL and
	// the blob assertion below fails.
	rates, err := s.TechCards().GetCostingFxRatesToBase(ctx)
	require.NoError(t, err)
	fx := dto.CostingFx{ToBase: rates, Base: cache.GetBaseCurrency()}
	blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(released, fx))
	require.NoError(t, err)
	unit, unitCurrency := dto.ComputeTechCardUnitCost(released, fx)
	require.NoError(t, s.TechCards().SaveTechCardRelease(ctx, entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: tcID,
			UnitCost:   unit,
			Currency:   sql.NullString{String: unitCurrency, Valid: unit.Valid && unitCurrency != ""},
		},
		Snapshot: string(blob),
	}))
	var storedSnap string
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT snapshot FROM tech_card_release
		WHERE tech_card_id = ? ORDER BY release_number DESC LIMIT 1`, tcID).Scan(&storedSnap))
	var snap pb_common.TechCard
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(storedSnap), &snap))
	frozenPrices := map[string]*pb_decimal.Decimal{}
	frozenSources := map[string]string{}
	for _, b := range snap.GetTechCard().GetBomItems() {
		frozenPrices[b.GetLineKey()] = b.GetUnitPrice()
		frozenSources[b.GetLineKey()] = b.GetPriceSource()
	}
	require.NotNil(t, frozenPrices[lateKey],
		"the frozen release document must carry the backfilled price, not NULL")
	require.True(t, decimal.RequireFromString(frozenPrices[lateKey].GetValue()).
		Equal(decimal.RequireFromString("9.99")))
	require.Equal(t, entity.BomPriceSourceCatalog, frozenSources[lateKey])
	require.NotNil(t, frozenPrices[agreedKey])
	require.True(t, decimal.RequireFromString(frozenPrices[agreedKey].GetValue()).
		Equal(decimal.RequireFromString("5.00")), "the frozen document keeps the agreed price")
	require.Nil(t, frozenPrices[noPriceKey],
		"an unpriceable line freezes as NULL — priced by nobody, claimed by nobody")

	// A card created DIRECTLY in released state freezes at commit the same way — same backfill.
	card2 := &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "RB-BACKFILL-2", Valid: true}, Name: "RB-BACKFILL-2",
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalReleased,
		MeasurementUnit: entity.TechCardUnitMm,
		Costing:         &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
		BomItems: []entity.TechCardBomItem{
			{
				LineKey: "RBCREATERELEASEDLINE000001", Section: entity.BomSectionLining,
				Name:       "RB late-priced lining",
				MaterialId: sql.NullInt64{Int64: int64(matA), Valid: true},
			},
		},
	}
	tc2ID, err := s.TechCards().AddTechCard(ctx, card2)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tc2ID) })
	price, cur, src, _ = readLine(tc2ID, "RBCREATERELEASEDLINE000001")
	require.True(t, price.Valid)
	require.Equal(t, "9.9900", price.String)
	require.Equal(t, "EUR", cur)
	require.Equal(t, entity.BomPriceSourceCatalog, src)
}
