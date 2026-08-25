package admin

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ─────────────────────────────────────────────────────────────────────────────
// card.json builder (Ф1.2).
//
// Helpers here are prefixed tcz* — the admin test package already owns `dec`, and the
// techcard-analysis tests already own the tca* prefix.
//
// Nothing in this file greps the JSON. protojson's output is not byte-stable (protobuf-go's
// detrand deliberately jitters the whitespace), and a substring check for `"unit_price"` would
// also miss a price that survived under a name the writer never spelled. Every assertion parses
// the blob back into pb_common.TechCard and walks it with protoreflect.
// ─────────────────────────────────────────────────────────────────────────────

func tczNS(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func tczNI(n int32) sql.NullInt32   { return sql.NullInt32{Int32: n, Valid: true} }
func tczND(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}
func tczAt() time.Time                    { return time.Unix(1755000000, 0).UTC() }
func tczNT() sql.NullTime                 { return sql.NullTime{Time: tczAt(), Valid: true} }
func tczURL(s string) string              { return "https://cdn.source-instance.example/" + s }
func tczDecimal(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// tczMoneyCard is a card with EVERY money carrier of the tech-card contract filled in, plus every
// instance fact the scrub is supposed to remove. A fixture that only half-fills these would let
// the builder pass by doing nothing, which is why TestBuildArchiveCard opens with a positive
// control over this same card before it looks at the archive.
func tczMoneyCard() *entity.TechCard {
	card := &entity.TechCard{Id: 214, LockVersion: 37}
	card.StyleNumber = tczNS("GRB-SS26-014")
	card.Name = "Field jacket"
	card.CreatedAt = tczAt()
	card.UpdatedAt = tczAt()
	card.CreatedBy = "im"
	card.UpdatedBy = "im"
	card.SizeIds = []int{3, 4}
	card.SizeQuantities = []entity.TechCardSizeQuantity{{SizeId: 3, OrderQty: 10}, {SizeId: 4, OrderQty: 20}}

	// Money carrier 1 — the costing block.
	card.Costing = &entity.TechCardCosting{
		CmtCost:         tczND("12.50"),
		LogisticsCost:   tczND("3.00"),
		OverheadCost:    tczND("4.00"),
		DefectPercent:   tczND("5"),
		Currency:        tczNS("EUR"),
		Notes:           tczNS("quote from the March run"),
		TargetMarginPct: tczND("62"),
	}

	// Money carrier 2 — the BOM line's purchase price and its provenance.
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, LineKey: "B1", Name: "Shell 240 gsm", Section: entity.BomSectionFabric,
		Unit:            tczNS("m"),
		UnitPrice:       tczND("60.00"),
		Currency:        tczNS("PLN"),
		PriceSource:     tczNS("production_run"),
		PriceSnapshotAt: tczNT(),
		// Structure that must SURVIVE, sitting on the same row as the money above.
		WastagePercent: tczND("3"),
		QtyPerGarment:  tczND("1.4"),
		FabricWidth:    tczND("150"),
	}}

	// Money carrier 3 — the colourway's own COGS, its provenance and its retail price list.
	card.Colorways = []entity.TechCardColorway{{
		Id: 812, ColorCode: "BLK", Name: "Black", BaseSku: tczNS("GRB-SS26-014-BLK"),
		CostPrice:          tczND("41.20"),
		CostPriceSource:    tczNS("tech_card"),
		CostPriceUpdatedAt: tczNT(),
		Prices:             []entity.ColorwayPrice{{Currency: "EUR", Price: tczDecimal("180")}},
		Usages: []entity.TechCardColorwayUsage{{
			Id: 1, BomLineKey: "B1", BomItemIndex: tczNI(0),
			Consumption: tczND("1.4"), Color: tczNS("black"),
		}},
	}}

	// SAM's ingredients: per-operation minutes, which live OUTSIDE the costing block.
	card.Operations = []entity.TechCardOperation{{
		OperationNumber: tczNI(10),
		OperationType:   entity.OpTypeMachine,
		Zone:            entity.ZoneOuter,
		MachineType:     tczNS("lockstitch"),
		SMV:             tczND("4.5"),
	}}

	// Instance facts. Patterns carry the source's object url; the tokenised view/download pair is
	// filled by the handler layer, not by the converter — so it is
	// TestSanitizeCardForArchiveBlanksDecoratedURLs below, not this fixture, that covers those two.
	card.Patterns = []entity.TechCardSizePattern{{
		SizeId: 3, LineKey: "P1", URL: tczURL("patterns/front-v1.dxf"),
		Filename: tczNS("front-v1.dxf"), Version: 1, UploadedAt: tczNT(),
	}}
	card.Signoffs = []entity.TechCardSignoff{{
		Section: entity.SignoffConstruction, State: entity.SignoffStateApproved,
		SignedBy: tczNS("im"), SignedAt: tczNT(), SignedDigest: tczNS("2f8a…"),
	}}
	card.RoleAssignments = []entity.TechCardRoleAssignment{{
		Id: 1, TechCardId: 214, Role: entity.RoleConstructor,
		AdminId: 9, AdminUsername: "im", AssignedBy: "im", AssignedAt: tczAt(),
	}}
	// The fit model: a row in the SOURCE's model table, the same class as the role assignment
	// above and carried by the same reasoning (FORMAT.md §4).
	card.BaseModelId = tczNI(91)
	// The output colour variants of an auxiliary card. Not money — which is exactly why neither
	// money layer can see them — but `on_hand` is the source warehouse's CURRENT BALANCE, and
	// material_id/id/tech_card_id are three rows of the source base. Filled here so the claim
	// "output_variants do not travel" has something to be a claim ABOUT.
	card.OutputVariants = []entity.TechCardOutputVariant{{
		TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{
			Id: 55, ColorCode: "BLK", MaterialId: 8301, Active: true,
		},
		TechCardId: 214, ColorName: "Black", MaterialName: "Dust bag / black",
		Unit: "pcs", OnHand: tczND("820"),
	}}
	card.ResolvedMedia = []entity.TechCardMediaFull{{
		Media: entity.MediaFull{Id: 4021, CreatedAt: tczAt(), MediaItem: entity.MediaItem{
			FullSizeMediaURL:   tczURL("2026/08/1a2b.jpg"),
			FullSizeWidth:      1200,
			FullSizeHeight:     1600,
			ThumbnailMediaURL:  tczURL("2026/08/1a2b-thumb.jpg"),
			ThumbnailWidth:     300,
			ThumbnailHeight:    400,
			CompressedMediaURL: tczURL("2026/08/1a2b-c.jpg"),
			CompressedWidth:    600,
			CompressedHeight:   800,
			BlurHash:           tczNS("LKO2?U%2Tw=w"),
		}},
		Category: entity.TechCardMediaCategoryTechnical,
		Kind:     entity.TechCardMediaFront,
		Caption:  tczNS("front"),
	}}
	card.ResolvedOperationMedia = []entity.TechCardMediaFull{{
		Media: entity.MediaFull{Id: 4022, CreatedAt: tczAt(), MediaItem: entity.MediaItem{
			FullSizeMediaURL: tczURL("2026/08/9c3d.jpg"), FullSizeWidth: 800, FullSizeHeight: 800,
		}},
		Kind: entity.TechCardMediaDetail,
	}}
	return card
}

// tczMoneyHits walks a parsed message and returns the PATH of every set field whose name is in
// MoneyFieldNamesArchive. protoreflect.Range never enters an unset field, so a hit is by
// construction a field carrying a value — which is exactly the claim under test.
//
// A matched field is reported and NOT descended into, mirroring RedactFieldsDeep: the money
// under a block that survived is the same one leak, not a dozen.
func tczMoneyHits(m protoreflect.Message, path string) []string {
	var hits []string
	if m == nil || !m.IsValid() {
		return nil
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		p := path + "." + name
		if techcardarchive.MoneyFieldNamesArchive[name] {
			hits = append(hits, p)
			return true
		}
		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() != protoreflect.MessageKind {
				return true
			}
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				hits = append(hits, tczMoneyHits(mv.Message(), fmt.Sprintf("%s[%s]", p, k.String()))...)
				return true
			})
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				hits = append(hits, tczMoneyHits(l.Get(i).Message(), fmt.Sprintf("%s[%d]", p, i))...)
			}
		case fd.Kind() == protoreflect.MessageKind:
			hits = append(hits, tczMoneyHits(v.Message(), p)...)
		}
		return true
	})
	sort.Strings(hits)
	return hits
}

// tczBuild runs the builder and parses card.json back STRICTLY — DiscardUnknown stays false here
// on purpose. The import reads with DiscardUnknown:true (FORMAT.md §3) because it must tolerate a
// newer MINOR; a test of our OWN writer has no such excuse, and a field name that stopped
// resolving should surface as a parse failure rather than be quietly dropped on the way in.
func tczBuild(t *testing.T, card *entity.TechCard) *pb_common.TechCard {
	t.Helper()
	blob, holes, err := buildArchiveCardJSON(card)
	require.NoError(t, err)
	require.NotEmpty(t, blob)
	require.Empty(t, holes,
		"the builder reports no hole for a complete card: everything it removes is removed by the "+
			"FORMAT (§4) and is not a hole. A hole appearing here means a new case was added and this "+
			"expectation has to be restated, not deleted")

	var got pb_common.TechCard
	require.NoError(t, protojson.UnmarshalOptions{}.Unmarshal(blob, &got),
		"card.json must parse back under strict protojson")
	return &got
}

func TestBuildArchiveCard(t *testing.T) {
	// ── Positive control ─────────────────────────────────────────────────────────────────────
	// Everything below claims something is ABSENT from the archive. Absence proves nothing until
	// the same thing is shown PRESENT in what the builder was handed.
	t.Run("positive control: the fixture really carries money and instance facts", func(t *testing.T) {
		raw := dto.ConvertEntityTechCardToPb(tczMoneyCard(), dto.CostingFx{})
		require.NotNil(t, raw)

		hits := tczMoneyHits(raw.ProtoReflect(), "TechCard")
		require.NotEmpty(t, hits, "the fixture must carry money before the builder removes it")
		require.NotNil(t, raw.GetTechCard().GetCosting(), "costing block")
		require.NotNil(t, raw.GetTechCard().GetBomItems()[0].GetUnitPrice(), "bom unit_price")
		require.NotEmpty(t, raw.GetTechCard().GetBomItems()[0].GetPriceSource(), "bom price_source")
		require.NotNil(t, raw.GetTechCard().GetBomItems()[0].GetPriceSnapshotAt(), "bom price_snapshot_at")
		require.NotEmpty(t, raw.GetColorways(), "colourway refs")
		require.NotNil(t, raw.GetColorways()[0].GetCostPrice(), "colourway cost_price")
		require.NotEmpty(t, raw.GetTechCard().GetSignoffs(), "signoffs")
		require.NotEmpty(t, raw.GetRoleAssignments(), "role assignments")
		require.NotZero(t, raw.GetTechCard().GetBaseModelId(), "fit model")
		require.NotEmpty(t, raw.GetOutputVariants(), "output colour variants")
		require.NotNil(t, raw.GetOutputVariants()[0].GetOnHand(), "the variant's warehouse balance")
		require.NotEmpty(t, raw.GetTechCard().GetPatterns()[0].GetUrl(), "pattern object url")
		require.NotEmpty(t, raw.GetSectionDigests(), "section digests")
		require.NotEmpty(t,
			raw.GetResolvedTechnicalMedia()[0].GetMedia().GetMedia().GetFullSize().GetMediaUrl(),
			"resolved media url")
	})

	// ── The zero CostingFx decision, pinned as a fact ────────────────────────────────────────
	t.Run("the converter survives a zero CostingFx", func(t *testing.T) {
		// The builder passes dto.CostingFx{} rather than s.costingFx(ctx). This is the fact that
		// makes that safe: no panic, the costing block still renders, and only the *_base rollup
		// (the one thing FX buys) is omitted. The block is deleted three lines later either way,
		// so the export costs the caller no FX table read.
		zero := dto.ConvertEntityTechCardToPb(tczMoneyCard(), dto.CostingFx{})
		require.NotNil(t, zero.GetTechCard().GetCosting())
		require.Nil(t, zero.GetTechCard().GetCosting().GetUnitCostBase(), "no base rollup without rates")
		require.Empty(t, zero.GetTechCard().GetCosting().GetBaseCurrency(), "no base currency without rates")
		require.NotNil(t, zero.GetTechCard().GetCosting().GetUnitCost(), "the costing itself still computes")
	})

	// ── The claim the whole feature rests on ─────────────────────────────────────────────────
	t.Run("no money field survives with a value", func(t *testing.T) {
		got := tczBuild(t, tczMoneyCard())
		hits := tczMoneyHits(got.ProtoReflect(), "TechCard")
		require.Empty(t, hits,
			"these money fields left the building inside card.json: %v.\n"+
				"An archive goes to an outside factory. Fix the builder — never the denylist", hits)
	})

	t.Run("each money layer is sufficient on its own", func(t *testing.T) {
		// MEASURED, and it is why the obvious mutation of this file comes back GREEN: commenting
		// out stripTechCardCosting alone leaves card.json money-free, and so does commenting out
		// RedactFieldsDeep alone. Only removing BOTH turns "no money field survives" red.
		//
		// The subtest above therefore cannot tell a working layer from a redundant one — it sees
		// the pipeline, not the layers. This one takes each half on its own so that a layer which
		// quietly stops working is still caught by something, and so that the redundancy is a
		// recorded fact rather than a surprise for whoever writes the adversarial gate next.
		card := tczMoneyCard()

		byName := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
		stripTechCardCosting(byName)
		sanitizeCardForArchive(byName)
		require.Empty(t, tczMoneyHits(byName.ProtoReflect(), "TechCard"),
			"layer 1 (the API's own by-name cut) must clear this card unaided")

		recursive := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
		sanitizeCardForArchive(recursive)
		techcardarchive.RedactFieldsDeep(recursive.ProtoReflect(), techcardarchive.MoneyFieldNamesArchive)
		require.Empty(t, tczMoneyHits(recursive.ProtoReflect(), "TechCard"),
			"layer 3 (the recursive net) must clear this card unaided")
	})

	t.Run("the costing block is absent, not empty", func(t *testing.T) {
		// The recursive layer cannot tell "must be nil" from "is an empty message": Range never
		// visits an unset field, so a present-but-blank TechCardCosting would score zero hits
		// above while still announcing that this card HAS a costing block. Checked by identity.
		got := tczBuild(t, tczMoneyCard())
		require.Nil(t, got.GetTechCard().GetCosting())
	})

	t.Run("total_sam leaves with the block and smv stays behind", func(t *testing.T) {
		// EXPECTED, not a defect. total_sam is minutes, not money, but it lives inside
		// TechCardCosting and MoneyFieldNamesArchive lists `costing` — the block, not its leaves —
		// so that a money field added inside the block later cannot leak. Cutting the block whole
		// is what makes that unexpressible; total_sam is the price of it.
		//
		// The sum is rebuildable on the far side because its ingredients travel: per-operation smv
		// is outside the block. That is what makes the loss acceptable rather than merely known,
		// and it is the half that would silently stop being true, so it is asserted here.
		card := tczMoneyCard()
		raw := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
		require.NotNil(t, raw.GetTechCard().GetCosting().GetTotalSam(),
			"positive control: the fixture's operations do produce a total_sam")

		got := tczBuild(t, card)
		require.Nil(t, got.GetTechCard().GetCosting(), "…and it leaves with the block")
		require.NotNil(t, got.GetTechCard().GetOperations()[0].GetSmv(),
			"the minutes total_sam summed must survive, or the far side cannot rebuild it")
		require.Equal(t, "4.5", got.GetTechCard().GetOperations()[0].GetSmv().GetValue())
	})

	// ── The exporting instance's own facts ───────────────────────────────────────────────────
	t.Run("instance facts are gone", func(t *testing.T) {
		got := tczBuild(t, tczMoneyCard())

		require.Nil(t, got.GetTechCard().GetSignoffs(), "an archived card must never look signed")
		require.Nil(t, got.GetRoleAssignments(), "role assignments name accounts in the source's admins table")
		require.Zero(t, got.GetTechCard().GetBaseModelId(),
			"base_model_id is a row in the source's model table and no model dictionary travels: a "+
				"number left in card.json is one a foreign reader cannot know is nobody's (§4). The "+
				"import clears it too, but that is the defence against a HAND-MADE archive — our own "+
				"exports must not produce one")
		require.Nil(t, got.GetColorways(), "colourways travel in colorways.json, not as product refs")
		require.Nil(t, got.GetOutputVariants(),
			"output variants are warehouse buckets: on_hand is the SOURCE'S STOCK BALANCE — no money "+
				"layer can see it and no id rule covers it — and material_id names a catalogue row "+
				"with no passport beside it. What the card produces travels as output_material_id "+
				"plus its passport in materials/index.json (§4)")

		p := got.GetTechCard().GetPatterns()[0]
		require.Empty(t, p.GetViewUrl())
		require.Empty(t, p.GetDownloadUrl())
		require.Empty(t, p.GetUrl(),
			"the pattern's own object url is the source instance's key: a foreign host fails the "+
				"whole import, and a matching one (beta and prod on one CDN) silently writes a live "+
				"link to an object nobody moved")

		require.Empty(t, got.GetSectionDigests(),
			"section digests are derived, and the costing section's was fingerprinted BEFORE the "+
				"money was cut")

		for i, m := range got.GetResolvedTechnicalMedia() {
			mi := m.GetMedia().GetMedia()
			require.Empty(t, mi.GetFullSize().GetMediaUrl(), "resolved_technical_media[%d] full size", i)
			require.Empty(t, mi.GetThumbnail().GetMediaUrl(), "resolved_technical_media[%d] thumbnail", i)
			require.Empty(t, mi.GetCompressed().GetMediaUrl(), "resolved_technical_media[%d] compressed", i)
		}
		require.Empty(t,
			got.GetResolvedOperationMedia()[0].GetMedia().GetMedia().GetFullSize().GetMediaUrl(),
			"operation media are the third resolved list and are forgotten the most easily")

		b := got.GetTechCard().GetBomItems()[0]
		require.Empty(t, b.GetPriceSource(), "the source of a price whose figure is gone is still a leak")
		require.Nil(t, b.GetPriceSnapshotAt(), "…and so is its date")
	})

	t.Run("what the owner decided travels, travels", func(t *testing.T) {
		// B-2 / B-3, FORMAT.md §4: created_by / updated_by and the revision journal are NOT
		// instance secrets and are not scrubbed. Pinned so a later "tidy up the provenance" pass
		// has to argue with a test instead of a habit.
		got := tczBuild(t, tczMoneyCard())
		require.Equal(t, "im", got.GetCreatedBy())
		require.Equal(t, "im", got.GetUpdatedBy())
	})

	// ── The other half of "structure stays, money goes" ──────────────────────────────────────
	t.Run("structure stays", func(t *testing.T) {
		// Percentages, consumption and widths are metres and geometry, not money. A builder that
		// passed the money test by emptying the card would fail here.
		got := tczBuild(t, tczMoneyCard())
		b := got.GetTechCard().GetBomItems()[0]
		require.Equal(t, "B1", b.GetLineKey())
		require.NotNil(t, b.GetWastagePercent(), "wastage is a percentage of fabric, not a price")
		require.NotNil(t, b.GetQtyPerGarment(), "consumption is metres per garment")
		require.NotNil(t, b.GetFabricWidth(), "roll width is geometry")
		require.NotEmpty(t, got.GetTechCard().GetPatterns()[0].GetLineKey(), "the sheet's identity")
		require.Len(t, got.GetTechCard().GetSizeIds(), 2, "the size range")
		require.Equal(t, int32(4021),
			got.GetResolvedTechnicalMedia()[0].GetMedia().GetId(),
			"the media id stays — media/index.json is keyed by it and the import remaps it")
		require.Equal(t, int32(1200),
			got.GetResolvedTechnicalMedia()[0].GetMedia().GetMedia().GetFullSize().GetWidth(),
			"dimensions describe the picture, not where this instance keeps it")
	})

	t.Run("a nil card is an error, not an empty archive", func(t *testing.T) {
		blob, holes, err := buildArchiveCardJSON(nil)
		require.Error(t, err)
		require.Nil(t, blob)
		require.Nil(t, holes)
	})
}

// TestSanitizeCardForArchiveBlanksDecoratedURLs covers the half of the scrub that the entity path
// cannot reach.
//
// dto.ConvertEntityTechCardToPb never fills a pattern's view_url / download_url — they are minted
// by patternaccess.Service, which decorates a response AFTER conversion. So the assertions in
// TestBuildArchiveCard that those two are empty would pass over a scrub that did nothing at all.
// This test hands sanitizeCardForArchive an already-decorated message and requires the blanking to
// actually fire, which is what keeps the guarantee a property of the FORMAT rather than of the one
// call path that happens not to decorate.
func TestSanitizeCardForArchiveBlanksDecoratedURLs(t *testing.T) {
	pb := &pb_common.TechCard{
		TechCard: &pb_common.TechCardInsert{
			Patterns: []*pb_common.TechCardSizePattern{{
				LineKey:     "P1",
				Url:         tczURL("patterns/front-v1.dxf"),
				ViewUrl:     "https://backend.source-instance.example/api/p/eyJhbGciOi",
				DownloadUrl: "https://backend.source-instance.example/api/p/eyJhbGciOi?dl=1",
			}},
		},
	}
	require.NotEmpty(t, pb.GetTechCard().GetPatterns()[0].GetViewUrl(), "positive control")

	sanitizeCardForArchive(pb)

	require.Empty(t, pb.GetTechCard().GetPatterns()[0].GetViewUrl())
	require.Empty(t, pb.GetTechCard().GetPatterns()[0].GetDownloadUrl())
	require.NotEmpty(t, pb.GetTechCard().GetPatterns()[0].GetLineKey(),
		"line_key is the sheet's identity across the trip and must not be caught up in the blanking")
}
