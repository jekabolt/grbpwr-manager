package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// productionRunStatusPbToEntity maps the proto status enum to the stored string.
var productionRunStatusPbToEntity = map[pb_common.ProductionRunStatus]entity.ProductionRunStatus{
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED:     entity.ProductionRunPlanned,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS: entity.ProductionRunInProgress,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_RECEIVED:    entity.ProductionRunReceived,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CLOSED:      entity.ProductionRunClosed,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CANCELLED:   entity.ProductionRunCancelled,
}

// productionRunStatusEntityToPb is the reverse map.
var productionRunStatusEntityToPb = func() map[entity.ProductionRunStatus]pb_common.ProductionRunStatus {
	m := make(map[entity.ProductionRunStatus]pb_common.ProductionRunStatus, len(productionRunStatusPbToEntity))
	for k, v := range productionRunStatusPbToEntity {
		m[v] = k
	}
	return m
}()

// ConvertPbProductionRunInsertToEntity validates and converts a writable production run. The
// planned-cost snapshot is NOT taken from the client — the service layer sets it separately.
func ConvertPbProductionRunInsertToEntity(pb *pb_common.ProductionRunInsert) (*entity.ProductionRunInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("production run is required")
	}
	if pb.TechCardId <= 0 {
		return nil, fmt.Errorf("tech_card_id is required")
	}
	status, ok := productionRunStatusPbToEntity[pb.Status]
	if !ok {
		return nil, fmt.Errorf("status is required and must be valid")
	}
	if len(pb.Notes) > maxVarchar1024 {
		return nil, fmt.Errorf("notes must be at most %d characters", maxVarchar1024)
	}
	sizes, err := convertPbProductionRunSizes(pb.Sizes)
	if err != nil {
		return nil, err
	}
	return &entity.ProductionRunInsert{
		TechCardId: int(pb.TechCardId),
		ReleaseId:  nullInt64FromPb(int64(pb.ReleaseId)),
		Status:     status,
		StartedAt:  nullTimeFromPbTimestamp(pb.StartedAt),
		ReceivedAt: nullTimeFromPbTimestamp(pb.ReceivedAt),
		Notes:      nullStringFromPb(pb.Notes),
		Sizes:      sizes,
	}, nil
}

func convertPbProductionRunSizes(pbs []*pb_common.ProductionRunSize) ([]entity.ProductionRunSize, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	seen := make(map[int]struct{}, len(pbs))
	out := make([]entity.ProductionRunSize, 0, len(pbs))
	for _, sz := range pbs {
		if sz == nil {
			continue
		}
		if sz.SizeId <= 0 {
			return nil, fmt.Errorf("production run size: size_id is required")
		}
		if _, dup := seen[int(sz.SizeId)]; dup {
			return nil, fmt.Errorf("production run size: duplicate size_id %d", sz.SizeId)
		}
		seen[int(sz.SizeId)] = struct{}{}
		if sz.PlannedQty < 0 {
			return nil, fmt.Errorf("production run size: planned_qty must be non-negative")
		}
		e := entity.ProductionRunSize{SizeId: int(sz.SizeId), PlannedQty: int(sz.PlannedQty)}
		if sz.ReceivedQty != nil {
			if *sz.ReceivedQty < 0 {
				return nil, fmt.Errorf("production run size: received_qty must be non-negative")
			}
			e.ReceivedQty = sql.NullInt64{Int64: int64(*sz.ReceivedQty), Valid: true}
		}
		if sz.DefectQty != nil {
			if *sz.DefectQty < 0 {
				return nil, fmt.Errorf("production run size: defect_qty must be non-negative")
			}
			e.DefectQty = sql.NullInt64{Int64: int64(*sz.DefectQty), Valid: true}
		}
		out = append(out, e)
	}
	return out, nil
}

// ConvertEntityProductionRunToPb converts a stored run (with its size grid) to pb.
func ConvertEntityProductionRunToPb(r *entity.ProductionRun) *pb_common.ProductionRun {
	if r == nil {
		return nil
	}
	return &pb_common.ProductionRun{
		Id: int32(r.Id),
		Run: &pb_common.ProductionRunInsert{
			TechCardId: int32(r.TechCardId),
			ReleaseId:  int32(r.ReleaseId.Int64),
			Status:     productionRunStatusEntityToPb[r.Status],
			StartedAt:  pbTimestampFromNullTime(r.StartedAt),
			ReceivedAt: pbTimestampFromNullTime(r.ReceivedAt),
			Notes:      pbStringFromNull(r.Notes),
			Sizes:      productionRunSizesToPb(r.Sizes),
		},
		PlannedUnitCost: pbDecimalFromNull(r.PlannedUnitCost),
		PlannedCurrency: pbStringFromNull(r.PlannedCurrency),
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
	}
}

func productionRunSizesToPb(sizes []entity.ProductionRunSize) []*pb_common.ProductionRunSize {
	out := make([]*pb_common.ProductionRunSize, 0, len(sizes))
	for _, sz := range sizes {
		pb := &pb_common.ProductionRunSize{SizeId: int32(sz.SizeId), PlannedQty: int32(sz.PlannedQty)}
		if sz.ReceivedQty.Valid {
			v := int32(sz.ReceivedQty.Int64)
			pb.ReceivedQty = &v
		}
		if sz.DefectQty.Valid {
			v := int32(sz.DefectQty.Int64)
			pb.DefectQty = &v
		}
		out = append(out, pb)
	}
	return out
}

// NormalizeProductionRunStatusFilter validates an optional status filter string, returning the
// entity status ("" for no filter). It rejects an unknown non-empty value.
func NormalizeProductionRunStatusFilter(s string) (entity.ProductionRunStatus, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", nil
	}
	st := entity.ProductionRunStatus(s)
	if !entity.IsValidProductionRunStatus(st) {
		return "", fmt.Errorf("unknown production run status %q", s)
	}
	return st, nil
}
