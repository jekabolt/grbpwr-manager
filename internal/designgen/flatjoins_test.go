package designgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func loadJoinsCase(t *testing.T, name string) entity.DesignJoinsDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "joins", name+"-joins.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := entity.ParseDesignJoinsAnswer(string(raw))
	if !ok {
		t.Fatalf("%s: the example list does not parse", name)
	}
	return a.Doc()
}

// TestJoinsCraftMatchesTheWinningPrompts — the join paragraphs of the flat prompt, built from the
// round-5 example lists (cards 38 and 49), are BYTE FOR BYTE the paragraphs of round 7's winning
// prompts (tmp/plans/flat-consistency/out/r7/c38/prompt-text-b2.txt, out/r7/c49/prompt-text-b.txt),
// plus one sentence per interior SHARP corner (a V point, a sleeve-end corner: r5's `sharp`, which the
// text route used to drop), and less ONE thing on card 49: the positive note round 7 turned into «Draw NOTHING for: collar and
// placket edges are … raw» — the cleaner drops it from the absences (the round-7 generator bug).
//
// MUTATIONS IT CATCHES: any wording drift of a sentence; the diagonal / crossing / no-back-neckline /
// pocket-side rules not firing or firing on the wrong garment; a positive absence surviving.
func TestJoinsCraftMatchesTheWinningPrompts(t *testing.T) {
	for _, c := range []string{"c38", "c49", "c2"} {
		t.Run(c, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "joins", c+"-golden.txt"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(joinsCraft(loadJoinsCase(t, c)), "\n\n")
			if got != string(want) {
				gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
				for i := 0; i < len(gl) || i < len(wl); i++ {
					var g, w string
					if i < len(gl) {
						g = gl[i]
					}
					if i < len(wl) {
						w = wl[i]
					}
					if g != w {
						t.Fatalf("line %d differs:\n got: %q\nwant: %q", i+1, g, w)
					}
				}
			}
		})
	}
}

// TestFlatCraftWithJoins — the list rides only a garment run, the side convention only with a side
// view, and the no-grey sentence on every flat run, before the owner's verbatim style.
func TestFlatCraftWithJoins(t *testing.T) {
	j := loadJoinsCase(t, "c38")
	p := runParams{Views: []string{"front", "back", "side_l", "side_r"}, Layout: layoutOne}
	got := flatCraftWith(p, nil, 3, &j)
	for _, want := range []string{flatSideFacing, "JOIN LIST", "CHECK EVERY VIEW AGAINST THESE BEFORE DRAWING:", flatNoTextNoGrey} {
		if !strings.Contains(got, want) {
			t.Fatalf("the prompt lacks %q", want)
		}
	}
	if strings.Index(got, flatNoTextNoGrey) > strings.Index(got, flatStyleGarment) {
		t.Fatal("the no-grey sentence must come before the owner's style paragraph")
	}
	if !strings.HasSuffix(got, flatStyleGarment+"\n\n"+flatExcludedGarment+"\n\n"+flatOutput) {
		t.Fatal("the owner's paragraphs must close the prompt verbatim")
	}

	plain := flatCraft(p, nil, 3)
	if strings.Contains(plain, "JOIN LIST") || strings.Contains(plain, flatSideFacing) {
		t.Fatal("a run without joins must not carry the list")
	}
	if !strings.Contains(plain, flatNoTextNoGrey) {
		t.Fatal("every flat run says no grey")
	}
	front := flatCraftWith(runParams{Views: []string{"front", "back"}, Layout: layoutOne}, nil, 3, &j)
	if strings.Contains(front, flatSideFacing) {
		t.Fatal("the side convention is said only when a side is drawn")
	}
	detail := flatCraftWith(runParams{Views: []string{"detail"}}, []string{"collar"}, 1, &j)
	if strings.Contains(detail, "JOIN LIST") {
		t.Fatal("a detail callout does not carry the garment's join list")
	}
}

// TestJoinsLayerSentences — the multi-layer case on Fable's confirmed list for card 38
// (out/layers/joins-A.json): the whole join block equals the one Fable tested live
// (out/layers/test1/prompt.txt — the V as one fine dashed line 4/4, no grey 4/4), less this port's two
// deliberate differences: the neckband (NP_R→NP_L, horizontal) is not called a diagonal strap, and the
// positive absence «inner V layer does not extend below BUST_C» is dropped by the cleaner.
func TestJoinsLayerSentences(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "joins", "c38-layers-golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	d := loadJoinsCase(t, "c38-layers")
	got := strings.Join(joinsCraft(d), "\n\n")
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("line %d differs:\n got: %q\nwant: %q", i+1, g, w)
			}
		}
	}
	if len(d.Uncertain) != 6 {
		t.Fatalf("uncertain: %v", d.Uncertain)
	}
	if got := joinsLayerSentences(loadJoinsCase(t, "c38")); len(got) != 0 {
		t.Fatalf("a single-layer list says no layer sentence: %v", got)
	}
	n := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{
		Layers: []entity.DesignJoinLayer{{Index: 0, Name: "outer"}, {Index: 1, Name: "lining"}},
		Items:  []entity.DesignJoinItem{{ID: "e", Kind: "edge", From: "HEM_L", To: "HEM_R", Layer: 1, Visibility: "through"}},
	})
	if n.Items[0].Visibility != entity.DesignJoinVisible {
		t.Fatal("through needs a sheer layer above")
	}
}

// TestJoinsHorizontalBandIsNotDiagonal — the one deliberate departure from r7: a waistband from side
// to side is not a diagonal strap.
func TestJoinsHorizontalBandIsNotDiagonal(t *testing.T) {
	d := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "wb", Kind: "waistband", From: "WL_L", Via: []string{"WF_C"}, To: "WL_R"},
	}})
	if s := strings.Join(joinsSentences(d), "\n"); strings.Contains(s, "DIAGONALLY") {
		t.Fatalf("a waistband became a diagonal:\n%s", s)
	}
}

// TestComposePromptCarriesFrozenJoins — the list reaches the prompt from the FROZEN snapshot key.
func TestComposePromptCarriesFrozenJoins(t *testing.T) {
	raw := `{"garment_note":"a top","joins":{"items":[{"id":"strap_L","kind":"strap","from":"NP_L","via":["CBN..UB_C:0.6"],"to":"UA_R..MB_R:0.3","visibility":"visible"}],"absences":["no sleeves"]}}`
	in := parseInputs(entity.RawJSON(raw))
	if in.Joins == nil || len(in.Joins.Items) != 1 {
		t.Fatal("the narrow reader must read inputs.joins")
	}
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	got := composePrompt(run, runParams{Views: []string{"front", "back"}, Layout: layoutOne}, in, nil)
	if !strings.Contains(got, "strap_L runs DIAGONALLY across the back") || !strings.Contains(got, "Draw NOTHING for: no sleeves.") {
		t.Fatalf("the frozen list did not reach the prompt:\n%s", got)
	}
}
