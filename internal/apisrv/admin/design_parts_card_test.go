package admin

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var cardViews4 = []designPartsCardView{
	{View: "front", Count: 6}, {View: "back", Count: 4}, {View: "side_l", Count: 3}, {View: "side_r", Count: 3},
}

func TestParseDesignPartsCardTopology(t *testing.T) {
	raw := "Reasoning first.\n```json\n" + `{"parts":[
		{"label":"Collar","regions":{"front":[1,"2"],"back":[1],"side_l":[1],"side-r":1,"detail":[1]}},
		{"label":"left sleeve","regions":{"front":[3,2,99,0],"back":[2],"side_l":[2]}},
		{"label":"","regions":{"front":[4]}},
		{"label":"unnamed","regions":{"back":[3]}},
		{"label":"collar","regions":{"front":[5]}},
		{"label":"ghost","regions":{"front":[42],"top":[1]}},
		{"label":"` + strings.Repeat("X", 60) + `","regions":{"side_r":[2]}}
	],"split_needed":[{"view":"back","region":"4","why":"yoke and body"},{"view":"back","region":4,"why":"dup"},
		{"view":"front","region":9,"why":"off"},{"view":"top","region":1,"why":"no such view"},{"view":"side_l","region":3,"why":"x"}]}` + "\n```"
	parts, splits, ok := parseDesignPartsCard(raw, cardViews4)
	if !ok {
		t.Fatal("must be usable")
	}
	long := strings.Repeat("x", 40)
	want := map[string][]entity.DesignPartGroup{
		"front": {
			{Label: "collar", Regions: []int{1, 2}, PartKey: "collar"},
			{Label: "left sleeve", Regions: []int{3}, PartKey: "left-sleeve"},  // 2 is the collar's, 99/0 dropped
			{Label: "collar", Regions: []int{5}, PartKey: "collar-2"},          // a second "collar" is a second part
			{Label: "unnamed", Regions: []int{4, 6}, PartKey: "unnamed-front"}, // the empty label + the lost 6
		},
		"back": {
			{Label: "collar", Regions: []int{1}, PartKey: "collar"},
			{Label: "left sleeve", Regions: []int{2}, PartKey: "left-sleeve"},
			{Label: "unnamed", Regions: []int{3, 4}, PartKey: "unnamed-back"}, // "unnamed" is not a part
		},
		"side_l": {
			{Label: "collar", Regions: []int{1}, PartKey: "collar"},
			{Label: "left sleeve", Regions: []int{2}, PartKey: "left-sleeve"},
			{Label: "unnamed", Regions: []int{3}, PartKey: "unnamed-side_l"},
		},
		"side_r": {
			{Label: "collar", Regions: []int{1}, PartKey: "collar"}, // "side-r": 1 — a folded key, a bare number
			{Label: long, Regions: []int{2}, PartKey: long},
			{Label: "unnamed", Regions: []int{3}, PartKey: "unnamed-side_r"},
		},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts:\n got %+v\nwant %+v", parts, want)
	}
	wantSplits := map[string][]entity.DesignPartSplit{
		"back":   {{Region: 4, Why: "yoke and body"}},
		"side_l": {{Region: 3, Why: "x"}},
	}
	if !reflect.DeepEqual(splits, wantSplits) {
		t.Fatalf("splits: %+v", splits)
	}
}

// Only the sides asked about are read: the same answer cut for two sides keeps the keys.
func TestParseDesignPartsCardSubsetOfSides(t *testing.T) {
	raw := `{"parts":[{"label":"back yoke","regions":{"front":[1],"back":[1,2]}},{"label":"collar","regions":{"front":[2]}}]}`
	parts, _, ok := parseDesignPartsCard(raw, []designPartsCardView{{View: "back", Count: 2}})
	if !ok || len(parts) != 1 || !reflect.DeepEqual(parts["back"], []entity.DesignPartGroup{{Label: "back yoke", Regions: []int{1, 2}, PartKey: "back-yoke"}}) {
		t.Fatalf("got %+v ok=%v", parts, ok)
	}
	// The collar has nothing on the side asked about: no part survives → unusable only when none does.
	if _, _, ok := parseDesignPartsCard(`{"parts":[{"label":"collar","regions":{"front":[2]}}]}`,
		[]designPartsCardView{{View: "back", Count: 2}}); ok {
		t.Fatal("no part on the asked sides must be unusable")
	}
}

func TestParseDesignPartsCardUnusable(t *testing.T) {
	for name, raw := range map[string]string{
		"prose":           "I cannot see the pictures.",
		"no parts key":    `{"groups":[]}`,
		"empty parts":     `{"parts":[]}`,
		"per-side shape":  `{"parts":[{"label":"x","regions":[1,2]}]}`,
		"all off range":   `{"parts":[{"label":"x","regions":{"front":[0,7,-1]}}]}`,
		"unknown views":   `{"parts":[{"label":"x","regions":{"top":[1]}}]}`,
		"only unnamed":    `{"parts":[{"label":"unnamed","regions":{"front":[1]}}]}`,
		"non-int regions": `{"parts":[{"label":"x","regions":{"front":[1.5,"a"]}}]}`,
	} {
		if _, _, ok := parseDesignPartsCard(raw, cardViews4); ok {
			t.Fatalf("%s: must be unusable", name)
		}
	}
}

func TestParseDesignPartsCardCapKeepsEveryRegion(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"parts":[`)
	for i := 1; i <= 45; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		b.WriteString(`{"label":"part ` + strconv.Itoa(i) + `","regions":{"front":[` + strconv.Itoa(i) + `],"back":[` + strconv.Itoa(i) + `]}}`)
	}
	b.WriteString(`]}`)
	views := []designPartsCardView{{View: "front", Count: 50}, {View: "back", Count: 40}}
	parts, _, ok := parseDesignPartsCard(b.String(), views)
	if !ok {
		t.Fatal("must be usable")
	}
	for _, v := range views {
		got := parts[v.View]
		if len(got) > entity.DesignPartsMaxParts {
			t.Fatalf("%s: %d groups", v.View, len(got))
		}
		seen := map[int]int{}
		for _, p := range got {
			for _, r := range p.Regions {
				seen[r]++
			}
		}
		for n := 1; n <= v.Count; n++ {
			if seen[n] != 1 {
				t.Fatalf("%s: region %d is in %d groups", v.View, n, seen[n])
			}
		}
	}
	// back: 40 parts claim all 40 regions, no unnamed. front: 40 parts + 10 lost → the 40th yields.
	if last := parts["front"][len(parts["front"])-1]; last.PartKey != "unnamed-front" || len(last.Regions) != 11 {
		t.Fatalf("front's last group: %+v", last)
	}
	if last := parts["back"][len(parts["back"])-1]; last.PartKey != "part-40" {
		t.Fatalf("back's last group: %+v", last)
	}
}

// The live answer of 2026-10-04 (sonnet-5.5, the shirt's four sides, prose + fence) parses with one
// key per physical part across the sides.
func TestParseDesignPartsCardLiveShape(t *testing.T) {
	raw := "Reasoning from the pictures:\n\nFront view: the collar is regions 1, 2, 3.\n\n```json\n" + `{"parts":[
{"label":"collar","regions":{"front":[1,2,3],"back":[1],"side_l":[1],"side_r":[1]}},
{"label":"back yoke","regions":{"front":[5,6],"back":[2],"side_l":[2],"side_r":[2]}},
{"label":"left front body","regions":{"front":[8],"side_l":[3]}},
{"label":"right sleeve","regions":{"front":[12,11],"back":[4],"side_r":[5,8]}},
{"label":"left sleeve","regions":{"front":[13,10],"back":[5],"side_l":[5,8]}}
],"split_needed":[]}` + "\n```"
	views := []designPartsCardView{{View: "front", Count: 21}, {View: "back", Count: 14}, {View: "side_l", Count: 12}, {View: "side_r", Count: 12}}
	parts, _, ok := parseDesignPartsCard(raw, views)
	if !ok {
		t.Fatal("must be usable")
	}
	keysOn := func(view string) map[string]bool {
		out := map[string]bool{}
		for _, p := range parts[view] {
			out[p.PartKey] = true
		}
		return out
	}
	for _, v := range views {
		if !keysOn(v.View)["collar"] || !keysOn(v.View)["back-yoke"] {
			t.Fatalf("%s lacks the collar or the yoke: %+v", v.View, parts[v.View])
		}
	}
	if !keysOn("side_l")["left-sleeve"] || keysOn("side_l")["right-sleeve"] || !keysOn("side_r")["right-sleeve"] {
		t.Fatalf("the sides must carry their own sleeve: %+v / %+v", parts["side_l"], parts["side_r"])
	}
}

func TestDesignPartsSlug(t *testing.T) {
	for in, want := range map[string]string{
		"left sleeve":       "left-sleeve",
		"  back -- yoke ":   "back-yoke",
		"pocket flap (l)":   "pocket-flap-l",
		"воротник стойка":   "воротник-стойка",
		"!!!":               "part",
		"collar-stand 2":    "collar-stand-2",
		"front/back panels": "front-back-panels",
	} {
		if got := designPartsSlug(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
}

// The prompt golden: the topology rules the client's one-gesture painting relies on.
func TestDesignPartsCardPrompt(t *testing.T) {
	for _, must := range []string{
		"ONE garment",
		"numbering restarts at 1 on every picture",
		"PHYSICAL parts",
		"One physical part is ONE entry",
		"WEARER'S left and right, never the viewer's",
		"A side view shows the parts of THAT side",
		"exactly one part",
		"lowercase, at most 3 words",
		`"split_needed"`,
		`"regions":{"front":[1,2],"back":[1],"side_l":[1],"side_r":[1]}`,
		"Answer with JSON only",
	} {
		if !strings.Contains(designPartsCardSystemPrompt, must) {
			t.Fatalf("the system prompt lost %q", must)
		}
	}
	got := designPartsCardUserPrompt([]designPartsCardView{{View: "front", Count: 21}, {View: "side_l", Count: 12}})
	want := "The garment has 2 views; the pictures follow in this order:\n" +
		"image 1: front view flat (key \"front\"), regions 1..21\n" +
		"image 2: left side view flat (key \"side_l\"), regions 1..12\n" +
		"List the garment's physical parts, each with its region numbers on every view where it is visible."
	if got != want {
		t.Fatalf("user prompt:\n got %q\nwant %q", got, want)
	}
}

func TestDesignPartsCardFlightKey(t *testing.T) {
	a := designPartsCardFlightKey(7, []designPartsCardView{{View: "front", Base: 1}, {View: "back", Base: 2}}, "r")
	b := designPartsCardFlightKey(7, []designPartsCardView{{View: "back", Base: 2}, {View: "front", Base: 1}}, "r")
	if a != b || a != "7|back:2,front:1|r" {
		t.Fatalf("%q vs %q", a, b)
	}
}

// The doors before money: a Server with no AI router (s.ai nil) must answer every one of them
// without reaching it.
func TestSuggestDesignPartsCardDoorsBeforeMoney(t *testing.T) {
	const card = 7
	newSrv := func(t *testing.T) (*Server, *mocks.MockDesign) {
		repo := mocks.NewMockRepository(t)
		design := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(design).Maybe()
		return &Server{repo: repo}, design
	}
	req := func() *pb_admin.SuggestDesignPartsCardRequest {
		return &pb_admin.SuggestDesignPartsCardRequest{TechCardId: card, AlgoRev: "r2", Views: []*pb_admin.DesignPartsViewInput{
			{View: "front", BaseMediaId: 11, MarksMediaId: 12, RegionCount: 9},
			{View: "back", BaseMediaId: 21, MarksMediaId: 22, RegionCount: 5},
		}}
	}
	flats := map[string]int{"front": 11, "back": 21, "side_l": 31}
	code := func(err error) codes.Code { return status.Code(err) }
	row := func(view string, base int, key string) *entity.DesignPartsSuggestion {
		return &entity.DesignPartsSuggestion{View: view, BaseMediaId: base, AlgoRev: "r2", Model: "m",
			Parts: []entity.DesignPartGroup{{Label: "collar", Regions: []int{1}, PartKey: key}}}
	}

	t.Run("arguments", func(t *testing.T) {
		srv, _ := newSrv(t)
		for name, mut := range map[string]func(*pb_admin.SuggestDesignPartsCardRequest){
			"card":     func(r *pb_admin.SuggestDesignPartsCardRequest) { r.TechCardId = 0 },
			"algo":     func(r *pb_admin.SuggestDesignPartsCardRequest) { r.AlgoRev = " " },
			"algo-len": func(r *pb_admin.SuggestDesignPartsCardRequest) { r.AlgoRev = strings.Repeat("r", 33) },
			"no views": func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views = nil },
			"five": func(r *pb_admin.SuggestDesignPartsCardRequest) {
				r.Views = append(r.Views, r.Views[0], r.Views[0], r.Views[0])
			},
			"view":      func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views[1].View = "detail" },
			"twice":     func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views[1].View = "front" },
			"base":      func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views[1].BaseMediaId = 0 },
			"marks":     func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views[0].MarksMediaId = 0 },
			"nil views": func(r *pb_admin.SuggestDesignPartsCardRequest) { r.Views = []*pb_admin.DesignPartsViewInput{{}} },
		} {
			r := req()
			mut(r)
			if _, err := srv.SuggestDesignPartsCard(context.Background(), r); code(err) != codes.InvalidArgument {
				t.Fatalf("%s: %v", name, err)
			}
		}
		for _, n := range []int32{1, 61} {
			r := req()
			r.Views[1].RegionCount = n
			_, err := srv.SuggestDesignPartsCard(context.Background(), r)
			if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designPartsTooManyMsg) {
				t.Fatalf("region_count %d: %v", n, err)
			}
		}
	})
	t.Run("one flat changed", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(map[string]int{"front": 11, "back": 99}, nil)
		_, err := srv.SuggestDesignPartsCard(context.Background(), req())
		if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designPartsFlatChangedMsg) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no card", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(nil, entity.ErrDesignNotFound)
		if _, err := srv.SuggestDesignPartsCard(context.Background(), req()); code(err) != codes.NotFound {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("cache hit on every side", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(flats, nil)
		design.EXPECT().GetPartsSuggestion(mock.Anything, card, "front", 11, "r2").Return(row("front", 11, "collar"), nil)
		design.EXPECT().GetPartsSuggestion(mock.Anything, card, "back", 21, "r2").Return(row("back", 21, "collar"), nil)
		resp, err := srv.SuggestDesignPartsCard(context.Background(), req())
		if err != nil || !resp.GetCached() || len(resp.GetSuggestions()) != 2 {
			t.Fatalf("got %+v %v", resp, err)
		}
		if s := resp.GetSuggestions(); s[0].GetView() != "front" || s[1].GetView() != "back" ||
			s[1].GetParts()[0].GetPartKey() != "collar" {
			t.Fatalf("order or key: %+v", s)
		}
	})
	for name, back := range map[string]*entity.DesignPartsSuggestion{
		"one side missing":         nil,
		"a per-side row (no keys)": row("back", 21, ""),
	} {
		t.Run(name+" is a miss", func(t *testing.T) {
			srv, design := newSrv(t)
			design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(flats, nil)
			design.EXPECT().GetPartsSuggestion(mock.Anything, card, "front", 11, "r2").Return(row("front", 11, "collar"), nil)
			design.EXPECT().GetPartsSuggestion(mock.Anything, card, "back", 21, "r2").Return(back, nil)
			_, err := srv.SuggestDesignPartsCard(context.Background(), req())
			if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designGenerationDisabledMsg) {
				t.Fatalf("a miss must reach the generation gate (off here): %v", err)
			}
		})
	}
	t.Run("force skips the cache", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(flats, nil)
		r := req()
		r.Force = true
		if _, err := srv.SuggestDesignPartsCard(context.Background(), r); code(err) != codes.FailedPrecondition {
			t.Fatalf("got %v", err)
		}
	})
}
