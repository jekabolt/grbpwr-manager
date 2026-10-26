package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func operationSeamKindPayload(aware bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{OperationSeamKindAware: aware, Operations: ops}
}

func TestOperationSeamKindGateProtectsFullReplace(t *testing.T) {
	echoes := map[string]*pb_common.TechCardOperation{
		"seam kind":    {SeamKind: "bound_open"},
		"work variant": {WorkVariant: "open_end"},
		"button holes": {Fastening: &pb_common.TechCardOperationFastening{ButtonHoles: 4}},
	}
	for name, op := range echoes {
		err := operationSeamKindWireGate(operationSeamKindPayload(false, op))
		wantFailedPrecondition(t, err, name+" without awareness")
		if !strings.Contains(err.Error(), "hard-refresh") {
			t.Fatalf("wire refusal must tell the operator how to recover: %v", err)
		}
		if err := operationSeamKindWireGate(operationSeamKindPayload(true, op)); err != nil {
			t.Fatalf("aware client carrying %s refused: %v", name, err)
		}
	}

	storedOps := map[string]entity.TechCardOperation{
		"seam kind":    {SeamKind: sql.NullString{String: "felled", Valid: true}},
		"work variant": {WorkVariant: sql.NullString{String: "two_way", Valid: true}},
		"button holes": {ButtonHoles: sql.NullInt32{Int32: 2, Valid: true}},
	}
	for name, op := range storedOps {
		wantFailedPrecondition(t, operationSeamKindStoredGate(operationSeamKindPayload(false), storedCard(op)),
			"unaware full replace over stored "+name)
		if err := operationSeamKindStoredGate(operationSeamKindPayload(true), storedCard(op)); err != nil {
			t.Fatalf("aware client must be able to clear the %s of a step: %v", name, err)
		}
	}

	// Сегодняшние карточки: ни одного значения — старый бандл сохраняет как раньше.
	if err := operationSeamKindWireGate(operationSeamKindPayload(false, &pb_common.TechCardOperation{
		Fastening: &pb_common.TechCardOperationFastening{AttachPattern: pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_CROSS_X},
	})); err != nil {
		t.Fatalf("a payload without icon fields stays writable by an old bundle: %v", err)
	}
	if err := operationSeamKindStoredGate(operationSeamKindPayload(false), storedCard(entity.TechCardOperation{})); err != nil {
		t.Fatalf("a stored card without icon fields stays writable: %v", err)
	}
	if err := operationSeamKindStoredGate(operationSeamKindPayload(false), nil); err != nil {
		t.Fatalf("create has no stored card: %v", err)
	}
	if err := operationSeamKindWireGate(nil); err != nil {
		t.Fatalf("nil payload: %v", err)
	}
}

func TestOperationSeamKindGateIsWiredIntoWrites(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationSeamKindWireGate(") < 2 ||
		!strings.Contains(src, "operationSeamKindStoredGate(in, stored)") {
		t.Fatal("operation seam kind shield is not wired into create/update")
	}
	archive := readDraftGateSource(t, "techcard_archive.go")
	if !strings.Contains(archive, "operationSeamKindWireGate,") || !strings.Contains(archive, "operationSeamKindStoredGate,") {
		t.Fatal("archive import does not run the operation seam kind shield")
	}
	for _, f := range []string{"style.go", "techcard_archive_card.go", "techcard_archive_resolve.go", "techcard_restore.go"} {
		if !strings.Contains(readDraftGateSource(t, f), "OperationSeamKindAware = true") {
			t.Fatalf("%s builds a server payload without operation_seam_kind_aware", f)
		}
	}
}
