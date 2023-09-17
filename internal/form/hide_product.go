package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type HideProductRequest struct {
	*pb_admin.HideProductRequest
}

func (f *HideProductRequest) Validate() error {
	if f == nil {
		return status.Error(codes.InvalidArgument, "request is nil")
	}
	return ValidateStruct(f,
		v.Field(&f.ProductId, v.Required),
	)
}
