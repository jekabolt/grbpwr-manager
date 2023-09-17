package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UploadContentVideoRequest struct {
	*pb_admin.UploadContentVideoRequest
}

func (f *UploadContentVideoRequest) Validate() error {
	if f == nil {
		return status.Error(codes.InvalidArgument, "request is nil")
	}
	return ValidateStruct(f,
		v.Field(&f.Raw, v.Required),
		v.Field(&f.Folder, v.Required),
		v.Field(&f.VideoName, v.Required),
	)
}
