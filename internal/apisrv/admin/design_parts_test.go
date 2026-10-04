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

func TestParseDesignPartsShapes(t *testing.T) {
	body := `{"parts":[{"label":"Left Sleeve","regions":[1,2]},{"label":"collar","regions":[3]}],"split_needed":[]}`
	for name, raw := range map[string]string{
		"object":       body,
		"fenced prose": "Sure:\n```json\n" + body + "\n```\n",
	} {
		parts, splits, ok := parseDesignParts(raw, 3)
		if !ok || len(parts) != 2 || parts[0].Label != "left sleeve" || len(splits) != 0 {
			t.Fatalf("%s: got %+v %+v ok=%v", name, parts, splits, ok)
		}
	}
	for name, raw := range map[string]string{
		"prose":          "I cannot see the picture.",
		"no parts key":   `{"groups":[{"label":"x","regions":[1]}]}`,
		"empty parts":    `{"parts":[]}`,
		"all off range":  `{"parts":[{"label":"x","regions":[0,9,-1]}]}`,
		"no regions":     `{"parts":[{"label":"x","regions":[]}]}`,
		"non-int region": `{"parts":[{"label":"x","regions":[1.5,"a"]}]}`,
	} {
		if _, _, ok := parseDesignParts(raw, 3); ok {
			t.Fatalf("%s: must be unusable", name)
		}
	}
}

func TestParseDesignPartsCleaning(t *testing.T) {
	raw := `{"parts":[
		{"label":"  Front   BODY ","regions":[1,"2",2,99,0]},
		{"label":"left sleeve","regions":[2,3]},
		{"label":"","regions":[4]},
		{"label":"` + strings.Repeat("x", 60) + `","regions":[5]}
	],"split_needed":[{"region":"3","why":"sleeve and body share it"},{"region":3,"why":"dup"},{"region":42,"why":"off"},
		{"region":5,"why":"` + strings.Repeat("y", 200) + `"}]}`
	parts, splits, ok := parseDesignParts(raw, 7)
	if !ok {
		t.Fatal("must be usable")
	}
	want := []entity.DesignPartGroup{
		{Label: "front body", Regions: []int{1, 2}},
		{Label: "left sleeve", Regions: []int{3}},           // 2 is the first part's
		{Label: "unnamed", Regions: []int{4, 6, 7}},         // the empty label, then the lost 6 and 7
		{Label: strings.Repeat("x", 40), Regions: []int{5}}, // cut to 40 runes
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts:\n got %+v\nwant %+v", parts, want)
	}
	if len(splits) != 2 || splits[0] != (entity.DesignPartSplit{Region: 3, Why: "sleeve and body share it"}) ||
		splits[1].Region != 5 || len([]rune(splits[1].Why)) != entity.DesignPartsMaxWhyRunes {
		t.Fatalf("splits: %+v", splits)
	}
}

func TestParseDesignPartsCapKeepsEveryRegion(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"parts":[`)
	for i := 1; i <= 45; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		b.WriteString(`{"label":"p` + string(rune('a'+i%26)) + `","regions":[` + strconv.Itoa(i) + `]}`)
	}
	b.WriteString(`]}`)
	parts, _, ok := parseDesignParts(b.String(), 50)
	if !ok || len(parts) != entity.DesignPartsMaxParts {
		t.Fatalf("got %d parts ok=%v", len(parts), ok)
	}
	last := parts[len(parts)-1]
	if last.Label != entity.DesignPartsUnnamed {
		t.Fatalf("the last part must be unnamed, got %q", last.Label)
	}
	seen := map[int]int{}
	for _, p := range parts {
		for _, r := range p.Regions {
			seen[r]++
		}
	}
	for n := 1; n <= 50; n++ {
		if seen[n] != 1 {
			t.Fatalf("region %d is in %d parts", n, seen[n])
		}
	}
}

// The prompt golden: the rules the FRONT↔BACK transfer and the cleaner rely on.
func TestDesignPartsPrompt(t *testing.T) {
	for _, must := range []string{
		"numbered regions",
		"exactly one part",
		"WEARER'S left and right, never the viewer's",
		"lowercase, at most 3 words",
		"same part seen on the front and on the back view gets the same label",
		`"split_needed"`,
		"Answer with JSON only",
	} {
		if !strings.Contains(designPartsSystemPrompt, must) {
			t.Fatalf("the system prompt lost %q", must)
		}
	}
	if got, want := designPartsUserPrompt(entity.DesignViewBack, 12),
		"This is the back view flat of the garment, cut into 12 numbered regions (1..12). Group every region number into named parts."; got != want {
		t.Fatalf("user prompt:\n got %q\nwant %q", got, want)
	}
	for _, v := range entity.DesignCardinalViews {
		if designPartsViewWords[v] == "" {
			t.Fatalf("view %q has no words", v)
		}
	}
}

// The doors before money: a Server with no AI router (s.ai nil) must answer every one of them
// without reaching it.
func TestSuggestDesignPartsDoorsBeforeMoney(t *testing.T) {
	const card = 7
	newSrv := func(t *testing.T) (*Server, *mocks.MockDesign) {
		repo := mocks.NewMockRepository(t)
		design := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(design).Maybe()
		return &Server{repo: repo}, design
	}
	req := func() *pb_admin.SuggestDesignPartsRequest {
		return &pb_admin.SuggestDesignPartsRequest{
			TechCardId: card, View: "front", BaseMediaId: 11, MarksMediaId: 12, RegionCount: 9, AlgoRev: "r1",
		}
	}
	code := func(err error) codes.Code { return status.Code(err) }

	t.Run("arguments", func(t *testing.T) {
		srv, _ := newSrv(t)
		for name, mut := range map[string]func(*pb_admin.SuggestDesignPartsRequest){
			"view":     func(r *pb_admin.SuggestDesignPartsRequest) { r.View = "detail" },
			"base":     func(r *pb_admin.SuggestDesignPartsRequest) { r.BaseMediaId = 0 },
			"marks":    func(r *pb_admin.SuggestDesignPartsRequest) { r.MarksMediaId = 0 },
			"algo":     func(r *pb_admin.SuggestDesignPartsRequest) { r.AlgoRev = "" },
			"algo-len": func(r *pb_admin.SuggestDesignPartsRequest) { r.AlgoRev = strings.Repeat("r", 33) },
		} {
			r := req()
			mut(r)
			if _, err := srv.SuggestDesignParts(context.Background(), r); code(err) != codes.InvalidArgument {
				t.Fatalf("%s: %v", name, err)
			}
		}
		for _, n := range []int32{1, 61} {
			r := req()
			r.RegionCount = n
			_, err := srv.SuggestDesignParts(context.Background(), r)
			if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designPartsTooManyMsg) {
				t.Fatalf("region_count %d: %v", n, err)
			}
		}
	})
	t.Run("flat changed", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(map[string]int{"front": 99}, nil)
		_, err := srv.SuggestDesignParts(context.Background(), req())
		if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designPartsFlatChangedMsg) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no card", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(nil, entity.ErrDesignNotFound)
		if _, err := srv.SuggestDesignParts(context.Background(), req()); code(err) != codes.NotFound {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("cache hit", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(map[string]int{"front": 11}, nil)
		design.EXPECT().GetPartsSuggestion(mock.Anything, card, "front", 11, "r1").Return(&entity.DesignPartsSuggestion{
			View: "front", BaseMediaId: 11, AlgoRev: "r1", Model: "m",
			Parts: []entity.DesignPartGroup{{Label: "collar", Regions: []int{1}}},
		}, nil)
		resp, err := srv.SuggestDesignParts(context.Background(), req())
		if err != nil || !resp.GetCached() || resp.GetSuggestion().GetParts()[0].GetLabel() != "collar" {
			t.Fatalf("got %+v %v", resp, err)
		}
	})
	t.Run("generation off after a miss", func(t *testing.T) {
		srv, design := newSrv(t)
		design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(map[string]int{"front": 11}, nil)
		design.EXPECT().GetPartsSuggestion(mock.Anything, card, "front", 11, "r1").Return(nil, nil)
		if _, err := srv.SuggestDesignParts(context.Background(), req()); err == nil {
			t.Fatal("generation is off: a miss must be refused")
		}
	})
}
