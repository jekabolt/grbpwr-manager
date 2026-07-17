package dto

import (
	"fmt"
	"strings"
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
	if pb.ProductId < 0 || pb.TechCardId < 0 {
		return nil, fmt.Errorf("fitting product_id and tech_card_id must not be negative")
	}
	// A fitting must anchor to the style and/or the specific colour sample.
	if pb.ProductId <= 0 && pb.TechCardId <= 0 {
		return nil, fmt.Errorf("fitting requires product_id or tech_card_id")
	}
	if pb.FittingDate == nil {
		return nil, fmt.Errorf("fitting_date is required")
	}
	// recorded_by is deprecated (§2.7): the recorder is now the server-stamped created_by. Ignored on write.

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

	patterns := make([]entity.FittingPattern, 0, len(pb.Patterns))
	for _, p := range pb.Patterns {
		if p.SizeId < 0 {
			return nil, fmt.Errorf("fitting pattern size_id must not be negative")
		}
		url := strings.TrimSpace(p.Url)
		if url == "" {
			return nil, fmt.Errorf("fitting pattern url is required")
		}
		if len(url) > maxVarchar1024 {
			return nil, fmt.Errorf("fitting pattern url must be at most %d characters", maxVarchar1024)
		}
		if !isHTTPURL(url) {
			return nil, fmt.Errorf("fitting pattern url must be an http(s) URL")
		}
		if len(p.Filename) > maxVarchar255 {
			return nil, fmt.Errorf("fitting pattern filename must be at most %d characters", maxVarchar255)
		}
		if p.SizeBytes < 0 {
			return nil, fmt.Errorf("fitting pattern size_bytes must not be negative")
		}
		patterns = append(patterns, entity.FittingPattern{
			SizeId:    nullInt32FromPb(p.SizeId),
			URL:       url,
			Filename:  nullStringFromPb(p.Filename),
			SizeBytes: nullInt64FromPb(p.SizeBytes),
		})
	}

	callouts := make([]entity.FittingCallout, 0, len(pb.Callouts))
	for _, c := range pb.Callouts {
		if c.Number < 0 {
			return nil, fmt.Errorf("fitting callout number must not be negative")
		}
		note := strings.TrimSpace(c.Note)
		if note == "" {
			return nil, fmt.Errorf("fitting callout note is required")
		}
		if len(note) > maxTaskText {
			return nil, fmt.Errorf("fitting callout note must be at most %d characters", maxTaskText)
		}
		if c.MediaId < 0 {
			return nil, fmt.Errorf("fitting callout media_id must not be negative")
		}
		posX, err := nullDecimalFromPb(c.PosX)
		if err != nil {
			return nil, fmt.Errorf("fitting callout pos_x: %w", err)
		}
		posY, err := nullDecimalFromPb(c.PosY)
		if err != nil {
			return nil, fmt.Errorf("fitting callout pos_y: %w", err)
		}
		if err := validateUnitInterval(posX, "fitting callout pos_x"); err != nil {
			return nil, err
		}
		if err := validateUnitInterval(posY, "fitting callout pos_y"); err != nil {
			return nil, err
		}
		callouts = append(callouts, entity.FittingCallout{
			Number:  int(c.Number),
			Note:    nullStringFromPb(note),
			MediaId: nullInt32FromPb(c.MediaId),
			PosX:    posX,
			PosY:    posY,
		})
	}

	outcome := nullStringFromPb("")
	if o := strings.ToLower(strings.TrimSpace(pb.Outcome)); o != "" {
		if !entity.ValidFittingOutcomes[entity.FittingOutcome(o)] {
			return nil, fmt.Errorf("fitting outcome must be one of approved|new_round|dropped")
		}
		outcome = nullStringFromPb(o)
	}
	if pb.RoundNumber < 0 {
		return nil, fmt.Errorf("fitting round_number must not be negative")
	}

	changeRequests := make([]entity.FittingChangeRequest, 0, len(pb.ChangeRequests))
	for _, cr := range pb.ChangeRequests {
		e, err := fittingChangeRequestEntity(cr.Target, cr.Note, cr.CalloutNumber, cr.PieceId, cr.CarriedFromId, cr.Zone, cr.Status)
		if err != nil {
			return nil, err
		}
		changeRequests = append(changeRequests, e)
	}

	// Normalize to a UTC calendar date so storage into the DATE column is
	// deterministic regardless of the incoming timestamp's time-of-day.
	// (Clients should send the fitting date at UTC midnight.)
	ft := pb.FittingDate.AsTime().UTC()
	fittingDate := time.Date(ft.Year(), ft.Month(), ft.Day(), 0, 0, 0, 0, time.UTC)

	return &entity.FittingInsert{
		TechCardId:     nullInt32FromPb(pb.TechCardId),
		ProductId:      nullInt32FromPb(pb.ProductId),
		ModelId:        nullInt32FromPb(pb.ModelId),
		FittingDate:    fittingDate,
		Comment:        nullStringFromPb(pb.Comment),
		Status:         status,
		Verdict:        verdict,
		RoundNumber:    nullInt32FromPb(pb.RoundNumber),
		Outcome:        outcome,
		SampleId:       nullInt32FromPb(pb.SampleId),
		Sizes:          sizes,
		MediaIds:       mediaIds,
		Patterns:       patterns,
		Callouts:       callouts,
		ChangeRequests: changeRequests,
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
			TechCardId:     pbInt32FromNull(f.TechCardId),
			ProductId:      pbInt32FromNull(f.ProductId),
			ModelId:        pbInt32FromNull(f.ModelId),
			FittingDate:    timestamppb.New(f.FittingDate),
			Comment:        pbStringFromNull(f.Comment),
			Status:         fittingStatusEntityToPb[f.Status],
			Verdict:        fittingVerdictEntityToPb[f.Verdict],
			RecordedBy:     f.CreatedBy, // deprecated field: mirror the server-stamped recorder for back-compat
			RoundNumber:    pbInt32FromNull(f.RoundNumber),
			Outcome:        f.Outcome.String,
			SampleId:       pbInt32FromNull(f.SampleId),
			Sizes:          sizes,
			MediaIds:       mediaIds,
			Patterns:       fittingPatternsToPb(f.Patterns),
			Callouts:       fittingCalloutsToPb(f.Callouts),
			ChangeRequests: fittingChangeRequestsToPb(f.ChangeRequests),
		},
		Media:       media,
		LockVersion: int32(f.LockVersion),
		CreatedBy:   f.CreatedBy,
		UpdatedBy:   f.UpdatedBy,
		CreatedAt:   timestamppb.New(f.CreatedAt),
		UpdatedAt:   timestamppb.New(f.UpdatedAt),
	}
}

// fittingChangeRequestsToPb emits a fitting's structured change requests for display.
func fittingChangeRequestsToPb(crs []entity.FittingChangeRequest) []*pb_common.FittingChangeRequest {
	out := make([]*pb_common.FittingChangeRequest, 0, len(crs))
	for _, c := range crs {
		out = append(out, ConvertEntityFittingChangeRequestToPb(c))
	}
	return out
}

// ConvertEntityFittingChangeRequestToPb converts one stored change-request item to pb (S26). resolved
// is a deprecated read-only mirror of status.
func ConvertEntityFittingChangeRequestToPb(c entity.FittingChangeRequest) *pb_common.FittingChangeRequest {
	return &pb_common.FittingChangeRequest{
		Id:            int32(c.Id),
		FittingId:     int32(c.FittingId),
		Target:        c.Target,
		Note:          c.Note,
		CalloutNumber: pbInt32FromNull(c.CalloutNumber),
		Zone:          c.Zone.String,
		PieceId:       pbInt32FromNull(c.PieceId),
		Status:        c.Status,
		CarriedFromId: pbInt32FromNull(c.CarriedFromId),
		CreatedBy:     c.CreatedBy,
		RoundNumber:   pbInt32FromNull(c.RoundNumber),
		Resolved:      c.Status == entity.FittingChangeStatusResolved,
	}
}

// fittingChangeRequestEntity validates and converts the shared change-request fields (S26), used by
// both the embedded initial batch (FittingInsert) and the dedicated CRUD.
func fittingChangeRequestEntity(target, note string, calloutNumber, pieceID, carriedFromID int32, zone, status string) (entity.FittingChangeRequest, error) {
	t := strings.ToLower(strings.TrimSpace(target))
	if !entity.ValidFittingChangeTargets[t] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request target must be one of pattern|construction|material|grading|other")
	}
	n := strings.TrimSpace(note)
	if n == "" {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request note is required")
	}
	if len(n) > maxTaskText {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request note must be at most %d characters", maxTaskText)
	}
	if calloutNumber < 0 || pieceID < 0 || carriedFromID < 0 {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request callout_number, piece_id and carried_from_id must not be negative")
	}
	z := strings.ToLower(strings.TrimSpace(zone))
	if z != "" && !entity.ValidFittingChangeZones[z] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request zone must be one of unknown|outer|lining|interlining|other")
	}
	st := strings.ToLower(strings.TrimSpace(status))
	if st == "" {
		st = entity.FittingChangeStatusOpen
	}
	if !entity.ValidFittingChangeStatuses[st] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request status must be open or resolved")
	}
	return entity.FittingChangeRequest{
		Target:        t,
		Note:          n,
		CalloutNumber: nullInt32FromPb(calloutNumber),
		Zone:          nullStringFromPb(z),
		PieceId:       nullInt32FromPb(pieceID),
		Status:        st,
		CarriedFromId: nullInt32FromPb(carriedFromID),
	}, nil
}

// ConvertPbFittingChangeRequestInsertToEntity validates a dedicated change-request write payload (S26).
func ConvertPbFittingChangeRequestInsertToEntity(pb *pb_common.FittingChangeRequestInsert) (*entity.FittingChangeRequest, error) {
	if pb == nil {
		return nil, fmt.Errorf("change_request is required")
	}
	e, err := fittingChangeRequestEntity(pb.Target, pb.Note, pb.CalloutNumber, pb.PieceId, pb.CarriedFromId, pb.Zone, pb.Status)
	if err != nil {
		return nil, err
	}
	e.FittingId = int(pb.FittingId)
	return &e, nil
}

// fittingCalloutsToPb emits a fitting's photo callouts for display.
func fittingCalloutsToPb(cs []entity.FittingCallout) []*pb_common.FittingCallout {
	out := make([]*pb_common.FittingCallout, 0, len(cs))
	for _, c := range cs {
		out = append(out, &pb_common.FittingCallout{
			Number:  int32(c.Number),
			Note:    pbStringFromNull(c.Note),
			MediaId: pbInt32FromNull(c.MediaId),
			PosX:    pbDecimalFromNull(c.PosX),
			PosY:    pbDecimalFromNull(c.PosY),
		})
	}
	return out
}

// fittingPatternsToPb emits a fitting's PDF выкройка iterations for display.
func fittingPatternsToPb(ps []entity.FittingPattern) []*pb_common.FittingPattern {
	out := make([]*pb_common.FittingPattern, 0, len(ps))
	for _, p := range ps {
		out = append(out, &pb_common.FittingPattern{
			SizeId:    pbInt32FromNull(p.SizeId),
			Url:       p.URL,
			Filename:  pbStringFromNull(p.Filename),
			SizeBytes: p.SizeBytes.Int64,
		})
	}
	return out
}
