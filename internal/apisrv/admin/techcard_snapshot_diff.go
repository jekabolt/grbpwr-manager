package admin

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
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
		"output_material_id", "aux_subtype", "aux_made_by", "target_drop_date", "base_model_id")
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
	if after.GetAuxMadeBy() == pb_common.AuxMadeBy_AUX_MADE_BY_UNKNOWN &&
		after.GetPurpose() == pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY {
		after.AuxMadeBy = before.GetAuxMadeBy() // aux: absent keeps the stored value
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
	// 0452: "" is vertical on the wire, and a bundle without packed_media_ids sends the cover alone.
	for _, cl := range []*pb_common.TechCardCareLabel{before.GetCareLabel(), after.GetCareLabel()} {
		if cl != nil && cl.Orientation == "" {
			cl.Orientation = entity.CareLabelVertical
		}
	}
	normalizePackedPhotosDiff(before.GetPackaging(), after.GetPackaging())
	if after.PieceDxfAliases == nil {
		after.PieceDxfAliases = cloneMessage(before.PieceDxfAliases)
	}
	materializePreservedGarmentLabelGeometry(before, after)
	materializePieceFusingBase(before, after)
	if !after.GetLabelsAware() {
		after.GarmentLabels = cloneMessages(before.GetGarmentLabels())
		after.PackagingItems = cloneMessages(before.GetPackagingItems())
	}
	for _, item := range before.GetPackagingItems() {
		stripPackagingItemReadOnly(item)
	}
	for _, item := range after.GetPackagingItems() {
		stripPackagingItemReadOnly(item)
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

// materializePreservedGarmentLabelGeometry mirrors the pre-0454 compatibility carry on the write
// path, so an old tab does not report labels and operations changed merely because it cannot echo
// the new fields.
func materializePreservedGarmentLabelGeometry(before, after *pb_common.TechCardInsert) {
	allLabelsOmitted := true
	for _, label := range after.GetGarmentLabels() {
		if strings.TrimSpace(label.GetLineKey()) != "" {
			allLabelsOmitted = false
			break
		}
	}
	allOperationsOmitted := true
	for _, op := range after.GetOperations() {
		if len(op.GetLabelKeys()) > 0 {
			allOperationsOmitted = false
			break
		}
	}
	if !after.GetLabelsAware() {
		after.GarmentLabels = cloneMessages(before.GetGarmentLabels())
	}
	byKey := make(map[string][]*pb_common.TechCardGarmentLabel, len(before.GetGarmentLabels()))
	for _, label := range before.GetGarmentLabels() {
		if label != nil {
			key := strings.ToLower(strings.TrimSpace(label.GetKey()))
			byKey[key] = append(byKey[key], label)
		}
	}
	for _, label := range after.GetGarmentLabels() {
		if label == nil || strings.TrimSpace(label.GetLineKey()) != "" {
			continue
		}
		matches := byKey[strings.ToLower(strings.TrimSpace(label.GetKey()))]
		if len(matches) != 1 {
			continue
		}
		label.LineKey = matches[0].GetLineKey()
		label.WidthMm = cloneMessage(matches[0].GetWidthMm())
		label.HeightMm = cloneMessage(matches[0].GetHeightMm())
		label.Fold = matches[0].GetFold()
	}
	materializeGarmentLabelFoldLines(before, after)
	materializeGarmentLabelPiece(before, after)
	if !allLabelsOmitted || !allOperationsOmitted {
		return
	}
	live := make(map[string]bool, len(after.GetGarmentLabels()))
	for _, label := range after.GetGarmentLabels() {
		if key := strings.ToUpper(strings.TrimSpace(label.GetLineKey())); key != "" {
			live[key] = true
		}
	}
	storedOps := make(map[int32]*pb_common.TechCardOperation, len(before.GetOperations()))
	for _, op := range before.GetOperations() {
		if op != nil {
			storedOps[op.GetOperationNumber()] = op
		}
	}
	for _, op := range after.GetOperations() {
		if op == nil {
			continue
		}
		prev := storedOps[op.GetOperationNumber()]
		if prev == nil {
			continue
		}
		seen := map[string]bool{}
		for _, raw := range prev.GetLabelKeys() {
			key := strings.ToUpper(strings.TrimSpace(raw))
			if key != "" && live[key] && !seen[key] {
				seen[key] = true
				op.LabelKeys = append(op.LabelKeys, key)
			}
		}
	}
}

// materializePieceFusingBase mirrors the 0468 carry of the markup's staleness key: an absent key takes
// the stored key of the piece with the same line key, and a present empty key is the read form of "no
// key" (absent). So an older tab's save, or a clear of a key that was never stored, is not a pieces
// change; a real new key is.
func materializePieceFusingBase(before, after *pb_common.TechCardInsert) {
	empty := func(base *pb_common.TechCardPieceFusingBase) bool {
		return base.GetBaseSizeId() == 0 && base.GetContourSignature() == ""
	}
	stored := make(map[string]*pb_common.TechCardPieceFusingBase, len(before.GetPieces()))
	for _, piece := range before.GetPieces() {
		if piece == nil {
			continue
		}
		if empty(piece.GetFusingBase()) {
			piece.FusingBase = nil
		}
		if key := strings.TrimSpace(piece.GetLineKey()); key != "" {
			stored[key] = piece.GetFusingBase()
		}
	}
	for _, piece := range after.GetPieces() {
		if piece == nil {
			continue
		}
		if piece.FusingBase == nil {
			piece.FusingBase = cloneMessage(stored[strings.TrimSpace(piece.GetLineKey())])
		} else if empty(piece.FusingBase) {
			piece.FusingBase = nil
		}
	}
}

// materializeGarmentLabelFoldLines mirrors dto.CarryStoredGarmentLabelGeometry for fold_lines_mm and stitch_lines_mm:
// an absent field takes the stored lines of the label with the same line key, and every list is put
// in the form the save writes, so only a real change of lines reads as a labels change.
func materializeGarmentLabelFoldLines(before, after *pb_common.TechCardInsert) {
	byLineKey := make(map[string]*pb_common.TechCardGarmentLabel, len(before.GetGarmentLabels()))
	for _, label := range before.GetGarmentLabels() {
		if label == nil {
			continue
		}
		// A snapshot taken before 0460 has no field: it had no lines.
		if label.FoldLinesMm == nil {
			label.FoldLinesMm = new(string)
		}
		if label.StitchLinesMm == nil {
			label.StitchLinesMm = new(string)
		}
		// A snapshot taken before 0466 has neither: lines along the height, one page.
		if label.FoldAxis == nil {
			label.FoldAxis = new(string)
		}
		if label.Pages == nil {
			one := int32(1)
			label.Pages = &one
		}
		if key := strings.ToUpper(strings.TrimSpace(label.GetLineKey())); key != "" {
			byLineKey[key] = label
		}
	}
	for _, label := range after.GetGarmentLabels() {
		if label == nil {
			continue
		}
		raw, rawStitch := label.GetFoldLinesMm(), label.GetStitchLinesMm()
		prev := byLineKey[strings.ToUpper(strings.TrimSpace(label.GetLineKey()))]
		if prev != nil && label.FoldLinesMm == nil {
			raw = prev.GetFoldLinesMm()
		}
		if prev != nil && label.StitchLinesMm == nil {
			rawStitch = prev.GetStitchLinesMm()
		}
		if prev != nil && label.FoldAxis == nil {
			label.FoldAxis = prev.FoldAxis
		}
		if prev != nil && label.Pages == nil {
			label.Pages = prev.Pages
		}
		axis := dto.NormalizeGarmentLabelFoldAxis(label.GetFoldAxis())
		label.FoldAxis = &axis
		pages := int32(dto.NormalizeGarmentLabelPages(int(label.GetPages())))
		label.Pages = &pages
		dim := func(v string) decimal.NullDecimal {
			if d, err := decimal.NewFromString(strings.TrimSpace(v)); err == nil {
				return decimal.NewNullDecimal(d)
			}
			return decimal.NullDecimal{}
		}
		height := dto.GarmentLabelLineLimit(axis, dim(label.GetWidthMm().GetValue()), dim(label.GetHeightMm().GetValue()))
		normalized := dto.NormalizeGarmentLabelFoldLines(raw, height)
		label.FoldLinesMm = &normalized
		normalizedStitch := dto.NormalizeGarmentLabelStitchLines(rawStitch, height)
		label.StitchLinesMm = &normalizedStitch
	}
}

// materializeGarmentLabelPiece mirrors the 0467 carry: an absent piece field takes the stored value of
// the label with the same line key, an absent label_on_piece keeps the stored prints, and both sides are
// put in the form the read returns (a snapshot taken before 0467 has no piece and no prints). Print ids
// are output-only and change on every save, so they are not compared.
func materializeGarmentLabelPiece(before, after *pb_common.TechCardInsert) {
	canon := func(d *pb_decimal.Decimal) *pb_decimal.Decimal {
		if d == nil || strings.TrimSpace(d.GetValue()) == "" {
			return &pb_decimal.Decimal{Value: "0"}
		}
		if v, err := decimal.NewFromString(strings.TrimSpace(d.GetValue())); err == nil {
			return &pb_decimal.Decimal{Value: v.Round(2).String()}
		}
		return d
	}
	readForm := func(label *pb_common.TechCardGarmentLabel) {
		if label.PieceBomItemId == nil {
			label.PieceBomItemId = new(int32)
		}
		label.PieceWidthMm, label.PieceHeightMm = canon(label.PieceWidthMm), canon(label.PieceHeightMm)
		label.PieceSeamMm = canon(label.PieceSeamMm)
		label.LabelXMm, label.LabelYMm = canon(label.LabelXMm), canon(label.LabelYMm)
		onPiece := dto.NormalizeGarmentLabelOnPiece(label.GetLabelOnPiece())
		label.LabelOnPiece = &onPiece
		for _, p := range label.GetPrints() {
			if p == nil {
				continue
			}
			p.Id = 0
			p.Method = strings.ToLower(strings.TrimSpace(p.GetMethod()))
			p.Name, p.Colour = strings.TrimSpace(p.GetName()), strings.TrimSpace(p.GetColour())
			p.WidthMm, p.HeightMm, p.XMm, p.YMm = canon(p.WidthMm), canon(p.HeightMm), canon(p.XMm), canon(p.YMm)
		}
	}
	byLineKey := make(map[string]*pb_common.TechCardGarmentLabel, len(before.GetGarmentLabels()))
	for _, label := range before.GetGarmentLabels() {
		if label == nil {
			continue
		}
		readForm(label)
		if key := strings.ToUpper(strings.TrimSpace(label.GetLineKey())); key != "" {
			byLineKey[key] = label
		}
	}
	for _, label := range after.GetGarmentLabels() {
		if label == nil {
			continue
		}
		if prev := byLineKey[strings.ToUpper(strings.TrimSpace(label.GetLineKey()))]; prev != nil {
			if label.PieceBomItemId == nil {
				label.PieceBomItemId = prev.PieceBomItemId
			}
			if label.PieceWidthMm == nil {
				label.PieceWidthMm = prev.PieceWidthMm
			}
			if label.PieceHeightMm == nil {
				label.PieceHeightMm = prev.PieceHeightMm
			}
			if label.PieceSeamMm == nil {
				label.PieceSeamMm = prev.PieceSeamMm
			}
			if label.LabelXMm == nil {
				label.LabelXMm = prev.LabelXMm
			}
			if label.LabelYMm == nil {
				label.LabelYMm = prev.LabelYMm
			}
			if label.LabelOnPiece == nil {
				label.LabelOnPiece = prev.LabelOnPiece
				label.Prints = cloneMessages(prev.GetPrints())
			}
		}
		readForm(label)
	}
}

// normalizePackedPhotosDiff puts both pack sheets in the form the save writes and the read returns
// (0452): the list wins and its first photo is the cover; a cover alone is a one-photo list, except
// that a cover equal to the stored one keeps the stored photos (entity.CarryStoredPackedPhotos).
func normalizePackedPhotosDiff(before, after *pb_common.TechCardPackaging) {
	readForm := func(p *pb_common.TechCardPackaging) {
		if p == nil {
			return
		}
		if len(p.PackedMediaIds) > 0 {
			p.PackedMediaId = p.PackedMediaIds[0]
		} else if p.PackedMediaId > 0 {
			p.PackedMediaIds = []int32{p.PackedMediaId}
		}
	}
	readForm(before)
	if after != nil && before != nil && len(after.PackedMediaIds) == 0 && after.PackedMediaId > 0 &&
		len(before.PackedMediaIds) > 1 && before.PackedMediaIds[0] == after.PackedMediaId {
		after.PackedMediaIds = slices.Clone(before.PackedMediaIds)
	}
	readForm(after)
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
	if after.ConsumptionPerUnit == nil {
		after.ConsumptionPerUnit = cloneMessage(before.ConsumptionPerUnit)
	}
}

// stripPackagingItemReadOnly cuts what a read adds to the pick, and puts the colourways in the order a
// read gives them: the list means a set, so a reorder is no change.
func stripPackagingItemReadOnly(item *pb_common.TechCardPackagingItem) {
	if item == nil {
		return
	}
	item.PickedName = ""
	item.PickedThumbnail = nil
	slices.Sort(item.ColorwayIds)
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
