package entity

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ─── Board labels (101-MOODBOARD-ROLES, wave 11) ───
//
// The moodboard is the one place pictures live; a design_reference row is the SERVER'S LABEL on a
// board picture: which view of the garment it shows (front / back / side_l / side_r / side) or which
// detail slot it belongs to. A cheap model labels a new picture, a strong model takes the unclear
// ones, and what neither is sure of waits for a person (a question card under the board). A label a
// person set is never touched by a model again.
//
// What a model READ about a picture (model_caption) never reaches a prompt — only the label word
// (the view, or the detail slot's name) does (101 §2.7, lesson 51).

// DesignViewSide — a side view whose left / right is not known (101 Q2: no question asked; the prompt
// says «side photo»). A reference role only — never a bench slot or a drawn view.
const DesignViewSide = "side"

// IsDesignReferenceRole — the vocabulary of design_reference.role: a ghost view (the six silhouette
// sides + detail) or the uncertain `side`.
func IsDesignReferenceRole(v string) bool {
	return v == DesignViewSide || IsDesignGhostView(v)
}

// Who set a label (design_reference.label_source). ” = a row older than the column — read as human.
const (
	DesignLabelSourceHuman       = "human"
	DesignLabelSourceModelCheap  = "model_cheap"
	DesignLabelSourceModelStrong = "model_strong"
	DesignLabelSourceQuiz        = "quiz"
)

// IsDesignLabelByModel — a label a model owns, which the server may relabel or drop. Everything else
// (human, quiz, the ” of a legacy row) is a person's and is never touched by a model.
func IsDesignLabelByModel(source string) bool {
	return source == DesignLabelSourceModelCheap || source == DesignLabelSourceModelStrong
}

// The state of a label (design_reference.label_state). Only `ok` travels to a run.
const (
	DesignLabelStatePending = "pending" // a model call is owed or in flight
	DesignLabelStateOk      = "ok"
	DesignLabelStateUnsure  = "unsure" // both models were unsure — a person answers the question card
	DesignLabelStateFailed  = "failed" // AI off / errors — a person sets the view by a tap
)

// DesignLabelStateOrOk — ” (a row older than the column) reads as ok.
func DesignLabelStateOrOk(v string) string {
	if v == "" {
		return DesignLabelStateOk
	}
	return v
}

// DesignReferenceTravels — whether a design_reference row may feed a run: a role is set and the label
// is settled (`ok`). Pending / unsure / failed rows carry an empty role, but the state is checked too:
// a legacy three-quarter row moved to `unsure` keeps its role and must not ride.
func DesignReferenceTravels(r DesignReference) bool {
	return strings.TrimSpace(r.Role) != "" && DesignLabelStateOrOk(r.LabelState) == DesignLabelStateOk
}

// DesignBoardLabelMaxAttempts — a pending label older than DesignBoardLabelStaleAfter is re-queued
// lazily (a restart lost its goroutine); after this many attempts it becomes `failed`.
const (
	DesignBoardLabelMaxAttempts = 2
	DesignBoardLabelStaleAfter  = 2 * time.Minute
)

// DesignBoardLabelBegin — claim a board picture for labelling: a pending row is written (or an old
// model row re-armed) under the rules of the store's BeginBoardLabel.
type DesignBoardLabelBegin struct {
	TechCardId int
	MediaId    int
	// Relabel — the picture's purpose changed under a MODEL label: drop the old answer and start over.
	Relabel bool
	// StaleBefore — a pending row whose labelled_at is before this is a lost task and may be re-armed.
	StaleBefore time.Time
	Actor       string
}

// DesignBoardLabel — a model's settled answer for one board picture. Written only over a row that is
// still the model's and still pending (a person's tap in the meantime wins).
type DesignBoardLabel struct {
	TechCardId      int
	MediaId         int
	Role            string // a view, `detail`, or '' (unsure / failed / not a target)
	DetailSlotId    int    // role=detail: an existing slot of the card
	NewDetailName   string // role=detail, DetailSlotId=0: mint a model slot with this name in the same tx
	Source          string // model_cheap | model_strong
	State           string // ok | unsure | failed
	ProposedPurpose string // target | detail | mood | material | ''
	ModelCaption    string // what the model read; never sent to a prompt
	LabelModel      string // the slug that answered
}

// Board purposes a model may propose (the tech_card_media.role vocabulary, less the empty word).
func IsDesignProposedPurpose(v string) bool {
	switch TechCardMediaRole(v) {
	case TechCardMediaRoleTarget, TechCardMediaRoleDetail, TechCardMediaRoleMaterial, TechCardMediaRoleMood:
		return true
	}
	return false
}

// DesignBoardLabelAnswer — the parsed JSON of a board label / board read call. Lenient by design: a
// field the model got wrong is dropped to its zero value rather than failing the whole answer.
type DesignBoardLabelAnswer struct {
	Purpose    string  // target | detail | mood | material | ''
	View       string  // front | back | side_l | side_r | side | unclear | ''
	Confidence float64 // 0..1
	LRSure     bool
	Why        string // ≤ 12 words; the strong model's reason (model_caption)
	// Detail read (101 §2.5): an existing slot id, or 0 with Slot == "new".
	Slot      string
	SlotID    int
	Name      string
	Caption   string
	HasLRSure bool
}

// ParseDesignBoardLabelAnswer reads the model's JSON object (code fences and prose around it are
// tolerated — the first {...} is taken). ok=false only when no object can be read at all.
func ParseDesignBoardLabelAnswer(raw string) (DesignBoardLabelAnswer, bool) {
	var out DesignBoardLabelAnswer
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return out, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return out, false
	}
	str := func(k string) string {
		v, ok := m[k]
		if !ok || v == nil {
			return ""
		}
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
		return ""
	}
	out.Purpose = strings.ToLower(str("purpose"))
	if !IsDesignProposedPurpose(out.Purpose) {
		out.Purpose = ""
	}
	out.View = normaliseDesignLabelView(str("view"))
	switch c := m["confidence"].(type) {
	case float64:
		out.Confidence = c
	case string:
		out.Confidence, _ = strconv.ParseFloat(strings.TrimSpace(c), 64)
	}
	if out.Confidence > 1 && out.Confidence <= 100 {
		out.Confidence /= 100
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		out.Confidence = 0
	}
	switch b := m["lr_sure"].(type) {
	case bool:
		out.LRSure, out.HasLRSure = b, true
	case string:
		out.LRSure, out.HasLRSure = strings.EqualFold(strings.TrimSpace(b), "true"), true
	}
	out.Why = designLabelWords(str("why"), 16)
	out.Caption = designLabelWords(str("caption"), 60)
	out.Name = normaliseDesignDetailName(str("name"))
	switch v := m["slot"].(type) {
	case float64:
		if v > 0 && v == float64(int(v)) {
			out.SlotID = int(v)
		}
	case string:
		t := strings.ToLower(strings.TrimSpace(v))
		if t == "new" {
			out.Slot = "new"
		} else if n, err := strconv.Atoi(t); err == nil && n > 0 {
			out.SlotID = n
		}
	}
	return out, true
}

// normaliseDesignLabelView maps the model's spelling onto the label vocabulary; anything else is
// `unclear`.
func normaliseDesignLabelView(v string) string {
	t := strings.ToLower(strings.TrimSpace(v))
	t = strings.NewReplacer(" ", "_", "-", "_").Replace(t)
	switch t {
	case DesignViewFront, DesignViewBack, DesignViewSideL, DesignViewSideR, DesignViewSide:
		return t
	case "left", "left_side", "side_left":
		return DesignViewSideL
	case "right", "right_side", "side_right":
		return DesignViewSideR
	}
	return "unclear"
}

// normaliseDesignDetailName — a detail slot name as a model may give it: lowercase, ≤ 3 words, letters,
// digits, spaces and hyphens only (101 §2.5).
func normaliseDesignDetailName(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '\t':
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 3 {
		words = words[:3]
	}
	return strings.Join(words, " ")
}

// DesignDetailNameKey — the dedup key of a detail name: lowercase words joined by one space.
func DesignDetailNameKey(v string) string {
	return strings.Join(strings.Fields(strings.ToLower(v)), " ")
}

func designLabelWords(v string, max int) string {
	w := strings.Fields(v)
	if len(w) > max {
		w = w[:max]
	}
	return strings.Join(w, " ")
}
