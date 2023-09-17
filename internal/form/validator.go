package form

import (
	"errors"
	"strings"
	"unicode"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Analog validation.ValidateStruct ozzy validation but returns
func ValidateStruct(structField interface{}, rules ...*validation.FieldRules) error {
	var rawErrors []error

	for _, rule := range rules {
		err := validation.ValidateStruct(structField, rule)
		if err != nil {
			rawErrors = append(rawErrors, convertRepositoryErrors(err))
		}
	}
	if len(rawErrors) == 0 {
		return nil
	}

	br := &errdetails.BadRequest{}
	for _, err := range rawErrors {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Description: formatErrMsg(err.Error()),
		})
	}

	st, err := status.New(codes.InvalidArgument, "Validation message").WithDetails(br)
	if err != nil {
		return status.New(codes.Internal, err.Error()).Err()
	}

	return st.Err()
}

func formatErrMsg(s string) string {
	return ucfirst(strings.Trim(s, " .")) + "."
}

func ucfirst(str string) string {
	for i, v := range str {
		return string(unicode.ToUpper(v)) + str[i+1:]
	}
	return ""
}

func convertRepositoryErrors(err error) validation.Errors {
	if ve, ok := err.(validation.Errors); ok {
		for key, value := range ve {
			if st := status.Convert(value); st != nil && st.Code() == codes.NotFound {
				ve[key] = errors.New(st.Message())
			}
		}
		return ve
	}
	return nil
}
