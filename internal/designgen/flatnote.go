package designgen

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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
// What a flat keeps: the garment class (one «garment: …» line) and — only behind
// FlatWordsCarryDescription, off — every clause of the description that is not about material /
// colour / lining / inside / hidden finishing / fit ease. (The join list and its CHECK sentences rode
// here too until the owner called construction a dead end, 06.10 / 07.10; gone with M7b.)

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
	`\w*-?weight|gsm|stretch\w*|2-way|4-way|breathab\w*|translucen\w*|opacity|opaque|cling\w*|colou?rs?|shell|soft|tailored drape`

var (
	flatHardJunkRe = regexp.MustCompile(`(?i)\b(` + flatHardJunk + `)\b`)
	flatSoftJunkRe = regexp.MustCompile(`(?i)\b(` + flatSoftJunk + `)\b`)
)

// flatCleanClause — the clause as a flat says it, "" when nothing drawable is left.
func flatCleanClause(c string) string {
	if c == "" || flatHardJunkRe.MatchString(c) {
		return ""
	}
	if flatSoftJunkRe.MatchString(c) {
		// whole clauses only (owner, wave 10): stripping words left fragments («shell with soft tailored»)
		return ""
	}
	return c
}

// flatDropLabelRe — a card-facts line whose label is not construction (card-facts.ts cardFactLines).
var flatDropLabelRe = regexp.MustCompile(`(?i)^(fit|age group|for|fabric|materials?|colou?rs?|colou?rways?|lining|notes on the board)\s*:`)

// flatDecisionLineRe — the quiz decisions as fact lines (card-facts.ts decisionFactLines) or as the
// server block (designQuizImageBlock).
var flatDecisionLineRe = regexp.MustCompile(`(?i)^(decided ·|unconfirmed ·|decided with the designer|earlier quiz answers|\(\+\d+ more decisions)`)

var flatGarmentLineRe = regexp.MustCompile(`(?i)^garment\s*:\s*(.*)$`)

// FlatWordsCarryDescription — whether a flat's garment note carries the card description's clauses
// (filtered) or only the «garment: <class>» line. OFF (owner 06.10): WORDS are seeded by a model brief
// (EnhanceText) and the card stores no author per sentence, so model-written text («slim body through
// the waist», invisible «welt pockets hidden in the side seams») would reach the image model as fact.
// Back on only once WORDS carry a human/model provenance.
var FlatWordsCarryDescription = false

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
		// Wave 10 (owner): the description is written by a model as often as by a person, and the
		// store cannot tell which — so a flat sends none of it, only the class (FlatWordsCarryDescription).
		if !FlatWordsCarryDescription {
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

// FlatGarmentNote — THE WORDS A FLAT RUN SENDS (M14, owner 07.10: «показывай в WORDS только то, что
// уходит»): the class line of the card's description (FlatConstructionNote — the description's
// other words stay home, they have no author) and then the person's own flat words
// (TechCard.flat_words — typed in FLAT › WORDS and written by nothing else), as typed. A card with
// no flat words sends exactly what it sent before. The client draws the same text in the WORDS box
// and in «what the model gets» (flat-route.ts flatWordsSent).
func FlatGarmentNote(description, human string) string {
	note := FlatConstructionNote(description)
	words := FlatHumanWords(human)
	switch {
	case words == "":
		return note
	case note == "":
		return words
	}
	return note + "\n" + words
}

// FlatHumanWords — the person's flat words as they travel: each line trimmed, blank lines dropped,
// nothing else touched (they are a person's, so no junk filter rewrites them).
func FlatHumanWords(human string) string {
	var lines []string
	for _, line := range strings.Split(human, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
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
