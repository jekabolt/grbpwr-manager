package admin

import (
	"fmt"
	"reflect"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// techCardChangedSections returns stable, already-human labels for the history chip. It compares
// the writable TechCardInsert contract, not the enriched TechCard read model; output-only costing,
// BOM and pattern fields are removed below so a rates refresh or a newly parsed DXF cannot make an
// unrelated note save claim that those sections changed.
func techCardChangedSections(before, after *pb_common.TechCardInsert) []string {
	if before == nil || after == nil {
		return nil
	}
	b := proto.Clone(before).(*pb_common.TechCardInsert)
	a := proto.Clone(after).(*pb_common.TechCardInsert)
	normalizeTechCardDiffPair(b, a)

	changes := make([]string, 0, 8)
	appendGroup := func(label string, fields ...protoreflect.Name) {
		if !techCardFieldsEqual(b, a, fields...) {
			changes = append(changes, label)
		}
	}

	appendGroup("name", "name")
	appendGroup("note", "status", "notes", "mood_note", "garment_description", "flat_words", "concept")
	appendGroup("style", "style_number", "style_number_source", "category_id", "stage", "purpose",
		"output_material_id", "aux_subtype", "target_drop_date", "base_model_id")
	appendGroup("sizes", "measurement_unit", "base_sample_size_id", "size_ids", "size_quantities",
		"required_seam_allowance_mm")
	if n := changedBomLineCount(b.GetBomItems(), a.GetBomItems()); n > 0 {
		lineWord := "lines"
		if n == 1 {
			lineWord = "line"
		}
		changes = append(changes, fmt.Sprintf("bom · %d %s", n, lineWord))
	}
	appendGroup("media", "moodboard_media", "technical_media", "callouts")
	appendGroup("construction", "construction", "details")
	appendGroup("operations", "operations")
	appendGroup("pieces", "pieces", "piece_dxf_aliases")
	appendGroup("patterns", "patterns")
	appendGroup("labels", "labels", "care_label", "garment_labels")
	appendGroup("packaging", "packaging", "packaging_items")
	appendGroup("costing", "costing")
	appendGroup("issues", "issues")
	appendGroup("approval", "approval_state", "approved_at", "released_at", "signoffs")
	return changes
}

// normalizeTechCardDiffPair applies the write contract's preserve-on-absence rules and removes
// read-only projections. This keeps changed_paths about the save that will commit, including for an
// older open tab that legitimately omits a newly added presence-aware field.
func normalizeTechCardDiffPair(before, after *pb_common.TechCardInsert) {
	if after.GetCategoryId() == 0 {
		after.CategoryId = before.GetCategoryId()
	}
	if after.GetMeasurementUnit() == pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_UNKNOWN {
		after.MeasurementUnit = before.GetMeasurementUnit()
	}
	if after.MoodNote == nil {
		after.MoodNote = cloneStringPtr(before.MoodNote)
	}
	if after.GarmentDescription == nil {
		after.GarmentDescription = cloneStringPtr(before.GarmentDescription)
	}
	if after.FlatWords == nil {
		after.FlatWords = cloneStringPtr(before.FlatWords)
	}
	if after.Construction == nil {
		after.Construction = cloneMessage(before.Construction)
	}
	if after.Packaging == nil {
		after.Packaging = cloneMessage(before.Packaging)
	}
	if after.Costing == nil {
		after.Costing = cloneMessage(before.Costing)
	}
	if after.CareLabel == nil {
		after.CareLabel = cloneMessage(before.CareLabel)
	}
	if after.PieceDxfAliases == nil {
		after.PieceDxfAliases = cloneMessage(before.PieceDxfAliases)
	}
	if !after.GetLabelsAware() {
		after.GarmentLabels = cloneMessages(before.GetGarmentLabels())
		after.PackagingItems = cloneMessages(before.GetPackagingItems())
	}

	beforeBom := bomByDiffKey(before.GetBomItems())
	for i, item := range after.GetBomItems() {
		if item == nil {
			continue
		}
		old := beforeBom[bomDiffKey(item, i)]
		materializePreservedBomFields(old, item)
	}
	for _, item := range before.GetBomItems() {
		stripBomReadOnly(item)
	}
	for _, item := range after.GetBomItems() {
		stripBomReadOnly(item)
	}

	beforePatterns := patternByDiffKey(before.GetPatterns())
	for i, pattern := range after.GetPatterns() {
		if pattern == nil {
			continue
		}
		old := beforePatterns[patternDiffKey(pattern, i)]
		materializePreservedPatternFields(old, pattern)
	}
	for _, pattern := range before.GetPatterns() {
		stripPatternReadOnly(pattern)
	}
	for _, pattern := range after.GetPatterns() {
		stripPatternReadOnly(pattern)
	}

	for _, op := range before.GetOperations() {
		stripOperationReadOnly(op)
	}
	for _, op := range after.GetOperations() {
		stripOperationReadOnly(op)
	}
	before.Costing = writableCosting(before.GetCosting())
	after.Costing = writableCosting(after.GetCosting())
}

func techCardFieldsEqual(before, after *pb_common.TechCardInsert, fields ...protoreflect.Name) bool {
	keep := make(map[protoreflect.Name]struct{}, len(fields))
	for _, field := range fields {
		keep[field] = struct{}{}
	}
	b := proto.Clone(before).(*pb_common.TechCardInsert)
	a := proto.Clone(after).(*pb_common.TechCardInsert)
	retainProtoFields(b.ProtoReflect(), keep)
	retainProtoFields(a.ProtoReflect(), keep)
	return proto.Equal(b, a)
}

func retainProtoFields(message protoreflect.Message, keep map[protoreflect.Name]struct{}) {
	var clear []protoreflect.FieldDescriptor
	message.Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if _, ok := keep[field.Name()]; !ok {
			clear = append(clear, field)
		}
		return true
	})
	for _, field := range clear {
		message.Clear(field)
	}
	message.SetUnknown(nil)
}

func changedBomLineCount(before, after []*pb_common.TechCardBomItem) int {
	bm, am := bomByDiffKey(before), bomByDiffKey(after)
	changed := 0
	seen := make(map[string]struct{}, len(bm)+len(am))
	for key, old := range bm {
		seen[key] = struct{}{}
		if current, ok := am[key]; !ok || !proto.Equal(old, current) {
			changed++
		}
	}
	for key := range am {
		if _, ok := seen[key]; !ok {
			changed++
		}
	}
	return changed
}

func bomByDiffKey(items []*pb_common.TechCardBomItem) map[string]*pb_common.TechCardBomItem {
	out := make(map[string]*pb_common.TechCardBomItem, len(items))
	for i, item := range items {
		out[bomDiffKey(item, i)] = item
	}
	return out
}

func bomDiffKey(item *pb_common.TechCardBomItem, index int) string {
	if item != nil && item.GetLineKey() != "" {
		return "key:" + item.GetLineKey()
	}
	return fmt.Sprintf("index:%d", index)
}

func materializePreservedBomFields(before, after *pb_common.TechCardBomItem) {
	if before == nil || after == nil {
		return
	}
	if after.Purpose == nil {
		after.Purpose, after.PurposeNote = before.Purpose, cloneStringPtr(before.PurposeNote)
	}
	if after.Kind == nil && after.KindNote == nil {
		after.Kind, after.KindNote = before.Kind, cloneStringPtr(before.KindNote)
	}
	if after.LabelPart == nil {
		after.LabelPart = before.LabelPart
	}
	if after.IsSample == nil {
		after.IsSample = before.IsSample
	}
	if after.FabricDirection == nil {
		after.FabricDirection = before.FabricDirection
	}
	if after.WastageSource == nil && after.WastageLayCount == nil {
		after.WastageSource, after.WastageLayCount = cloneStringPtr(before.WastageSource), before.WastageLayCount
	}
	if after.QtyPerGarment == nil && after.SpareQty == nil {
		after.QtyPerGarment, after.SpareQty = cloneMessage(before.QtyPerGarment), cloneMessage(before.SpareQty)
	}
	if after.EstUsage == nil {
		after.EstUsage = cloneMessage(before.EstUsage)
	}
}

func stripBomReadOnly(item *pb_common.TechCardBomItem) {
	if item == nil {
		return
	}
	item.Id = 0
	item.UnitCode = pb_common.MaterialUnit_MATERIAL_UNIT_UNKNOWN
	item.PriceSource = ""
	item.PriceSnapshotAt = nil
	item.EffectiveFabricWidthCm = nil
	item.SelvedgeCm = nil
	item.CuttingCoefficient = nil
	item.WastageAppliedAt = nil
}

func patternByDiffKey(items []*pb_common.TechCardSizePattern) map[string]*pb_common.TechCardSizePattern {
	out := make(map[string]*pb_common.TechCardSizePattern, len(items))
	for i, item := range items {
		out[patternDiffKey(item, i)] = item
	}
	return out
}

func patternDiffKey(item *pb_common.TechCardSizePattern, index int) string {
	if item != nil && item.GetLineKey() != "" {
		return "key:" + item.GetLineKey()
	}
	return fmt.Sprintf("index:%d", index)
}

func materializePreservedPatternFields(before, after *pb_common.TechCardSizePattern) {
	if before == nil || after == nil {
		return
	}
	if after.Name == nil {
		after.Name = cloneStringPtr(before.Name)
	}
	if after.BomLineKey == nil {
		after.BomLineKey = cloneStringPtr(before.BomLineKey)
	}
	if after.FabricPurpose == nil {
		after.FabricPurpose = before.FabricPurpose
	}
	if after.Sha256 == nil {
		after.Sha256 = cloneStringPtr(before.Sha256)
	}
}

func stripPatternReadOnly(pattern *pb_common.TechCardSizePattern) {
	if pattern == nil {
		return
	}
	pattern.ViewUrl = ""
	pattern.DownloadUrl = ""
	pattern.History = nil
}

func stripOperationReadOnly(operation *pb_common.TechCardOperation) {
	if operation == nil {
		return
	}
	operation.PieceIds = nil
	operation.BomItemIds = nil
}

func writableCosting(in *pb_common.TechCardCosting) *pb_common.TechCardCosting {
	if in == nil {
		return nil
	}
	return &pb_common.TechCardCosting{
		CmtCost:         cloneMessage(in.CmtCost),
		LogisticsCost:   cloneMessage(in.LogisticsCost),
		OverheadCost:    cloneMessage(in.OverheadCost),
		DefectPercent:   cloneMessage(in.DefectPercent),
		Currency:        in.Currency,
		Notes:           in.Notes,
		TargetMarginPct: cloneMessage(in.TargetMarginPct),
	}
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneMessage[T proto.Message](in T) T {
	var zero T
	value := reflect.ValueOf(in)
	if !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return zero
	}
	return proto.Clone(in).(T)
}

func cloneMessages[T proto.Message](in []T) []T {
	out := make([]T, 0, len(in))
	for _, item := range in {
		out = append(out, cloneMessage(item))
	}
	return out
}
