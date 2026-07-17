package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// CostingFx carries the manual FX rates used to fold a tech card's multi-currency costing into
// the base currency for the *_base rollup. ToBase maps an UPPERCASE ISO currency to how many
// base-currency units one unit is worth; the base currency itself is implicitly 1. A zero
// value (Base == "") means "no base configured" — the *_base fields are then left unset.
type CostingFx struct {
	ToBase map[string]decimal.Decimal
	Base   string
}

// toBase converts amount from ccy into the base currency. An empty ccy is treated as the base
// currency (amounts with no currency are assumed already-base). Returns ok=false when no base
// is configured or the currency has no rate — the caller then leaves the base figure unset.
func (fx CostingFx) toBase(amount decimal.Decimal, ccy string) (decimal.Decimal, bool) {
	if fx.Base == "" {
		return decimal.Zero, false
	}
	if ccy == "" || strings.EqualFold(ccy, fx.Base) {
		return amount, true
	}
	r, ok := fx.ToBase[strings.ToUpper(ccy)]
	if !ok {
		return decimal.Zero, false
	}
	return amount.Mul(r), true
}

// Decimal bounds for the Phase 3 production/costing columns.
const (
	maxVarchar128 = 128

	costMaxFrac = 2 // cost articles DECIMAL(12,2)
	costLimit   = 10_000_000_000
	// weightGramsLimit caps packaging weight (INT grams). Generous — 1 tonne — so real parcels
	// (well above 750 g) are never rejected.
	weightGramsLimit = 1_000_000
	stitchFrac       = 2 // stitches_per_cm DECIMAL(5,2)
	stitchLimit      = 1_000
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
	return &entity.TechCardConstruction{
		MainStitchType:  nullStringFromPb(pb.MainStitchType),
		StitchDensity:   nullStringFromPb(pb.StitchDensity),
		OverlockThreads: nullStringFromPb(pb.OverlockThreads),
		SeamAllowances:  nullStringFromPb(pb.SeamAllowances),
		HemFinish:       nullStringFromPb(pb.HemFinish),
		Pressing:        nullStringFromPb(pb.Pressing),
		MachineClass:    nullStringFromPb(pb.MachineClass),
		Notes:           nullStringFromPb(pb.Notes),
	}, nil
}

var techCardOperationTypePbToEntity = map[pb_common.TechCardOperationType]entity.TechCardOperationType{
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH:    entity.OpTypeLockstitch,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_DOUBLE_NEEDLE: entity.OpTypeDoubleNeedle,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OVERLOCK:      entity.OpTypeOverlock,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_COVERSTITCH:   entity.OpTypeCoverstitch,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_CHAINSTITCH:   entity.OpTypeChainstitch,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BLINDHEM:      entity.OpTypeBlindhem,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BARTACK:       entity.OpTypeBartack,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTONHOLE:    entity.OpTypeButtonhole,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTON_ATTACH: entity.OpTypeButtonAttach,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING:        entity.OpTypeFusing,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK:      entity.OpTypeHandwork,
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OTHER:         entity.OpTypeOther,
}
var techCardOperationTypeEntityToPb = func() map[entity.TechCardOperationType]pb_common.TechCardOperationType {
	m := make(map[entity.TechCardOperationType]pb_common.TechCardOperationType, len(techCardOperationTypePbToEntity))
	for k, v := range techCardOperationTypePbToEntity {
		m[v] = k
	}
	return m
}()

var techCardConstructionZonePbToEntity = map[pb_common.TechCardConstructionZone]entity.TechCardConstructionZone{
	pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_OUTER:       entity.ZoneOuter,
	pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_LINING:      entity.ZoneLining,
	pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_INTERLINING: entity.ZoneInterlining,
	pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_OTHER:       entity.ZoneOther,
}
var techCardConstructionZoneEntityToPb = func() map[entity.TechCardConstructionZone]pb_common.TechCardConstructionZone {
	m := make(map[entity.TechCardConstructionZone]pb_common.TechCardConstructionZone, len(techCardConstructionZonePbToEntity))
	for k, v := range techCardConstructionZonePbToEntity {
		m[v] = k
	}
	return m
}()

// parseTechCardOperations validates and converts operations. calloutNumbers is the
// set of TechCardCallout.number values in the same payload (so an operation's
// callout_number can be range-checked) and bomItemCount is the number of submitted
// bom_items (so an operation's bom_item_index can be range-checked).
func parseTechCardOperations(pbs []*pb_common.TechCardOperation, calloutNumbers map[int]bool, bomItemCount int) ([]entity.TechCardOperation, error) {
	out := make([]entity.TechCardOperation, 0, len(pbs))
	for i, o := range pbs {
		if o.Node == "" {
			return nil, fmt.Errorf("operation node is required")
		}
		if len(o.Node) > maxVarchar255 || len(o.SeamType) > maxVarchar255 || len(o.Thread) > maxVarchar255 {
			return nil, fmt.Errorf("operation node/seam_type/thread must be at most %d characters", maxVarchar255)
		}
		if len(o.TopstitchWidth) > maxVarchar64 || len(o.Machine) > maxVarchar64 ||
			len(o.SeamAllowance) > maxVarchar64 || len(o.Needle) > maxVarchar64 || len(o.Attachment) > maxVarchar64 {
			return nil, fmt.Errorf("operation topstitch_width/machine/seam_allowance/needle/attachment must be at most %d characters", maxVarchar64)
		}
		if len(o.Placement) > maxVarchar255 {
			return nil, fmt.Errorf("operation placement must be at most %d characters", maxVarchar255)
		}
		opType := entity.OpTypeUnknown
		if o.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN {
			t, ok := techCardOperationTypePbToEntity[o.OperationType]
			if !ok {
				return nil, fmt.Errorf("unknown operation operation_type: %v", o.OperationType)
			}
			opType = t
		}
		zone := entity.ZoneUnknown
		if o.Zone != pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_UNKNOWN {
			z, ok := techCardConstructionZonePbToEntity[o.Zone]
			if !ok {
				return nil, fmt.Errorf("unknown operation zone: %v", o.Zone)
			}
			zone = z
		}
		// bom_item_index uses proto3 explicit presence: a nil pointer means "no
		// material" (so index 0 stays a valid reference), a set value must be in range.
		var bomItemIndex sql.NullInt32
		if o.BomItemIndex != nil {
			idx := *o.BomItemIndex
			if idx < 0 || int(idx) >= bomItemCount {
				return nil, fmt.Errorf("operation bom_item_index %d out of range (have %d bom_items)", idx, bomItemCount)
			}
			bomItemIndex = sql.NullInt32{Int32: idx, Valid: true}
		}
		if o.CalloutNumber < 0 {
			return nil, fmt.Errorf("operation callout_number must not be negative")
		}
		if o.CalloutNumber > 0 && !calloutNumbers[int(o.CalloutNumber)] {
			return nil, fmt.Errorf("operation callout_number %d does not match any callout", o.CalloutNumber)
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
			// operation_number is server-assigned = (position+1)*10 («оп. 10, 20, …»);
			// any client value is ignored (plan §4). Reorder shifts numbers (Q6).
			OperationNumber: sql.NullInt32{Int32: int32((i + 1) * 10), Valid: true},
			Node:            o.Node,
			Description:     nullStringFromPb(o.Description),
			SeamType:        nullStringFromPb(o.SeamType),
			Machine:         nullStringFromPb(o.Machine),
			StitchesPerCm:   stitches,
			TopstitchWidth:  nullStringFromPb(o.TopstitchWidth),
			SeamAllowance:   nullStringFromPb(o.SeamAllowance),
			Thread:          nullStringFromPb(o.Thread),
			Needle:          nullStringFromPb(o.Needle),
			Attachment:      nullStringFromPb(o.Attachment),
			TimeNorm:        timeNorm,
			Note:            nullStringFromPb(o.Note),
			OperationType:   opType,
			Zone:            zone,
			BomLineKey:      strings.TrimSpace(o.BomLineKey), // stable ref (WS3 follow-up); store prefers it over the index
			BomItemIndex:    bomItemIndex,
			CalloutNumber:   nullInt32FromPb(o.CalloutNumber),
			Placement:       normalizedPlacementNull(o.Placement),
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
			BomItemId:  nullInt32FromPb(l.BomItemId), // §2.8 link to the physical label material's BOM line
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
	if pb.WeightNetGrams < 0 || pb.WeightGrossGrams < 0 {
		return nil, fmt.Errorf("packaging weight must not be negative")
	}
	if pb.WeightNetGrams > weightGramsLimit || pb.WeightGrossGrams > weightGramsLimit {
		return nil, fmt.Errorf("packaging weight exceeds max %d grams", weightGramsLimit)
	}
	return &entity.TechCardPackaging{
		FoldingMethod:    nullStringFromPb(pb.FoldingMethod),
		Polybag:          nullStringFromPb(pb.Polybag),
		BagSticker:       nullStringFromPb(pb.BagSticker),
		Inserts:          nullStringFromPb(pb.Inserts),
		UnitsPerBox:      nullInt32FromPb(pb.UnitsPerBox),
		BoxMarking:       nullStringFromPb(pb.BoxMarking),
		BoxDimensions:    nullStringFromPb(pb.BoxDimensions),
		WeightNetGrams:   nullInt32FromPb(pb.WeightNetGrams),
		WeightGrossGrams: nullInt32FromPb(pb.WeightGrossGrams),
		Notes:            nullStringFromPb(pb.Notes),
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
	return &entity.TechCardCosting{
		CmtCost:       cmt,
		HardwareCost:  hardware,
		PackagingCost: packaging,
		LogisticsCost: logistics,
		OverheadCost:  overhead,
		DefectPercent: defect,
		Currency:      nullStringFromPb(pb.Currency),
		Notes:         nullStringFromPb(pb.Notes),
	}, nil
}

// --- emit entity -> pb ---

func techCardConstructionToPb(c *entity.TechCardConstruction) *pb_common.TechCardConstruction {
	if c == nil {
		return nil
	}
	return &pb_common.TechCardConstruction{
		MainStitchType:  pbStringFromNull(c.MainStitchType),
		StitchDensity:   pbStringFromNull(c.StitchDensity),
		OverlockThreads: pbStringFromNull(c.OverlockThreads),
		SeamAllowances:  pbStringFromNull(c.SeamAllowances),
		HemFinish:       pbStringFromNull(c.HemFinish),
		Pressing:        pbStringFromNull(c.Pressing),
		MachineClass:    pbStringFromNull(c.MachineClass),
		Notes:           pbStringFromNull(c.Notes),
	}
}

func techCardOperationsToPb(ops []entity.TechCardOperation) []*pb_common.TechCardOperation {
	out := make([]*pb_common.TechCardOperation, 0, len(ops))
	for i := range ops {
		o := ops[i]
		var bomItemIndex *int32
		if o.BomItemIndex.Valid {
			v := o.BomItemIndex.Int32
			bomItemIndex = &v
		}
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
			Attachment:      pbStringFromNull(o.Attachment),
			TimeNorm:        pbDecimalFromNull(o.TimeNorm),
			Note:            pbStringFromNull(o.Note),
			OperationType:   techCardOperationTypeEntityToPb[o.OperationType],
			Zone:            techCardConstructionZoneEntityToPb[o.Zone],
			BomItemIndex:    bomItemIndex,
			BomItemId:       o.BomItemId.Int64, // OUTPUT: resolved FK (S2/S3); 0 = unset
			CalloutNumber:   pbInt32FromNull(o.CalloutNumber),
			Placement:       pbStringFromNull(o.Placement),
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

// --- sign-off (Phase 3.5a-2) ---

var techCardSignoffSectionPbToEntity = map[pb_common.TechCardSignoffSection]entity.TechCardSignoffSection{
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN:       entity.SignoffDesign,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION: entity.SignoffConstruction,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS:    entity.SignoffMaterials,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR:       entity.SignoffColour,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_LABELS:       entity.SignoffLabels,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_PACKAGING:    entity.SignoffPackaging,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING:      entity.SignoffCosting,
}
var techCardSignoffSectionEntityToPb = func() map[entity.TechCardSignoffSection]pb_common.TechCardSignoffSection {
	m := make(map[entity.TechCardSignoffSection]pb_common.TechCardSignoffSection, len(techCardSignoffSectionPbToEntity))
	for k, v := range techCardSignoffSectionPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardSignoffStatePbToEntity = map[pb_common.TechCardSignoffState]entity.TechCardSignoffState{
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_PENDING:  entity.SignoffStatePending,
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED: entity.SignoffStateApproved,
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_REJECTED: entity.SignoffStateRejected,
}
var techCardSignoffStateEntityToPb = func() map[entity.TechCardSignoffState]pb_common.TechCardSignoffState {
	m := make(map[entity.TechCardSignoffState]pb_common.TechCardSignoffState, len(techCardSignoffStatePbToEntity))
	for k, v := range techCardSignoffStatePbToEntity {
		m[v] = k
	}
	return m
}()

func parseTechCardSignoffs(pbs []*pb_common.TechCardSignoff) ([]entity.TechCardSignoff, error) {
	out := make([]entity.TechCardSignoff, 0, len(pbs))
	seen := make(map[entity.TechCardSignoffSection]bool, len(pbs))
	for _, s := range pbs {
		section, ok := techCardSignoffSectionPbToEntity[s.Section]
		if !ok {
			return nil, fmt.Errorf("signoff section is required and must be valid")
		}
		if seen[section] {
			return nil, fmt.Errorf("duplicate signoff for section %q", section)
		}
		seen[section] = true
		if len(s.SignedBy) > maxVarchar255 {
			return nil, fmt.Errorf("signoff signed_by must be at most %d characters", maxVarchar255)
		}
		state := entity.SignoffStatePending
		if s.State != pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_UNKNOWN {
			v, ok := techCardSignoffStatePbToEntity[s.State]
			if !ok {
				return nil, fmt.Errorf("unknown signoff state: %v", s.State)
			}
			state = v
		}
		out = append(out, entity.TechCardSignoff{
			Section:  section,
			State:    state,
			SignedBy: nullStringFromPb(s.SignedBy),
			SignedAt: nullTimeFromPbTimestamp(s.SignedAt),
			Note:     nullStringFromPb(s.Note),
		})
	}
	return out, nil
}

func techCardSignoffsToPb(signoffs []entity.TechCardSignoff) []*pb_common.TechCardSignoff {
	out := make([]*pb_common.TechCardSignoff, 0, len(signoffs))
	for _, s := range signoffs {
		out = append(out, &pb_common.TechCardSignoff{
			Section:  techCardSignoffSectionEntityToPb[s.Section],
			State:    techCardSignoffStateEntityToPb[s.State],
			SignedBy: pbStringFromNull(s.SignedBy),
			SignedAt: pbTimestampFromNullTime(s.SignedAt),
			Note:     pbStringFromNull(s.Note),
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
			BomItemId:  l.BomItemId.Int32, // §2.8 link (0 = unlinked)
		})
	}
	return out
}

func techCardPackagingToPb(p *entity.TechCardPackaging) *pb_common.TechCardPackaging {
	if p == nil {
		return nil
	}
	return &pb_common.TechCardPackaging{
		FoldingMethod:    pbStringFromNull(p.FoldingMethod),
		Polybag:          pbStringFromNull(p.Polybag),
		BagSticker:       pbStringFromNull(p.BagSticker),
		Inserts:          pbStringFromNull(p.Inserts),
		UnitsPerBox:      pbInt32FromNull(p.UnitsPerBox),
		BoxMarking:       pbStringFromNull(p.BoxMarking),
		BoxDimensions:    pbStringFromNull(p.BoxDimensions),
		WeightNetGrams:   pbInt32FromNull(p.WeightNetGrams),
		WeightGrossGrams: pbInt32FromNull(p.WeightGrossGrams),
		Notes:            pbStringFromNull(p.Notes),
	}
}

// techCardCostingToPb emits the stored per-unit cost articles plus the computed per-colourway
// costs and the root rollup. Root figures are the PRIMARY colourway = index 0. Cost is built
// per GARMENT (unit_cost = materials_per_unit + shared manual articles, × (1 + defect%)), then
// scaled to the whole run (order_cost = unit_cost × order_qty, order_qty = Σ size_quantities).
// Returns nil when no costing row exists.
func techCardCostingToPb(tc *entity.TechCard, fx CostingFx) *pb_common.TechCardCosting {
	if tc.Costing == nil {
		return nil
	}
	c := tc.Costing
	orderQtyBySize := make(map[int]int, len(tc.SizeQuantities))
	totalOrderQty := 0
	for _, q := range tc.SizeQuantities {
		orderQtyBySize[q.SizeId] = q.OrderQty
		if q.OrderQty > 0 {
			totalOrderQty += q.OrderQty
		}
	}
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}

	// Manual per-unit articles are shared across colourways; each colourway's unit cost is
	// its own materials plus these, grossed up by defect%.
	manualPerUnit := decimal.Zero
	for _, d := range []decimal.NullDecimal{c.CmtCost, c.HardwareCost, c.PackagingCost, c.LogisticsCost, c.OverheadCost} {
		if d.Valid {
			manualPerUnit = manualPerUnit.Add(d.Decimal)
		}
	}
	defectMul := decimal.NewFromInt(1)
	if c.DefectPercent.Valid {
		defectMul = decimal.NewFromInt(1).Add(c.DefectPercent.Decimal.Div(decimal.NewFromInt(100)))
	}
	qtyDec := decimal.NewFromInt(int64(totalOrderQty))
	unitAndOrder := func(materialsPerUnit decimal.Decimal) (unit, order decimal.Decimal) {
		unit = materialsPerUnit.Add(manualPerUnit).Mul(defectMul)
		return unit, unit.Mul(qtyDec)
	}

	// Per-colourway cost (OUTPUT-ONLY). The root rollup is the primary colourway (index 0).
	colorwayCosts := make([]*pb_common.TechCardColorwayCost, 0, len(tc.Colorways))
	var rootMaterialsTotal []*pb_common.TechCardCostLine
	rootMaterialsPerUnit := decimal.Zero
	rootHasUnconverted := false
	rootMaterialsPerUnitBase := decimal.Zero
	rootBaseConvertible := false
	for ci := range tc.Colorways {
		cc := colorwayCost(&tc.Colorways[ci], tc.BomItems, costingCcy, orderQtyBySize, totalOrderQty, fx)
		unit, order := unitAndOrder(cc.materialsPerUnit)
		colorwayCosts = append(colorwayCosts, &pb_common.TechCardColorwayCost{
			ColorwayId:               int64(tc.Colorways[ci].Id),
			MaterialsTotal:           cc.materialsTotal,
			MaterialsPerUnit:         pbDecimalFromDecimal(roundMoney(cc.materialsPerUnit)),
			UnitCost:                 pbDecimalFromDecimal(roundMoney(unit)),
			OrderQty:                 int32(totalOrderQty),
			OrderCost:                pbDecimalFromDecimal(roundMoney(order)),
			HasUnconvertedCurrencies: cc.hasUnconverted,
		})
		if ci == 0 {
			rootMaterialsTotal = cc.materialsTotal
			rootMaterialsPerUnit = cc.materialsPerUnit
			rootHasUnconverted = cc.hasUnconverted
			rootMaterialsPerUnitBase = cc.materialsPerUnitBase
			rootBaseConvertible = cc.baseConvertible
		}
	}

	rootUnit, rootOrder := unitAndOrder(rootMaterialsPerUnit)
	out := &pb_common.TechCardCosting{
		CmtCost:                  pbDecimalFromNull(c.CmtCost),
		HardwareCost:             pbDecimalFromNull(c.HardwareCost),
		PackagingCost:            pbDecimalFromNull(c.PackagingCost),
		LogisticsCost:            pbDecimalFromNull(c.LogisticsCost),
		OverheadCost:             pbDecimalFromNull(c.OverheadCost),
		DefectPercent:            pbDecimalFromNull(c.DefectPercent),
		Currency:                 pbStringFromNull(c.Currency),
		Notes:                    pbStringFromNull(c.Notes),
		MaterialsTotal:           rootMaterialsTotal,
		MaterialsPerUnit:         pbDecimalFromDecimal(roundMoney(rootMaterialsPerUnit)),
		UnitCost:                 pbDecimalFromDecimal(roundMoney(rootUnit)),
		OrderQty:                 int32(totalOrderQty),
		OrderCost:                pbDecimalFromDecimal(roundMoney(rootOrder)),
		HasUnconvertedCurrencies: rootHasUnconverted,
		ColorwayCosts:            colorwayCosts,
	}

	// Base-currency rollup (OUTPUT-ONLY): fold the primary colourway's materials and the manual
	// articles (in the costing currency) into the base currency via the FX rates. Set only when
	// every currency involved has a rate, so the seed can trust unit_cost_base as a complete
	// figure; otherwise it is left unset and callers fall back / skip.
	if manualBase, ok := fx.toBase(manualPerUnit, costingCcy); ok && rootBaseConvertible {
		unitBase := rootMaterialsPerUnitBase.Add(manualBase).Mul(defectMul)
		out.UnitCostBase = pbDecimalFromDecimal(roundMoney(unitBase))
		out.OrderCostBase = pbDecimalFromDecimal(roundMoney(unitBase.Mul(qtyDec)))
		out.BaseCurrency = fx.Base
	}

	// total_sam = Σ(operation time_norm); informative, pricing-independent.
	totalSam := decimal.Zero
	for i := range tc.Operations {
		if tc.Operations[i].TimeNorm.Valid {
			totalSam = totalSam.Add(tc.Operations[i].TimeNorm.Decimal)
		}
	}
	if totalSam.IsPositive() {
		out.TotalSam = pbDecimalFromDecimal(totalSam.Round(3))
	}
	return out
}

// ComputeTechCardUnitCost returns a tech card's per-garment unit cost and its currency,
// computed exactly as the read path renders unit_cost — it reuses techCardCostingToPb so
// there is a single source of truth for the math. Returns an invalid NullDecimal when there
// is no costing row or the computed unit cost is not positive.
func ComputeTechCardUnitCost(tc *entity.TechCard, fx CostingFx) (decimal.NullDecimal, string) {
	if tc == nil {
		return decimal.NullDecimal{}, ""
	}
	pb := techCardCostingToPb(tc, fx)
	if pb == nil {
		return decimal.NullDecimal{}, ""
	}
	// Prefer the base-currency rollup so a non-base costing can still seed the product cost;
	// it is set only when every currency involved has an FX rate. Fall back to the costing-
	// currency unit_cost when no base figure is available (e.g. no rates configured) AND the
	// costing is already in the base currency, so an all-base card still seeds without rates.
	if pb.UnitCostBase != nil {
		if v, err := decimal.NewFromString(pb.UnitCostBase.Value); err == nil && v.IsPositive() {
			return decimal.NullDecimal{Decimal: v, Valid: true}, pb.BaseCurrency
		}
	}
	if pb.UnitCost == nil {
		return decimal.NullDecimal{}, ""
	}
	v, err := decimal.NewFromString(pb.UnitCost.Value)
	if err != nil || !v.IsPositive() {
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: v, Valid: true}, pb.Currency
}

// ComputeTechCardCostBreakdownBase decomposes a tech card's per-garment cost into base-currency
// (EUR) components — the same articles ComputeTechCardUnitCost rolls into one number — so the
// seed can snapshot them onto product.cost_breakdown for COGS-structure analytics. Components
// are the primary colourway's materials plus each manual cost article, each folded to base via
// the FX rates; defect_pct is carried raw (unit cost = (Σ components) × (1 + defect_pct/100)).
// Returns ok=false when there is no costing, no colourway, or any component currency lacks an FX
// rate — i.e. exactly when ComputeTechCardUnitCost's base rollup is unset — so cost_breakdown is
// written iff cost_price is seeded from a base-convertible cost, and the two never disagree.
func ComputeTechCardCostBreakdownBase(tc *entity.TechCard, fx CostingFx) (entity.CostBreakdown, bool) {
	if tc == nil || tc.Costing == nil || len(tc.Colorways) == 0 {
		return entity.CostBreakdown{}, false
	}
	c := tc.Costing
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}
	orderQtyBySize := make(map[int]int, len(tc.SizeQuantities))
	totalOrderQty := 0
	for _, q := range tc.SizeQuantities {
		orderQtyBySize[q.SizeId] = q.OrderQty
		if q.OrderQty > 0 {
			totalOrderQty += q.OrderQty
		}
	}
	// Primary colourway (index 0) materials, folded to base — the root rollup's basis.
	cc := colorwayCost(&tc.Colorways[0], tc.BomItems, costingCcy, orderQtyBySize, totalOrderQty, fx)
	if !cc.baseConvertible {
		return entity.CostBreakdown{}, false
	}
	// Each manual article is in the costing currency; fold individually. An absent (invalid)
	// article contributes 0 and never blocks convertibility.
	fold := func(d decimal.NullDecimal) (decimal.Decimal, bool) {
		if !d.Valid {
			return decimal.Zero, true
		}
		return fx.toBase(d.Decimal, costingCcy)
	}
	cmt, ok1 := fold(c.CmtCost)
	hw, ok2 := fold(c.HardwareCost)
	pkg, ok3 := fold(c.PackagingCost)
	logi, ok4 := fold(c.LogisticsCost)
	ovh, ok5 := fold(c.OverheadCost)
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return entity.CostBreakdown{}, false
	}
	defect := decimal.Zero
	if c.DefectPercent.Valid {
		defect = c.DefectPercent.Decimal
	}
	return entity.CostBreakdown{
		Materials: roundMoney(cc.materialsPerUnitBase),
		Cmt:       roundMoney(cmt),
		Hardware:  roundMoney(hw),
		Packaging: roundMoney(pkg),
		Logistics: roundMoney(logi),
		Overhead:  roundMoney(ovh),
		DefectPct: defect,
	}, true
}

// colorwayCostResult holds one colourway's computed PER-GARMENT material cost.
type colorwayCostResult struct {
	materialsTotal   []*pb_common.TechCardCostLine // per-unit material cost grouped by article currency
	materialsPerUnit decimal.Decimal               // Σ per-garment usage cost in costingCcy (and currency-less)
	hasUnconverted   bool                          // a usage currency ≠ costingCcy (excluded from materialsPerUnit)
	// baseConvertible is true when every usage currency could be folded into the base currency
	// via the FX rates; materialsPerUnitBase is that Σ in base currency (valid only when true).
	baseConvertible      bool
	materialsPerUnitBase decimal.Decimal
}

// colorwayCost computes one colourway's PER-GARMENT material cost from its usages. Each usage
// contributes its per-garment UnitTotal (a per-size-only usage is normalised to per-garment by
// dividing its whole-run cost by totalOrderQty), resolved against the BOM article it points at.
// Buckets are per-currency (no FX conversion); currency-less lines fold into the costing
// currency, and a line in another currency is flagged (and left out of materialsPerUnit).
func colorwayCost(cw *entity.TechCardColorway, bomItems []entity.TechCardBomItem, costingCcy string, orderQtyBySize map[int]int, totalOrderQty int, fx CostingFx) colorwayCostResult {
	byCcy := map[string]decimal.Decimal{}
	order := make([]string, 0)
	hasUnconverted := false
	for i := range cw.Usages {
		u := &cw.Usages[i]
		bom := bomItemAtIndex(bomItems, u.BomItemIndex)
		ut := u.UnitTotal(bom, orderQtyBySize, totalOrderQty)
		if !ut.Valid {
			continue
		}
		ccy := ""
		if bom != nil && bom.Currency.Valid {
			ccy = bom.Currency.String
		}
		if _, ok := byCcy[ccy]; !ok {
			order = append(order, ccy)
		}
		byCcy[ccy] = byCcy[ccy].Add(ut.Decimal)
		if ccy != "" && ccy != costingCcy {
			hasUnconverted = true
		}
	}

	lines := make([]*pb_common.TechCardCostLine, 0, len(order))
	for _, ccy := range order {
		lines = append(lines, &pb_common.TechCardCostLine{
			Currency: ccy,
			Amount:   pbDecimalFromDecimal(roundMoney(byCcy[ccy])),
		})
	}

	materialsPerUnit := decimal.Zero
	if v, ok := byCcy[costingCcy]; ok {
		materialsPerUnit = materialsPerUnit.Add(v)
	}
	if costingCcy != "" {
		if v, ok := byCcy[""]; ok { // currency-less lines fold into the costing currency
			materialsPerUnit = materialsPerUnit.Add(v)
		}
	}

	// Base-currency rollup: fold every bucket into the base currency via the FX rates. A
	// currency-less bucket is treated as the costing currency first. If any bucket cannot be
	// converted (no rate), the base figure is incomplete and marked not convertible.
	baseSum := decimal.Zero
	baseConvertible := true
	for _, ccy := range order {
		eff := ccy
		if eff == "" {
			eff = costingCcy
		}
		b, ok := fx.toBase(byCcy[ccy], eff)
		if !ok {
			baseConvertible = false
			continue
		}
		baseSum = baseSum.Add(b)
	}

	return colorwayCostResult{
		materialsTotal:       lines,
		materialsPerUnit:     materialsPerUnit,
		hasUnconverted:       hasUnconverted,
		baseConvertible:      baseConvertible,
		materialsPerUnitBase: baseSum,
	}
}
