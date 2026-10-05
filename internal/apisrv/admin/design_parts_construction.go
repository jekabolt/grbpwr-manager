package admin

import (
	"context"
	"fmt"
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

// designPartsThinKinds — the parts that are a THIN strip along an edge: only such a strip is a
// binding/band (f3: card 38's open-back triangles were called «armhole bind»).
var designPartsThinKinds = map[string]bool{
	entity.DesignJoinKindBinding: true, entity.DesignJoinKindBand: true, entity.DesignJoinKindBindingWide: true,
	entity.DesignJoinKindWaistband: true, entity.DesignJoinKindHoodEdge: true, entity.DesignJoinKindDrawcordChannel: true,
}

// designPartsBackPoints — ruler landmarks that lie on the back of the garment.
var designPartsBackPoints = map[string]bool{
	"CBN": true, "CBN_LOW": true, "HEM_BC": true, "UB_C": true, "MB_C": true, "WB_C": true, "HIP_C_B": true,
	"SKIRT_HEM_C_B": true, "CROTCH_B": true, "HOOD_BACK": true,
}

// designPartsCentreFront — ruler landmarks on the centre-front line below the neck: a closure or a
// placket running through one of them splits the front into a left and a right front.
var designPartsCentreFront = map[string]bool{
	"CHEST_C": true, "BUST_C": true, "WF_C": true, "HEM_FC": true, "BREAK": true, "HIP_C_F": true,
}

// designPartsPointBase — «NP_L..SP_L:0.3» → «NP_L».
func designPartsPointBase(p string) string {
	b, _, _ := strings.Cut(p, "..")
	return strings.TrimSpace(b)
}

// designPartsVocabEntry — one closed name and what the labeller must know about it.
type designPartsVocabEntry struct {
	name, what string
}

// designPartsVocabulary — the closed list of part names the construction allows (f3): the body
// panels, EVERY separately cut piece of the list (bands, bindings, straps, collar, cuffs, pockets…),
// every inner LAYER of joins.layers, and the openings. A painter must be able to paint each cut piece
// on its own, so none of them may be folded into a body panel.
func designPartsVocabulary(doc entity.DesignJoinsDoc) string {
	var entries []designPartsVocabEntry
	seen := map[string]bool{}
	add := func(n, what string) {
		if n != "" && !seen[n] {
			seen[n] = true
			entries = append(entries, designPartsVocabEntry{n, what})
		}
	}
	back, splitFront, splitBack := false, false, false
	for _, it := range doc.Items {
		// A strap or an opening crosses the back without being back cloth (a backless garment's
		// straps run through UB_C): neither says there is a back body.
		cloth := it.Kind != entity.DesignJoinKindStrap && it.Kind != entity.DesignJoinKindOpening
		if cloth && it.View == entity.DesignViewBack {
			back = true
		}
		bases := map[string]bool{}
		for _, p := range it.Path() {
			b := designPartsPointBase(p)
			bases[b] = true
			if cloth && designPartsBackPoints[b] {
				back = true
			}
		}
		lowFront := bases["HEM_FC"] || bases["WF_C"] || bases["HIP_C_F"]
		switch {
		case (it.Kind == entity.DesignJoinKindClosure || it.Kind == entity.DesignJoinKindPlacket) && lowFront:
			// A closure/placket down the whole front divides it; a polo/henley placket does not.
			splitFront = true
		case it.Kind == entity.DesignJoinKindSeam && (bases["CFN"] || bases["CFN_LOW"]) && lowFront:
			splitFront = true // a centre-front seam
		case it.Kind == entity.DesignJoinKindSeam && (bases["CBN"] || bases["CBN_LOW"]) &&
			(bases["HEM_BC"] || bases["WB_C"] || bases["HIP_C_B"]):
			splitBack, back = true, true // a centre-back seam
		}
	}
	for _, l := range doc.Layers {
		if l.Face == entity.DesignViewBack || l.Face == "both" {
			back = true
		}
	}
	if splitFront {
		add("left front body", "the body panel on the wearer's left of the centre front")
		add("right front body", "the body panel on the wearer's right of the centre front")
	} else {
		add("front body", "the outer front body panel")
	}
	switch {
	case splitBack:
		add("left back body", "the body panel on the wearer's left of the centre-back seam")
		add("right back body", "the body panel on the wearer's right of the centre-back seam")
	case back:
		add("back body", "the outer back body panel")
	}
	for _, it := range doc.Items {
		switch {
		case designPartsNamedKinds[it.Kind]:
			add(designPartsNameOf(it), designPartsWhatOf(it))
		case it.Kind == entity.DesignJoinKindSeam && designPartsHasToken(it.ID, "yoke"):
			// A yoke seam bounds a yoke panel: a cut piece of its own.
			n := "front yoke"
			for _, p := range it.Path() {
				if designPartsBackPoints[designPartsPointBase(p)] || strings.HasPrefix(designPartsPointBase(p), "YOKE_") {
					n = "back yoke"
				}
			}
			add(n, "a separately cut panel above the yoke seam ("+strings.Join(it.Path(), " → ")+")")
		}
	}
	for _, l := range doc.Layers {
		if l.Index <= 0 {
			continue // layer 0 is the outer shell: the body panels and the pieces above
		}
		n := designPartsLayerName(l)
		if designPartsBodySynonym[n] && (seen[n] || l.Face == entity.DesignViewBack && seen["back body"] ||
			l.Face == entity.DesignViewFront && seen["front body"]) {
			continue // the layer only restates a body panel the list already has (c38: «back panel»)
		}
		if seen[n] {
			n = fmt.Sprintf("%s %d", n, l.Index) // two layers, one head: each keeps its own key
		}
		what := fmt.Sprintf("layer %d", l.Index)
		if l.Face != "" {
			what += ", on the " + l.Face
		}
		what += ": a separately cut inner piece; every region where it shows"
		if designPartsLayerSeenThrough(doc, l.Index) {
			what += " (through the sheer outer layer)"
		}
		what += " is THIS part with this name on every view, never the outer body"
		add(n, what)
	}
	var lines []string
	for _, e := range entries {
		line := "- " + e.name
		if e.what != "" {
			line += " — " + e.what
		}
		lines = append(lines, line)
	}
	for _, it := range doc.Items {
		if it.Kind == entity.DesignJoinKindOpening {
			lines = append(lines, "- opening: bounded by "+strings.Join(it.BoundedBy, ", ")+" — no cloth: label it «opening», or put it in the part whose inside shows through it (seen_through)")
		}
	}
	return "PART VOCABULARY (closed — use exactly these names; every separately cut piece below is its own part with its own regions, never folded into a body panel; never name a part the construction does not have; a region with no cloth of its own is an opening or the inside of the part seen through it, never a binding or a band):\n" +
		strings.Join(lines, "\n")
}

// designPartsBodySynonym — layer names that only restate a body panel the vocabulary already has.
var designPartsBodySynonym = map[string]bool{
	"body": true, "front body": true, "back body": true, "front panel": true, "back panel": true,
	"outer body": true, "outer shell": true, "shell": true, "main body": true,
}

// designPartsLayerName — a layer's name as a part name: the head of its name (before «/», «(», «,»,
// «;»), lowercase, at most 3 words; «inner layer N» when nothing is left.
func designPartsLayerName(l entity.DesignJoinLayer) string {
	n := l.Name
	if i := strings.IndexAny(n, "/(,;"); i >= 0 {
		n = n[:i]
	}
	words := strings.Fields(strings.ToLower(n))
	if len(words) > 3 {
		words = words[:3]
	}
	if len(words) == 0 {
		return fmt.Sprintf("inner layer %d", l.Index)
	}
	return strings.Join(words, " ")
}

// designPartsLayerSeenThrough — whether the list says the layer shows through another one.
func designPartsLayerSeenThrough(doc entity.DesignJoinsDoc, index int) bool {
	for _, it := range doc.Items {
		if it.Layer == index && it.Visibility == "through" {
			return true
		}
	}
	for _, l := range doc.Layers {
		if l.Index < index && l.Sheer {
			return true
		}
	}
	return false
}

// designPartsHasToken — the id has this «_»-separated token (case-insensitive).
func designPartsHasToken(id, tok string) bool {
	for _, t := range strings.Split(id, "_") {
		if strings.EqualFold(t, tok) {
			return true
		}
	}
	return false
}

// designPartsWhatOf — what the labeller must know about one named piece.
func designPartsWhatOf(it entity.DesignJoinItem) string {
	path := it.Path()
	switch {
	case it.Kind == entity.DesignJoinKindStrap && len(path) > 0:
		start, end := path[0], path[len(path)-1]
		if s := designPartsNeckEnd(path); s != "" && s != start {
			start, end = s, path[0]
		}
		return "a strap cut as its own piece; it STARTS at " + start + " and ends at " + end +
			"; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side"
	case designPartsThinKinds[it.Kind]:
		w := ""
		if it.Width != "" {
			w = " (" + it.Width + ")"
		}
		return "a thin " + strings.ReplaceAll(it.Kind, "_", " ") + " strip" + w + " cut as its own piece, along " + strings.Join(path, " → ")
	default:
		return "cut as its own piece"
	}
}

// designPartsNeckEnd — the end of a path at the neck or the shoulder (NP_x / SP_x, also as the base
// of a point between two landmarks); "" when neither end is there.
func designPartsNeckEnd(path []string) string {
	if len(path) == 0 {
		return ""
	}
	for _, p := range []string{path[0], path[len(path)-1]} {
		b := designPartsPointBase(p)
		if strings.HasPrefix(b, "NP_") || strings.HasPrefix(b, "SP_") {
			return p
		}
	}
	return ""
}

// designPartsSideOfPoint — the wearer's side of a ruler point («NP_L..SP_L:0.3» → left).
func designPartsSideOfPoint(p string) string {
	switch b := designPartsPointBase(p); {
	case strings.HasSuffix(b, "_L"):
		return "left"
	case strings.HasSuffix(b, "_R"):
		return "right"
	}
	return ""
}

// designPartsWordOf — id abbreviations spelled out («slv» → «sleeve», «bind» → «binding»).
var designPartsWordOf = map[string]string{
	"slv": "sleeve", "bind": "binding", "btn": "button", "pkt": "pocket", "wb": "waistband",
}

// designPartsNameOf — a part name from an item id: «strap_from_L» → «left strap», «neck_bind» →
// «neck binding»; the wearer's side from the id's L/R token, else from the item's side. A STRAP is
// named by the shoulder where it starts at the neck point (f3): the side of its neck end wins.
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
			w := strings.ToLower(tok)
			if full, ok := designPartsWordOf[w]; ok {
				w = full
			}
			words = append(words, w)
		}
	}
	if it.Kind == entity.DesignJoinKindStrap {
		if s := designPartsSideOfPoint(designPartsNeckEnd(it.Path())); s != "" {
			side = s
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
	// «neck binding front» → «front neck binding»: the face word leads, like the body panels.
	for i, w := range words {
		if i > 0 && (w == "front" || w == "back") {
			words = append([]string{w}, append(append([]string{}, words[:i]...), words[i+1:]...)...)
			break
		}
	}
	if len(words) == 1 && words[0] == entity.DesignJoinKindStand {
		words = []string{"collar", "stand"}
	}
	n := strings.Join(words, " ")
	if side != "" {
		n = side + " " + n
	}
	return n
}
