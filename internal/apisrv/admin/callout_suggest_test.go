package admin

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── fixtures ───

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nd(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

func ni(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

const (
	csFront = 812
	csBack  = 813
)

// csCard — a jacket with one of every source the builder reads.
func csCard() *entity.TechCard {
	c := &entity.TechCard{Id: 7, LockVersion: 3, UpdatedAt: time.Unix(1700000000, 0)}
	c.Name = "Field jacket"
	c.RequiredSeamAllowanceMm = nd("10")
	c.Construction = &entity.TechCardConstruction{DefaultStitchesPerCm: nd("4")}
	c.Media = []entity.TechCardMediaItem{
		{MediaId: csFront, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
		{MediaId: csBack, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaBack},
		{MediaId: 900, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard},
	}
	for _, m := range c.Media {
		c.ResolvedMedia = append(c.ResolvedMedia, entity.TechCardMediaFull{
			Media:    entity.MediaFull{Id: m.MediaId, MediaItem: entity.MediaItem{FullSizeMediaURL: fmt.Sprintf("https://cdn.test/%d.png", m.MediaId), CompressedMediaURL: fmt.Sprintf("https://cdn.test/%d-c.webp", m.MediaId)}},
			Category: m.Category, Kind: m.Kind,
		})
	}
	c.BomItems = []entity.TechCardBomItem{
		{Id: 1, LineKey: "L1", Section: entity.BomSectionFabric, Purpose: ns("main"), Name: "Cotton twill"},
		{Id: 2, LineKey: "L2", Section: entity.BomSectionHardware, Kind: ns("zipper"), Name: "YKK zip #5", Color: ns("black")},
		{Id: 3, LineKey: "L3", Section: entity.BomSectionHardware, Kind: ns("button"), Name: "Horn button 20L"},
		{Id: 4, LineKey: "L4", Section: entity.BomSectionTrim, Kind: ns("elastic"), Name: "Elastic 25 mm"},
		{Id: 5, LineKey: "L5", Section: entity.BomSectionDecoration, Kind: ns("print"), Name: "Back logo print"},
		{Id: 6, LineKey: "L6", Section: entity.BomSectionThread, Kind: ns("sewing_thread"), Name: "Gütermann 120"},
		{Id: 7, LineKey: "L7", Section: entity.BomSectionLining, Purpose: ns("lining"), Name: "Cupro lining"},
	}
	c.Pieces = []entity.TechCardPiece{
		{Id: 11, Name: "front", LineKey: "P1", Materials: []entity.TechCardPieceMaterial{{BomLineKey: "L1"}}},
		{Id: 12, Name: "back", LineKey: "P2", Materials: []entity.TechCardPieceMaterial{{BomItemId: sql.NullInt64{Int64: 1, Valid: true}}}},
		{Id: 13, Name: "collar", LineKey: "P3", Fused: true},
	}
	c.Operations = []entity.TechCardOperation{
		{OperationNumber: ni(10), OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"), Work: ns("side_seam"),
			SeamClass: ns("ss_plain"), PieceLineKeys: []string{"P1", "P2"}},
		{OperationNumber: ni(20), OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"), Work: ns("shoulder_seam"),
			SeamClass: ns("ss_plain"), PieceIds: []int{13}}, // same spec as op 10 → merged
		{OperationType: entity.OpTypeMachine, MachineType: ns("overlock"), ThreadCount: ni(4)}, // no number → 30
		{OperationNumber: ni(40), OperationType: entity.OpTypeMachine, MachineType: ns("bartack"), BartackLengthMm: nd("12")},
		{OperationNumber: ni(50), OperationType: entity.OpTypePress},
		{OperationNumber: ni(60), OperationType: entity.OpTypeMachine, MachineType: ns("seam_taping")},
	}
	c.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: "brand", Placement: ns("CB neck"), Size: ns("40 x 20 mm"), Attachment: ns("sewn 4 sides")},
		{Key: "care"},
	}
	c.Details = []entity.TechCardDetail{
		{Key: ns("collar"), Text: ns("two-piece collar, 4 cm stand"), MediaIds: []int{901}},
		{Key: ns("silhouette"), Text: ns("boxy, hip length"), MediaIds: []int{902}},
		{Key: ns("pocket"), Text: ns("a long paragraph about the pockets")}, // no photo: not a candidate
	}
	c.QuizAnswers = []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "pocket_kind", Category: "details", Part: "pocket", View: "front", Question: "Which pockets?"},
			Selected: []string{"patch pocket with flap"}},
		{Question: entity.DesignQuizQuestion{ID: "collar_q", Category: "details", Part: "collar", Question: "Collar?"},
			Selected: []string{"stand"}}, // the card's own collar aspect speaks for it
		{Question: entity.DesignQuizQuestion{ID: "season", Category: "use", Part: "whole", Question: "Season?"},
			Selected: []string{"autumn"}},
		{Question: entity.DesignQuizQuestion{ID: "hood", Part: "hood", Question: "Hood?"}, Skipped: true},
	}
	return c
}

func csIDs(cs []calloutCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.sourceID)
	}
	return out
}

func csFind(cs []calloutCandidate, id string) (calloutCandidate, bool) {
	for _, c := range cs {
		if c.sourceID == id {
			return c, true
		}
	}
	return calloutCandidate{}, false
}

// ─── ISO 4915 ───

// The server table mirrors the client's equipment-options.ts ISO4915_FIXED / ISO4915_BY_THREADS.
func TestISO4915StitchTypeMirrorsTheClient(t *testing.T) {
	for _, tc := range []struct {
		machine string
		threads int
		want    string
	}{
		{"lockstitch", 0, "301"}, {"lockstitch_double_needle", 2, "301"}, {"chainstitch", 0, "401"},
		{"blindstitch", 0, "103"}, {"zigzag", 0, "304"},
		{"overlock", 3, "504"}, {"overlock", 4, "514"}, {"overlock", 5, "516"},
		{"overlock", 0, ""}, {"overlock", 2, ""}, // the thread count decides; unnamed = no guess
		{"coverstitch", 0, ""}, {"seam_taping", 0, ""}, {"ultrasonic_welder", 0, ""}, {"", 0, ""},
	} {
		require.Equal(t, tc.want, iso4915StitchType(tc.machine, tc.threads), "%s/%d", tc.machine, tc.threads)
	}
}

// ─── the candidate builder ───

func TestCalloutCandidatesSourcesSpecsAndRanking(t *testing.T) {
	cs := buildCalloutCandidates(csCard(), nil)
	require.Equal(t, []string{
		// closures & stress points
		"bom:L2", "bom:L3", "op:40",
		// trims & labels
		"bom:L4", "label:brand", "label:care",
		// artwork
		"bom:L5",
		// distinct seam specs (op 20 merged into op 10)
		"op:10", "op:30",
		// fabric zones
		"bom:L1", "section:card",
		// details
		"detail:collar", "quiz:pocket_kind",
	}, csIDs(cs))

	zip, _ := csFind(cs, "bom:L2")
	require.Equal(t, `{"lineKey":"L2","name":"YKK zip #5","t":"material"}`, zip.spec)
	require.Equal(t, "YKK zip #5 · black", zip.description)
	require.Equal(t, "BOM · YKK zip #5", zip.sourceLabel)

	fabric, _ := csFind(cs, "bom:L1")
	require.Equal(t, []string{"front", "back"}, fabric.parts, "pieces cut from the line, by key and by id")

	seam, _ := csFind(cs, "op:10")
	require.Equal(t, `{"allowance":"10","iso":"301","seam":"TECH_CARD_SEAM_CLASS_SS_PLAIN","stcm":"4","t":"stitch"}`, seam.spec,
		"st/cm and allowance inherit from the card when the step leaves them unset")
	require.Equal(t, []string{"front", "back", "collar"}, seam.parts, "merged steps pool their pieces")
	require.Empty(t, seam.missing)
	require.Equal(t, "op 10 · side seam", seam.sourceLabel)

	over, _ := csFind(cs, "op:30")
	require.Equal(t, `{"allowance":"10","iso":"514","stcm":"4","t":"stitch"}`, over.spec)

	bartack, _ := csFind(cs, "op:40")
	require.Equal(t, `{"method":"bartack 12 mm","t":"stitch"}`, bartack.spec)

	brand, _ := csFind(cs, "label:brand")
	require.Equal(t, `{"from":"CB neck","h":"20","sub":"label","t":"artwork","w":"40"}`, brand.spec)
	require.Equal(t, "back", brand.view)
	require.Empty(t, brand.missing)
	care, _ := csFind(cs, "label:care")
	require.Equal(t, []string{"size", "placement"}, care.missing)

	print, _ := csFind(cs, "bom:L5")
	require.Equal(t, `{"method":"print","sub":"print","t":"artwork"}`, print.spec)

	section, _ := csFind(cs, "section:card")
	require.Equal(t, `{"layers":[{"name":"shell"},{"name":"interlining"},{"name":"lining"}],"t":"section"}`, section.spec)
	require.Equal(t, []string{"collar"}, section.parts)

	detail, _ := csFind(cs, "detail:collar")
	require.Equal(t, `{"scale":2,"t":"detail"}`, detail.spec)
	quiz, _ := csFind(cs, "quiz:pocket_kind")
	require.Equal(t, "pocket — patch pocket with flap", quiz.description)
	require.Equal(t, "front", quiz.view)
}

func TestCalloutCandidatesStitchMissingData(t *testing.T) {
	c := csCard()
	c.Construction = nil
	c.RequiredSeamAllowanceMm = decimal.NullDecimal{}
	c.Operations = []entity.TechCardOperation{{OperationNumber: ni(10), OperationType: entity.OpTypeMachine, MachineType: ns("coverstitch")}}
	cs := buildCalloutCandidates(c, nil)
	op, ok := csFind(cs, "op:10")
	require.True(t, ok)
	require.Equal(t, []string{"stitch", "spi", "allowance"}, op.missing)
	require.Equal(t, `{"t":"stitch"}`, op.spec)
}

// Covered sources are not suggested again: material by lineKey, a step whose callout_number points
// at a sheet callout (the whole merged group with it), a label by placement, a section whose layers
// include ours, a detail by part. A MOODBOARD callout covers nothing on the sheet.
func TestCalloutCandidatesCoverage(t *testing.T) {
	c := csCard()
	c.Operations[1].CalloutNumber = ni(5) // op 20 is linked → the op 10/20 group is covered
	c.Callouts = []entity.TechCardCallout{
		{Number: 1, MediaId: ni(csFront), Spec: ns(`{"lineKey":"L2","name":"zip","t":"material"}`)},
		{Number: 5, MediaId: ni(csFront), Spec: ns(`{"t":"stitch"}`)},
		{Number: 6, MediaId: ni(csBack), Spec: ns(`{"from":"cb neck","sub":"label","t":"artwork"}`)},
		{Number: 7, MediaId: ni(csFront), Spec: ns(`{"layers":[{"name":"Shell"},{"name":"interlining"},{"name":"lining"}],"t":"section"}`)},
		{Number: 8, MediaId: ni(csFront), Spec: ns(`{"scale":2,"t":"detail"}`), Parts: []string{"Collar"}},
		{Number: 9, MediaId: ni(csFront), Spec: ns(`{"sub":"print","t":"artwork"}`)},
		{Number: 1, MediaId: ni(900), Spec: ns(`{"lineKey":"L3","t":"material"}`)}, // moodboard
	}
	ids := csIDs(buildCalloutCandidates(c, nil))
	for _, gone := range []string{"bom:L2", "op:10", "op:20", "label:brand", "section:card", "detail:collar", "bom:L5"} {
		require.NotContains(t, ids, gone)
	}
	for _, kept := range []string{"bom:L3", "op:30", "label:care", "bom:L1", "quiz:pocket_kind"} {
		require.Contains(t, ids, kept)
	}
}

// Dismissed source ids never come back — and dismissing any member of a merged stitch group drops
// the group, so its fate does not depend on which step happens to lead it.
func TestCalloutCandidatesDismissed(t *testing.T) {
	ids := csIDs(buildCalloutCandidates(csCard(), map[string]bool{"bom:L3": true, "op:20": true, "quiz:pocket_kind": true}))
	require.NotContains(t, ids, "bom:L3")
	require.NotContains(t, ids, "op:10")
	require.NotContains(t, ids, "quiz:pocket_kind")
	require.Contains(t, ids, "bom:L2")
}

func TestCalloutCandidatesCap(t *testing.T) {
	c := csCard()
	for i := 0; i < 60; i++ {
		c.BomItems = append(c.BomItems, entity.TechCardBomItem{LineKey: fmt.Sprintf("H%d", i),
			Section: entity.BomSectionHardware, Kind: ns("snap"), Name: fmt.Sprintf("snap %d", i)})
	}
	cs := buildCalloutCandidates(c, nil)
	require.Len(t, cs, calloutMaxCandidates)
	require.Equal(t, "bom:L2", cs[0].sourceID, "the cap cuts the tail, not the head")
}

// ─── per view: validate, merge ───

func csFrontFlat() calloutFlat {
	return calloutFlat{mediaID: csFront, kind: "front", view: "front", url: "u1"}
}
func csBackFlat() calloutFlat {
	return calloutFlat{mediaID: csBack, kind: "back", view: "back", url: "u2"}
}

func csAnswer(t *testing.T, raw string) calloutAnswer {
	t.Helper()
	a, ok := parseCalloutAnswer(raw)
	require.True(t, ok, raw)
	return a
}

func csWhere(out []*pb_admin.CalloutSuggestion) []string {
	var got []string
	for _, s := range out {
		got = append(got, s.GetSourceId()+"@"+fmt.Sprint(s.GetMediaId()))
	}
	return got
}

func TestValidateCalloutViewDropsWhatTheModelGotWrong(t *testing.T) {
	cands := buildCalloutCandidates(csCard(), nil) // c1 = bom:L2 (point), c5 = label:brand (box), c11 = section (line)
	require.Equal(t, "label:brand", cands[4].sourceID)
	require.Equal(t, "section:card", cands[10].sourceID)
	raw := `{"placements":[
	  {"id":"c1","media_id":813,"points":[[0.5,0.4]],"label":[0.1,0.4],"text":"IGNORE ME","spec":"{\"t\":\"note\"}","description":"model words"},
	  {"id":"c1","points":[[0.5,0.4]]},
	  {"id":"c99","points":[[0.5,0.5]]},
	  {"id":"c3","points":[[1.2,0.5]]},
	  {"id":"c4","points":[[0.2,0.2],[0.3,0.3]]},
	  {"id":"c5","points":[[0.45,0.1],[0.55,0.16]],"confidence":"0.7"},
	  {"id":"c6","points":[[0.1,0.1],[0.2,0.2],[0.3,0.3]]},
	  {"id":"c7","skip":true},
	  {"id":"c11","points":[[0.3,0.3]]},
	  {"id":"c8","points":[{"x":0.62,"y":0.5}]}
	],"own":[
	  {"purpose":"detail","points":[[0.3,0.4],[0.45,0.55]],"label":[0.9,0.45],"text":"double welt pocket"},
	  {"purpose":"note","points":[[0.3,0.4]],"text":"no notes"},
	  {"purpose":"stitch","points":[[0.3,0.4]],"text":"   "}
	]}`
	out, st := mergeCalloutViews([]calloutViewAnswer{{flat: csFrontFlat(), ans: csAnswer(t, raw)}}, cands)
	require.Equal(t, []string{"bom:L2@812", "label:brand@812", "op:10@812", "pic:1@812"}, csWhere(out),
		"the view is the call's: a media_id in the answer moves nothing")
	require.Equal(t, 1, st.skipped)

	zip := out[0]
	require.Equal(t, cands[0].spec, zip.GetSpec(), "the spec is the row's, never the model's")
	require.Equal(t, "YKK zip #5 · black", zip.GetDescription(), "the description is the row's, never the model's")
	require.Equal(t, "YKK zip #5", zip.GetLabel(), "the plate line is the row's")
	require.True(t, zip.GetFromData())
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL, zip.GetKind())
	require.Len(t, zip.GetPoints(), 1)
	require.Equal(t, "0.3", zip.GetPosX().GetValue(), "the model's plate position is ignored: the fallback sits beside the anchor")

	label := out[1]
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON, label.GetKind())
	var corners []string
	for _, p := range label.GetPoints() {
		corners = append(corners, p.GetX().GetValue()+","+p.GetY().GetValue())
	}
	require.Equal(t, []string{"0.45,0.1", "0.55,0.1", "0.55,0.16", "0.45,0.16"}, corners, "two corners → a TL TR BR BL rect")
	require.Equal(t, "0.25", label.GetPosX().GetValue())

	own := out[3]
	require.False(t, own.GetFromData())
	require.Equal(t, "from picture", own.GetSourceLabel())
	require.Equal(t, `{"scale":2,"t":"detail"}`, own.GetSpec())
	require.Equal(t, "double welt pocket", own.GetDescription())
	require.Equal(t, "double welt pocket", own.GetLabel())
	require.Empty(t, own.GetMissing())

	ids := map[string]bool{}
	for _, s := range out {
		require.False(t, ids[s.GetId()], "ids are unique")
		ids[s.GetId()] = true
	}
}

// A candidate placed on several views keeps ONE: its usual view beats the model's confidence, the
// confidence beats the order, and on a tie front goes before back whatever the request order.
func TestMergeCalloutViewsKeepsOnePlacePerCandidate(t *testing.T) {
	cands := buildCalloutCandidates(csCard(), nil) // c1 bom:L2 zip, c2 bom:L3 button, c5 label:brand (usually back)
	require.Equal(t, "back", cands[4].view)
	front := `{"placements":[{"id":"c1","points":[[0.5,0.3]],"confidence":0.3},{"id":"c2","points":[[0.5,0.5]],"confidence":0.5},` +
		`{"id":"c5","points":[[0.45,0.1],[0.55,0.16]],"confidence":0.9}]}`
	back := `{"placements":[{"id":"c1","points":[[0.5,0.3]],"confidence":0.8},{"id":"c2","points":[[0.5,0.5]],"confidence":0.5},` +
		`{"id":"c5","points":[[0.45,0.05],[0.55,0.1]],"confidence":0.4}]}`
	out, st := mergeCalloutViews([]calloutViewAnswer{
		{flat: csBackFlat(), ans: csAnswer(t, back)}, // the back first in the request: the tie rule must still pick front
		{flat: csFrontFlat(), ans: csAnswer(t, front)},
	}, cands)
	require.Equal(t, []string{"bom:L2@813", "label:brand@813", "bom:L3@812"}, csWhere(out))
	require.Equal(t, 3, st.merged)
	require.Equal(t, "0.05", out[1].GetPoints()[0].GetY().GetValue(), "the kept place is the winning view's points")
}

// The plate line per source: rendered from the row, ≤ 32 runes, one line.
func TestCalloutLabelsPerSource(t *testing.T) {
	c := csCard()
	c.BomItems = append(c.BomItems, entity.TechCardBomItem{LineKey: "L9", Section: entity.BomSectionTrim, Kind: ns("drawcord"),
		Name: "Flat cotton drawcord with metal aglets 120 cm"})
	cs := buildCalloutCandidates(c, nil)
	for id, want := range map[string]string{
		"bom:L1":           "Cotton twill",
		"bom:L2":           "YKK zip #5",
		"bom:L5":           "Back logo print",
		"bom:L9":           "Flat cotton drawcord with metal…",
		"op:10":            "301 · side seam",
		"op:30":            "514 · overlock",
		"op:40":            "bartack 12 mm",
		"label:brand":      "brand label",
		"label:care":       "care label",
		"section:card":     "A–A layers",
		"detail:collar":    "collar",
		"quiz:pocket_kind": "pocket",
	} {
		got, ok := csFind(cs, id)
		require.True(t, ok, id)
		require.Equal(t, want, got.label, id)
		require.LessOrEqual(t, utf8.RuneCountInString(got.label), calloutLabelMaxRunes, id)
	}
	_, ok := csFind(cs, "detail:pocket")
	require.False(t, ok, "an aspect without a photo is not a candidate")

	own := `{"own":[{"purpose":"stitch","points":[[0.3,0.4]],"text":"double needle topstitch along the yoke seam, 6 mm"}]}`
	out, _ := mergeCalloutViews([]calloutViewAnswer{{flat: csFrontFlat(), ans: csAnswer(t, own)}}, cs)
	require.Len(t, out, 1)
	require.Equal(t, "double needle topstitch along t…", out[0].GetLabel(), "model-own: its text cut to 32")
	require.Equal(t, "double needle topstitch along the yoke seam, 6 mm", out[0].GetDescription(), "the description stays whole")
}

// Zones are places: a side over a quarter shrinks around the centre, one over half goes.
func TestCalloutZoneCap(t *testing.T) {
	ans := `{"own":[` +
		`{"purpose":"detail","points":[[0.1,0.2],[0.5,0.3]],"text":"pocket"},` +
		`{"purpose":"artwork","points":[[0.05,0.1],[0.95,0.2]],"text":"hem-wide band"},` +
		`{"purpose":"detail","points":[[0.6,0.6],[0.7,0.7]],"text":"tab"}]}`
	out, st := mergeCalloutViews([]calloutViewAnswer{{flat: csFrontFlat(), ans: csAnswer(t, ans)}}, nil)
	require.Len(t, out, 2)
	var corners []string
	for _, p := range out[0].GetPoints() {
		corners = append(corners, p.GetX().GetValue()+","+p.GetY().GetValue())
	}
	require.Equal(t, []string{"0.175,0.2", "0.425,0.2", "0.425,0.3", "0.175,0.3"}, corners, "clamped toward the centre 0.3")
	require.Equal(t, "tab", out[1].GetDescription())
	require.Equal(t, 1, st.clamped)
	require.Equal(t, 1, st.zoneDropped)
}

// ≤ 2 details per view (data-backed by rank first, the model's own last), the rest of the view untouched.
func TestCalloutDetailLimitPerView(t *testing.T) {
	c := csCard()
	c.Details = append(c.Details,
		entity.TechCardDetail{Key: ns("cuff"), Text: ns("button cuff"), MediaIds: []int{903}},
		entity.TechCardDetail{Key: ns("hem"), Text: ns("raw hem"), MediaIds: []int{904}})
	cands := buildCalloutCandidates(c, nil)
	var items []string
	nDetail := 0
	for i, cd := range cands {
		word, _, _ := calloutGeometryOf(cd.purpose)
		pts := map[string]string{"point": `[[0.5,0.5]]`, "box": `[[0.4,0.4],[0.5,0.5]]`, "line": `[[0.4,0.4],[0.5,0.5]]`}[word]
		if cd.purpose == calloutPurposeDetail {
			nDetail++
		} else if i > 3 {
			continue // keep the view under 12 so only the detail rule bites
		}
		items = append(items, fmt.Sprintf(`{"id":"c%d","points":%s}`, i+1, pts))
	}
	require.Equal(t, 4, nDetail) // collar, cuff, hem, quiz pocket
	own := `{"purpose":"detail","points":[[0.1,0.1],[0.2,0.2]],"text":"own detail"}`
	out, st := mergeCalloutViews([]calloutViewAnswer{{flat: csFrontFlat(),
		ans: csAnswer(t, `{"placements":[`+strings.Join(items, ",")+`],"own":[`+own+`]}`)}}, cands)
	var details []string
	for _, s := range out {
		if strings.Contains(s.GetSpec(), `"t":"detail"`) {
			details = append(details, s.GetSourceId())
		}
	}
	require.Equal(t, []string{"detail:collar", "detail:cuff"}, details)
	require.Equal(t, 3, st.detailCapped)
	require.Len(t, out, 4+2)
}

// ≤ 12 per flat: data-backed first by rank, model-own last.
func TestValidateCalloutViewCapsPerFlatWithModelOwnLast(t *testing.T) {
	c := csCard()
	for i := 0; i < 20; i++ {
		c.BomItems = append(c.BomItems, entity.TechCardBomItem{LineKey: fmt.Sprintf("T%d", i),
			Section: entity.BomSectionTrim, Kind: ns("tape"), Name: fmt.Sprintf("tape %d", i)})
	}
	cands := buildCalloutCandidates(c, nil)
	var items []string
	for i := len(cands); i >= 1; i-- { // reverse order: the cap must follow RANK, not answer order
		pts := map[string]string{"point": `[[0.5,0.5]]`, "box": `[[0.4,0.4],[0.5,0.5]]`, "line": `[[0.4,0.4],[0.5,0.5]]`}
		word, _, _ := calloutGeometryOf(cands[i-1].purpose)
		items = append(items, fmt.Sprintf(`{"id":"c%d","points":%s}`, i, pts[word]))
	}
	front := `{"placements":[` + strings.Join(items, ",") + `],"own":[{"purpose":"detail","points":[[0.1,0.1],[0.2,0.2]],"text":"own one"}]}`
	back := `{"own":[{"purpose":"detail","points":[[0.1,0.1],[0.2,0.2]],"text":"own on back"}]}`
	out, st := mergeCalloutViews([]calloutViewAnswer{{flat: csFrontFlat(), ans: csAnswer(t, front)}, {flat: csBackFlat(), ans: csAnswer(t, back)}}, cands)
	n := 0
	for _, s := range out {
		if s.GetMediaId() == csFront {
			n++
			require.True(t, s.GetFromData(), "a full flat keeps data-backed callouts, the model's own go first")
		}
	}
	require.Equal(t, calloutMaxPerFlat, n)
	require.Equal(t, "bom:L2", out[0].GetSourceId(), "rank order, not the answer's order")
	require.Equal(t, "pic:1", out[len(out)-1].GetSourceId(), "the back flat still has room for its own")
	require.Zero(t, st.invalid)
	require.Equal(t, len(cands)-calloutMaxPerFlat+1, st.capped)
}

func TestParseCalloutAnswerLenient(t *testing.T) {
	for name, raw := range map[string]string{
		"object":          `{"placements":[{"id":"c1","skip":true}]}`,
		"fenced":          "Sure:\n```json\n{\"placements\":[{\"id\":\"c1\",\"skip\":true}],\"own\":[]}\n```",
		"one bad element": `{"placements":[{"id":"c1","skip":true},{"id":["broken"]}]}`,
	} {
		a, ok := parseCalloutAnswer(raw)
		require.True(t, ok, name)
		require.Len(t, a.Placements, 1, name)
	}
	for _, raw := range []string{"I cannot see the flats.", `{"ideas":[]}`} {
		_, ok := parseCalloutAnswer(raw)
		require.False(t, ok, raw)
	}
}

// ─── the handler, against a fake provider ───

func csStand(t *testing.T, card *entity.TechCard, client *openrouter.Client) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil).Maybe()
	s := newEnhanceServer(t, client)
	s.repo = repo
	return s
}

func TestSuggestCalloutsHandler(t *testing.T) {
	// Every view gets the same answer: c1 on both views must come back once; the own one is per view.
	answer := `{"placements":[{"id":"c1","points":[[0.52,0.4]],"confidence":0.8},{"id":"c2","skip":true}],` +
		`"own":[{"purpose":"stitch","points":[[0.5,0.2]],"text":"yoke topstitch"}]}`
	client, rec := newSuggestFakeOR(t, openrouter.Config{}, suggestAnswer(answer))
	s := csStand(t, csCard(), client)
	req := &pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{csFront, csBack}, DismissedSourceIds: []string{"bom:L3"}}

	resp, err := s.SuggestCallouts(adminCtx("olga"), req)
	require.NoError(t, err)
	require.Equal(t, []string{"bom:L2@812", "pic:1@812", "pic:2@813"}, csWhere(resp.GetSuggestions()))
	require.Equal(t, "shared/model", resp.GetModel())
	zip := resp.GetSuggestions()[0]
	require.Equal(t, `{"lineKey":"L2","name":"YKK zip #5","t":"material"}`, zip.GetSpec())
	require.Equal(t, "YKK zip #5", zip.GetLabel())
	require.True(t, zip.GetFromData())

	calls := rec.all()
	require.Len(t, calls, 2, "one call per flat")
	views := map[string]string{}
	for _, c := range calls {
		require.Len(t, c.Images, 1, "each call sees its own flat only")
		require.True(t, c.JSONMode)
		require.Contains(t, c.UserText, "c1 | material | point")
		require.NotContains(t, c.UserText, "Horn button", "a dismissed source never reaches the model")
		require.NotContains(t, c.System, "Field jacket", "no card byte in the system role")
		views[c.Images[0]] = c.UserText
	}
	require.Contains(t, views["https://cdn.test/812-c.webp"], "VIEW: front (the picture, media_id 812)")
	require.Contains(t, views["https://cdn.test/813-c.webp"], "VIEW: back (the picture, media_id 813)")

	// An identical press within ten minutes is free.
	again, err := s.SuggestCallouts(adminCtx("olga"), req)
	require.NoError(t, err)
	require.Len(t, rec.all(), 2)
	require.Equal(t, csWhere(resp.GetSuggestions()), csWhere(again.GetSuggestions()))

	// The cache is per flat: dropping the back flat asks nobody.
	_, err = s.SuggestCallouts(adminCtx("olga"), &pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{csFront}, DismissedSourceIds: []string{"bom:L3"}})
	require.NoError(t, err)
	require.Len(t, rec.all(), 2)
}

func TestSuggestCalloutsRefusals(t *testing.T) {
	client, rec := newSuggestFakeOR(t, openrouter.Config{}, suggestAnswer(`{"placements":[]}`))
	s := csStand(t, csCard(), client)
	for name, tc := range map[string]struct {
		req   *pb_admin.SuggestCalloutsRequest
		field string
	}{
		"no card":         {&pb_admin.SuggestCalloutsRequest{MediaIds: []int32{csFront}}, "tech_card_id"},
		"no flats":        {&pb_admin.SuggestCalloutsRequest{TechCardId: 7}, "media_ids"},
		"five flats":      {&pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{1, 2, 3, 4, 5}}, "media_ids"},
		"moodboard media": {&pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{900}}, "media_ids"},
		"foreign media":   {&pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{csFront, 4242}}, "media_ids"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.SuggestCallouts(adminCtx("olga"), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.Equal(t, tc.field, fieldViolationOf(t, err).GetField())
		})
	}
	require.Empty(t, rec.all(), "a refused request reaches no provider")

	off := newEnhanceServer(t, openrouter.New(openrouter.Config{APIKey: " "}))
	_, err := off.SuggestCallouts(adminCtx("olga"), &pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{csFront}})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Equal(t, aiReasonNotConfigured, aiReasonOf(t, err))
}

func TestSuggestCalloutsUnusableAnswerIsAnError(t *testing.T) {
	client, _ := newSuggestFakeOR(t, openrouter.Config{}, suggestAnswer("I cannot see the flats."))
	s := csStand(t, csCard(), client)
	_, err := s.SuggestCallouts(adminCtx("olga"), &pb_admin.SuggestCalloutsRequest{TechCardId: 7, MediaIds: []int32{csFront}})
	require.Equal(t, codes.Internal, status.Code(err), "%v", err)
}
