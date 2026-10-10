package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CONFIRMED SEAMS — wire ↔ domain. The seams travel on the read model (TechCard.seams = 32) and
// through their own RPCs only; nothing here touches TechCardInsert, so no section digest sees them.

var seamStatusFromPb = map[pb_common.TechCardSeamStatus]entity.TechCardSeamStatus{
	pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_CONFIRMED: entity.TechCardSeamConfirmed,
	pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_REJECTED:  entity.TechCardSeamRejected,
}

var seamKindFromPb = map[pb_common.TechCardSeamKind]entity.TechCardSeamKind{
	pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_EDGE:      entity.TechCardSeamKindEdge,
	pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_PARTIAL:   entity.TechCardSeamKindPartial,
	pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_COMPOSITE: entity.TechCardSeamKindComposite,
	pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_SURFACE:   entity.TechCardSeamKindSurface,
	pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_CLOSURE:   entity.TechCardSeamKindClosure,
}

var seamDirectionFromPb = map[pb_common.TechCardSeamDirection]entity.TechCardSeamDirection{
	pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_UNKNOWN:  entity.TechCardSeamDirectionUnknown,
	pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_REVERSED: entity.TechCardSeamReversed,
	pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_SAME:     entity.TechCardSeamSame,
}

var seamSourceFromPb = map[pb_common.TechCardSeamSource]entity.TechCardSeamSource{
	pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_GRAPH:  entity.TechCardSeamSourceGraph,
	pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_DOLL:   entity.TechCardSeamSourceDoll,
	pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_ORDER:  entity.TechCardSeamSourceOrder,
	pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_MANUAL: entity.TechCardSeamSourceManual,
	pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_AI:     entity.TechCardSeamSourceAI,
}

func invertSeamMap[K comparable, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	seamStatusToPb    = invertSeamMap(seamStatusFromPb)
	seamKindToPb      = invertSeamMap(seamKindFromPb)
	seamDirectionToPb = invertSeamMap(seamDirectionFromPb)
	seamSourceToPb    = invertSeamMap(seamSourceFromPb)
)

// TechCardSeamsWriteFromPb converts an UpsertTechCardSeams payload and validates it (shape,
// dictionaries, bounds — everything but piece membership, which the store checks). Output-only fields
// (stale, *_by, *_at) are ignored. UNKNOWN status / kind / source are refused with the field named;
// UNKNOWN direction is legal («the reader picks the pairing»).
func TechCardSeamsWriteFromPb(techCardID int, seams []*pb_common.TechCardSeam, by string) (entity.TechCardSeamsWrite, error) {
	in := entity.TechCardSeamsWrite{TechCardId: techCardID, By: by, Seams: make([]entity.TechCardSeamInput, 0, len(seams))}
	for i, s := range seams {
		f := fmt.Sprintf("seams[%d]", i)
		if s == nil {
			return in, entity.NewFieldViolation(f, "required", "", "")
		}
		st, ok := seamStatusFromPb[s.GetStatus()]
		if !ok {
			return in, entity.NewFieldViolation(f+".status", "invalid", s.GetStatus().String(), "CONFIRMED or REJECTED")
		}
		kind, ok := seamKindFromPb[s.GetKind()]
		if !ok {
			return in, entity.NewFieldViolation(f+".kind", "invalid", s.GetKind().String(), "EDGE, PARTIAL, COMPOSITE, SURFACE or CLOSURE")
		}
		dir, ok := seamDirectionFromPb[s.GetDirection()]
		if !ok {
			return in, entity.NewFieldViolation(f+".direction", "invalid", s.GetDirection().String(), "REVERSED, SAME or UNKNOWN")
		}
		src, ok := seamSourceFromPb[s.GetSource()]
		if !ok {
			return in, entity.NewFieldViolation(f+".source", "invalid", s.GetSource().String(), "GRAPH, DOLL, ORDER, MANUAL or AI")
		}
		in.Seams = append(in.Seams, entity.TechCardSeamInput{
			SeamKey:      s.GetSeamKey(),
			Status:       st,
			Kind:         kind,
			Direction:    dir,
			Source:       src,
			SideA:        seamSideFromPb(s.GetSideA()),
			SideB:        seamSideFromPb(s.GetSideB()),
			AnchoredSize: s.GetAnchoredSize(),
			Note:         s.GetNote(),
		})
	}
	if err := entity.ValidateTechCardSeamsWrite(&in); err != nil {
		return in, err
	}
	return in, nil
}

func seamSideFromPb(side *pb_common.TechCardSeamSide) []entity.TechCardSeamAnchor {
	parts := side.GetParts()
	out := make([]entity.TechCardSeamAnchor, 0, len(parts))
	for _, p := range parts {
		samples := make([]entity.TechCardSeamSample, 0, len(p.GetSamples()))
		for _, sm := range p.GetSamples() {
			samples = append(samples, entity.TechCardSeamSample{U: sm.GetU(), V: sm.GetV()})
		}
		out = append(out, entity.TechCardSeamAnchor{
			PieceLineKey: p.GetPieceLineKey(),
			Samples:      samples,
			PerimShare:   p.GetPerimShare(),
			LenMm:        p.GetLenMm(),
			Notches:      p.GetNotches(),
			TurnDeg:      p.GetTurnDeg(),
			RangeFrom:    p.GetRangeFrom(),
			RangeTo:      p.GetRangeTo(),
			EdgeHint:     p.GetEdgeHint(),
			ContourSig:   p.GetContourSig(),
		})
	}
	return out
}

func seamSideToPb(side []entity.TechCardSeamAnchor) *pb_common.TechCardSeamSide {
	parts := make([]*pb_common.TechCardSeamAnchor, 0, len(side))
	for _, a := range side {
		samples := make([]*pb_common.TechCardSeamSample, 0, len(a.Samples))
		for _, sm := range a.Samples {
			samples = append(samples, &pb_common.TechCardSeamSample{U: sm.U, V: sm.V})
		}
		parts = append(parts, &pb_common.TechCardSeamAnchor{
			PieceLineKey: a.PieceLineKey,
			Samples:      samples,
			PerimShare:   a.PerimShare,
			LenMm:        a.LenMm,
			Notches:      a.Notches,
			TurnDeg:      a.TurnDeg,
			RangeFrom:    a.RangeFrom,
			RangeTo:      a.RangeTo,
			EdgeHint:     a.EdgeHint,
			ContourSig:   a.ContourSig,
		})
	}
	return &pb_common.TechCardSeamSide{Parts: parts}
}

// TechCardSeamsToPb emits stored seams in store order (insertion), with the server's stale verdict.
// A stored dictionary value this binary does not know maps to UNKNOWN rather than failing the card
// read — a newer binary's row must not take the whole card down on a rollback.
func TechCardSeamsToPb(seams []entity.TechCardSeam) []*pb_common.TechCardSeam {
	if len(seams) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardSeam, 0, len(seams))
	for _, s := range seams {
		out = append(out, &pb_common.TechCardSeam{
			SeamKey:      s.SeamKey,
			Status:       seamStatusToPb[s.Status],
			Kind:         seamKindToPb[s.Kind],
			Direction:    seamDirectionToPb[s.Direction],
			Source:       seamSourceToPb[s.Source],
			SideA:        seamSideToPb(s.SideA),
			SideB:        seamSideToPb(s.SideB),
			AnchoredSize: s.AnchoredSize,
			Note:         s.Note,
			Stale:        s.Stale,
			CreatedBy:    s.CreatedBy,
			CreatedAt:    timestamppb.New(s.CreatedAt),
			UpdatedBy:    s.UpdatedBy,
			UpdatedAt:    timestamppb.New(s.UpdatedAt),
		})
	}
	return out
}
