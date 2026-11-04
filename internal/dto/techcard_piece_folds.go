package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PIECE FOLDS — wire ↔ domain. The folds travel on the read model (TechCard.piece_folds = 34) and
// through their own RPCs only; nothing here touches TechCardInsert, so no section digest sees them.

var pieceFoldKindFromPb = map[pb_common.TechCardPieceFoldKind]entity.TechCardPieceFoldKind{
	pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_INWARD:      entity.TechCardPieceFoldInward,
	pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_OUTWARD:     entity.TechCardPieceFoldOutward,
	pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_PLEAT:       entity.TechCardPieceFoldPleat,
	pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_PLEAT_LEFT:  entity.TechCardPieceFoldPleatLeft,
	pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_PLEAT_RIGHT: entity.TechCardPieceFoldPleatRight,
}

var pieceFoldSourceFromPb = map[pb_common.TechCardPieceFoldSource]entity.TechCardPieceFoldSource{
	pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_DXF:           entity.TechCardPieceFoldSourceDXF,
	pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_OWNER:         entity.TechCardPieceFoldSourceOwner,
	pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_ACCEPTED_HINT: entity.TechCardPieceFoldSourceAcceptedHint,
}

var (
	pieceFoldKindToPb   = invertSeamMap(pieceFoldKindFromPb)
	pieceFoldSourceToPb = invertSeamMap(pieceFoldSourceFromPb)
)

// TechCardPieceFoldsWriteFromPb converts an UpsertTechCardPieceFolds payload and validates it (shape,
// dictionaries, bounds — everything but piece membership and stored-seq uniqueness, which the store
// checks). Output-only fields (stale, *_by, *_at) are ignored; UNKNOWN kind / source are refused.
func TechCardPieceFoldsWriteFromPb(techCardID int, folds []*pb_common.TechCardPieceFold, by string) (entity.TechCardPieceFoldsWrite, error) {
	// BOUNDS BEFORE ANY COPY (the seams' rule): counts and key lengths on the wire messages themselves.
	if len(folds) > entity.TechCardPieceFoldMaxRowsPerCard {
		return entity.TechCardPieceFoldsWrite{}, entity.NewFieldViolation("folds", "too_many", "",
			fmt.Sprintf("at most %d folds per card", entity.TechCardPieceFoldMaxRowsPerCard))
	}
	for i, f := range folds {
		at := fmt.Sprintf("folds[%d]", i)
		if len(f.GetLine()) != entity.TechCardPieceFoldLineSamples {
			return entity.TechCardPieceFoldsWrite{}, entity.NewFieldViolation(at+".line", "count", "",
				fmt.Sprintf("exactly %d points", entity.TechCardPieceFoldLineSamples))
		}
		if n := len(f.GetEdgeSamples()); n != 0 && n != entity.TechCardPieceFoldEdgeSamples {
			return entity.TechCardPieceFoldsWrite{}, entity.NewFieldViolation(at+".edge_samples", "count", "",
				fmt.Sprintf("none or exactly %d samples along the edge", entity.TechCardPieceFoldEdgeSamples))
		}
		if len(f.GetPieceLineKey()) > entity.TechCardSeamPieceKeyMaxBytes {
			return entity.TechCardPieceFoldsWrite{}, entity.NewFieldViolation(at+".piece_line_key", "too_long", "", "the piece's line_key")
		}
	}
	in := entity.TechCardPieceFoldsWrite{TechCardId: techCardID, By: by, Folds: make([]entity.TechCardPieceFoldInput, 0, len(folds))}
	for i, f := range folds {
		at := fmt.Sprintf("folds[%d]", i)
		if f == nil {
			return in, entity.NewFieldViolation(at, "required", "", "")
		}
		kind, ok := pieceFoldKindFromPb[f.GetKind()]
		if !ok {
			return in, entity.NewFieldViolation(at+".kind", "invalid", f.GetKind().String(), "INWARD, OUTWARD, PLEAT, PLEAT_LEFT or PLEAT_RIGHT")
		}
		src, ok := pieceFoldSourceFromPb[f.GetSource()]
		if !ok {
			return in, entity.NewFieldViolation(at+".source", "invalid", f.GetSource().String(), "DXF, OWNER or ACCEPTED_HINT")
		}
		var flip entity.TechCardSeamSample
		if p := f.GetFlipAt(); p != nil {
			flip = entity.TechCardSeamSample{U: p.GetU(), V: p.GetV()}
		} else {
			return in, entity.NewFieldViolation(at+".flip_at", "required", "", "a point on the part that flips")
		}
		in.Folds = append(in.Folds, entity.TechCardPieceFoldInput{
			FoldKey:      f.GetFoldKey(),
			PieceLineKey: f.GetPieceLineKey(),
			Seq:          int(f.GetSeq()),
			Kind:         kind,
			Source:       src,
			Line:         pieceFoldSamplesFromPb(f.GetLine()),
			FlipAt:       flip,
			EdgeHint:     f.GetEdgeHint(),
			EdgeSamples:  pieceFoldSamplesFromPb(f.GetEdgeSamples()),
			OffsetMm:     f.GetOffsetMm(),
			ContourSig:   f.GetContourSig(),
			AnchoredSize: f.GetAnchoredSize(),
			Note:         f.GetNote(),
		})
	}
	if err := entity.ValidateTechCardPieceFoldsWrite(&in); err != nil {
		return in, err
	}
	return in, nil
}

func pieceFoldSamplesFromPb(in []*pb_common.TechCardSeamSample) []entity.TechCardSeamSample {
	if len(in) == 0 {
		return nil
	}
	out := make([]entity.TechCardSeamSample, 0, len(in))
	for _, s := range in {
		out = append(out, entity.TechCardSeamSample{U: s.GetU(), V: s.GetV()})
	}
	return out
}

func pieceFoldSamplesToPb(in []entity.TechCardSeamSample) []*pb_common.TechCardSeamSample {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardSeamSample, 0, len(in))
	for _, s := range in {
		out = append(out, &pb_common.TechCardSeamSample{U: s.U, V: s.V})
	}
	return out
}

// TechCardPieceFoldsToPb emits stored folds in store order ((piece, seq)), with the server's stale
// verdict. A stored dictionary value this binary does not know maps to UNKNOWN rather than failing
// the card read.
func TechCardPieceFoldsToPb(folds []entity.TechCardPieceFold) []*pb_common.TechCardPieceFold {
	if len(folds) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardPieceFold, 0, len(folds))
	for _, f := range folds {
		out = append(out, &pb_common.TechCardPieceFold{
			FoldKey:      f.FoldKey,
			PieceLineKey: f.PieceLineKey,
			Seq:          int32(f.Seq),
			Kind:         pieceFoldKindToPb[f.Kind],
			Line:         pieceFoldSamplesToPb(f.Line),
			FlipAt:       &pb_common.TechCardSeamSample{U: f.FlipAt.U, V: f.FlipAt.V},
			EdgeHint:     f.EdgeHint,
			EdgeSamples:  pieceFoldSamplesToPb(f.EdgeSamples),
			OffsetMm:     f.OffsetMm,
			ContourSig:   f.ContourSig,
			Source:       pieceFoldSourceToPb[f.Source],
			AnchoredSize: f.AnchoredSize,
			Note:         f.Note,
			Stale:        f.Stale,
			CreatedBy:    f.CreatedBy,
			CreatedAt:    timestamppb.New(f.CreatedAt),
			UpdatedBy:    f.UpdatedBy,
			UpdatedAt:    timestamppb.New(f.UpdatedAt),
		})
	}
	return out
}
