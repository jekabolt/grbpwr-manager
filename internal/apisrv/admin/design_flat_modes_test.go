package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func flatParamsOf(mode string, under int32, rev int32) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Views: []string{"front", "back", "side_l", "side_r"}, Layout: designLayoutOne,
		Flat: &pb_common.DesignFlatParams{Mode: mode, UnderdrawingMediaId: under, UnderdrawingJoinsRev: rev}}
}

func bandWithJoins(rev int) *entity.DesignBand {
	return &entity.DesignBand{Joins: &entity.DesignJoins{Rev: rev, Doc: entity.DesignJoinsDoc{
		Items: []entity.DesignJoinItem{{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R"}}}}}
}

func TestUnderdrawingDoorRefusals(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 70, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
		{MediaId: 71, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaBack},
		{MediaId: 80, Category: entity.TechCardMediaCategoryMoodboard},
	}
	hand := func(refs ...*pb_common.DesignFlatStructureRef) *pb_common.DesignRunParams {
		p := flatParamsOf(designgen.FlatModeDrawing, 0, 0)
		p.Flat.StructureSource = designgen.FlatStructureHandFlat
		p.Flat.StructureRefs = refs
		return p
	}
	ref := func(id int32, v string) *pb_common.DesignFlatStructureRef {
		return &pb_common.DesignFlatStructureRef{MediaId: id, View: v}
	}
	parent := &entity.DesignRun{Id: 1}
	cases := []struct {
		name   string
		kind   string
		params *pb_common.DesignRunParams
		parent *entity.DesignRun
		band   *entity.DesignBand
		want   string
	}{
		{"no block is quick", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"front"}, Layout: designLayoutOne}, nil, nil, ""},
		{"quick stated", entity.DesignRunKindFlat, flatParamsOf("quick", 0, 0), nil, nil, ""},
		{"flat on a render", entity.DesignRunKindRender, flatParamsOf("quick", 0, 0), nil, nil, "flat_forbidden"},
		{"unknown mode", entity.DesignRunKindFlat, flatParamsOf("trace", 0, 0), nil, nil, "unknown_flat_mode"},
		{"underdrawing on quick", entity.DesignRunKindFlat, flatParamsOf("quick", 5, 3), nil, nil, "underdrawing_forbidden"},
		{"required", entity.DesignRunKindFlat, flatParamsOf("drawing", 0, 3), nil, bandWithJoins(3), "underdrawing_required"},
		{"fresh", entity.DesignRunKindFlat, flatParamsOf("drawing", 5, 3), nil, bandWithJoins(3), ""},
		{"stale rev", entity.DesignRunKindFlat, flatParamsOf("drawing_photos", 5, 2), nil, bandWithJoins(3), "underdrawing_stale"},
		{"no list", entity.DesignRunKindFlat, flatParamsOf("drawing", 5, 0), nil, &entity.DesignBand{}, "underdrawing_stale"},
		{"rerun skips the rev", entity.DesignRunKindFlat, flatParamsOf("drawing", 5, 2), parent, bandWithJoins(3), ""},
		{"per_view", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"front"}, Layout: designLayoutPerView,
			Flat: &pb_common.DesignFlatParams{Mode: "drawing", UnderdrawingMediaId: 5}}, nil, bandWithJoins(0), "mode_not_for_this_run"},
		{"detail only", entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"detail"}, Layout: designLayoutOne,
			Flat: &pb_common.DesignFlatParams{Mode: "drawing", UnderdrawingMediaId: 5}}, nil, bandWithJoins(0), "mode_not_for_this_run"},
		{"unknown source", entity.DesignRunKindFlat, func() *pb_common.DesignRunParams {
			p := flatParamsOf("drawing", 5, 3)
			p.Flat.StructureSource = "photo"
			return p
		}(), nil, bandWithJoins(3), "unknown_structure_source"},
		{"refs on rendered", entity.DesignRunKindFlat, func() *pb_common.DesignRunParams {
			p := flatParamsOf("drawing", 5, 3)
			p.Flat.StructureRefs = []*pb_common.DesignFlatStructureRef{ref(70, "front")}
			return p
		}(), nil, bandWithJoins(3), "structure_malformed"},
		{"hand flat, no rev guard", entity.DesignRunKindFlat, hand(ref(70, "front"), ref(71, "back")), nil, nil, ""},
		{"hand flat, none", entity.DesignRunKindFlat, hand(), nil, nil, "structure_required"},
		{"hand flat, moodboard media", entity.DesignRunKindFlat, hand(ref(80, "front")), nil, nil, "structure_not_on_card"},
		{"hand flat, foreign media", entity.DesignRunKindFlat, hand(ref(99, "front")), nil, nil, "structure_not_on_card"},
		{"hand flat, twice one view", entity.DesignRunKindFlat, hand(ref(70, "front"), ref(71, "front")), nil, nil, "structure_malformed"},
		{"hand flat, detail view", entity.DesignRunKindFlat, hand(ref(70, "detail")), nil, nil, "structure_malformed"},
		{"hand flat + rendered id", entity.DesignRunKindFlat, func() *pb_common.DesignRunParams {
			p := hand(ref(70, "front"))
			p.Flat.UnderdrawingMediaId = 5
			return p
		}(), nil, nil, "structure_malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, flatReason(t, designRefuseFlatParams(c.kind, c.params, c.parent, c.band, card)))
		})
	}
	st, _ := status.FromError(designRefuseFlatParams(entity.DesignRunKindFlat, flatParamsOf("drawing", 5, 2), nil, bandWithJoins(3), card))
	require.Equal(t, codes.FailedPrecondition, st.Code())
	for _, d := range st.Details() {
		require.Equal(t, "3", d.(*errdetails.ErrorInfo).GetMetadata()["joins_rev"])
	}

	require.Equal(t, "", flatReason(t, designUnderdrawingShapeRefusal(5, 1536, 864)))
	require.Equal(t, "", flatReason(t, designUnderdrawingShapeRefusal(5, 0, 0)), "unknown size passes")
	require.Equal(t, "underdrawing_malformed", flatReason(t, designUnderdrawingShapeRefusal(5, 1024, 1024)))
	require.Equal(t, "underdrawing_malformed", flatReason(t, designUnderdrawingShapeRefusal(5, 1536, 800)))
}

func TestFlatRerunInheritsTheParentsMode(t *testing.T) {
	parent := &entity.DesignRun{Id: 7, Params: entity.RawJSON(`{"views":["front","back"],"layout":"one","flat":{"mode":"drawing","underdrawing_media_id":5,"underdrawing_joins_rev":3}}`), RequestedOutputs: 4}
	// a client that knows nothing of the block (every client before the modes) inherits it
	p := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, p, parent))
	require.Equal(t, "drawing", p.GetFlat().GetMode())
	require.Equal(t, int32(5), p.GetFlat().GetUnderdrawingMediaId())
	require.Equal(t, 4, designRerunFlatOutputs(entity.DesignRunKindFlat, p, designRequestedOutputs(entity.DesignRunKindFlat, p), parent))
	// the same block restated passes; another mode or drawing is refused
	same := flatParamsOf("drawing", 5, 3)
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, same, parent))
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("quick", 0, 0), parent)))
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("drawing", 6, 3), parent)))
	// a legacy parent (no block) stays quick; quick restated is fine; a drawing mode is refused
	legacy := &entity.DesignRun{Id: 8, Params: entity.RawJSON(`{"views":["front","back"],"layout":"one"}`), RequestedOutputs: 4}
	q := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	require.NoError(t, designFlatRerunInherit(entity.DesignRunKindFlat, q, legacy))
	require.Nil(t, q.GetFlat())
	require.Equal(t, 4, designRerunFlatOutputs(entity.DesignRunKindFlat, q, designRequestedOutputs(entity.DesignRunKindFlat, q), legacy),
		"a four-candidate run from before the modes reruns as four")
	require.Equal(t, "mode_not_for_this_run", flatReason(t, designFlatRerunInherit(entity.DesignRunKindFlat, flatParamsOf("drawing", 5, 3), legacy)))
	// a fix is one picture whatever its mode
	fix := flatParamsOf("drawing", 5, 3)
	fix.FixTargets = []string{"front"}
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat, fix))
	fix.Flat.UnderdrawingJoinsRev = 1
	require.Equal(t, "", flatReason(t, designRefuseFlatParams(entity.DesignRunKindFlat, fix, nil, bandWithJoins(3), nil)),
		"a fix traces the drawing its plate was traced from: no rev check")
}

func TestSnapshotRefsPerMode(t *testing.T) {
	refs := []entity.DesignReference{{MediaId: 11, Role: "front"}, {MediaId: 12, Role: "back"}}
	bench := []entity.DesignBenchSlot{}
	snap := func(p *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
		out, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Refs: refs, Bench: bench, Params: p})
		require.NoError(t, err)
		return out.GetRefs()
	}
	quick := snap(&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne, ExtraInputMediaIds: []int32{9}})
	require.Equal(t, []int32{11, 12, 9}, refIDs(quick), "quick: as today")

	d := flatParamsOf("drawing", 5, 3)
	d.ExtraInputMediaIds = []int32{9}
	got := snap(d)
	require.Equal(t, []int32{5}, refIDs(got), "drawing: the drawing alone")
	require.Equal(t, entity.DesignRefRoleUnderdrawing, got[0].GetRole())

	dp := flatParamsOf("drawing_photos", 5, 3)
	dp.ExtraInputMediaIds = []int32{9}
	got = snap(dp)
	require.Equal(t, []int32{5, 11, 12, 9}, refIDs(got), "drawing_photos: the drawing first, then the photos")
	require.Equal(t, "front", got[1].GetRole())

	h := flatParamsOf("drawing", 0, 0)
	h.Flat.StructureSource = designgen.FlatStructureHandFlat
	h.Flat.StructureRefs = []*pb_common.DesignFlatStructureRef{{MediaId: 70, View: "front"}, {MediaId: 71, View: "back"}}
	got = snap(h)
	require.Equal(t, []int32{70, 71}, refIDs(got))
	require.Equal(t, entity.DesignRefRoleUnderdrawing, got[1].GetRole())

	// every structure picture is a media source the doors see
	var where []string
	for _, r := range designRunInputMediaRefs(h, nil) {
		where = append(where, r.Where)
	}
	require.Equal(t, []string{"params.flat.structure_refs.0.media_id", "params.flat.structure_refs.1.media_id"}, where)
	require.Equal(t, "params.flat.underdrawing_media_id", designRunInputMediaRefs(d, nil)[1].Where)
}

func refIDs(refs []*pb_common.DesignInputRef) []int32 {
	out := make([]int32, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.GetMediaId())
	}
	return out
}

func TestReferenceRoleUnderdrawingIsReserved(t *testing.T) {
	_, err := (&Server{}).SetDesignReferenceRole(context.Background(), &pb_admin.SetDesignReferenceRoleRequest{
		TechCardId: 1, MediaId: 2, Role: "underdrawing"})
	require.Equal(t, "role_reserved", flatReason(t, err))
}

func TestPartsCardConstructionFromJoins(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "designgen", "testdata", "joins", "c38-joins.json"))
	require.NoError(t, err)
	a, ok := entity.ParseDesignJoinsAnswer(string(raw))
	require.True(t, ok)
	got := designPartsConstructionText(a.Doc())
	require.True(t, strings.HasPrefix(got, designgen.JoinsListText(a.Doc())+"\n\n"))
	want := `PART VOCABULARY (closed — use these names; never name a part the construction does not have; a region that matches none of them is an opening or the nearest body panel):
- front body
- back body
- neck bind front
- left strap
- right strap
- left armhole bind
- right armhole bind
- back open bind
- opening: bounded by strap_L, strap_R, back_open_bind`
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
}
