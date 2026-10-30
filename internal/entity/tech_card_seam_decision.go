package entity

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// SEAM DECISIONS (tech_card_seam_decision, 0439) — what the technologist did with a seam guess when
// closing one join step of the seam picker / quiz: accepted it unchanged, saved their own pick,
// skipped the step, or refused a proposal. One immutable row per decision, appended beside the seams.
//
// tech_card_seam keeps only the OUTCOME: accept and own both land as source=manual and a skip leaves
// no row, so the guess cannot be measured from it. This log keeps the decision. It is not card
// content: no lock_version, no digest, no released-card refusal, and it does not travel with clone /
// archive / restore. It dies with its card (FK CASCADE).

// Bounds of one decision write. Field-addressed refusals name them; nothing here is a DB CHECK.
const (
	TechCardSeamDecisionMaxPerCall        = 50
	TechCardSeamDecisionShownMaxBytes     = 8 * 1024
	TechCardSeamDecisionClientVersionMax  = 40 // client_version VARCHAR(40)
	TechCardSeamDecisionCreatedByMaxRunes = 255
)

// TechCardSeamDecisionAction is the decision. Stored as these exact strings (VARCHAR(8)).
type TechCardSeamDecisionAction string

const (
	TechCardSeamDecisionAccept TechCardSeamDecisionAction = "accept"
	TechCardSeamDecisionOwn    TechCardSeamDecisionAction = "own"
	TechCardSeamDecisionSkip   TechCardSeamDecisionAction = "skip"
	TechCardSeamDecisionRefuse TechCardSeamDecisionAction = "refuse"
)

// TechCardSeamGuessTier is the tier of the guess the UI showed. "" = the client had none
// (stored as '' — VARCHAR(10) NOT NULL DEFAULT '').
type TechCardSeamGuessTier string

const (
	TechCardSeamGuessTierUnknown   TechCardSeamGuessTier = ""
	TechCardSeamGuessTierConfident TechCardSeamGuessTier = "confident"
	TechCardSeamGuessTierGuess     TechCardSeamGuessTier = "guess"
	TechCardSeamGuessTierNone      TechCardSeamGuessTier = "none"
)

var techCardSeamDecisionActions = map[TechCardSeamDecisionAction]bool{
	TechCardSeamDecisionAccept: true, TechCardSeamDecisionOwn: true,
	TechCardSeamDecisionSkip: true, TechCardSeamDecisionRefuse: true,
}

var techCardSeamGuessTiers = map[TechCardSeamGuessTier]bool{
	TechCardSeamGuessTierUnknown: true, TechCardSeamGuessTierConfident: true,
	TechCardSeamGuessTierGuess: true, TechCardSeamGuessTierNone: true,
}

// TechCardSeamDecisionInput is one decision as the client sends it.
type TechCardSeamDecisionInput struct {
	// OperationNumber is the step (operation_number of the card's operation); 0 = the card-level
	// seam review, which has no step.
	OperationNumber int                        `db:"operation_number"`
	Action          TechCardSeamDecisionAction `db:"action"`
	SeamKey         string                     `db:"seam_key"` // empty on skip
	Tier            TechCardSeamGuessTier      `db:"tier"`
	Shown           string                     `db:"-"` // client JSON or ""; stored NULL when ""
	ClientVersion   string                     `db:"client_version"`
}

// TechCardSeamDecision is one stored decision.
type TechCardSeamDecision struct {
	TechCardSeamDecisionInput
	Id         int64          `db:"id"`
	TechCardId int            `db:"tech_card_id"`
	ShownJSON  sql.NullString `db:"shown"`
	CreatedBy  string         `db:"created_by"`
	CreatedAt  time.Time      `db:"created_at"`
}

// TechCardSeamDecisionsWrite is one AddTechCardSeamDecisions call. By is stamped by the handler
// from the auth context; the store cannot know who is asking.
type TechCardSeamDecisionsWrite struct {
	TechCardId int
	Decisions  []TechCardSeamDecisionInput
	By         string
}

// ValidateTechCardSeamDecisionsWrite checks everything about a decision write that does not need
// the database. Errors are field-addressed (decisions[i].seam_key …). Normalises seam keys in place
// (trim + upper-case, the same canonical form the seams use). Card existence is the store's check.
func ValidateTechCardSeamDecisionsWrite(in *TechCardSeamDecisionsWrite) error {
	if in.TechCardId <= 0 {
		return NewFieldViolation("tech_card_id", "required", "", "")
	}
	if len(in.Decisions) == 0 {
		return NewFieldViolation("decisions", "empty", "", "send at least one decision")
	}
	if len(in.Decisions) > TechCardSeamDecisionMaxPerCall {
		return NewFieldViolation("decisions", "too_many", "",
			fmt.Sprintf("at most %d decisions per call", TechCardSeamDecisionMaxPerCall))
	}
	if utf8.RuneCountInString(in.By) > TechCardSeamDecisionCreatedByMaxRunes {
		return NewFieldViolation("created_by", "too_long", "", "")
	}
	for i := range in.Decisions {
		d := &in.Decisions[i]
		f := fmt.Sprintf("decisions[%d]", i)
		if d.OperationNumber < 0 {
			return NewFieldViolation(f+".operation_number", "invalid", "",
				"the step's operation_number, or 0 for the card-level seam review")
		}
		if !techCardSeamDecisionActions[d.Action] {
			return NewFieldViolation(f+".action", "invalid", "", "accept, own, skip or refuse")
		}
		d.SeamKey = NormalizeTechCardSeamKey(d.SeamKey)
		if d.Action == TechCardSeamDecisionSkip {
			if d.SeamKey != "" {
				return NewFieldViolation(f+".seam_key", "invalid", "", "a skip writes no seam row; send it empty")
			}
		} else {
			if d.SeamKey == "" {
				return NewFieldViolation(f+".seam_key", "required", "",
					"the seam row written or refused (accept / own / refuse)")
			}
			if !ValidTechCardSeamKey(d.SeamKey) {
				return NewFieldViolation(f+".seam_key", "invalid", "", "a 26-char ULID")
			}
		}
		if !techCardSeamGuessTiers[d.Tier] {
			return NewFieldViolation(f+".tier", "invalid", "", "confident, guess, none or unset")
		}
		if len(d.Shown) > TechCardSeamDecisionShownMaxBytes {
			return NewFieldViolation(f+".shown", "too_long", "",
				fmt.Sprintf("at most %d bytes", TechCardSeamDecisionShownMaxBytes))
		}
		if d.Shown != "" && !json.Valid([]byte(d.Shown)) {
			return NewFieldViolation(f+".shown", "invalid", "", "valid JSON or empty")
		}
		if !utf8.ValidString(d.ClientVersion) || utf8.RuneCountInString(d.ClientVersion) > TechCardSeamDecisionClientVersionMax {
			return NewFieldViolation(f+".client_version", "too_long", "",
				fmt.Sprintf("at most %d characters", TechCardSeamDecisionClientVersionMax))
		}
	}
	return nil
}
