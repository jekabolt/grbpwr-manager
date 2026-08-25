package techcardarchive

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestWalkRedactFieldsDeep / TestWalkRemapIntFieldsDeep — the two traversals.
// TestFieldListGuard — the guard that keeps the name lists from rotting.
// ─────────────────────────────────────────────────────────────────────────────

func dec(s string) *decimal.Decimal { return &decimal.Decimal{Value: s} }

// moneyLoadedCard builds a card whose every money-bearing branch is populated, plus enough
// non-money content next to each one to prove the redaction is surgical.
func moneyLoadedCard() *pb_common.TechCard {
	return &pb_common.TechCard{
		Id: 42,
		TechCard: &pb_common.TechCardInsert{
			StyleNumber: "GB-001",
			SizeIds:     []int32{3, 4, 5},
			BomItems: []*pb_common.TechCardBomItem{{
				Name:            "main fabric",
				Supplier:        "Mill",
				MaterialId:      777,
				UnitPrice:       dec("12.50"),
				Currency:        "EUR",
				PriceSource:     "catalog",
				PriceSnapshotAt: timestamppb.New(timeFixed()),
				FabricWidth:     dec("150"),
			}},
			Costing: &pb_common.TechCardCosting{
				CmtCost:          dec("8.00"),
				LogisticsCost:    dec("1.00"),
				OverheadCost:     dec("2.00"),
				Currency:         "EUR",
				UnitCost:         dec("23.50"),
				OrderCost:        dec("2350"),
				MaterialsPerUnit: dec("12.50"),
				TotalSam:         dec("44"),
				MaterialsTotal:   []*pb_common.TechCardCostLine{{Currency: "EUR", Amount: dec("12.50")}},
				ColorwayCosts: []*pb_common.TechCardColorwayCost{{
					ColorwayId: 812,
					UnitCost:   dec("23.50"),
					OrderCost:  dec("2350"),
				}},
			},
		},
		Colorways: []*pb_common.AdminColorwayRef{{
			ColorwayId:         812,
			ColorCode:          "BLK",
			CostPrice:          dec("23.50"),
			CostPriceSource:    "techcard",
			CostPriceUpdatedAt: timestamppb.New(timeFixed()),
			Prices:             []*pb_common.ColorwayPrice{{Currency: "EUR", Price: dec("199")}},
			NetPrices:          []*pb_common.ColorwayPrice{{Currency: "EUR", Price: dec("161.79")}},
			SwatchMediaId:      9001,
			Usages: []*pb_common.TechCardColorwayUsage{{
				Placement:    "outer",
				Consumption:  dec("1.4"),
				LineTotal:    dec("17.50"),
				SizeRunTotal: dec("1750"),
				MaterialId:   proto.Int64(777),
			}},
		}},
	}
}

func timeFixed() time.Time { return time.Unix(1700000000, 0).UTC() }

func TestWalkRedactFieldsDeep(t *testing.T) {
	t.Run("money is cleared through nesting, lists and the whole costing block", func(t *testing.T) {
		tc := moneyLoadedCard()
		RedactFieldsDeep(tc.ProtoReflect(), MoneyFieldNamesArchive)

		if tc.TechCard.Costing != nil {
			t.Errorf("costing block survived redaction: %v", tc.TechCard.Costing)
		}
		bom := tc.TechCard.BomItems[0]
		if bom.UnitPrice != nil || bom.Currency != "" || bom.PriceSource != "" || bom.PriceSnapshotAt != nil {
			t.Errorf("bom money survived: unit_price=%v currency=%q price_source=%q snapshot=%v",
				bom.UnitPrice, bom.Currency, bom.PriceSource, bom.PriceSnapshotAt)
		}
		// Surgical: the non-money neighbours of a cleared field stay.
		if bom.Name != "main fabric" || bom.Supplier != "Mill" || bom.MaterialId != 777 || bom.FabricWidth.GetValue() != "150" {
			t.Errorf("redaction was not surgical on the bom line: %+v", bom)
		}
		if got := tc.TechCard.SizeIds; len(got) != 3 {
			t.Errorf("size_ids must survive money redaction, got %v", got)
		}

		cw := tc.Colorways[0]
		if cw.CostPrice != nil || cw.CostPriceSource != "" || cw.CostPriceUpdatedAt != nil {
			t.Errorf("colourway cost price survived: %v / %q / %v", cw.CostPrice, cw.CostPriceSource, cw.CostPriceUpdatedAt)
		}
		if len(cw.Prices) != 0 || len(cw.NetPrices) != 0 {
			t.Errorf("price lists survived: %v / %v", cw.Prices, cw.NetPrices)
		}
		usage := cw.Usages[0]
		if usage.LineTotal != nil || usage.SizeRunTotal != nil {
			t.Errorf("usage totals survived: %v / %v", usage.LineTotal, usage.SizeRunTotal)
		}
		// …while the usage itself, its consumption and its material pin are untouched.
		if usage.Consumption.GetValue() != "1.4" || usage.GetMaterialId() != 777 || usage.Placement != "outer" {
			t.Errorf("redaction damaged a non-money usage field: %+v", usage)
		}
		if cw.ColorCode != "BLK" || cw.SwatchMediaId != 9001 {
			t.Errorf("redaction damaged colourway identity: %+v", cw)
		}
	})

	t.Run("a matched field is cleared whole, not descended into", func(t *testing.T) {
		tc := moneyLoadedCard()
		// `amount` lives only inside costing. Redacting by a list that contains BOTH `costing`
		// and nothing else proves the parent match short-circuits the descent.
		RedactFieldsDeep(tc.ProtoReflect(), map[string]bool{"costing": true})
		if tc.TechCard.Costing != nil {
			t.Fatalf("costing not cleared")
		}
		if tc.TechCard.BomItems[0].UnitPrice.GetValue() != "12.50" {
			t.Errorf("clearing costing must not touch anything outside it")
		}
	})

	t.Run("google.type.Decimal is a message: clearing means Clear(fd)", func(t *testing.T) {
		tc := &pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
			Costing: &pb_common.TechCardCosting{UnitCost: dec("0")},
		}}
		RedactFieldsDeep(tc.ProtoReflect(), map[string]bool{"unit_cost": true})
		if tc.TechCard.Costing.UnitCost != nil {
			t.Errorf("a Decimal holding \"0\" must be cleared as a message, got %v", tc.TechCard.Costing.UnitCost)
		}
	})

	t.Run("map: named map is cleared, unnamed map is descended into", func(t *testing.T) {
		d := &pb_common.Dictionary{
			BaseCurrency: "EUR",
			ComplimentaryShippingPrices: map[string]*decimal.Decimal{
				"EUR": dec("100"),
				"USD": dec("120"),
			},
		}
		RedactFieldsDeep(d.ProtoReflect(), map[string]bool{"value": true})
		if len(d.ComplimentaryShippingPrices) != 2 {
			t.Fatalf("the map itself must survive, got %v", d.ComplimentaryShippingPrices)
		}
		for k, v := range d.ComplimentaryShippingPrices {
			if v.GetValue() != "" {
				t.Errorf("map value %q was not descended into: %q", k, v.GetValue())
			}
		}

		d2 := &pb_common.Dictionary{
			BaseCurrency:                "EUR",
			ComplimentaryShippingPrices: map[string]*decimal.Decimal{"EUR": dec("100")},
		}
		RedactFieldsDeep(d2.ProtoReflect(), map[string]bool{"complimentary_shipping_prices": true})
		if len(d2.ComplimentaryShippingPrices) != 0 {
			t.Errorf("a map matched BY NAME must be cleared whole, got %v", d2.ComplimentaryShippingPrices)
		}
		if d2.BaseCurrency != "EUR" {
			t.Errorf("neighbour field damaged")
		}
	})

	t.Run("empty name list and nil message are no-ops", func(t *testing.T) {
		tc := moneyLoadedCard()
		before := proto.Clone(tc)
		RedactFieldsDeep(tc.ProtoReflect(), nil)
		if !proto.Equal(before, tc) {
			t.Errorf("an empty name list must change nothing")
		}
		RedactFieldsDeep(nil, MoneyFieldNamesArchive)
		var typed *pb_common.TechCard
		RedactFieldsDeep(typed.ProtoReflect(), MoneyFieldNamesArchive) // invalid (read-only nil) message
	})
}

func TestWalkRemapIntFieldsDeep(t *testing.T) {
	// sizes 3,4,5 in the source database are 30,40,50 in the target; 6 is a hole.
	sizeMap := map[int64]int64{3: 30, 4: 40, 5: 50}

	t.Run("nested, repeated-scalar and repeated-message branches", func(t *testing.T) {
		tc := &pb_common.TechCard{
			ModelWearsSizeId: 4,
			TechCard: &pb_common.TechCardInsert{
				BaseSampleSizeId: 3,
				SizeIds:          []int32{3, 4, 5},
				SizeQuantities: []*pb_common.TechCardSizeQuantity{
					{SizeId: 3, OrderQty: 10},
					{SizeId: 5, OrderQty: 20},
				},
				Patterns: []*pb_common.TechCardSizePattern{{SizeId: 4, Filename: "front.dxf"}},
			},
			Colorways: []*pb_common.AdminColorwayRef{{
				Usages: []*pb_common.TechCardColorwayUsage{{
					SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{{SizeId: 5, Consumption: dec("1.4")}},
				}},
			}},
		}
		var misses []string
		RemapIntFieldsDeep(tc.ProtoReflect(), SizeFieldNames, sizeMap, func(f string, o int64) {
			misses = append(misses, fmt.Sprintf("%s=%d", f, o))
		})
		if len(misses) != 0 {
			t.Fatalf("unexpected holes: %v", misses)
		}
		if tc.ModelWearsSizeId != 40 {
			t.Errorf("root scalar not remapped: %d", tc.ModelWearsSizeId)
		}
		if tc.TechCard.BaseSampleSizeId != 30 {
			t.Errorf("nested message field not remapped: %d", tc.TechCard.BaseSampleSizeId)
		}
		if got := tc.TechCard.SizeIds; len(got) != 3 || got[0] != 30 || got[1] != 40 || got[2] != 50 {
			t.Errorf("repeated scalar not remapped: %v", got)
		}
		if tc.TechCard.SizeQuantities[0].SizeId != 30 || tc.TechCard.SizeQuantities[1].SizeId != 50 {
			t.Errorf("repeated message not remapped: %v", tc.TechCard.SizeQuantities)
		}
		if tc.TechCard.SizeQuantities[0].OrderQty != 10 {
			t.Errorf("a same-message neighbour was damaged")
		}
		if tc.TechCard.Patterns[0].SizeId != 40 {
			t.Errorf("patterns not remapped: %v", tc.TechCard.Patterns)
		}
		if tc.Colorways[0].Usages[0].SizeConsumptions[0].SizeId != 50 {
			t.Errorf("three-level nesting not remapped")
		}
	})

	// The two halves of "0 is unset" are enforced by DIFFERENT mechanisms, and only one of
	// them is a guard in this package's code. Both subtests below exist because deleting the
	// `if old == 0` branch from RemapIntFieldsDeep left the first one green: a proto3 scalar
	// without explicit presence holding 0 is not populated, so Range never yields it and the
	// branch is not even reached. The guard is reachable — and therefore testable — only
	// through a field that carries presence, and through a 0 sitting inside a repeated list.
	t.Run("zero is never touched and never reported (no-presence scalars, lists)", func(t *testing.T) {
		tc := &pb_common.TechCard{
			TechCard: &pb_common.TechCardInsert{
				Callouts: []*pb_common.TechCardCallout{
					{Number: 1, MediaId: 0}, // legitimate "not anchored to a picture"
					{Number: 2, MediaId: 500},
				},
				Details:        []*pb_common.TechCardDetail{{Key: "collar", MediaIds: []int32{0, 500}}},
				MoodboardMedia: []*pb_common.TechCardMediaItem{{MediaId: 0}},
			},
			Colorways: []*pb_common.AdminColorwayRef{{
				SwatchMediaId: 0,
				LabDipRounds:  []*pb_common.ColorwayLabDipRound{{RoundNumber: 1, SwatchMediaId: 500}},
			}},
		}
		var misses []string
		RemapIntFieldsDeep(tc.ProtoReflect(), MediaFieldNames, map[int64]int64{500: 999},
			func(f string, o int64) { misses = append(misses, fmt.Sprintf("%s=%d", f, o)) })
		if len(misses) != 0 {
			t.Fatalf("0 must not be reported as a hole, got %v", misses)
		}
		if tc.TechCard.Callouts[0].MediaId != 0 || tc.TechCard.MoodboardMedia[0].MediaId != 0 {
			t.Errorf("0 was rewritten")
		}
		if tc.TechCard.Callouts[1].MediaId != 999 {
			t.Errorf("non-zero sibling not remapped: %d", tc.TechCard.Callouts[1].MediaId)
		}
		if got := tc.TechCard.Details[0].MediaIds; len(got) != 2 || got[0] != 0 || got[1] != 999 {
			t.Errorf("repeated media_ids: 0 must stay in place, got %v", got)
		}
		if tc.Colorways[0].SwatchMediaId != 0 || tc.Colorways[0].LabDipRounds[0].SwatchMediaId != 999 {
			t.Errorf("swatch_media_id handling wrong: %v", tc.Colorways[0])
		}
	})

	t.Run("zero is never touched and never reported (explicit-presence scalar)", func(t *testing.T) {
		// TechCardColorwayUsage.material_id is `optional int64`, so an explicit 0 IS populated
		// and IS yielded by Range — this is the only shape that reaches the `old == 0` branch of
		// RemapIntFieldsDeep. (material_id is matched, not remapped, in production; here it is
		// simply the one presence-carrying int in the tree that the generic walker can be aimed
		// at.)
		tc := &pb_common.TechCard{Colorways: []*pb_common.AdminColorwayRef{{
			Usages: []*pb_common.TechCardColorwayUsage{
				{Placement: "unset-on-purpose", MaterialId: proto.Int64(0)},
				{Placement: "real", MaterialId: proto.Int64(777)},
			},
		}}}
		var misses []string
		RemapIntFieldsDeep(tc.ProtoReflect(), map[string]bool{"material_id": true},
			map[int64]int64{777: 888},
			func(f string, o int64) { misses = append(misses, fmt.Sprintf("%s=%d", f, o)) })
		if len(misses) != 0 {
			t.Fatalf("an explicit-presence 0 must not be reported as a hole, got %v", misses)
		}
		u0 := tc.Colorways[0].Usages[0]
		if u0.MaterialId == nil || *u0.MaterialId != 0 {
			t.Errorf("an explicit-presence 0 must be left exactly as authored, got %v", u0.MaterialId)
		}
		if tc.Colorways[0].Usages[1].GetMaterialId() != 888 {
			t.Errorf("the non-zero sibling must still be remapped, got %v", tc.Colorways[0].Usages[1].MaterialId)
		}
	})

	t.Run("a hole clears the scalar and drops the repeated entry", func(t *testing.T) {
		tc := &pb_common.TechCard{
			ModelWearsSizeId: 6, // not in the map
			TechCard: &pb_common.TechCardInsert{
				SizeIds:        []int32{3, 6, 5},
				SizeQuantities: []*pb_common.TechCardSizeQuantity{{SizeId: 6, OrderQty: 7}},
			},
		}
		var misses []string
		RemapIntFieldsDeep(tc.ProtoReflect(), SizeFieldNames, sizeMap,
			func(f string, o int64) { misses = append(misses, fmt.Sprintf("%s=%d", f, o)) })
		sort.Strings(misses)
		want := []string{"model_wears_size_id=6", "size_id=6", "size_ids=6"}
		if strings.Join(misses, ",") != strings.Join(want, ",") {
			t.Errorf("holes: got %v want %v", misses, want)
		}
		if tc.ModelWearsSizeId != 0 {
			t.Errorf("a missing scalar must be cleared, got %d", tc.ModelWearsSizeId)
		}
		if got := tc.TechCard.SizeIds; len(got) != 2 || got[0] != 30 || got[1] != 50 {
			t.Errorf("a missing repeated entry must be dropped, not zeroed: %v", got)
		}
		if tc.TechCard.SizeQuantities[0].SizeId != 0 || tc.TechCard.SizeQuantities[0].OrderQty != 7 {
			t.Errorf("missing nested scalar: %v", tc.TechCard.SizeQuantities[0])
		}
	})

	t.Run("a nil onMiss does not panic", func(t *testing.T) {
		tc := &pb_common.TechCard{ModelWearsSizeId: 6}
		RemapIntFieldsDeep(tc.ProtoReflect(), SizeFieldNames, sizeMap, nil)
		if tc.ModelWearsSizeId != 0 {
			t.Errorf("expected the unmapped value to be cleared")
		}
	})

	t.Run("map: message values are descended into, a named map is left alone", func(t *testing.T) {
		md := dynamicMapMessage(t)
		root := dynamicpb.NewMessage(md)
		fdByKey := md.Fields().ByName("by_key")
		fdSizeIds := md.Fields().ByName("size_ids")
		leafMD := fdByKey.MapValue().Message()

		leaf := dynamicpb.NewMessage(leafMD)
		leaf.Set(leafMD.Fields().ByName("size_id"), protoreflect.ValueOfInt32(4))
		leaf.Set(leafMD.Fields().ByName("keep"), protoreflect.ValueOfString("intact"))
		root.Mutable(fdByKey).Map().Set(protoreflect.ValueOfString("a").MapKey(), protoreflect.ValueOfMessage(leaf))

		sm := root.Mutable(fdSizeIds).Map()
		sm.Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfInt32(3))

		RemapIntFieldsDeep(root, SizeFieldNames, sizeMap, func(f string, o int64) {
			t.Errorf("unexpected hole %s=%d", f, o)
		})

		got := root.Get(fdByKey).Map().Get(protoreflect.ValueOfString("a").MapKey()).Message()
		if v := got.Get(leafMD.Fields().ByName("size_id")).Int(); v != 40 {
			t.Errorf("map message value not remapped: %d", v)
		}
		if v := got.Get(leafMD.Fields().ByName("keep")).String(); v != "intact" {
			t.Errorf("map message neighbour damaged: %q", v)
		}
		if v := root.Get(fdSizeIds).Map().Get(protoreflect.ValueOfString("k").MapKey()).Int(); v != 3 {
			t.Errorf("a map matched BY NAME must be left alone by the remapper, got %d", v)
		}

		// …and the redactor clears that same named map whole.
		RedactFieldsDeep(root, map[string]bool{"size_ids": true})
		if root.Get(fdSizeIds).Map().Len() != 0 {
			t.Errorf("named map not cleared by the redactor")
		}
	})
}

// dynamicMapMessage builds, at runtime, a message with a map<string, Leaf> and a
// map<string, int32> — shapes that no generated proto in this repo has (every map in the
// tree is map<string, google.type.Decimal>), and that the map branches of both walkers must
// nevertheless handle.
func dynamicMapMessage(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	str := func(s string) *string { return &s }
	i32 := func(i int32) *int32 { return &i }
	lbl := func(l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto_Label { return &l }
	typ := func(k descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto_Type { return &k }
	tr := true

	mapEntry := func(name, valueType string, valueKind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.DescriptorProto {
		val := &descriptorpb.FieldDescriptorProto{
			Name: str("value"), Number: i32(2),
			Label: lbl(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL), Type: typ(valueKind),
		}
		if valueType != "" {
			val.TypeName = str(valueType)
		}
		return &descriptorpb.DescriptorProto{
			Name:    str(name),
			Options: &descriptorpb.MessageOptions{MapEntry: &tr},
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: str("key"), Number: i32(1),
					Label: lbl(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
					Type:  typ(descriptorpb.FieldDescriptorProto_TYPE_STRING)},
				val,
			},
		}
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    str("techcardarchive/walk_dynamic_test.proto"),
		Package: str("techcardarchive.walktest"),
		Syntax:  str("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: str("Leaf"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: str("size_id"), Number: i32(1),
						Label: lbl(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						Type:  typ(descriptorpb.FieldDescriptorProto_TYPE_INT32)},
					{Name: str("keep"), Number: i32(2),
						Label: lbl(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						Type:  typ(descriptorpb.FieldDescriptorProto_TYPE_STRING)},
				},
			},
			{
				Name: str("Root"),
				NestedType: []*descriptorpb.DescriptorProto{
					mapEntry("ByKeyEntry", ".techcardarchive.walktest.Leaf", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					mapEntry("SizeIdsEntry", "", descriptorpb.FieldDescriptorProto_TYPE_INT32),
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: str("by_key"), Number: i32(1),
						Label:    lbl(descriptorpb.FieldDescriptorProto_LABEL_REPEATED),
						Type:     typ(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
						TypeName: str(".techcardarchive.walktest.Root.ByKeyEntry")},
					{Name: str("size_ids"), Number: i32(2),
						Label:    lbl(descriptorpb.FieldDescriptorProto_LABEL_REPEATED),
						Type:     typ(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
						TypeName: str(".techcardarchive.walktest.Root.SizeIdsEntry")},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build dynamic file descriptor: %v", err)
	}
	return fd.Messages().ByName("Root")
}

// ─────────────────────────────────────────────────────────────────────────────
// The guard.
//
// The canonical lists above are lists of STRINGS. Strings are silent: add a field to
// common.TechCard called `swatch_media_id` and every name-driven walker in this package
// keeps passing while quietly not touching it. This test walks the DESCRIPTORS (not values —
// protoreflect.Range never enters an unset field, so a value walk would see almost nothing)
// of everything the archive serialises, and fails in both directions:
//
//	forward  — a field whose name looks like an id or like money and is in no list;
//	backward — a name in a list that no longer exists in the contract, i.e. a redaction or a
//	           remap that silently stopped happening.
//
// The forward direction has one structural escape hatch, and it is the good kind: a field is
// also covered when EVERY path to it passes through a field that MoneyFieldNamesArchive
// clears whole (`costing`, `latest_price`, `prices`, …). That is not an exemption, it is the
// walker's actual semantics — RedactFieldsDeep clears a matched field without descending —
// so the money inside a redacted block cannot leak no matter what gets added to it later.
// ─────────────────────────────────────────────────────────────────────────────

// guardSubstrings is what "looks like an id or like money" means. The first six come from the
// phase plan; `currenc`, `margin` and `amount` were added after the first run of this test
// showed TechCardCosting carrying money under names none of the six would ever match
// (materials_per_unit, amount, target_margin_pct).
var guardSubstrings = []string{
	"size_id", "media_id", "material_id",
	"price", "cost", "total",
	"currenc", "margin", "amount",
}

// guardExclusions are names the guard raises and that were DECIDED not to belong to any
// canonical list. The reason is the point of the entry: an exclusion without one is just a
// silent list under another name. Every entry must still be raised by the walk — a stale
// exclusion fails this test too.
var guardExclusions = map[string]string{
	"material_id": "materials are matched in the target database by code/supplier_ref " +
		"(materials/index.json), not remapped by id — an id-remap here would bind the card to a " +
		"material row that merely happens to share the number",
	"output_material_id": "same as material_id: the aux card's output article is matched, not remapped",
	"total_count": "TechCardMarkerSummary: how many pieces the marker holds. A count, not money — " +
		"and a marker's own numbers are exported as data, not as a figure to hide",
	"total_units": "TechCardMarkerSummary: garments per lay. Same as total_count",
	"edge_margin_cm": "TechCardMarkerSummary: the CENTIMETRES left free at the edge of the marker. " +
		"Caught by the `margin` substring, which is there for gross_margin — geometry, not money",
}

type guardHit struct {
	name string
	path string
}

// guardRoots are the message types the archive actually serialises. TechCard is card.json;
// StyleSizeChart is sizechart.json and is the ONLY home of grade_base_size_id;
// Material is materials/index.json and the only home of latest_price. Walking just TechCard
// would leave those two list entries unverifiable in both directions.
func guardRoots() []protoreflect.MessageDescriptor {
	return []protoreflect.MessageDescriptor{
		(&pb_common.TechCard{}).ProtoReflect().Descriptor(),
		(&pb_common.StyleSizeChart{}).ProtoReflect().Descriptor(),
		(&pb_common.Material{}).ProtoReflect().Descriptor(),
	}
}

// walkGuardDescriptors returns every field name reachable from the roots, and the subset of
// them reachable WITHOUT passing through a field that MoneyFieldNamesArchive clears whole.
func walkGuardDescriptors() (all map[string]bool, exposed map[string][]string) {
	all = map[string]bool{}
	exposed = map[string][]string{}

	type state struct {
		md       protoreflect.MessageDescriptor
		redacted bool
		path     string
	}
	// Two states per message type (reached under a redacted ancestor or not) — enough to make
	// the walk terminate on a recursive contract while still answering "is there ANY exposed
	// path to this field".
	seen := map[string]bool{}
	queue := make([]state, 0, 64)
	for _, md := range guardRoots() {
		queue = append(queue, state{md: md, path: string(md.Name())})
	}
	for len(queue) > 0 {
		st := queue[0]
		queue = queue[1:]
		key := fmt.Sprintf("%s|%t", st.md.FullName(), st.redacted)
		if seen[key] {
			continue
		}
		seen[key] = true

		fields := st.md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			name := string(fd.Name())
			path := st.path + "." + name
			all[name] = true
			if !st.redacted {
				exposed[name] = append(exposed[name], path)
			}
			var child protoreflect.MessageDescriptor
			switch {
			case fd.IsMap():
				if isMessageValueMap(fd) {
					child = fd.MapValue().Message()
				}
			case isMessageKind(fd):
				child = fd.Message()
			}
			if child != nil {
				queue = append(queue, state{md: child, redacted: st.redacted || MoneyFieldNamesArchive[name], path: path})
			}
		}
	}
	return all, exposed
}

func looksLikeIDOrMoney(name string) bool {
	for _, s := range guardSubstrings {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

func TestFieldListGuard(t *testing.T) {
	all, exposed := walkGuardDescriptors()
	if len(all) < 200 {
		t.Fatalf("the descriptor walk saw only %d field names — it is not walking the contract", len(all))
	}

	t.Run("no id-shaped or money-shaped field escapes every list", func(t *testing.T) {
		var uncovered []guardHit
		for name, paths := range exposed {
			if !looksLikeIDOrMoney(name) {
				continue
			}
			if SizeFieldNames[name] || MediaFieldNames[name] || MoneyFieldNamesArchive[name] {
				continue
			}
			if _, ok := guardExclusions[name]; ok {
				continue
			}
			uncovered = append(uncovered, guardHit{name: name, path: paths[0]})
		}
		sort.Slice(uncovered, func(i, j int) bool { return uncovered[i].name < uncovered[j].name })
		for _, h := range uncovered {
			t.Errorf("field %q (%s) is in NO canonical list and has no exclusion.\n"+
				"Decide, do not delete this test: put it in SizeFieldNames / MediaFieldNames / "+
				"moneyFieldNamesTechCard (walk.go), or add it to guardExclusions WITH the reason it "+
				"needs neither redaction nor remap.", h.name, h.path)
		}
	})

	t.Run("no dead entry in a canonical list", func(t *testing.T) {
		for _, set := range []struct {
			label string
			names map[string]bool
		}{
			{"SizeFieldNames", SizeFieldNames},
			{"MediaFieldNames", MediaFieldNames},
			{"moneyFieldNamesTechCard", moneyFieldNamesTechCard},
		} {
			names := make([]string, 0, len(set.names))
			for n := range set.names {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if !all[n] {
					t.Errorf("%s lists %q, which no longer exists anywhere in the archived contract — "+
						"the field was renamed or removed, and whatever this entry used to protect is now "+
						"unprotected", set.label, n)
				}
			}
		}
	})

	t.Run("no dead exclusion", func(t *testing.T) {
		names := make([]string, 0, len(guardExclusions))
		for n := range guardExclusions {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if len(exposed[n]) == 0 {
				t.Errorf("guardExclusions excuses %q, which the walk never raises (gone from the "+
					"contract, or only reachable under a redacted block) — drop the entry", n)
			}
			if !looksLikeIDOrMoney(n) {
				t.Errorf("guardExclusions excuses %q, which no guard substring matches — the entry "+
					"can never fire and only pretends the name was considered", n)
			}
			if strings.TrimSpace(guardExclusions[n]) == "" {
				t.Errorf("guardExclusions[%q] has no reason", n)
			}
		}
	})

	t.Run("the copied costing denylist still matches the API's", func(t *testing.T) {
		// Not a descriptor check: this one guards the COPY. moneyFieldNamesCosting is a
		// hand-copy of costingRedactedFieldNames from internal/apisrv/admin (which must not be
		// imported). If the API's list grows, this count is the tripwire that says so.
		if got, want := len(moneyFieldNamesCosting), 19; got != want {
			t.Errorf("moneyFieldNamesCosting has %d names, expected %d — re-copy it from "+
				"internal/apisrv/admin/costing_rbac.go (costingRedactedFieldNames) and update this count",
				got, want)
		}
	})
}
