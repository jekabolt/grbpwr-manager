package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFittingPatternsRoundTrip(t *testing.T) {
	in := &pb_common.FittingInsert{
		TechCardId:  5,
		FittingDate: timestamppb.New(time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)),
		Patterns: []*pb_common.FittingPattern{
			{SizeId: 4, Url: "https://cdn/iter1.pdf", Filename: "iter1.pdf", SizeBytes: 99},
			{Url: "https://cdn/iter2.pdf"}, // size unset
		},
	}
	ent, err := ConvertPbFittingInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(ent.Patterns) != 2 || ent.Patterns[0].URL != "https://cdn/iter1.pdf" ||
		!ent.Patterns[0].SizeId.Valid || ent.Patterns[0].SizeId.Int32 != 4 {
		t.Fatalf("fitting patterns parse mismatch: %+v", ent.Patterns)
	}
	if ent.Patterns[1].SizeId.Valid {
		t.Fatalf("second pattern size should be unset")
	}

	out := ConvertEntityFittingToPb(&entity.Fitting{FittingInsert: *ent})
	if len(out.Fitting.Patterns) != 2 || out.Fitting.Patterns[0].Filename != "iter1.pdf" ||
		out.Fitting.Patterns[0].SizeBytes != 99 {
		t.Fatalf("fitting patterns round-trip mismatch: %+v", out.Fitting.Patterns)
	}
	if out.Fitting.Patterns[1].SizeId != 0 {
		t.Fatalf("second pattern size should emit 0, got %d", out.Fitting.Patterns[1].SizeId)
	}
}

func TestFittingPatternURLRequired(t *testing.T) {
	in := &pb_common.FittingInsert{
		TechCardId:  5,
		FittingDate: timestamppb.New(time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)),
		Patterns:    []*pb_common.FittingPattern{{SizeId: 4, Url: ""}},
	}
	if _, err := ConvertPbFittingInsertToEntity(in); err == nil {
		t.Fatalf("expected error for empty pattern url")
	}
}
