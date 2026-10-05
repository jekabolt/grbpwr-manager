package entity

import (
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ─────────────── FLAT ROUTE · THE JOIN LIST (0397) ───────────────
//
// The garment's construction as a list of joins on a FIXED, garment-agnostic landmark ruler
// (tmp/plans/flat-consistency r5.py/r7.py, rounds 5–7): the model writes it from the reference
// photos (chat.design_joins), the designer corrects it, the flat run freezes it into its input
// snapshot and the flat prompt turns it into words. One shape for all three, cleaned by ONE function
// (SanitizeDesignJoinsDoc) whether the model or a person wrote it.

// Caps of a join list. A list over a cap is cut, never refused: it is a hint to a prompt.
const (
	DesignJoinsMaxItems         = 60
	DesignJoinsMaxAbsences      = 20
	DesignJoinsMaxLayers        = 4
	DesignJoinsMaxVia           = 12
	DesignJoinsMaxRefs          = 8 // bounded_by / continues_into
	DesignJoinsMaxTextRunes     = 300
	DesignJoinsMaxAbsenceRunes  = 160
	DesignJoinsMaxIDRunes       = 40
	DesignJoinsMaxLayerName     = 40
	DesignJoinsMaxConsistNote   = 600
	DesignJoinsMaxGroups        = 6
	DesignJoinsMaxCount         = 40
	DesignJoinsMaxTypeRunes     = 24
	DesignJoinsMaxPhotos        = 6 // reference photos one GenerateDesignJoins call reads
	DesignJoinsMaxFingerprint   = 64
	DesignJoinsMaxUncertain     = 10
	designJoinsMaxLandmarkRunes = 40
)

// Join kinds.
const (
	DesignJoinKindEdge      = "edge"
	DesignJoinKindSeam      = "seam"
	DesignJoinKindBinding   = "binding"
	DesignJoinKindBand      = "band"
	DesignJoinKindStrap     = "strap"
	DesignJoinKindCollar    = "collar"
	DesignJoinKindStand     = "stand"
	DesignJoinKindPlacket   = "placket"
	DesignJoinKindCuff      = "cuff"
	DesignJoinKindWaistband = "waistband"
	DesignJoinKindSleeve    = "sleeve"
	DesignJoinKindClosure   = "closure"
	DesignJoinKindPocket    = "pocket"
	DesignJoinKindOpening   = "opening"
)

// Visibility of an item.
const (
	DesignJoinVisible = "visible"
	DesignJoinThrough = "through" // seen through a sheer layer above it
	DesignJoinHidden  = "hidden"
)

// IsDesignJoinKind reports whether k is a join kind.
func IsDesignJoinKind(k string) bool {
	switch k {
	case DesignJoinKindEdge, DesignJoinKindSeam, DesignJoinKindBinding, DesignJoinKindBand,
		DesignJoinKindStrap, DesignJoinKindCollar, DesignJoinKindStand, DesignJoinKindPlacket,
		DesignJoinKindCuff, DesignJoinKindWaistband, DesignJoinKindSleeve, DesignJoinKindClosure,
		DesignJoinKindPocket, DesignJoinKindOpening:
		return true
	}
	return false
}

// DesignJoinBandKinds — the kinds that are a band of their own width (r6.BANDS; binding is apart).
func DesignJoinIsBand(k string) bool {
	switch k {
	case DesignJoinKindStrap, DesignJoinKindBand, DesignJoinKindCollar, DesignJoinKindStand,
		DesignJoinKindPlacket, DesignJoinKindCuff, DesignJoinKindWaistband:
		return true
	}
	return false
}

// designJoinLM — the ruler: (x lateral, wearer's LEFT positive; z depth, FRONT positive; y down from
// the top of the shoulders). r5.py LM, value for value.
var designJoinLM = func() map[string][3]float64 {
	m := map[string][3]float64{
		"CFN": {0, .07, .11}, "CBN": {0, -.07, .06}, "CFN_LOW": {0, .09, .22}, "CBN_LOW": {0, -.09, .2},
		"BUST_C": {0, .1, .38}, "CHEST_C": {0, .1, .27}, "UB_C": {0, -.09, .22}, "MB_C": {0, -.09, .45},
		"WF_C": {0, .1, .55}, "WB_C": {0, -.1, .55}, "HEM_FC": {0, .1, .95}, "HEM_BC": {0, -.1, .95},
	}
	for _, sd := range []struct {
		s  string
		sg float64
	}{{"L", 1}, {"R", -1}} {
		s, sg := sd.s, sd.sg
		add := func(name string, x, z, y float64) { m[name+"_"+s] = [3]float64{sg * x, z, y} }
		add("NP", .06, 0, .05)
		add("SP", .18, 0, .09)
		add("UA", .17, 0, .3)
		add("MB", .16, 0, .45)
		add("WL", .15, 0, .55)
		add("HEM", .17, 0, .95)
		add("CHEST", .09, .1, .25)
		add("BUSTSIDE", .12, .09, .38)
		add("SB", .09, -.09, .22)
		add("YOKE", .17, -.04, .17)
		add("FSH", .12, .05, .07)
		add("BSH", .12, -.05, .07)
		add("ELB_OUT", .27, 0, .55)
		add("WRIST_OUT", .3, 0, .88)
		add("WRIST_IN", .21, 0, .9)
		add("SSLV_OUT", .29, 0, .24)
		add("SSLV_IN", .23, 0, .33)
	}
	return m
}()

// DesignJoinLandmarkHelp — the ruler in words, for the model that writes the list and for the
// model that draws from it (r5.LM_HELP, verbatim).
const DesignJoinLandmarkHelp = `Landmark ruler (fixed for every garment; L/R = WEARER'S left/right):
NP_L/NP_R neck points (side of the neck, top of the shoulder line); CFN centre-front neck; CBN centre-back neck (nape); CFN_LOW / CBN_LOW a low front / back neck centre;
SP_L/SP_R shoulder tips; FSH_x / BSH_x mid-shoulder slightly to the front / back; UA_L/UA_R underarms (top of side seam);
CHEST_x chest (pocket height), CHEST_C centre chest; BUST_C centre front under the bust, BUSTSIDE_x bust side; SB_x shoulder blades, UB_C centre upper back; YOKE_x back yoke seam at the armhole;
MB_x side at mid-back height, MB_C centre back at mid-back; WL_x waist at the side, WF_C / WB_C centre front / back waist; HEM_x hem at the side, HEM_FC / HEM_BC centre front / back hem;
sleeves: SSLV_OUT_x / SSLV_IN_x short-sleeve end outer / inner; ELB_OUT_x elbow outer; WRIST_OUT_x / WRIST_IN_x long-sleeve end outer / inner.
A point between two landmarks is written "A..B:t" (t from 0 at A to 1 at B), e.g. "NP_L..SP_L:0.3".`

// IsDesignJoinLandmark reports whether name is a ruler point, plain or "A..B:t" with t in 0..1.
func IsDesignJoinLandmark(name string) bool {
	_, ok := DesignJoinPoint(name)
	return ok
}

// DesignJoinPoint resolves a ruler point to (x, z, y). False for anything the ruler does not know.
func DesignJoinPoint(name string) ([3]float64, bool) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > designJoinsMaxLandmarkRunes {
		return [3]float64{}, false
	}
	if a, rest, ok := strings.Cut(name, ".."); ok {
		i := strings.LastIndex(rest, ":")
		if i < 0 {
			return [3]float64{}, false
		}
		b, ts := rest[:i], rest[i+1:]
		t, err := strconv.ParseFloat(ts, 64)
		if err != nil || t < 0 || t > 1 {
			return [3]float64{}, false
		}
		pa, oka := designJoinLM[a]
		pb, okb := designJoinLM[b]
		if !oka || !okb {
			return [3]float64{}, false
		}
		var out [3]float64
		for k := range out {
			out[k] = pa[k] + (pb[k]-pa[k])*t
		}
		return out, true
	}
	p, ok := designJoinLM[name]
	return p, ok
}

// DesignJoinSideOf — "L" / "R" / "" for one ruler point (r7.side_of, exactly).
func DesignJoinSideOf(n string) string {
	switch {
	case strings.HasSuffix(n, "_L") || strings.Contains(n, "_L..") || strings.HasSuffix(n, "_L:"):
		return "L"
	case strings.HasSuffix(n, "_R") || strings.Contains(n, "_R.."):
		return "R"
	}
	return ""
}

// DesignJoinLayer — one layer of a multi-layer garment (0 = outermost).
type DesignJoinLayer struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Sheer bool   `json:"sheer,omitempty"`
	Note  string `json:"note,omitempty"`
	// Face — front | back | both | "": a layer is a DEPTH level per face, not a panel.
	Face string `json:"face,omitempty"`
}

// DesignJoinItem — one item of the list. The path is From, Via…, To.
type DesignJoinItem struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	From          string   `json:"from,omitempty"`
	To            string   `json:"to,omitempty"`
	Via           []string `json:"via,omitempty"`
	View          string   `json:"view,omitempty"`
	Side          string   `json:"side,omitempty"`
	Text          string   `json:"text,omitempty"`
	Width         string   `json:"width,omitempty"`
	Closed        bool     `json:"closed,omitempty"`
	Type          string   `json:"type,omitempty"`
	Count         int      `json:"count,omitempty"`
	BoundedBy     []string `json:"bounded_by,omitempty"`
	ContinuesInto []string `json:"continues_into,omitempty"`
	Layer         int      `json:"layer,omitempty"`
	Visibility    string   `json:"visibility,omitempty"`
	// CaughtInto — item ids this edge ends INTO (a seam / binding of another layer).
	CaughtInto []string `json:"caught_into,omitempty"`
	// FreeEdge — the edge hangs free: it does not reach the side seams or the hem.
	FreeEdge bool `json:"free_edge,omitempty"`
	// Sharp — points of the path that are CORNERS (the point of a V, hem corners); the rest is smooth.
	Sharp []string `json:"sharp,omitempty"`
}

// Path — From, Via…, To (empty parts skipped).
func (it DesignJoinItem) Path() []string {
	out := make([]string, 0, len(it.Via)+2)
	if it.From != "" {
		out = append(out, it.From)
	}
	out = append(out, it.Via...)
	if it.To != "" {
		out = append(out, it.To)
	}
	return out
}

// DesignJoinsGroup — photos that show one garment.
type DesignJoinsGroup struct {
	MediaIDs []int  `json:"media_ids"`
	What     string `json:"what,omitempty"`
}

// DesignJoinsConsistency — whether the reference photos show ONE garment.
type DesignJoinsConsistency struct {
	Consistent   bool               `json:"consistent"`
	Note         string             `json:"note,omitempty"`
	KeepMediaIDs []int              `json:"keep_media_ids,omitempty"`
	Groups       []DesignJoinsGroup `json:"groups,omitempty"`
}

// DesignJoinsDoc — the list itself: what is stored in design_joins.joins and frozen into a flat
// run's inputs (`inputs.joins`).
type DesignJoinsDoc struct {
	Layers   []DesignJoinLayer `json:"layers,omitempty"`
	Items    []DesignJoinItem  `json:"items"`
	Absences []string          `json:"absences,omitempty"`
	// Uncertain — the model's honest doubts (questions for the designer; not read by the prompt).
	Uncertain []string `json:"uncertain,omitempty"`
}

// DesignJoins — the card's current list (one row per card).
type DesignJoins struct {
	Id                int
	TechCardId        int
	Rev               int
	Doc               DesignJoinsDoc
	Consistency       DesignJoinsConsistency
	Model             string
	SourceFingerprint string
	EditedBy          string
	EditedAt          sql.NullTime
	CreatedAt         time.Time
}

// DesignJoinsSave — a write of the list. ExpectedRev < 0 = the generator's write (no CAS: the model's
// answer replaces whatever is stored, rev + 1); ≥ 0 = a designer's edit under CAS.
type DesignJoinsSave struct {
	TechCardId        int
	ExpectedRev       int
	Doc               DesignJoinsDoc
	Consistency       DesignJoinsConsistency
	Model             string
	SourceFingerprint string
	Actor             string
	Edited            bool
}

// ErrDesignJoinsRevMismatch — the CAS of SetDesignJoins.
var ErrDesignJoinsRevMismatch = errors.New("design: joins_rev_mismatch")

// ─── the cleaner ───

var designJoinNegation = regexp.MustCompile(`^(no|not|none|nothing|without|never|zero)\b`)

// DesignJoinIsNegation — an absence must SAY that something is absent: it starts with a negation
// word. «collar and placket edges are not cleanly finished; they are … raw» (round 7) does not — it is
// a positive note that landed in the wrong list, and as an absence it became «Draw NOTHING for: …».
func DesignJoinIsNegation(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "-•* ")
	return designJoinNegation.MatchString(s)
}

func designJoinsTrim(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

// designJoinsID — the id as a key: ASCII letters (case kept: strap_L is the wearer's left), digits
// and single underscores.
func designJoinsID(s string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.TrimSpace(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = false
			b.WriteRune(r)
			continue
		}
		under = true
	}
	out := b.String()
	if utf8.RuneCountInString(out) > DesignJoinsMaxIDRunes {
		out = string([]rune(out)[:DesignJoinsMaxIDRunes])
	}
	return out
}

func designJoinLandmark(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "")
}

// DesignJoinFace — "front" / "back" / "" for a path: the mean depth of its points (r7's rule for a
// diagonal band), with a dead zone for paths that run along the side.
func DesignJoinFace(path []string) string {
	if len(path) == 0 {
		return ""
	}
	sum, n := 0.0, 0
	for _, p := range path {
		if pt, ok := DesignJoinPoint(p); ok {
			sum += pt[1]
			n++
		}
	}
	if n == 0 {
		return ""
	}
	m := sum / float64(n)
	switch {
	case m > 0.015:
		return DesignViewFront
	case m < -0.015:
		return DesignViewBack
	}
	return ""
}

// designJoinSideOfPath — the wearer's side every point of the path lies on, "" when mixed or central.
func designJoinSideOfPath(path []string) string {
	side := ""
	for _, p := range path {
		s := DesignJoinSideOf(p)
		if s == "" {
			return ""
		}
		if side != "" && s != side {
			return ""
		}
		side = s
	}
	return side
}

// SanitizeDesignJoinsDoc cleans a list, the model's or a person's, into what the prompt may read:
//   - an unknown kind drops the item; an unknown landmark drops the point (an end that is unknown
//     drops the item, a pocket without a known anchor too);
//   - ids are slugged and made unique; references to ids that are not in the list are dropped;
//   - absences are kept only when they are negations (DesignJoinIsNegation);
//   - layers are 0..MaxLayers-1, an item's layer is clamped to the known layers; visibility is one of
//     visible | through | hidden (default visible);
//   - view and side are derived from the path when not stated (or stated wrong);
//   - every count and length is capped.
func SanitizeDesignJoinsDoc(in DesignJoinsDoc) DesignJoinsDoc {
	out := DesignJoinsDoc{Items: []DesignJoinItem{}}

	seenLayer := map[int]bool{}
	for _, l := range in.Layers {
		if len(out.Layers) == DesignJoinsMaxLayers {
			break
		}
		if l.Index < 0 || l.Index >= DesignJoinsMaxLayers || seenLayer[l.Index] {
			continue
		}
		seenLayer[l.Index] = true
		face := strings.ToLower(strings.TrimSpace(l.Face))
		if face != DesignViewFront && face != DesignViewBack && face != "both" {
			face = ""
		}
		out.Layers = append(out.Layers, DesignJoinLayer{
			Index: l.Index, Name: designJoinsTrim(l.Name, DesignJoinsMaxLayerName), Sheer: l.Sheer,
			Note: designJoinsTrim(l.Note, DesignJoinsMaxTextRunes), Face: face,
		})
	}
	maxLayer := 0
	for _, l := range out.Layers {
		if l.Index > maxLayer {
			maxLayer = l.Index
		}
	}

	ids := map[string]bool{}
	for _, it := range in.Items {
		if len(out.Items) == DesignJoinsMaxItems {
			break
		}
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		if !IsDesignJoinKind(kind) {
			continue
		}
		c := DesignJoinItem{Kind: kind}
		switch kind {
		case DesignJoinKindOpening:
			// bounded_by is resolved below, once every id is known.
		case DesignJoinKindPocket:
			a := designJoinLandmark(it.From)
			if !IsDesignJoinLandmark(a) {
				continue
			}
			c.From = a
		default:
			from, to := designJoinLandmark(it.From), designJoinLandmark(it.To)
			if !IsDesignJoinLandmark(from) || !IsDesignJoinLandmark(to) {
				continue
			}
			c.From, c.To = from, to
			for _, v := range it.Via {
				if len(c.Via) == DesignJoinsMaxVia {
					break
				}
				if v = designJoinLandmark(v); IsDesignJoinLandmark(v) {
					c.Via = append(c.Via, v)
				}
			}
			c.Closed = it.Closed
		}
		c.Text = designJoinsTrim(it.Text, DesignJoinsMaxTextRunes)
		switch w := strings.ToLower(strings.TrimSpace(it.Width)); w {
		case "narrow", "wide":
			c.Width = w
		}
		c.Type = strings.ToLower(designJoinsTrim(it.Type, DesignJoinsMaxTypeRunes))
		if it.Count > 0 {
			c.Count = min(it.Count, DesignJoinsMaxCount)
		}
		if it.Layer > 0 {
			c.Layer = min(it.Layer, maxLayer)
		}
		switch v := strings.ToLower(strings.TrimSpace(it.Visibility)); v {
		case DesignJoinThrough, DesignJoinHidden:
			c.Visibility = v
		default:
			c.Visibility = DesignJoinVisible
		}
		// «through» needs a SHEER layer above it — when the list names its layers at all.
		if c.Visibility == DesignJoinThrough && len(out.Layers) > 0 && !designJoinsSheerAbove(out.Layers, c.Layer) {
			c.Visibility = DesignJoinVisible
		}
		c.FreeEdge = it.FreeEdge
		// view / side: what the path says wins; a stated word survives only where the path is silent.
		path := c.Path()
		if f := DesignJoinFace(path); f != "" {
			c.View = f
		} else if v := strings.ToLower(strings.TrimSpace(it.View)); v == DesignViewFront || v == DesignViewBack {
			c.View = v
		}
		if s := designJoinSideOfPath(path); s != "" {
			c.Side = s
		} else if s := strings.ToUpper(strings.TrimSpace(it.Side)); (s == "L" || s == "R") && len(path) == 0 {
			c.Side = s
		}
		id := designJoinsID(it.ID)
		if id == "" {
			id = kind
		}
		base := id
		for n := 2; ids[id]; n++ {
			id = base + "_" + strconv.Itoa(n)
		}
		ids[id] = true
		c.ID = id
		// the raw references travel to the second pass below
		c.BoundedBy = append([]string(nil), it.BoundedBy...)
		c.ContinuesInto = append([]string(nil), it.ContinuesInto...)
		c.CaughtInto = append([]string(nil), it.CaughtInto...)
		// sharp: only points of this item's own path, once each
		onPath := map[string]bool{}
		for _, pt := range c.Path() {
			onPath[pt] = true
		}
		for _, pt := range it.Sharp {
			if pt = designJoinLandmark(pt); onPath[pt] {
				onPath[pt] = false
				c.Sharp = append(c.Sharp, pt)
			}
		}
		out.Items = append(out.Items, c)
	}
	refs := func(list []string, self string) []string {
		var keep []string
		seen := map[string]bool{}
		for _, r := range list {
			r = designJoinsID(r)
			if r == "" || r == self || !ids[r] || seen[r] || len(keep) == DesignJoinsMaxRefs {
				continue
			}
			seen[r] = true
			keep = append(keep, r)
		}
		return keep
	}
	kept := out.Items[:0]
	for _, it := range out.Items {
		it.BoundedBy = refs(it.BoundedBy, it.ID)
		it.ContinuesInto = refs(it.ContinuesInto, it.ID)
		it.CaughtInto = refs(it.CaughtInto, it.ID)
		if it.Kind == DesignJoinKindOpening && len(it.BoundedBy) == 0 {
			continue
		}
		if it.Kind != DesignJoinKindOpening {
			it.BoundedBy = nil
		}
		kept = append(kept, it)
	}
	out.Items = kept

	seen := map[string]bool{}
	for _, a := range in.Absences {
		if len(out.Absences) == DesignJoinsMaxAbsences {
			break
		}
		a = designJoinsTrim(a, DesignJoinsMaxAbsenceRunes)
		if a == "" || !DesignJoinIsNegation(a) || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out.Absences = append(out.Absences, a)
	}
	for _, u := range in.Uncertain {
		if len(out.Uncertain) == DesignJoinsMaxUncertain {
			break
		}
		if u = designJoinsTrim(u, DesignJoinsMaxAbsenceRunes); u != "" {
			out.Uncertain = append(out.Uncertain, u)
		}
	}
	return out
}

// designJoinsSheerAbove — some layer above `layer` (a smaller index) is sheer.
func designJoinsSheerAbove(layers []DesignJoinLayer, layer int) bool {
	for _, l := range layers {
		if l.Index < layer && l.Sheer {
			return true
		}
	}
	return false
}

// SanitizeDesignJoinsConsistency keeps only the photos the call actually read (allowed); an empty
// keep list on a consistent verdict means every photo.
func SanitizeDesignJoinsConsistency(in DesignJoinsConsistency, allowed []int) DesignJoinsConsistency {
	ok := make(map[int]bool, len(allowed))
	for _, id := range allowed {
		ok[id] = true
	}
	pick := func(list []int) []int {
		var outIDs []int
		seen := map[int]bool{}
		for _, id := range list {
			if ok[id] && !seen[id] {
				seen[id] = true
				outIDs = append(outIDs, id)
			}
		}
		return outIDs
	}
	out := DesignJoinsConsistency{
		Consistent:   in.Consistent,
		Note:         designJoinsTrim(in.Note, DesignJoinsMaxConsistNote),
		KeepMediaIDs: pick(in.KeepMediaIDs),
	}
	for _, g := range in.Groups {
		if len(out.Groups) == DesignJoinsMaxGroups {
			break
		}
		ids := pick(g.MediaIDs)
		if len(ids) == 0 {
			continue
		}
		out.Groups = append(out.Groups, DesignJoinsGroup{MediaIDs: ids, What: designJoinsTrim(g.What, DesignJoinsMaxTextRunes)})
	}
	if len(out.KeepMediaIDs) == 0 && out.Consistent {
		out.KeepMediaIDs = pick(allowed)
	}
	return out
}
