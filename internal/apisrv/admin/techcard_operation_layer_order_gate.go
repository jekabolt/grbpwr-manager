package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and have no stable row key. A payload from a bundle that has never
// heard of TechCardOperation.layer_order (0445) cannot be told apart from «no order set», so it would
// silently put every step back on its flags. Refuse such a write instead
// (operation_layer_order_aware, 131), the same shield as the fold links.

const outdatedOperationLayerOrderClientFix = "this version of the admin panel does not know the layer order of a step, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationLayerOrderClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationLayerOrderClientFix)
}

func operationLayerOrderWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationLayerOrderAware() || !payloadCarriesLayerOrder(pb) {
		return nil
	}
	slog.Default().Warn("operation layer order gate refused an unaware payload that carries an order",
		slog.String("gate", "wire"), slog.String("cell", "payload:order/aware:no"))
	return outdatedOperationLayerOrderClient("the payload orders a step's layers without declaring support for it")
}

func operationLayerOrderStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationLayerOrderAware() || !storedHasLayerOrder(stored) {
		return nil
	}
	slog.Default().Warn("operation layer order gate refused an outdated bundle against a card with an order",
		slog.String("gate", "stored"), slog.String("cell", "stored:order/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationLayerOrderClient("this tech card has steps with a layer order")
}

func payloadCarriesLayerOrder(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if len(op.GetLayerOrder()) > 0 {
			return true
		}
	}
	return false
}

func storedHasLayerOrder(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if len(stored.Operations[i].LayerOrder) > 0 {
			return true
		}
	}
	return false
}
