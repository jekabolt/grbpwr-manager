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
	return &entity.SampleInsert{
		TechCardId:   int(pb.TechCardId),
		Purpose:      purpose,
		SizeId:       nullInt32FromPb(pb.SizeId),
		ColorwayId:   nullInt32FromPb(pb.ColorwayId),
		Status:       status,
		FabricSource: fabric,
		Notes:        nullStringFromPb(pb.Notes),
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
	}, nil
}

// ConvertEntitySampleToPb converts a stored sample (with its composed cost, if loaded) to pb.
func ConvertEntitySampleToPb(sm entity.Sample) *pb_common.Sample {
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
		},
		CreatedAt: timestamppb.New(sm.CreatedAt),
		UpdatedAt: timestamppb.New(sm.UpdatedAt),
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
