package admin

import (
	"context"
	"strings"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestColorwayWritesRejectEmbeddedUsages(t *testing.T) {
	dev := &pb_common.ColorwayDevelopmentInsert{
		Usages: []*pb_common.TechCardColorwayUsage{{}},
	}
	s := &Server{}
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "create",
			call: func() error {
				_, err := s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{Development: dev})
				return err
			},
		},
		{
			name: "update",
			call: func() error {
				_, err := s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{Development: dev})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Equal(t, codes.InvalidArgument, status.Code(err))

			var violation *errdetails.BadRequest_FieldViolation
			for _, detail := range status.Convert(err).Details() {
				if badRequest, ok := detail.(*errdetails.BadRequest); ok && len(badRequest.FieldViolations) > 0 {
					violation = badRequest.FieldViolations[0]
					break
				}
			}
			require.NotNil(t, violation)
			require.Equal(t, "development.usages", violation.Field)
			require.Contains(t, strings.ToLower(violation.Description), "updatecolorwayrecipe")
		})
	}
}
