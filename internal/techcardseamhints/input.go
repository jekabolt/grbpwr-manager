// Package techcardseamhints is the pure half of the seam hints (unit-seams line F, SPEC-F §2–4): the
// client-built input (bounded, canonicalised, digested), the closed vocabularies, the prompt, and the
// validation of the model's answer. No I/O: the handler loads media, dictionary and overrides and
// passes them in.
//
// AI IS A HINT ONLY. Nothing here writes a seam. A role or a join the model names is kept only when
// every word is in the closed vocabulary and every piece is in the input (a join's pieces among the
// live inputs of its step); a row that fails is dropped and its piece / step lands in not_inferred,
// so a miss is visible, never silent.
package techcardseamhints

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// SchemaVersion of the hints JSON (SPEC-F §4). Part of the digest: a new schema re-asks.
const SchemaVersion = 1

// Bounds on the client-built input. Raw size first (before any parse), then per field.
const (
	MaxInputBytes   = 3 << 20 // the whole input_json, atlas included
	MaxAtlasBytes   = 2 << 20 // the atlas PNG, decoded
	MaxPieces       = 120
	MaxOps          = 300
	MaxUnits        = 120
	MaxOpInputs     = 24
	MaxUnitPieces   = 60
	MaxSketches     = 4
	MaxNameRunes    = 64
	MaxTextRunes    = 120 // zone, work
	MaxShortRunes   = 32  // purpose, symmetry, operation_type
	MaxWhyRunes     = 160
	maxExpandDepth  = 16
	maxNotInferred  = 400
	maxAnswerRoles  = 400
	maxAnswerJoins  = 600
	maxSidePieces   = 24
	maxDictionaryIn = 400 // dictionary rows sent with one card
)

// Piece is one cut piece as the client read it. No edge data (SPEC-F §3).
type Piece struct {
	Name       string  `json:"name"`
	Purpose    string  `json:"purpose"`   // BOM purpose: main, pocketing, lining, fusing … ("" = unknown)
	Fused      bool    `json:"fused"`     // fused with interfacing
	Symmetry   string  `json:"symmetry"`  // pair, single, fold … ("" = unknown)
	DictRole   string  `json:"dict_role"` // the role the dictionary reads, "" = none
	Aspect     float64 `json:"aspect"`    // bbox long / short side
	Area       float64 `json:"area"`      // mm²
	Perimeter  float64 `json:"perimeter"` // mm
	Notches    int     `json:"notches"`
	InnerLines int     `json:"inner_lines"`
}

// Unit is a named subassembly: its inputs are piece or unit names.
type Unit struct {
	Name   string   `json:"name"`
	Inputs []string `json:"inputs"`
}

// Op is one step of the card's operations, in sewing order.
type Op struct {
	Number        int      `json:"number"`
	Zone          string   `json:"zone"`
	Work          string   `json:"work"`
	OperationType string   `json:"operation_type"`
	Inputs        []string `json:"inputs"` // piece names or unit names
	Output        string   `json:"output"` // the unit the step makes, "" = none
}

// Input is the client-built input of SuggestTechCardSeamHints (SPEC-F §3).
//
// The atlas (a PNG of the DXF silhouettes with the piece names on them, rendered by the client) is
// sent inline as base64 (with or without a data: prefix): it is a throwaway rendering, not a library
// file, so it never goes through the media upload. It is NOT part of the digest — it renders the
// pieces already digested, and a browser's rasteriser must not invalidate the cache.
type Input struct {
	Pieces         []Piece `json:"pieces"`
	Units          []Unit  `json:"units"`
	Ops            []Op    `json:"ops"`
	SketchMediaIDs []int   `json:"sketch_media_ids"`
	AtlasPNG       string  `json:"atlas_png"`

	atlas []byte // decoded AtlasPNG
}

// Atlas returns the decoded atlas PNG (nil when none was sent).
func (in Input) Atlas() []byte { return in.atlas }

// AtlasDataURL is the atlas as a data URL for the provider ("" when none).
func (in Input) AtlasDataURL() string {
	if len(in.atlas) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(in.atlas)
}

// InputError names the offending field of a refused input.
type InputError struct {
	Field, Reason string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Reason }

func inErr(field, format string, args ...any) error {
	return &InputError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// ParseInput bounds the raw size, decodes and validates the input. Names are trimmed; free text is
// bounded (refused when longer, never silently cut). Unknown JSON fields are ignored (forward
// compatible); an op input naming neither a piece nor a unit is kept and expands to no piece.
func ParseInput(raw []byte) (Input, error) {
	var in Input
	if len(raw) == 0 {
		return in, inErr("input_json", "required")
	}
	if len(raw) > MaxInputBytes {
		return in, inErr("input_json", "at most %d bytes, got %d", MaxInputBytes, len(raw))
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, inErr("input_json", "not valid JSON: %v", err)
	}
	if len(in.Pieces) == 0 {
		return in, inErr("pieces", "at least one piece is required")
	}
	if len(in.Pieces) > MaxPieces {
		return in, inErr("pieces", "at most %d, got %d", MaxPieces, len(in.Pieces))
	}
	if len(in.Ops) > MaxOps {
		return in, inErr("ops", "at most %d, got %d", MaxOps, len(in.Ops))
	}
	if len(in.Units) > MaxUnits {
		return in, inErr("units", "at most %d, got %d", MaxUnits, len(in.Units))
	}
	if len(in.SketchMediaIDs) > MaxSketches {
		return in, inErr("sketch_media_ids", "at most %d, got %d", MaxSketches, len(in.SketchMediaIDs))
	}
	names := make(map[string]bool, len(in.Pieces))
	for i := range in.Pieces {
		p := &in.Pieces[i]
		f := fmt.Sprintf("pieces[%d]", i)
		var err error
		if p.Name, err = name(f+".name", p.Name); err != nil {
			return in, err
		}
		if names[p.Name] {
			return in, inErr(f+".name", "duplicate piece %q", p.Name)
		}
		names[p.Name] = true
		if p.Purpose, err = short(f+".purpose", strings.ToLower(p.Purpose)); err != nil {
			return in, err
		}
		if p.Symmetry, err = short(f+".symmetry", strings.ToLower(p.Symmetry)); err != nil {
			return in, err
		}
		p.DictRole = strings.ToLower(strings.TrimSpace(p.DictRole))
		if p.DictRole != "" && !Roles[p.DictRole] {
			return in, inErr(f+".dict_role", "unknown role %q", p.DictRole)
		}
		for _, v := range []struct {
			field string
			x     float64
		}{{"aspect", p.Aspect}, {"area", p.Area}, {"perimeter", p.Perimeter}} {
			if math.IsNaN(v.x) || math.IsInf(v.x, 0) || v.x < 0 || v.x > 1e9 {
				return in, inErr(f+"."+v.field, "must be a finite number ≥ 0")
			}
		}
		if p.Notches < 0 || p.Notches > 1000 || p.InnerLines < 0 || p.InnerLines > 1000 {
			return in, inErr(f, "notches and inner_lines must be 0..1000")
		}
	}
	units := make(map[string]bool, len(in.Units))
	for i := range in.Units {
		u := &in.Units[i]
		f := fmt.Sprintf("units[%d]", i)
		var err error
		if u.Name, err = name(f+".name", u.Name); err != nil {
			return in, err
		}
		if names[u.Name] || units[u.Name] {
			return in, inErr(f+".name", "duplicate name %q", u.Name)
		}
		units[u.Name] = true
		if len(u.Inputs) > MaxUnitPieces {
			return in, inErr(f+".inputs", "at most %d, got %d", MaxUnitPieces, len(u.Inputs))
		}
		if u.Inputs, err = nameList(f+".inputs", u.Inputs); err != nil {
			return in, err
		}
	}
	numbers := make(map[int]bool, len(in.Ops))
	for i := range in.Ops {
		o := &in.Ops[i]
		f := fmt.Sprintf("ops[%d]", i)
		if o.Number <= 0 {
			return in, inErr(f+".number", "must be > 0")
		}
		if numbers[o.Number] {
			return in, inErr(f+".number", "duplicate operation %d", o.Number)
		}
		numbers[o.Number] = true
		var err error
		if o.Zone, err = text(f+".zone", o.Zone, MaxTextRunes); err != nil {
			return in, err
		}
		if o.Work, err = text(f+".work", o.Work, MaxTextRunes); err != nil {
			return in, err
		}
		if o.OperationType, err = short(f+".operation_type", o.OperationType); err != nil {
			return in, err
		}
		if len(o.Inputs) > MaxOpInputs {
			return in, inErr(f+".inputs", "at most %d, got %d", MaxOpInputs, len(o.Inputs))
		}
		if o.Inputs, err = nameList(f+".inputs", o.Inputs); err != nil {
			return in, err
		}
		if o.Output != "" {
			if o.Output, err = name(f+".output", o.Output); err != nil {
				return in, err
			}
			if names[o.Output] {
				return in, inErr(f+".output", "%q is a piece, not a unit", o.Output)
			}
		}
	}
	seen := make(map[int]bool, len(in.SketchMediaIDs))
	for i, id := range in.SketchMediaIDs {
		if id <= 0 {
			return in, inErr(fmt.Sprintf("sketch_media_ids[%d]", i), "must be > 0")
		}
		if seen[id] {
			return in, inErr(fmt.Sprintf("sketch_media_ids[%d]", i), "duplicate media %d", id)
		}
		seen[id] = true
	}
	sort.Ints(in.SketchMediaIDs)
	if a := strings.TrimSpace(in.AtlasPNG); a != "" {
		a = strings.TrimPrefix(a, "data:image/png;base64,")
		if base64.StdEncoding.DecodedLen(len(a)) > MaxAtlasBytes+3 {
			return in, inErr("atlas_png", "at most %d bytes decoded", MaxAtlasBytes)
		}
		b, err := base64.StdEncoding.DecodeString(a)
		if err != nil {
			return in, inErr("atlas_png", "not base64")
		}
		if len(b) > MaxAtlasBytes {
			return in, inErr("atlas_png", "at most %d bytes decoded, got %d", MaxAtlasBytes, len(b))
		}
		if !bytes.HasPrefix(b, pngMagic) {
			return in, inErr("atlas_png", "not a PNG")
		}
		in.atlas = b
	}
	in.AtlasPNG = ""
	return in, nil
}

func name(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", inErr(field, "must not be empty")
	}
	if n := utf8.RuneCountInString(v); n > MaxNameRunes {
		return "", inErr(field, "at most %d characters, got %d", MaxNameRunes, n)
	}
	return v, nil
}

func nameList(field string, vs []string) ([]string, error) {
	out := make([]string, 0, len(vs))
	seen := make(map[string]bool, len(vs))
	for i, v := range vs {
		n, err := name(fmt.Sprintf("%s[%d]", field, i), v)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

func text(field, v string, max int) (string, error) {
	v = strings.Join(strings.Fields(v), " ")
	if n := utf8.RuneCountInString(v); n > max {
		return "", inErr(field, "at most %d characters, got %d", max, n)
	}
	return v, nil
}

func short(field, v string) (string, error) { return text(field, v, MaxShortRunes) }

// ─── expansion: a name → the pieces it stands for ───

// Expander resolves piece / unit names to piece names. A unit is the union of its inputs (explicit
// units) or of the inputs of the step that makes it (op outputs); both may nest.
type Expander struct {
	pieces map[string]bool
	units  map[string][]string
}

// NewExpander builds the name resolution of in.
func NewExpander(in Input) *Expander {
	e := &Expander{pieces: make(map[string]bool, len(in.Pieces)), units: map[string][]string{}}
	for _, p := range in.Pieces {
		e.pieces[p.Name] = true
	}
	for _, u := range in.Units {
		e.units[u.Name] = append(e.units[u.Name], u.Inputs...)
	}
	for _, o := range in.Ops {
		if o.Output != "" {
			e.units[o.Output] = append(e.units[o.Output], o.Inputs...)
		}
	}
	return e
}

// IsPiece reports whether n is a piece name.
func (e *Expander) IsPiece(n string) bool { return e.pieces[n] }

// Pieces expands names to the set of pieces they stand for (unknown names expand to nothing).
func (e *Expander) Pieces(names []string) map[string]bool {
	out := map[string]bool{}
	var walk func(n string, depth int, path map[string]bool)
	walk = func(n string, depth int, path map[string]bool) {
		if e.pieces[n] {
			out[n] = true
			return
		}
		if depth > maxExpandDepth || path[n] {
			return
		}
		path[n] = true
		for _, c := range e.units[n] {
			walk(c, depth+1, path)
		}
		delete(path, n)
	}
	for _, n := range names {
		walk(n, 0, map[string]bool{})
	}
	return out
}

// ─── the digest ───

// MediaFact is one sketch the server loaded: its id and the content hash of the stored file
// ("" when the media row has none — then its URL stands in, so a replaced file still re-asks).
type MediaFact struct {
	ID   int    `json:"id"`
	Hash string `json:"sha256"`
}

// DictRow is one row of the owner's code dictionary.
type DictRow struct {
	Code string `json:"code"`
	Role string `json:"role"`
	Hand string `json:"hand,omitempty"`
}

type canonPiece struct {
	Name       string  `json:"name"`
	Purpose    string  `json:"purpose"`
	Fused      bool    `json:"fused"`
	Symmetry   string  `json:"symmetry"`
	DictRole   string  `json:"dict_role"`
	Aspect     float64 `json:"aspect"`
	Area       float64 `json:"area"`
	Perimeter  float64 `json:"perimeter"`
	Notches    int     `json:"notches"`
	InnerLines int     `json:"inner_lines"`
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// StoreVersion — the version of the STORED cache (roles runs, join rows). v2 = the split cache of
// SPEC-F §5a: a roles run per pieces digest and a join row per (op number, op digest). It is in every
// digest, so a new store shape re-asks; v1 whole-card runs are never read again.
const StoreVersion = 2

// Digests are the fingerprints of one card input (SPEC-F §5a):
//   - Roles: the pieces (sorted, shape summary rounded: aspect 0.01, area / perimeter 1), the sketches
//     (sorted by id, with their content hash) and the matched dictionary rows (sorted by code). The key
//     of the roles run: the same DXF + dictionary + sketch never runs twice.
//   - Ops: per op number — the op's own fields (zone, work, type, inputs, output), the summaries of the
//     pieces its inputs stand for (units expanded), and the roles digest. An edit of one op re-asks only
//     that op; any change of the pieces re-asks all of them.
//   - Full: the roles digest, the ops in the card's order with their digests, the units — the identity
//     of the whole input (input_digest on the wire).
//
// Owner overrides are in none of them (they survive reruns and the client merges them on top); the
// atlas is in none of them (see Input).
type Digests struct {
	Roles string
	Ops   map[int]string
	Full  string
}

func canonPieceOf(p Piece) canonPiece {
	return canonPiece{p.Name, p.Purpose, p.Fused, p.Symmetry, p.DictRole,
		round(p.Aspect, 2), round(p.Area, 0), round(p.Perimeter, 0), p.Notches, p.InnerLines}
}

func digestOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil { // plain structs of strings / numbers: cannot fail
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ComputeDigests fingerprints a card input.
func ComputeDigests(in Input, sketches []MediaFact, dict []DictRow) Digests {
	pieces := make(map[string]canonPiece, len(in.Pieces))
	allPieces := make([]canonPiece, 0, len(in.Pieces))
	for _, p := range in.Pieces {
		c := canonPieceOf(p)
		pieces[p.Name] = c
		allPieces = append(allPieces, c)
	}
	sort.Slice(allPieces, func(i, j int) bool { return allPieces[i].Name < allPieces[j].Name })
	sk := append([]MediaFact{}, sketches...)
	sort.Slice(sk, func(i, j int) bool { return sk[i].ID < sk[j].ID })
	dr := append([]DictRow{}, dict...)
	sort.Slice(dr, func(i, j int) bool { return dr[i].Code < dr[j].Code })
	d := Digests{Ops: make(map[int]string, len(in.Ops))}
	d.Roles = digestOf(struct {
		V          int          `json:"v"`
		Pieces     []canonPiece `json:"pieces"`
		Sketches   []MediaFact  `json:"sketches"`
		Dictionary []DictRow    `json:"dictionary"`
	}{StoreVersion, allPieces, sk, dr})

	ex := NewExpander(in)
	type opRef struct {
		Number int    `json:"n"`
		Digest string `json:"d"`
	}
	order := make([]opRef, 0, len(in.Ops))
	for _, o := range in.Ops {
		names := make([]string, 0)
		for n := range ex.Pieces(o.Inputs) {
			names = append(names, n)
		}
		sort.Strings(names)
		ps := make([]canonPiece, 0, len(names))
		for _, n := range names {
			ps = append(ps, pieces[n])
		}
		o.Inputs = orEmpty(append([]string{}, o.Inputs...))
		od := digestOf(struct {
			V      int          `json:"v"`
			Roles  string       `json:"roles"`
			Op     Op           `json:"op"`
			Pieces []canonPiece `json:"pieces"`
		}{StoreVersion, d.Roles, o, ps})
		d.Ops[o.Number] = od
		order = append(order, opRef{o.Number, od})
	}
	units := make([]Unit, 0, len(in.Units))
	for _, u := range in.Units {
		ins := append([]string{}, u.Inputs...)
		sort.Strings(ins)
		units = append(units, Unit{Name: u.Name, Inputs: orEmpty(ins)})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	d.Full = digestOf(struct {
		V     int     `json:"v"`
		Roles string  `json:"roles"`
		Ops   []opRef `json:"ops"`
		Units []Unit  `json:"units"`
	}{StoreVersion, d.Roles, order, units})
	return d
}

// Digest is the full digest of an input (Digests.Full).
func Digest(in Input, sketches []MediaFact, dict []DictRow) string {
	return ComputeDigests(in, sketches, dict).Full
}

// ─── the owner's code dictionary ───

// NormalizeCode upper-cases and trims a dictionary code and checks its shape: 1..32 of A-Z 0-9 _ -.
func NormalizeCode(code string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" || len(c) > 32 {
		return c, false
	}
	for _, r := range c {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return c, false
		}
	}
	return c, true
}

// MatchDictionary keeps the dictionary rows whose code occurs in at least one piece name as a whole
// token run (bounded by the name's start / end or a character outside A-Z 0-9), compared upper-case:
// FLP matches FLP_R_MAIN, CLR_SECOND matches CLR_SECOND_U, P matches P_L_U but not PLCK. Sorted by
// code, at most maxDictionaryIn rows. Which row wins for a piece is the client's call (precedence:
// owner fix for this card > dictionary > AI > static default); the server only tells the model.
func MatchDictionary(in Input, rows []DictRow) []DictRow {
	names := make([]string, 0, len(in.Pieces))
	for _, p := range in.Pieces {
		names = append(names, strings.ToUpper(p.Name))
	}
	var out []DictRow
	for _, r := range rows {
		code := strings.ToUpper(r.Code)
		if code == "" {
			continue
		}
		for _, n := range names {
			if tokenMatch(n, code) {
				out = append(out, DictRow{Code: code, Role: r.Role, Hand: r.Hand})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	if len(out) > maxDictionaryIn {
		out = out[:maxDictionaryIn]
	}
	return out
}

func isAlnum(b byte) bool { return b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }

func tokenMatch(name, code string) bool {
	for from := 0; from <= len(name)-len(code); {
		i := strings.Index(name[from:], code)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(code)
		if (i == 0 || !isAlnum(name[i-1])) && (end == len(name) || !isAlnum(name[end])) {
			return true
		}
		from = i + 1
	}
	return false
}
