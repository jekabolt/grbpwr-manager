package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and have no stable row key. A payload from a bundle that has never
// heard of TechCardOperation.seam_tolerance_mm (0446) cannot be told apart from «no step states it»,
// so its save would silently return every step to the default. Refuse such a write instead
// (operation_seam_tolerance_aware, 131), the same shield as the seam links and the folds.

const outdatedOperationSeamToleranceClientFix = "this version of the admin panel does not know the seam tolerance of a step, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationSeamToleranceClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationSeamToleranceClientFix)
}

func operationSeamToleranceWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationSeamToleranceAware() || !payloadCarriesOperationSeamTolerance(pb) {
		return nil
	}
	slog.Default().Warn("operation seam tolerance gate refused an unaware payload that carries it",
		slog.String("gate", "wire"), slog.String("cell", "payload:tolerance/aware:no"))
	return outdatedOperationSeamToleranceClient("the payload states a seam tolerance without declaring support for it")
}

func operationSeamToleranceStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationSeamToleranceAware() || !storedHasOperationSeamTolerance(stored) {
		return nil
	}
	slog.Default().Warn("operation seam tolerance gate refused an outdated bundle against a card whose steps state it",
		slog.String("gate", "stored"), slog.String("cell", "stored:tolerance/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationSeamToleranceClient("this tech card has steps with a seam tolerance")
}

func payloadCarriesOperationSeamTolerance(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if op.GetSeamToleranceMm() != nil {
			return true
		}
	}
	return false
}

func storedHasOperationSeamTolerance(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if stored.Operations[i].SeamToleranceMm.Valid {
			return true
		}
	}
	return false
}
