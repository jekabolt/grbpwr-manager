package apierr

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInvalid_FieldTaggedCarriesBadRequestDetail(t *testing.T) {
	ve := entity.NewFieldViolation("bom_items[2].material_id", "material_not_found",
		`colorway "BLK" recipe`, "unlink the material there first")
	err := Invalid(ve)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Invalid did not return a gRPC status error: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", st.Code())
	}
	var br *errdetails.BadRequest
	for _, d := range st.Details() {
		if v, is := d.(*errdetails.BadRequest); is {
			br = v
		}
	}
	if br == nil {
		t.Fatalf("expected a BadRequest detail, got details %v", st.Details())
	}
	if len(br.FieldViolations) != 1 {
		t.Fatalf("field violations = %d, want 1", len(br.FieldViolations))
	}
	fv := br.FieldViolations[0]
	if fv.Field != "bom_items[2].material_id" {
		t.Errorf("field = %q, want bom_items[2].material_id", fv.Field)
	}
	// Description packs reason + conflicting entity + how-to-fix so a client reading only
	// Field+Description still gets the full story.
	for _, want := range []string{"material_not_found", `colorway "BLK" recipe`, "unlink the material"} {
		if !strings.Contains(fv.Description, want) {
			t.Errorf("description %q missing %q", fv.Description, want)
		}
	}
}

func TestInvalid_MessageOnlyHasNoDetail(t *testing.T) {
	err := Invalid(&entity.ValidationError{Message: "plain validation failure"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", st.Code())
	}
	if len(st.Details()) != 0 {
		t.Fatalf("message-only error should carry no details, got %v", st.Details())
	}
}

func TestStatus_SentinelMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
		ok   bool
	}{
		{"nil", nil, codes.OK, false},
		{"tech-card conflict", entity.ErrTechCardConflict, codes.Aborted, true},
		{"material conflict", entity.ErrMaterialConflict, codes.Aborted, true},
		{"wrapped material conflict", fmt.Errorf("update: %w", entity.ErrMaterialConflict), codes.Aborted, true},
		{"code taken", entity.ErrMaterialCodeTaken, codes.FailedPrecondition, true},
		{"unit locked", entity.ErrMaterialUnitLocked, codes.FailedPrecondition, true},
		{"material not found", entity.ErrMaterialNotFound, codes.NotFound, true},
		{"sql no rows", sql.ErrNoRows, codes.NotFound, true},
		{"field-tagged", entity.NewFieldViolation("f", "r", "", ""), codes.InvalidArgument, true},
		{"unknown", errors.New("boom"), codes.OK, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Status(tc.err)
			if ok != tc.ok {
				t.Fatalf("recognised = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				if got != nil {
					t.Fatalf("unrecognised error should map to nil, got %v", got)
				}
				return
			}
			if status.Code(got) != tc.want {
				t.Fatalf("code = %v, want %v", status.Code(got), tc.want)
			}
		})
	}
}

