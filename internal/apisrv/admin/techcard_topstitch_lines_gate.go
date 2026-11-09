package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/proto"
)

// Operation rows are full-replaced, and topstitch lines are their cascade children. An unaware
// payload cannot express the difference between an empty list and a field it has never seen.
func topstitchLinesWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetTopstitchLinesAware() || !payloadCarriesTopstitchLines(pb) {
		return nil
	}
	slog.Default().Warn("topstitch lines gate refused an unaware payload carrying lines",
		slog.String("gate", "wire"))
	return outdatedOperationSeamsClient("the payload carries measured topstitch lines without declaring support for them")
}

func topstitchLinesStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetTopstitchLinesAware() || !storedHasTopstitchLines(stored) {
		return nil
	}
	slog.Default().Warn("topstitch lines gate refused an outdated bundle against a card with measured lines",
		slog.String("gate", "stored"), slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationSeamsClient("this tech card has measured topstitch lines")
}

func payloadCarriesTopstitchLines(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if len(op.GetTopstitchLines()) > 0 {
			return true
		}
	}
	return false
}

func storedHasTopstitchLines(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if len(stored.Operations[i].TopstitchLines) > 0 {
			return true
		}
	}
	return false
}

// sameOperationsIgnoringTopstitch proves that a pre-0464 restore can use the operation slice as a
// positional identity. Any other operation change makes carrying children by index unsafe.
func sameOperationsIgnoringTopstitch(a, b *pb_common.TechCardInsert) bool {
	if a == nil || b == nil || len(a.GetOperations()) != len(b.GetOperations()) {
		return false
	}
	for i := range a.GetOperations() {
		left, right := a.GetOperations()[i], b.GetOperations()[i]
		if left == nil || right == nil {
			if left != right {
				return false
			}
			continue
		}
		leftCopy := proto.Clone(left).(*pb_common.TechCardOperation)
		rightCopy := proto.Clone(right).(*pb_common.TechCardOperation)
		leftCopy.TopstitchLines = nil
		rightCopy.TopstitchLines = nil
		if !proto.Equal(leftCopy, rightCopy) {
			return false
		}
	}
	return true
}

func restoredTopstitchStale(lines *pb_common.TechCardInsert) map[string]bool {
	out := map[string]bool{}
	if lines == nil {
		return out
	}
	for _, op := range lines.GetOperations() {
		for _, line := range op.GetTopstitchLines() {
			if line.GetStale() {
				out[entity.CutStripLineKey(line.GetLineKey())] = true
			}
		}
	}
	return out
}

func markRestoredTopstitchLines(tc *entity.TechCardInsert, stale map[string]bool) {
	if tc == nil {
		return
	}
	for i := range tc.Operations {
		for j := range tc.Operations[i].TopstitchLines {
			line := &tc.Operations[i].TopstitchLines[j]
			line.RestoreSourceFingerprint = true
			line.RestoreStale = stale[entity.CutStripLineKey(line.LineKey)]
		}
	}
}
