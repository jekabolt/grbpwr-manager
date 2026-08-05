package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestOutputVariantRegistry exercises the auxiliary colour-variant registry (migration 0252) against
// a real MySQL schema. Everything here is SQL that no unit test can reach: the joined read, the
// guards that hold a range inside the write transaction, the auto-created output bucket, the
// purpose-lock arm, and the list enrichment. StructScan correctness is half the point — a column
// with no destination field, or a NULL landing in a non-nullable one, only fails against a real
// schema, and every query in output_variants.go is new.
//
// SAFE ONLY against a local container DSN: this suite's TestMain drops every table on cleanup
// (mysql_test.go), so a prod/beta DSN would be destructive. The guard below refuses to run unless the
// DSN targets a container.
func TestOutputVariantRegistry(t *testing.T) {
	// Only run in CI (which points MYSQL_* at a container) or when the DSN explicitly targets a local
	// container. Otherwise skip — a bare local `go test ./internal/store/...` uses config.toml's prod
	// DSN, and this suite's TestMain drops all tables on cleanup (see mysql_test.go / project memory).
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	// fixture collects what a subtest created so it can be removed in FK order: variants first (they
	// RESTRICT the material), then the cards, then the buckets. Auto-created buckets are appended
	// once the store reports them, which is why this is a pointer collected at cleanup time rather
	// than a chain of per-object t.Cleanup calls.
	type fixture struct {
		cards     []int
		materials []int
	}
	newFixture := func(t *testing.T) *fixture {
		f := &fixture{}
		t.Cleanup(func() {
			bg := context.Background()
			for _, id := range f.cards {
				_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card_output_variant WHERE tech_card_id = ?", id)
			}
			for _, id := range f.cards {
				_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", id)
			}
			for _, id := range f.materials {
				_, _ = testDB.ExecContext(bg, "DELETE FROM material WHERE id = ?", id)
			}
		})
		return f
	}

	// unique keeps style numbers and names collision-free across runs against a reused container.
	unique := func(tag string) string { return fmt.Sprintf("%s-%d", tag, time.Now().UnixNano()%1_000_000_000) }

	mkCard := func(f *fixture, name string, purpose entity.TechCardPurpose, outputMaterial sql.NullInt64) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(unique("OV")),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: purpose, OutputMaterialId: outputMaterial,
		})
		require.NoError(t, err)
		f.cards = append(f.cards, id)
		return id
	}

	mkMaterial := func(f *fixture, name, unit string) int {
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: unique(name), Section: "packaging", MaterialClass: "packaging",
			Unit: ns(unit), Purpose: "production", CreatedBy: "tester", UpdatedBy: "tester",
		})
		require.NoError(t, err)
		f.materials = append(f.materials, id)
		return id
	}

	// setStock gives a bucket a material_stock row. A bucket with NO row is a distinct state the read
	// must preserve as "no balance recorded" rather than collapse to zero, so it is asserted too.
	setStock := func(materialID int, onHand string) {
		_, err := testDB.ExecContext(ctx,
			`INSERT INTO material_stock (material_id, on_hand) VALUES (?, ?)
			 ON DUPLICATE KEY UPDATE on_hand = VALUES(on_hand)`, materialID, onHand)
		require.NoError(t, err)
	}

	// (a) A create with material_id 0 mints the bucket itself, named "<card> — <colour>", copying the
	// section/class/unit/purpose of the card's existing output material (the template chain).
	t.Run("auto_creates_the_output_bucket_from_the_card_and_colour", func(t *testing.T) {
		f := newFixture(t)
		tmpl := mkMaterial(f, "OV template", "m")
		cardName := unique("Dust bag")
		cardID := mkCard(f, cardName, entity.TechCardPurposeAuxiliary,
			sql.NullInt64{Int64: int64(tmpl), Valid: true})

		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", Active: true}, "tester")
		require.NoError(t, err)
		require.Positive(t, id)

		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1)
		f.materials = append(f.materials, vs[0].MaterialId)

		require.Equal(t, cardName+" — black", vs[0].MaterialName,
			"the minted bucket is named after the card and the colour")
		// Attributes come from the template, not from the packaging defaults: the operator asked for a
		// colour, not for a catalog entry, so the bucket must match what the card already produces.
		require.Equal(t, "m", vs[0].Unit, "unit is copied from the card's existing output material")
		var section, class, purpose, color string
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT section, material_class, purpose, COALESCE(color, '') FROM material WHERE id = ?`,
			vs[0].MaterialId).Scan(&section, &class, &purpose, &color))
		require.Equal(t, "packaging", section)
		require.Equal(t, "packaging", class)
		require.Equal(t, "production", purpose)
		require.Equal(t, "black", color, "the bucket records the colour it belongs to")
		// min_stock stays NULL: the low-stock alert fires on any material with a threshold and no
		// stock, so a freshly minted empty bucket must not raise an alarm about a colour nobody has
		// produced yet.
		var minStock sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT min_stock FROM material WHERE id = ?`, vs[0].MaterialId).Scan(&minStock))
		require.False(t, minStock.Valid, "an auto-created bucket must not carry a low-stock threshold")
	})

	// (b) Adopting an existing material as a colour: the bucket keeps its identity, stock and history.
	t.Run("adopts_an_existing_material_as_a_colour", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Shopper"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV adopt", "pcs")
		setStock(matID, "42.000")

		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "WHT", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err)

		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1)
		require.Equal(t, id, vs[0].Id)
		require.Equal(t, matID, vs[0].MaterialId, "adoption must not mint a second bucket")
		require.True(t, vs[0].OnHand.Valid)
		require.Equal(t, "42", vs[0].OnHand.Decimal.String(),
			"the adopted bucket carries its existing stock under the new colour")

		// The same row re-saved by id updates in place rather than creating a second colour, and the
		// UPDATE path does honour `active` — that is where a colour is retired.
		again, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{Id: id, ColorCode: "WHT", MaterialId: matID, Active: false}, "tester")
		require.NoError(t, err)
		require.Equal(t, id, again)
		vs, err = T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1, "an update must not add a row")
		require.False(t, vs[0].Active, "deactivation is the normal retirement and keeps the bucket")
	})

	// A proto3 bool has no presence, so an omitted `active` arrives as false. A create must ignore it
	// and mint the colour ACTIVE — otherwise the card is in variant mode for the purpose lock (which
	// counts every row) but not for the list badge or the run guards (which are ACTIVE-only), and the
	// three disagree about what the card is.
	t.Run("a_created_colour_is_always_active", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Born active"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV born active", "pcs")

		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: false}, "tester")
		require.NoError(t, err)

		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1)
		require.True(t, vs[0].Active,
			"active=false on CREATE is ignored; a new colour is born active and is deactivated by a later update")

		// And the follow-up update is the way to retire it.
		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{Id: id, ColorCode: "BLK", MaterialId: matID, Active: false}, "tester")
		require.NoError(t, err)
		vs, err = T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.False(t, vs[0].Active)
	})

	// The legacy claim the table's UNIQUE cannot see: another card may hold this material as its
	// single tech_card.output_material_id, a column no UNIQUE has ever guarded.
	t.Run("refuses_a_material_that_is_another_cards_single_output", func(t *testing.T) {
		f := newFixture(t)
		matID := mkMaterial(f, "OV legacy output", "pcs")
		legacyCard := mkCard(f, unique("Legacy single output"), entity.TechCardPurposeAuxiliary,
			sql.NullInt64{Int64: int64(matID), Valid: true})
		otherID := mkCard(f, unique("Would adopt"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})

		_, err := T.UpsertOutputVariant(ctx, otherID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantMaterialClaimed)
		require.Contains(t, err.Error(), "single output",
			"the refusal must name the legacy mechanism, not just say 'claimed'")

		// SELF-adoption is the intended migration out of single-output mode and must still pass: the
		// card adopting its OWN output material is not a collision.
		id, err := T.UpsertOutputVariant(ctx, legacyCard,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err, "a card must be able to adopt its own output material as a colour")
		require.Positive(t, id)
	})

	// A bucket with no unit cannot take part in the one-unit-per-card rule and its received
	// quantities would mean nothing on the shelf — refused at the door rather than allowed to weaken
	// the rule for every later colour.
	t.Run("refuses_a_unitless_or_archived_material", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Picky"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})

		unitless, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: unique("OV unitless"), Section: "packaging", MaterialClass: "packaging",
			Purpose: "production", CreatedBy: "tester", UpdatedBy: "tester",
		})
		require.NoError(t, err)
		f.materials = append(f.materials, unitless)

		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: unitless, Active: true}, "tester")
		require.Error(t, err)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "material_id", ve.Field)
		require.Equal(t, "no_unit", ve.Reason)

		archived := mkMaterial(f, "OV archived", "pcs")
		require.NoError(t, T.ArchiveMaterial(ctx, archived, true))
		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "WHT", MaterialId: archived, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "archived", ve.Reason)
	})

	// A foreign/stale id must report NotFound even when the colour it carries collides with an
	// existing one — telling the caller to "edit the existing colour" would point them at a row they
	// cannot see from the id they sent.
	t.Run("a_foreign_id_reports_not_found_before_duplicate_colour", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Foreign id"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		otherID := mkCard(f, unique("Foreign id other"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		mine := mkMaterial(f, "OV foreign mine", "pcs")
		theirs := mkMaterial(f, "OV foreign theirs", "pcs")

		_, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: mine, Active: true}, "tester")
		require.NoError(t, err)
		foreign, err := T.UpsertOutputVariant(ctx, otherID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: theirs, Active: true}, "tester")
		require.NoError(t, err)

		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{Id: foreign, ColorCode: "BLK", MaterialId: mine, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantNotFound,
			"a foreign id is NotFound, not a duplicate-colour complaint")
	})

	// (c) One colour, one row. Retirement is active=false, never a second row for the same colour.
	t.Run("refuses_a_duplicate_colour_on_the_same_card", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Pouch"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		m1 := mkMaterial(f, "OV dup one", "pcs")
		m2 := mkMaterial(f, "OV dup two", "pcs")

		_, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: m1, Active: true}, "tester")
		require.NoError(t, err)

		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: m2, Active: true}, "tester")
		require.Error(t, err)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve, "a duplicate colour must be field-tagged, not a raw 1062")
		require.Equal(t, "color_code", ve.Field)
		require.Equal(t, "duplicate", ve.Reason)
	})

	// (d) uniq_tcov_material across cards: one bucket, one colour, one card — otherwise its moving
	// average blends two physically different articles.
	t.Run("refuses_a_material_another_cards_variant_already_claims", func(t *testing.T) {
		f := newFixture(t)
		ownerID := mkCard(f, unique("Owner bag"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		otherID := mkCard(f, unique("Other bag"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV claimed", "pcs")

		_, err := T.UpsertOutputVariant(ctx, ownerID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err)

		_, err = T.UpsertOutputVariant(ctx, otherID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantMaterialClaimed)
		require.Contains(t, err.Error(), "BLK", "the refusal names the colour that holds the claim")

		// Same card, different colour, same bucket is refused too — it is the same blend.
		_, err = T.UpsertOutputVariant(ctx, ownerID,
			entity.TechCardOutputVariantInsert{ColorCode: "WHT", MaterialId: matID, Active: true}, "tester")
		require.ErrorIs(t, err, entity.ErrOutputVariantMaterialClaimed)
	})

	// (e) One card, one unit: a run's quantity is booked per colour but counted once, and
	// material.unit freezes on the first movement, so the mismatch has to be caught at claim time.
	t.Run("refuses_a_bucket_measured_in_another_unit", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Mixed unit"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		pcs := mkMaterial(f, "OV unit pcs", "pcs")
		metres := mkMaterial(f, "OV unit m", "m")

		_, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: pcs, Active: true}, "tester")
		require.NoError(t, err)

		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "WHT", MaterialId: metres, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantUnitMismatch)
		require.Contains(t, err.Error(), "pcs")
		require.Contains(t, err.Error(), `"m"`)

		// The refusal is a rollback, not a partial write.
		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1, "the refused colour must not have landed")
	})

	// (f) A sellable style's colours are colourways — products, SKUs, product stock. It must never
	// gain a bucket in the material warehouse.
	t.Run("refuses_a_sellable_card", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Garment"), entity.TechCardPurposeSellable, sql.NullInt64{})
		matID := mkMaterial(f, "OV sellable", "pcs")

		_, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrTechCardNotAuxiliary)
		require.Contains(t, err.Error(), "sellable")
	})

	// (g) The joined read: colour name, bucket name and unit resolved in one round trip, and an
	// INVALID on_hand for a bucket that has no stock row at all.
	t.Run("list_resolves_the_joined_identity_and_leaves_unstocked_on_hand_null", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Joined read"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		stocked := mkMaterial(f, "OV joined stocked", "pcs")
		unstocked := mkMaterial(f, "OV joined unstocked", "pcs")
		setStock(stocked, "7.500")

		_, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: stocked, Active: true}, "tester")
		require.NoError(t, err)
		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "WHT", MaterialId: unstocked, Active: true}, "tester")
		require.NoError(t, err)

		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 2)
		byColour := map[string]entity.TechCardOutputVariant{}
		for _, v := range vs {
			byColour[v.ColorCode] = v
		}
		blk := byColour["BLK"]
		require.Equal(t, "black", blk.ColorName, "colour name comes from the dictionary join")
		require.Equal(t, "pcs", blk.Unit, "unit comes from the material join")
		require.Contains(t, blk.MaterialName, "OV joined stocked")
		require.True(t, blk.OnHand.Valid)
		require.Equal(t, "7.5", blk.OnHand.Decimal.String())
		require.Equal(t, cardID, blk.TechCardId)
		require.Equal(t, "tester", blk.CreatedBy)
		require.False(t, blk.CreatedAt.IsZero(), "audit timestamps must scan")

		wht := byColour["WHT"]
		require.Equal(t, "white", wht.ColorName)
		require.False(t, wht.OnHand.Valid,
			"a bucket with no stock row reads as 'no balance recorded', not as zero")
	})

	// (h) Delete is the hard action (the escape from the purpose lock); the bucket survives it.
	t.Run("delete_removes_the_variant_and_is_not_found_twice", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Deletable"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV deletable", "pcs")

		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err)

		require.NoError(t, T.DeleteOutputVariant(ctx, id))
		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Empty(t, vs)

		err = T.DeleteOutputVariant(ctx, id)
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantNotFound)

		// The warehouse bucket keeps its existence: unhooking a colour is not a catalog deletion.
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM material WHERE id = ?`, matID).Scan(&n))
		require.Equal(t, 1, n, "deleting a variant must not delete its material")
	})

	// (i) The fifth purpose-lock arm: any variant row pins the card as auxiliary, and deleting it is
	// the only escape (deactivating is deliberately not enough — the bucket is still the card's).
	t.Run("a_variant_pins_the_auxiliary_purpose_until_it_is_deleted", func(t *testing.T) {
		f := newFixture(t)
		cardName := unique("Flippable")
		cardID := mkCard(f, cardName, entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV flip", "pcs")

		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err)
		// Retire it: the lock counts EVERY row, active or not, so a deactivated colour must still pin
		// the purpose. It owns a warehouse bucket with stock and history that only an auxiliary card
		// can produce into, and deactivating does not give that up — deleting does.
		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{Id: id, ColorCode: "BLK", MaterialId: matID, Active: false}, "tester")
		require.NoError(t, err)

		flip := func(to entity.TechCardPurpose) error {
			card, err := T.GetTechCardById(ctx, cardID)
			require.NoError(t, err)
			return T.UpdateTechCard(ctx, cardID, &entity.TechCardInsert{
				Name: card.Name, Stage: card.Stage, StyleNumber: card.StyleNumber,
				MeasurementUnit: card.MeasurementUnit, ApprovalState: card.ApprovalState,
				Purpose: to, UpdatedBy: "tester",
			}, card.LockVersion)
		}

		err = flip(entity.TechCardPurposeSellable)
		require.Error(t, err, "a registered colour must pin the auxiliary purpose")
		require.ErrorIs(t, err, entity.ErrTechCardPurposeLocked)
		require.Contains(t, err.Error(), "colour variant",
			"the refusal must name the arm that fired")
		require.Contains(t, err.Error(), "delete them first",
			"deactivating is not the escape — the message must say so")

		// Deleting the colour releases the lock.
		require.NoError(t, T.DeleteOutputVariant(ctx, id))
		require.NoError(t, flip(entity.TechCardPurposeSellable))
		card, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		require.Equal(t, entity.TechCardPurposeSellable, card.Purpose)
	})

	// (j) The single-card read carries the colours, so an editor opening a card sees them without a
	// second call — and a card with none reports none rather than "not loaded".
	t.Run("get_tech_card_by_id_attaches_the_variants", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkCard(f, unique("Attached"), entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		matID := mkMaterial(f, "OV attached", "pcs")
		setStock(matID, "3.000")

		bare, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		require.Empty(t, bare.OutputVariants, "a card in legacy single-output mode carries no colours")

		_, err = T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: "GRY", MaterialId: matID, Active: true}, "tester")
		require.NoError(t, err)

		card, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, card.OutputVariants, 1)
		require.Equal(t, "GRY", card.OutputVariants[0].ColorCode)
		require.Equal(t, "grey", card.OutputVariants[0].ColorName)
		require.Equal(t, "3", card.OutputVariants[0].OnHand.Decimal.String())
	})

	// (k) The list enrichment summarises ACTIVE colours only — the badge answers "what can I plan",
	// not "what has ever existed" — and sums their on-hand in one grouped query.
	t.Run("list_enrichment_counts_active_variants_and_sums_their_stock", func(t *testing.T) {
		f := newFixture(t)
		cardName := unique("Enriched")
		cardID := mkCard(f, cardName, entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		live1 := mkMaterial(f, "OV enrich live1", "pcs")
		live2 := mkMaterial(f, "OV enrich live2", "pcs")
		retired := mkMaterial(f, "OV enrich retired", "pcs")
		setStock(live1, "10.000")
		setStock(live2, "5.500")
		setStock(retired, "999.000")

		for _, v := range []struct {
			code   string
			mat    int
			active bool
		}{
			{"BLK", live1, true},
			{"WHT", live2, true},
			{"RED", retired, false},
		} {
			// Every colour is born ACTIVE (a create ignores `active`), so a retired one is created and
			// then deactivated by a follow-up update — the only route into that state.
			id, err := T.UpsertOutputVariant(ctx, cardID,
				entity.TechCardOutputVariantInsert{ColorCode: v.code, MaterialId: v.mat, Active: true}, "tester")
			require.NoError(t, err)
			if !v.active {
				_, err = T.UpsertOutputVariant(ctx, cardID, entity.TechCardOutputVariantInsert{
					Id: id, ColorCode: v.code, MaterialId: v.mat, Active: false,
				}, "tester")
				require.NoError(t, err)
			}
		}

		cards, _, err := T.ListTechCards(ctx, 50, 0, entity.Ascending,
			entity.TechCardListFilter{Purpose: string(entity.TechCardPurposeAuxiliary), Name: cardName})
		require.NoError(t, err)
		var found *entity.TechCard
		for i := range cards {
			if cards[i].Id == cardID {
				found = &cards[i]
			}
		}
		require.NotNil(t, found, "the aux card must appear in its own filtered list")
		require.Equal(t, 2, found.OutputVariantCount,
			"a deactivated colour is not a colour this card currently makes")
		require.True(t, found.OutputVariantsOnHand.Valid)
		require.Equal(t, "15.5", found.OutputVariantsOnHand.Decimal.String(),
			"the retired colour's 999 must not be in the total")

		// A card whose active colours have NO stock row reports an INVALID total, not a zero: SUM over
		// an all-NULL group is NULL, and the row must render "—" rather than assert a measured zero.
		unstockedName := unique("Enriched unstocked")
		unstockedCard := mkCard(f, unstockedName, entity.TechCardPurposeAuxiliary, sql.NullInt64{})
		neverReceived := mkMaterial(f, "OV enrich unstocked", "pcs")
		_, err = T.UpsertOutputVariant(ctx, unstockedCard,
			entity.TechCardOutputVariantInsert{ColorCode: "BLK", MaterialId: neverReceived, Active: true}, "tester")
		require.NoError(t, err)

		cards, _, err = T.ListTechCards(ctx, 50, 0, entity.Ascending,
			entity.TechCardListFilter{Name: unstockedName})
		require.NoError(t, err)
		var unstockedRow *entity.TechCard
		for i := range cards {
			if cards[i].Id == unstockedCard {
				unstockedRow = &cards[i]
			}
		}
		require.NotNil(t, unstockedRow)
		require.Equal(t, 1, unstockedRow.OutputVariantCount)
		require.False(t, unstockedRow.OutputVariantsOnHand.Valid,
			"no bucket has a stock row, so there is no balance recorded — not a measured zero")

		// A sellable card is never asked about colours and reports none.
		otherName := unique("Enriched garment")
		otherID := mkCard(f, otherName, entity.TechCardPurposeSellable, sql.NullInt64{})
		cards, _, err = T.ListTechCards(ctx, 50, 0, entity.Ascending,
			entity.TechCardListFilter{Name: otherName})
		require.NoError(t, err)
		for i := range cards {
			if cards[i].Id == otherID {
				require.Zero(t, cards[i].OutputVariantCount)
				require.False(t, cards[i].OutputVariantsOnHand.Valid)
			}
		}
	})
}
