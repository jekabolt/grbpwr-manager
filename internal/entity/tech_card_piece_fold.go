package entity

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// PIECE FOLDS (tech_card_piece_fold, 0443) — a fold line on one piece: a placket strip folded in half,
// a cuff, a hem turned up. Construction data the paper doll folds BEFORE the solve; the solver never
// discovers or moves a fold (tmp/plans/doll-folds/02-FOLD-SYNTHESIS.md).
//
// One row = one STRAIGHT line on one piece, applied in Seq order: a double turn-up is two rows, a
// grown-on placket folded twice is two rows. The line lives in the piece's own frame (the frame of
// TechCardSeamAnchor.samples), with an optional EDGE LOCATOR — the edge the fold runs along and its
// distance in mm — so grading keeps a hem allowance exact; the browser resolves both (the server does
// not parse DXF). What the server owns is the staleness of the SOURCE, by the seams' own device.
//
// OUTSIDE EVERY SECTION DIGEST AND OUTSIDE lock_version, as the seams: the folds ride the read model
// (TechCard.PieceFolds), never TechCardInsert.

// Bounds of one fold write. Field-addressed refusals name them; nothing here is a DB CHECK.
const (
	TechCardPieceFoldMaxRowsPerCard   = 200 // a garment has a handful of folds; 200 is a runaway client
	TechCardPieceFoldMaxSeq           = 16  // folds on ONE piece; a double turn-up is 2
	TechCardPieceFoldLineSamples      = 2
	TechCardPieceFoldEdgeSamples      = 5 // the shape of TechCardSeamAnchor.samples
	TechCardPieceFoldMaxOffsetMm      = 500
	TechCardPieceFoldMinLineLenUV     = 1e-3 // two distinct points in the unit square
	TechCardPieceFoldMinFlipDistUV    = 1e-4 // flip_at must lie off the line
	TechCardPieceFoldNoteMaxRunes     = 255
	TechCardPieceFoldAnchoredSizeRune = 16
)

// TechCardPieceFoldKind is which way the flipping part goes.
type TechCardPieceFoldKind string

const (
	// TechCardPieceFoldInward: the flap lands on the piece's inside face (toward the body).
	TechCardPieceFoldInward TechCardPieceFoldKind = "inward"
	// TechCardPieceFoldOutward: the flap lands on the piece's outside face.
	TechCardPieceFoldOutward TechCardPieceFoldKind = "outward"
)

// TechCardPieceFoldSource is where the line came from.
type TechCardPieceFoldSource string

const (
	TechCardPieceFoldSourceDXF          TechCardPieceFoldSource = "dxf"
	TechCardPieceFoldSourceOwner        TechCardPieceFoldSource = "owner"
	TechCardPieceFoldSourceAcceptedHint TechCardPieceFoldSource = "accepted_hint"
)

// TechCardPieceFoldInput is one fold as a client submits it (keyed upsert, replaced whole). Line,
// FlipAt and EdgeSamples are stored as JSON with TechCardSeamSample's keys ({"u","v"}).
type TechCardPieceFoldInput struct {
	FoldKey      string
	PieceLineKey string
	Seq          int
	Kind         TechCardPieceFoldKind
	Source       TechCardPieceFoldSource
	Line         []TechCardSeamSample
	FlipAt       TechCardSeamSample
	// Edge locator — all or nothing: EdgeSamples empty, EdgeHint "" and OffsetMm 0, or five samples
	// and OffsetMm > 0 (EdgeHint optional, a fast path).
	EdgeHint     string
	EdgeSamples  []TechCardSeamSample
	OffsetMm     float64
	ContourSig   string
	AnchoredSize string
	Note         string
}

// HasEdge reports whether the fold carries an edge locator.
func (in TechCardPieceFoldInput) HasEdge() bool { return len(in.EdgeSamples) > 0 }

// TechCardPieceFold is one stored fold with the server's verdict resolved on read.
type TechCardPieceFold struct {
	TechCardPieceFoldInput
	TechCardId int
	// SizeId is reserved (NULL = every size; v1 writes no per-size rows).
	SizeId            sql.NullInt64
	SourceFingerprint string
	// Stale: the fingerprint of the piece's source, computed NOW, differs from the stored one.
	// Computed on every read, never stored.
	Stale     bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedBy string
	UpdatedAt time.Time
}

// TechCardPieceFoldsWrite is one UpsertTechCardPieceFolds call.
type TechCardPieceFoldsWrite struct {
	TechCardId int
	Folds      []TechCardPieceFoldInput
	By         string
}

var techCardPieceFoldKinds = map[TechCardPieceFoldKind]bool{
	TechCardPieceFoldInward: true, TechCardPieceFoldOutward: true,
}

var techCardPieceFoldSources = map[TechCardPieceFoldSource]bool{
	TechCardPieceFoldSourceDXF: true, TechCardPieceFoldSourceOwner: true, TechCardPieceFoldSourceAcceptedHint: true,
}

// ValidateTechCardPieceFoldsWrite checks everything about a fold write that does not need the
// database: dictionaries, shapes, bounds, duplicates within the call. The store adds what does —
// the piece must be a piece of THIS card, and seq stays unique per piece over the whole stored list.
// Errors are field-addressed (folds[i].…). Normalises keys and trims strings in place.
func ValidateTechCardPieceFoldsWrite(in *TechCardPieceFoldsWrite) error {
	if in.TechCardId <= 0 {
		return NewFieldViolation("tech_card_id", "required", "", "")
	}
	if len(in.Folds) == 0 {
		return NewFieldViolation("folds", "empty", "", "send at least one fold; to remove rows use DeleteTechCardPieceFolds")
	}
	if len(in.Folds) > TechCardPieceFoldMaxRowsPerCard {
		return NewFieldViolation("folds", "too_many", "",
			fmt.Sprintf("at most %d folds per card", TechCardPieceFoldMaxRowsPerCard))
	}
	seenKey := make(map[string]int, len(in.Folds))
	seenSeq := make(map[string]int, len(in.Folds))
	for i := range in.Folds {
		f := &in.Folds[i]
		at := fmt.Sprintf("folds[%d]", i)
		f.FoldKey = NormalizeTechCardSeamKey(f.FoldKey)
		if !ValidTechCardSeamKey(f.FoldKey) {
			return NewFieldViolation(at+".fold_key", "invalid", "", "a 26-char ULID minted by the client")
		}
		if j, dup := seenKey[f.FoldKey]; dup {
			return NewFieldViolation(at+".fold_key", "duplicate", fmt.Sprintf("folds[%d]", j),
				"one row per fold_key per call")
		}
		seenKey[f.FoldKey] = i
		f.PieceLineKey = strings.TrimSpace(f.PieceLineKey)
		if f.PieceLineKey == "" {
			return NewFieldViolation(at+".piece_line_key", "required", "", "the piece's line_key")
		}
		if len(f.PieceLineKey) > TechCardSeamPieceKeyMaxBytes {
			return NewFieldViolation(at+".piece_line_key", "too_long", "", "the piece's line_key")
		}
		if f.Seq < 1 || f.Seq > TechCardPieceFoldMaxSeq {
			return NewFieldViolation(at+".seq", "out_of_range", "",
				fmt.Sprintf("1..%d — the order of the folds on the piece", TechCardPieceFoldMaxSeq))
		}
		seqKey := fmt.Sprintf("%s\x00%d", strings.ToUpper(f.PieceLineKey), f.Seq)
		if j, dup := seenSeq[seqKey]; dup {
			return NewFieldViolation(at+".seq", "duplicate", fmt.Sprintf("folds[%d]", j),
				"one fold per seq on a piece")
		}
		seenSeq[seqKey] = i
		if !techCardPieceFoldKinds[f.Kind] {
			return NewFieldViolation(at+".kind", "invalid", string(f.Kind), "inward or outward")
		}
		if !techCardPieceFoldSources[f.Source] {
			return NewFieldViolation(at+".source", "invalid", string(f.Source), "dxf, owner or accepted_hint")
		}
		if len(f.Line) != TechCardPieceFoldLineSamples {
			return NewFieldViolation(at+".line", "count", "",
				fmt.Sprintf("exactly %d points of the straight line", TechCardPieceFoldLineSamples))
		}
		for k, p := range f.Line {
			if !unitInterval(p.U) || !unitInterval(p.V) {
				return NewFieldViolation(fmt.Sprintf("%s.line[%d]", at, k), "out_of_frame", "",
					"u and v are in [0, 1] — the piece's bbox-normalised frame")
			}
		}
		a, b := f.Line[0], f.Line[1]
		lineLen := math.Hypot(b.U-a.U, b.V-a.V)
		if lineLen < TechCardPieceFoldMinLineLenUV {
			return NewFieldViolation(at+".line", "degenerate", "", "two distinct points")
		}
		if !unitInterval(f.FlipAt.U) || !unitInterval(f.FlipAt.V) {
			return NewFieldViolation(at+".flip_at", "out_of_frame", "",
				"u and v are in [0, 1] — the piece's bbox-normalised frame")
		}
		// Distance of flip_at from the line: |cross| / |b − a|.
		cross := (b.U-a.U)*(f.FlipAt.V-a.V) - (b.V-a.V)*(f.FlipAt.U-a.U)
		if math.Abs(cross)/lineLen < TechCardPieceFoldMinFlipDistUV {
			return NewFieldViolation(at+".flip_at", "on_line", "",
				"a point on the part that flips, off the fold line")
		}
		f.EdgeHint = strings.TrimSpace(f.EdgeHint)
		if utf8.RuneCountInString(f.EdgeHint) > TechCardSeamHintMaxRunes {
			return NewFieldViolation(at+".edge_hint", "too_long", "", "")
		}
		if err := validateTechCardPieceFoldEdge(at, f); err != nil {
			return err
		}
		f.ContourSig = strings.TrimSpace(f.ContourSig)
		if utf8.RuneCountInString(f.ContourSig) > TechCardSeamHintMaxRunes {
			return NewFieldViolation(at+".contour_sig", "too_long", "", "")
		}
		f.AnchoredSize = strings.TrimSpace(f.AnchoredSize)
		if utf8.RuneCountInString(f.AnchoredSize) > TechCardPieceFoldAnchoredSizeRune {
			return NewFieldViolation(at+".anchored_size", "too_long", "",
				fmt.Sprintf("at most %d characters", TechCardPieceFoldAnchoredSizeRune))
		}
		f.Note = strings.TrimSpace(f.Note)
		if utf8.RuneCountInString(f.Note) > TechCardPieceFoldNoteMaxRunes {
			return NewFieldViolation(at+".note", "too_long", "",
				fmt.Sprintf("at most %d characters", TechCardPieceFoldNoteMaxRunes))
		}
	}
	return nil
}

// validateTechCardPieceFoldEdge: the locator is all or nothing — no samples means no hint and no
// offset (a hint alone is a corner number, never trusted alone); five samples need an offset in mm.
func validateTechCardPieceFoldEdge(at string, f *TechCardPieceFoldInput) error {
	if !finite(f.OffsetMm) {
		return NewFieldViolation(at+".offset_mm", "out_of_range", "", "finite millimetres")
	}
	if len(f.EdgeSamples) == 0 {
		if f.EdgeHint != "" {
			return NewFieldViolation(at+".edge_samples", "required", "",
				"an edge hint travels with the edge's five samples")
		}
		if f.OffsetMm != 0 {
			return NewFieldViolation(at+".edge_samples", "required", "",
				"an offset is measured from an edge: send the edge's five samples")
		}
		return nil
	}
	if len(f.EdgeSamples) != TechCardPieceFoldEdgeSamples {
		return NewFieldViolation(at+".edge_samples", "count", "",
			fmt.Sprintf("exactly %d samples along the edge, or none", TechCardPieceFoldEdgeSamples))
	}
	for k, p := range f.EdgeSamples {
		if !unitInterval(p.U) || !unitInterval(p.V) {
			return NewFieldViolation(fmt.Sprintf("%s.edge_samples[%d]", at, k), "out_of_frame", "",
				"u and v are in [0, 1] — the piece's bbox-normalised frame")
		}
	}
	// DECIMAL(7,2): two decimals are what the column keeps; round first so the echo equals the write
	// and a sub-0.005 offset cannot land as a 0 beside five samples.
	f.OffsetMm = math.Round(f.OffsetMm*100) / 100
	if f.OffsetMm <= 0 || f.OffsetMm > TechCardPieceFoldMaxOffsetMm {
		return NewFieldViolation(at+".offset_mm", "out_of_range", "",
			fmt.Sprintf("the fold's distance from the edge, in (0, %d] mm", TechCardPieceFoldMaxOffsetMm))
	}
	return nil
}
