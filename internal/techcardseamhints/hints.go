package techcardseamhints

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ─── the closed vocabularies (SPEC-F §4) ───

// Roles — what a piece is in the garment.
var Roles = set("front", "back", "yoke", "sleeve", "cuff", "placket", "collar", "stand", "neck_facing",
	"facing", "pocket_bag", "pocket", "flap", "waistband", "lining", "interlining", "other")

// Edges — the edge word on each side of a join.
var Edges = set("front_edge", "hem", "neckline", "shoulder", "armhole", "side", "slit", "top", "bottom",
	"long", "short", "curved", "inner_line")

// Relations — how two sides of a join meet.
var Relations = set("boundary", "surface", "layer")

// Hands, confidences, override kinds and states.
var (
	Hands  = set("", "L", "R")
	Confs  = set("high", "medium", "low")
	Kinds  = set("role", "join")
	States = set("ok", "fixed", "off")
)

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// SortedKeys lists a vocabulary for a prompt or an error message ("" left out).
func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ─── the hints (the model's answer after validation, schema_version 1) ───

// Role is one piece's role.
type Role struct {
	Piece string `json:"piece"`
	Role  string `json:"role"`
	Hand  string `json:"hand"`
	Conf  string `json:"conf"`
	Why   string `json:"why,omitempty"`
}

// Side is one side of a join: the pieces and the edge word.
type Side struct {
	Pieces []string `json:"pieces"`
	Edge   string   `json:"edge"`
}

// Join is one join step's map: A goes onto B.
type Join struct {
	Op       int    `json:"op"`
	A        Side   `json:"a"`
	B        Side   `json:"b"`
	Relation string `json:"relation"`
	Conf     string `json:"conf"`
	Why      string `json:"why,omitempty"`
}

// Hints is what is stored and served (hints_json).
type Hints struct {
	SchemaVersion int      `json:"schema_version"`
	Roles         []Role   `json:"roles"`
	Joins         []Join   `json:"joins"`
	NotInferred   []string `json:"not_inferred"`
}

// Report says what validation dropped and why (for the log and the tests; never the model's text).
type Report struct {
	Dropped []string
}

func (r *Report) drop(format string, args ...any) {
	r.Dropped = append(r.Dropped, fmt.Sprintf(format, args...))
}

// answer is the raw shape the model is asked for; fields are decoded loosely so one bad row does not
// sink the rest.
type answer struct {
	Roles       []json.RawMessage `json:"roles"`
	Joins       []json.RawMessage `json:"joins"`
	NotInferred []json.RawMessage `json:"not_inferred"`
}

type rawSide struct {
	Pieces []string `json:"pieces"`
	Edge   string   `json:"edge"`
}

type rawJoin struct {
	Op       json.Number `json:"op"`
	A        rawSide     `json:"a"`
	B        rawSide     `json:"b"`
	Relation string      `json:"relation"`
	Conf     string      `json:"conf"`
	Why      string      `json:"why"`
}

// ErrNotJSON — the answer holds no JSON object at all (the caller asks once more).
var ErrNotJSON = errors.New("the answer is not a JSON object")

// decodeObject finds the first JSON object in raw (tolerating a code fence or a sentence around it).
func decodeObject(raw string) (answer, error) {
	var a answer
	s := strings.TrimSpace(raw)
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		dec.UseNumber()
		if err := dec.Decode(&a); err == nil {
			return a, nil
		} else if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return a, ErrNotJSON
}

// Validate parses the model's raw text and keeps only what the input supports (SPEC-F §2):
//   - a role row: the piece is in the input, role in Roles, hand in Hands (else the piece goes to
//     not_inferred); an unknown piece is dropped; a second row for a piece is dropped;
//   - a join row: the op is in the input (else dropped); both sides name ≥ 1 piece, every piece (unit
//     names are expanded) is among the op's live inputs, the sides do not overlap, both edges in
//     Edges, relation in Relations (else the op goes to not_inferred); one row per op;
//   - conf outside high/medium/low reads as low; why is collapsed and cut to MaxWhyRunes;
//   - the model's own not_inferred keeps only "op:<n>" / "piece:<name>" of the input;
//   - every piece with neither a dictionary role nor a kept AI role is added to not_inferred.
//
// not_inferred is deduplicated: ops by number, then pieces by name.
func Validate(raw string, in Input) (Hints, Report, error) {
	var rep Report
	a, err := decodeObject(raw)
	if err != nil {
		return Hints{}, rep, err
	}
	ex := NewExpander(in)
	pieceDict := make(map[string]bool, len(in.Pieces))
	for _, p := range in.Pieces {
		pieceDict[p.Name] = p.DictRole != ""
	}
	ops := make(map[int]Op, len(in.Ops))
	for _, o := range in.Ops {
		ops[o.Number] = o
	}
	h := Hints{SchemaVersion: SchemaVersion, Roles: []Role{}, Joins: []Join{}}
	notPieces := map[string]bool{}
	notOps := map[int]bool{}

	roled := map[string]bool{}
	for i, r := range a.Roles {
		if i >= maxAnswerRoles {
			rep.drop("roles: more than %d rows", maxAnswerRoles)
			break
		}
		var row Role
		if json.Unmarshal(r, &row) != nil {
			rep.drop("roles[%d]: not an object", i)
			continue
		}
		row.Piece = strings.TrimSpace(row.Piece)
		if _, ok := pieceDict[row.Piece]; !ok {
			rep.drop("roles[%d]: unknown piece", i)
			continue
		}
		if roled[row.Piece] {
			rep.drop("roles[%d]: second row for %s", i, row.Piece)
			continue
		}
		row.Role = strings.ToLower(strings.TrimSpace(row.Role))
		row.Hand = strings.ToUpper(strings.TrimSpace(row.Hand))
		if !Roles[row.Role] || !Hands[row.Hand] {
			rep.drop("roles[%d]: role or hand outside the vocabulary", i)
			notPieces[row.Piece] = true
			continue
		}
		roled[row.Piece] = true
		row.Conf = conf(row.Conf)
		row.Why = whyText(row.Why)
		h.Roles = append(h.Roles, row)
	}

	joined := map[int]bool{}
	for i, r := range a.Joins {
		if i >= maxAnswerJoins {
			rep.drop("joins: more than %d rows", maxAnswerJoins)
			break
		}
		var row rawJoin
		if json.Unmarshal(r, &row) != nil {
			rep.drop("joins[%d]: not an object", i)
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(row.Op.String()))
		op, ok := ops[n]
		if err != nil || !ok {
			rep.drop("joins[%d]: unknown op", i)
			continue
		}
		if joined[n] {
			rep.drop("joins[%d]: second row for op %d", i, n)
			continue
		}
		j, why := validJoin(row, op, ex)
		if why != "" {
			rep.drop("joins[%d] (op %d): %s", i, n, why)
			notOps[n] = true
			continue
		}
		joined[n] = true
		h.Joins = append(h.Joins, j)
	}

	for _, r := range a.NotInferred {
		var s string
		if json.Unmarshal(r, &s) != nil {
			continue
		}
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "op:"):
			if n, err := strconv.Atoi(strings.TrimSpace(s[3:])); err == nil {
				if _, ok := ops[n]; ok && !joined[n] {
					notOps[n] = true
				}
			}
		case strings.HasPrefix(s, "piece:"):
			p := strings.TrimSpace(s[6:])
			if _, ok := pieceDict[p]; ok && !roled[p] {
				notPieces[p] = true
			}
		}
	}
	for p, dict := range pieceDict {
		if !dict && !roled[p] {
			notPieces[p] = true
		}
	}

	nums := make([]int, 0, len(notOps))
	for n := range notOps {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	names := make([]string, 0, len(notPieces))
	for p := range notPieces {
		names = append(names, p)
	}
	sort.Strings(names)
	h.NotInferred = make([]string, 0, len(nums)+len(names))
	for _, n := range nums {
		h.NotInferred = append(h.NotInferred, "op:"+strconv.Itoa(n))
	}
	for _, p := range names {
		h.NotInferred = append(h.NotInferred, "piece:"+p)
	}
	if len(h.NotInferred) > maxNotInferred {
		h.NotInferred = h.NotInferred[:maxNotInferred]
	}
	return h, rep, nil
}

// validJoin checks one join row against its op; why != "" names the first violation.
func validJoin(row rawJoin, op Op, ex *Expander) (Join, string) {
	live := ex.Pieces(op.Inputs)
	side := func(s rawSide, label string) (Side, string) {
		if len(s.Pieces) == 0 {
			return Side{}, label + ": no pieces"
		}
		if len(s.Pieces) > maxSidePieces {
			return Side{}, label + ": too many pieces"
		}
		got := map[string]bool{}
		var out []string
		for _, n := range s.Pieces {
			n = strings.TrimSpace(n)
			var expanded []string
			if ex.IsPiece(n) {
				expanded = []string{n}
			} else {
				for p := range ex.Pieces([]string{n}) {
					expanded = append(expanded, p)
				}
				sort.Strings(expanded)
			}
			if len(expanded) == 0 {
				return Side{}, label + ": unknown piece"
			}
			for _, p := range expanded {
				if !live[p] {
					return Side{}, label + ": a piece that is not an input of the step"
				}
				if !got[p] {
					got[p] = true
					out = append(out, p)
				}
			}
		}
		edge := strings.ToLower(strings.TrimSpace(s.Edge))
		if !Edges[edge] {
			return Side{}, label + ": edge outside the vocabulary"
		}
		return Side{Pieces: out, Edge: edge}, ""
	}
	a, why := side(row.A, "a")
	if why != "" {
		return Join{}, why
	}
	b, why := side(row.B, "b")
	if why != "" {
		return Join{}, why
	}
	inA := map[string]bool{}
	for _, p := range a.Pieces {
		inA[p] = true
	}
	for _, p := range b.Pieces {
		if inA[p] {
			return Join{}, "a piece on both sides"
		}
	}
	rel := strings.ToLower(strings.TrimSpace(row.Relation))
	if !Relations[rel] {
		return Join{}, "relation outside the vocabulary"
	}
	return Join{Op: op.Number, A: a, B: b, Relation: rel, Conf: conf(row.Conf), Why: whyText(row.Why)}, ""
}

func conf(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if Confs[c] {
		return c
	}
	return "low"
}

func whyText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= MaxWhyRunes {
		return s
	}
	r := []rune(s)
	return string(r[:MaxWhyRunes])
}

// Marshal is the stored / served form of h.
func (h Hints) Marshal() string {
	b, err := json.Marshal(h)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ─── owner overrides ───

// RoleValue is an override's value for kind role.
type RoleValue struct {
	Role string `json:"role"`
	Hand string `json:"hand"`
}

// JoinValue is an override's value for kind join (the op is the hint key).
type JoinValue struct {
	A        Side   `json:"a"`
	B        Side   `json:"b"`
	Relation string `json:"relation"`
}

// MaxOverrideValueBytes bounds one override's value JSON.
const MaxOverrideValueBytes = 4 << 10

// NormalizeOverride checks one owner override and returns its key and value in canonical form.
// kind role: key = piece name (≤ 64 runes), value {"role","hand"} with role in Roles (required unless
// state off). kind join: key = the op number as decimal text (> 0), value {"a":{pieces,edge},
// "b":{pieces,edge},"relation"} in vocabulary (required unless state off). state off may carry no
// value. An empty state is not handled here (the caller deletes the row).
func NormalizeOverride(kind, key, value, state string) (string, string, error) {
	if !Kinds[kind] {
		return "", "", inErr("kind", "role or join, got %q", kind)
	}
	if !States[state] {
		return "", "", inErr("state", "ok, fixed or off (empty deletes), got %q", state)
	}
	key = strings.TrimSpace(key)
	switch kind {
	case "role":
		k, err := name("hint_key", key)
		if err != nil {
			return "", "", err
		}
		key = k
	case "join":
		n, err := strconv.Atoi(key)
		if err != nil || n <= 0 {
			return "", "", inErr("hint_key", "the operation number of a join, got %q", key)
		}
		key = strconv.Itoa(n)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if state == "off" {
			return key, "", nil
		}
		return "", "", inErr("value_json", "required unless state is off")
	}
	if len(value) > MaxOverrideValueBytes {
		return "", "", inErr("value_json", "at most %d bytes", MaxOverrideValueBytes)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(value)))
	dec.DisallowUnknownFields()
	var out any
	switch kind {
	case "role":
		var v RoleValue
		if err := dec.Decode(&v); err != nil {
			return "", "", inErr("value_json", "expected {\"role\",\"hand\"}: %v", err)
		}
		v.Role, v.Hand = strings.ToLower(strings.TrimSpace(v.Role)), strings.ToUpper(strings.TrimSpace(v.Hand))
		if !Roles[v.Role] {
			return "", "", inErr("value_json.role", "unknown role %q", v.Role)
		}
		if !Hands[v.Hand] {
			return "", "", inErr("value_json.hand", "L, R or empty, got %q", v.Hand)
		}
		out = v
	case "join":
		var v JoinValue
		if err := dec.Decode(&v); err != nil {
			return "", "", inErr("value_json", "expected {\"a\",\"b\",\"relation\"}: %v", err)
		}
		for _, s := range []struct {
			label string
			side  *Side
		}{{"a", &v.A}, {"b", &v.B}} {
			ps, err := nameList("value_json."+s.label+".pieces", s.side.Pieces)
			if err != nil {
				return "", "", err
			}
			if len(ps) == 0 || len(ps) > maxSidePieces {
				return "", "", inErr("value_json."+s.label+".pieces", "1..%d piece names", maxSidePieces)
			}
			s.side.Pieces = ps
			s.side.Edge = strings.ToLower(strings.TrimSpace(s.side.Edge))
			if !Edges[s.side.Edge] {
				return "", "", inErr("value_json."+s.label+".edge", "unknown edge %q", s.side.Edge)
			}
		}
		v.Relation = strings.ToLower(strings.TrimSpace(v.Relation))
		if !Relations[v.Relation] {
			return "", "", inErr("value_json.relation", "unknown relation %q", v.Relation)
		}
		out = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", "", err
	}
	return key, string(b), nil
}

// JoinFits reports whether a stored join row still fits the input: its op exists and every piece on
// both sides is among the pieces that op's inputs stand for now. A stored row of an older op digest
// is shown (as stale) only when it fits.
func JoinFits(in Input, j Join) bool {
	ex := NewExpander(in)
	for _, o := range in.Ops {
		if o.Number != j.Op {
			continue
		}
		live := ex.Pieces(o.Inputs)
		if len(j.A.Pieces) == 0 || len(j.B.Pieces) == 0 {
			return false
		}
		for _, side := range [][]string{j.A.Pieces, j.B.Pieces} {
			for _, p := range side {
				if !live[p] {
					return false
				}
			}
		}
		return true
	}
	return false
}

// Joinable reports whether an op can hold a join at all: its inputs stand for at least two pieces.
// An op that cannot is stored as outcome none without asking the model.
func Joinable(in Input, op Op) bool {
	return len(NewExpander(in).Pieces(op.Inputs)) >= 2
}
