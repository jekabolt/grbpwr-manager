package designgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE JOIN LIST IN WORDS — FOR THE PARTS LABELLER, NEVER FOR A FLAT ═══
//
// The card's join list (design_joins; GenerateDesignJoins / SetDesignJoins) once rode the flat prompt
// as a landmark ruler, the list and a block of CHECK sentences (tmp/plans/flat-consistency rounds 5–7,
// then behind one switch). Owner 06.10 / 07.10 (100-CONSTRUCTION-DEADEND): a dead end — a flat is drawn
// from the photos with their roles. The switch, the sentence generator and the frozen `inputs.joins`
// left with M7b; a flat prompt does not move by a byte for it (flat_prompt_bytes_test.go).
//
// What is left is the list's own text: the PARTS labeller reads it as its construction block, beside
// the part vocabulary (admin design_parts_construction.go), until M6 moves PARTS onto the approved
// FRONT / BACK sheet.

// JoinsUsable — whether a list says anything (at least one item or absence).
func JoinsUsable(j *entity.DesignJoinsDoc) bool {
	return j != nil && (len(j.Items) > 0 || len(j.Absences) > 0)
}

// JoinsListText — r5.list_text: the ruler, the layers (when the garment has more than one), every item
// on one line, the absences.
func JoinsListText(j entity.DesignJoinsDoc) string {
	L := []string{entity.DesignJoinLandmarkHelp, ""}
	if len(j.Layers) > 0 {
		L = append(L, "LAYERS (depth levels; 0 = outermost):")
		for _, l := range j.Layers {
			sheer := ""
			if l.Sheer {
				sheer = " — SHEER"
			}
			L = append(L, "- layer "+strconv.Itoa(l.Index)+": "+l.Name+sheer+". "+l.Note)
		}
		L = append(L, "")
	}
	L = append(L, "JOIN LIST (the construction; every view must agree with it):")
	for _, it := range j.Items {
		path := it.Path()
		switch it.Kind {
		case entity.DesignJoinKindPocket:
			t := it.Type
			if t == "" {
				t = "patch"
			}
			L = append(L, fmt.Sprintf("- %s: %s pocket at %s. %s", it.ID, t, it.From, it.Text))
		case entity.DesignJoinKindOpening:
			L = append(L, fmt.Sprintf("- %s: OPENING (no cloth) bounded by %s. %s", it.ID, strings.Join(it.BoundedBy, ", "), it.Text))
		case entity.DesignJoinKindClosure:
			count := ""
			if it.Count > 0 {
				count = strconv.Itoa(it.Count)
			}
			L = append(L, fmt.Sprintf("- %s: %s closure, %s along %s. %s", it.ID, it.Type, count, strings.Join(path, " → "), it.Text))
		default:
			width := ""
			if it.Width != "" {
				width = " (" + it.Width + ")"
			}
			closed := ""
			if it.Closed {
				closed = " (closed loop)"
			}
			cont := ""
			if len(it.ContinuesInto) > 0 {
				cont = " continues into " + strings.Join(it.ContinuesInto, ", ") + "."
			}
			L = append(L, strings.TrimRight(fmt.Sprintf("- %s: %s%s from %s%s.%s %s", it.ID, it.Kind, width, strings.Join(path, " → "), closed, cont, it.Text), " "))
		}
	}
	if len(j.Absences) > 0 {
		L = append(L, "ABSENT (do not draw): "+strings.Join(j.Absences, "; "))
	}
	return strings.Join(L, "\n")
}
