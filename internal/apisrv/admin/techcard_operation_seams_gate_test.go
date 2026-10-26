package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

const testSeamKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func operationSeamsPayload(aware bool, keys ...string) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		OperationSeamsAware: aware,
		Operations:          []*pb_common.TechCardOperation{{SeamKeys: keys}},
	}
}

func TestOperationSeamsGateProtectsFullReplace(t *testing.T) {
	wireErr := operationSeamsWireGate(operationSeamsPayload(false, testSeamKey))
	wantFailedPrecondition(t, wireErr, "seam keys without awareness")
	if !strings.Contains(wireErr.Error(), "hard-refresh") {
		t.Fatalf("wire refusal must tell the operator how to recover: %v", wireErr)
	}

	stored := storedCard(entity.TechCardOperation{SeamKeys: []string{testSeamKey}})
	wantFailedPrecondition(t, operationSeamsStoredGate(operationSeamsPayload(false), stored),
		"unaware full replace over stored seam keys")

	if err := operationSeamsStoredGate(operationSeamsPayload(true), stored); err != nil {
		t.Fatalf("aware client must be able to clear the seams of a step: %v", err)
	}
	if err := operationSeamsWireGate(operationSeamsPayload(false)); err != nil {
		t.Fatalf("cards without step seams remain writable by an old bundle: %v", err)
	}
	if err := operationSeamsStoredGate(operationSeamsPayload(false), storedCard(entity.TechCardOperation{})); err != nil {
		t.Fatalf("stored card without step seams stays writable: %v", err)
	}
}

func TestStripOperationSeamKeys(t *testing.T) {
	pb := operationSeamsPayload(true, testSeamKey)
	stripOperationSeamKeys(pb)
	if len(pb.Operations[0].SeamKeys) != 0 {
		t.Fatalf("seam keys survived the strip: %v", pb.Operations[0].SeamKeys)
	}
	stripOperationSeamKeys(nil)
}

func TestOperationSeamsGateIsWiredIntoWrites(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationSeamsWireGate(") < 2 ||
		!strings.Contains(src, "operationSeamsStoredGate(in, stored)") {
		t.Fatal("operation seams shield is not wired into create/update")
	}
	if !strings.Contains(readDraftGateSource(t, "techcard_archive.go"), "operationSeamsWireGate,") {
		t.Fatal("archive import does not run the operation seams shield")
	}
	for _, f := range []string{"style.go", "techcard_archive_card.go", "techcard_archive_resolve.go", "techcard_restore.go"} {
		if !strings.Contains(readDraftGateSource(t, f), "OperationSeamsAware = true") {
			t.Fatalf("%s builds a server payload without operation_seams_aware", f)
		}
	}
}
