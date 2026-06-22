package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConvertPbFittingInsertToEntity(t *testing.T) {
	date := timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	valid := &pb_common.FittingInsert{
		ProductId:   42,
		ModelId:     7,
		FittingDate: date,
		Comment:     "sample run",
		Status:      pb_common.FittingStatus_FITTING_STATUS_DONE,
		Verdict:     pb_common.FittingVerdict_FITTING_VERDICT_APPROVED,
		RecordedBy:  "admin@x.cc",
		Sizes: []*pb_common.FittingSizeInsert{
			{SizeId: 4, FitNote: "perfect"},
			{SizeId: 5, FitNote: ""},
		},
		MediaIds: []int32{11, 12},
	}

	got, err := ConvertPbFittingInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ProductId.Valid || got.ProductId.Int32 != 42 || !got.ModelId.Valid || got.ModelId.Int32 != 7 {
		t.Errorf("product/model mismatch: %+v", got)
	}
	if got.Status != entity.FittingDone || got.Verdict != entity.FittingApproved {
		t.Errorf("status/verdict mismatch: %v %v", got.Status, got.Verdict)
	}
	if len(got.Sizes) != 2 || got.Sizes[0].SizeId != 4 || !got.Sizes[0].FitNote.Valid || got.Sizes[1].FitNote.Valid {
		t.Errorf("sizes mismatch: %+v", got.Sizes)
	}
	if len(got.MediaIds) != 2 || got.MediaIds[0] != 11 {
		t.Errorf("media ids mismatch: %+v", got.MediaIds)
	}

	// defaults: unset status/verdict become planned/pending
	def, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{ProductId: 1, FittingDate: date})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if def.Status != entity.FittingPlanned || def.Verdict != entity.FittingPending {
		t.Errorf("defaults mismatch: %v %v", def.Status, def.Verdict)
	}
	if def.ModelId.Valid {
		t.Errorf("model id 0 should be NULL, got %+v", def.ModelId)
	}

	// a tech-card-only fitting (no product yet, e.g. a proto) is valid.
	tc, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{TechCardId: 8, FittingDate: date})
	if err != nil {
		t.Fatalf("tech-card-only: %v", err)
	}
	if !tc.TechCardId.Valid || tc.TechCardId.Int32 != 8 || tc.ProductId.Valid {
		t.Errorf("tech-card-only anchor mismatch: tc=%+v prod=%+v", tc.TechCardId, tc.ProductId)
	}

	// fitting_date is normalized to the UTC calendar date regardless of time-of-day.
	noon := timestamppb.New(time.Date(2026, 6, 19, 15, 30, 0, 0, time.UTC))
	norm, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{ProductId: 1, FittingDate: noon})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if want := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC); !norm.FittingDate.Equal(want) {
		t.Errorf("fitting_date not normalized: got %v want %v", norm.FittingDate, want)
	}

	// invalid cases (incl. out-of-range enum values that must be rejected, not defaulted)
	bad := map[string]*pb_common.FittingInsert{
		"nil":                  nil,
		"no anchor":            {ProductId: 0, FittingDate: date},
		"no date":              {ProductId: 1},
		"size id zero":         {ProductId: 1, FittingDate: date, Sizes: []*pb_common.FittingSizeInsert{{SizeId: 0}}},
		"duplicate size":       {ProductId: 1, FittingDate: date, Sizes: []*pb_common.FittingSizeInsert{{SizeId: 4}, {SizeId: 4}}},
		"bad status":           {ProductId: 1, FittingDate: date, Status: pb_common.FittingStatus(99)},
		"bad verdict":          {ProductId: 1, FittingDate: date, Verdict: pb_common.FittingVerdict(99)},
		"recorded_by too long": {ProductId: 1, FittingDate: date, RecordedBy: string(make([]byte, 256))},
	}
	for name, in := range bad {
		if _, err := ConvertPbFittingInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertEntityFittingToPb(t *testing.T) {
	f := &entity.Fitting{
		Id: 3,
		FittingInsert: entity.FittingInsert{
			TechCardId:  nullInt32FromPb(8),
			ProductId:   nullInt32FromPb(42),
			FittingDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			Status:      entity.FittingPlanned,
			Verdict:     entity.FittingPending,
			Sizes:       []entity.FittingSize{{SizeId: 4}},
		},
		Media: []entity.MediaFull{{Id: 11}, {Id: 12}},
	}
	pb := ConvertEntityFittingToPb(f)
	if pb.Id != 3 || pb.Fitting.ProductId != 42 || pb.Fitting.TechCardId != 8 {
		t.Errorf("id/product/tech_card mismatch: %+v", pb)
	}
	if pb.Fitting.Status != pb_common.FittingStatus_FITTING_STATUS_PLANNED ||
		pb.Fitting.Verdict != pb_common.FittingVerdict_FITTING_VERDICT_PENDING {
		t.Errorf("status/verdict mismatch: %v %v", pb.Fitting.Status, pb.Fitting.Verdict)
	}
	if len(pb.Media) != 2 || len(pb.Fitting.MediaIds) != 2 || pb.Fitting.MediaIds[1] != 12 {
		t.Errorf("media mismatch: media=%d ids=%+v", len(pb.Media), pb.Fitting.MediaIds)
	}
}
