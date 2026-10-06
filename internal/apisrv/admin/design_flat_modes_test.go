package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// flatReason — the ErrorInfo reason of a refusal ("" for nil).
func flatReason(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei.GetReason()
		}
	}
	return "no-reason:" + st.Message()
}

var flatFour = []string{"front", "back", "side_l", "side_r"}

func flatParamsOf(mode string, refs ...*pb_common.DesignFlatStructureRef) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Views: flatFour, Layout: designLayoutOne,
		Flat: &pb_common.DesignFlatParams{Mode: mode, StructureRefs: refs}}
}

func sref(id int32, role string) *pb_common.DesignFlatStructureRef {
	return &pb_common.DesignFlatStructureRef{MediaId: id, Role: role}
}

// bandWithJoins — a band whose list was confirmed (when confirmed) against the card it is checked with
// in these tests: no references, no garment note.
func bandWithJoins(rev int, confirmed bool) *entity.DesignBand {
	src := ""
	if confirmed {
		src = designJoinsSourceFP(nil, nil)
	}
	return &entity.DesignBand{Joins: &entity.DesignJoins{Rev: rev, Doc: entity.DesignJoinsDoc{
		Items: []entity.DesignJoinItem{{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R"}}, Confirmed: confirmed, ConfirmedSource: src}}}
}

// TestStrapsDoorRefusesAStaleConfirmation — Codex b1: the straps door refuses a confirmation made
// against other photos or another garment note (joins_unconfirmed, reason=stale); a confirmation older
// than the stored fingerprint falls back to the fingerprint the list was written from.
// MUTATIONS IT CATCHES: the gate checking only `Confirmed`; the fingerprint ignoring a role, a note or
// the garment note; the legacy fallback missing (every old confirmation stale) or always passing.
func TestStrapsDoorRefusesAStaleConfirmation(t *testing.T) {
	card := &entity.TechCard{}
	refs := []entity.DesignReference{{MediaId: 5, Role: "front"}, {MediaId: 6, Role: "back"}}
	confirmedNow := func() *entity.DesignBand {
		b := bandWithJoins(4, true)
		b.References = refs
		b.Joins.Doc.ConfirmedSource = designJoinsSourceFP(card, refs)
		return b
	}
	staleOf := func(b *entity.DesignBand, c *entity.TechCard) (string, string) {
		st, _ := status.FromError(designRefuseFlatParams(entity.DesignRunKindFlat, flatParamsOf("straps"), nil, b, c))
		for _, d := range st.Details() {
			ei := d.(*errdetails.ErrorInfo)
			return ei.GetReason(), ei.GetMetadata()["reason"]
		}
		return "", ""
	}
	r, why := staleOf(confirmedNow(), card)
	require.Equal(t, "", r, "a confirmation of today's photos passes")
	require.Equal(t, "", why)

	// a photo replaced
	b := confirmedNow()
	b.References = []entity.DesignReference{{MediaId: 5, Role: "front"}, {MediaId: 9, Role: "back"}}
	r, why = staleOf(b, card)
	require.Equal(t, "joins_unconfirmed", r)
	require.Equal(t, "stale", why)
	// a role changed
	b = confirmedNow()
	b.References = []entity.DesignReference{{MediaId: 5, Role: "front"}, {MediaId: 6, Role: "side_l"}}
	r, why = staleOf(b, card)
	require.Equal(t, "stale", why)
	// a photo's note changed
	b = confirmedNow()
	b.References = []entity.DesignReference{{MediaId: 5, Role: "front"}, {MediaId: 6, Role: "back"}}
	b.References[1].Note.String, b.References[1].Note.Valid = "the crossed straps", true
	r, why = staleOf(b, card)
	require.Equal(t, "stale", why)
	// the garment note changed
	noted := &entity.TechCard{}
	noted.GarmentDescription.String, noted.GarmentDescription.Valid = "open back", true
	r, why = staleOf(confirmedNow(), noted)
	require.Equal(t, "joins_unconfirmed", r)
	require.Equal(t, "stale", why)

	// a confirmation from before the field: the list's own source decides
	legacy := confirmedNow()
	legacy.Joins.Doc.ConfirmedSource = ""
	legacy.Joins.SourceFingerprint = designJoinsSourceFP(card, refs)
	r, _ = staleOf(legacy, card)
	require.Equal(t, "", r)
	legacy.Joins.SourceFingerprint = "other"
	_, why = staleOf(legacy, card)
	require.Equal(t, "stale", why)
	legacy.Joins.SourceFingerprint = ""
	_, why = staleOf(legacy, card)
	require.Equal(t, "stale", why, "a hand-written list confirmed before the field must be confirmed again")
}

// TestJoinsFlightKeySeparatesForce — Codex b2: a forced re-read never shares a flight with a non-force
// read of the same source, nor with a read of other photos.
func TestJoinsFlightKeySeparatesForce(t *testing.T) {
	all := []designJoinsPhoto{{MediaID: 3}, {MediaID: 1}, {MediaID: 2}}
	kept := []designJoinsPhoto{{MediaID: 1}}
	require.NotEqual(t, designJoinsFlightKey(7, "fp", true, all), designJoinsFlightKey(7, "fp", false, all))
	require.NotEqual(t, designJoinsFlightKey(7, "fp", false, all), designJoinsFlightKey(7, "fp", false, kept))
	require.Equal(t, designJoinsFlightKey(7, "fp", false, all),
		designJoinsFlightKey(7, "fp", false, []designJoinsPhoto{{MediaID: 1}, {MediaID: 2}, {MediaID: 3}}), "order does not split a flight")
}

func TestFlatModeDoorRefusals(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 70, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
		{MediaId: 71, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaBack},
		{MediaId: 80, Category: entity.TechCardMediaCategoryMoodboard},
	}
	parent := &entity.DesignRun{Id: 1}
	fix := flatParamsOf("straps")
	fix.FixTargets = []string{"front"}
	cases := []struct {
		name   string
		kind   string
		params *pb_common.DesignRunParams
		parent *entity.DesignRun
		band   *entity.DesignBand
		want   string
	}{
		{"no block is photos", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"front"}, Layout: designLayoutOne}, nil, nil, ""},
		{"photos stated", entity.DesignRunKindFlat, flatParamsOf("photos"), nil, nil, ""},
		{"photos on per_view", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"front"}, Layout: designLayoutPerView, Flat: &pb_common.DesignFlatParams{}}, nil, nil, ""},
		{"flat on a render", entity.DesignRunKindRender, flatParamsOf(""), nil, nil, "flat_forbidden"},
		{"unknown mode", entity.DesignRunKindFlat, flatParamsOf("drawing"), nil, nil, "unknown_flat_mode"},
		{"refs on photos", entity.DesignRunKindFlat, flatParamsOf("", sref(70, "front_flat")), nil, nil, "structure_forbidden"},
		{"refs on straps", entity.DesignRunKindFlat, flatParamsOf("straps", sref(70, "front_flat")), nil, bandWithJoins(3, true), "structure_forbidden"},
		{"straps confirmed", entity.DesignRunKindFlat, flatParamsOf("straps"), nil, bandWithJoins(3, true), ""},
		{"straps unconfirmed", entity.DesignRunKindFlat, flatParamsOf("straps"), nil, bandWithJoins(3, false), "joins_unconfirmed"},
		{"straps no list", entity.DesignRunKindFlat, flatParamsOf("straps"), nil, &entity.DesignBand{}, "joins_unconfirmed"},
		{"straps rerun skips", entity.DesignRunKindFlat, flatParamsOf("straps"), parent, bandWithJoins(3, false), ""},
		{"straps fix needs the confirmation too", entity.DesignRunKindFlat, fix, nil, bandWithJoins(3, false), "joins_unconfirmed"},
		{"straps per_view", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"front"}, Layout: designLayoutPerView,
			Flat: &pb_common.DesignFlatParams{Mode: "straps"}}, nil, bandWithJoins(3, true), "mode_not_for_this_run"},
		{"hand flat detail only", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"detail"}, Layout: designLayoutOne,
			Flat: &pb_common.DesignFlatParams{Mode: "hand_flat", StructureRefs: []*pb_common.DesignFlatStructureRef{sref(70, "front_flat")}}}, nil, nil, "mode_not_for_this_run"},
		{"hand flat", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat"), sref(71, "back_flat")), nil, nil, ""},
		{"hand flat none", entity.DesignRunKindFlat, flatParamsOf("hand_flat"), nil, nil, "structure_required"},
		{"hand flat moodboard media", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(80, "front_flat")), nil, nil, "structure_not_on_card"},
		{"hand flat foreign media", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(99, "front_flat")), nil, nil, "structure_not_on_card"},
		{"hand flat rerun not re-read", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(99, "front_flat")), parent, nil, ""},
		{"hand flat role twice", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat"), sref(71, "front_flat")), nil, nil, "structure_malformed"},
		{"hand flat bad role", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front")), nil, nil, "structure_malformed"},
		{"hand flat media twice", entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat"), sref(70, "back_flat")), nil, nil, "structure_malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, flatReason(t, designRefuseFlatParams(c.kind, c.params, c.parent, c.band, card)))
		})
	}
	st, _ := status.FromError(designRefuseFlatParams(entity.DesignRunKindFlat, flatParamsOf("straps"), nil, bandWithJoins(3, false), card))
	require.Equal(t, codes.FailedPrecondition, st.Code())
	for _, d := range st.Details() {
		require.Equal(t, "3", d.(*errdetails.ErrorInfo).GetMetadata()["joins_rev"])
	}
}

func TestFlatModeOutputsAndReruns(t *testing.T) {
	// Wave 10: every mode buys ONE sheet (the candidate quiz is gone).
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, flatParamsOf("")))
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat"))))
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, flatParamsOf("straps")))
	fix := flatParamsOf("straps")
	fix.FixTargets = []string{"front"}
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, fix), "a fix is one picture")

	parent := &entity.DesignRun{Id: 7, Params: entity.RawJSON(`{"views":["front","back","side_l","side_r"],"layout":"one","flat":{"mode":"straps"}}`), RequestedOutputs: 4}
	// a client that knows nothing of the block (every client before the modes) inherits it
	p := &pb_common.DesignRunParams{Views: flatFour, Layout: designLayoutOne}
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, p, parent))
	require.Equal(t, "straps", p.GetFlat().GetMode())
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, p), "a rerun of a four-candidate parent is one sheet")
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("straps"), parent))
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf(""), parent)))
	// a legacy parent (no block) is the photos route; its four candidates rerun as one sheet
	legacy := &entity.DesignRun{Id: 8, Params: entity.RawJSON(`{"views":["front","back"],"layout":"one"}`), RequestedOutputs: 4}
	q := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, q, legacy))
	require.Nil(t, q.GetFlat())
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, q))
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("straps"), legacy)))
	// hand_flat: the same flats and the same views
	hp := &entity.DesignRun{Id: 9, Params: entity.RawJSON(`{"views":["front","back","side_l","side_r"],"layout":"one","flat":{"mode":"hand_flat","structure_refs":[{"media_id":70,"role":"front_flat"}]}}`)}
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat")), hp))
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(71, "front_flat")), hp)))
	swapped := &pb_common.DesignRunParams{Views: []string{"back", "front", "side_l", "side_r"}, Layout: designLayoutOne}
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, swapped, hp)))
}

func TestFlatSnapshotRefsPerMode(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{{MediaId: 13, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMood}}
	refs := []entity.DesignReference{{MediaId: 11, Role: "front"}, {MediaId: 12, Role: "back"}, {MediaId: 13}}
	snap := func(p *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
		out, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: card, Refs: refs, Params: p})
		require.NoError(t, err)
		return out.GetRefs()
	}
	got := snap(&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	require.Equal(t, []int32{11, 12}, refIDs(got), "photos: the roled photos only — the mood picture stays home (wave 10)")
	got = snap(flatParamsOf("hand_flat", sref(70, "front_flat"), sref(71, "back_flat")))
	require.Equal(t, []int32{70, 71, 11, 12}, refIDs(got), "hand_flat: the flats first, then the photos")
	require.Equal(t, entity.DesignRefRoleBackFlat, got[1].GetRole())

	var where []string
	for _, r := range designRunInputMediaRefs(flatParamsOf("hand_flat", sref(70, "front_flat")), nil) {
		where = append(where, r.Where)
	}
	require.Equal(t, []string{"params.flat.structure_refs.0.media_id"}, where)

	band := bandWithJoins(3, true)
	require.Nil(t, designRunJoins(entity.DesignRunKindFlat, flatParamsOf("hand_flat", sref(70, "front_flat")), band, nil), "hand_flat freezes no list")
	require.NotNil(t, designRunJoins(entity.DesignRunKindFlat, flatParamsOf("straps"), band, nil))
}

func refIDs(refs []*pb_common.DesignInputRef) []int32 {
	out := make([]int32, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.GetMediaId())
	}
	return out
}

func TestFlatReferenceCeiling(t *testing.T) {
	srv := &Server{}
	srv.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	p := flatParamsOf("")
	p.Image = &pb_common.DesignImageOptions{Model: designgen.FlatDefaultEngine}
	e, ok := designgen.FindEngine(srv.designEngineTable(), designgen.FlatDefaultEngine)
	require.True(t, ok)
	refs := func(n int) []byte {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, `{"media_id":`+strconv.Itoa(100+i)+`,"role":"front"}`)
		}
		return []byte(`{"refs":[` + strings.Join(parts, ",") + `]}`)
	}
	params := []byte(`{"views":["front","back"],"layout":"one","colour":{"fabric_media_id":9}}`)
	require.Equal(t, "too_many_pictures", flatReason(t, srv.designRefuseFlatReferenceCeiling(entity.DesignRunKindFlat, p, params, refs(e.MaxRefs))),
		"the fabric photo counts too")
	require.NoError(t, srv.designRefuseFlatReferenceCeiling(entity.DesignRunKindFlat, p, params, refs(e.MaxRefs-1)))
}

func TestReservedReferenceRoles(t *testing.T) {
	for _, role := range []string{"front_flat", "back_flat", "mood", "underdrawing"} {
		_, err := (&Server{}).SetDesignReferenceRole(context.Background(), &pb_admin.SetDesignReferenceRoleRequest{
			TechCardId: 1, MediaId: 2, Role: role})
		require.Equal(t, "role_reserved", flatReason(t, err), role)
	}
}

func TestJoinsEditMarksAndConfirm(t *testing.T) {
	prev := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{
		Items:    []entity.DesignJoinItem{{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R", Text: "hem"}, {ID: "side", Kind: "seam", From: "UA_L", To: "HEM_L"}},
		Absences: []string{"no sleeves"},
	})
	next := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{
		Items: []entity.DesignJoinItem{
			{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R", Text: "raw hem"},
			{ID: "side", Kind: "seam", From: "UA_L", To: "HEM_L"},
			{ID: "pocket_L", Kind: "pocket", From: "CHEST_L", Text: "patch pocket"},
		},
		Absences: []string{"no sleeves", "no collar"},
	})
	designJoinsMarkEdits(&next, &prev)
	require.True(t, next.Items[0].Edited, "changed text")
	require.False(t, next.Items[1].Edited, "untouched")
	require.True(t, next.Items[2].Edited, "added")
	require.Equal(t, []string{"no collar"}, next.EditedAbsences)
	// a later save keeps the marks of what the designer already changed
	again := entity.SanitizeDesignJoinsDoc(next)
	designJoinsMarkEdits(&again, &next)
	require.True(t, again.Items[0].Edited)
	require.Equal(t, []string{"no collar"}, again.EditedAbsences)
	// the wire carries edited + confirmed, and never trusts an incoming edited
	next.Confirmed = true
	pb := designJoinsToPb(&entity.DesignJoins{Rev: 4, Doc: next})
	require.True(t, pb.GetConfirmed())
	require.True(t, pb.GetItems()[0].GetEdited())
	back := entity.SanitizeDesignJoinsDoc(designJoinsDocFromPb(pb))
	require.False(t, back.Items[0].Edited)
	require.False(t, back.Confirmed)
}

func TestPartsCardConstructionFromJoins(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "designgen", "testdata", "joins", "c38-joins.json"))
	require.NoError(t, err)
	a, ok := entity.ParseDesignJoinsAnswer(string(raw))
	require.True(t, ok)
	got := designPartsConstructionText(a.Doc())
	require.True(t, strings.HasPrefix(got, designgen.JoinsListText(a.Doc())+"\n\n"))
	want := `PART VOCABULARY (closed — use exactly these names; every separately cut piece below is its own part with its own regions, never folded into a body panel; never name a part the construction does not have; a region with no cloth of its own is an opening or the inside of the part seen through it, never a binding or a band):
- front body — the outer front body panel
- back body — the outer back body panel
- front neck binding — a thin binding strip (narrow) cut as its own piece, along NP_R → CFN → NP_L
- left strap — a strap cut as its own piece; it STARTS at NP_L and ends at UA_R..MB_R:0.3; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- right strap — a strap cut as its own piece; it STARTS at NP_R and ends at UA_L..MB_L:0.3; named by the wearer's shoulder where it starts, with this one name on every view, even where it crosses to the other side
- left armhole binding — a thin binding strip (narrow) cut as its own piece, along NP_L..SP_L:0.35 → FSH_L..UA_L:0.5 → UA_L
- right armhole binding — a thin binding strip (narrow) cut as its own piece, along NP_R..SP_R:0.35 → FSH_R..UA_R:0.5 → UA_R
- back open binding — a thin binding strip (narrow) cut as its own piece, along UA_L → MB_L → MB_C → MB_R → UA_R
- opening: bounded by strap_L, strap_R, back_open_bind — no cloth: label it «opening», or put it in the part whose inside shows through it (seen_through)`
	require.True(t, strings.HasSuffix(got, want), got)
	require.NotContains(t, strings.ToLower(got[strings.Index(got, "PART VOCABULARY"):]), "upper back")
	require.Equal(t, "", designPartsConstructionText(entity.DesignJoinsDoc{}), "no list, no block")
	require.Equal(t, "left strap", designPartsNameOf(entity.DesignJoinItem{ID: "strap_from_L", Kind: "strap"}))
	require.Equal(t, "neck binding", designPartsNameOf(entity.DesignJoinItem{ID: "neck_binding", Kind: "binding"}))
	require.Equal(t, "right pocket", designPartsNameOf(entity.DesignJoinItem{ID: "pocket", Kind: "pocket", Side: "R"}))
}

func TestJoinsFitOnTheWire(t *testing.T) {
	j := &entity.DesignJoins{Rev: 2, Doc: entity.DesignJoinsDoc{
		Items: []entity.DesignJoinItem{{ID: "p", Kind: "pocket", From: "CHEST_L", Size: 0.05}},
		Fit:   &entity.DesignJoinsFit{Ease: "relaxed", Waist: "straight"}}}
	pb := designJoinsToPb(j)
	require.Equal(t, "relaxed", pb.GetFit().GetEase())
	require.Equal(t, 0.05, pb.GetItems()[0].GetSize())
	back := entity.SanitizeDesignJoinsDoc(designJoinsDocFromPb(pb))
	require.Equal(t, j.Doc.Fit, back.Fit)
	require.Equal(t, 0.05, back.Items[0].Size)
	b, _ := json.Marshal(back)
	require.Contains(t, string(b), `"fit":{"ease":"relaxed","waist":"straight"}`)
	closed := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "loop_closure", Kind: "closure", From: "CFN", Via: []string{"CHEST_C"}, To: "CFN", Closed: true}}})
	require.True(t, closed.Items[0].Closed, "a closure keeps closed")
}

// TestFlatSendsOnlyRoledPhotos — wave 10 (owner 06.10): a flat run sends no moodboard picture (a board
// MOOD picture, whatever role its reference row names, or a `mood` ref) and no role-less input; every
// other kind keeps its refs.
func TestFlatSendsOnlyRoledPhotos(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 80, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMood},
		{MediaId: 81, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleTarget},
	}
	refs := []*pb_common.DesignInputRef{{MediaId: 80, Role: "front"}, {MediaId: 82}, {MediaId: 83, Role: "mood"},
		{MediaId: 81, Role: "back"}, {MediaId: 5, Role: "side_l"}, {MediaId: 6, Role: "detail"}}
	got := designFlatOnlyRoledPhotos(designInputSources{Kind: entity.DesignRunKindFlat, Card: card}, refs)
	var ids []int32
	for _, r := range got {
		ids = append(ids, r.GetMediaId())
	}
	require.Equal(t, []int32{81, 5, 6}, ids)
	other := []*pb_common.DesignInputRef{{MediaId: 80, Role: "front"}, {MediaId: 82}}
	require.Len(t, designFlatOnlyRoledPhotos(designInputSources{Kind: entity.DesignRunKindRender, Card: card}, other), 2, "only a flat run")
}

// TestFlatWithNothingToDrawIsRefused — wave 10 (Codex review): a garment sheet with no side-role photo
// and no construction words is refused before money; a photo or words let it through.
func TestFlatWithNothingToDrawIsRefused(t *testing.T) {
	p := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	_, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{}, Params: p,
		Refs: []entity.DesignReference{{MediaId: 9}}})
	require.Equal(t, "flat_nothing_to_draw", flatReason(t, err))
	_, err = designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{}, Params: p,
		Refs: []entity.DesignReference{{MediaId: 9, Role: "front"}}})
	require.NoError(t, err)
}

// TestHandFlatWithOnlyItsFlatsIsNotRefused — the nothing-to-draw check counts the designer's own flats.
func TestHandFlatWithOnlyItsFlatsIsNotRefused(t *testing.T) {
	_, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{},
		Params: flatParamsOf("hand_flat", sref(70, "front_flat"))})
	require.NoError(t, err)
}

// TestFlatDetailRunSendsOnlyThatDetailsRefs — T74 (owner 06.10): a detail run's snapshot carries only
// the `detail` references tied to the requested slot; sides, other details, untied details, mood and
// extras stay out. No tied reference → no refs. A garment-sheet run keeps them all.
func TestFlatDetailRunSendsOnlyThatDetailsRefs(t *testing.T) {
	slot := func(id int32) sql.NullInt32 { return sql.NullInt32{Int32: id, Valid: true} }
	refs := []entity.DesignReference{
		{MediaId: 1, Role: "front"},
		{MediaId: 2, Role: "back"},
		{MediaId: 3, Role: "detail", DetailSlotId: slot(11)},
		{MediaId: 4, Role: "detail", DetailSlotId: slot(12)},
		{MediaId: 5, Role: "detail"},
		{MediaId: 6, Role: "mood"},
		{MediaId: 7, Role: "detail", DetailSlotId: slot(11)},
	}
	ids := func(in *pb_common.DesignInputSnapshot) []int32 {
		var out []int32
		for _, r := range in.GetRefs() {
			out = append(out, r.GetMediaId())
		}
		return out
	}
	detail := &pb_common.DesignRunParams{Views: []string{"detail"}, Layout: designLayoutOne,
		DetailSlotIds: []int32{11}, ExtraInputMediaIds: []int32{9}}
	got, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{}, Params: detail, Refs: refs})
	require.NoError(t, err)
	require.Equal(t, []int32{3, 7}, ids(got))

	detail.DetailSlotIds = []int32{13}
	got, err = designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{}, Params: detail, Refs: refs})
	require.NoError(t, err)
	require.Empty(t, got.GetRefs(), "no reference tied to the asked detail → none sent")

	sheet := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	got, err = designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: &entity.TechCard{}, Params: sheet, Refs: refs})
	require.NoError(t, err)
	require.Equal(t, []int32{1, 2, 3, 4, 5, 7}, ids(got), "non-detail runs unchanged")
}
