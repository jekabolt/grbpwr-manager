package dto

import (
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Decimal bounds for the Phase 3 production/costing columns.
const (
	maxVarchar128 = 128

	costMaxFrac = 2 // cost articles DECIMAL(12,2)
	costLimit   = 10_000_000_000
	markupFrac  = 3 // markup_multiplier DECIMAL(6,3)
	markupLimit = 1_000
	weightFrac  = 3 // weight DECIMAL(8,3)
	weightLimit = 100_000
	stitchFrac  = 2 // stitches_per_cm DECIMAL(5,2)
	stitchLimit = 1_000
)

var techCardLabelTypePbToEntity = map[pb_common.TechCardLabelType]entity.TechCardLabelType{
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_MAIN:    entity.LabelTypeMain,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_SIZE:    entity.LabelTypeSize,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_CARE:    entity.LabelTypeCare,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_ORIGIN:  entity.LabelTypeOrigin,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_FLAG:    entity.LabelTypeFlag,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_HANGTAG: entity.LabelTypeHangtag,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_BARCODE: entity.LabelTypeBarcode,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_SPECIAL: entity.LabelTypeSpecial,
}

var techCardLabelTypeEntityToPb = func() map[entity.TechCardLabelType]pb_common.TechCardLabelType {
	m := make(map[entity.TechCardLabelType]pb_common.TechCardLabelType, len(techCardLabelTypePbToEntity))
	for k, v := range techCardLabelTypePbToEntity {
		m[v] = k
	}
	return m
}()

// --- parse pb -> entity ---

func parseTechCardConstruction(pb *pb_common.TechCardConstruction) (*entity.TechCardConstruction, error) {
	if pb == nil {
		return nil, nil
	}
	for _, c := range []struct {
		field string
		val   string
		max   int
	}{
		{"construction main_stitch_type", pb.MainStitchType, maxVarchar255},
		{"construction stitch_density", pb.StitchDensity, maxVarchar64},
		{"construction overlock_threads", pb.OverlockThreads, maxVarchar32},
		{"construction seam_allowances", pb.SeamAllowances, maxVarchar255},
		{"construction hem_finish", pb.HemFinish, maxVarchar255},
		{"construction pressing", pb.Pressing, maxVarchar255},
		{"construction machine_class", pb.MachineClass, maxVarchar255},
	} {
		if len(c.val) > c.max {
			return nil, fmt.Errorf("%s must be at most %d characters", c.field, c.max)
		}
	}
	if pb.LabourRateCurrency != "" && len(pb.LabourRateCurrency) != maxCurrency {
		return nil, fmt.Errorf("construction labour_rate_currency must be a 3-letter ISO 4217 code")
	}
	labourRate, err := nullDecimalFromPb(pb.LabourRate)
	if err != nil {
		return nil, fmt.Errorf("construction labour_rate: %w", err)
	}
	if err := validateDecimalScale(labourRate, "construction labour_rate", bomPriceMaxFrac, bomPriceLimit); err != nil {
		return nil, err
	}
	return &entity.TechCardConstruction{
		MainStitchType:     nullStringFromPb(pb.MainStitchType),
		StitchDensity:      nullStringFromPb(pb.StitchDensity),
		OverlockThreads:    nullStringFromPb(pb.OverlockThreads),
		SeamAllowances:     nullStringFromPb(pb.SeamAllowances),
		HemFinish:          nullStringFromPb(pb.HemFinish),
		Pressing:           nullStringFromPb(pb.Pressing),
		MachineClass:       nullStringFromPb(pb.MachineClass),
		Notes:              nullStringFromPb(pb.Notes),
		LabourRate:         labourRate,
		LabourRateCurrency: nullStringFromPb(pb.LabourRateCurrency),
	}, nil
}

func parseTechCardOperations(pbs []*pb_common.TechCardOperation) ([]entity.TechCardOperation, error) {
	out := make([]entity.TechCardOperation, 0, len(pbs))
	for _, o := range pbs {
		if o.Node == "" {
			return nil, fmt.Errorf("operation node is required")
		}
		if len(o.Node) > maxVarchar255 || len(o.SeamType) > maxVarchar255 || len(o.Thread) > maxVarchar255 {
			return nil, fmt.Errorf("operation node/seam_type/thread must be at most %d characters", maxVarchar255)
		}
		if len(o.TopstitchWidth) > maxVarchar64 || len(o.Machine) > maxVarchar64 ||
			len(o.SeamAllowance) > maxVarchar64 || len(o.Needle) > maxVarchar64 {
			return nil, fmt.Errorf("operation topstitch_width/machine/seam_allowance/needle must be at most %d characters", maxVarchar64)
		}
		if o.OperationNumber < 0 {
			return nil, fmt.Errorf("operation operation_number must not be negative")
		}
		stitches, err := nullDecimalFromPb(o.StitchesPerCm)
		if err != nil {
			return nil, fmt.Errorf("operation stitches_per_cm: %w", err)
		}
		if err := validateDecimalScale(stitches, "operation stitches_per_cm", stitchFrac, stitchLimit); err != nil {
			return nil, err
		}
		timeNorm, err := nullDecimalFromPb(o.TimeNorm)
		if err != nil {
			return nil, fmt.Errorf("operation time_norm: %w", err)
		}
		if err := validateDecimalScale(timeNorm, "operation time_norm", 3, 10_000); err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardOperation{
			OperationNumber: nullInt32FromPb(o.OperationNumber),
			Node:            o.Node,
			Description:     nullStringFromPb(o.Description),
			SeamType:        nullStringFromPb(o.SeamType),
			Machine:         nullStringFromPb(o.Machine),
			StitchesPerCm:   stitches,
			TopstitchWidth:  nullStringFromPb(o.TopstitchWidth),
			SeamAllowance:   nullStringFromPb(o.SeamAllowance),
			Thread:          nullStringFromPb(o.Thread),
			Needle:          nullStringFromPb(o.Needle),
			TimeNorm:        timeNorm,
			Note:            nullStringFromPb(o.Note),
		})
	}
	return out, nil
}

func parseTechCardLabels(pbs []*pb_common.TechCardLabel) ([]entity.TechCardLabel, error) {
	out := make([]entity.TechCardLabel, 0, len(pbs))
	for _, l := range pbs {
		lt, ok := techCardLabelTypePbToEntity[l.LabelType]
		if !ok {
			return nil, fmt.Errorf("label label_type is required and must be valid")
		}
		if len(l.Content) > maxVarchar255 || len(l.Placement) > maxVarchar255 || len(l.Attachment) > maxVarchar255 {
			return nil, fmt.Errorf("label content/placement/attachment must be at most %d characters", maxVarchar255)
		}
		if len(l.Size) > maxVarchar64 {
			return nil, fmt.Errorf("label size must be at most %d characters", maxVarchar64)
		}
		out = append(out, entity.TechCardLabel{
			LabelType:  lt,
			Content:    nullStringFromPb(l.Content),
			Placement:  nullStringFromPb(l.Placement),
			Attachment: nullStringFromPb(l.Attachment),
			Size:       nullStringFromPb(l.Size),
			Note:       nullStringFromPb(l.Note),
		})
	}
	return out, nil
}

func parseTechCardPackaging(pb *pb_common.TechCardPackaging) (*entity.TechCardPackaging, error) {
	if pb == nil {
		return nil, nil
	}
	for _, c := range []struct {
		field string
		val   string
		max   int
	}{
		{"packaging folding_method", pb.FoldingMethod, maxVarchar255},
		{"packaging polybag", pb.Polybag, maxVarchar255},
		{"packaging bag_sticker", pb.BagSticker, maxVarchar255},
		{"packaging inserts", pb.Inserts, maxVarchar255},
		{"packaging box_marking", pb.BoxMarking, maxVarchar255},
		{"packaging box_dimensions", pb.BoxDimensions, maxVarchar128},
	} {
		if len(c.val) > c.max {
			return nil, fmt.Errorf("%s must be at most %d characters", c.field, c.max)
		}
	}
	if pb.UnitsPerBox < 0 {
		return nil, fmt.Errorf("packaging units_per_box must not be negative")
	}
	weightNet, err := nullDecimalFromPb(pb.WeightNet)
	if err != nil {
		return nil, fmt.Errorf("packaging weight_net: %w", err)
	}
	if err := validateDecimalScale(weightNet, "packaging weight_net", weightFrac, weightLimit); err != nil {
		return nil, err
	}
	weightGross, err := nullDecimalFromPb(pb.WeightGross)
	if err != nil {
		return nil, fmt.Errorf("packaging weight_gross: %w", err)
	}
	if err := validateDecimalScale(weightGross, "packaging weight_gross", weightFrac, weightLimit); err != nil {
		return nil, err
	}
	return &entity.TechCardPackaging{
		FoldingMethod: nullStringFromPb(pb.FoldingMethod),
		Polybag:       nullStringFromPb(pb.Polybag),
		BagSticker:    nullStringFromPb(pb.BagSticker),
		Inserts:       nullStringFromPb(pb.Inserts),
		UnitsPerBox:   nullInt32FromPb(pb.UnitsPerBox),
		BoxMarking:    nullStringFromPb(pb.BoxMarking),
		BoxDimensions: nullStringFromPb(pb.BoxDimensions),
		WeightNet:     weightNet,
		WeightGross:   weightGross,
		Notes:         nullStringFromPb(pb.Notes),
	}, nil
}

func parseTechCardCosting(pb *pb_common.TechCardCosting) (*entity.TechCardCosting, error) {
	if pb == nil {
		return nil, nil
	}
	if pb.Currency != "" && len(pb.Currency) != maxCurrency {
		return nil, fmt.Errorf("costing currency must be a 3-letter ISO 4217 code")
	}
	cost := func(d *pb_decimal.Decimal, field string) (decimal.NullDecimal, error) {
		nd, err := nullDecimalFromPb(d)
		if err != nil {
			return nd, fmt.Errorf("costing %s: %w", field, err)
		}
		return nd, validateDecimalScale(nd, "costing "+field, costMaxFrac, costLimit)
	}
	cmt, err := cost(pb.CmtCost, "cmt_cost")
	if err != nil {
		return nil, err
	}
	hardware, err := cost(pb.HardwareCost, "hardware_cost")
	if err != nil {
		return nil, err
	}
	packaging, err := cost(pb.PackagingCost, "packaging_cost")
	if err != nil {
		return nil, err
	}
	logistics, err := cost(pb.LogisticsCost, "logistics_cost")
	if err != nil {
		return nil, err
	}
	overhead, err := cost(pb.OverheadCost, "overhead_cost")
	if err != nil {
		return nil, err
	}
	wholesale, err := cost(pb.WholesalePrice, "wholesale_price")
	if err != nil {
		return nil, err
	}
	retail, err := cost(pb.RetailPrice, "retail_price")
	if err != nil {
		return nil, err
	}
	defect, err := nullDecimalFromPb(pb.DefectPercent)
	if err != nil {
		return nil, fmt.Errorf("costing defect_percent: %w", err)
	}
	if err := validateDecimalScale(defect, "costing defect_percent", costMaxFrac, 1_000); err != nil {
		return nil, err
	}
	if defect.Valid && defect.Decimal.GreaterThan(decimal.NewFromInt(100)) {
		return nil, fmt.Errorf("costing defect_percent must be between 0 and 100")
	}
	markup, err := nullDecimalFromPb(pb.MarkupMultiplier)
	if err != nil {
		return nil, fmt.Errorf("costing markup_multiplier: %w", err)
	}
	if err := validateDecimalScale(markup, "costing markup_multiplier", markupFrac, markupLimit); err != nil {
		return nil, err
	}
	return &entity.TechCardCosting{
		CmtCost:          cmt,
		HardwareCost:     hardware,
		PackagingCost:    packaging,
		LogisticsCost:    logistics,
		OverheadCost:     overhead,
		DefectPercent:    defect,
		MarkupMultiplier: markup,
		WholesalePrice:   wholesale,
		RetailPrice:      retail,
		Currency:         nullStringFromPb(pb.Currency),
		Notes:            nullStringFromPb(pb.Notes),
	}, nil
}

// --- emit entity -> pb ---

func techCardConstructionToPb(c *entity.TechCardConstruction) *pb_common.TechCardConstruction {
	if c == nil {
		return nil
	}
	return &pb_common.TechCardConstruction{
		MainStitchType:     pbStringFromNull(c.MainStitchType),
		StitchDensity:      pbStringFromNull(c.StitchDensity),
		OverlockThreads:    pbStringFromNull(c.OverlockThreads),
		SeamAllowances:     pbStringFromNull(c.SeamAllowances),
		HemFinish:          pbStringFromNull(c.HemFinish),
		Pressing:           pbStringFromNull(c.Pressing),
		MachineClass:       pbStringFromNull(c.MachineClass),
		Notes:              pbStringFromNull(c.Notes),
		LabourRate:         pbDecimalFromNull(c.LabourRate),
		LabourRateCurrency: pbStringFromNull(c.LabourRateCurrency),
	}
}

func techCardOperationsToPb(ops []entity.TechCardOperation) []*pb_common.TechCardOperation {
	out := make([]*pb_common.TechCardOperation, 0, len(ops))
	for _, o := range ops {
		out = append(out, &pb_common.TechCardOperation{
			OperationNumber: pbInt32FromNull(o.OperationNumber),
			Node:            o.Node,
			Description:     pbStringFromNull(o.Description),
			SeamType:        pbStringFromNull(o.SeamType),
			Machine:         pbStringFromNull(o.Machine),
			StitchesPerCm:   pbDecimalFromNull(o.StitchesPerCm),
			TopstitchWidth:  pbStringFromNull(o.TopstitchWidth),
			SeamAllowance:   pbStringFromNull(o.SeamAllowance),
			Thread:          pbStringFromNull(o.Thread),
			Needle:          pbStringFromNull(o.Needle),
			TimeNorm:        pbDecimalFromNull(o.TimeNorm),
			Note:            pbStringFromNull(o.Note),
		})
	}
	return out
}

// --- issues (Phase 3.5b) ---

var techCardIssueSeverityPbToEntity = map[pb_common.TechCardIssueSeverity]entity.TechCardIssueSeverity{
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_LOW:    entity.IssueSeverityLow,
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_MEDIUM: entity.IssueSeverityMedium,
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_HIGH:   entity.IssueSeverityHigh,
}
var techCardIssueSeverityEntityToPb = func() map[entity.TechCardIssueSeverity]pb_common.TechCardIssueSeverity {
	m := make(map[entity.TechCardIssueSeverity]pb_common.TechCardIssueSeverity, len(techCardIssueSeverityPbToEntity))
	for k, v := range techCardIssueSeverityPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardIssueStatusPbToEntity = map[pb_common.TechCardIssueStatus]entity.TechCardIssueStatus{
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_OPEN:     entity.IssueStatusOpen,
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_RESOLVED: entity.IssueStatusResolved,
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_WONTFIX:  entity.IssueStatusWontfix,
}
var techCardIssueStatusEntityToPb = func() map[entity.TechCardIssueStatus]pb_common.TechCardIssueStatus {
	m := make(map[entity.TechCardIssueStatus]pb_common.TechCardIssueStatus, len(techCardIssueStatusPbToEntity))
	for k, v := range techCardIssueStatusPbToEntity {
		m[v] = k
	}
	return m
}()

func parseTechCardIssues(pbs []*pb_common.TechCardIssue) ([]entity.TechCardIssue, error) {
	out := make([]entity.TechCardIssue, 0, len(pbs))
	for _, i := range pbs {
		if i.Description == "" {
			return nil, fmt.Errorf("issue description is required")
		}
		if i.OperationNumber < 0 || i.CalloutNumber < 0 {
			return nil, fmt.Errorf("issue operation_number/callout_number must not be negative")
		}
		if len(i.RaisedBy) > maxVarchar255 {
			return nil, fmt.Errorf("issue raised_by must be at most %d characters", maxVarchar255)
		}
		severity := entity.IssueSeverityMedium
		if i.Severity != pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_UNKNOWN {
			s, ok := techCardIssueSeverityPbToEntity[i.Severity]
			if !ok {
				return nil, fmt.Errorf("unknown issue severity: %v", i.Severity)
			}
			severity = s
		}
		st := entity.IssueStatusOpen
		if i.Status != pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_UNKNOWN {
			v, ok := techCardIssueStatusPbToEntity[i.Status]
			if !ok {
				return nil, fmt.Errorf("unknown issue status: %v", i.Status)
			}
			st = v
		}
		out = append(out, entity.TechCardIssue{
			OperationNumber: nullInt32FromPb(i.OperationNumber),
			CalloutNumber:   nullInt32FromPb(i.CalloutNumber),
			RaisedBy:        nullStringFromPb(i.RaisedBy),
			Severity:        severity,
			Status:          st,
			Description:     i.Description,
			ResolutionNote:  nullStringFromPb(i.ResolutionNote),
		})
	}
	return out, nil
}

func techCardIssuesToPb(issues []entity.TechCardIssue) []*pb_common.TechCardIssue {
	out := make([]*pb_common.TechCardIssue, 0, len(issues))
	for _, i := range issues {
		out = append(out, &pb_common.TechCardIssue{
			OperationNumber: pbInt32FromNull(i.OperationNumber),
			CalloutNumber:   pbInt32FromNull(i.CalloutNumber),
			RaisedBy:        pbStringFromNull(i.RaisedBy),
			Severity:        techCardIssueSeverityEntityToPb[i.Severity],
			Status:          techCardIssueStatusEntityToPb[i.Status],
			Description:     i.Description,
			ResolutionNote:  pbStringFromNull(i.ResolutionNote),
		})
	}
	return out
}

func techCardLabelsToPb(labels []entity.TechCardLabel) []*pb_common.TechCardLabel {
	out := make([]*pb_common.TechCardLabel, 0, len(labels))
	for _, l := range labels {
		out = append(out, &pb_common.TechCardLabel{
			LabelType:  techCardLabelTypeEntityToPb[l.LabelType],
			Content:    pbStringFromNull(l.Content),
			Placement:  pbStringFromNull(l.Placement),
			Attachment: pbStringFromNull(l.Attachment),
			Size:       pbStringFromNull(l.Size),
			Note:       pbStringFromNull(l.Note),
		})
	}
	return out
}

func techCardPackagingToPb(p *entity.TechCardPackaging) *pb_common.TechCardPackaging {
	if p == nil {
		return nil
	}
	return &pb_common.TechCardPackaging{
		FoldingMethod: pbStringFromNull(p.FoldingMethod),
		Polybag:       pbStringFromNull(p.Polybag),
		BagSticker:    pbStringFromNull(p.BagSticker),
		Inserts:       pbStringFromNull(p.Inserts),
		UnitsPerBox:   pbInt32FromNull(p.UnitsPerBox),
		BoxMarking:    pbStringFromNull(p.BoxMarking),
		BoxDimensions: pbStringFromNull(p.BoxDimensions),
		WeightNet:     pbDecimalFromNull(p.WeightNet),
		WeightGross:   pbDecimalFromNull(p.WeightGross),
		Notes:         pbStringFromNull(p.Notes),
	}
}

// techCardCostingToPb emits the stored costing articles plus the computed
// materials rollup and total. Returns nil when no costing row exists.
func techCardCostingToPb(tc *entity.TechCard) *pb_common.TechCardCosting {
	if tc.Costing == nil {
		return nil
	}
	c := tc.Costing
	materialsTotal, materialsCost := bomMaterialsRollup(tc.BomItems, c.Currency)
	out := &pb_common.TechCardCosting{
		CmtCost:          pbDecimalFromNull(c.CmtCost),
		HardwareCost:     pbDecimalFromNull(c.HardwareCost),
		PackagingCost:    pbDecimalFromNull(c.PackagingCost),
		LogisticsCost:    pbDecimalFromNull(c.LogisticsCost),
		OverheadCost:     pbDecimalFromNull(c.OverheadCost),
		DefectPercent:    pbDecimalFromNull(c.DefectPercent),
		MarkupMultiplier: pbDecimalFromNull(c.MarkupMultiplier),
		WholesalePrice:   pbDecimalFromNull(c.WholesalePrice),
		RetailPrice:      pbDecimalFromNull(c.RetailPrice),
		Currency:         pbStringFromNull(c.Currency),
		Notes:            pbStringFromNull(c.Notes),
		MaterialsTotal:   materialsTotal,
		MaterialsCost:    pbDecimalFromDecimal(materialsCost.Round(costMaxFrac)),
	}

	// total_cost = (materials in costing currency + cmt+hardware+packaging+
	// logistics+overhead) grossed up by the defect/reserve %. Single-currency,
	// best-effort: cross-currency materials are surfaced in materials_total.
	total := materialsCost
	for _, d := range []decimal.NullDecimal{c.CmtCost, c.HardwareCost, c.PackagingCost, c.LogisticsCost, c.OverheadCost} {
		if d.Valid {
			total = total.Add(d.Decimal)
		}
	}
	if c.DefectPercent.Valid {
		total = total.Mul(decimal.NewFromInt(1).Add(c.DefectPercent.Decimal.Div(decimal.NewFromInt(100))))
	}
	out.TotalCost = pbDecimalFromDecimal(total.Round(costMaxFrac))

	// Flag when a BOM line is priced in a currency other than the costing currency:
	// such lines are surfaced in materials_total but excluded from total_cost (no
	// auto-conversion), so the total is not the full landed cost.
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if b.Currency.Valid && b.Currency.String != "" && b.Currency.String != costingCcy && b.LineTotal().Valid {
			out.HasUnconvertedCurrencies = true
			break
		}
	}

	// total_sam = Σ(operation time_norm); labour_cost = total_sam × labour_rate.
	totalSam := decimal.Zero
	for i := range tc.Operations {
		if tc.Operations[i].TimeNorm.Valid {
			totalSam = totalSam.Add(tc.Operations[i].TimeNorm.Decimal)
		}
	}
	if totalSam.IsPositive() {
		out.TotalSam = pbDecimalFromDecimal(totalSam.Round(3))
		if tc.Construction != nil && tc.Construction.LabourRate.Valid {
			out.LabourCost = pbDecimalFromDecimal(totalSam.Mul(tc.Construction.LabourRate.Decimal).Round(costMaxFrac))
		}
	}
	return out
}

// bomMaterialsRollup sums BOM line totals grouped by the line's currency (first-
// seen order preserved), and returns the subtotal foldable into costingCurrency
// (matching-currency lines plus currency-less lines).
func bomMaterialsRollup(items []entity.TechCardBomItem, costingCurrency sql.NullString) ([]*pb_common.TechCardCostLine, decimal.Decimal) {
	byCcy := map[string]decimal.Decimal{}
	order := make([]string, 0)
	for i := range items {
		lt := items[i].LineTotal()
		if !lt.Valid {
			continue
		}
		ccy := ""
		if items[i].Currency.Valid {
			ccy = items[i].Currency.String
		}
		if _, ok := byCcy[ccy]; !ok {
			order = append(order, ccy)
		}
		byCcy[ccy] = byCcy[ccy].Add(lt.Decimal)
	}

	lines := make([]*pb_common.TechCardCostLine, 0, len(order))
	for _, ccy := range order {
		lines = append(lines, &pb_common.TechCardCostLine{
			Currency: ccy,
			Amount:   pbDecimalFromDecimal(byCcy[ccy].Round(costMaxFrac)),
		})
	}

	target := ""
	if costingCurrency.Valid {
		target = costingCurrency.String
	}
	materialsCost := decimal.Zero
	if v, ok := byCcy[target]; ok {
		materialsCost = materialsCost.Add(v)
	}
	if target != "" {
		if v, ok := byCcy[""]; ok { // currency-less lines fold into the costing currency
			materialsCost = materialsCost.Add(v)
		}
	}
	return lines, materialsCost
}
