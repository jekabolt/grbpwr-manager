package admin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE LABELLER'S CONSTRUCTION BLOCK (80-BUILD-MODES §6) ═══
//
// The card's join list becomes the construction the parts labeller trusts, and a CLOSED part
// vocabulary: a region that matches none of the names is an `opening` or the nearest body panel,
// never a new name (card 38 got an «upper back» it does not have).

// designPartsCardConstruction — the card's join list in words + the part vocabulary; "" when the
// card has no usable list or it cannot be read (the block helps, it never blocks the call).
func (s *Server) designPartsCardConstruction(ctx context.Context, cardID int) string {
	j, err := s.repo.Design().GetJoins(ctx, cardID)
	if err != nil {
		slog.Default().WarnContext(ctx, "design parts card: the join list is not read",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return ""
	}
	if j == nil {
		return ""
	}
	return designPartsConstructionText(j.Doc)
}

// designPartsConstructionText — the pure half: the list (designgen.JoinsListText) and the vocabulary.
func designPartsConstructionText(doc entity.DesignJoinsDoc) string {
	if !designgen.JoinsUsable(&doc) {
		return ""
	}
	return designgen.JoinsListText(doc) + "\n\n" + designPartsVocabulary(doc)
}

// designPartsNamedKinds — the kinds that are a physical part of their own (a band of cloth, a sleeve,
// a pocket); edges and seams bound parts, they are not parts.
var designPartsNamedKinds = map[string]bool{
	entity.DesignJoinKindBinding: true, entity.DesignJoinKindBand: true, entity.DesignJoinKindStrap: true,
	entity.DesignJoinKindCollar: true, entity.DesignJoinKindStand: true, entity.DesignJoinKindPlacket: true,
	entity.DesignJoinKindCuff: true, entity.DesignJoinKindWaistband: true, entity.DesignJoinKindSleeve: true,
	entity.DesignJoinKindPocket: true, entity.DesignJoinKindHoodEdge: true, entity.DesignJoinKindDrawcordChannel: true,
	entity.DesignJoinKindBindingWide: true,
}

// designPartsVocabulary — the closed list of part names the construction allows.
func designPartsVocabulary(doc entity.DesignJoinsDoc) string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	front, back := false, false
	for _, it := range doc.Items {
		if it.View == entity.DesignViewFront {
			front = true
		}
		if it.Kind == entity.DesignJoinKindEdge || it.Kind == entity.DesignJoinKindSeam {
			for _, p := range it.Path() {
				if b, _, _ := strings.Cut(p, ".."); b == "CBN" || b == "CBN_LOW" || b == "HEM_BC" {
					back = true
				}
			}
		}
	}
	if front {
		add("front body")
	}
	if back {
		add("back body")
	}
	for _, it := range doc.Items {
		if designPartsNamedKinds[it.Kind] {
			add(designPartsNameOf(it))
		}
	}
	var lines []string
	for _, n := range names {
		lines = append(lines, "- "+n)
	}
	for _, it := range doc.Items {
		if it.Kind == entity.DesignJoinKindOpening {
			lines = append(lines, "- opening: bounded by "+strings.Join(it.BoundedBy, ", "))
		}
	}
	return "PART VOCABULARY (closed — use these names; never name a part the construction does not have; a region that matches none of them is an opening or the nearest body panel):\n" +
		strings.Join(lines, "\n")
}

// designPartsNameOf — a part name from an item id: «strap_from_L» → «left strap», «neck_binding» →
// «neck binding»; the wearer's side from the id's L/R token, else from the item's path.
func designPartsNameOf(it entity.DesignJoinItem) string {
	side := ""
	var words []string
	for _, tok := range strings.Split(it.ID, "_") {
		switch tok {
		case "":
		case "L", "l":
			side = "left"
		case "R", "r":
			side = "right"
		case "from", "to", "From", "To":
		default:
			words = append(words, strings.ToLower(tok))
		}
	}
	if side == "" {
		switch it.Side {
		case "L":
			side = "left"
		case "R":
			side = "right"
		}
	}
	if len(words) == 0 {
		words = []string{strings.ReplaceAll(it.Kind, "_", " ")}
	}
	n := strings.Join(words, " ")
	if side != "" {
		n = side + " " + n
	}
	return n
}
