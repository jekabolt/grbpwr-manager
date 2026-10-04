package admin

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────── T39: THE PROSE BRANCH WRITES THE DESCRIPTION FROM THE BOARD ───────────────
//
// Owner, item 39: «если DESCRIPTION пустой но мы заполнили картинки в MOODBOARD предложить
// пользователю сгенерировать дескрипшен исходя из картинок колаутов и кард дитейл».
//
// WHY THE PROSE BRANCH OF DraftDesignIdea AND NOT A NEW VERB. The prose branch (construction=false)
// already reads exactly these inputs — the board pictures as images, the callouts bound to their
// picture and spot — through the same money register, idempotency and AI ledger. Its old answer
// (three titled sections, parsed by head/mood-draft.tsx) has had NO caller since that file was
// removed: on 2026-10-04 neither the beta nor the master client calls DraftDesignIdea with
// construction=false (use-generation.ts defines the mutation and nothing invokes it). So the
// branch is repurposed instead of a proto change: construction=false now answers the concept &
// construction description itself, as plain text in `run.output_text`.
//
// ⚠ admin.proto still describes construction=false as "the three-section prose"; that comment is
// stale and is to be corrected with the next proto change (the wire is unchanged).

// draftDescriptionMaxSampleRunes — how much of the designer's own text is quoted as the language
// sample. Enough for a model to recognise a language, short enough not to read as content.
const draftDescriptionMaxSampleRunes = 240

// designCardDataOpen / designCardDataClose — the block the prose prompt wraps all card and board
// content in (T39 review 1). Named in the role, so the model knows where the data ends.
const (
	designCardDataOpen  = "<card_data>"
	designCardDataClose = "</card_data>"
)

// designNeutraliseDataTags — a person's text cannot close the data block early: any spelling of
// the two tags inside the content is rewritten so the block has exactly one end, ours.
func designNeutraliseDataTags(s string) string {
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "card_data") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(lower[i:], "</card_data>") {
			b.WriteString("(/card_data)")
			i += len("</card_data>")
			continue
		}
		if strings.HasPrefix(lower[i:], "<card_data>") {
			b.WriteString("(card_data)")
			i += len("<card_data>")
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// designReasonBoardHasNoPictures — the description is written FROM the pictures; a board without
// one attached picture is refused before any money moves.
const designReasonBoardHasNoPictures = "board_has_no_pictures"

const designBoardHasNoPicturesMsg = "the description is written from the moodboard pictures: put a picture on the moodboard first"

// designDescriptionLanguageLine — THE LANGUAGE RULE, as the last line of the user turn.
//
// The description is written in the language the designer already writes on this card (its
// concept, the notes on the board pictures, the aspect texts, the table callouts, the card note);
// with none of them, in English. The model is shown a short quoted sample rather than a language
// name: naming the language here would need a detector, and a quoted sample is exact for every
// language a designer may type. WORDS is unaffected — T03 still turns this into the English brief.
func designDescriptionLanguageLine(card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int) string {
	if sample := designDesignerTextSample(card, mood, attachedIDs); sample != "" {
		// The sample is data too: its quote marks and the data tags cannot be closed from inside.
		sample = designNeutraliseDataTags(strings.NewReplacer("«", "\"", "»", "\"").Replace(sample))
		return "Language: write the description in the same language as the designer's own words on this card, " +
			"for example: «" + sample + "». Do not translate them into English."
	}
	return "Language: write the description in English."
}

// designDesignerTextSample — the designer's own free text on the card, flattened and cut to
// draftDescriptionMaxSampleRunes. "" when none of it has a single letter (a callout reading
// "12 / 3,5" says nothing about a language).
//
// ONLY HUMAN-AUTHORED SOURCES (T39 review 2), in this order: the board note (concept + legacy
// mood note), the card note, then the callouts a person pinned on the board pictures. Left out on
// purpose: the aspects (accepted construction-draft aspects are AI text and carry no provenance
// column), the table callouts (the older construction draft proposed callouts and the client could
// accept them, again with no provenance), the card name (a label) and aspect keys (a fixed English
// vocabulary). A model-written line in English must not decide the language of a designer who
// writes in Russian.
//
// A board callout counts only when its picture is attached: the words of a picture that did not
// reach the model go nowhere, the sample included (the rule of designBoardPromptBody).
func designDesignerTextSample(card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int) string {
	var parts []string
	add := func(s string) {
		s = designOneLine(s)
		if s != "" && strings.IndexFunc(s, unicode.IsLetter) >= 0 {
			parts = append(parts, s)
		}
	}
	add(mood.GetNote())
	if card != nil {
		add(card.Notes.String)
	}
	attached := make(map[int32]bool, len(attachedIDs))
	for _, id := range attachedIDs {
		attached[int32(id)] = true
	}
	for _, c := range mood.GetCallouts() {
		if attached[c.GetMediaId()] {
			add(c.GetText())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return aiBoundedText(strings.Join(parts, " / "), draftDescriptionMaxSampleRunes)
}

// designDescriptionCardFacts — the card details the description is built from, besides the board:
// the aspects (silhouette, fabric and the rest, by the designer), the card's composition, the
// material slots and the callouts of the table. Bounded by the same row/line/byte ceilings as the
// construction draft's "already on the card", so a long card cannot blow the prompt.
func designDescriptionCardFacts(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	var b strings.Builder
	budget := designConstructionMaxAlreadyBytes
	write := func(line string) bool {
		if len(line) > budget {
			return false
		}
		budget -= len(line)
		b.WriteString(line)
		return true
	}
	rows := 0
	for _, d := range card.Details {
		key := aiBoundedText(strings.TrimSpace(d.Key.String), designConstructionMaxVarchar64)
		text := aiBoundedText(designOneLine(d.Text.String), designConstructionMaxAlreadyLineRunes)
		if key == "" || text == "" || rows >= designConstructionMaxAlreadyRows {
			continue
		}
		if write("- " + key + ": " + text + "\n") {
			rows++
		}
	}
	rows = 0
	for _, item := range card.BomItems {
		name := aiBoundedText(designOneLine(item.Name), designConstructionMaxAlreadyLineRunes)
		if name == "" || rows >= designConstructionMaxAlreadyRows {
			continue
		}
		line := "- material: " + string(item.Section) + " · " + name
		if comp := aiBoundedText(designOneLine(item.Composition.String),
			designConstructionMaxAlreadyLineRunes); comp != "" {
			line += " · " + comp
		}
		if write(line + "\n") {
			rows++
		}
	}
	rows = 0
	for _, c := range card.Callouts {
		if c.MediaId.Valid && c.MediaId.Int32 > 0 {
			continue // pinned on a board picture: the board body already carries it, bound to its spot
		}
		line := aiBoundedText(designOneLine(entity.TechCardCalloutPrintedLine(c)),
			designConstructionMaxAlreadyLineRunes)
		if line == "" || rows >= designConstructionMaxAlreadyRows {
			continue
		}
		if c.Number > 0 {
			line = "#" + strconv.Itoa(c.Number) + " " + line
		}
		if write("- callout: " + line + "\n") {
			rows++
		}
	}
	return b.String()
}
