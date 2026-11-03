package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func seamTolerancePayload(aware bool, value *pb_decimal.Decimal) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		OperationSeamToleranceAware: aware,
		Operations:                  []*pb_common.TechCardOperation{{SeamToleranceMm: value}},
	}
}

func TestOperationSeamToleranceGateProtectsFullReplace(t *testing.T) {
	wireErr := operationSeamToleranceWireGate(seamTolerancePayload(false, &pb_decimal.Decimal{Value: "2"}))
	wantFailedPrecondition(t, wireErr, "tolerance without awareness")
	if !strings.Contains(wireErr.Error(), "hard-refresh") {
		t.Fatalf("wire refusal must tell the operator how to recover: %v", wireErr)
	}

	stored := storedCard(entity.TechCardOperation{
		SeamToleranceMm: decimal.NullDecimal{Decimal: decimal.Zero, Valid: true},
	})
	wantFailedPrecondition(t, operationSeamToleranceStoredGate(seamTolerancePayload(false, nil), stored),
		"unaware full replace over a stored zero")

	if err := operationSeamToleranceStoredGate(seamTolerancePayload(true, nil), stored); err != nil {
		t.Fatalf("aware client must be able to clear a tolerance: %v", err)
	}
	if err := operationSeamToleranceWireGate(seamTolerancePayload(false, nil)); err != nil {
		t.Fatalf("cards without a tolerance remain writable by an old bundle: %v", err)
	}
	if err := operationSeamToleranceStoredGate(seamTolerancePayload(false, nil), storedCard(entity.TechCardOperation{})); err != nil {
		t.Fatalf("stored card without a tolerance stays writable: %v", err)
	}
}
