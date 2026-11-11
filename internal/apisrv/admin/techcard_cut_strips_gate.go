package admin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/proto"
)

// cutStripsStoredGate refuses a save that removes a strip a step of the same payload still takes. The
// strip goes either by the list of an aware client or with its BOM line, and the line is the only way a
// client that does not know about strips can remove one. The seam half of the same rule is in the store,
// where the seam table is visible under the card lock.
func cutStripsStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if stored == nil || len(stored.CutStrips) == 0 {
		return nil
	}
	keptStrip := make(map[string]bool, len(pb.GetCutStrips()))
	for _, s := range pb.GetCutStrips() {
		keptStrip[entity.CutStripLineKey(s.GetLineKey())] = true
	}
	keptLine := make(map[string]bool, len(pb.GetBomItems()))
	for _, b := range pb.GetBomItems() {
		keptLine[entity.CutStripLineKey(b.GetLineKey())] = true
	}
	doomed := make(map[string]bool)
	for i := range stored.CutStrips {
		s := &stored.CutStrips[i]
		key := entity.CutStripLineKey(s.LineKey)
		if !keptLine[entity.CutStripLineKey(s.BomLineKey)] || (pb.GetCutStripsAware() && !keptStrip[key]) {
			doomed[key] = true
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	for i, op := range pb.GetOperations() {
		for j, k := range op.GetInputKeys() {
			if doomed[entity.CutStripLineKey(k)] {
				return apierr.Invalid(entity.NewFieldViolation(fmt.Sprintf("operations[%d].input_keys[%d]", i, j),
					"cut_strip_in_use", "", "this step uses the strip; remove it from the step first"))
			}
		}
	}
	return nil
}

// storedHasCutStripInputs reports whether a step of the stored card takes a strip.
func storedHasCutStripInputs(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if slices.ContainsFunc(stored.Operations[i].AssemblyInputs, func(in entity.OperationInput) bool {
			return in.Kind == entity.AssemblyInputStrip
		}) {
			return true
		}
	}
	return false
}

// cloneCutStripsWithoutSeams is the clone's copy of the strips: seams are not cloned, so no strip may name
// one, and the seam lengths stay as what the from_seam formula keeps working on. A strip whose seam is
// stale or rejected on the source (staleLines, by strip line key) has no length there; it loses its seam
// lengths, so the clone does not read them as known again. Manual cuts are kept.
func cloneCutStripsWithoutSeams(strips []*pb_common.TechCardCutStrip, staleLines map[string]bool) []*pb_common.TechCardCutStrip {
	out := make([]*pb_common.TechCardCutStrip, 0, len(strips))
	for _, s := range strips {
		c := proto.Clone(s).(*pb_common.TechCardCutStrip)
		c.SeamKey = ""
		if staleLines[entity.CutStripLineKey(c.GetLineKey())] {
			for _, size := range c.GetSizes() {
				size.SeamMm = nil
			}
		}
		out = append(out, c)
	}
	return out
}

// cutStripsChanged is the history chip's question: did this save change the strips. Numbers are compared
// by value, since a client may echo 100.00 for 100.
// staleSeamCutStrips is the set of strips (by line key) whose seam is stale or rejected on a read card.
func staleSeamCutStrips(strips []entity.TechCardCutStrip) map[string]bool {
	out := map[string]bool{}
	for i := range strips {
		if strips[i].SeamStale {
			out[entity.CutStripLineKey(strips[i].LineKey)] = true
		}
	}
	return out
}

func cutStripsChanged(stored []entity.TechCardCutStrip, in *entity.TechCardInsert) bool {
	if !in.CutStripsAware {
		return false
	}
	return !slices.Equal(cutStripSignature(stored), cutStripSignature(in.CutStrips))
}

func cutStripSignature(strips []entity.TechCardCutStrip) []string {
	out := make([]string, 0, len(strips))
	for i := range strips {
		s := &strips[i]
		var b strings.Builder
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%s|%v|%d|%d", entity.CutStripLineKey(s.LineKey), entity.CutStripLineKey(s.BomLineKey),
			s.LengthMode, s.SeamKey.String, s.FitPct.String(), s.OverlapMm.String(), s.WidthMm.String(), s.OneLengthMm, s.Qty, len(s.Sizes))
		for j := range s.Sizes {
			r := &s.Sizes[j]
			fmt.Fprintf(&b, "|%d:%v:%s:%v", r.SizeId, r.SeamMm.Valid, r.SeamMm.Decimal.String(), r.ManualCutMm)
		}
		out = append(out, b.String())
	}
	slices.Sort(out)
	return out
}
