package entity

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const recordedTerminalPressGraphProfile = "single-saved-row-size-adjacent-native-stitch-with-recorded-terminal-press"

// Project only the fields explicitly admitted by this profile. A future nonzero column,
// operation child, or private store-only link must block admission even if protobuf omits it.
func unassignedSavedPress(op TechCardOperation, number int64, note string) bool {
	if !op.OperationNumber.Valid || int64(op.OperationNumber.Int32) != number || op.OperationType != OpTypePress ||
		op.Zone != ZoneOther || op.Work != (sql.NullString{String: "press_flat", Valid: true}) ||
		op.PressEquipment != (sql.NullString{String: "iron", Valid: true}) || op.Note.String != note {
		return false
	}
	op.OperationNumber = sql.NullInt32{}
	op.OperationType = ""
	op.Zone = ""
	op.Work = sql.NullString{}
	op.PressEquipment = sql.NullString{}
	op.Note = sql.NullString{}
	remaining := reflect.ValueOf(op)
	for i := 0; i < remaining.NumField(); i++ {
		field := remaining.Field(i)
		if field.Kind() == reflect.Slice && field.Len() == 0 {
			continue
		}
		if !field.IsZero() {
			return false
		}
	}
	return true
}

func validateRecordedTerminalPressOrder(graph map[string]any, expected PatternProductCompositeSidecarExpected, requireCurrent bool) error {
	invalid := errors.New("recorded terminal press must exactly match the complete current production order")
	order, ok := graph["productionOrder"].([]any)
	if !ok || len(order) != 2 {
		return invalid
	}
	stitch, ok := exactJSONObject(order[0], "role", "sourceOperationNumber")
	if !ok || stitch["role"] != "simulated-stitch" {
		return invalid
	}
	press, ok := exactJSONObject(order[1], "role", "sourceOperationNumber", "canonicalSourceOperation")
	if !ok || press["role"] != "recorded-terminal-press" {
		return invalid
	}
	stitchNumber, ok := safeJSONInteger(stitch["sourceOperationNumber"], 1)
	if !ok {
		return invalid
	}
	pressNumber, ok := safeJSONInteger(press["sourceOperationNumber"], 1)
	if !ok || pressNumber <= stitchNumber || pressNumber > 2147483647 {
		return invalid
	}
	raw, ok := press["canonicalSourceOperation"].(string)
	if !ok || len(raw) > 32*1024 {
		return invalid
	}
	var wire pb_common.TechCardOperation
	if err := protojson.Unmarshal([]byte(raw), &wire); err != nil || len(wire.GetNote()) > 2048 {
		return invalid
	}
	want := &pb_common.TechCardOperation{OperationNumber: int32(pressNumber), OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS, Zone: pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OTHER, PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON, Work: "press_flat", Note: wire.GetNote()}
	if !proto.Equal(&wire, want) {
		return invalid
	}
	all, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(want)
	if err != nil {
		return invalid
	}
	canonical, err := canonicalJSONWithJSNumbers(all)
	if err != nil || string(canonical) != raw {
		return invalid
	}
	ops, ok := graph["operations"].([]any)
	if !ok || len(ops) != 1 {
		return invalid
	}
	op, ok := exactJSONObject(ops[0], "clientOperationId", "sourceOperationNumber", "kind", "stage", "order", "seamIds", "inputKeys", "outputUnitKey")
	if !ok || op["kind"] != "stitch" {
		return invalid
	}
	orderIndex, ok := safeJSONInteger(op["order"], 0)
	if !ok || orderIndex != 0 {
		return invalid
	}
	if _, ok := safeJSONInteger(op["stage"], 0); !ok {
		return invalid
	}
	n, ok := safeJSONInteger(op["sourceOperationNumber"], 1)
	if !ok || n != stitchNumber {
		return invalid
	}
	seams, ok := graph["seams"].([]any)
	if !ok || len(seams) != 1 {
		return invalid
	}
	seam, ok := seams[0].(map[string]any)
	if !ok {
		return invalid
	}
	ids, ok := op["seamIds"].([]any)
	if !ok || len(ids) != 1 {
		return invalid
	}
	id, ok := ids[0].(string)
	if !ok || id == "" || seam["id"] != id {
		return invalid
	}
	oid, ok := op["clientOperationId"].(string)
	if !ok || oid == "" || seam["clientOperationId"] != oid {
		return invalid
	}
	inputKeys, ok := op["inputKeys"].([]any)
	if !ok || len(inputKeys) != 2 {
		return invalid
	}
	firstKey, firstOK := inputKeys[0].(string)
	secondKey, secondOK := inputKeys[1].(string)
	if !firstOK || !secondOK || firstKey == "" || secondKey == "" || firstKey == secondKey {
		return invalid
	}
	pieces, ok := graph["pieces"].([]any)
	if !ok || len(pieces) != 2 {
		return invalid
	}
	pieceKeys := map[string]bool{}
	for _, p := range pieces {
		piece, ok := p.(map[string]any)
		if !ok {
			return invalid
		}
		pieceKey, ok := piece["pieceLineKey"].(string)
		if !ok || pieceKeys[pieceKey] {
			return invalid
		}
		pieceKeys[pieceKey] = true
	}
	if !pieceKeys[firstKey] || !pieceKeys[secondKey] {
		return invalid
	}
	if !requireCurrent {
		return nil
	}
	if int64(expected.SelectedOperationNumber) != stitchNumber || expected.AssemblyOrderCard == nil || expected.AssemblyOrderCard.Id != expected.Row.TechCardID || len(expected.AssemblyOrderCard.Operations) != 2 || !unassignedSavedPress(expected.AssemblyOrderCard.Operations[1], pressNumber, wire.GetNote()) {
		return invalid
	}
	// An unkeyed iron may inherit a card-level equipment profile. This profile
	// has no carrier for an inherited press recipe, including an ambiguous choice.
	if c := expected.AssemblyOrderCard.Construction; c != nil && c.EquipmentDefaults != nil {
		for _, profile := range c.EquipmentDefaults.Presses {
			if profile.PressEquipment == "iron" && (!profile.PressOperationType.Valid || profile.PressOperationType.String == "press") {
				return invalid
			}
		}
	}
	full := []byte(expected.AssemblyOrderContentCanonicalJSON)
	h := sha256.New()
	h.Write([]byte("saved-full-assembly-physical-ao-content/1\x00"))
	h.Write(full)
	if hex.EncodeToString(h.Sum(nil)) != expected.AssemblyOrderContentSHA256 {
		return invalid
	}
	var payload struct {
		OperationsPayloadCanonicalJSON string `json:"operationsPayloadCanonicalJson"`
	}
	if json.Unmarshal(full, &payload) != nil {
		return invalid
	}
	var rows []json.RawMessage
	if json.Unmarshal([]byte(payload.OperationsPayloadCanonicalJSON), &rows) != nil || len(rows) != 2 || !bytes.Equal(rows[1], canonical) {
		return invalid
	}
	var actualStitch pb_common.TechCardOperation
	if protojson.Unmarshal(rows[0], &actualStitch) != nil || int64(actualStitch.GetOperationNumber()) != stitchNumber || actualStitch.GetWork() != "join_lockstitch" || actualStitch.GetOperationType() != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE || len(actualStitch.GetSeamKeys()) != 1 || actualStitch.GetSeamKeys()[0] != seam["sourceSeamKey"] {
		return invalid
	}
	if len(inputKeys) != len(actualStitch.GetInputKeys()) || op["outputUnitKey"] != actualStitch.GetOutputUnitKey() {
		return invalid
	}
	for i, k := range actualStitch.GetInputKeys() {
		if inputKeys[i] != k {
			return invalid
		}
	}
	return nil
}
