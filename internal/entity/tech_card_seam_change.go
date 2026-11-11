package entity

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ONE SEAM GESTURE, ONE WRITE (ChangeTechCardSeams). Rows upserted, linked to steps, detached from
// steps and deleted in one transaction under the card's lock_version — unlike the old Upsert/Delete
// RPCs, which never touch the version. A step is addressed by its operation_number: safe only because
// the client flushes its autosave first and the server checks the version, so a step reordered in
// between moves the version and the call is refused.

// ErrTechCardLockVersionMoved: ChangeTechCardSeams was sent with an expected_lock_version that is no
// longer the card's. Nothing was written; the client re-reads the card and repeats the gesture.
var ErrTechCardLockVersionMoved = errors.New("lock_version_moved")

// TechCardSeamLink is one step → seam link: the step by its operation_number on the card.
type TechCardSeamLink struct {
	OperationNumber int
	SeamKey         string
}

// TechCardSeamsChange is one ChangeTechCardSeams call.
type TechCardSeamsChange struct {
	TechCardId int
	// ExpectedLockVersion is required; nil is refused before anything is read.
	ExpectedLockVersion *int
	Upsert              []TechCardSeamInput
	Delete              []string
	Link                []TechCardSeamLink
	Detach              []TechCardSeamLink
	By                  string
}

// TechCardSeamKeyMap is the key a sent upsert row was stored under.
type TechCardSeamKeyMap struct {
	Sent   string
	Stored string
}

// TechCardOperationSeamKeys is one numbered step with its linked seam keys in sewing order.
type TechCardOperationSeamKeys struct {
	OperationNumber int
	SeamKeys        []string
}

// TechCardSeamsChangeResult is what the change committed, read in its own transaction.
type TechCardSeamsChangeResult struct {
	Seams       []TechCardSeam
	Links       []TechCardOperationSeamKeys
	LockVersion int
	KeyMap      []TechCardSeamKeyMap
	DeletedKeys []string
}

// TechCardSeamChangeMaxRefs bounds each of delete / link / detach: one gesture names a handful of
// rows; a card holds at most TechCardSeamMaxRowsPerCard.
const TechCardSeamChangeMaxRefs = TechCardSeamMaxRowsPerCard

// ValidateTechCardSeamsChange checks everything about a change that does not need the database and
// normalises it in place: upsert rows through ValidateTechCardSeamsWrite (field paths say
// upsert[i]), keys upper-cased in delete / link / detach. Contradictions refuse the whole
// request: the same (step, key) in link and detach, a key in link and in delete, an upsert key in
// delete. The store adds what needs the card: steps exist and are unique, link keys exist, anchors
// name the card's pieces.
func ValidateTechCardSeamsChange(in *TechCardSeamsChange) error {
	if in.TechCardId <= 0 {
		return NewFieldViolation("tech_card_id", "required", "", "")
	}
	if in.ExpectedLockVersion == nil {
		return NewFieldViolation("expected_lock_version", "required", "",
			"send the card's lock_version as last read")
	}
	if len(in.Upsert) == 0 && len(in.Delete) == 0 && len(in.Link) == 0 && len(in.Detach) == 0 {
		return NewFieldViolation("upsert", "empty", "", "name at least one upsert, delete, link or detach")
	}
	if len(in.Upsert) > 0 {
		w := TechCardSeamsWrite{TechCardId: in.TechCardId, Seams: in.Upsert, By: in.By}
		if err := ValidateTechCardSeamsWrite(&w); err != nil {
			return RenameSeamFieldPrefix(err, "seams", "upsert")
		}
		in.Upsert = w.Seams
	}
	if len(in.Delete) > TechCardSeamChangeMaxRefs {
		return NewFieldViolation("delete", "too_many", "", fmt.Sprintf("at most %d keys per call", TechCardSeamChangeMaxRefs))
	}
	// Repeats are kept (indices stay the request's for field paths); the store applies them as
	// no-ops: a second delete or detach finds nothing, a second link finds the pair linked.
	delAt := map[string]int{}
	for i := range in.Delete {
		k := NormalizeTechCardSeamKey(in.Delete[i])
		if !ValidTechCardSeamKey(k) {
			return NewFieldViolation(fmt.Sprintf("delete[%d]", i), "invalid", "", "a 26-char ULID")
		}
		in.Delete[i] = k
		if _, dup := delAt[k]; !dup {
			delAt[k] = i
		}
	}
	link, err := normalizeTechCardSeamLinks("link", in.Link)
	if err != nil {
		return err
	}
	detach, err := normalizeTechCardSeamLinks("detach", in.Detach)
	if err != nil {
		return err
	}
	in.Link, in.Detach = link, detach
	for i, s := range in.Upsert {
		if j, ok := delAt[s.SeamKey]; ok {
			return NewFieldViolation(fmt.Sprintf("delete[%d]", j), "contradicts_upsert", fmt.Sprintf("upsert[%d]", i),
				"a row is either written or deleted in one change")
		}
	}
	return TechCardSeamLinksContradiction(in.Link, in.Detach, in.Delete)
}

// normalizeTechCardSeamLinks upper-cases the keys and checks bounds. Repeats are kept, as for delete.
func normalizeTechCardSeamLinks(field string, links []TechCardSeamLink) ([]TechCardSeamLink, error) {
	if len(links) > TechCardSeamChangeMaxRefs {
		return nil, NewFieldViolation(field, "too_many", "", fmt.Sprintf("at most %d per call", TechCardSeamChangeMaxRefs))
	}
	out := make([]TechCardSeamLink, 0, len(links))
	for i, l := range links {
		f := fmt.Sprintf("%s[%d]", field, i)
		if l.OperationNumber <= 0 {
			return nil, NewFieldViolation(f+".operation_number", "required", "", "the step's operation_number on the card")
		}
		l.SeamKey = NormalizeTechCardSeamKey(l.SeamKey)
		if !ValidTechCardSeamKey(l.SeamKey) {
			return nil, NewFieldViolation(f+".seam_key", "invalid", "", "a 26-char ULID")
		}
		out = append(out, l)
	}
	return out, nil
}

// TechCardSeamLinksContradiction refuses the same (step, key) in link and detach and a linked key that
// is also deleted. The store runs it again after upsert keys are rekeyed onto stored rows.
func TechCardSeamLinksContradiction(link, detach []TechCardSeamLink, del []string) error {
	linkAt := make(map[TechCardSeamLink]int, len(link))
	keyLinkedAt := make(map[string]int, len(link))
	for i, l := range link {
		linkAt[l] = i
		if _, ok := keyLinkedAt[l.SeamKey]; !ok {
			keyLinkedAt[l.SeamKey] = i
		}
	}
	for i, d := range detach {
		if j, ok := linkAt[d]; ok {
			return NewFieldViolation(fmt.Sprintf("detach[%d]", i), "contradicts_link", fmt.Sprintf("link[%d]", j),
				"a step either gains or loses a seam in one change")
		}
	}
	for i, k := range del {
		if j, ok := keyLinkedAt[k]; ok {
			return NewFieldViolation(fmt.Sprintf("delete[%d]", i), "contradicts_link", fmt.Sprintf("link[%d]", j),
				"a deleted row cannot be linked in the same change")
		}
	}
	return nil
}

// RenameSeamFieldPrefix rewrites a field-tagged violation's leading path segment (seams[i]… →
// upsert[i]…), so a shared check names the field of the request that carried the row.
func RenameSeamFieldPrefix(err error, from, to string) error {
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.HasPrefix(ve.Field, from) {
		return err
	}
	rest := ve.Field[len(from):]
	if rest != "" && rest[0] != '[' && rest[0] != '.' {
		return err
	}
	return NewFieldViolation(to+rest, ve.Reason, ve.Conflicting, ve.HowToFix)
}

// TechCardSeamPairRangeSig is a row's pair with its sewn windows, order-free — the server twin of the
// admin client's rowPairRangeSig: kind | both sides, each side its anchors as edge_hint@from-to
// sorted and joined with '+', the two sides sorted and joined with '~'. A whole run reads 0-1 however
// it was sent (the wire's 0/0 or an explicit 0/1), as the client reads it. Two rows with one
// signature are one seam: a new row of a stored pair is stored under the stored row's key.
func TechCardSeamPairRangeSig(s TechCardSeamInput) string {
	side := func(xs []TechCardSeamAnchor) string {
		parts := make([]string, 0, len(xs))
		for _, a := range xs {
			from, to := a.RangeFrom, a.RangeTo
			if from == 0 && to == 0 {
				to = 1
			}
			parts = append(parts, a.EdgeHint+"@"+sigFloat(from)+"-"+sigFloat(to))
		}
		sort.Strings(parts)
		return strings.Join(parts, "+")
	}
	sides := []string{side(s.SideA), side(s.SideB)}
	sort.Strings(sides)
	return string(s.Kind) + "|" + strings.Join(sides, "~")
}

func sigFloat(x float64) string {
	if x == 0 {
		x = 0 // -0 reads as 0
	}
	return strconv.FormatFloat(x, 'f', -1, 64)
}
