package apierr

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInvalid_FieldTaggedAttachesBadRequest(t *testing.T) {
	ve := entity.NewFieldViolation("style_number", "format_invalid", "", "use A-Z 0-9 segments")
	err := Invalid(ve)
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", st.Code())
	}
	var found *errdetails.BadRequest_FieldViolation
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.FieldViolations) > 0 {
			found = br.FieldViolations[0]
		}
	}
	if found == nil {
		t.Fatal("expected a BadRequest FieldViolation detail")
	}
	if found.Field != "style_number" {
		t.Errorf("field = %q, want style_number", found.Field)
	}
}

func TestInvalid_MessageOnlyNoDetail(t *testing.T) {
	err := Invalid(&entity.ValidationError{Message: "bad"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", st.Code())
	}
	if len(st.Details()) != 0 {
		t.Errorf("message-only error should carry no details, got %d", len(st.Details()))
	}
}

func TestStatus_Mapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
		ok   bool
	}{
		{"nil", nil, codes.OK, false},
		{"field-tagged", entity.NewFieldViolation("f", "r", "", ""), codes.InvalidArgument, true},
		{"conflict", entity.ErrTechCardConflict, codes.Aborted, true},
		{"conflict-wrapped", fmt.Errorf("wrap: %w", entity.ErrTechCardConflict), codes.Aborted, true},
		{"released", entity.ErrTechCardReleased, codes.FailedPrecondition, true},
		{"not-found", sql.ErrNoRows, codes.NotFound, true},
		{"unknown", fmt.Errorf("boom"), codes.OK, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Status(tc.err)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if status.Code(got) != tc.want {
				t.Errorf("code = %v, want %v", status.Code(got), tc.want)
			}
		})
	}
}
