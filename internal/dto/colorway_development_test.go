package dto

import (
	"testing"
	"time"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestColorwayDevelopmentPatchIgnoresClientLabDipAudit(t *testing.T) {
	forged := &pb_common.ColorwayDevelopmentInsert{
		LabDipSubmittedAt: timestamppb.New(time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)),
		LabDipDecidedAt:   timestamppb.New(time.Date(2002, 3, 4, 5, 6, 7, 0, time.UTC)),
		LabDipDecidedBy:   "forged-admin",
	}
	auditOnly := &fieldmaskpb.FieldMask{Paths: []string{
		"development.lab_dip_submitted_at",
		"development.lab_dip_decided_at",
		"development.lab_dip_decided_by",
	}}
	require.Nil(t, ColorwayDevelopmentPatchFromPb(forged, auditOnly),
		"client audit fields are read-only and cannot form a writable patch")

	forged.LabDipStatus = pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_APPROVED
	withStatus := &fieldmaskpb.FieldMask{Paths: append(auditOnly.Paths, "development.lab_dip_status")}
	patch := ColorwayDevelopmentPatchFromPb(forged, withStatus)
	require.NotNil(t, patch)
	require.NotNil(t, patch.LabDipStatus, "the lifecycle transition remains writable")
	require.Empty(t, patch.Actor, "the DTO never accepts an actor from the request")
}
