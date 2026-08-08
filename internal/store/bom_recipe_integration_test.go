package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestUpdateColorwayRecipeRoundTrip is the contract test for the restored recipe write-path
// (WS3 / S2-S3; closes the A3.4 "silent no-op" — ColorwayDevelopmentInsert.usages was accepted on the
// wire but never written). A colourway recipe written via UpdateColorwayRecipe persists and reads
// back, referencing the style BOM by stable line_key resolved to a real bom_item_id FK. A stale
// shared version is rejected.
// seedRecipeStampMarkers кладёт на карточку две настоящие раскладки — ровно чтобы штамп Ф6.8 было
// чем называть. Сырым INSERT'ом, а не через SaveMarker: тесту нужны только ИДЕНТИЧНОСТЬ и
// ПРИНАДЛЕЖНОСТЬ карточке, а не геометрия, и полноценная фикстура раскладки (блоб, состав, размерный
// ряд) утащила бы сюда половину markers_integration_test и переписывалась бы на каждой правке Ф2.
func seedRecipeStampMarkers(ctx context.Context, t *testing.T, db *sql.DB, techCardID int) (int64, int64) {
	t.Helper()
	one := func(name string) int64 {
		res, err := db.ExecContext(ctx, `INSERT INTO tech_card_marker
			(tech_card_id, size_id, name, source, fabric_width_cm, used_length_cm,
			 placed_count, total_count, total_units, layout)
			VALUES (?, NULL, ?, 'auto', 140, 120, 1, 1, 1, '{}')`, techCardID, name)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	return one("штамп А"), one("штамп Б")
}

func TestUpdateColorwayRecipeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Recipe Style", Stage: entity.TechCardStageProto, StyleNumber: ns("RCP-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "RK1", Section: entity.TechCardBomSection("fabric"), Name: "Main Fabric"},
			{LineKey: "RK2", Section: entity.TechCardBomSection("thread"), Name: "Thread"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	pinnedMaterialID, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Pinned Recipe Fabric", Section: "fabric", Unit: ns("m"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", pinnedMaterialID)
	})

	// A colourway is a product under the style (post-R1 merge).
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("RCP-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, err := res.LastInsertId()
	require.NoError(t, err)
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	// Write a recipe referencing the fabric BOM line by its stable line_key.
	newVer, err := T.UpdateColorwayRecipe(ctx, cwID, card.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5")),
			MaterialId:  sql.NullInt64{Int64: int64(pinnedMaterialID), Valid: true}, MaterialIdSet: true},
	})
	require.NoError(t, err)
	require.Equal(t, card.LockVersion+1, newVer, "recipe write bumps the shared lock")

	// Read back: the recipe persisted and resolved to a real bom_item_id (was a silent no-op before).
	card2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	var found *entity.TechCardColorwayUsage
	for i := range card2.Colorways {
		if card2.Colorways[i].Id == cwID {
			require.Len(t, card2.Colorways[i].Usages, 1)
			found = &card2.Colorways[i].Usages[0]
		}
	}
	require.NotNil(t, found, "recipe usage must read back")
	require.True(t, found.BomItemId.Valid, "usage resolved line_key -> real bom_item_id")
	require.Equal(t, "outer", found.Placement.String)
	require.True(t, found.MaterialId.Valid, "explicit material pin must remain set")
	require.Equal(t, int64(pinnedMaterialID), found.MaterialId.Int64, "explicit material pin must round-trip")

	// An older client omits material_id while adding another whole-garment usage of the same BOM
	// line. Only the matching placement inherits the old pin; a genuinely new placement stays
	// unpinned and follows the BOM slot default.
	latestVer, err := T.UpdateColorwayRecipe(ctx, cwID, newVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns(" OUTER "), Color: ns("black")},
		{BomLineKey: "RK1", Placement: ns("lining"), Color: ns("black")},
	})
	require.NoError(t, err)
	require.Equal(t, newVer+1, latestVer)
	recipe, err := T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 2)
	byPlacement := make(map[string]entity.TechCardColorwayUsage, len(recipe))
	for _, usage := range recipe {
		byPlacement[strings.ToLower(strings.TrimSpace(usage.Placement.String))] = usage
	}
	require.True(t, byPlacement["outer"].MaterialId.Valid,
		"same normalized placement retains a material pin")
	require.Equal(t, int64(pinnedMaterialID), byPlacement["outer"].MaterialId.Int64,
		"same normalized placement inherits its one prior pin")
	require.False(t, byPlacement["lining"].MaterialId.Valid,
		"new placement must not inherit another whole-garment usage's pin")

	// A stale shared version is rejected (optimistic lock).
	_, err = T.UpdateColorwayRecipe(ctx, cwID, newVer, nil)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)

	// Provenance triple (Ф9.4): a marker-sourced norm round-trips with its decomposition, and a
	// stale client's presence-less rewrite preserves it (same protocol as the material pin) —
	// resetting it to manual would silently re-enable the wastage gross-up in costing.
	markerVer, err := T.UpdateColorwayRecipe(ctx, cwID, latestVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(decimal.RequireFromString("1.65")),
			WasteCutPct:       decimal.NewNullDecimal(decimal.RequireFromString("21.95"))},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 1)
	require.Equal(t, entity.ConsumptionSourceMarker, recipe[0].ConsumptionSource.String)
	require.Equal(t, "1.65", recipe[0].WasteSelvedgePct.Decimal.String())
	require.Equal(t, "21.95", recipe[0].WasteCutPct.Decimal.String())

	staleVer, err := T.UpdateColorwayRecipe(ctx, cwID, markerVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.42"))},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, entity.ConsumptionSourceMarker, recipe[0].ConsumptionSource.String,
		"presence-less rewrite must preserve marker provenance")
	require.Equal(t, "21.95", recipe[0].WasteCutPct.Decimal.String())

	// A low-efficiency раскладка wastes MORE cloth than it turns into pieces, so the cut
	// component (1/efficiency − 1, of the piece area) exceeds 100. The column and its CHECK were
	// widened to 1000 in 0263 for exactly this — before it, such a marker failed the whole
	// recipe save on a constraint the operator could not act on.
	wideVer, err := T.UpdateColorwayRecipe(ctx, cwID, staleVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(decimal.RequireFromString("4.10")),
			WasteCutPct:       decimal.NewNullDecimal(decimal.RequireFromString("122.22"))},
	})
	require.NoError(t, err, "cut waste above 100%% of piece area is a real marker, not bad input")
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, "122.22", recipe[0].WasteCutPct.Decimal.String())
	staleVer = wideVer

	// An explicit manual write clears the decomposition.
	manualVer, err := T.UpdateColorwayRecipe(ctx, cwID, staleVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true}},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, entity.ConsumptionSourceManual, recipe[0].ConsumptionSource.String)
	require.False(t, recipe[0].WasteSelvedgePct.Valid)
	require.False(t, recipe[0].WasteCutPct.Valid)

	// ───────────────────────────── Ф6.8. ШТАМП НОРМЫ (0291) ─────────────────────────────
	//
	// The stamp says WHICH раскладка a marker-sourced consumption came from and WHEN it was applied,
	// so the card can report «норма применена тогда-то, а раскладка с тех пор изменена». It lives
	// on a table written by FULL REPLACE, which is why every case below is about what a save does to
	// a column it did NOT send.
	//
	// Раскладки НАСТОЯЩИЕ. FK на них нет и не будет (0291), но явно присланный штамп обязан называть
	// раскладку ЭТОЙ карточки — иначе экран печатал бы «раскладка удалена» и на клиентской опечатке,
	// и на чужой карточке одинаково, то есть врал бы правдоподобно. Отсутствие FK доказывается не
	// приёмом мусора на запись, а тем, что ПЕРЕНЕСЁННЫЙ штамп переживает удаление своей раскладки —
	// это проверяет случай (7) ниже.
	markerA, markerB := seedRecipeStampMarkers(ctx, t, testDB, tcID)
	stampSet := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

	// (1) Applying a norm from раскладка A stamps both columns.
	appliedVer, err := T.UpdateColorwayRecipe(ctx, cwID, manualVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(decimal.RequireFromString("1.65")),
			WasteCutPct:       decimal.NewNullDecimal(decimal.RequireFromString("21.95")),
			NormMarkerId:      stampSet(markerA), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.True(t, recipe[0].NormMarkerId.Valid, "applying from a раскладка must stamp its id")
	require.Equal(t, markerA, recipe[0].NormMarkerId.Int64)
	require.True(t, recipe[0].NormAppliedAt.Valid, "applying from a раскладка must stamp the moment")

	// BACKDATE THE STAMP BEFORE ASSERTING ANYTHING ABOUT «did it move», and this is not a
	// convenience. norm_applied_at is a plain TIMESTAMP (second resolution, matching
	// tech_card_marker.updated_at, which is what it is compared against), and every write in this
	// test lands inside the same second. Comparing two NOW()s would therefore report «unchanged»
	// whether the server carried the stamp across or re-stamped it — the carry assertion below would
	// pass vacuously, and the case it is meant to protect (an edit of a neighbouring field
	// extinguishing the drift indicator) would sail straight through. Against a value from 2020 both
	// outcomes are distinguishable with no sleeping and no tolerance.
	//
	// Patched by colorway_id, not by usage id: the recipe is written by FULL REPLACE, so the row's id
	// changes on every save.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_colorway_usage SET norm_applied_at = '2020-01-02 03:04:05' WHERE colorway_id = ?`, cwID)
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.True(t, recipe[0].NormAppliedAt.Valid)
	backdated := recipe[0].NormAppliedAt.Time
	require.Equal(t, 2020, backdated.Year(), "sanity: the backdate landed")

	// (2) THE CASE THIS COLUMN EXISTS FOR: today's deployed client knows 'marker' but not the stamp,
	// so it echoes consumption_source and omits norm_marker_id entirely. A full-replace that took the
	// absence literally would blank the audit on the operator's next ordinary save.
	staleStampVer, err := T.UpdateColorwayRecipe(ctx, cwID, appliedVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(decimal.RequireFromString("1.65")),
			WasteCutPct:       decimal.NewNullDecimal(decimal.RequireFromString("21.95"))},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.True(t, recipe[0].NormMarkerId.Valid,
		"a client that omits norm_marker_id must not blank the stamp")
	require.Equal(t, markerA, recipe[0].NormMarkerId.Int64)
	require.True(t, recipe[0].NormAppliedAt.Valid)
	require.True(t, backdated.Equal(recipe[0].NormAppliedAt.Time),
		"a presence-less save must carry the moment across verbatim")

	// (3) THE RULE THIS FEATURE TURNS ON. An edit of a NEIGHBOURING field (the consumption itself),
	// re-sending the SAME marker id, must NOT move norm_applied_at. If it did, the «раскладка
	// изменена» indicator would go dark because somebody corrected an unrelated number next to it —
	// and the breakage would be indistinguishable from the divergence having been resolved.
	//
	// Compared verbatim against the value read before the write, not against a tolerance: the whole
	// point is that the server did not touch it.
	neighbourVer, err := T.UpdateColorwayRecipe(ctx, cwID, staleStampVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.77")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(decimal.RequireFromString("1.65")),
			WasteCutPct:       decimal.NewNullDecimal(decimal.RequireFromString("21.95")),
			NormMarkerId:      stampSet(markerA), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, "1.77", recipe[0].Consumption.Decimal.String(), "the neighbouring edit did land")
	require.Equal(t, markerA, recipe[0].NormMarkerId.Int64)
	require.True(t, backdated.Equal(recipe[0].NormAppliedAt.Time),
		"editing a neighbouring field must NOT move norm_applied_at — that would extinguish the drift indicator")

	// (4) Applying a DIFFERENT раскладка is a new decision, so the moment moves with it.
	otherVer, err := T.UpdateColorwayRecipe(ctx, cwID, neighbourVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.31")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			NormMarkerId:      stampSet(markerB), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, markerB, recipe[0].NormMarkerId.Int64)
	require.True(t, recipe[0].NormAppliedAt.Valid)
	require.False(t, backdated.Equal(recipe[0].NormAppliedAt.Time),
		"a different раскладка is a new application and must re-stamp the moment")
	require.True(t, recipe[0].NormAppliedAt.Time.After(backdated),
		"the re-stamp moves the moment forward, to now")

	// (5) An EXPLICIT 0 is the client saying «this norm is no longer from a раскладка». Presence with
	// a non-positive value clears, exactly like the material pin — and the moment goes with the id,
	// because a moment with no раскладка answers no question.
	clearedVer, err := T.UpdateColorwayRecipe(ctx, cwID, otherVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.31")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			NormMarkerId:      sql.NullInt64{}, NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.False(t, recipe[0].NormMarkerId.Valid, "an explicit 0 clears the stamp")
	require.False(t, recipe[0].NormAppliedAt.Valid, "the moment is cleared with the id it was about")

	// (6) DEMOTION TO MANUAL clears both. Two flavours, and both must land in the same place:
	// first an explicit manual save carrying a stamp (a confused client), then the SILENT demotion
	// the store performs when a carried 'marker' row sits on a BOM line that is not roll goods.
	restampVer, err := T.UpdateColorwayRecipe(ctx, cwID, clearedVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.31")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			NormMarkerId:      stampSet(markerA), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	demotedVer, err := T.UpdateColorwayRecipe(ctx, cwID, restampVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.31")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
			NormMarkerId:      stampSet(markerA), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Equal(t, entity.ConsumptionSourceManual, recipe[0].ConsumptionSource.String)
	require.False(t, recipe[0].NormMarkerId.Valid,
		"a manual norm cannot claim a раскладка — demotion must clear the stamp")
	require.False(t, recipe[0].NormAppliedAt.Valid)

	// The silent demotion: RK2 is a thread line, so a carried 'marker' provenance is quietly demoted
	// rather than refused (a stale client's presence-less save must not fail). The stamp must fall
	// with it — otherwise a manual thread row would still name a раскладка.
	_, err = T.UpdateColorwayRecipe(ctx, cwID, demotedVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK2", Placement: ns("seams"), Color: ns("black"),
			Quantity:     decimal.NewNullDecimal(decimal.RequireFromString("2")),
			NormMarkerId: stampSet(markerA), NormMarkerIdSet: true},
	})
	require.NoError(t, err, "a stamp on a non-roll-goods line is demoted, not refused")
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 1)
	require.Equal(t, entity.ConsumptionSourceManual, recipe[0].ConsumptionSource.String)
	require.False(t, recipe[0].NormMarkerId.Valid,
		"silent demotion off roll goods must clear the stamp together with the pcts")
	require.False(t, recipe[0].NormAppliedAt.Valid)

	// (7) ОТСУТСТВИЕ FK — ЭТО ПРО ПЕРЕНОС, А НЕ ПРО ПРИЁМ МУСОРА. Явно присланный штамп обязан
	// называть раскладку этой карточки; а вот УЖЕ ПОСТАВЛЕННЫЙ переживает удаление своей раскладки и
	// продолжает ездить на строке — именно это и значит «раскладка удалена» на экране. RESTRICT
	// запретил бы удаление, SET NULL стёр бы память, и обе развязки были бы хуже висящего id.
	stampedVer, err := T.UpdateColorwayRecipe(ctx, cwID, demotedVer+1, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.42")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			NormMarkerId:      stampSet(markerB), NormMarkerIdSet: true},
	})
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_marker WHERE id = ?", markerB)
	require.NoError(t, err)
	// Стейлый клиент (штамп не эхает) сохраняет рецепт после удаления раскладки.
	_, err = T.UpdateColorwayRecipe(ctx, cwID, stampedVer, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.44")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true}},
	})
	require.NoError(t, err, "удаление раскладки не имеет права запирать правку рецепта")
	recipe, err = T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.True(t, recipe[0].NormMarkerId.Valid,
		"перенесённый штамп переживает удаление своей раскладки — висящий id и есть «удалена»")
	require.Equal(t, markerB, recipe[0].NormMarkerId.Int64)

	// А вот НАЗВАТЬ несуществующую раскладку явно — нельзя: иначе опечатка клиента и чужая карточка
	// печатались бы на экране одинаково, как «раскладка удалена».
	_, err = T.UpdateColorwayRecipe(ctx, cwID, stampedVer+1, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("1.44")),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
			NormMarkerId:      stampSet(markerB), NormMarkerIdSet: true},
	})
	require.Error(t, err, "явный штамп на удалённую раскладку — это заявка на происхождение, которого нет")
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "usages[0].norm_marker_id", ve.Field)
}

// TestMarkerNormBumpsTechCardLock pins the Ф6.8 revision of the rule in markers.go's header. Marker
// writes were flatly forbidden from bumping tech_card.lock_version, because saving a раскладка from
// the nesting modal would 409 the same operator's open card form. That argument holds for an
// ORDINARY раскладка and fails for the НОРМА: the norm is the number the card promises (the
// readiness gate and the apply dialog both read it), so designating one — or taking it away, or
// re-shooting the one in force — changes the card's content and must move the fence.
//
// The negative half is the point of the test, not decoration: if an ordinary save started bumping,
// nothing would fail loudly — the operator would simply get a 409 on their card form every time
// they saved a layout, which is a bug report about the wrong feature.
func TestMarkerNormBumpsTechCardLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Norm Lock Style", Stage: entity.TechCardStageProto, StyleNumber: ns("NLK-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:         []int{szA},
		BomItems: []entity.TechCardBomItem{
			{LineKey: "NLK1", Section: entity.BomSectionFabric, Name: "Main Fabric"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	lockVersion := func() int {
		t.Helper()
		var v int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT lock_version FROM tech_card WHERE id = ?", tcID).Scan(&v))
		return v
	}
	marker := func(name string) entity.TechCardMarkerInsert {
		m := entity.TechCardMarkerInsert{
			Name: name, Source: entity.MarkerSourceAuto,
			FabricWidthCm: decimal.RequireFromString("140"),
			GapCm:         decimal.RequireFromString("0.5"),
			EdgeMarginCm:  decimal.RequireFromString("1"),
			UsedLengthCm:  decimal.RequireFromString("120"),
			PlacedCount:   1, TotalCount: 1,
			Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
			// Injected as the API layer injects it; unused here (no BOM link, so no direction rule).
			DistilStoredLayout: dto.MarkerLayoutFactsFromBlob,
		}
		markerSizing(&m, szA, 1)
		return m
	}

	// Creating a раскладка does not move the fence: a new one is not the norm by construction.
	before := lockVersion()
	normID, err := T.SaveMarker(ctx, tcID, 0, marker("раскладка А"), "tester")
	require.NoError(t, err)
	require.Equal(t, before, lockVersion(), "creating a раскладка must not bump the card lock")

	plainID, err := T.SaveMarker(ctx, tcID, 0, marker("раскладка Б"), "tester")
	require.NoError(t, err)
	require.Equal(t, before, lockVersion(), "creating a second раскладка must not bump the card lock")

	// Designating the norm moves it by exactly one.
	before = lockVersion()
	_, err = T.SetMarkerNorm(ctx, normID, true, "tester")
	require.NoError(t, err)
	require.Equal(t, before+1, lockVersion(), "designating the norm bumps the card lock by exactly 1")

	// Re-saving an ORDINARY раскладка still does not: this is the whole reason the blanket ban
	// existed, and Ф6.8 narrows the ban rather than lifting it.
	before = lockVersion()
	_, err = T.SaveMarker(ctx, tcID, plainID, marker("раскладка Б"), "tester")
	require.NoError(t, err)
	require.Equal(t, before, lockVersion(), "re-saving a NON-norm раскладка must not bump the card lock")

	// Re-shooting the раскладка that IS the norm changes the number the card promises.
	before = lockVersion()
	_, err = T.SaveMarker(ctx, tcID, normID, marker("раскладка А"), "tester")
	require.NoError(t, err)
	require.Equal(t, before+1, lockVersion(), "re-saving the NORM bumps the card lock by exactly 1")

	// CLEARING is not a no-op for the card: a card left without a norm is a different card, and a
	// form opened before the clear still promises one.
	before = lockVersion()
	_, err = T.SetMarkerNorm(ctx, normID, false, "tester")
	require.NoError(t, err)
	require.Equal(t, before+1, lockVersion(), "clearing the norm bumps the card lock by exactly 1")

	// And once it is no longer the norm, re-saving it goes back to being free.
	before = lockVersion()
	_, err = T.SaveMarker(ctx, tcID, normID, marker("раскладка А"), "tester")
	require.NoError(t, err)
	require.Equal(t, before, lockVersion(), "a demoted раскладка saves without bumping again")

	// ПОВТОРНОЕ СНЯТИЕ НЕ БАМПАЕТ. Снять норму с раскладки, которая нормой не была, — обычный повтор
	// (двойной клик, ретрай сети): карточка не меняется, и 409 в чужой открытой форме был бы платой
	// за то, что ничего не произошло.
	before = lockVersion()
	_, err = T.SetMarkerNorm(ctx, normID, false, "tester")
	require.NoError(t, err)
	require.Equal(t, before, lockVersion(), "clearing a norm that was not one changes nothing and must not bump")

	// УДАЛЕНИЕ НОРМЫ — ТОТ ЖЕ ПЕРЕХОД, ЧТО И СНЯТИЕ, и замок обязан двинуться так же. Иначе переход
	// «карточка осталась без нормы» тихо проходил бы через удаление, и форма, открытая до него,
	// сохранилась бы, обещая норму, которой уже нет.
	_, err = T.SetMarkerNorm(ctx, normID, true, "tester")
	require.NoError(t, err)
	before = lockVersion()
	require.NoError(t, T.DeleteMarker(ctx, normID))
	require.Equal(t, before+1, lockVersion(), "deleting the NORM bumps the card lock by exactly 1")

	// Удаление обычной раскладки — по-прежнему бесплатно.
	before = lockVersion()
	require.NoError(t, T.DeleteMarker(ctx, plainID))
	require.Equal(t, before, lockVersion(), "deleting an ORDINARY раскладка must not bump the card lock")
}

// TestColorwayRecipeReadPath is the H1 fix's contract test. Review finding H1: `techCardUsagesToPb`
// (dto/techcard.go) had zero call sites in the whole repo (confirmed by grep) — the recipe write-path
// (UpdateColorwayRecipe, restored above) was write-only. Because the write is a full-replace (DELETE
// all usages + re-INSERT), a UI that saves without ever being able to load the current recipe first
// would silently blank whatever it didn't resubmit. This proves BOTH restored read surfaces
// (01-DOMAIN-MODEL §2.3: recipe is colourway-owned, so GetColorwayByID is the minimum that must
// return it; the tech-card constructor view must show it too) round-trip a written recipe with
// matching field values, including the derived per-line money resolved against the style's own BOM.
func TestColorwayRecipeReadPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Recipe Read Style", Stage: entity.TechCardStageProto, StyleNumber: ns("RCPR-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "RRK1", Section: entity.TechCardBomSection("fabric"), Name: "Shell Fabric",
				UnitPrice: decimal.NewNullDecimal(decimal.RequireFromString("10.00")), Currency: ns("EUR")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("RCPR-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, err := res.LastInsertId()
	require.NoError(t, err)
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	_, err = T.UpdateColorwayRecipe(ctx, cwID, card.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: "RRK1", Placement: ns("outer"), Color: ns("black"), Pantone: ns("19-4005"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
	})
	require.NoError(t, err)

	// --- surface 1: GetTechCardById's constructor view (TechCard.colorways[].usages) ---
	card2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	pbTC := dto.ConvertEntityTechCardToPb(card2, dto.CostingFx{Base: "EUR"})
	var ref *pb_common.AdminColorwayRef
	for _, c := range pbTC.Colorways {
		if int(c.ColorwayId) == cwID {
			ref = c
		}
	}
	require.NotNil(t, ref, "GetTechCardById must list the colourway")
	require.Len(t, ref.Usages, 1, "GetTechCardById's colourway ref must carry the written recipe (H1)")
	u := ref.Usages[0]
	require.Equal(t, "outer", u.Placement)
	require.Equal(t, "black", u.Color)
	require.Equal(t, "19-4005", u.Pantone)
	require.Equal(t, "1.5", u.Consumption.GetValue())
	require.Greater(t, u.BomItemId, int64(0), "bom_line_key must resolve to a real bom_item_id")
	require.Equal(t, "15", u.LineTotal.GetValue(), "1.5 consumption x 10.00 unit_price, with costing:read")

	// --- surface 2: the dedicated recipe read (store side of GetColorwayByID) ---
	recipe, err := T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 1, "GetColorwayRecipe must return the written recipe (H1, GetColorwayByID's read side)")
	require.Equal(t, "outer", recipe[0].Placement.String)
	require.True(t, recipe[0].BomItemId.Valid)
	require.Equal(t, u.BomItemId, recipe[0].BomItemId.Int64, "both read surfaces resolve the same bom_item_id")

	// A colourway with no recipe reads back empty, not an error.
	res2, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'WHT', '#ffffff', 'US', ?, ?)`, fmt.Sprintf("RCPR-CW2-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID2_64, err := res2.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID2_64) })
	emptyRecipe, err := T.GetColorwayRecipe(ctx, int(cwID2_64))
	require.NoError(t, err)
	require.Empty(t, emptyRecipe)
}
