package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and have no stable row key. An unaware payload therefore cannot be
// merged safely with stored assembly-block detachment flags: a reorder makes every positional match
// ambiguous. Refuse the write instead of silently attaching detached operations again.

const outdatedOperationBlockMembershipClientFix = "this version of the admin panel does not know which operations sit outside assembly blocks, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationBlockMembershipClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationBlockMembershipClientFix)
}

func operationBlockMembershipWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationBlockMembershipAware() || !payloadCarriesDetachedOperation(pb) {
		return nil
	}
	slog.Default().Warn("operation block membership gate refused an unaware payload that carries a detached operation",
		slog.String("gate", "wire"), slog.String("cell", "payload:detached/aware:no"))
	return outdatedOperationBlockMembershipClient(
		"the payload detaches an operation from assembly blocks without declaring support for it")
}

func operationBlockMembershipStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationBlockMembershipAware() || !storedHasDetachedOperations(stored) {
		return nil
	}
	slog.Default().Warn("operation block membership gate refused an outdated bundle against a card with detached operations",
		slog.String("gate", "stored"), slog.String("cell", "stored:detached/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationBlockMembershipClient(
		"this tech card has operations placed outside every assembly block")
}

func payloadCarriesDetachedOperation(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if op.GetAssemblyBlockDetached() {
			return true
		}
	}
	return false
}

func storedHasDetachedOperations(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if stored.Operations[i].AssemblyBlockDetached {
			return true
		}
	}
	return false
}
