package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func layerOrderPayload(aware bool, keys ...string) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		OperationLayerOrderAware: aware,
		Operations:               []*pb_common.TechCardOperation{{LayerOrder: keys}},
	}
}

func TestOperationLayerOrderGates(t *testing.T) {
	stored := &entity.TechCard{}
	stored.Operations = []entity.TechCardOperation{{LayerOrder: entity.TechCardLayerOrder{"BACK", "POCKET"}}}

	wantFailedPrecondition(t, operationLayerOrderWireGate(layerOrderPayload(false, "BACK")), "outdated admin client")
	wantFailedPrecondition(t, operationLayerOrderStoredGate(layerOrderPayload(false), stored), "outdated admin client")

	if err := operationLayerOrderWireGate(layerOrderPayload(true, "BACK")); err != nil {
		t.Fatalf("an aware payload with an order: %v", err)
	}
	if err := operationLayerOrderStoredGate(layerOrderPayload(true), stored); err != nil {
		t.Fatalf("an aware payload over a stored order: %v", err)
	}
	if err := operationLayerOrderWireGate(layerOrderPayload(false)); err != nil {
		t.Fatalf("an unaware payload with no order: %v", err)
	}
	if err := operationLayerOrderStoredGate(layerOrderPayload(false), &entity.TechCard{}); err != nil {
		t.Fatalf("an unaware payload over a card with no order: %v", err)
	}
}

func TestOperationLayerOrderGateIsWiredIntoWrites(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationLayerOrderWireGate(") < 2 ||
		!strings.Contains(src, "operationLayerOrderStoredGate(in, stored)") {
		t.Fatal("operation layer order shield is not wired into create/update")
	}
	archive := readDraftGateSource(t, "techcard_archive.go")
	if !strings.Contains(archive, "operationLayerOrderWireGate,") || !strings.Contains(archive, "operationLayerOrderStoredGate,") {
		t.Fatal("archive import does not run the operation layer order shield")
	}
	for _, f := range []string{"style.go", "techcard_archive_card.go", "techcard_archive_resolve.go", "techcard_restore.go"} {
		if !strings.Contains(readDraftGateSource(t, f), "OperationLayerOrderAware = true") {
			t.Fatalf("%s builds a server payload without operation_layer_order_aware", f)
		}
	}
}
