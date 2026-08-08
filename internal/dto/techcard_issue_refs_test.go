package dto

import (
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestConvertTechCardIssueReferences(t *testing.T) {
	// Both required fields, and nothing else: this test is about issue -> operation references, and a
	// step that fails validation for an unrelated reason would fail it for the wrong one.
	op := func() *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH,
			Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OUTER,
		}
	}
	operations := []*pb_common.TechCardOperation{op(), op()}

	t.Run("valid operation and callout references", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
			StyleNumber: "ISSUE-REFS",
			Name:        "Issue refs",
			Operations:  operations,
			Callouts:    []*pb_common.TechCardCallout{{Number: 7}},
			Issues: []*pb_common.TechCardIssue{
				{OperationNumber: 20, CalloutNumber: 7, Description: "linked"},
				{Description: "unlinked"},
			},
		})
		if err != nil {
			t.Fatalf("expected valid references, got %v", err)
		}
		if !got.Issues[0].OperationNumber.Valid || got.Issues[0].OperationNumber.Int32 != 20 ||
			!got.Issues[0].CalloutNumber.Valid || got.Issues[0].CalloutNumber.Int32 != 7 {
			t.Errorf("linked issue references not preserved: %+v", got.Issues[0])
		}
		if got.Issues[1].OperationNumber.Valid || got.Issues[1].CalloutNumber.Valid {
			t.Errorf("zero references must remain unset: %+v", got.Issues[1])
		}
	})

	for _, tt := range []struct {
		name            string
		operationNumber int32
	}{
		{"not a multiple of ten", 15},
		{"below the first operation", 1},
		{"above the final operation", 30},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
				StyleNumber: "ISSUE-OP",
				Name:        "Issue operation ref",
				Operations:  operations,
				Issues: []*pb_common.TechCardIssue{
					{Description: "unlinked first row"},
					{OperationNumber: tt.operationNumber, Description: "bad operation ref"},
				},
			})
			ve := requireIssueFieldViolation(t, err, "issues[1].operation_number")
			if !strings.Contains(ve.Reason, "[10, 20]") {
				t.Errorf("violation must name valid range [10, 20], got %q", ve.Reason)
			}
		})
	}

	t.Run("operation reference with no operations", func(t *testing.T) {
		_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
			StyleNumber: "ISSUE-NO-OPS",
			Name:        "Issue without operations",
			Issues:      []*pb_common.TechCardIssue{{OperationNumber: 10, Description: "bad operation ref"}},
		})
		ve := requireIssueFieldViolation(t, err, "issues[0].operation_number")
		if !strings.Contains(ve.Reason, "no operations") {
			t.Errorf("violation must explain that no operation range exists, got %q", ve.Reason)
		}
	})

	t.Run("dangling callout reference", func(t *testing.T) {
		_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
			StyleNumber: "ISSUE-CALLOUT",
			Name:        "Issue callout ref",
			Callouts:    []*pb_common.TechCardCallout{{Number: 7}},
			Issues: []*pb_common.TechCardIssue{
				{Description: "unlinked first row"},
				{CalloutNumber: 8, Description: "bad callout ref"},
			},
		})
		requireIssueFieldViolation(t, err, "issues[1].callout_number")
	})
}

func requireIssueFieldViolation(t *testing.T, err error, wantField string) *entity.ValidationError {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected field-tagged validation error, got %T: %v", err, err)
	}
	if ve.Field != wantField {
		t.Fatalf("violation field = %q, want %q", ve.Field, wantField)
	}
	return ve
}
