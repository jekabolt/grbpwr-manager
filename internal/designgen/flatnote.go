package designgen

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ A FLAT IS DRAWN FROM CONSTRUCTION ONLY (owner 06.10, wave 10: «максимально уберем мусор из промпта») ═══
//
// A technical flat is black lines on white: the outside of the garment, its pieces, seams, edges,
// closures, pockets, vents, hem, neckline and sleeves. The card's description (WORDS) and the quiz
// decisions were written for every kind of run, so a flat prompt carried the fabric («rustic slubby
// linen»), the lining, the inside chest pocket, the fit philosophy («our existing block», «easy chest
// room»), the finishing nobody can see (overlocked, pressed open, blind hemmed) and the Q&A phrasing
// of the quiz — and the same fact two or three times. None of it can be drawn; some of it the model
// drew anyway (a lining line, a slubby texture, an inside pocket on the front).
//
// What a flat keeps: the garment class (one «garment: …» line), every clause of the description
// that is not about material / colour / lining / inside / hidden finishing / fit ease, the JOIN LIST
// and the CHECK sentences (73-AB-LAYOUT: the long list is what holds the back — never shorten that),
// minus the join items that are hidden by definition (lining, inside pockets) and the layers left
// with nothing to draw.

// TWO KINDS OF JUNK (Codex review, wave 10). A clause naming the HIDDEN or the fit philosophy is dropped
// whole («inside chest pocket on the lining», «regular fit with easy chest room», «pressed open with
// overlocked edges»): keeping its other words would draw what cannot be seen. A clause naming only a
// MATERIAL / colour / feel loses those words but keeps its construction when it has some («linen
// jacket with patch pockets» → «jacket with patch pockets»); without a construction word it goes whole
// («mid-weight rustic linen with a slubby surface»).
//
// NOT junk on purpose: knit / rib (a «rib band» is a visible band), mesh (a visible panel), sheer and
// inner (a sheer layer and what shows through it are drawn — 38's dashed inner V), fitted (a shape),
// draping (a neckline «draping at the chest» is a cut), pad (a shoulder pad shapes the shoulder line).
const flatHardJunk = `lining|lined|unlined|interlin\w*|interfac\w*|fus(ed|ible)|wadding|inside|interior|facings?|` +
	`overlock\w*|serg(ed|er)|pressed open|blind\w*|bagged|` +
	`fit|ease|easy|room|block|movement|compression|regular|colou?rways?`

const flatSoftJunk = `linen|cotton|wool|silk|cashmere|denim|jersey|fleece|nylon|polyester|elastane|spandex|lycra|viscose|rayon|leather|suede|twill|poplin|canvas|` +
	`fabrics?|textiles?|textur\w*|slub\w*|rustic|drapes?|soft hand|hand[- ]?feel|matte|sheen|glossy|lustr\w*|` +
	`\w*-?weight|gsm|stretch\w*|2-way|4-way|breathab\w*|translucen\w*|opacity|opaque|cling\w*|colou?rs?`

var (
	flatHardJunkRe = regexp.MustCompile(`(?i)\b(` + flatHardJunk + `)\b`)
	flatSoftJunkRe = regexp.MustCompile(`(?i)\b(` + flatSoftJunk + `)\b`)
	// a soft junk token with its hyphenated compound («self-fabric», «cotton-elastane», «mid-weight»)
	flatSoftTokenRe = regexp.MustCompile(`(?i)[\w-]*\b(` + flatSoftJunk + `)\b[\w-]*`)
	// a construction word: what a flat draws
	flatConstructionRe = regexp.MustCompile(`(?i)\b(pockets?|zip\w*|buttons?|snaps?|seams?|collars?|lapels?|sleeves?|hems?|vents?|yokes?|darts?|pleats?|plackets?|cuffs?|straps?|necklines?|bands?|bindings?|closures?|panels?|hoods?|waistbands?|drawcords?|tabs?|belts?|loops?|epaulettes?|flaps?|welts?|gussets?|ruffles?|frills?|slits?|gores?|godets?|tucks?|gathers?|shirring|smocking|topstitch\w*|stitch\w*|piping|trims?|edges?|waist\w*|shaping|silhouette|jacket|blazer|coat|shirt|top|trousers|pants|shorts|dress|skirt|tank)\b`)
	flatSpacesRe       = regexp.MustCompile(`\s{2,}`)
)

// flatCleanClause — the clause as a flat says it, "" when nothing drawable is left.
func flatCleanClause(c string) string {
	if c == "" || flatHardJunkRe.MatchString(c) {
		return ""
	}
	if !flatSoftJunkRe.MatchString(c) {
		return c
	}
	c = flatSoftTokenRe.ReplaceAllString(c, "")
	c = strings.TrimSpace(flatSpacesRe.ReplaceAllString(c, " "))
	for _, dangling := range []string{" in", " with", " of", " and", " a", " an", " the"} {
		c = strings.TrimSuffix(c, dangling)
	}
	c = strings.TrimSpace(c)
	// What is left stays when it names construction, or says enough on its own (three content words:
	// «blazer with a curved front edge» yes, «soft tailored» / «knit with mild» no).
	if flatConstructionRe.MatchString(c) || flatContentWords(c) >= 3 {
		return c
	}
	return ""
}

var flatStopWords = map[string]bool{"a": true, "an": true, "the": true, "with": true, "and": true, "of": true,
	"in": true, "on": true, "at": true, "to": true, "over": true, "for": true, "by": true, "its": true}

func flatContentWords(c string) int {
	n := 0
	for _, w := range strings.Fields(c) {
		if !flatStopWords[strings.ToLower(w)] {
			n++
		}
	}
	return n
}

// flatDropLabelRe — a card-facts line whose label is not construction (card-facts.ts cardFactLines).
var flatDropLabelRe = regexp.MustCompile(`(?i)^(fit|age group|for|fabric|materials?|colou?rs?|colou?rways?|lining|notes on the board)\s*:`)

// flatDecisionLineRe — the quiz decisions as fact lines (card-facts.ts decisionFactLines) or as the
// server block (designQuizImageBlock).
var flatDecisionLineRe = regexp.MustCompile(`(?i)^(decided ·|unconfirmed ·|decided with the designer|earlier quiz answers|\(\+\d+ more decisions)`)

var flatGarmentLineRe = regexp.MustCompile(`(?i)^garment\s*:\s*(.*)$`)

// FlatConstructionNote — the card's garment note as a flat reads it: «garment: <class>» on the first
// line (once), then the description's construction clauses. "" when nothing is left.
func FlatConstructionNote(note string) string {
	var class string
	var body []string
	seen := map[string]bool{}
	for _, line := range strings.Split(note, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" {
			continue
		}
		if m := flatGarmentLineRe.FindStringSubmatch(line); m != nil {
			// «garment:» alone (an empty class) or a second class line says nothing new.
			if class == "" {
				class = strings.TrimSpace(m[1])
			}
			continue
		}
		if flatDropLabelRe.MatchString(line) || flatDecisionLineRe.MatchString(line) {
			continue
		}
		if t := flatConstructionText(line, seen); t != "" {
			body = append(body, t)
		}
	}
	out := strings.Join(body, "\n")
	if class != "" {
		if out == "" {
			return "garment: " + class
		}
		return "garment: " + class + "\n" + out
	}
	return out
}

// flatConstructionText — the sentences of free text with their junk clauses dropped. A clause is a
// piece between «;» and «,» inside a sentence; a one-word list head left in front of a dropped clause
// («hem, front edge and sleeve openings blind hemmed» → «hem») goes with it. `seen` drops a sentence
// already said (case- and space-folded).
func flatConstructionText(text string, seen map[string]bool) string {
	var sentences []string
	for _, s := range flatSentences(text) {
		body := strings.TrimRight(s, ".!?")
		end := s[len(body):]
		var groups []string
		for _, g := range strings.Split(body, ";") {
			if kept := flatKeepClauses(strings.Split(g, ",")); kept != "" {
				groups = append(groups, kept)
			}
		}
		if len(groups) == 0 {
			continue
		}
		out := strings.Join(groups, "; ")
		if out == strings.TrimSpace(body) {
			out = s // nothing dropped: the sentence verbatim
		} else {
			// something dropped: the sentence still starts with a capital (rune-safe) and keeps its end
			r, n := utf8.DecodeRuneInString(out)
			out = string(unicode.ToUpper(r)) + out[n:] + end
		}
		key := strings.ToLower(strings.Join(strings.Fields(out), " "))
		if seen != nil {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		sentences = append(sentences, out)
	}
	return strings.Join(sentences, " ")
}

func flatKeepClauses(clauses []string) string {
	keep := make([]bool, len(clauses))
	for i, c := range clauses {
		c = flatCleanClause(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(c), ".")))
		clauses[i] = c
		keep[i] = c != ""
	}
	for i := range clauses {
		if keep[i] && i+1 < len(clauses) && !keep[i+1] && len(strings.Fields(clauses[i])) < 2 {
			keep[i] = false
		}
	}
	var out []string
	for i, c := range clauses {
		if keep[i] {
			out = append(out, c)
		}
	}
	return strings.Join(out, ", ")
}

// flatSentences — text cut after «. », «! », «? », each sentence with its own end (a dot inside «0.4»
// followed by no space stays).
func flatSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c == '.' || c == '!' || c == '?') && (i+1 == len(text) || text[i+1] == ' ') {
			if s := strings.TrimSpace(text[start : i+1]); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(text[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// flatVisibleJoins — the join list as a flat prompt says it: hidden items (lining, an inside pocket)
// go, and every reference to them (continues into, caught into, bounded by) with them; a layer left
// with no item to draw goes, and so does the LAYERS block when one layer is left (one layer says
// nothing); the junk clauses leave the layer notes and the model's item texts (a designer's edited
// text stays verbatim — it is also a «designer:» CHECK line).
func flatVisibleJoins(j entity.DesignJoinsDoc) entity.DesignJoinsDoc {
	out := j
	gone := map[string]bool{}
	out.Items = make([]entity.DesignJoinItem, 0, len(j.Items))
	for _, it := range j.Items {
		if it.Visibility == entity.DesignJoinHidden {
			gone[it.ID] = true
			continue
		}
		out.Items = append(out.Items, it)
	}
	prune := func(ids []string) []string {
		if len(ids) == 0 {
			return ids
		}
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if !gone[id] {
				kept = append(kept, id)
			}
		}
		return kept
	}
	drawn := map[int]bool{}
	for i := range out.Items {
		it := &out.Items[i]
		it.ContinuesInto = prune(it.ContinuesInto)
		it.CaughtInto = prune(it.CaughtInto)
		it.BoundedBy = prune(it.BoundedBy)
		if !it.Edited {
			it.Text = flatConstructionText(it.Text, nil)
		}
		drawn[it.Layer] = true
	}
	var layers []entity.DesignJoinLayer
	for _, l := range j.Layers {
		if !drawn[l.Index] && l.Index != 0 {
			continue
		}
		l.Note = flatConstructionText(l.Note, nil)
		layers = append(layers, l)
	}
	if len(layers) <= 1 {
		layers = nil
	}
	out.Layers = layers
	return out
}
