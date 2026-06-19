package dto

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fittingStatusPbToEntity = map[pb_common.FittingStatus]entity.FittingStatus{
	pb_common.FittingStatus_FITTING_STATUS_PLANNED:   entity.FittingPlanned,
	pb_common.FittingStatus_FITTING_STATUS_DONE:      entity.FittingDone,
	pb_common.FittingStatus_FITTING_STATUS_CANCELLED: entity.FittingCancelled,
}

var fittingStatusEntityToPb = map[entity.FittingStatus]pb_common.FittingStatus{
	entity.FittingPlanned:   pb_common.FittingStatus_FITTING_STATUS_PLANNED,
	entity.FittingDone:      pb_common.FittingStatus_FITTING_STATUS_DONE,
	entity.FittingCancelled: pb_common.FittingStatus_FITTING_STATUS_CANCELLED,
}

var fittingVerdictPbToEntity = map[pb_common.FittingVerdict]entity.FittingVerdict{
	pb_common.FittingVerdict_FITTING_VERDICT_PENDING:      entity.FittingPending,
	pb_common.FittingVerdict_FITTING_VERDICT_APPROVED:     entity.FittingApproved,
	pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK: entity.FittingNeedsRework,
	pb_common.FittingVerdict_FITTING_VERDICT_REJECTED:     entity.FittingRejected,
}

var fittingVerdictEntityToPb = map[entity.FittingVerdict]pb_common.FittingVerdict{
	entity.FittingPending:     pb_common.FittingVerdict_FITTING_VERDICT_PENDING,
	entity.FittingApproved:    pb_common.FittingVerdict_FITTING_VERDICT_APPROVED,
	entity.FittingNeedsRework: pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK,
	entity.FittingRejected:    pb_common.FittingVerdict_FITTING_VERDICT_REJECTED,
}

// ConvertPbFittingInsertToEntity converts a pb_common.FittingInsert to entity,
// validating the product, date, and sizes. Status/verdict default to
// planned/pending when unset.
func ConvertPbFittingInsertToEntity(pb *pb_common.FittingInsert) (*entity.FittingInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("fitting insert is nil")
	}
	if pb.ProductId <= 0 {
		return nil, fmt.Errorf("fitting product_id is required")
	}
	if pb.FittingDate == nil {
		return nil, fmt.Errorf("fitting_date is required")
	}
	if len(pb.RecordedBy) > maxVarchar255 {
		return nil, fmt.Errorf("recorded_by must be at most %d characters", maxVarchar255)
	}

	// Default only when explicitly unset; reject any other unmapped value
	// instead of silently coercing it to the default.
	status := entity.FittingPlanned
	if pb.Status != pb_common.FittingStatus_FITTING_STATUS_UNKNOWN {
		v, ok := fittingStatusPbToEntity[pb.Status]
		if !ok {
			return nil, fmt.Errorf("unknown fitting status: %v", pb.Status)
		}
		status = v
	}
	verdict := entity.FittingPending
	if pb.Verdict != pb_common.FittingVerdict_FITTING_VERDICT_UNKNOWN {
		v, ok := fittingVerdictPbToEntity[pb.Verdict]
		if !ok {
			return nil, fmt.Errorf("unknown fitting verdict: %v", pb.Verdict)
		}
		verdict = v
	}

	sizes := make([]entity.FittingSize, 0, len(pb.Sizes))
	seen := make(map[int]bool, len(pb.Sizes))
	for _, sz := range pb.Sizes {
		if sz.SizeId <= 0 {
			return nil, fmt.Errorf("fitting size size_id is required")
		}
		if seen[int(sz.SizeId)] {
			return nil, fmt.Errorf("duplicate fitting size_id: %d", sz.SizeId)
		}
		seen[int(sz.SizeId)] = true
		sizes = append(sizes, entity.FittingSize{
			SizeId:  int(sz.SizeId),
			FitNote: nullStringFromPb(sz.FitNote),
		})
	}

	mediaIds := make([]int, 0, len(pb.MediaIds))
	for _, mid := range pb.MediaIds {
		mediaIds = append(mediaIds, int(mid))
	}

	// Normalize to a UTC calendar date so storage into the DATE column is
	// deterministic regardless of the incoming timestamp's time-of-day.
	// (Clients should send the fitting date at UTC midnight.)
	ft := pb.FittingDate.AsTime().UTC()
	fittingDate := time.Date(ft.Year(), ft.Month(), ft.Day(), 0, 0, 0, 0, time.UTC)

	return &entity.FittingInsert{
		ProductId:   int(pb.ProductId),
		ModelId:     nullInt32FromPb(pb.ModelId),
		FittingDate: fittingDate,
		Comment:     nullStringFromPb(pb.Comment),
		Status:      status,
		Verdict:     verdict,
		RecordedBy:  nullStringFromPb(pb.RecordedBy),
		Sizes:       sizes,
		MediaIds:    mediaIds,
	}, nil
}

// ConvertEntityFittingToPb converts an entity.Fitting to pb_common.Fitting,
// including resolved media.
func ConvertEntityFittingToPb(f *entity.Fitting) *pb_common.Fitting {
	if f == nil {
		return nil
	}

	sizes := make([]*pb_common.FittingSizeInsert, 0, len(f.Sizes))
	for _, sz := range f.Sizes {
		sizes = append(sizes, &pb_common.FittingSizeInsert{
			SizeId:  int32(sz.SizeId),
			FitNote: pbStringFromNull(sz.FitNote),
		})
	}

	media := make([]*pb_common.MediaFull, 0, len(f.Media))
	mediaIds := make([]int32, 0, len(f.Media))
	for i := range f.Media {
		media = append(media, ConvertEntityToCommonMedia(&f.Media[i]))
		mediaIds = append(mediaIds, int32(f.Media[i].Id))
	}

	return &pb_common.Fitting{
		Id: int32(f.Id),
		Fitting: &pb_common.FittingInsert{
			ProductId:   int32(f.ProductId),
			ModelId:     pbInt32FromNull(f.ModelId),
			FittingDate: timestamppb.New(f.FittingDate),
			Comment:     pbStringFromNull(f.Comment),
			Status:      fittingStatusEntityToPb[f.Status],
			Verdict:     fittingVerdictEntityToPb[f.Verdict],
			RecordedBy:  pbStringFromNull(f.RecordedBy),
			Sizes:       sizes,
			MediaIds:    mediaIds,
		},
		Media:     media,
		CreatedAt: timestamppb.New(f.CreatedAt),
		UpdatedAt: timestamppb.New(f.UpdatedAt),
	}
}
