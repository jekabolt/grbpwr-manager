package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func blockMembershipPayload(aware, detached bool) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		OperationBlockMembershipAware: aware,
		Operations: []*pb_common.TechCardOperation{{
			AssemblyBlockDetached: detached,
		}},
	}
}

func storedBlockMembershipCard(detached bool) *entity.TechCard {
	return storedCard(entity.TechCardOperation{AssemblyBlockDetached: detached})
}

func TestOperationBlockMembershipGateProtectsFullReplace(t *testing.T) {
	wireErr := operationBlockMembershipWireGate(blockMembershipPayload(false, true))
	wantFailedPrecondition(t, wireErr, "detached payload without awareness")
	if !strings.Contains(wireErr.Error(), "hard-refresh") {
		t.Fatalf("wire refusal must tell the operator how to recover: %v", wireErr)
	}

	storedErr := operationBlockMembershipStoredGate(
		blockMembershipPayload(false, false), storedBlockMembershipCard(true))
	wantFailedPrecondition(t, storedErr, "unaware full replace over stored detachment")

	if err := operationBlockMembershipStoredGate(
		blockMembershipPayload(true, false), storedBlockMembershipCard(true)); err != nil {
		t.Fatalf("aware client must be able to attach the operation again: %v", err)
	}
	if err := operationBlockMembershipWireGate(blockMembershipPayload(false, false)); err != nil {
		t.Fatalf("legacy cards without detached operations remain writable: %v", err)
	}
}

func TestOperationBlockMembershipGateIsWiredIntoWrites(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationBlockMembershipWireGate(") < 2 ||
		!strings.Contains(src, "operationBlockMembershipStoredGate(in, stored)") {
		t.Fatal("operation block membership shield is not wired into create/update")
	}
	if !strings.Contains(readDraftGateSource(t, "techcard_archive.go"), "operationBlockMembershipWireGate,") {
		t.Fatal("archive import does not run the operation block membership shield")
	}
}
