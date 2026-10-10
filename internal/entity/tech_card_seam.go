package entity

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// CONFIRMED SEAMS (tech_card_seam) — the technologist's decisions on the seams the assembly engine
// proposes: confirmed, rejected, or connected by hand on the pieces map. The doll, the skeleton, the
// assembly map and the POM engine resolve ONE list of these instead of guessing again on every open
// (tmp/plans/assembly-3d-doll/03-SEAMS-DESIGN.md).
//
// AN EDGE IS NEVER ADDRESSED BY ITS CORNER NUMBER (`pieceKey#k`) — that number is counted from the
// DXF's first vertex and moves with the exporter. A side is a list of ANCHORS: the piece's stable
// line_key plus the edge's shape in the piece's own frame. Resolution is geometric and happens in the
// browser (the server does not parse DXF); what the server owns is the staleness of the SOURCE: it
// stamps every row with the fingerprint of the sheets + block→piece links of the seam's pieces' fabric
// scopes, and reports `stale` on every read when that moved.
//
// OUTSIDE EVERY SECTION DIGEST AND OUTSIDE lock_version: the seams ride the read model
// (TechCard.Seams), never TechCardInsert, so the digest write = read invariant cannot see them, and
// the card form's autosave never ABORTs because someone confirmed a seam.

// Bounds of one seam write. Field-addressed refusals name them; nothing here is a DB CHECK.
const (
	TechCardSeamKeyLen               = 26  // ULID
	TechCardSeamMaxRowsPerCard       = 400 // a garment has dozens of seams; 400 is a runaway client
	TechCardSeamMaxAnchorsPerSide    = 8
	TechCardSeamSamplesPerAnchor     = 5
	TechCardSeamNoteMaxRunes         = 255
	TechCardSeamAnchoredSizeMaxRunes = 16
	TechCardSeamHintMaxRunes         = 64 // edge_hint / contour_sig: diagnostics, not payload
)

// TechCardSeamStatus is the decision on a seam.
type TechCardSeamStatus string

const (
	TechCardSeamConfirmed TechCardSeamStatus = "confirmed"
	TechCardSeamRejected  TechCardSeamStatus = "rejected"
)

// TechCardSeamKind is the seam's shape.
type TechCardSeamKind string

const (
	TechCardSeamKindEdge      TechCardSeamKind = "edge"
	TechCardSeamKindPartial   TechCardSeamKind = "partial"
	TechCardSeamKindComposite TechCardSeamKind = "composite"
	TechCardSeamKindSurface   TechCardSeamKind = "surface"
	TechCardSeamKindClosure   TechCardSeamKind = "closure"
)

// TechCardSeamDirection is how the two sides walk against each other. Unknown is a legal stored
// value: the reader then picks the pairing itself.
type TechCardSeamDirection string

const (
	TechCardSeamReversed         TechCardSeamDirection = "reversed"
	TechCardSeamSame             TechCardSeamDirection = "same"
	TechCardSeamDirectionUnknown TechCardSeamDirection = "unknown"
)

// TechCardSeamSource is where the seam came from before a person decided on it. `ai` is reserved.
type TechCardSeamSource string

const (
	TechCardSeamSourceGraph  TechCardSeamSource = "graph"
	TechCardSeamSourceDoll   TechCardSeamSource = "doll"
	TechCardSeamSourceOrder  TechCardSeamSource = "order"
	TechCardSeamSourceManual TechCardSeamSource = "manual"
	TechCardSeamSourceAI     TechCardSeamSource = "ai"
)

// TechCardSeamSample is one point of an edge's shape in the piece's bbox-normalised frame.
type TechCardSeamSample struct {
	U float64 `json:"u"`
	V float64 `json:"v"`
}

// TechCardSeamAnchor is one run of one piece taking part in a seam side. Stored inside the side's
// JSON array with these exact keys — they are the column's format, renaming one is a migration.
type TechCardSeamAnchor struct {
	PieceLineKey string               `json:"piece_line_key"`
	Samples      []TechCardSeamSample `json:"samples"`
	PerimShare   float64              `json:"perim_share"`
	LenMm        float64              `json:"len_mm"`
	Notches      int32                `json:"notches"`
	TurnDeg      float64              `json:"turn_deg"`
	// RangeFrom / RangeTo: the sewn sub-range as shares of the run from its start; 0 / 0 = whole run.
	RangeFrom  float64 `json:"range_from"`
	RangeTo    float64 `json:"range_to"`
	EdgeHint   string  `json:"edge_hint"`
	ContourSig string  `json:"contour_sig"`
}

// TechCardSeamInput is one seam as a client submits it (keyed upsert, replaced whole).
type TechCardSeamInput struct {
	SeamKey      string
	Status       TechCardSeamStatus
	Kind         TechCardSeamKind
	Direction    TechCardSeamDirection
	Source       TechCardSeamSource
	SideA        []TechCardSeamAnchor
	SideB        []TechCardSeamAnchor
	AnchoredSize string
	Note         string
}

// TechCardSeam is one stored seam with the server's verdict resolved on read.
type TechCardSeam struct {
	TechCardSeamInput
	TechCardId int
	// SizeId is reserved (NULL = every size; v1 writes no per-size rows).
	SizeId            sql.NullInt64
	SourceFingerprint string
	// Stale: the fingerprint of the seam's pieces' source, computed NOW, differs from the stored one.
	// COMPUTED on every read, never stored — a stored flag would be wrong exactly when a sheet was
	// replaced and nobody opened the card.
	Stale     bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedBy string
	UpdatedAt time.Time
}

// TechCardSeamsWrite is one UpsertTechCardSeams call.
type TechCardSeamsWrite struct {
	TechCardId int
	Seams      []TechCardSeamInput
	By         string
}

// PieceLineKeys returns the distinct piece line keys of both sides, upper-cased and sorted — the set
// whose fabric scopes the seam's source fingerprint covers.
func (in TechCardSeamInput) PieceLineKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, side := range [][]TechCardSeamAnchor{in.SideA, in.SideB} {
		for _, a := range side {
			k := strings.ToUpper(strings.TrimSpace(a.PieceLineKey))
			if k != "" && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TechCardSeamSourceFingerprint is the source fingerprint of a seam over its pieces: the ONE device
// piece areas already use (PieceAreaSourceFingerprint — sheets AND block→piece links), applied to the
// UNION of the fabric scopes the seam's pieces belong to.
//
// A piece's scopes are the scopes whose block links point at it. A piece with no link at all (not
// aliased to any DXF block yet) widens the union to EVERY scope of the card — conservative: with no
// way to tell which file draws it, any re-upload may have moved it. Scoping is what keeps a pocketing
// re-upload from staling the shell seams while a MAIN re-upload stales them all.
//
// Sheets and links are bucketed by FabricScopeIdentity by the caller (the same buckets piece areas
// use), so each sheet and each link appears in exactly one scope and the union is a plain concat;
// PieceAreaSourceFingerprint sorts, so map order does not matter.
func TechCardSeamSourceFingerprint(pieceKeys []string, sheetsByScope map[string][]PatternSheetRef, blocksByScope map[string][]PieceAreaBlockRef) string {
	piecesScopes := map[string]map[string]bool{}
	for scope, blocks := range blocksByScope {
		for _, b := range blocks {
			k := strings.ToUpper(strings.TrimSpace(b.PieceLineKey))
			if piecesScopes[k] == nil {
				piecesScopes[k] = map[string]bool{}
			}
			piecesScopes[k][scope] = true
		}
	}
	scopes := map[string]bool{}
	all := false
	for _, p := range pieceKeys {
		got := piecesScopes[strings.ToUpper(strings.TrimSpace(p))]
		if len(got) == 0 {
			all = true
			break
		}
		for s := range got {
			scopes[s] = true
		}
	}
	if all {
		scopes = map[string]bool{}
		for s := range sheetsByScope {
			scopes[s] = true
		}
		for s := range blocksByScope {
			scopes[s] = true
		}
	}
	var sheets []PatternSheetRef
	var blocks []PieceAreaBlockRef
	for s := range scopes {
		sheets = append(sheets, sheetsByScope[s]...)
		blocks = append(blocks, blocksByScope[s]...)
	}
	return PieceAreaSourceFingerprint(sheets, blocks)
}

var techCardSeamStatuses = map[TechCardSeamStatus]bool{TechCardSeamConfirmed: true, TechCardSeamRejected: true}

var techCardSeamKinds = map[TechCardSeamKind]bool{
	TechCardSeamKindEdge: true, TechCardSeamKindPartial: true, TechCardSeamKindComposite: true,
	TechCardSeamKindSurface: true, TechCardSeamKindClosure: true,
}

var techCardSeamDirections = map[TechCardSeamDirection]bool{
	TechCardSeamReversed: true, TechCardSeamSame: true, TechCardSeamDirectionUnknown: true,
}

var techCardSeamSources = map[TechCardSeamSource]bool{
	TechCardSeamSourceGraph: true, TechCardSeamSourceDoll: true, TechCardSeamSourceOrder: true,
	TechCardSeamSourceManual: true, TechCardSeamSourceAI: true,
}

// NormalizeTechCardSeamKey trims and upper-cases a ULID (the column's collation is case-insensitive,
// so two spellings of one key would collide on the UNIQUE anyway — one canonical form on the wire).
func NormalizeTechCardSeamKey(k string) string { return strings.ToUpper(strings.TrimSpace(k)) }

// ValidTechCardSeamKey reports whether k (normalised) is a 26-char Crockford base-32 ULID.
func ValidTechCardSeamKey(k string) bool {
	if len(k) != TechCardSeamKeyLen {
		return false
	}
	for _, r := range k {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z' && r != 'I' && r != 'L' && r != 'O' && r != 'U':
		default:
			return false
		}
	}
	return true
}

// ValidateTechCardSeamsWrite checks everything about a seam write that does not need the database:
// dictionaries, shapes, bounds, duplicates. The store adds the one check that does — every anchor's
// piece must be a piece of THIS card. Errors are field-addressed (seams[i].side_a.parts[j].…).
// Normalises seam keys in place.
func ValidateTechCardSeamsWrite(in *TechCardSeamsWrite) error {
	if in.TechCardId <= 0 {
		return NewFieldViolation("tech_card_id", "required", "", "")
	}
	if len(in.Seams) == 0 {
		return NewFieldViolation("seams", "empty", "", "send at least one seam; to remove rows use DeleteTechCardSeams")
	}
	if len(in.Seams) > TechCardSeamMaxRowsPerCard {
		return NewFieldViolation("seams", "too_many", "",
			fmt.Sprintf("at most %d seams per card", TechCardSeamMaxRowsPerCard))
	}
	seen := make(map[string]int, len(in.Seams))
	for i := range in.Seams {
		s := &in.Seams[i]
		f := fmt.Sprintf("seams[%d]", i)
		s.SeamKey = NormalizeTechCardSeamKey(s.SeamKey)
		if !ValidTechCardSeamKey(s.SeamKey) {
			return NewFieldViolation(f+".seam_key", "invalid", "", "a 26-char ULID minted by the client")
		}
		if j, dup := seen[s.SeamKey]; dup {
			return NewFieldViolation(f+".seam_key", "duplicate", fmt.Sprintf("seams[%d]", j),
				"one row per seam_key per call")
		}
		seen[s.SeamKey] = i
		if !techCardSeamStatuses[s.Status] {
			return NewFieldViolation(f+".status", "invalid", "", "confirmed or rejected")
		}
		if !techCardSeamKinds[s.Kind] {
			return NewFieldViolation(f+".kind", "invalid", "", "edge, partial, composite, surface or closure")
		}
		if s.Direction == "" {
			s.Direction = TechCardSeamDirectionUnknown
		}
		if !techCardSeamDirections[s.Direction] {
			return NewFieldViolation(f+".direction", "invalid", "", "reversed, same or unknown")
		}
		if !techCardSeamSources[s.Source] {
			return NewFieldViolation(f+".source", "invalid", "", "graph, doll, order, manual or ai")
		}
		if s.Kind == TechCardSeamKindClosure && s.Status != TechCardSeamConfirmed {
			return NewFieldViolation(f+".status", "rejected_closure", "",
				"a rejected closure is a seam — send it as a seam")
		}
		s.AnchoredSize = strings.TrimSpace(s.AnchoredSize)
		if utf8.RuneCountInString(s.AnchoredSize) > TechCardSeamAnchoredSizeMaxRunes {
			return NewFieldViolation(f+".anchored_size", "too_long", "",
				fmt.Sprintf("at most %d characters", TechCardSeamAnchoredSizeMaxRunes))
		}
		s.Note = strings.TrimSpace(s.Note)
		if utf8.RuneCountInString(s.Note) > TechCardSeamNoteMaxRunes {
			return NewFieldViolation(f+".note", "too_long", "",
				fmt.Sprintf("at most %d characters", TechCardSeamNoteMaxRunes))
		}
		if err := validateTechCardSeamSide(f+".side_a", s.SideA); err != nil {
			return err
		}
		if err := validateTechCardSeamSide(f+".side_b", s.SideB); err != nil {
			return err
		}
		if s.Kind == TechCardSeamKindComposite && len(s.SideA) < 2 && len(s.SideB) < 2 {
			return NewFieldViolation(f+".kind", "composite_needs_parts", "",
				"a composite seam walks across at least two anchors on one side; send a single run as edge")
		}
	}
	return nil
}

func validateTechCardSeamSide(f string, side []TechCardSeamAnchor) error {
	if len(side) == 0 {
		return NewFieldViolation(f+".parts", "empty", "", "a seam side names at least one anchor")
	}
	if len(side) > TechCardSeamMaxAnchorsPerSide {
		return NewFieldViolation(f+".parts", "too_many", "",
			fmt.Sprintf("at most %d anchors per side", TechCardSeamMaxAnchorsPerSide))
	}
	seen := map[string]int{}
	for j := range side {
		a := &side[j]
		af := fmt.Sprintf("%s.parts[%d]", f, j)
		a.PieceLineKey = strings.TrimSpace(a.PieceLineKey)
		if a.PieceLineKey == "" {
			return NewFieldViolation(af+".piece_line_key", "required", "", "the piece's line_key")
		}
		a.EdgeHint = strings.TrimSpace(a.EdgeHint)
		a.ContourSig = strings.TrimSpace(a.ContourSig)
		if utf8.RuneCountInString(a.EdgeHint) > TechCardSeamHintMaxRunes {
			return NewFieldViolation(af+".edge_hint", "too_long", "", "")
		}
		if utf8.RuneCountInString(a.ContourSig) > TechCardSeamHintMaxRunes {
			return NewFieldViolation(af+".contour_sig", "too_long", "", "")
		}
		pair := strings.ToUpper(a.PieceLineKey) + "\x00" + a.EdgeHint
		if k, dup := seen[pair]; dup {
			return NewFieldViolation(af, "duplicate_anchor", fmt.Sprintf("%s.parts[%d]", f, k),
				"a side must not name the same piece edge twice")
		}
		seen[pair] = j
		if len(a.Samples) != TechCardSeamSamplesPerAnchor {
			return NewFieldViolation(af+".samples", "count", "",
				fmt.Sprintf("exactly %d samples along the run", TechCardSeamSamplesPerAnchor))
		}
		for k, p := range a.Samples {
			if !unitInterval(p.U) || !unitInterval(p.V) {
				return NewFieldViolation(fmt.Sprintf("%s.samples[%d]", af, k), "out_of_frame", "",
					"u and v are in [0, 1] — the piece's bbox-normalised frame")
			}
		}
		if !finite(a.PerimShare) || a.PerimShare <= 0 || a.PerimShare > 1 {
			return NewFieldViolation(af+".perim_share", "out_of_range", "", "in (0, 1]")
		}
		if !finite(a.LenMm) || a.LenMm <= 0 {
			return NewFieldViolation(af+".len_mm", "out_of_range", "", "a positive length in mm")
		}
		if a.Notches < 0 {
			return NewFieldViolation(af+".notches", "out_of_range", "", "zero or more")
		}
		if !finite(a.TurnDeg) {
			return NewFieldViolation(af+".turn_deg", "out_of_range", "", "a finite angle")
		}
		whole := a.RangeFrom == 0 && a.RangeTo == 0
		if !whole && (!unitInterval(a.RangeFrom) || !unitInterval(a.RangeTo) || a.RangeFrom >= a.RangeTo) {
			return NewFieldViolation(af+".range_from", "out_of_range", "",
				"0 ≤ range_from < range_to ≤ 1, or both 0 for the whole run")
		}
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func unitInterval(x float64) bool { return finite(x) && x >= 0 && x <= 1 }
