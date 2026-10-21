package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func reviewPayload(aware bool, reviews ...string) *pb_common.TechCardInsert {
	operations := make([]*pb_common.TechCardOperation, 0, len(reviews))
	for i, review := range reviews {
		operations = append(operations, &pb_common.TechCardOperation{
			OperationNumber: int32((i + 1) * 10),
			OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:            gateZone(),
			MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Review:          review,
		})
	}
	return &pb_common.TechCardInsert{
		StyleNumber:          "DXF-REVIEW-GATE",
		Name:                 "gate",
		OperationReviewAware: aware,
		Operations:           operations,
	}
}

func storedReviewCard(review string) *entity.TechCard {
	return storedCard(entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		Review:        sql.NullString{String: review, Valid: review != ""},
	})
}

func TestOperationReviewGateProtectsFullReplace(t *testing.T) {
	wantFailedPrecondition(t, operationReviewWireGate(reviewPayload(false, "reshaped")),
		"review-marked payload without awareness")
	wantFailedPrecondition(t, operationReviewStoredGate(reviewPayload(false, ""), storedReviewCard("orphan")),
		"unaware save over a stored review mark")

	if err := operationReviewStoredGate(reviewPayload(true, ""), storedReviewCard("split")); err != nil {
		t.Fatalf("an aware client must be able to clear a review mark: %v", err)
	}
	if err := operationReviewWireGate(reviewPayload(false, "")); err != nil {
		t.Fatalf("legacy cards without review marks must remain writable: %v", err)
	}
}

func TestOperationReviewDTOValidationAndRoundTrip(t *testing.T) {
	in := reviewPayload(true, "remapped")
	card, err := dto.ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert review-marked operation: %v", err)
	}
	if len(card.Operations) != 1 || !card.Operations[0].Review.Valid || card.Operations[0].Review.String != "remapped" {
		t.Fatalf("review did not reach entity: %+v", card.Operations)
	}
	out := dto.ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *card}, dto.CostingFx{})
	if got := out.GetTechCard().GetOperations()[0].GetReview(); got != "remapped" {
		t.Fatalf("review round-trip = %q, want remapped", got)
	}

	bad := reviewPayload(true, "invented")
	if _, err := dto.ConvertPbTechCardInsertToEntity(bad); err == nil || !strings.Contains(err.Error(), "operations[0].review") {
		t.Fatalf("unknown review must be a field error, got %v", err)
	}
}

func TestOperationReviewBlocksRelease(t *testing.T) {
	card, err := dto.ConvertPbTechCardInsertToEntity(reviewPayload(true, "split"))
	if err != nil {
		t.Fatalf("convert release payload: %v", err)
	}
	card.ApprovalState = entity.TechCardApprovalReleased
	wantFailedPrecondition(t, operationReviewReleaseGate(card), "release with an uncleared review mark")

	card.Operations[0].Review = sql.NullString{}
	if err := operationReviewReleaseGate(card); err != nil {
		t.Fatalf("cleared review marks must allow release: %v", err)
	}
}
