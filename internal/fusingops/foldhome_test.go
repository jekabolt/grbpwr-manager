package fusingops

import (
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestFoldHome(t *testing.T) {
	steps := []FoldStep{
		{Pieces: []string{"SLEEVE"}},                // 0: a sewing step of the sleeve
		{Owner: "FRONT", Pieces: []string{"FRONT"}}, // 1: the automatic step of the front
		{Owner: "BACK", Pieces: []string{"BACK"}},   // 2: the automatic step of the back
		{Pieces: []string{"CUFF", "BACK"}},          // 3: a sewing step of the cuff and the back
		{Pieces: []string{"COLLAR"}},                // 4: a sewing step of the collar
	}
	for _, tt := range []struct {
		name      string
		foldPiece string
		from      []string
		want      int
	}{
		{"own piece's automatic step, whatever the step it sat on", "back", []string{"FRONT"}, 2},
		{"survivor of the step's pieces before a mere input of the fold's piece", "SLEEVE", []string{"FRONT"}, 1},
		{"the first survivor when the step had several", "HOOD", []string{"BACK", "FRONT"}, 1},
		{"an input of the fold's piece when it has no automatic step", "CUFF", []string{"GONE"}, 3},
		{"an input of the step's piece when nothing else fits", "HOOD", []string{"COLLAR"}, 4},
		{"no step of any of them", "HOOD", []string{"GONE"}, -1},
		{"a blank fold piece and no pieces match nothing", " ", nil, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := FoldHome(tt.foldPiece, tt.from, steps); got != tt.want {
				t.Fatalf("FoldHome() = %d, want %d", got, tt.want)
			}
		})
	}
}

// A deleted duplicate hands its links to the store with their address in the body as sent; the steps
// that stay keep theirs, and no fold key is written anywhere the store would read it from twice.
func TestReconcileDisplacesTheLinksOfADeletedStep(t *testing.T) {
	survivor := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	duplicate := storedAutoFusing(20, "INTERLINING-A", "FRONT")
	duplicate.FoldKeys = []string{"K1", "K2"}
	join := machineOp(30, "join")
	join.FoldKeys = []string{"K3"}
	card := fusedCard([]entity.TechCardOperation{survivor, duplicate, join})

	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 2 || card.Operations[1].Note.String != "join" {
		t.Fatalf("operations = %#v, want the duplicate gone", card.Operations)
	}
	if card.Operations[0].FoldKeys != nil || !reflect.DeepEqual(card.Operations[1].FoldKeys, []string{"K3"}) {
		t.Fatalf("fold keys moved in the write model: %#v", card.Operations)
	}
	want := []entity.TechCardDisplacedFoldKey{
		{Key: "K1", Field: "operations[1].fold_keys[0]", Pieces: []string{"FRONT", "FRONT"}},
		{Key: "K2", Field: "operations[1].fold_keys[1]", Pieces: []string{"FRONT", "FRONT"}},
	}
	if !reflect.DeepEqual(card.DisplacedFoldKeys, want) {
		t.Fatalf("displaced = %#v, want %#v", card.DisplacedFoldKeys, want)
	}
	assertConverged(t, card)
}

// A split step has no link of its own any more: the store sends each to the step of the fold's piece.
func TestReconcileDisplacesTheLinksOfASplitStep(t *testing.T) {
	grouped := legacyGroupedFusing(10, "INTERLINING-A", "FRONT", "BACK")
	grouped.FoldKeys = []string{"K1"}
	card := fusedCard([]entity.TechCardOperation{grouped, machineOp(20, "join")})
	card.Pieces[1].Fused = true
	card.Pieces[1].FusingBomLineKey = "INTERLINING-A"

	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 3 {
		t.Fatalf("operations = %#v, want the group split in two", card.Operations)
	}
	for i := range card.Operations {
		if len(card.Operations[i].FoldKeys) != 0 {
			t.Fatalf("step %d kept a split step's key: %#v", i, card.Operations[i].FoldKeys)
		}
	}
	if len(card.DisplacedFoldKeys) != 1 || card.DisplacedFoldKeys[0].Field != "operations[0].fold_keys[0]" {
		t.Fatalf("displaced = %#v", card.DisplacedFoldKeys)
	}
	steps := FoldStepsOf(card.Operations, card.Pieces)
	if home := FoldHome("BACK", card.DisplacedFoldKeys[0].Pieces, steps); steps[home].Owner != "BACK" {
		t.Fatalf("home of a back fold = %d in %#v", home, steps)
	}
	if home := FoldHome("SLEEVE", card.DisplacedFoldKeys[0].Pieces, steps); steps[home].Owner == "" {
		t.Fatalf("a fold of another piece must stay on a step of the group: %d in %#v", home, steps)
	}
	assertConverged(t, card)
}

// A step carried back brings its keys, except one the payload moved: that would be one fold on two steps.
func TestCarriedStepKeepsOnlyTheKeysNoStepNames(t *testing.T) {
	carried := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	carried.FoldKeys = []string{"KEEP", "MOVED"}
	stored := fusedCard([]entity.TechCardOperation{carried, machineOp(20, "join")})
	join := machineOp(10, "join")
	join.FoldKeys = []string{"MOVED"}
	incoming := staleCopy(stored, []entity.TechCardOperation{join})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil {
		t.Fatalf("ReconcileTechCardUpdate() error = %v", err)
	}
	if !outcome.Carried || len(incoming.Operations) != 2 {
		t.Fatalf("outcome = %+v, operations = %#v", outcome, incoming.Operations)
	}
	if got := incoming.Operations[0].FoldKeys; !reflect.DeepEqual(got, []string{"KEEP"}) {
		t.Fatalf("carried step keys = %#v", got)
	}
	if got := stored.Operations[0].FoldKeys; !reflect.DeepEqual(got, []string{"KEEP", "MOVED"}) {
		t.Fatalf("the stored card must not change: %#v", got)
	}
}
