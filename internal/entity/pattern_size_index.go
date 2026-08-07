package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ИНДЕКС РАЗМЕРОВ ВЫКРОЕК (Ф6.3, 0280): the only thing the server can honestly answer «is there a
// pattern for this size» from.
//
// WHY THE SERVER HAS NOTHING ELSE. tech_card_size_pattern.size_id is a STORAGE ARTEFACT: the client
// files every sheet of a card under the smallest size of the range and says so in its own comment.
// The existing readiness row that counts DISTINCT size_id therefore does not merely approximate the
// truth — it reports a different quantity and calls it coverage. The real answer lives in the DXF
// block names, the server does not parse DXF, and it is not going to start.
//
// SO THE CLIENT PARSES AND THE SERVER STORES — but only the half that cannot be forged in the
// dangerous direction. The dangerous direction is CLAIMING COVERAGE THAT DOES NOT EXIST, and it is
// closed by the fingerprint: the client says WHICH SHEETS it parsed, the SERVER computes the
// fingerprint of that set from its own rows, and a re-upload of any sheet changes the url/version
// and makes the stored fingerprint stop matching. The index then reads as STALE, i.e. as no verdict,
// with nobody having to notice. Same device as 0268's blob-version exemption: the value that grants
// a pass is written by the server and only from its own facts.

// PatternSheetRef is one pattern sheet as the fingerprint sees it. Three fields and no more:
// line_key answers WHICH sheet (stable across a file replacement), url and version answer WHICH FILE
// is currently under that identity. A replacement changes at least one of the latter two, which is
// precisely the event the fingerprint has to catch.
type PatternSheetRef struct {
	LineKey string
	URL     string
	Version int
}

// patternSheetDigestEntry is the hashed projection. Field names are one letter because they are
// hashed and never read; what matters is that the ENCODING IS PINNED BY TYPE, the same reason
// PieceSetFingerprint spells its projection out rather than marshalling a []any.
type patternSheetDigestEntry struct {
	K string `json:"k"`
	U string `json:"u"`
	V int    `json:"v"`
}

// PatternSheetFingerprint hashes a scope's CURRENT sheet set.
//
// Sorted by (line_key, url, version) so the answer does not depend on the query's ORDER BY, and
// trimmed + upper-cased on the key for the same reason PieceSetFingerprint does it: ULIDs are
// upper-case already, so the fold costs nothing and makes the digest immune to a collation change
// between prod (utf8mb3) and a test container (utf8mb4).
//
// AN EMPTY SET HASHES STABLY rather than returning "". A scope with no sheets is a real state, and
// it has to stay distinguishable from «we could not compute a fingerprint» — which this function
// deliberately cannot return, because there is no input it cannot hash.
func PatternSheetFingerprint(sheets []PatternSheetRef) string {
	projection := make([]patternSheetDigestEntry, 0, len(sheets))
	for _, s := range sheets {
		projection = append(projection, patternSheetDigestEntry{
			K: strings.ToUpper(strings.TrimSpace(s.LineKey)),
			U: strings.TrimSpace(s.URL),
			V: s.Version,
		})
	}
	sort.Slice(projection, func(i, j int) bool {
		if projection[i].K != projection[j].K {
			return projection[i].K < projection[j].K
		}
		if projection[i].U != projection[j].U {
			return projection[i].U < projection[j].U
		}
		return projection[i].V < projection[j].V
	})
	b, err := json.Marshal(projection)
	if err != nil {
		// Plain data; a marshal failure is a programming error. Hash the empty projection rather than
		// returning a value that could accidentally equal a stored one.
		b = []byte("[]")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PatternSizeIndexRow is one stored index row: the size tokens of one fabric scope, plus the
// fingerprint of the sheet set they were derived from.
type PatternSizeIndexRow struct {
	TechCardId int    `db:"tech_card_id"`
	ScopeKey   string `db:"scope_key"`
	// SheetFingerprint is what PatternSheetFingerprint returned for the scope's sheets AT THE MOMENT
	// OF THE WRITE. Compared against today's on every read; a mismatch is STALE, never «no sizes».
	SheetFingerprint string `db:"sheet_fingerprint"`
	// SizeTokensJSON is the raw column. Kept raw rather than decoded on scan because a row that is
	// not a JSON array of strings must read as NO INDEX (see Tokens), and a scan-time decode error
	// would instead fail the whole card read.
	SizeTokensJSON string    `db:"size_tokens"`
	ParsedBy       string    `db:"parsed_by"`
	ParsedAt       time.Time `db:"parsed_at"`
}

// Tokens decodes the stored token array. ok=false means the column does not hold a JSON array of
// strings, and the caller MUST treat that as «there is no index» rather than as an empty set: an
// empty set is a meaningful answer («the files carry no size coding») and a corrupt column is not
// entitled to make that statement.
//
// Tokens come back normalised through NormalizeSizeToken even though the writer normalises too — a
// row written by an older binary, or by hand at the mysql prompt, must not be able to make a size
// look uncovered because of a stray bracket.
func (r PatternSizeIndexRow) Tokens() (map[string]bool, bool) {
	raw := strings.TrimSpace(r.SizeTokensJSON)
	if raw == "" {
		return nil, false
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, false
	}
	out := make(map[string]bool, len(list))
	for _, t := range list {
		if n := NormalizeSizeToken(t); n != "" {
			out[n] = true
		}
	}
	return out, true
}

// PatternSizeIndexState is how a stored index reads TODAY. Four states and only ONE of them lets the
// gate produce a verdict — see RunReadinessSizesInDxf.
type PatternSizeIndexState int

const (
	// PatternSizeIndexMissing — no row, or a row whose token column is not a JSON array of strings.
	// Nobody has run «⌕ размеры в файлах» on this scope (or the row is unreadable).
	PatternSizeIndexMissing PatternSizeIndexState = iota
	// PatternSizeIndexStale — the sheet set changed after the index was written. A sheet was
	// uploaded, replaced or removed, so the tokens describe files that are no longer the ones in play.
	PatternSizeIndexStale
	// PatternSizeIndexUngraded — the index is current and its token set is EMPTY. This is a LEGAL
	// answer and it means «the files carry no size coding»: one size per file yields no tokens,
	// because deriveBlockSizes requires at least two size tails on one name stem before it will call
	// anything a size. Reading it as «no sizes are present» would be a false blocker on every size at
	// once — exactly the failure the patterns panel already refuses to make.
	PatternSizeIndexUngraded
	// PatternSizeIndexUsable — the index is current and carries tokens. Only here can a size be
	// judged present or absent.
	PatternSizeIndexUsable
)

// PatternSizeIndexStatus reads a stored row against the scope's fingerprint today.
func PatternSizeIndexStatus(row *PatternSizeIndexRow, currentFingerprint string) (PatternSizeIndexState, map[string]bool) {
	if row == nil {
		return PatternSizeIndexMissing, nil
	}
	tokens, ok := row.Tokens()
	if !ok {
		return PatternSizeIndexMissing, nil
	}
	if row.SheetFingerprint != currentFingerprint {
		return PatternSizeIndexStale, nil
	}
	if len(tokens) == 0 {
		return PatternSizeIndexUngraded, nil
	}
	return PatternSizeIndexUsable, tokens
}

// PatternSizeIndexWrite is one call of PutTechCardPatternSizeIndex.
//
// Note what is NOT here: the fingerprint. It is the server's, computed from the server's own sheet
// rows, and a field for it on this struct would be an invitation to accept one.
type PatternSizeIndexWrite struct {
	TechCardId int
	ScopeKey   string
	// SheetLineKeys is the set of sheets the CLIENT parsed. It is checked against the scope's current
	// membership and a mismatch is refused in BOTH directions — see the store for why a partial parse
	// is not a subset of a full one.
	SheetLineKeys []string
	SizeTokens    []string
	ParsedBy      string
}

// PatternSizeIndexResult is what the write recorded, for the caller to echo back.
//
// CardSizeIds travels out rather than a resolved count travelling in: resolving a token to a size
// needs the size DICTIONARY, which lives in the API layer's cache, and pushing a lookup callback
// into the store would put a rule («which token spells this size») on the wrong side of the layer
// boundary. The store reports what the card's range IS; the caller resolves.
type PatternSizeIndexResult struct {
	SheetFingerprint string
	StoredTokens     []string
	CardSizeIds      []int
}

// NormalizeSizeTokens cleans an incoming token list for storage: normalised, de-duplicated, sorted.
// Sorting is not cosmetic — the column is compared by eye during debugging and diffed between two
// runs of the same audit, and an order that depended on map iteration would make two identical
// parses look like two different answers.
func NormalizeSizeTokens(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		n := NormalizeSizeToken(t)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
