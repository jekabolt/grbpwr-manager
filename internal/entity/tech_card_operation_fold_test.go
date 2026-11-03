package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func foldLinkRefs() map[string]TechCardFoldOrderRef {
	return map[string]TechCardFoldOrderRef{
		"F1": {PieceLineKey: "FLAP_L", Seq: 1},
		"F2": {PieceLineKey: "flap_l ", Seq: 2}, // one piece however it is spelled
		"F3": {PieceLineKey: "FLAP_L", Seq: 3},
		"P1": {PieceLineKey: "POCKET", Seq: 1},
		"P2": {PieceLineKey: "POCKET", Seq: 2},
	}
}

func TestCheckTechCardFoldLinksAdmits(t *testing.T) {
	cases := map[string][][]string{
		"no steps":                              nil,
		"no links":                              {nil, {}, nil},
		"a piece in order":                      {{"F1"}, {"F2"}, {"F3"}},
		"gaps between steps":                    {{"F1"}, nil, nil, {"F3"}},
		"two folds share a step":                {{"F1", "F2"}},
		"a shared step in either order":         {{"F2", "F1"}, {"F3"}},
		"a fold with no step takes no part":     {{"F1"}, {"F3"}},
		"another piece does not order this one": {{"P2", "F1"}, {"F2"}},
		"lower case normalises":                 {{"f1"}, {" f2 "}},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			require.Empty(t, CheckTechCardFoldLinks(steps, foldLinkRefs()))
		})
	}
}

func TestCheckTechCardFoldLinksProblems(t *testing.T) {
	cases := []struct {
		name  string
		steps [][]string
		want  []TechCardFoldLinkProblem
	}{
		{"unknown key", [][]string{{"F1", "ZZ"}},
			[]TechCardFoldLinkProblem{{Step: 0, Pos: 1, Key: "ZZ", Kind: TechCardFoldLinkUnknown, OtherStep: -1}}},
		{"named twice on a step", [][]string{{"F1", "f1"}},
			[]TechCardFoldLinkProblem{{Step: 0, Pos: 1, Key: "F1", Kind: TechCardFoldLinkDuplicate, OtherStep: -1}}},
		{"one fold on two steps", [][]string{{"F1"}, nil, {"F1"}},
			[]TechCardFoldLinkProblem{{Step: 2, Pos: 0, Key: "F1", Kind: TechCardFoldLinkTwoSteps, OtherStep: 0}}},
		{"a larger seq on an earlier step", [][]string{{"F2"}, {"F1"}},
			[]TechCardFoldLinkProblem{{Step: 1, Pos: 0, Key: "F1", Kind: TechCardFoldLinkOrder, OtherStep: 0, OtherKey: "F2"}}},
		{"the reversal is judged against the largest earlier seq", [][]string{{"F1", "F3"}, {"F2"}},
			[]TechCardFoldLinkProblem{{Step: 1, Pos: 0, Key: "F2", Kind: TechCardFoldLinkOrder, OtherStep: 0, OtherKey: "F3"}}},
		{"a cut link does not order the rest", [][]string{{"F1"}, {"F1", "F2"}},
			[]TechCardFoldLinkProblem{{Step: 1, Pos: 0, Key: "F1", Kind: TechCardFoldLinkTwoSteps, OtherStep: 0}}},
		{"each piece is judged alone", [][]string{{"P2", "F1"}, {"P1", "F2"}},
			[]TechCardFoldLinkProblem{{Step: 1, Pos: 0, Key: "P1", Kind: TechCardFoldLinkOrder, OtherStep: 0, OtherKey: "P2"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, CheckTechCardFoldLinks(c.steps, foldLinkRefs()))
		})
	}
}

// What CutTechCardFoldLinks leaves passes the check again; a step left with nothing is nil.
func TestCutTechCardFoldLinks(t *testing.T) {
	steps := [][]string{{"f2", "ZZ"}, {"F1"}, {"F2", "F3"}, {"P1"}}
	got, problems := CutTechCardFoldLinks(steps, foldLinkRefs())
	require.Len(t, problems, 3)
	require.Equal(t, [][]string{{"F2"}, nil, {"F3"}, {"P1"}}, got)
	require.Empty(t, CheckTechCardFoldLinks(got, foldLinkRefs()))
	require.Equal(t, [][]string{{"f2", "ZZ"}, {"F1"}, {"F2", "F3"}, {"P1"}}, steps, "the input is left alone")
}
