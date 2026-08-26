package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф1.4 — THE ADVERSARIAL MONEY GATE.
//
// This file is not a unit test of the builder. It is the last thing that stands between GRBPWR's
// cost base and an outside factory: it builds a FINISHED archive through the whole export —
// buildArchiveCardJSON (Ф1.2) + collectArchiveSidecars (Ф1.3) + techcardarchive.WriteArchive (Ф1.5)
// — opens it the way the receiving side opens it (techcardarchive.OpenArchive, the trust boundary
// itself), and then hunts for money in every file the ZIP carries.
//
// Three properties make it a gate rather than a restatement of TestBuildArchiveCard:
//
//  1. IT LOOKS AT THE WHOLE ARCHIVE, NOT AT card.json. A material passport with a price or a
//     colourway sidecar with a COGS figure leaks exactly as much as a costing block does, and
//     neither is reachable through pb_common.TechCard. Sidecars are scanned by KEY NAME over the
//     parsed JSON, so a money field ADDED to techcardarchive.MaterialPassport or ColorwayPayload
//     later is caught by this file the day it is written — the Go type cannot express it today,
//     which is precisely why nobody would think to write a test for it.
//  2. IT LOOKS FOR VALUES, NOT ONLY FOR NAMES. Every money figure in the fixture is a canary with
//     an unmistakable digit signature (8180000.0x, currency XTS), and the gate requires those
//     strings to be absent from every text entry of the ZIP. A name denylist cannot catch money
//     that reappears under a name nobody listed; a canary can.
//  3. IT IS ADVERSARIAL ABOUT ITSELF. Every "the archive does not contain X" claim is preceded by
//     a positive control proving X was there before the export, and both scanners are run against
//     a synthetic money-bearing payload to prove they can go red at all. An absence assertion over
//     an empty archive proves nothing, and a scanner that never matches is a green light wired to
//     nothing.
//
// MEASURED AND LOAD-BEARING (Ф1.2, restated here because it is the first thing a reader of this
// file will disbelieve): layers 1 and 3 of the money removal — stripTechCardCosting and
// RedactFieldsDeep over MoneyFieldNamesArchive — overlap COMPLETELY on today's contract. Removing
// ONE of them leaves this gate green, and that is the point of having two, not a defect in this
// test. The end-to-end mutation for this file therefore removes BOTH (verified: with both calls
// commented out the gate fails on tech_card.costing, tech_card.bom_items[0].unit_price and
// tech_card.bom_items[0].currency). Each layer ON ITS OWN is measured by the "each money layer is
// sufficient on its own" subtest in techcard_archive_card_test.go — a pipeline test sees the
// pipeline, not the layers, and must not be weakened to pretend otherwise.
//
// Names: the admin test package already owns `dec` (costing_rbac_test.go), `tcz*` (Ф1.2) and
// `tca*`; everything here is prefixed amg*.
// ─────────────────────────────────────────────────────────────────────────────

// Canary money values. Each is a figure no consumption, width, percentage or count would ever
// produce, so a substring hit anywhere in the archive is a leak and never a coincidence. XTS is the
// ISO 4217 code reserved for testing — legal to write, meaningless to a factory, and impossible to
// confuse with a currency the business actually uses.
const (
	amgBomUnitPrice     = "8180000.01"
	amgCostingCmt       = "8180000.02"
	amgCostingLogistics = "8180000.03"
	amgColorwayCost     = "8180000.04"
	amgColorwayRetail   = "8180000.05"
	amgMaterialPrice    = "8180000.06"
	amgCurrency         = "XTS"
)

func amgCanaries() []string {
	return []string{
		amgBomUnitPrice, amgCostingCmt, amgCostingLogistics,
		amgColorwayCost, amgColorwayRetail, amgMaterialPrice, amgCurrency,
	}
}

func amgNS(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func amgND(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}
func amgAt() time.Time    { return time.Unix(1755000000, 0).UTC() }
func amgNT() sql.NullTime { return sql.NullTime{Time: amgAt(), Valid: true} }

// ─────────────────────────────────────────────────────────────────────────────
// The fixture
// ─────────────────────────────────────────────────────────────────────────────

// amgMoneyCard is a card carrying EVERY money vehicle the tech-card contract has, wired to every
// sidecar the archive writes: a costing block, a priced BOM line with its provenance, a colourway
// with a COGS and a retail price list, a recipe whose totals derive from that price, a material
// whose catalogue row has a latest price, plus media / patterns / markers so the archive under test
// is a full one and not a card.json with decorations.
func amgMoneyCard() *entity.TechCard {
	card := &entity.TechCard{Id: 214, LockVersion: 37}
	card.StyleNumber = amgNS("GRB-SS26-014")
	card.Name = "Field jacket"
	card.CreatedAt = amgAt()
	card.UpdatedAt = amgAt()
	card.CreatedBy = "im"
	card.UpdatedBy = "im"
	card.SizeIds = []int{3, 4}
	card.SizeQuantities = []entity.TechCardSizeQuantity{{SizeId: 3, OrderQty: 10}, {SizeId: 4, OrderQty: 20}}

	// Money vehicle 1 — the style-level costing block.
	card.Costing = &entity.TechCardCosting{
		CmtCost:         amgND(amgCostingCmt),
		LogisticsCost:   amgND(amgCostingLogistics),
		DefectPercent:   amgND("5"),
		Currency:        amgNS(amgCurrency),
		Notes:           amgNS("quote from the March run"),
		TargetMarginPct: amgND("62"),
	}

	// Money vehicle 2 — the BOM line's purchase price, its currency and its provenance. The
	// structural neighbours (wastage, consumption, roll width) are filled too: an export that
	// passed this gate by emptying the card must fail somewhere, and that somewhere is the
	// "structure survives" subtest.
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, LineKey: "BOM-SHELL", Name: "Shell 240 gsm", Section: entity.BomSectionFabric,
		MaterialId:      sql.NullInt64{Int64: 8120, Valid: true},
		Unit:            amgNS("m"),
		UnitPrice:       amgND(amgBomUnitPrice),
		Currency:        amgNS(amgCurrency),
		PriceSource:     amgNS("production_run"),
		PriceSnapshotAt: amgNT(),
		WastagePercent:  amgND("3"),
		QtyPerGarment:   amgND("1.4"),
		FabricWidth:     amgND("150"),
	}}

	card.Pieces = []entity.TechCardPiece{{
		Id: 900, LineKey: "PIECE-FRONT", Name: "полочка",
		Materials: []entity.TechCardPieceMaterial{{
			ColorwayID: 812, BomLineKey: "BOM-SHELL", Note: amgNS("долевая"),
		}},
	}}

	// Money vehicle 3 — the colourway's COGS with its provenance, its retail price list, and a
	// recipe row whose line_total / size_run_total the converter derives from the BOM price above.
	card.Colorways = []entity.TechCardColorway{{
		Id: 812, ColorCode: "BLK", Name: "Black", BaseSku: amgNS("GRB-SS26-014-BLK"),
		CostPrice:          amgND(amgColorwayCost),
		CostPriceSource:    amgNS("tech_card"),
		CostPriceUpdatedAt: amgNT(),
		Prices:             []entity.ColorwayPrice{{Currency: amgCurrency, Price: decimal.RequireFromString(amgColorwayRetail)}},
		Usages: []entity.TechCardColorwayUsage{{
			Id: 1, BomItemId: sql.NullInt64{Int64: 501, Valid: true}, BomLineKey: "BOM-SHELL",
			MaterialId:        sql.NullInt64{Int64: 8120, Valid: true},
			Placement:         amgNS("outer"),
			Color:             amgNS("black"),
			Consumption:       amgND("1.42"),
			ConsumptionSource: amgNS("marker"),
			WasteCutPct:       amgND("12.4"),
			NormMarkerId:      sql.NullInt64{Int64: 77, Valid: true},
			SizeConsumptions: []entity.TechCardBomSizeConsumption{
				{SizeId: 3, Consumption: decimal.RequireFromString("1.38")},
				{SizeId: 4, Consumption: decimal.RequireFromString("1.42")},
			},
		}},
	}}

	card.Operations = []entity.TechCardOperation{{
		OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
		OperationType:   entity.OpTypeMachine,
		Zone:            entity.ZoneOuter,
		MachineType:     amgNS("lockstitch"),
		SMV:             amgND("4.5"),
	}}

	card.Media = []entity.TechCardMediaItem{{
		MediaId: 4020, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
		Caption: amgNS("front flat"),
	}}
	card.ResolvedMedia = []entity.TechCardMediaFull{{
		Media: entity.MediaFull{Id: 4020, CreatedAt: amgAt(), MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/a.jpg",
			FullSizeWidth:    2400, FullSizeHeight: 3200,
		}},
		Category: entity.TechCardMediaCategoryTechnical,
		Kind:     entity.TechCardMediaFront,
		Caption:  amgNS("front flat"),
	}}
	card.Patterns = []entity.TechCardSizePattern{{
		LineKey: "PAT-FRONT", SizeId: 4, Version: 3,
		URL:           "https://cdn.grbpwr.com/tech-card-patterns/front_v3.dxf",
		Filename:      amgNS("front_v3.dxf"),
		Name:          amgNS("перед"),
		FabricPurpose: amgNS(string(entity.BomPurposeMain)),
		UploadedAt:    amgNT(),
	}}
	card.Markers = []entity.TechCardMarkerSummary{{
		Id: 77, TechCardId: 214, Name: "shell 150 cm",
		SizeId:       sql.NullInt64{Int64: 4, Valid: true},
		Sets:         sql.NullInt64{Int64: 2, Valid: true},
		BomItemId:    sql.NullInt64{Int64: 501, Valid: true},
		BomLineKey:   amgNS("BOM-SHELL"),
		UsedLengthCm: decimal.RequireFromString("300"),
	}}

	// Instance facts, so the archive under test is the real shape and not a stripped-down one.
	card.Signoffs = []entity.TechCardSignoff{{
		Section: entity.SignoffConstruction, State: entity.SignoffStateApproved,
		SignedBy: amgNS("im"), SignedAt: amgNT(), SignedDigest: amgNS("2f8a…"),
	}}
	card.RoleAssignments = []entity.TechCardRoleAssignment{{
		Id: 1, TechCardId: 214, Role: entity.RoleConstructor,
		AdminId: 9, AdminUsername: "im", AssignedBy: "im", AssignedAt: amgAt(),
	}}
	return card
}

// amgServer wires the repository the sidecar collector reads through. The material catalogue row
// carries a latest price ON PURPOSE — a passport has nowhere to put it, and "nowhere to put it" is
// a claim this gate is here to verify against the archive rather than against the struct.
func amgServer(t *testing.T) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	cache := mocks.NewMockCache(t)
	media := mocks.NewMockMedia(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().TechCards().Return(cards)
	repo.EXPECT().Cache().Return(cache)
	repo.EXPECT().Media().Return(media)

	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(&entity.DictionaryInfo{
		Sizes: []entity.Size{{Id: 3, Name: "s"}, {Id: 4, Name: "m"}, {Id: 5, Name: "l"}},
		Measurements: []entity.MeasurementName{
			{Id: 11, Name: "chest"}, {Id: 12, Name: "length"},
		},
	}, nil)

	cards.EXPECT().GetStyleSizeChart(mock.Anything, 214).Return(entity.StyleSizeChart{
		StyleID: 214,
		Cells: []entity.StyleSizeChartCell{
			{SizeID: 3, MeasurementNameID: 11, Value: decimal.RequireFromString("50")},
			{SizeID: 4, MeasurementNameID: 11, Value: decimal.RequireFromString("52")},
		},
		GradeBaseSizeID: 4,
		GradeSteps:      []entity.StyleSizeChartGradeStep{{MeasurementNameID: 11, Step: decimal.RequireFromString("2")}},
	}, nil)
	cards.EXPECT().ListStyleAssembly(mock.Anything, 214).Return([]entity.StyleAssembly{{
		Id: 1, StyleId: 214, ComponentTechCardId: 902, Qty: decimal.RequireFromString("1"),
		PrintNote: amgNS("brand logo"), Active: true,
	}}, nil)
	component := &entity.TechCard{Id: 902}
	component.StyleNumber = amgNS("GRB-AUX-0012")
	cards.EXPECT().GetTechCardById(mock.Anything, 902).Return(component, nil)

	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{{
		Material: entity.Material{Id: 8120, MaterialInsert: entity.MaterialInsert{
			Name: "wool melton 320", Code: amgNS("F-WOOL-320"),
			Unit: amgNS("m"), MaterialClass: string(entity.MaterialClassFabric),
			CuttingCoefficient: amgND("1.03"),
			FabricAttr:         &entity.MaterialFabricAttr{SelvedgeCm: decimal.RequireFromString("1.5")},
		}},
		LatestPrice: &entity.MaterialPrice{
			MaterialId: 8120,
			Price:      decimal.RequireFromString(amgMaterialPrice),
			Currency:   amgCurrency,
		},
	}}, nil)

	media.EXPECT().GetMediaByIds(mock.Anything, []int{4020}).Return(map[int]entity.MediaFull{
		4020: {Id: 4020, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/a.jpg",
			FullSizeWidth:    2400, FullSizeHeight: 3200,
		}},
	}, nil)
	files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/a.jpg").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) {
			return io.NopCloser(strings.NewReader("JPEG-BYTES")), 10, nil
		}).Maybe()
	files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/front_v3.dxf").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) {
			return io.NopCloser(strings.NewReader("DXF-FRONT")), 9, nil
		}).Maybe()

	layout, err := protojson.Marshal(&pb_common.TechCardMarkerLayout{
		SchemaVersion: 3,
		Pieces: []*pb_common.TechCardMarkerPiece{{
			PieceId: 1, Name: "FP_L", Quantity: 1, SizeId: 4,
			SourceUrl: "https://cdn.grbpwr.com/tech-card-patterns/front_v3.dxf",
		}},
	})
	require.NoError(t, err)
	cards.EXPECT().GetMarker(mock.Anything, 77).Return(&entity.TechCardMarker{
		TechCardMarkerSummary: entity.TechCardMarkerSummary{
			Id: 77, TechCardId: 214, Name: "shell 150 cm",
			SizeId:     sql.NullInt64{Int64: 4, Valid: true},
			BomItemId:  sql.NullInt64{Int64: 501, Valid: true},
			BomLineKey: amgNS("BOM-SHELL"),
		},
		Layout: string(layout),
	}, nil)

	return &Server{repo: repo, bucket: files}
}

// ─────────────────────────────────────────────────────────────────────────────
// Building the archive under test
// ─────────────────────────────────────────────────────────────────────────────

// amgArchive is the ZIP this file interrogates: its bytes, the entry names the WRITER actually put
// in it (read back out of the ZIP directory, not predicted — the gate scans what shipped), and the
// manifest the writer returned to its caller.
type amgArchive struct {
	Bytes    []byte
	Names    []string
	Manifest techcardarchive.Manifest
}

// amgBuildArchive produces the archive under test from all three halves of the export: the card.json
// builder (Ф1.2), the sidecar collector (Ф1.3) and the ZIP writer (Ф1.5). It is deliberately the
// ONLY place in this file that knows how an archive is made — the fixture, both scanners, the
// canaries and the mutation pair below work on the product, whatever produced it.
//
// The writer landed in the tree while this gate was being written, so the gate points at THE REAL
// PRODUCT rather than at a hand-rolled stand-in. That matters for one assertion in particular: the
// manifest's money_policy is now the writer's, so "the archive promises its money was cut" is a
// claim about what ships and not about what this test typed.
//
// Everything the handler (Ф1.5, still in flight when this was written) will add on top is the
// caller's half of the seam and is reproduced here: the merged hole list, and id_maps.sizes as the
// SUPERSET — the collectors' SizeNames, which include sizes named only inside a marker blob.
func amgBuildArchive(t *testing.T, s *Server, card *entity.TechCard) amgArchive {
	t.Helper()

	cardJSON, cardHoles, err := buildArchiveCardJSON(card)
	require.NoError(t, err)

	sc, err := s.collectArchiveSidecars(t.Context(), card)
	require.NoError(t, err)
	defer sc.Close()
	require.Empty(t, sc.Holes, "the fixture is healthy: a hole here means a mock stopped answering")

	sizes := make(map[string]string, len(sc.SizeNames))
	for id, name := range sc.SizeNames {
		sizes[strconv.Itoa(id)] = name
	}
	colorways := make(map[string]string, len(card.Colorways))
	for _, cw := range card.Colorways {
		colorways[strconv.Itoa(cw.Id)] = cw.ColorCode
	}

	in := techcardarchive.ArchiveInput{
		ExportedAt: amgAt(),
		ExportedBy: "im",
		Source: techcardarchive.Source{
			Host:        "backend.grbpwr.com",
			TechCardID:  int32(card.Id),
			StyleNumber: card.StyleNumber.String,
			LockVersion: int32(card.LockVersion),
		},
		IDMaps: techcardarchive.IDMaps{Sizes: sizes, Colorways: colorways},
		// BOTH sources of holes, merged by the caller because only the caller has both halves.
		Holes:     append(append([]techcardarchive.ExportHole{}, cardHoles...), sc.Holes...),
		CardJSON:  cardJSON,
		SizeChart: sc.SizeChart,
		Assembly:  sc.Assembly,
		Colorways: sc.Colorways,
		Materials: sc.Materials,
		Media:     sc.Media,
		Patterns:  sc.Patterns,
		Markers:   sc.Markers,
	}
	for _, f := range sc.MarkerFiles {
		in.MarkerFiles = append(in.MarkerFiles, techcardarchive.JSONFile{Name: f.Name, Data: f.Data})
	}
	for _, b := range sc.Blobs {
		in.Files = append(in.Files, techcardarchive.BinaryFile{
			Name: b.Name, SHA256: b.SHA256, Size: b.Size, Open: b.Open,
		})
	}

	var buf bytes.Buffer
	mf, err := techcardarchive.WriteArchive(&buf, in)
	require.NoError(t, err)

	// The entry names come out of the finished ZIP, never out of the plan above: the money scans
	// walk every JSON entry the archive CARRIES, so a file the writer adds later is scanned the day
	// it appears rather than the day somebody remembers to list it here.
	body := buf.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	return amgArchive{Bytes: body, Names: names, Manifest: mf}
}

func amgJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// The two scanners
// ─────────────────────────────────────────────────────────────────────────────

// amgProtoMoney walks a parsed message and returns the PATH of every SET field whose name is in
// MoneyFieldNamesArchive.
//
// Written here rather than borrowed from techcard_archive_card_test.go on purpose: this is the
// gate, and a gate that shares its measuring instrument with the thing it measures inherits that
// instrument's blind spots. protoreflect.Range never enters an unset field, so every hit is by
// construction a field carrying a value. A matched field is reported and NOT descended into,
// mirroring RedactFieldsDeep: the money under a block that survived is one leak, not a dozen.
func amgProtoMoney(m protoreflect.Message, path string) []string {
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
				hits = append(hits, amgProtoMoney(mv.Message(), fmt.Sprintf("%s[%s]", p, k.String()))...)
				return true
			})
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				hits = append(hits, amgProtoMoney(l.Get(i).Message(), fmt.Sprintf("%s[%d]", p, i))...)
			}
		case fd.Kind() == protoreflect.MessageKind:
			hits = append(hits, amgProtoMoney(v.Message(), p)...)
		}
		return true
	})
	sort.Strings(hits)
	return hits
}

// amgMoneyKeyShapes are the word stems that make a JSON key a money key regardless of whether
// anybody remembered to add it to MoneyFieldNamesArchive.
//
// This is the half of the gate that is not a denylist. The sidecars are OUR structs: today none of
// them can express a price, and a test that asserted "MaterialPassport has no price field" would be
// asserting a compile-time fact in the most expensive way possible. What can actually happen is
// that somebody adds `LastPurchasePrice` to a passport, or `run_cost` to a recipe line, ships it,
// and no name list hears about it. A stem match over the SERIALISED archive hears about it.
//
// Kept deliberately blunt. A false positive costs one line in amgMoneyKeyExempt with a written
// reason; a false negative costs a factory learning what GRBPWR pays for its wool.
//
// The first row is the stems Ф0.4's descriptor guard settled on (guardSubstrings in
// internal/techcardarchive/walk_test.go), minus the id stems it also carries — this file is about
// money only. "vat" is deliberately NOT among them: it matches "private" and "activated", and a VAT
// figure has no home in the tech-card contract for it to leak out of. "rate" is out for the same
// shape of reason: it matches "generated", "separate" and every *_rate that is a ratio.
//
// The second row is WIDER THAN THAT GUARD ON PURPOSE (R2-7). Ф0.4 measured the contract as it
// stands; this gate has to survive the field nobody has added yet, and `handling_fee`,
// `import_duty`, `retail_markup` or a `discount` on a recipe line would clear all three instruments
// at once — not on the denylist, no stem to catch them, and no canary a fixture can plant for a
// field that does not exist. A false positive here costs one line in amgMoneyKeyExempt with a
// written reason; a false negative costs a factory learning what GRBPWR pays for its wool.
var amgMoneyKeyShapes = []string{
	"price", "cost", "margin", "amount", "currenc", "total", "payment", "invoice",
	"fee", "tax", "duty", "discount", "retail",
}

// amgMoneyKeyExempt names keys that match a stem above and are NOT money, each with the reason it
// is safe. Keys are NORMALISED (lower-case, underscores removed) so one entry covers both
// spellings the archive uses: protojson writes card.json and the marker blobs in camelCase, our own
// sidecars are snake_case.
//
// The three entries are the same three decisions Ф0.4's guard already recorded (guardExclusions),
// restated rather than re-argued: two counts and a geometric border, all from the marker summary.
// One vocabulary, two enforcement points.
var amgMoneyKeyExempt = map[string]string{
	"totalcount": "TechCardMarkerSummary.total_count — placed PIECE instances in the lay. A count, " +
		"and a marker's own numbers travel as data",
	"totalunits": "TechCardMarkerSummary.total_units — GARMENTS the lay cuts. Same as total_count",
	"edgemargincm": "TechCardMarkerSummary.edge_margin_cm — the CENTIMETRES left free at the edge " +
		"of the lay. Caught by the `margin` stem, which is there for gross_margin: geometry, not money",
}

func amgNormaliseKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", ""))
}

func amgKeyIsMoneyShaped(key string) (bool, string) {
	norm := amgNormaliseKey(key)
	if reason, ok := amgMoneyKeyExempt[norm]; ok {
		return false, reason
	}
	for name := range techcardarchive.MoneyFieldNamesArchive {
		if amgNormaliseKey(name) == norm {
			return true, "listed in MoneyFieldNamesArchive"
		}
	}
	for _, stem := range amgMoneyKeyShapes {
		if strings.Contains(norm, stem) {
			return true, "key name carries the money stem " + strconv.Quote(stem)
		}
	}
	return false, ""
}

// amgJSONMoney walks decoded JSON and reports every money-shaped KEY it finds, by path.
//
// Presence is the offence, not the value: a `unit_price` key holding null still tells a reader that
// this format has a place for a price, and the format's promise (money_policy: stripped-v1) is that
// it does not.
func amgJSONMoney(v any, path string) []string {
	var hits []string
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := path + "." + k
			if money, why := amgKeyIsMoneyShaped(k); money {
				hits = append(hits, fmt.Sprintf("%s (%s)", p, why))
				continue
			}
			hits = append(hits, amgJSONMoney(t[k], p)...)
		}
	case []any:
		for i, item := range t {
			hits = append(hits, amgJSONMoney(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return hits
}

// ─────────────────────────────────────────────────────────────────────────────
// The gate
// ─────────────────────────────────────────────────────────────────────────────

func TestArchiveMoneyGate(t *testing.T) {
	card := amgMoneyCard()
	arc := amgBuildArchive(t, amgServer(t), card)

	// The archive is opened the way the RECEIVING side opens it. Everything the reader refuses —
	// a foreign format, another MAJOR, a missing money_policy, a forbidden entry name, a body that
	// disagrees with the digest in its own name — is refused here, before any claim below is made.
	a, err := techcardarchive.OpenArchive(bytes.NewReader(arc.Bytes), int64(len(arc.Bytes)))
	require.NoError(t, err, "the archive must open under the real reader, not just under this test")
	require.Empty(t, a.UnknownEntries,
		"every entry the export writes must be one FORMAT.md §1 names; an unknown entry here is a "+
			"file that travels to a factory and that no reader on either side knows anything about")

	// ── Positive control ─────────────────────────────────────────────────────────────────────
	// Every assertion in this file says something is ABSENT. Absence proves nothing until the same
	// thing is shown PRESENT in what the export was handed.
	t.Run("positive control: the export was handed real money", func(t *testing.T) {
		raw := dto.ConvertEntityTechCardToPb(amgMoneyCard(), dto.CostingFx{})
		require.NotNil(t, raw)

		hits := amgProtoMoney(raw.ProtoReflect(), "TechCard")
		require.NotEmpty(t, hits, "the fixture must carry money before the builder removes it")
		t.Logf("money the fixture carries into the export (%d fields): %v", len(hits), hits)

		require.NotNil(t, raw.GetTechCard().GetCosting(), "costing block")
		require.Equal(t, amgBomUnitPrice, raw.GetTechCard().GetBomItems()[0].GetUnitPrice().GetValue())
		require.Equal(t, amgCurrency, raw.GetTechCard().GetBomItems()[0].GetCurrency())
		require.NotEmpty(t, raw.GetTechCard().GetBomItems()[0].GetPriceSource())
		require.Equal(t, amgColorwayCost, raw.GetColorways()[0].GetCostPrice().GetValue())
		require.NotEmpty(t, raw.GetColorways()[0].GetPrices(), "the colourway's retail price list")

		// And the catalogue row the material passport is built from really is priced — otherwise
		// "materials/index.json has no price" would be a statement about the fixture.
		priced := dto.ConvertEntityMaterialToPb(entity.MaterialWithPrice{
			Material: entity.Material{Id: 8120, MaterialInsert: entity.MaterialInsert{Name: "wool melton 320"}},
			LatestPrice: &entity.MaterialPrice{
				MaterialId: 8120, Price: decimal.RequireFromString(amgMaterialPrice), Currency: amgCurrency,
			},
		})
		require.NotNil(t, priced.GetLatestPrice(), "the catalogue row carries a latest price")
	})

	// ── The manifest's promise ───────────────────────────────────────────────────────────────
	t.Run("the manifest promises the money was cut", func(t *testing.T) {
		raw, err := a.ReadFile(techcardarchive.FileManifest)
		require.NoError(t, err)
		var m techcardarchive.Manifest
		require.NoError(t, json.Unmarshal(raw, &m))

		require.Equal(t, techcardarchive.MoneyPolicyStrippedV1, m.MoneyPolicy,
			"an archive that does not SAY its money was cut is one nobody promised it was")
		require.Equal(t, techcardarchive.FormatName, m.Format)
		require.Equal(t, techcardarchive.FormatVersion, m.FormatVersion)

		// The manifest the writer HANDED BACK and the one it WROTE must be the same statement: the
		// handler shows the caller the returned copy while the factory reads the file, and two
		// answers to "what left the building" is one answer too many.
		require.Equal(t, arc.Manifest.MoneyPolicy, m.MoneyPolicy)
		require.Equal(t, arc.Manifest.Contents, m.Contents)
		require.Equal(t, techcardarchive.Contents{Media: 1, Patterns: 1, Markers: 1, Materials: 1}, m.Contents,
			"the archive must CLAIM the content the subtests below scan — a claim of zero would "+
				"make every absence assertion here vacuous")

		// The flag is next to the check, not merely next to the data: an archive without it is
		// refused whole by the reader every consumer goes through.
		var without techcardarchive.Manifest
		require.NoError(t, json.Unmarshal(raw, &without))
		without.MoneyPolicy = ""
		swapped := amgReplaceEntry(t, arc, techcardarchive.FileManifest, amgJSON(t, without))
		_, err = techcardarchive.OpenArchive(bytes.NewReader(swapped), int64(len(swapped)))
		require.Error(t, err, "a manifest without money_policy must refuse the whole archive")
		require.Contains(t, err.Error(), "money_policy")
	})

	// ── card.json ────────────────────────────────────────────────────────────────────────────
	t.Run("no money field survives in card.json", func(t *testing.T) {
		// STRICT parse: the import reads with DiscardUnknown:true because it must tolerate a newer
		// MINOR (FORMAT.md §3); a test of our OWN writer has no such excuse, and a field that
		// stopped resolving must surface as a parse failure instead of vanishing on the way in.
		raw, err := a.ReadFile(techcardarchive.FileCard)
		require.NoError(t, err)
		var parsed pb_common.TechCard
		require.NoError(t, (protojson.UnmarshalOptions{}).Unmarshal(raw, &parsed),
			"card.json must parse back under strict protojson")

		hits := amgProtoMoney(parsed.ProtoReflect(), "card.json")
		require.Empty(t, hits,
			"these money fields left the building inside card.json: %v.\n"+
				"An archive goes to an outside factory. Fix the builder (Ф1.2) — never the denylist", hits)

		// Checked by identity and NOT by the walk above: Range never visits an unset field, so a
		// present-but-blank TechCardCosting scores zero hits while still announcing that this card
		// HAS a costing block.
		require.Nil(t, parsed.GetTechCard().GetCosting(), "the costing block must be absent, not empty")
		require.Nil(t, parsed.GetColorways(), "colourway refs carry a COGS and a price list")
	})

	// ── The sidecars: the half of the archive card.json cannot speak for ─────────────────────
	t.Run("the sidecars under test are actually present", func(t *testing.T) {
		// The three subtests below assert that money is absent from colorways.json and
		// materials/index.json. If either file were missing, they would pass by vacuity.
		for _, name := range []string{
			techcardarchive.FileColorways,
			techcardarchive.FileMaterialsIndex,
			techcardarchive.FileSizeChart,
			techcardarchive.FileAssembly,
			techcardarchive.FileMediaIndex,
			techcardarchive.FilePatternsIndex,
			techcardarchive.FileMarkersIndex,
		} {
			require.True(t, a.Has(name), "the archive under test must carry %s", name)
		}

		var colorways []techcardarchive.ColorwayPayload
		require.NoError(t, json.Unmarshal(amgRead(t, a, techcardarchive.FileColorways), &colorways))
		require.Len(t, colorways, 1, "the colourway travels — with its recipe and without its COGS")
		require.Equal(t, "BLK", colorways[0].ColorCode)
		require.NotEmpty(t, colorways[0].Recipe, "an empty recipe would make the money claim vacuous")

		var materials []techcardarchive.MaterialPassport
		require.NoError(t, json.Unmarshal(amgRead(t, a, techcardarchive.FileMaterialsIndex), &materials))
		require.Len(t, materials, 1, "the priced catalogue row travels as a passport")
		require.Equal(t, "F-WOOL-320", materials[0].Code, "…identified well enough to be matched")
	})

	t.Run("no money-shaped key in any file of the archive", func(t *testing.T) {
		// Runs over EVERY json entry, not over the two structs a reviewer would think of: the
		// manifest, the indexes and the raw marker blobs are files that travel too.
		for _, name := range arc.Names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			var v any
			require.NoError(t, json.Unmarshal(amgRead(t, a, name), &v), "entry %q", name)
			hits := amgJSONMoney(v, name)
			require.Empty(t, hits,
				"money-shaped keys in %s: %v.\n"+
					"Either a money field was added to an archive payload — fix the payload, not this "+
					"test — or the key is innocent and belongs in amgMoneyKeyExempt WITH A REASON",
				name, hits)
		}
	})

	t.Run("no money VALUE anywhere in the archive's text", func(t *testing.T) {
		// The net under the name lists. A denylist cannot catch a price that reappears under a
		// name nobody wrote down; the figure itself is the same either way, and every money figure
		// in the fixture is a canary no consumption, width or count could produce.
		//
		// EVERY entry, binaries included — a canary hit inside a pattern or a media file would be
		// absurd, and absurd is exactly what a gate is for.
		for _, name := range arc.Names {
			body := string(amgRead(t, a, name))
			for _, canary := range amgCanaries() {
				require.NotContains(t, body, canary,
					"the money value %s travelled in %s under a name no denylist caught", canary, name)
			}
		}
	})

	// ── The other half of "structure stays, money goes" ──────────────────────────────────────
	t.Run("the archive still describes the garment", func(t *testing.T) {
		// An export that passed everything above by writing an empty archive would fail here. This
		// is the assertion that makes the gate a gate and not a shredder.
		var parsed pb_common.TechCard
		require.NoError(t, (protojson.UnmarshalOptions{}).Unmarshal(
			amgRead(t, a, techcardarchive.FileCard), &parsed))

		b := parsed.GetTechCard().GetBomItems()[0]
		require.Equal(t, "BOM-SHELL", b.GetLineKey())
		require.Equal(t, "3", b.GetWastagePercent().GetValue(), "wastage is a share of fabric, not a price")
		require.Equal(t, "1.4", b.GetQtyPerGarment().GetValue(), "consumption is metres per garment")
		require.Equal(t, "150", b.GetFabricWidth().GetValue(), "roll width is geometry")
		require.Equal(t, "4.5", parsed.GetTechCard().GetOperations()[0].GetSmv().GetValue(),
			"per-operation minutes survive: they are what lets the far side rebuild the total_sam "+
				"that left with the costing block")
		require.Len(t, parsed.GetTechCard().GetSizeIds(), 2, "the size range")

		var materials []techcardarchive.MaterialPassport
		require.NoError(t, json.Unmarshal(amgRead(t, a, techcardarchive.FileMaterialsIndex), &materials))
		require.Equal(t, "1.03", materials[0].CuttingCoefficient,
			"the cutting coefficient is a property of the article, not a price — it must NOT be "+
				"caught up in the money cut")
		require.NotNil(t, materials[0].Attributes, "the CTI attributes are how the article is identified")

		var colorways []techcardarchive.ColorwayPayload
		require.NoError(t, json.Unmarshal(amgRead(t, a, techcardarchive.FileColorways), &colorways))
		require.Equal(t, "1.42", colorways[0].Recipe[0].Consumption, "the recipe's norm is not money")
		require.Equal(t, map[string]string{"s": "1.38", "m": "1.42"}, colorways[0].Recipe[0].SizeConsumptions)
	})

	// ── The gate's own instruments ───────────────────────────────────────────────────────────
	t.Run("both scanners can go red", func(t *testing.T) {
		// A scanner that never matches is a green light wired to nothing. Neither of these is a
		// mutation of production code — they are the instruments being calibrated against known
		// money, which is what makes every "require.Empty" above mean something.
		leaky := &pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
			BomItems: []*pb_common.TechCardBomItem{{
				LineKey: "BOM-SHELL", Currency: amgCurrency,
			}},
			Costing: &pb_common.TechCardCosting{},
		}}
		require.ElementsMatch(t,
			[]string{"probe.tech_card.bom_items[0].currency", "probe.tech_card.costing"},
			amgProtoMoney(leaky.ProtoReflect(), "probe"),
			"the proto scanner must name both the leaf and the block")

		var payload any
		require.NoError(t, json.Unmarshal([]byte(`[
			{"color_code":"BLK","recipe":[{"bom_line_key":"BOM-SHELL","line_total":"12.00"}]},
			{"color_code":"OLV","latest_price":{"price":"18.40","currency":"EUR"}},
			{"color_code":"ECR","last_purchase_cost":"9.10"}
		]`), &payload))
		hits := amgJSONMoney(payload, "probe")
		require.Len(t, hits, 3, "one hit per money key, and a matched key is not descended into: %v", hits)
		require.Contains(t, hits[0], "[0].recipe[0].line_total")
		require.Contains(t, hits[1], "[1].latest_price")
		require.Contains(t, hits[2], "[2].last_purchase_cost",
			"a name NOBODY listed must still be caught — this is the half of the gate that is not "+
				"a denylist")
	})

	t.Run("the exemption list cannot hide money", func(t *testing.T) {
		// An exemption is the one way this gate can be widened, so it is held to two rules. A
		// stale entry — one no stem raises any more — is dead weight that would silently exempt a
		// future field of that name; and an entry naming an actual money field would be the
		// denylist being weakened for green, which is the one thing Ф1.4 may not do.
		for norm, reason := range amgMoneyKeyExempt {
			require.NotEmpty(t, reason, "exemption %q must say WHY", norm)

			var raised bool
			for _, stem := range amgMoneyKeyShapes {
				if strings.Contains(norm, stem) {
					raised = true
					break
				}
			}
			require.True(t, raised,
				"exemption %q is raised by no stem any more — delete it rather than leave a silent "+
					"widening of the gate behind", norm)

			for name := range techcardarchive.MoneyFieldNamesArchive {
				require.NotEqual(t, amgNormaliseKey(name), norm,
					"%q is money by MoneyFieldNamesArchive and must never be exempt here", norm)
			}
		}
	})
}

// amgRead reads one entry whole through the reader, which verifies its length and — for a
// content-addressed name — its digest on the way.
func amgRead(t *testing.T, a *techcardarchive.Archive, name string) []byte {
	t.Helper()
	b, err := a.ReadFile(name)
	require.NoError(t, err, "entry %q", name)
	return b
}

// amgReplaceEntry rebuilds the archive with one entry's body replaced, preserving entry order. Used
// to show that a claim in the manifest is enforced rather than merely written down.
func amgReplaceEntry(t *testing.T, arc amgArchive, name string, body []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(arc.Bytes), int64(len(arc.Bytes)))
	require.NoError(t, err)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		w, err := zw.Create(f.Name)
		require.NoError(t, err)
		if f.Name == name {
			_, err = w.Write(body)
			require.NoError(t, err)
			continue
		}
		rc, err := f.Open()
		require.NoError(t, err)
		_, err = io.Copy(w, rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
