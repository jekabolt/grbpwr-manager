package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertPbSampleInsertToEntity validates and converts a sample write payload.
func ConvertPbSampleInsertToEntity(pb *pb_common.SampleInsert) (*entity.SampleInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("sample is required")
	}
	if pb.TechCardId <= 0 {
		return nil, fmt.Errorf("tech_card_id is required")
	}
	purpose := strings.ToLower(strings.TrimSpace(pb.Purpose))
	if purpose == "" {
		purpose = entity.SamplePurposeProto
	}
	if !entity.ValidSamplePurposes[purpose] {
		return nil, fmt.Errorf("invalid sample purpose %q", purpose)
	}
	status := strings.ToLower(strings.TrimSpace(pb.Status))
	if status == "" {
		status = entity.SampleStatusPlanned
	}
	if !entity.ValidSampleStatuses[status] {
		return nil, fmt.Errorf("invalid sample status %q", status)
	}
	fabric := strings.ToLower(strings.TrimSpace(pb.FabricSource))
	if fabric == "" {
		fabric = entity.SampleFabricSample
	}
	if !entity.ValidSampleFabricSources[fabric] {
		return nil, fmt.Errorf("invalid sample fabric_source %q", fabric)
	}
	startedAt, err := parseNullDate(pb.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("started_at: %w", err)
	}
	finishedAt, err := parseNullDate(pb.FinishedAt)
	if err != nil {
		return nil, fmt.Errorf("finished_at: %w", err)
	}
	// Length guards (A2/§2.7): pattern_url/pattern_note had no validation at all — an over-length value
	// hit the DB column raw. Match the column widths (VARCHAR(512)/(255)).
	patternURL := strings.TrimSpace(pb.PatternUrl)
	if len(patternURL) > maxVarchar512 {
		return nil, fmt.Errorf("pattern_url must be at most %d characters", maxVarchar512)
	}
	patternNote := strings.TrimSpace(pb.PatternNote)
	if len(patternNote) > maxVarchar255 {
		return nil, fmt.Errorf("pattern_note must be at most %d characters", maxVarchar255)
	}
	if pb.RoundNumber < 0 || pb.SpecReleaseId < 0 || pb.PreviousSampleId < 0 {
		return nil, fmt.Errorf("sample round_number, spec_release_id and previous_sample_id must not be negative")
	}
	mediaIds := make([]int, 0, len(pb.MediaIds))
	for _, mid := range pb.MediaIds {
		if mid <= 0 {
			return nil, fmt.Errorf("sample media_id must be positive")
		}
		mediaIds = append(mediaIds, int(mid))
	}
	return &entity.SampleInsert{
		TechCardId:   int(pb.TechCardId),
		Purpose:      purpose,
		SizeId:       nullInt32FromPb(pb.SizeId),
		ColorwayId:   nullInt32FromPb(pb.ColorwayId),
		Status:       status,
		FabricSource: fabric,
		Notes:            nullStringFromPb(pb.Notes),
		StartedAt:        startedAt,
		FinishedAt:       finishedAt,
		PatternUrl:       nullStringFromPb(patternURL),
		PatternNote:      nullStringFromPb(patternNote),
		RoundNumber:      nullInt32FromPb(pb.RoundNumber),
		SpecReleaseId:    nullInt32FromPb(pb.SpecReleaseId),
		PreviousSampleId: nullInt32FromPb(pb.PreviousSampleId),
		MediaIds:         mediaIds,
	}, nil
}

// ConvertEntitySampleToPb converts a stored sample (with its composed cost, if loaded) to pb.
func ConvertEntitySampleToPb(sm entity.Sample) *pb_common.Sample {
	mediaIds := make([]int32, 0, len(sm.Media))
	media := make([]*pb_common.MediaFull, 0, len(sm.Media))
	for i := range sm.Media {
		mediaIds = append(mediaIds, int32(sm.Media[i].Id))
		media = append(media, ConvertEntityToCommonMedia(&sm.Media[i]))
	}
	out := &pb_common.Sample{
		Id:     int32(sm.Id),
		Number: int32(sm.Number),
		Sample: &pb_common.SampleInsert{
			TechCardId:   int32(sm.TechCardId),
			Purpose:      sm.Purpose,
			SizeId:       nullInt32Value(sm.SizeId),
			ColorwayId:   nullInt32Value(sm.ColorwayId),
			Status:       sm.Status,
			FabricSource: sm.FabricSource,
			Notes:        sm.Notes.String,
			StartedAt:    dateString(sm.StartedAt),
			FinishedAt:   dateString(sm.FinishedAt),
			PatternUrl:       sm.PatternUrl.String,
			PatternNote:      sm.PatternNote.String,
			RoundNumber:      pbInt32FromNull(sm.RoundNumber),
			SpecReleaseId:    pbInt32FromNull(sm.SpecReleaseId),
			PreviousSampleId: pbInt32FromNull(sm.PreviousSampleId),
			MediaIds:         mediaIds,
		},
		Media:       media,
		LockVersion: int32(sm.LockVersion),
		CreatedBy:   sm.CreatedBy,
		UpdatedBy:   sm.UpdatedBy,
		CreatedAt:   timestamppb.New(sm.CreatedAt),
		UpdatedAt:   timestamppb.New(sm.UpdatedAt),
	}
	if sm.Cost != nil {
		out.Cost = &pb_common.SampleCost{
			MaterialsBase: pbDecimalFromDecimal(sm.Cost.MaterialsBase),
			ManualBase:    pbDecimalFromDecimal(sm.Cost.ManualBase),
			TotalBase:     pbDecimalFromDecimal(sm.Cost.TotalBase),
			HasUncosted:   sm.Cost.HasUncosted,
		}
	}
	return out
}

// dateString formats a NullTime as YYYY-MM-DD, or "" when invalid.
func dateString(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02")
}

// ConvertPbSampleSubstitutionInsertToEntity validates and converts a substitution write payload (§2.7).
func ConvertPbSampleSubstitutionInsertToEntity(pb *pb_common.SampleSubstitutionInsert) (*entity.SampleSubstitutionInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("substitution is required")
	}
	if pb.SampleId <= 0 {
		return nil, fmt.Errorf("sample_id is required")
	}
	if pb.BomItemId < 0 || pb.OriginalMaterialId < 0 || pb.SubstitutedMaterialId < 0 {
		return nil, fmt.Errorf("substitution material/bom ids must not be negative")
	}
	reason := strings.TrimSpace(pb.Reason)
	if len(reason) > maxVarchar255 {
		return nil, fmt.Errorf("reason must be at most %d characters", maxVarchar255)
	}
	plannedQty, err := nullDecimalFromPb(pb.PlannedQty)
	if err != nil {
		return nil, fmt.Errorf("planned_qty: %w", err)
	}
	actualQty, err := nullDecimalFromPb(pb.ActualQty)
	if err != nil {
		return nil, fmt.Errorf("actual_qty: %w", err)
	}
	return &entity.SampleSubstitutionInsert{
		SampleId:              int(pb.SampleId),
		BomItemId:             nullInt32FromPb(pb.BomItemId),
		OriginalMaterialId:    nullInt32FromPb(pb.OriginalMaterialId),
		SubstitutedMaterialId: nullInt32FromPb(pb.SubstitutedMaterialId),
		Reason:                nullStringFromPb(reason),
		PlannedQty:            plannedQty,
		ActualQty:             actualQty,
	}, nil
}

// ConvertEntitySampleSubstitutionToPb converts a stored substitution to pb.
func ConvertEntitySampleSubstitutionToPb(s entity.SampleSubstitution) *pb_common.SampleSubstitution {
	return &pb_common.SampleSubstitution{
		Id:                    int32(s.Id),
		SampleId:              int32(s.SampleId),
		BomItemId:             pbInt32FromNull(s.BomItemId),
		OriginalMaterialId:    pbInt32FromNull(s.OriginalMaterialId),
		SubstitutedMaterialId: pbInt32FromNull(s.SubstitutedMaterialId),
		Reason:                s.Reason.String,
		PlannedQty:            pbDecimalFromNull(s.PlannedQty),
		ActualQty:             pbDecimalFromNull(s.ActualQty),
		CreatedBy:             s.CreatedBy,
		CreatedAt:             timestamppb.New(s.CreatedAt),
	}
}
