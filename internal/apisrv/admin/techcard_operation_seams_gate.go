package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and have no stable row key. A payload from a bundle that has never
// heard of TechCardOperation.seam_keys (0434) cannot be told apart from «no step sews a marked
// seam», so it would silently unmark every step. Refuse such a write instead
// (operation_seams_aware, 128), the same shield as the block-membership flag.

const outdatedOperationSeamsClientFix = "this version of the admin panel does not know which seams each step sews, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationSeamsClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationSeamsClientFix)
}

func operationSeamsWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationSeamsAware() || !payloadCarriesOperationSeams(pb) {
		return nil
	}
	slog.Default().Warn("operation seams gate refused an unaware payload that carries seam keys",
		slog.String("gate", "wire"), slog.String("cell", "payload:seams/aware:no"))
	return outdatedOperationSeamsClient("the payload marks the seams of a step without declaring support for it")
}

func operationSeamsStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationSeamsAware() || !storedHasOperationSeams(stored) {
		return nil
	}
	slog.Default().Warn("operation seams gate refused an outdated bundle against a card whose steps name seams",
		slog.String("gate", "stored"), slog.String("cell", "stored:seams/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationSeamsClient("this tech card has steps marked with the seams they sew")
}

func payloadCarriesOperationSeams(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if len(op.GetSeamKeys()) > 0 {
			return true
		}
	}
	return false
}

func storedHasOperationSeams(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if len(stored.Operations[i].SeamKeys) > 0 {
			return true
		}
	}
	return false
}

// stripOperationSeamKeys drops every step's seam keys from a server-built payload whose seams do not
// travel with it (season clone, archive export/import: tech_card_seam is not carried in v1). The
// keys would name seams the target card does not have and the save would refuse them.
func stripOperationSeamKeys(pb *pb_common.TechCardInsert) {
	if pb == nil {
		return
	}
	for _, op := range pb.GetOperations() {
		op.SeamKeys = nil
	}
}
