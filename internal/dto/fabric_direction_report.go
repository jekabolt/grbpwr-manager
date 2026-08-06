package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// FabricDirectionGapReportToPb puts the кампания Д1 worklist (Ф1.8) on the wire.
//
// Section and назначение travel as the SAME enums the BOM tab reads (pbBomSection / pbBomPurpose),
// so an unrecognised stored value degrades exactly the way it degrades on the card read instead of
// growing a second vocabulary here. purpose comes off a plain string because the report's own
// loader already collapsed NULL to "" — sql.NullString reconstructs the distinction pbBomPurpose
// expects, and "" is the UNSET a line predating 0265 legitimately carries.
func FabricDirectionGapReportToPb(r entity.FabricDirectionGapReport) *pb_admin.ListTechCardFabricDirectionGapsResponse {
	out := &pb_admin.ListTechCardFabricDirectionGapsResponse{
		Cards:         make([]*pb_admin.FabricDirectionGapCard, 0, len(r.Cards)),
		TotalCards:    int32(r.TotalCards),
		TotalLines:    int32(r.TotalLines),
		Excluded:      make([]*pb_admin.FabricDirectionGapExclusion, 0, len(r.Excluded)),
		ExcludedCards: int32(r.ExcludedCards),
		ExcludedLines: int32(r.ExcludedLines),
	}
	for _, c := range r.Cards {
		card := &pb_admin.FabricDirectionGapCard{
			TechCardId:         int32(c.TechCardID),
			StyleNumber:        c.StyleNumber,
			Name:               c.Name,
			Stage:              pbTechCardStage(entity.TechCardStage(c.Stage)),
			ApprovalState:      pbTechCardApprovalState(entity.TechCardApprovalState(c.ApprovalState)),
			MarkerSavePossible: c.MarkerSavePossible(),
			BlockedMarkerCount: int32(c.BlockedMarkerCount()),
			LinkedMarkerCount:  int32(c.LinkedMarkerCount),
			HasPatterns:        c.HasPatterns,
			Lines:              make([]*pb_admin.FabricDirectionGapLine, 0, len(c.Lines)),
		}
		for _, l := range c.Lines {
			card.Lines = append(card.Lines, &pb_admin.FabricDirectionGapLine{
				BomItemId:          l.BomItemID,
				LineKey:            l.LineKey,
				Name:               l.Name,
				Section:            pbBomSection(entity.TechCardBomSection(l.Section)),
				Purpose:            pbBomPurpose(sql.NullString{String: l.Purpose, Valid: l.Purpose != ""}),
				IsSample:           l.IsSample,
				BlockedMarkerCount: int32(l.BlockedMarkerCount),
			})
		}
		out.Cards = append(out.Cards, card)
	}
	for _, e := range r.Excluded {
		out.Excluded = append(out.Excluded, &pb_admin.FabricDirectionGapExclusion{
			ApprovalState: pbTechCardApprovalState(entity.TechCardApprovalState(e.ApprovalState)),
			Cards:         int32(e.Cards),
			Lines:         int32(e.Lines),
		})
	}
	return out
}
