package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"
)

// Moodboard quiz (0389): the questions the model asked about one garment and the designer's answers.
// Vocabularies are closed in Go (no ENUM, no CHECK — the design band rule).
const (
	DesignQuizCategoryDesign    = "design"
	DesignQuizCategoryFit       = "fit"
	DesignQuizCategoryDetails   = "details"
	DesignQuizCategoryMaterials = "materials"
	DesignQuizCategoryUse       = "use"
	DesignQuizCategoryFinish    = "finish"

	DesignQuizKindSingle = "single"
	DesignQuizKindMulti  = "multi"

	DesignQuizViewFront = "front"
	DesignQuizViewBack  = "back"
	DesignQuizViewSideL = "side_l"

	DesignQuizPartWhole = "whole"
)

// IsDesignQuizCategory reports whether v is one of the six quiz categories.
func IsDesignQuizCategory(v string) bool {
	switch v {
	case DesignQuizCategoryDesign, DesignQuizCategoryFit, DesignQuizCategoryDetails, DesignQuizCategoryMaterials,
		DesignQuizCategoryUse, DesignQuizCategoryFinish:
		return true
	}
	return false
}

// IsDesignQuizKind reports whether v is single or multi.
func IsDesignQuizKind(v string) bool { return v == DesignQuizKindSingle || v == DesignQuizKindMulti }

// IsDesignQuizView reports whether v is a pictogram view the quiz draws.
func IsDesignQuizView(v string) bool {
	return v == DesignQuizViewFront || v == DesignQuizViewBack || v == DesignQuizViewSideL
}

// DesignQuizQuestion is one question as asked (and stored with its answer, so a re-run and an edit
// need nothing else). Contradicts is parallel to Options (empty = all false).
type DesignQuizQuestion struct {
	ID              string
	Category        string
	Part            string
	Family          string
	View            string
	Kind            string
	Question        string
	Options         []string
	Contradicts     []bool
	VisualEvidence  string
	ClarifyQuestion string
	ClarifyOptions  []string
	// DecisionKey — snake_case key of the DECISION (not the wording), "" = none (0394, 64-DEFERRED E1).
	DecisionKey string
	// MediaID — the moodboard picture (tech card media id) this question is about, 0 = not a picture
	// question (0398, 96-PICTURE-QUESTIONS). Set → Part is "whole", no pictogram.
	MediaID int
}

// TechCardQuizAnswer is one stored answer: the question plus what the designer chose.
type TechCardQuizAnswer struct {
	Question   DesignQuizQuestion
	Selected   []string
	FreeText   string
	Skipped    bool
	AnsweredAt time.Time
	// Fingerprint — the card's structured-input fingerprint when this answer was last saved (0392,
	// 62-DEEP-FIXES D1). "" = saved before 0392 or imported: counts as fresh.
	Fingerprint string
	// Topic — the ONE kind of card fact this answer depends on (0399, 98-STALE §1): fit, materials,
	// construction, design or picture; "" on rows saved before 0399.
	Topic string
	// Facts — the topic's facts AT ANSWER TIME (0399), human-labelled, in card order. nil = saved before
	// 0399 (legacy: staleness falls back to Fingerprint); an empty non-nil list is a real snapshot.
	Facts []DesignQuizFact
	// Stale — derived on read, never stored: the topic's facts changed since the answer was given (or,
	// for a legacy row, Fingerprint != "" and differs from the card's current fingerprint).
	Stale bool
	// StaleChanges — derived on read with Stale: one line per changed fact ("main fabric: cotton twill
	// → wool flannel"), at most DesignQuizMaxStaleLines then "+N more"; empty when fresh (98-STALE §3).
	StaleChanges []string
}

// Moodboard quiz per-topic staleness (98-STALE §1). The vocabulary is closed in Go (no ENUM/CHECK).
const (
	DesignQuizTopicFit          = "fit"
	DesignQuizTopicMaterials    = "materials"
	DesignQuizTopicConstruction = "construction"
	DesignQuizTopicDesign       = "design"
	DesignQuizTopicPicture      = "picture"
)

// Picture-topic fact labels: the picture's «picture N» number when the snapshot was taken (metadata,
// never compared — a re-numbered board is not a change), whether it is on the board, and its role.
const (
	DesignQuizFactPictureNumber = "#"
	DesignQuizFactPictureBoard  = "board"
	DesignQuizFactPictureRole   = "role"

	DesignQuizPictureOnBoard = "on the board"
	DesignQuizPictureRemoved = "removed"
	DesignQuizRoleNone       = "none"
)

// DesignQuizMaxStaleLines — change lines listed per stale answer before "+N more".
const DesignQuizMaxStaleLines = 4

// DesignQuizLegacyStaleLine — the one change line of a stale row saved before per-topic tracking.
const DesignQuizLegacyStaleLine = "the card changed (answered before per-topic tracking)"

// DesignQuizFact is one labelled card fact a quiz answer depended on ("main fabric" → "cotton twill").
type DesignQuizFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// DesignQuizFactChanges — what changed between the facts an answer was given against (old) and the
// topic's facts now (cur), one line per changed fact, compared by label (order-insensitive, so a
// re-ordered BOM is not a change): "label: old → new", added "label: — → new", removed "label: old →
// —". The picture topic renders its own lines: "picture N: removed from the board", "picture N role:
// mood → material". The full list; DesignQuizCapLines bounds it for the wire.
func DesignQuizFactChanges(topic string, old, cur []DesignQuizFact) []string {
	if topic == DesignQuizTopicPicture {
		return designQuizPictureChanges(old, cur)
	}
	curBy := make(map[string]string, len(cur))
	for _, f := range cur {
		curBy[f.Label] = f.Value
	}
	oldBy := make(map[string]bool, len(old))
	var out []string
	for _, f := range old {
		oldBy[f.Label] = true
		v, ok := curBy[f.Label]
		switch {
		case !ok:
			out = append(out, f.Label+": "+f.Value+" → —")
		case v != f.Value:
			out = append(out, f.Label+": "+f.Value+" → "+v)
		}
	}
	for _, f := range cur {
		if !oldBy[f.Label] {
			out = append(out, f.Label+": — → "+f.Value)
		}
	}
	return out
}

func designQuizPictureChanges(old, cur []DesignQuizFact) []string {
	get := func(fs []DesignQuizFact, label string) string {
		for _, f := range fs {
			if f.Label == label {
				return f.Value
			}
		}
		return ""
	}
	name := func(fs []DesignQuizFact) string {
		if n := get(fs, DesignQuizFactPictureNumber); n != "" {
			return "picture " + n
		}
		return "the picture"
	}
	wasOn := get(old, DesignQuizFactPictureBoard) == DesignQuizPictureOnBoard
	isOn := get(cur, DesignQuizFactPictureBoard) == DesignQuizPictureOnBoard
	switch {
	case wasOn && !isOn:
		return []string{name(old) + ": removed from the board"}
	case !wasOn && isOn:
		return []string{name(cur) + ": back on the board"}
	case !isOn:
		return nil
	}
	if o, c := get(old, DesignQuizFactPictureRole), get(cur, DesignQuizFactPictureRole); o != c {
		return []string{name(cur) + " role: " + o + " → " + c}
	}
	return nil
}

// DesignQuizCapLines — at most DesignQuizMaxStaleLines lines, then "+N more".
func DesignQuizCapLines(lines []string) []string {
	if len(lines) <= DesignQuizMaxStaleLines {
		return lines
	}
	out := append([]string(nil), lines[:DesignQuizMaxStaleLines]...)
	return append(out, "+"+strconv.Itoa(len(lines)-DesignQuizMaxStaleLines)+" more")
}

// DesignQuizIsStale — the one staleness rule: an answer saved under a fingerprint that is no longer
// the card's. An empty fingerprint on either side is fresh (pre-0392 rows; a current fingerprint
// that could not be computed).
func DesignQuizIsStale(answerFingerprint, current string) bool {
	return answerFingerprint != "" && current != "" && answerFingerprint != current
}

// MarkDesignQuizStale sets Stale and StaleChanges on every answer (98-STALE §1/§3). A row with a
// facts snapshot is stale when its topic's facts now (factsOf(topic, media id)) differ from the
// snapshot; a legacy row (Facts nil) falls back to the whole-card fingerprint against current, with
// the one DesignQuizLegacyStaleLine. factsOf nil = the current facts are unknown: every row with a
// snapshot is fresh (an unreadable chart degrades to fresh, never to a refusal).
func MarkDesignQuizStale(answers []TechCardQuizAnswer, current string, factsOf func(topic string, mediaID int) []DesignQuizFact) {
	for i := range answers {
		a := &answers[i]
		a.Stale, a.StaleChanges = false, nil
		if a.Facts == nil {
			if DesignQuizIsStale(a.Fingerprint, current) {
				a.Stale, a.StaleChanges = true, []string{DesignQuizLegacyStaleLine}
			}
			continue
		}
		if factsOf == nil {
			continue
		}
		if lines := DesignQuizFactChanges(a.Topic, a.Facts, factsOf(a.Topic, a.Question.MediaID)); len(lines) > 0 {
			a.Stale, a.StaleChanges = true, DesignQuizCapLines(lines)
		}
	}
}

// DesignQuizAnswersDigest — the token the DESIGN sign-off digest appends for the quiz (62-DEEP-FIXES
// D2): sha256 over (question id, selected, free text, skipped) of every answer, in display order.
// "" when the card has none, so a card without answers keeps a byte-identical DESIGN digest.
// answered_at and the fingerprint are deliberately out: re-confirming the same answer is not an edit.
func DesignQuizAnswersDigest(answers []TechCardQuizAnswer) string {
	if len(answers) == 0 {
		return ""
	}
	rows := make([][]any, 0, len(answers))
	for _, a := range answers {
		sel := a.Selected
		if sel == nil {
			sel = []string{}
		}
		rows = append(rows, []any{a.Question.ID, sel, a.FreeText, a.Skipped})
	}
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ErrDesignQuizTooManyAnswers — a save would leave more answers on the card than the cap allows.
var ErrDesignQuizTooManyAnswers = errors.New("too many quiz answers on the card")

// DesignQuizSession — the card's last generated question list (0394, 64-DEFERRED E2), so a quiz
// resumes on another tab or device. ClosedAt nil = the open one (one per card).
type DesignQuizSession struct {
	ID         int
	TechCardID int
	Family     string
	Questions  []DesignQuizQuestion
	CreatedBy  string
	CreatedAt  time.Time
	ClosedAt   *time.Time
}

// DesignQuizPending — the session's questions not yet saved on the card: every question whose id is
// among the saved rows (answered or skipped) is out, and so is every question whose non-empty
// decision key a saved row carries — a save that superseded (forgot) an older row by key must not
// bring that row's question back. The generated order is kept.
func DesignQuizPending(questions []DesignQuizQuestion, saved []TechCardQuizAnswer) []DesignQuizQuestion {
	done := make(map[string]bool, len(saved))
	keys := make(map[string]bool, len(saved))
	for _, a := range saved {
		done[a.Question.ID] = true
		if k := a.Question.DecisionKey; k != "" {
			keys[k] = true
		}
	}
	out := make([]DesignQuizQuestion, 0, len(questions))
	for _, q := range questions {
		if !done[q.ID] && (q.DecisionKey == "" || !keys[q.DecisionKey]) {
			out = append(out, q)
		}
	}
	return out
}

// MergeDesignQuizAnswers is the NON-DESTRUCTIVE save (61-QUICKWINS W-B1): the stored list prev with
// every row of upserts written over the stored row of the same question id (in place) or appended
// (in request order), and every id in forget removed. A stored row the request does not name STAYS —
// a client that saves before its list has loaded can no longer erase the card's history.
//
// SUPERSESSION (64-DEFERRED E1): an upsert carrying a decision key forgets every OTHER row (another
// question id) with the same key — the decision was re-asked in other words, the latest answer wins.
// Two upserts of one key in a request: the later in request order wins. "" never supersedes.
//
// answered_at is the server's: an answer that comes back unchanged (same selection, free text and
// skip) keeps the time it was first given; a new or changed one is stamped now.
func MergeDesignQuizAnswers(prev, upserts []TechCardQuizAnswer, forget []string, now time.Time) []TechCardQuizAnswer {
	gone := make(map[string]bool, len(forget))
	for _, id := range forget {
		gone[id] = true
	}
	stamp := func(p *TechCardQuizAnswer, a TechCardQuizAnswer) TechCardQuizAnswer {
		a.AnsweredAt = now
		if p != nil && p.Skipped == a.Skipped && p.FreeText == a.FreeText && slices.Equal(p.Selected, a.Selected) {
			a.AnsweredAt = p.AnsweredAt
		}
		return a
	}
	byID := make(map[string]TechCardQuizAnswer, len(upserts))
	for _, a := range upserts {
		byID[a.Question.ID] = a
	}
	out := make([]TechCardQuizAnswer, 0, len(prev)+len(upserts))
	placed := make(map[string]bool, len(prev))
	for _, p := range prev {
		id := p.Question.ID
		if gone[id] || placed[id] {
			continue
		}
		placed[id] = true
		if a, ok := byID[id]; ok {
			out = append(out, stamp(&p, a))
			continue
		}
		out = append(out, p)
	}
	for _, a := range upserts {
		id := a.Question.ID
		if gone[id] || placed[id] {
			continue
		}
		placed[id] = true
		out = append(out, stamp(nil, a))
	}
	owner := make(map[string]string, len(upserts))
	for _, a := range upserts {
		if k := a.Question.DecisionKey; k != "" && !gone[a.Question.ID] {
			owner[k] = a.Question.ID
		}
	}
	if len(owner) == 0 {
		return out
	}
	kept := out[:0]
	for _, a := range out {
		if id, ok := owner[a.Question.DecisionKey]; ok && a.Question.DecisionKey != "" && id != a.Question.ID {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}
