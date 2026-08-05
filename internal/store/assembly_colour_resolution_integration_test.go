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
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAssemblyColourResolutionReads covers the two SQL reads behind the packing spec's colour
// resolution (phase 4): the output_variant_count the assembly bill now carries, and the batched
// ACTIVE-only variant read the spec resolves against. Both are new queries whose correctness is
// entirely in the schema — a correlated COUNT that must stay one row per assembly line, an IN-list
// read whose joined colour/material names must land in the right struct fields — so a real MySQL is
// the only place they can be proven.
//
// It stops one step short of an order: entity.ResolveAssemblyOutput is fed the rows these queries
// actually returned, which is the packing spec's shape without dragging an order, a payment and a
// shipment fixture into a read-only test. The rule itself is table-tested in internal/entity, and the
// handler wiring (batching, per-item colour) is mock-tested in internal/apisrv/admin.
//
// SAFE ONLY against a local container DSN: this suite's TestMain drops every table on cleanup
// (mysql_test.go), so a prod/beta DSN would be destructive. The guard below refuses to run otherwise.
func TestAssemblyColourResolutionReads(t *testing.T) {
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
	unique := func(tag string) string { return fmt.Sprintf("%s-%d", tag, time.Now().UnixNano()%1_000_000_000) }

	var cards []int
	var materials []int
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range cards {
			_, _ = testDB.ExecContext(bg, "DELETE FROM style_assembly WHERE style_id = ?", id)
			_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card_output_variant WHERE tech_card_id = ?", id)
		}
		for _, id := range cards {
			_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", id)
		}
		for _, id := range materials {
			_, _ = testDB.ExecContext(bg, "DELETE FROM material WHERE id = ?", id)
		}
	})

	mkCard := func(name string, purpose entity.TechCardPurpose, aux sql.NullString, output sql.NullInt64) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(unique("P4")),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: purpose, AuxSubtype: aux, OutputMaterialId: output,
		})
		require.NoError(t, err)
		cards = append(cards, id)
		return id
	}
	mkMaterial := func(name string) int {
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: unique(name), Section: "packaging", MaterialClass: "packaging",
			Unit: ns("pcs"), Purpose: "production", CreatedBy: "tester", UpdatedBy: "tester",
		})
		require.NoError(t, err)
		materials = append(materials, id)
		return id
	}

	careMaterial := mkMaterial("P4 care label")
	styleID := mkCard(unique("P4 Jacket"), entity.TechCardPurposeSellable, sql.NullString{}, sql.NullInt64{})
	// The dust bag keeps a LEGACY single output material AND gains colours — the exact shape whose
	// stale output_material_id must never win over a colour.
	staleMaterial := mkMaterial("P4 dust bag (stale single output)")
	dustBag := mkCard(unique("P4 Dust bag"), entity.TechCardPurposeAuxiliary,
		ns(string(entity.AuxSubtypeDustBag)), sql.NullInt64{Int64: int64(staleMaterial), Valid: true})
	careLabel := mkCard(unique("P4 Care label"), entity.TechCardPurposeAuxiliary,
		ns(string(entity.AuxSubtypeCareLabel)), sql.NullInt64{Int64: int64(careMaterial), Valid: true})

	require.NoError(t, T.UpsertStyleAssembly(ctx, styleID, []entity.StyleAssemblyInsert{
		{ComponentTechCardId: dustBag, Qty: decimal.RequireFromString("1"), Active: true},
		{ComponentTechCardId: careLabel, Qty: decimal.RequireFromString("1"), Active: true},
	}, "tester"))

	lineFor := func(t *testing.T, componentID int) entity.StyleAssembly {
		t.Helper()
		lines, err := T.ListStyleAssembly(ctx, styleID)
		require.NoError(t, err)
		require.Len(t, lines, 2, "the correlated COUNT must not multiply the bill's rows")
		for _, l := range lines {
			if l.ComponentTechCardId == componentID {
				return l
			}
		}
		t.Fatalf("component %d missing from the bill", componentID)
		return entity.StyleAssembly{}
	}

	// Before any colour exists: legacy single-output mode, and the bill says so with a zero count.
	require.Equal(t, 0, lineFor(t, dustBag).OutputVariantCount)

	blackID, err := T.UpsertOutputVariant(ctx, dustBag,
		entity.TechCardOutputVariantInsert{ColorCode: "BLK", Active: true}, "tester")
	require.NoError(t, err)
	whiteID, err := T.UpsertOutputVariant(ctx, dustBag,
		entity.TechCardOutputVariantInsert{ColorCode: "WHT", Active: true}, "tester")
	require.NoError(t, err)
	// The two auto-created buckets are the store's, so they join the cleanup list.
	created, err := T.ListOutputVariants(ctx, dustBag)
	require.NoError(t, err)
	require.Len(t, created, 2)
	for _, v := range created {
		materials = append(materials, v.MaterialId)
	}

	dustBagLine := lineFor(t, dustBag)
	require.Equal(t, 2, dustBagLine.OutputVariantCount, "the bill now reports two colours")
	require.Equal(t, 0, lineFor(t, careLabel).OutputVariantCount, "a colourless component stays at zero")
	require.Equal(t, int32(staleMaterial), dustBagLine.OutputMaterialId.Int32,
		"the legacy column still travels — it is provenance, not the answer")

	// legacyOf projects a bill line onto what the resolution rule reads.
	legacyOf := func(l entity.StyleAssembly) entity.AssemblyLegacyOutput {
		return entity.AssemblyLegacyOutput{
			MaterialId:   l.OutputMaterialId,
			MaterialName: l.OutputMaterialName,
			Archived:     l.OutputMaterialArchived.Bool,
		}
	}

	// The batched read: both components in ONE call, keyed by card, joined names resolved.
	byCard, err := T.ListOutputVariantsByCardIds(ctx, []int{dustBag, careLabel})
	require.NoError(t, err)
	require.Len(t, byCard[dustBag], 2)
	require.NotContains(t, byCard, careLabel, "a colourless card is simply absent, not an empty slice")
	colours := map[string]entity.TechCardOutputVariant{}
	for _, v := range byCard[dustBag] {
		require.Equal(t, dustBag, v.TechCardId)
		require.NotZero(t, v.MaterialId)
		require.NotEmpty(t, v.MaterialName, "material name resolves in the same round trip")
		require.False(t, v.MaterialArchived, "a freshly minted bucket is live")
		colours[v.ColorCode] = v
	}
	require.Equal(t, "black", colours["BLK"].ColorName)
	require.Equal(t, "white", colours["WHT"].ColorName)

	// The packing-spec shape, on the rows the store actually returned: a black garment takes the black
	// bucket, a green one takes nothing at all.
	black := entity.ResolveAssemblyOutput("BLK", byCard[dustBag], legacyOf(dustBagLine))
	require.False(t, black.Unresolved)
	require.Equal(t, entity.AssemblyResolutionColorMatch, black.Basis)
	require.Equal(t, colours["BLK"].MaterialId, black.ResolvedMaterialId)
	require.Equal(t, "black", black.ResolvedColorName)
	require.NotEqual(t, staleMaterial, black.ResolvedMaterialId)

	green := entity.ResolveAssemblyOutput("GRN", byCard[dustBag], legacyOf(dustBagLine))
	require.True(t, green.Unresolved, "no green bucket exists and two colours compete — refuse to guess")
	require.Equal(t, entity.AssemblyResolutionNoColorMatch, green.Basis)
	require.Zero(t, green.ResolvedMaterialId)

	careLine := lineFor(t, careLabel)
	care := entity.ResolveAssemblyOutput("BLK", byCard[careLabel], legacyOf(careLine))
	require.False(t, care.Unresolved)
	require.Equal(t, entity.AssemblyResolutionLegacyOutput, care.Basis)
	require.Equal(t, careMaterial, care.ResolvedMaterialId, "legacy single output stands for a colourless card")

	// An ARCHIVED bucket is withdrawn nomenclature: the colour still matches, but nobody may be sent
	// to that shelf. Asserted through the real archive path, not a hand-set flag.
	require.NoError(t, T.ArchiveMaterial(ctx, colours["BLK"].MaterialId, true))
	archivedRead, err := T.ListOutputVariantsByCardIds(ctx, []int{dustBag})
	require.NoError(t, err)
	for _, v := range archivedRead[dustBag] {
		if v.ColorCode == "BLK" {
			require.True(t, v.MaterialArchived, "material.archived travels with the variant")
		}
	}
	archivedRes := entity.ResolveAssemblyOutput("BLK", archivedRead[dustBag], legacyOf(dustBagLine))
	require.True(t, archivedRes.Unresolved, "an archived bucket is never prescribed")
	require.Equal(t, entity.AssemblyResolutionArchivedMaterial, archivedRes.Basis)
	require.NoError(t, T.ArchiveMaterial(ctx, colours["BLK"].MaterialId, false))

	// Retiring a colour drops it from the BADGE but NOT from the batched read — the rule needs the
	// retired row to tell "your colour is switched off" from "your colour does not exist".
	_, err = T.UpsertOutputVariant(ctx, dustBag, entity.TechCardOutputVariantInsert{
		Id: whiteID, ColorCode: "WHT", MaterialId: colours["WHT"].MaterialId, Active: false}, "tester")
	require.NoError(t, err)
	require.Equal(t, 1, lineFor(t, dustBag).OutputVariantCount, "a retired colour is not counted")
	byCard, err = T.ListOutputVariantsByCardIds(ctx, []int{dustBag})
	require.NoError(t, err)
	require.Len(t, byCard[dustBag], 2, "the retired row is still returned")
	require.Equal(t, blackID, byCard[dustBag][0].Id, "active colours lead")
	require.False(t, byCard[dustBag][1].Active)

	// A WHITE garment now hits the retired-colour gap instead of being handed the black bag.
	whiteItem := entity.ResolveAssemblyOutput("WHT", byCard[dustBag], legacyOf(dustBagLine))
	require.True(t, whiteItem.Unresolved, "the white bucket is retired, not missing")
	require.Equal(t, entity.AssemblyResolutionRetiredColor, whiteItem.Basis)
	require.Zero(t, whiteItem.ResolvedMaterialId)

	// A colour that never existed on the card still takes the sole survivor.
	sole := entity.ResolveAssemblyOutput("GRN", byCard[dustBag], legacyOf(dustBagLine))
	require.False(t, sole.Unresolved, "one live bucket left means no choice to get wrong")
	require.Equal(t, entity.AssemblyResolutionSoleVariant, sole.Basis)
	require.Equal(t, colours["BLK"].MaterialId, sole.ResolvedMaterialId)

	// Retire the last colour: an ALL-RETIRED card must NOT fall back to its stale single output.
	_, err = T.UpsertOutputVariant(ctx, dustBag, entity.TechCardOutputVariantInsert{
		Id: blackID, ColorCode: "BLK", MaterialId: colours["BLK"].MaterialId, Active: false}, "tester")
	require.NoError(t, err)
	require.Equal(t, 0, lineFor(t, dustBag).OutputVariantCount)
	byCard, err = T.ListOutputVariantsByCardIds(ctx, []int{dustBag})
	require.NoError(t, err)
	require.Len(t, byCard[dustBag], 2, "both retired rows are still visible to the rule")
	allRetired := entity.ResolveAssemblyOutput("GRN", byCard[dustBag], legacyOf(dustBagLine))
	require.True(t, allRetired.Unresolved,
		"a card that ever had colours never serves its stale output_material_id again")
	require.NotEqual(t, staleMaterial, allRetired.ResolvedMaterialId)
	require.Equal(t, entity.AssemblyResolutionNoColorMatch, allRetired.Basis)

	// An empty id list must not reach the IN-clause at all.
	empty, err := T.ListOutputVariantsByCardIds(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
