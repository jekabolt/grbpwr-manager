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

// ─────────────────────────────────────────────────────────────────────────────
// §4.1 №27 — the measured scopes' `stale` verdict does not travel.
// ─────────────────────────────────────────────────────────────────────────────

// TestArchiveDropsTheStalenessVerdict. `stale` is not a stored column: the server recomputes it on
// every read by comparing today's sheet fingerprint with the one the areas were measured under
// (entity.PieceAreaScope). Carrying it would put a READ-SIDE PROJECTION in the file — the same
// class as section_digests, cut by the same rule — and the value it carries is about the SOURCE
// instance's pattern files, which the receiver has neither got nor can check.
//
// The positive control is the whole test: `stale` is a bool, so «absent from the archive» and «the
// export never set it» look identical downstream. The fixture is therefore built STALE, shown stale
// before the builder runs, and required false after.
func TestArchiveDropsTheStalenessVerdict(t *testing.T) {
	card := tczMoneyCard()
	card.PieceAreaScopes = map[string]entity.PieceAreaScope{
		"B1": {
			ScopeKey: "B1",
			Stale:    true,
			Rows: []entity.PieceAreaRow{{
				PieceLineKey: "P1",
				SizeId:       sql.NullInt64{Int64: 3, Valid: true},
				AreaCm2:      tczDecimal("1400"),
				ContourLayer: "1",
				ParsedBy:     "im",
				ParsedAt:     tczAt(),
			}},
		},
	}

	raw := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
	require.Len(t, raw.GetPieceAreaScopes(), 1, "positive control: the scope reaches the wire at all")
	require.True(t, raw.GetPieceAreaScopes()[0].GetStale(),
		"positive control: the fixture must read STALE before the builder runs, or `false` afterwards "+
			"proves nothing about the builder")

	got := tczBuild(t, card)

	require.Len(t, got.GetPieceAreaScopes(), 1,
		"the SCOPE travels — it is the measurement, and §4.1 №27 writes it on the far side")
	sc := got.GetPieceAreaScopes()[0]
	require.False(t, sc.GetStale(), "the verdict over the measurement does not travel")
	require.Len(t, sc.GetAreas(), 1, "and nothing else on the scope goes with it")
	require.Equal(t, "im", sc.GetParsedBy(),
		"provenance is a fact about the MEASUREMENT and is stored as it stands (§4.1)")
	require.NotNil(t, sc.GetParsedAt())
}

// ─────────────────────────────────────────────────────────────────────────────
// THE RE-IMPORT PROBE — a card can be exportable and NOT importable.
//
// MEASURED, and the measurement is the point of these tests. The store is softer than the
// converter every API write passes through: AddTechCard takes an entity.TechCardInsert and writes
// it, while ConvertPbTechCardInsertToEntity — which the import stands behind (tcciPayload) —
// refuses shapes the store never looks at. Three of them are covered below, each with a positive
// control showing the CONVERTER refusing the very payload the archive was built from.
//
// Every case also requires the export to SUCCEED. A hole is not a failure: the archive still opens
// and is still worth reading. What the card gains is the sentence, said where somebody can act on
// it, instead of a field violation weeks later in another base.
// ─────────────────────────────────────────────────────────────────────────────

// tczAssembledCard is the clean baseline for the probe: three cut pieces and two machine steps that
// assemble them — front+back into a shell, then shell+cuff into the garment. It is the shape the
// violations below are made from, and on its own it must probe CLEAN.
func tczAssembledCard() *entity.TechCard {
	card := tczMoneyCard()
	// A REAL sheet key, unlike the shorthand tczMoneyCard uses. The probe only looks at pattern
	// rows when a bucket host is configured (see the url test below), and the converter's key rule
	// is 26 alphanumerics — a shorthand here would make that test fail for the key while it is
	// asking about the url.
	card.Patterns = []entity.TechCardSizePattern{{
		SizeId: 3, LineKey: tczSheetFront, URL: tczURL("patterns/front-v1.dxf"),
		Filename: tczNS("front-v1.dxf"), Version: 1, UploadedAt: tczNT(),
	}}
	card.Pieces = []entity.TechCardPiece{
		{LineKey: "P1", Name: "front"},
		{LineKey: "P2", Name: "back"},
		{LineKey: "P3", Name: "cuff"},
	}
	card.Operations = []entity.TechCardOperation{
		{
			OperationNumber: tczNI(10), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
			MachineType:   tczNS("lockstitch"),
			PieceLineKeys: []string{"P1", "P2"},
			InputKeys:     []string{"P1", "P2"},
			OutputUnitKey: tczNS("SHELL"), OutputUnitName: tczNS("shell"),
		},
		{
			OperationNumber: tczNI(20), OperationType: entity.OpTypeMachine, Zone: entity.ZoneOuter,
			MachineType: tczNS("lockstitch"),
			// The legacy projection carries ONLY the piece; the union carries the unit beside it.
			// That difference is what the aware-flag test below turns on.
			PieceLineKeys: []string{"P3"},
			InputKeys:     []string{"SHELL", "P3"},
			OutputUnitKey: tczNS("GARMENT"), OutputUnitName: tczNS("garment"),
		},
	}
	return card
}

// tczProbeHole runs the builder and returns the single hole it raised, failing on any other count.
// The archive itself must still have been built: a hole is not a refusal.
func tczProbeHole(t *testing.T, card *entity.TechCard) techcardarchive.ExportHole {
	t.Helper()
	blob, holes, err := buildArchiveCardJSON(card)
	require.NoError(t, err, "a card the import would refuse still EXPORTS — the file is readable and useful")
	require.NotEmpty(t, blob)
	require.Len(t, holes, 1, "exactly one hole, about the card as a whole: %+v", holes)
	return holes[0]
}

// tczConverterRefuses is the positive control every case below opens with: the payload the archive
// was built from really is one the import's converter rejects. Without it a hole proves only that
// the probe fired, not that it fired over a real defect.
func tczConverterRefuses(t *testing.T, card *entity.TechCard) string {
	t.Helper()
	insert := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	require.NotNil(t, insert)
	insert.AssemblyAware = true
	insert.MachineFieldsAware = true
	insert.MediaAware = true
	insert.OperationKindsAware = true
	insert.OperationWorkAware = true
	insert.BomQtyAware = true
	// The pattern rows go, exactly as the probe drops them when no bucket host is configured —
	// otherwise this control would fail on the url rule, which is the one rule the probe does not
	// ask about (the import overwrites every surviving row's url before it converts).
	insert.Patterns = nil

	_, err := dto.ConvertPbTechCardInsertToEntity(insert)
	require.Error(t, err, "positive control: the import's converter must refuse this card")
	return err.Error()
}

// Sheet keys of the probe fixtures: 26 alphanumerics, the shape the converter requires.
const (
	tczSheetFront = "01TCZPATTERNFRONT00000000A"
	tczSheetBack  = "01TCZPATTERNBACK000000000B"
)

func TestReimportProbeCatchesWhatTheStoreLetsThrough(t *testing.T) {
	t.Run("positive control: the assembled baseline probes clean", func(t *testing.T) {
		_, holes, err := buildArchiveCardJSON(tczAssembledCard())
		require.NoError(t, err)
		require.Empty(t, holes,
			"the baseline has to be importable, or every case below would pass on a card that was "+
				"already broken for some other reason: %+v", holes)
	})

	t.Run("a per-step material count on a MEASURED bom section", func(t *testing.T) {
		// The store writes this without a word — and says so in its own comment («СТОРОЖА
		// "СЧЁТНОЙ СЕКЦИИ" ЗДЕСЬ НЕТ … его проверил конвертер», store/techcard/production.go).
		// B1 is a fabric line, and fabric is measured: its norm lives on the colourway recipe, so
		// a count per step would be a third answer to one question.
		card := tczAssembledCard()
		require.False(t, entity.IsCountableSection(card.BomItems[0].Section),
			"positive control: B1 has to be a MEASURED section for this case to be the case")
		card.Operations[0].BomLineKeys = []string{"B1"}
		card.Operations[0].BomQuantities = []entity.OperationBomQty{{
			LineKey: "B1", QtyPerGarment: tczDecimal("6"),
		}}

		want := tczConverterRefuses(t, card)
		hole := tczProbeHole(t, card)

		require.Equal(t, techcardarchive.EntityCard, hole.Entity)
		require.Equal(t, techcardarchive.ReasonCardNotImportable, hole.Reason)
		require.Equal(t, "style_number=GRB-SS26-014", hole.Ref,
			"the ref names the card the way a person reads it, not by an id that means nothing elsewhere")
		require.Contains(t, hole.Detail, want,
			"the detail carries the converter's own words — it is the only thing that tells the "+
				"operator WHICH field to go and fix")
	})

	t.Run("an equipment profile key that is not a 26-character key", func(t *testing.T) {
		// CHAR(26) in the schema, and MySQL pads a shorter value rather than refusing it. The
		// 26-alphanumeric rule lives only in the converter (parseProfileKey → validatePatternLineKey).
		card := tczAssembledCard()
		card.Construction = &entity.TechCardConstruction{
			EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Machines: []entity.TechCardMachineProfile{{
					ProfileKey: "MACH-01", MachineType: "lockstitch",
				}},
			},
		}

		want := tczConverterRefuses(t, card)
		hole := tczProbeHole(t, card)

		require.Equal(t, techcardarchive.ReasonCardNotImportable, hole.Reason)
		require.Contains(t, hole.Detail, want)
	})

	t.Run("an assembly graph that breaks rule 3", func(t *testing.T) {
		// Rule 3: a join needs two distinct existing inputs — a unit made of one input is a
		// treatment, not a unit. entity.AssemblySweep runs in the converter and nowhere near the
		// store, so a graph like this is written and read back without complaint.
		card := tczAssembledCard()
		card.Operations[0].PieceLineKeys = []string{"P1"}
		card.Operations[0].InputKeys = []string{"P1"}

		want := tczConverterRefuses(t, card)
		hole := tczProbeHole(t, card)

		require.Equal(t, techcardarchive.ReasonCardNotImportable, hole.Reason)
		require.Contains(t, hole.Detail, want)
	})

	t.Run("a card with no style number is named by its id", func(t *testing.T) {
		card := tczAssembledCard()
		card.StyleNumber = sql.NullString{}
		card.Stage = entity.TechCardStageIdea
		card.Operations[0].InputKeys = []string{"P1"}
		card.Operations[0].PieceLineKeys = []string{"P1"}

		hole := tczProbeHole(t, card)
		require.Equal(t, "tech_card_id=214", hole.Ref)
	})
}

// TestReimportProbeRunsTheConverterTheImportWillRun is the mutation this design would otherwise
// die of quietly.
//
// The six *_aware flags are write-only transport flags: the read converter never emits them, and
// the import's resolver sets all six to true before converting. They are not decoration — with
// AssemblyAware false the graph is classified from the LEGACY piece-only projection, so a step
// joining a UNIT to a PIECE reads as having ONE input and breaks rule 3. A probe that forgot the
// flags would therefore report a perfectly importable card as un-importable, on every card that
// uses assembly units at all — the loudest possible false alarm.
//
// So: the baseline probes clean (asserted above and again here), and the SAME payload without the
// flags is refused. If the flags are ever dropped from the probe, the first half of this test goes
// red and names the reason.
func TestReimportProbeRunsTheConverterTheImportWillRun(t *testing.T) {
	card := tczAssembledCard()

	_, holes, err := buildArchiveCardJSON(card)
	require.NoError(t, err)
	require.Empty(t, holes, "the baseline is importable: %+v", holes)

	unaware := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	unaware.Patterns = nil
	_, err = dto.ConvertPbTechCardInsertToEntity(unaware)
	require.Error(t, err,
		"positive control: without the aware flags the very same payload is refused, which is what "+
			"makes setting them load-bearing rather than tidy")
	require.ErrorContains(t, err, "too-few-inputs",
		"and refused for the GRAPH reason specifically: read from the legacy piece-only projection, "+
			"the second step joins a single piece and breaks rule 3 — the exact false alarm the "+
			"flags prevent, not some unrelated field")
}

// TestReimportProbeDoesNotBlameThePatternURL is the scope boundary, and it is a boundary rather
// than an omission.
//
// The archive does not carry a pattern url — the sanitiser blanks it — and the import never
// converts the one it travelled with: tcflApplyPatternObjects overwrites every surviving row's url
// with the target's own re-uploaded object and drops the rows it could not place. A probe that
// asked the url rule would therefore produce the one thing worse than silence: a true-sounding
// «no import will take this card» whose stated cause the import does not even look at.
//
// The fixture's pattern url is on a host this instance does not manage, which the converter refuses
// outright (the positive control below). The export must still report NOTHING.
func TestReimportProbeDoesNotBlameThePatternURL(t *testing.T) {
	dto.SetManagedPatternHosts("cdn.this-instance.example")
	t.Cleanup(func() { dto.SetManagedPatternHosts() })

	card := tczAssembledCard()
	require.NotEmpty(t, card.Patterns, "positive control: the fixture must carry a sheet at all")

	insert := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	insert.AssemblyAware = true
	insert.MachineFieldsAware = true
	_, err := dto.ConvertPbTechCardInsertToEntity(insert)
	require.ErrorContains(t, err, "pattern url",
		"positive control: the card's own url is NOT a managed object here, so the converter "+
			"refuses it — which is exactly the false alarm the stand-in exists to prevent")

	_, holes, err := buildArchiveCardJSON(card)
	require.NoError(t, err)
	require.Empty(t, holes, "the export says nothing about a field it does not carry: %+v", holes)

	// ONE STAND-IN PER ROW, and this is what that buys. The parser reads the url a second time —
	// two keyed rows sharing a (size, url) pair are refused as one sheet hung twice — so a single
	// stand-in shared by every row would invent that collision on any card with two sheets in one
	// size. Two distinct sheets on one size must probe clean.
	two := tczAssembledCard()
	two.Patterns = append(two.Patterns, entity.TechCardSizePattern{
		SizeId: 3, LineKey: tczSheetBack, URL: tczURL("patterns/back-v1.dxf"), Version: 1,
	})
	_, holes, err = buildArchiveCardJSON(two)
	require.NoError(t, err)
	require.Empty(t, holes,
		"two sheets filed under one size are ordinary; a shared stand-in would report them as the "+
			"same sheet hung twice: %+v", holes)

	// And the stand-in must not paper over the OTHER pattern rules: a duplicate line_key is still
	// caught, because the url is the ONLY field that leaves the probe's scope.
	dup := tczAssembledCard()
	dup.Patterns = append(dup.Patterns, entity.TechCardSizePattern{
		SizeId: 4, LineKey: tczSheetFront, URL: tczURL("patterns/back-v1.dxf"), Version: 1,
	})
	hole := tczProbeHole(t, dup)
	require.Equal(t, techcardarchive.ReasonCardNotImportable, hole.Reason)
	require.Contains(t, hole.Detail, "line_key",
		"the url leaves the probe's scope; every other rule of the pattern row stays in it")
}
