package entity

import (
	"strings"
	"testing"
)

func TestDesignJoinIsNegation(t *testing.T) {
	for s, want := range map[string]bool{
		"no sleeves":                    true,
		"No back neckline":              true,
		"- none on the back":            true,
		"without a collar":              true,
		"not a single pocket":           true,
		"nothing above the neck points": true,
		"collar and placket edges are not cleanly finished; they are deliberately raw and frayed": false,
		"raw hem":     false,
		"notch at CF": false,
		"":            false,
	} {
		if got := DesignJoinIsNegation(s); got != want {
			t.Errorf("%q: got %v", s, got)
		}
	}
}

func TestDesignJoinPoint(t *testing.T) {
	p, ok := DesignJoinPoint("UA_R..MB_R:0.3")
	if !ok || p[0] > -0.15 || p[0] < -0.17 {
		t.Fatalf("got %v %v", p, ok)
	}
	for _, bad := range []string{"", "MOON", "NP_L..SP_L", "NP_L..SP_L:1.5", "NP_L..X:0.5", strings.Repeat("A", 50)} {
		if IsDesignJoinLandmark(bad) {
			t.Errorf("%q must not be a landmark", bad)
		}
	}
	if DesignJoinSideOf("UA_R..MB_R:0.3") != "R" || DesignJoinSideOf("CBN..UB_C:0.6") != "" || DesignJoinSideOf("NP_L") != "L" {
		t.Fatal("side_of")
	}
}

// TestSanitizeDesignJoinsDoc — the caps, the unique ids, the dead references, the layers.
func TestSanitizeDesignJoinsDoc(t *testing.T) {
	var items []DesignJoinItem
	for i := 0; i < DesignJoinsMaxItems+5; i++ {
		items = append(items, DesignJoinItem{ID: "s", Kind: "seam", From: "UA_L", To: "HEM_L", Text: strings.Repeat("x", 400),
			ContinuesInto: []string{"ghost", "s_2"}, Layer: 7, Visibility: "weird"})
	}
	items = append([]DesignJoinItem{{ID: "o", Kind: "opening", BoundedBy: []string{"ghost"}}}, items...)
	d := SanitizeDesignJoinsDoc(DesignJoinsDoc{
		Layers: []DesignJoinLayer{{Index: 0, Name: "outer", Sheer: true}, {Index: 0, Name: "dup"}, {Index: 9}},
		Items:  items,
	})
	if len(d.Layers) != 1 || !d.Layers[0].Sheer {
		t.Fatalf("layers: %+v", d.Layers)
	}
	if len(d.Items) != DesignJoinsMaxItems-1 {
		t.Fatalf("an opening bounded by nothing is dropped and the list is capped: %d", len(d.Items))
	}
	first, second := d.Items[0], d.Items[1]
	if first.ID != "s" || second.ID != "s_2" || len([]rune(first.Text)) != DesignJoinsMaxTextRunes {
		t.Fatalf("ids / text: %+v", first)
	}
	if len(first.ContinuesInto) != 1 || first.ContinuesInto[0] != "s_2" {
		t.Fatalf("a reference to a missing id is dropped: %v", first.ContinuesInto)
	}
	if first.Layer != 0 || first.Visibility != DesignJoinVisible || first.Side != "L" {
		t.Fatalf("layer clamp / visibility / side: %+v", first)
	}
}

// TestParseDesignJoinsAnswerMapsImagesToMedia — image numbers become media ids; old verdict words
// are read; fenced JSON is found.
func TestParseDesignJoinsAnswerMapsImagesToMedia(t *testing.T) {
	raw := "Here:\n```json\n" + `{"consistency":{"consistent":false,"note":"three shirts","groups":[{"images":[1],"what":"black linen"},{"images":["2",9],"what":"grey check"}],"keep":[1]},
	"layers":[{"index":0,"name":"outer","sheer":true,"face":"FRONT"},{"index":"1","name":"inner","sheer":false}],"uncertain":["is the V a separate panel"],
	"items":[{"id":"p","kind":"pocket","anchor":"CHEST_L"},{"id":"v","kind":"edge","path":["NP_L..SP_L:0.35","BUST_C","NP_R..SP_R:0.35"],"layer":1,"visibility":"through"}],
	"absent":["no yoke"],"absences":["raw edges"]}` + "\n```"
	a, ok := ParseDesignJoinsAnswer(raw)
	if !ok {
		t.Fatal("must parse")
	}
	d := a.Doc()
	if len(d.Items) != 2 || d.Items[0].From != "CHEST_L" || d.Items[1].Via[0] != "BUST_C" || d.Items[1].Visibility != DesignJoinThrough || d.Items[1].Layer != 1 {
		t.Fatalf("doc: %+v", d)
	}
	if d.Layers[0].Face != "front" || len(d.Uncertain) != 1 {
		t.Fatalf("face / uncertain: %+v %v", d.Layers, d.Uncertain)
	}
	if len(d.Absences) != 1 {
		t.Fatalf("absences: %v", d.Absences)
	}
	c := a.ConsistencyFor([]int{101, 102, 103})
	if c.Consistent || len(c.KeepMediaIDs) != 1 || c.KeepMediaIDs[0] != 101 || len(c.Groups) != 2 || c.Groups[1].MediaIDs[0] != 102 || len(c.Groups[1].MediaIDs) != 1 {
		t.Fatalf("consistency: %+v", c)
	}
	old, _ := ParseDesignJoinsAnswer(`{"consistency":{"same_garment":true,"notes":"ok","trust":[2]},"items":[]}`)
	if c := old.ConsistencyFor([]int{5, 6}); !c.Consistent || c.KeepMediaIDs[0] != 6 || c.Note != "ok" {
		t.Fatalf("old words: %+v", c)
	}
	if _, ok := ParseDesignJoinsAnswer("no json here"); ok {
		t.Fatal("prose is not an answer")
	}
}
