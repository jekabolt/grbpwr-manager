package admin

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── house style (examples) for part 5 ───

func skeletonAITestExample() *pb_admin.AssemblySkeletonExample {
	return &pb_admin.AssemblySkeletonExample{
		Label: "SS26-001 Overshirt (shirt)",
		Units: []*pb_admin.AssemblySkeletonExampleUnit{
			{Name: "Collar", Parts: []string{"collar top", "collar under"}},
			{Name: "Collar with stand", Parts: []string{"Collar", "stand"}},
			{Name: "Body", Parts: []string{"front left", "front right", "back"}},
		},
	}
}

func TestSkeletonAIInputOfExamples(t *testing.T) {
	long := strings.Repeat("ж", skeletonAIMaxExampleRunes+1)
	cases := map[string]func(r *pb_admin.SuggestAssemblySkeletonRequest){
		"5 examples": func(r *pb_admin.SuggestAssemblySkeletonRequest) {
			for len(r.Examples) < skeletonAIMaxExamples+1 {
				r.Examples = append(r.Examples, skeletonAITestExample())
			}
		},
		"61 units": func(r *pb_admin.SuggestAssemblySkeletonRequest) {
			for len(r.Examples[0].Units) < skeletonAIMaxExampleUnits+1 {
				r.Examples[0].Units = append(r.Examples[0].Units, &pb_admin.AssemblySkeletonExampleUnit{Name: "u", Parts: []string{"p"}})
			}
		},
		"17 parts": func(r *pb_admin.SuggestAssemblySkeletonRequest) {
			for len(r.Examples[0].Units[0].Parts) < skeletonAIMaxExampleParts+1 {
				r.Examples[0].Units[0].Parts = append(r.Examples[0].Units[0].Parts, "p")
			}
		},
		"no parts":    func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Units[0].Parts = nil },
		"empty label": func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Label = "  " },
		"empty name":  func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Units[1].Name = "" },
		"empty part":  func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Units[1].Parts[1] = " " },
		"long label":  func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Label = long },
		"long name":   func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Units[0].Name = long },
		"long part":   func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.Examples[0].Units[0].Parts[0] = long },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := skeletonAITestRequest()
			r.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
			mutate(r)
			_, err := skeletonAIInputOf(r)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
	// Control: the bounds themselves are fine.
	r := skeletonAITestRequest()
	for len(r.Examples) < skeletonAIMaxExamples {
		r.Examples = append(r.Examples, skeletonAITestExample())
	}
	for len(r.Examples[0].Units) < skeletonAIMaxExampleUnits {
		r.Examples[0].Units = append(r.Examples[0].Units, &pb_admin.AssemblySkeletonExampleUnit{Name: "u", Parts: []string{"p"}})
	}
	for len(r.Examples[0].Units[0].Parts) < skeletonAIMaxExampleParts {
		r.Examples[0].Units[0].Parts = append(r.Examples[0].Units[0].Parts, strings.Repeat("ж", skeletonAIMaxExampleRunes))
	}
	in, err := skeletonAIInputOf(r)
	require.NoError(t, err)
	require.Len(t, in.Examples, skeletonAIMaxExamples)
}

func TestSkeletonAIDigestCoversExamples(t *testing.T) {
	a, b := skeletonAITestRequest(), skeletonAITestRequest()
	b.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
	require.NotEqual(t, skeletonAIDigest(a), skeletonAIDigest(b))
}

// Auto mode (no examples): the house style is picked from the card, so the card is in the key.
// Explicit examples: the card does not change the question, so it stays out (another card, same
// skeleton and examples → a cache hit). force never counts.
func TestSkeletonAIDigestCardOnlyInAutoMode(t *testing.T) {
	withCard := func(id int32, examples bool) *pb_admin.SuggestAssemblySkeletonRequest {
		r := skeletonAITestRequest()
		r.TechCardId = id
		if examples {
			r.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
		}
		return r
	}
	require.NotEqual(t, skeletonAIDigest(withCard(0, false)), skeletonAIDigest(withCard(7, false)), "auto: card 0 vs card 7")
	require.NotEqual(t, skeletonAIDigest(withCard(7, false)), skeletonAIDigest(withCard(8, false)), "auto: another card")
	require.Equal(t, skeletonAIDigest(withCard(7, false)), skeletonAIDigest(withCard(7, false)))
	require.Equal(t, skeletonAIDigest(withCard(0, true)), skeletonAIDigest(withCard(7, true)), "explicit: the card is cleared")
	require.Equal(t, skeletonAIDigest(withCard(7, true)), skeletonAIDigest(withCard(8, true)))
	forced := withCard(7, false)
	forced.Force = true
	require.Equal(t, skeletonAIDigest(withCard(7, false)), skeletonAIDigest(forced), "force never counts")
}

// End to end: card 0 (no house style) is answered first and cached; card 7 with the same body must
// not get that answer — it calls the provider again, with its house style in the prompt.
func TestSuggestAssemblySkeletonAutoModeCacheIsPerCard(t *testing.T) {
	rig := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().ListAssemblyExampleCards(mock.Anything, 7, skeletonAIAutoExamples).Return(skeletonAIExampleCards(), nil).Once()
	rig.s.repo = repo

	none := skeletonAITestRequest()
	none.TechCardId = 0
	_, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), none)
	require.NoError(t, err)
	require.NotContains(t, (*rig.recorded)[0].User, "House style")

	res, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), skeletonAITestRequest()) // card 7
	require.NoError(t, err)
	require.False(t, res.Cached, "card 7 never gets card 0's answer")
	require.Equal(t, 2, rig.providerCalls())
	require.Contains(t, (*rig.recorded)[1].User, "House style")

	// Explicit examples: another card with the same body and examples is a cache hit.
	ex := func(id int32) *pb_admin.SuggestAssemblySkeletonRequest {
		r := skeletonAITestRequest()
		r.TechCardId = id
		r.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
		return r
	}
	_, err = rig.s.SuggestAssemblySkeleton(adminCtx("alice"), ex(7))
	require.NoError(t, err)
	hit, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), ex(8))
	require.NoError(t, err)
	require.True(t, hit.Cached)
	require.Equal(t, 3, rig.providerCalls())
}

func TestSkeletonAIPromptHouseStyle(t *testing.T) {
	r := skeletonAITestRequest()
	r.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
	in, err := skeletonAIInputOf(r)
	require.NoError(t, err)
	user := skeletonAIUserPrompt(in)
	block := "\nHouse style — assembly trees this workshop's technologist made for other garments:\n" +
		`Example 1 ("SS26-001 Overshirt (shirt)"):` + "\n" +
		`- "Collar" = "collar top" + "collar under"` + "\n" +
		`- "Collar with stand" = "Collar" + "stand"` + "\n" +
		`- "Body" = "front left" + "front right" + "back"` + "\n"
	require.Contains(t, user, block)
	require.Less(t, strings.Index(user, "House style"), strings.Index(user, "\nPieces ("), "the examples come before the pieces")

	without := skeletonAIUserPrompt(skeletonAITestInput(t))
	require.NotContains(t, without, "House style")
	require.NotContains(t, without, "Example 1")
	require.Contains(t, skeletonAISystemPrompt, "When EXAMPLES of this workshop's own assembly trees are given, follow their house style")
	require.Contains(t, skeletonAISystemPrompt, "never copy their pieces or names; use only this request's piece keys")
}

// Other cards' joins become examples by NAME: pieces by their names, units by the name of the earlier
// unit that made them; a join that cannot be named is dropped (and so is what is built on it).
func TestSkeletonAIExamplesOfNamesTheInputs(t *testing.T) {
	piece := func(name string) entity.AssemblyExampleInput {
		return entity.AssemblyExampleInput{PieceName: name, PieceLineKey: "K_" + name}
	}
	unit := func(key string) entity.AssemblyExampleInput { return entity.AssemblyExampleInput{UnitKey: key} }
	cards := []entity.AssemblyExampleCard{{
		TechCardID: 11, StyleNumber: "SS26-015", Name: "Wool trousers", CategoryName: "trousers", SameCategory: true,
		Joins: []entity.AssemblyExampleJoin{
			{OutputUnitKey: "WB", OutputUnitName: "Waistband", Inputs: []entity.AssemblyExampleInput{piece("waistband"), piece("waistband facing")}},
			{OutputUnitKey: "LEG_L", OutputUnitName: "", Inputs: []entity.AssemblyExampleInput{piece("front left"), piece("back left")}},
			{OutputUnitKey: "SHELL", OutputUnitName: "Shell", Inputs: []entity.AssemblyExampleInput{unit("LEG_L"), piece("front right")}},
			// re-makes SHELL: inherits the name
			{OutputUnitKey: "SHELL", Inputs: []entity.AssemblyExampleInput{unit("SHELL"), unit("WB")}},
			// a nameless piece → dropped; what is built on it → dropped too
			{OutputUnitKey: "POCKET", OutputUnitName: "Pocket", Inputs: []entity.AssemblyExampleInput{piece(""), piece("pocket bag")}},
			{OutputUnitKey: "SHELL2", OutputUnitName: "Shell with pocket", Inputs: []entity.AssemblyExampleInput{unit("SHELL"), unit("POCKET")}},
			// a unit no join made → dropped; no inputs → dropped
			{OutputUnitKey: "X", OutputUnitName: "Ghost", Inputs: []entity.AssemblyExampleInput{unit("CARD_UNIT"), piece("belt")}},
			{OutputUnitKey: "Y", OutputUnitName: "Empty"},
		},
	}}
	got := skeletonAIExamplesOf(cards)
	require.Len(t, got, 1)
	require.Equal(t, "SS26-015 Wool trousers (trousers)", got[0].Label)
	require.Equal(t, []skeletonAIExampleUnit{
		{Name: "Waistband", Parts: []string{"waistband", "waistband facing"}},
		{Name: "LEG_L", Parts: []string{"front left", "back left"}},
		{Name: "Shell", Parts: []string{"LEG_L", "front right"}},
		{Name: "Shell", Parts: []string{"Shell", "Waistband"}},
	}, got[0].Units)
}

func TestSkeletonAIExamplesOfBoundsAndDropsEmptyCards(t *testing.T) {
	many := make([]entity.AssemblyExampleInput, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, entity.AssemblyExampleInput{PieceName: fmt.Sprintf("p%d", i)})
	}
	var joins []entity.AssemblyExampleJoin
	for i := 0; i < skeletonAIMaxExampleUnits+5; i++ {
		joins = append(joins, entity.AssemblyExampleJoin{OutputUnitKey: fmt.Sprintf("U%d", i), OutputUnitName: strings.Repeat("n", 100), Inputs: many})
	}
	nameless := entity.AssemblyExampleCard{TechCardID: 2, Name: "x", Joins: []entity.AssemblyExampleJoin{{OutputUnitKey: "A", Inputs: []entity.AssemblyExampleInput{{PieceName: ""}}}}}
	big := entity.AssemblyExampleCard{TechCardID: 1, Joins: joins}
	got := skeletonAIExamplesOf([]entity.AssemblyExampleCard{nameless, big, big, big, big})
	require.Len(t, got, skeletonAIAutoExamples, "a card with nothing nameable is no example; at most 3")
	require.Equal(t, "another garment", got[0].Label)
	require.Len(t, got[0].Units, skeletonAIMaxExampleUnits)
	require.Len(t, got[0].Units[0].Parts, skeletonAIMaxExampleParts)
	require.Len(t, []rune(got[0].Units[0].Name), skeletonAIMaxExampleRunes)
}

func skeletonAIExampleCards() []entity.AssemblyExampleCard {
	j := func(key, name string, pieces ...string) entity.AssemblyExampleJoin {
		in := make([]entity.AssemblyExampleInput, len(pieces))
		for i, p := range pieces {
			in[i] = entity.AssemblyExampleInput{PieceName: p}
		}
		return entity.AssemblyExampleJoin{OutputUnitKey: key, OutputUnitName: name, Inputs: in}
	}
	return []entity.AssemblyExampleCard{
		{TechCardID: 21, StyleNumber: "SS26-020", Name: "Poplin shirt", CategoryName: "shirt", SameCategory: true,
			Joins: []entity.AssemblyExampleJoin{j("C", "Collar", "collar top", "collar under"), j("B", "Body", "front", "back")}},
		{TechCardID: 22, StyleNumber: "FW25-003", Name: "Coat", CategoryName: "coat",
			Joins: []entity.AssemblyExampleJoin{j("L", "Lining", "lining front", "lining back")}},
	}
}

// The request has no examples: the server asks the store for 3 OTHER cards (the request's card is the
// one excluded; same category and ≥ 4 joins are the store's query) and shows them in the store's
// order — the same-category card first.
func TestSuggestAssemblySkeletonLoadsHouseStyle(t *testing.T) {
	rig := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().ListAssemblyExampleCards(mock.Anything, 7, skeletonAIAutoExamples).Return(skeletonAIExampleCards(), nil).Once()
	rig.s.repo = repo

	res, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), skeletonAITestRequest())
	require.NoError(t, err)
	require.Len(t, res.Order, 4)
	user := (*rig.recorded)[0].User
	require.Contains(t, user, `Example 1 ("SS26-020 Poplin shirt (shirt)"):`)
	require.Contains(t, user, `- "Collar" = "collar top" + "collar under"`)
	require.Contains(t, user, `Example 2 ("FW25-003 Coat (coat)"):`)

	// A cache hit reads nothing (the mock allows the one call only).
	again := skeletonAITestRequest()
	hit, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), again)
	require.NoError(t, err)
	require.True(t, hit.Cached)
}

// A failed read never fails the press: it goes on without examples.
func TestSuggestAssemblySkeletonHouseStyleFailureIsNoRefusal(t *testing.T) {
	rig := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().ListAssemblyExampleCards(mock.Anything, 7, skeletonAIAutoExamples).Return(nil, errors.New("db down"))
	rig.s.repo = repo

	res, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), skeletonAITestRequest())
	require.NoError(t, err)
	require.Len(t, res.Order, 4)
	require.NotContains(t, (*rig.recorded)[0].User, "House style")
}

// No store read when the request brings its own examples, or names no card (tech_card_id 0).
func TestSuggestAssemblySkeletonHouseStyleNotLoaded(t *testing.T) {
	for name, mutate := range map[string]func(r *pb_admin.SuggestAssemblySkeletonRequest){
		"own examples": func(r *pb_admin.SuggestAssemblySkeletonRequest) {
			r.Examples = []*pb_admin.AssemblySkeletonExample{skeletonAITestExample()}
		},
		"no card": func(r *pb_admin.SuggestAssemblySkeletonRequest) { r.TechCardId = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
			rig.s.repo = mocks.NewMockRepository(t) // strict: any store call fails the test
			r := skeletonAITestRequest()
			mutate(r)
			_, err := rig.s.SuggestAssemblySkeleton(adminCtx("alice"), r)
			require.NoError(t, err)
			user := (*rig.recorded)[0].User
			if len(r.Examples) > 0 {
				require.Contains(t, user, `Example 1 ("SS26-001 Overshirt (shirt)"):`)
			} else {
				require.NotContains(t, user, "House style")
			}
		})
	}
}
