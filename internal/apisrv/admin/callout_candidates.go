package admin

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ─────────────── CALLOUT SUGGESTIONS · THE CANDIDATES (T28, R36) ───────────────
//
// The deterministic half of SuggestCallouts: from the SAVED card, the callouts its own data implies
// (40-SUGGEST v2, 41-PLM-RESEARCH §4, 42-CONTRACT), minus those the sheet already has, minus those the
// designer dismissed, ranked: closures & stress points → trims & labels → artwork → distinct seam
// specs → fabric zones → details. Spec, description and parts of every candidate are rendered HERE
// from the source row; the model only decides where a candidate goes (callout_suggest.go).

// Ranks — the order the PLM research settled (41 §4.3): what a factory gets wrong first goes first.
const (
	calloutRankClosure = iota // closures and stress points: zips, buttons, snaps, rivets, bartacks
	calloutRankTrim           // other trims and hardware, garment labels
	calloutRankArtwork        // prints, embroidery, patches …
	calloutRankSeam           // one per distinct seam spec
	calloutRankFabric         // fabric zones (main / contrast) and the layer section
	calloutRankDetail         // details aspects and STUDIO quiz decisions
)

// Purposes (the client's purpose.ts PURPOSE_KEYS) and the geometry each is placed with.
const (
	calloutPurposeNote     = "note"
	calloutPurposeDetail   = "detail"
	calloutPurposeArtwork  = "artwork"
	calloutPurposeStitch   = "stitch"
	calloutPurposeMaterial = "material"
	calloutPurposeSection  = "section"
)

// calloutMaxCandidates bounds the list the model reads: 4 flats × 12 is the most that can come back.
const calloutMaxCandidates = 48

// calloutDescriptionMaxRunes — the callout text, data-backed or model-own (42-CONTRACT).
const calloutDescriptionMaxRunes = 120

// calloutCandidate — one callout the card's data implies.
type calloutCandidate struct {
	sourceID    string
	sourceLabel string
	purpose     string
	rank        int
	order       int // card order inside the rank
	spec        string
	description string
	parts       []string
	missing     []string
	facts       string // one line for the model
	view        string // where it usually shows: "front", "back" or ""
}

// ─── ISO 4915 from the machine (THE server copy of the client's equipment-options.ts) ───

// iso4915Fixed — machine types whose stitch is one and depends on nothing (client ISO4915_FIXED).
// seam_taping and ultrasonic_welder make no stitch and deliberately have no entry.
var iso4915Fixed = map[string]string{
	"lockstitch":               "301",
	"lockstitch_double_needle": "301", // two rows of ONE stitch: 301 stays 301
	"chainstitch":              "401",
	"blindstitch":              "103",
	"zigzag":                   "304",
}

// iso4915ByThreads — machine types whose stitch is decided by the thread count (client ISO4915_BY_THREADS).
var iso4915ByThreads = map[string]map[int]string{
	"overlock": {3: "504", 4: "514", 5: "516"},
}

// iso4915StitchType — the ISO 4915 number of ONE step; "" when the machine does not decide it and
// no thread count says which: an empty string is more honest than a guess (client stitchTypeNumber).
func iso4915StitchType(machineType string, threadCount int) string {
	if n, ok := iso4915Fixed[machineType]; ok {
		return n
	}
	if by, ok := iso4915ByThreads[machineType]; ok {
		return by[threadCount]
	}
	return ""
}

// ─── spec JSON (purpose.ts parseSpec / writeSpec) ───

// renderCalloutSpec writes a spec object the way the client's writeSpec does: keys sorted (Go sorts
// map keys), empty strings dropped, always an object.
func renderCalloutSpec(fields map[string]any) string {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// calloutSpecDefault — the minimal spec of a purpose (the client's PurposeTool.defaults).
func calloutSpecDefault(purpose, artworkSub string) string {
	switch purpose {
	case calloutPurposeDetail:
		return renderCalloutSpec(map[string]any{"t": purpose, "scale": 2})
	case calloutPurposeArtwork:
		if !calloutArtworkSubs[artworkSub] {
			artworkSub = "print"
		}
		return renderCalloutSpec(map[string]any{"t": purpose, "sub": artworkSub})
	case calloutPurposeSection:
		return renderCalloutSpec(map[string]any{"t": purpose, "layers": []map[string]string{}})
	}
	return renderCalloutSpec(map[string]any{"t": purpose})
}

var calloutArtworkSubs = map[string]bool{"print": true, "embroidery": true, "label": true, "branding": true}

// storedCalloutSpec — the fields of a stored callout spec the coverage rules read.
type storedCalloutSpec struct {
	T       string `json:"t"`
	LineKey string `json:"lineKey"`
	Sub     string `json:"sub"`
	From    string `json:"from"`
	Layers  []struct {
		Name string `json:"name"`
	} `json:"layers"`
}

func parseStoredCalloutSpec(raw string) storedCalloutSpec {
	var s storedCalloutSpec
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return storedCalloutSpec{}
	}
	return s
}

// ─── the card's existing callouts (coverage) ───

type existingCallout struct {
	number      int
	mediaID     int
	spec        storedCalloutSpec
	description string
	parts       []string // lower-cased
	x, y        float64
	hasPos      bool
}

// sheetCallouts — the callouts of the card that live on the SHEET (a technical picture, or none):
// moodboard callouts are a separate surface and cover nothing here.
func sheetCallouts(card *entity.TechCard) []existingCallout {
	technical := map[int]bool{}
	for _, m := range card.Media {
		if m.Category == entity.TechCardMediaCategoryTechnical {
			technical[m.MediaId] = true
		}
	}
	var out []existingCallout
	for _, c := range card.Callouts {
		mid := 0
		if c.MediaId.Valid {
			mid = int(c.MediaId.Int32)
		}
		if mid != 0 && !technical[mid] {
			continue
		}
		e := existingCallout{number: c.Number, mediaID: mid, spec: parseStoredCalloutSpec(c.Spec.String),
			description: strings.ToLower(designOneLine(c.Description.String))}
		parts := c.Parts
		if len(parts) == 0 && c.Part.Valid {
			parts = []string{c.Part.String}
		}
		for _, p := range parts {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				e.parts = append(e.parts, p)
			}
		}
		if c.PosX.Valid && c.PosY.Valid {
			e.x, _ = c.PosX.Decimal.Float64()
			e.y, _ = c.PosY.Decimal.Float64()
			e.hasPos = true
		} else if len(c.Points) > 0 {
			e.x, _ = c.Points[0].X.Float64()
			e.y, _ = c.Points[0].Y.Float64()
			e.hasPos = true
		}
		out = append(out, e)
	}
	return out
}

func (e existingCallout) hasPart(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range e.parts {
		if p == name {
			return true
		}
	}
	return false
}

// ─── the builder ───

// buildCalloutCandidates — every callout the card's data implies and the sheet lacks, minus the
// dismissed, ranked and capped. Pure: the card in, the list out.
func buildCalloutCandidates(card *entity.TechCard, dismissed map[string]bool) []calloutCandidate {
	if card == nil {
		return nil
	}
	existing := sheetCallouts(card)
	b := calloutBuilder{card: card, existing: existing, dismissed: dismissed}
	b.pieceNames()
	b.bomLines()
	b.garmentLabels()
	b.operations()
	b.section()
	b.details()
	sort.SliceStable(b.out, func(i, j int) bool {
		if b.out[i].rank != b.out[j].rank {
			return b.out[i].rank < b.out[j].rank
		}
		return b.out[i].order < b.out[j].order
	})
	if len(b.out) > calloutMaxCandidates {
		b.out = b.out[:calloutMaxCandidates]
	}
	return b.out
}

type calloutBuilder struct {
	card      *entity.TechCard
	existing  []existingCallout
	dismissed map[string]bool
	out       []calloutCandidate
	order     int

	pieceByKey map[string]string
	pieceByID  map[int]string
}

func (b *calloutBuilder) add(c calloutCandidate) {
	if b.dismissed[c.sourceID] {
		return
	}
	b.order++
	c.order = b.order
	c.description = aiBoundedText(designOneLine(c.description), calloutDescriptionMaxRunes)
	b.out = append(b.out, c)
}

func (b *calloutBuilder) pieceNames() {
	b.pieceByKey, b.pieceByID = map[string]string{}, map[int]string{}
	for _, p := range b.card.Pieces {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if p.LineKey != "" {
			b.pieceByKey[p.LineKey] = name
		}
		if p.Id > 0 {
			b.pieceByID[p.Id] = name
		}
	}
}

// piecesOfBomLine — the names of the pieces cut from (or fused with) a BOM line, card order, distinct.
func (b *calloutBuilder) piecesOfBomLine(line entity.TechCardBomItem) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range b.card.Pieces {
		for _, m := range p.Materials {
			hit := (line.LineKey != "" && (m.BomLineKey == line.LineKey || m.FusingBomLineKey == line.LineKey)) ||
				(line.Id > 0 && ((m.BomItemId.Valid && int(m.BomItemId.Int64) == line.Id) ||
					(m.FusingBomItemId.Valid && int(m.FusingBomItemId.Int64) == line.Id)))
			name := strings.TrimSpace(p.Name)
			if hit && name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// Closure and stress-point BOM kinds rank first (41 §4.3).
var calloutClosureKinds = map[entity.TechCardBomKind]bool{
	entity.BomKindZipper: true, entity.BomKindZipperSlider: true, entity.BomKindButton: true,
	entity.BomKindSnap: true, entity.BomKindRivet: true, entity.BomKindEyelet: true,
	entity.BomKindHookAndBar: true, entity.BomKindBuckle: true, entity.BomKindToggle: true,
	entity.BomKindMagnet: true, entity.BomKindHookLoop: true, entity.BomKindSnapHook: true,
}

// calloutArtworkKinds — decoration kinds → the artwork sub (purpose.ts ARTWORK_SUBS).
var calloutArtworkKinds = map[entity.TechCardBomKind]string{
	entity.BomKindPrint: "print", entity.BomKindHeatTransfer: "print", entity.BomKindFoil: "print",
	entity.BomKindEmbroidery: "embroidery",
	entity.BomKindApplique:   "branding", entity.BomKindPatch: "branding", entity.BomKindLaser: "branding",
	entity.BomKindRhinestone: "branding",
}

func calloutWords(token string) string {
	return strings.TrimSpace(strings.ReplaceAll(token, "_", " "))
}

func (b *calloutBuilder) bomLines() {
	// Artwork lines covered by an artwork callout of the same sub: first by name, then the remaining
	// callouts of that sub cover the remaining lines in card order (the spec has no lineKey for artwork).
	artworkLeft := map[string]int{}
	for _, e := range b.existing {
		if e.spec.T == calloutPurposeArtwork && e.spec.Sub != "label" {
			artworkLeft[e.spec.Sub]++
		}
	}
	type artLine struct {
		line entity.TechCardBomItem
		sub  string
	}
	var arts []artLine
	usedByName := map[int]bool{}
	for _, line := range b.card.BomItems {
		kind := entity.TechCardBomKind(line.Kind.String)
		if sub, ok := calloutArtworkKinds[kind]; ok && line.Section == entity.BomSectionDecoration {
			arts = append(arts, artLine{line, sub})
		}
	}
	coveredArt := map[string]bool{}
	for i, a := range arts {
		name := strings.ToLower(strings.TrimSpace(a.line.Name))
		for j, e := range b.existing {
			if usedByName[j] || e.spec.T != calloutPurposeArtwork || e.spec.Sub != a.sub || name == "" {
				continue
			}
			if strings.Contains(e.description, name) {
				usedByName[j] = true
				coveredArt[a.line.LineKey+"#"+strconv.Itoa(i)] = true
				artworkLeft[a.sub]--
				break
			}
		}
	}
	for i, a := range arts {
		k := a.line.LineKey + "#" + strconv.Itoa(i)
		if coveredArt[k] {
			continue
		}
		if artworkLeft[a.sub] > 0 {
			artworkLeft[a.sub]--
			coveredArt[k] = true
		}
	}

	materialCovered := map[string]bool{}
	for _, e := range b.existing {
		if e.spec.T == calloutPurposeMaterial && e.spec.LineKey != "" {
			materialCovered[e.spec.LineKey] = true
		}
	}

	artIdx := 0
	for _, line := range b.card.BomItems {
		name := strings.TrimSpace(line.Name)
		kind := entity.TechCardBomKind(line.Kind.String)
		src := "bom:" + line.LineKey
		if line.LineKey == "" || name == "" {
			if _, ok := calloutArtworkKinds[kind]; ok && line.Section == entity.BomSectionDecoration {
				artIdx++
			}
			continue
		}
		label := "BOM · " + name
		if sub, ok := calloutArtworkKinds[kind]; ok && line.Section == entity.BomSectionDecoration {
			covered := coveredArt[line.LineKey+"#"+strconv.Itoa(artIdx)]
			artIdx++
			if covered {
				continue
			}
			method := calloutWords(string(kind))
			for _, op := range b.card.Operations {
				if op.PrintMethod.Valid && op.PrintMethod.String != "" && containsString(op.BomLineKeys, line.LineKey) {
					method = calloutWords(op.PrintMethod.String)
					break
				}
			}
			b.add(calloutCandidate{
				sourceID: src, sourceLabel: label, purpose: calloutPurposeArtwork, rank: calloutRankArtwork,
				spec:        renderCalloutSpec(map[string]any{"t": calloutPurposeArtwork, "sub": sub, "method": method}),
				description: name, parts: b.piecesOfBomLine(line), missing: []string{"size", "placement"},
				facts: sub + " · " + method + " · " + name,
			})
			continue
		}
		if materialCovered[line.LineKey] {
			continue
		}
		rank := -1
		switch line.Section {
		case entity.BomSectionHardware, entity.BomSectionTrim:
			if kind == entity.BomKindSeamSealingTape {
				continue // a process consumable, not a thing the factory looks for on the flat
			}
			rank = calloutRankTrim
			if calloutClosureKinds[kind] {
				rank = calloutRankClosure
			}
		case entity.BomSectionFabric:
			p := entity.TechCardBomPurpose(line.Purpose.String)
			if !line.Purpose.Valid || p == entity.BomPurposeMain || p == entity.BomPurposeContrast {
				rank = calloutRankFabric
			}
		}
		if rank < 0 {
			continue
		}
		desc := name
		if c := strings.TrimSpace(line.Color.String); c != "" {
			desc += " · " + c
		}
		facts := calloutWords(string(line.Section))
		if k := calloutWords(string(kind)); k != "" {
			facts += " · " + k
		}
		if p := calloutWords(line.Purpose.String); p != "" {
			facts += " · " + p
		}
		parts := b.piecesOfBomLine(line)
		if len(parts) > 0 {
			facts += " · cut as " + strings.Join(parts, ", ")
		}
		b.add(calloutCandidate{
			sourceID: src, sourceLabel: label, purpose: calloutPurposeMaterial, rank: rank,
			spec:        renderCalloutSpec(map[string]any{"t": calloutPurposeMaterial, "lineKey": line.LineKey, "name": name}),
			description: desc, parts: parts, facts: facts + " · " + desc,
		})
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

var calloutSizeRe = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(?:mm)?\s*[x×*]\s*(\d+(?:[.,]\d+)?)`)

// calloutLabelSize — "40x20", "40 × 20 mm" → ("40", "20"); anything else → "", "".
func calloutLabelSize(s string) (string, string) {
	m := calloutSizeRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return "", ""
	}
	return strings.ReplaceAll(m[1], ",", "."), strings.ReplaceAll(m[2], ",", ".")
}

func (b *calloutBuilder) garmentLabels() {
	left := 0
	used := map[int]bool{}
	for _, e := range b.existing {
		if e.spec.T == calloutPurposeArtwork && e.spec.Sub == "label" {
			left++
		}
	}
	covered := make([]bool, len(b.card.GarmentLabels))
	for i, l := range b.card.GarmentLabels {
		key := strings.ToLower(calloutWords(l.Key))
		placement := strings.ToLower(strings.TrimSpace(l.Placement.String))
		for j, e := range b.existing {
			if used[j] || e.spec.T != calloutPurposeArtwork || e.spec.Sub != "label" {
				continue
			}
			if (placement != "" && strings.EqualFold(strings.TrimSpace(e.spec.From), placement)) ||
				(key != "" && strings.Contains(e.description, key)) {
				used[j], covered[i] = true, true
				left--
				break
			}
		}
	}
	for i := range b.card.GarmentLabels {
		if !covered[i] && left > 0 {
			covered[i] = true
			left--
		}
	}
	for i, l := range b.card.GarmentLabels {
		key := strings.TrimSpace(l.Key)
		if key == "" || covered[i] {
			continue
		}
		words := calloutWords(key)
		name := words
		if !strings.Contains(strings.ToLower(words), "label") && !strings.Contains(strings.ToLower(words), "tag") {
			name += " label"
		}
		desc := name
		if a := strings.TrimSpace(l.Attachment.String); a != "" {
			desc += " · " + a
		}
		placement := strings.TrimSpace(l.Placement.String)
		w, h := calloutLabelSize(l.Size.String)
		var missing []string
		if w == "" {
			missing = append(missing, "size")
		}
		if placement == "" {
			missing = append(missing, "placement")
		}
		view := "back" // a label sits at the CB neck unless its placement says otherwise (41 §2)
		if p := strings.ToLower(placement); p != "" && !strings.Contains(p, "back") && !strings.Contains(p, "neck") &&
			!strings.Contains(p, "cb") && (strings.Contains(p, "front") || strings.Contains(p, "chest") ||
			strings.Contains(p, "hem") || strings.Contains(p, "side")) {
			view = "front"
		}
		facts := name
		if placement != "" {
			facts += " · at " + placement
		}
		if s := strings.TrimSpace(l.Size.String); s != "" {
			facts += " · " + s
		}
		b.add(calloutCandidate{
			sourceID: "label:" + key, sourceLabel: "label · " + words, purpose: calloutPurposeArtwork,
			rank: calloutRankTrim, view: view,
			spec: renderCalloutSpec(map[string]any{"t": calloutPurposeArtwork, "sub": "label",
				"from": placement, "w": w, "h": h}),
			description: desc, missing: missing, facts: facts,
		})
	}
}

// Machine types that make no seam the flat should carry a stitch callout for.
var calloutNoSeamMachines = map[string]bool{"seam_taping": true, "ultrasonic_welder": true, "embroidery": true}

// Closure / stress-point machines: their stitch callout ranks with the closures.
var calloutClosureMachines = map[string]bool{"buttonhole": true, "bartack": true, "button_attach": true, "zipper_setting": true}

func decString(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
}

type stitchGroup struct {
	cand    calloutCandidate
	members []string // source ids
	covered bool
}

// opStitch renders ONE machine step as a stitch callout (spec, description, parts, missing, facts).
// ok=false: the step is not a sewing step the flat carries.
func (b *calloutBuilder) opStitch(i int, op entity.TechCardOperation) (calloutCandidate, bool) {
	if op.OperationType != entity.OpTypeMachine {
		return calloutCandidate{}, false
	}
	machine := strings.TrimSpace(op.MachineType.String)
	if calloutNoSeamMachines[machine] {
		return calloutCandidate{}, false
	}
	num := int(op.OperationNumber.Int32)
	if !op.OperationNumber.Valid || num <= 0 {
		num = (i + 1) * 10
	}
	what := calloutWords(op.Work.String)
	if what == "" {
		what = calloutWords(machine)
	}
	if what == "" {
		what = "stitch"
	}
	var parts []string
	seen := map[string]bool{}
	addPart := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			parts = append(parts, n)
		}
	}
	for _, k := range op.PieceLineKeys {
		addPart(b.pieceByKey[k])
	}
	for _, id := range op.PieceIds {
		addPart(b.pieceByID[id])
	}

	c := calloutCandidate{
		sourceID: "op:" + strconv.Itoa(num), sourceLabel: "op " + strconv.Itoa(num) + " · " + what,
		purpose: calloutPurposeStitch, parts: parts,
	}
	if calloutClosureMachines[machine] {
		method := calloutWords(machine)
		switch machine {
		case "bartack":
			if v := decString(op.BartackLengthMm); v != "" {
				method += " " + v + " mm"
			}
		case "buttonhole":
			if v := calloutWords(op.ButtonholeStyle.String); v != "" {
				method += " " + v
			}
			if v := decString(op.CutLengthMm); v != "" {
				method += " " + v + " mm"
			}
		case "button_attach":
			if v := calloutWords(op.AttachPattern.String); v != "" {
				method += " " + v
			}
		case "zipper_setting":
			if v := calloutWords(op.ZipperApplication.String); v != "" {
				method = v + " zip"
			}
		}
		c.rank = calloutRankClosure
		c.spec = renderCalloutSpec(map[string]any{"t": calloutPurposeStitch, "method": method})
		c.description = what
		c.facts = method
		return c, true
	}

	cons := b.card.Construction
	seam := strings.TrimSpace(op.SeamClass.String)
	if seam == "" && cons != nil {
		seam = strings.TrimSpace(cons.DefaultSeamClass.String)
	}
	seamToken := ""
	if seam != "" && seam != "other" {
		seamToken = "TECH_CARD_SEAM_CLASS_" + strings.ToUpper(seam)
	}
	stcm := decString(op.StitchesPerCm)
	if stcm == "" && cons != nil {
		stcm = decString(cons.DefaultStitchesPerCm)
	}
	allowance := decString(op.SeamAllowanceMm)
	if allowance == "" {
		allowance = decString(entity.RequiredSeamAllowanceMm(b.card.RequiredSeamAllowanceMm, b.card.WorkshopSeamAllowanceMm))
	}
	iso := iso4915StitchType(machine, int(op.ThreadCount.Int32))
	method := ""
	if mode := calloutWords(op.TopstitchMode.String); mode != "" {
		method = "topstitch " + mode
		if v := decString(op.TopstitchWidthMm); v != "" {
			method += " " + v + " mm"
		}
		if op.TopstitchRows.Valid && op.TopstitchRows.Int32 > 1 {
			method += " ×" + strconv.Itoa(int(op.TopstitchRows.Int32))
		}
	}
	var missing []string
	if iso == "" && seamToken == "" {
		missing = append(missing, "stitch")
	}
	if stcm == "" {
		missing = append(missing, "spi")
	}
	if allowance == "" {
		missing = append(missing, "allowance")
	}
	c.rank = calloutRankSeam
	c.missing = missing
	c.spec = renderCalloutSpec(map[string]any{"t": calloutPurposeStitch, "iso": iso, "seam": seamToken,
		"stcm": stcm, "allowance": allowance, "method": method})
	c.description = what
	facts := []string{what}
	if iso != "" {
		facts = append(facts, iso)
	}
	if seam != "" {
		facts = append(facts, "seam "+calloutWords(seam))
	}
	if method != "" {
		facts = append(facts, method)
	}
	if len(parts) > 0 {
		facts = append(facts, "joins "+strings.Join(parts, ", "))
	}
	c.facts = strings.Join(facts, " · ")
	return c, true
}

// operations — one stitch candidate per DISTINCT stitch spec (41 §4.3: one per distinct spec). Steps
// sharing a spec form a group led by the first; the group is covered when ANY member's callout_number
// points at a sheet callout, and dismissed when any member's source id was dismissed — so the group's
// fate does not move between runs.
func (b *calloutBuilder) operations() {
	numbers := map[int]bool{}
	for _, e := range b.existing {
		if e.number > 0 {
			numbers[e.number] = true
		}
	}
	var groups []*stitchGroup
	bySpec := map[string]*stitchGroup{}
	for i, op := range b.card.Operations {
		c, ok := b.opStitch(i, op)
		if !ok {
			continue
		}
		covered := op.CalloutNumber.Valid && numbers[int(op.CalloutNumber.Int32)]
		key := c.spec
		if c.rank == calloutRankClosure {
			key = c.sourceID // a closure step is its own place on the garment, never merged
		}
		g, ok := bySpec[key]
		if !ok {
			g = &stitchGroup{cand: c}
			bySpec[key] = g
			groups = append(groups, g)
		} else {
			for _, p := range c.parts {
				if !containsString(g.cand.parts, p) {
					g.cand.parts = append(g.cand.parts, p)
				}
			}
		}
		g.members = append(g.members, c.sourceID)
		g.covered = g.covered || covered
	}
	for _, g := range groups {
		if g.covered {
			continue
		}
		dismissed := false
		for _, m := range g.members {
			dismissed = dismissed || b.dismissed[m]
		}
		if dismissed {
			continue
		}
		b.add(g.cand)
	}
}

// section — ONE cut through the card's layers when it has more than the shell: fused pieces or an
// interlining line, padding, lining, binding/piping (40-SUGGEST §2).
func (b *calloutBuilder) section() {
	have := map[string]bool{}
	var fused []string
	for _, p := range b.card.Pieces {
		if p.Fused {
			have["interlining"] = true
			if n := strings.TrimSpace(p.Name); n != "" && !containsString(fused, n) {
				fused = append(fused, n)
			}
		}
	}
	for _, line := range b.card.BomItems {
		p := entity.TechCardBomPurpose(line.Purpose.String)
		switch {
		case line.Section == entity.BomSectionInterlining || p == entity.BomPurposeInterfacing:
			have["interlining"] = true
		case line.Section == entity.BomSectionInsulation || p == entity.BomPurposeInsulation:
			have["padding"] = true
		case line.Section == entity.BomSectionLining || p == entity.BomPurposeLining:
			have["lining"] = true
		}
		if k := entity.TechCardBomKind(line.Kind.String); k == entity.BomKindBinding || k == entity.BomKindPiping {
			have["binding"] = true
		}
	}
	layers := []string{"shell"}
	for _, l := range []string{"interlining", "lining", "binding", "padding"} { // purpose.ts SECTION_PRESETS order
		if have[l] {
			layers = append(layers, l)
		}
	}
	if len(layers) < 2 {
		return
	}
	for _, e := range b.existing {
		if e.spec.T != calloutPurposeSection {
			continue
		}
		names := map[string]bool{}
		for _, l := range e.spec.Layers {
			names[strings.ToLower(strings.TrimSpace(l.Name))] = true
		}
		all := true
		for _, l := range layers[1:] {
			all = all && names[l]
		}
		if all {
			return
		}
	}
	ls := make([]map[string]string, 0, len(layers))
	for _, l := range layers {
		ls = append(ls, map[string]string{"name": l})
	}
	desc := ""
	if len(fused) > 0 {
		desc = "fused: " + strings.Join(fused, ", ")
	}
	b.add(calloutCandidate{
		sourceID: "section:card", sourceLabel: "card · layers", purpose: calloutPurposeSection, rank: calloutRankFabric,
		spec: renderCalloutSpec(map[string]any{"t": calloutPurposeSection, "layers": ls}), description: desc, parts: fused,
		facts: "layers " + strings.Join(layers, " / ") + func() string {
			if desc != "" {
				return " · " + desc
			}
			return ""
		}(),
	})
}

// Aspects that restate the silhouette or the fit — noise on a construction flat (41 §2).
var calloutSkipAspects = map[string]bool{"silhouette": true, "fit": true, "overall": true, "length": true, "volume": true}

// details — the card's details aspects, then the STUDIO quiz decisions about a part (fresh, answered).
func (b *calloutBuilder) details() {
	covered := func(name, desc string) bool {
		d := strings.ToLower(designOneLine(desc))
		for _, e := range b.existing {
			if e.spec.T != calloutPurposeDetail {
				continue
			}
			if e.hasPart(name) || (d != "" && e.description == d) {
				return true
			}
		}
		return false
	}
	aspects := map[string]bool{}
	for _, d := range b.card.Details {
		key := strings.TrimSpace(d.Key.String)
		text := strings.TrimSpace(d.Text.String)
		if key == "" || text == "" {
			continue
		}
		lk := strings.ToLower(key)
		aspects[lk] = true
		if calloutSkipAspects[lk] || covered(key, text) {
			continue
		}
		b.add(calloutCandidate{
			sourceID: "detail:" + key, sourceLabel: "detail · " + calloutWords(key), purpose: calloutPurposeDetail,
			rank: calloutRankDetail, spec: calloutSpecDefault(calloutPurposeDetail, ""),
			description: text, parts: []string{key},
			facts: calloutWords(key) + ": " + aiBoundedText(designOneLine(text), 160),
		})
	}
	for _, a := range b.card.QuizAnswers {
		q := a.Question
		if a.Skipped || a.Stale || q.ID == "" || q.Part == "" || q.Part == entity.DesignQuizPartWhole {
			continue
		}
		ans := designQuizAnswerText(a)
		if ans == "" {
			continue
		}
		label := designQuizPartLabel(q.Part)
		if label == "" || aspects[strings.ToLower(label)] || aspects[strings.ToLower(q.Part)] {
			continue // the card's own aspect of that part already speaks for it
		}
		desc := label + " — " + ans
		if covered(label, desc) {
			continue
		}
		view := q.View
		if view != "front" && view != "back" {
			view = ""
		}
		b.add(calloutCandidate{
			sourceID: "quiz:" + q.ID, sourceLabel: "quiz · " + label, purpose: calloutPurposeDetail,
			rank: calloutRankDetail, spec: calloutSpecDefault(calloutPurposeDetail, ""), view: view,
			description: desc, parts: []string{label},
			facts: label + ": " + aiBoundedText(designOneLine(q.Question), 100) + " → " + ans,
		})
	}
}
