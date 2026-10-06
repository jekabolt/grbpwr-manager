package admin

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// designBoardIsTheSource — 101 §2.8 / Ф4: a run reads ONLY labels on pictures lying on the moodboard
// with the purpose `target` (a view) or `detail`. Until the REFERENCE rows are moved onto the board
// (Ф4), a person's label on a picture outside the board still travels, as before.
var designBoardIsTheSource = false

// designRunRefs — the design_reference rows a run (and the join list) may read:
//   - a settled label only (entity.DesignReferenceTravels: a role, state ok — pending / unsure / failed
//     rows and a person's «no view» never ride);
//   - a MODEL's label only while its picture lies on the board with the purpose the label belongs to —
//     a view on a `target`, `detail` on a `detail`: a label on a picture the person has since called
//     mood, or never confirmed as the garment, is the model's guess and stays home;
//   - with designBoardIsTheSource, a person's label the same way.
func designRunRefs(card *entity.TechCard, refs []entity.DesignReference) []entity.DesignReference {
	purposeOf := map[int]entity.TechCardMediaRole{}
	for _, p := range designBoardPictures(card) {
		purposeOf[p.MediaID] = p.Purpose
	}
	out := make([]entity.DesignReference, 0, len(refs))
	for _, r := range refs {
		if !entity.DesignReferenceTravels(r) {
			continue
		}
		if designBoardIsTheSource || entity.IsDesignLabelByModel(r.LabelSource) {
			purpose, onBoard := purposeOf[r.MediaId]
			if !onBoard || !designLabelFitsPurpose(r.Role, purpose) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// designLabelFitsPurpose — a view rides on a `target` picture, `detail` on a `detail` picture.
func designLabelFitsPurpose(role string, purpose entity.TechCardMediaRole) bool {
	if role == entity.DesignViewDetail {
		return purpose == entity.TechCardMediaRoleDetail
	}
	return purpose == entity.TechCardMediaRoleTarget
}
