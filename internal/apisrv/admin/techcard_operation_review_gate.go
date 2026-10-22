package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and still have no stable row key. As with the 0410 draft shield,
// an old bundle cannot merge an unknown per-step mark, so it must either carry every mark knowingly
// or be refused before it erases them.
const outdatedOperationReviewClientFix = "this version of the admin panel does not know which steps need DXF reconciliation review, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationReviewClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedOperationReviewClientFix)
}

func operationReviewWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationReviewAware() || !payloadCarriesOperationReview(pb) {
		return nil
	}
	slog.Default().Warn("operation review gate refused an unaware payload that carries review marks",
		slog.String("gate", "wire"), slog.String("cell", "payload:review/aware:no"))
	return outdatedOperationReviewClient("the payload marks steps for review without declaring support for it")
}

func operationReviewStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationReviewAware() || !storedHasOperationReview(stored) {
		return nil
	}
	slog.Default().Warn("operation review gate refused an outdated bundle against a marked card",
		slog.String("gate", "stored"), slog.String("cell", "stored:review/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationReviewClient("this tech card has steps awaiting DXF reconciliation review")
}

func payloadCarriesOperationReview(pb *pb_common.TechCardInsert) bool {
	for _, operation := range pb.GetOperations() {
		if operation.GetReview() != "" {
			return true
		}
	}
	return false
}

func storedHasOperationReview(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if stored.Operations[i].Review.Valid && stored.Operations[i].Review.String != "" {
			return true
		}
	}
	return false
}

func operationReviewReleaseGate(tc *entity.TechCardInsert) error {
	if tc == nil || tc.ApprovalState != entity.TechCardApprovalReleased {
		return nil
	}
	for _, operation := range tc.Operations {
		if operation.Review.Valid && operation.Review.String != "" {
			return status.Error(codes.FailedPrecondition,
				"tech card has assembly steps awaiting DXF reconciliation review; clear every review mark before release")
		}
	}
	return nil
}
