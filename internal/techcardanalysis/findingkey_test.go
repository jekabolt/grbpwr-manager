package techcardanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestFindingKeyIgnoresRefOrderAndDuplicates(t *testing.T) {
	a := FindingKey("x", []string{RefOp(20), RefUnit("Body"), RefCard, RefOp(20)}, "", nil)
	b := FindingKey("x", []string{RefCard, RefUnit("Body"), RefOp(20)}, "", nil)
	if a != b {
		t.Fatalf("order/duplicates moved the key: %q vs %q", a, b)
	}
	if want := "x@card,op:20,unit:Body"; a != want {
		t.Fatalf("key = %q, want %q", a, want)
	}
}

func TestFindingKeyAnchorsStepsByShapeAndOrdinal(t *testing.T) {
	fps := map[int32]string{10: "aaaaaaaa", 20: "bbbbbbbb", 30: "bbbbbbbb"}
	tokens := OpKeyTokens(fps)

	for ref, want := range map[int32]string{
		10: "smv_missing@opfp:aaaaaaaa.1", // a unique shape carries ".1" too — from the start
		20: "smv_missing@opfp:bbbbbbbb.1", // two steps of one shape: the ordinal in operation order
		30: "smv_missing@opfp:bbbbbbbb.2",
		99: "smv_missing@op:99", // a step the card does not have stays as written
	} {
		if got := FindingKey("smv_missing", []string{RefOp(ref)}, "", tokens); got != want {
			t.Errorf("op %d: key = %q, want %q", ref, got, want)
		}
	}

	// Renumbering: the same shapes in the same order under new numbers keep every key.
	renumbered := OpKeyTokens(map[int32]string{40: "aaaaaaaa", 50: "bbbbbbbb", 60: "bbbbbbbb"})
	for old, now := range map[int32]int32{10: 40, 20: 50, 30: 60} {
		if a, b := FindingKey("k", []string{RefOp(old)}, "", tokens), FindingKey("k", []string{RefOp(now)}, "", renumbered); a != b {
			t.Errorf("renumbering %d→%d moved the key: %q vs %q", old, now, a, b)
		}
	}
}

// TestOpKeyTokensDoNotDependOnLaterSteps: a step's token is decided by its own shape and the steps
// BEFORE it of the same shape — adding a later twin, or any other step, leaves it alone.
func TestOpKeyTokensDoNotDependOnLaterSteps(t *testing.T) {
	alone := OpKeyTokens(map[int32]string{10: "aaaaaaaa"})
	for name, more := range map[string]map[int32]string{
		"a later twin":       {10: "aaaaaaaa", 20: "aaaaaaaa"},
		"another shape":      {10: "aaaaaaaa", 20: "bbbbbbbb"},
		"twins of another":   {5: "cccccccc", 10: "aaaaaaaa", 20: "cccccccc"},
		"a renumbered route": {10: "aaaaaaaa", 30: "aaaaaaaa", 40: "dddddddd"},
	} {
		if got := OpKeyTokens(more)[10]; got != alone[10] {
			t.Errorf("%s moved op 10: %q, want %q", name, got, alone[10])
		}
	}
	if got := OpKeyTokens(map[int32]string{10: "aaaaaaaa", 20: "aaaaaaaa"})[20]; got != "opfp:aaaaaaaa.2" {
		t.Errorf("the twin = %q", got)
	}
}

func TestFindingKeyKeepsSeamRefsAndSubject(t *testing.T) {
	got := FindingKey("edge_x", []string{RefSeam("side-seam"), RefOp(10), RefSeam("hem")}, "", map[int32]string{10: "opfp:cccccccc"})
	if want := "edge_x@opfp:cccccccc,seam:hem,seam:side-seam"; got != want {
		t.Errorf("seam refs: key = %q, want %q", got, want)
	}
	if got, want := FindingKey("label_not_in_bom", []string{RefCard}, "brand", nil), "label_not_in_bom@card#brand"; got != want {
		t.Errorf("subject: key = %q, want %q", got, want)
	}
}

func TestFindingKeyFoldsOverlongKeysToAHash(t *testing.T) {
	long := strings.Repeat("ю", 400) // 800 bytes
	got := FindingKey("unit_key_case_collision", []string{RefUnit(long)}, "", nil)
	if len(got) > MaxFindingKeyBytes {
		t.Fatalf("key is %d bytes, column holds %d", len(got), MaxFindingKeyBytes)
	}
	if !strings.HasPrefix(got, "unit_key_case_collision@sha256:") {
		t.Fatalf("folded key = %q", got)
	}
	if again := FindingKey("unit_key_case_collision", []string{RefUnit(long)}, "", nil); again != got {
		t.Fatal("folded key is not deterministic")
	}
	if other := FindingKey("unit_key_case_collision", []string{RefUnit(long + "x")}, "", nil); other == got {
		t.Fatal("two different long keys folded into one")
	}
}

func TestFindingFingerprint(t *testing.T) {
	fps := map[int32]string{10: "bbbbbbbb", 20: "aaaaaaaa"}
	got := FindingFingerprint("T", "D", []string{RefOp(10), RefCard, RefOp(20), RefOp(10), RefOp(77)}, fps)

	// The contract vector: title 0 detail 0 then the fp8 of each referenced step, sorted.
	sum := sha256.Sum256([]byte("T\x00D\x00aaaaaaaabbbbbbbb"))
	if want := hex.EncodeToString(sum[:])[:16]; got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
	if len(got) != FindingFingerprintLen {
		t.Fatalf("fingerprint length %d", len(got))
	}
	if again := FindingFingerprint("T", "D", []string{RefOp(20), RefOp(10)}, fps); again != got {
		t.Error("ref order moved the fingerprint")
	}
	for name, other := range map[string]string{
		"title":  FindingFingerprint("T2", "D", []string{RefOp(10), RefOp(20)}, fps),
		"detail": FindingFingerprint("T", "D2", []string{RefOp(10), RefOp(20)}, fps),
		"shape":  FindingFingerprint("T", "D", []string{RefOp(10), RefOp(20)}, map[int32]string{10: "cccccccc", 20: "aaaaaaaa"}),
		// The NUL keeps «title|detail» boundaries apart.
		"boundary": FindingFingerprint("TD", "", []string{RefOp(10), RefOp(20)}, fps),
	} {
		if other == got {
			t.Errorf("a change of %s did not move the fingerprint", name)
		}
	}
}

func TestMaskOpNumbersMasksOnlyTheRefsStepsAsWholeWords(t *testing.T) {
	refs := []string{"op:20", "op:010", RefCard, "op:x"}
	for in, want := range map[string]string{
		"Operation 20 runs on overlock":   "Operation # runs on overlock",
		"Ops 10, 20 set hardware":         "Ops #, # set hardware",
		"see op:10 and #20 (20)":          "see op:# and ## (#)",
		"a20 20a 20_ x_20":                "a20 20a 20_ x_20",        // inside a word: not a step number
		"20.5 cm, 1,20 and 10,20":         "20.5 cm, 1,20 and 10,20", // part of a bigger number
		"Operation 30 is not in the refs": "Operation 30 is not in the refs",
		"Операция20 — шаг 10.":            "Операция# — шаг #.", // a UTF-8 letter is a boundary
		"":                                "",
	} {
		if got := MaskOpNumbers(in, refs); got != want {
			t.Errorf("MaskOpNumbers(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MaskOpNumbers("Operation 20", []string{RefCard}); got != "Operation 20" {
		t.Errorf("no step refs: %q", got)
	}
}

// Renumbering is not a change of the finding: the text that names the step by number keeps its
// fingerprint, while a real change of the text still moves it.
func TestFindingFingerprintIgnoresStepNumbers(t *testing.T) {
	before := FindingFingerprint("Operation 210: no SA", "Operation 210 runs on overlock.",
		[]string{RefOp(210)}, map[int32]string{210: "aaaaaaaa"})
	after := FindingFingerprint("Operation 215: no SA", "Operation 215 runs on overlock.",
		[]string{RefOp(215)}, map[int32]string{215: "aaaaaaaa"})
	if before != after {
		t.Fatalf("renumbering moved the fingerprint: %s vs %s", before, after)
	}
	changed := FindingFingerprint("Operation 215: no SA", "Operation 215 runs on lockstitch.",
		[]string{RefOp(215)}, map[int32]string{215: "aaaaaaaa"})
	if changed == after {
		t.Fatal("a changed defect kept its fingerprint")
	}
	reshaped := FindingFingerprint("Operation 215: no SA", "Operation 215 runs on overlock.",
		[]string{RefOp(215)}, map[int32]string{215: "bbbbbbbb"})
	if reshaped == after {
		t.Fatal("a reshaped step kept the fingerprint")
	}
}

// TestAssignKeysMasksStepsTheProseNamesOutsideRefs: a server finding that names a step its refs
// leave out (the pack step after a wet step, the tail of a capped list) keeps its fingerprint under
// renumbering too — only through the server's prose mask, since the refs do not carry that step.
func TestAssignKeysMasksStepsTheProseNamesOutsideRefs(t *testing.T) {
	run := func(wet, pack int32, packIsAStep bool) Finding {
		fs := []Finding{{
			Code:   CodeSmvMissing,
			Title:  "Label sewn before washing",
			Detail: fmt.Sprintf("Operation %d washes the garment, and it comes after Operation %d, which packs.", wet, pack),
			Refs:   []string{RefOp(wet)},
		}}
		ops := map[int32]string{wet: "aaaaaaaa"}
		if packIsAStep {
			ops[pack] = "bbbbbbbb"
		}
		AssignKeys(fs, ops)
		return fs[0]
	}
	a, b := run(40, 90, true), run(45, 95, true)
	if a.Key != b.Key || a.Fingerprint != b.Fingerprint {
		t.Fatalf("renumbering moved the finding: %s/%s vs %s/%s", a.Key, a.Fingerprint, b.Key, b.Fingerprint)
	}
	// A number that is not a step of the card is text like any other.
	if run(45, 95, false).Fingerprint == run(45, 96, false).Fingerprint {
		t.Fatal("a number the card has no step for was masked")
	}
}

// The server's own prose also names steps its refs leave out (the pack step, a list capped at
// three): maskOpProse takes those numbers out too, but only in «Operation N» / «ops N, M» and only
// for steps the card has.
func TestMaskOpProse(t *testing.T) {
	card := map[int32]string{40: "aaaaaaaa", 470: "bbbbbbbb", 480: "cccccccc", 490: "dddddddd"}
	for in, want := range map[string]string{
		"Ops 470, 480, 490 set hardware":                       "Ops #, #, # set hardware",
		"it comes after Operation 40, which packs":             "it comes after Operation #, which packs",
		"no SMV on operation 40":                               "no SMV on operation #",
		"is produced by op 470; input of ops 40, 7":            "is produced by op #; input of ops #, 7",
		"on 4 of 4 pressing operations; 40 profiles; shop 470": "on 4 of 4 pressing operations; 40 profiles; shop 470",
		"Operation 999 is gone":                                "Operation 999 is gone",
	} {
		if got := maskOpProse(in, card); got != want {
			t.Errorf("maskOpProse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssignKeysDisambiguatesLeftoverCollisions(t *testing.T) {
	fs := []Finding{
		{Code: "c", Title: "A", Refs: []string{RefCard}},
		{Code: "c", Title: "B", Refs: []string{RefCard}},
		{Code: "c", Title: "B", Refs: []string{RefCard}},
		{Code: "d", Title: "A", Refs: []string{RefCard}},
		{Title: "model finding", Refs: []string{RefCard}},
	}
	AssignKeys(fs, nil)
	seen := map[string]bool{}
	for _, f := range fs[:4] {
		if f.Key == "" || seen[f.Key] {
			t.Fatalf("key %q empty or repeated: %+v", f.Key, fs)
		}
		seen[f.Key] = true
	}
	if fs[3].Key != "d@card" {
		t.Errorf("a key that does not collide must stay plain, got %q", fs[3].Key)
	}
	if fs[4].Key != "" || fs[4].Fingerprint != "" {
		t.Errorf("a finding without a code must stay keyless, got %q/%q", fs[4].Key, fs[4].Fingerprint)
	}
}

// TestCollisionNetIsGloballyUnique: the suffix the net appends can be spelled by a ref (a BOM line
// named "A~<hash>"). Keys held by one finding are reserved first, so the net steps around them.
func TestCollisionNetIsGloballyUnique(t *testing.T) {
	ident := findingIdentity("wastage_not_stated", "", "T", []string{"bom:A"}, nil)
	fs := []Finding{
		{Code: "wastage_not_stated", Title: "T", Refs: []string{"bom:A"}},
		{Code: "wastage_not_stated", Title: "T", Refs: []string{"bom:A"}},
		{Code: "wastage_not_stated", Title: "T", Refs: []string{"bom:A~" + ident}},
	}
	AssignKeys(fs, nil)
	plain := "wastage_not_stated@bom:A~" + ident
	want := []string{plain + ".2", plain + ".3", plain}
	for i, f := range fs {
		if f.Key != want[i] {
			t.Errorf("finding %d: key %q, want %q", i, f.Key, want[i])
		}
	}

	// The identity is injective over its fields: refs that join to the same key text do not share it.
	if findingIdentity("c", "", "T", []string{"bom:a,b"}, nil) == findingIdentity("c", "", "T", []string{"bom:a", "b"}, nil) {
		t.Error("two ref lists that join alike share an identity")
	}
	if findingIdentity("c", "x", "T", nil, nil) == findingIdentity("c", "", "xT", nil, nil) {
		t.Error("subject and title boundaries are not kept apart")
	}
	// …and blind to step numbers, like the fingerprint.
	if findingIdentity("c", "", "Operation 20", []string{"op:20"}, map[int32]string{20: "opfp:aaaaaaaa.1"}) !=
		findingIdentity("c", "", "Operation 25", []string{"op:25"}, map[int32]string{25: "opfp:aaaaaaaa.1"}) {
		t.Error("renumbering moved the identity")
	}
}

func TestApplySeenMatchesOnFingerprint(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fs := []Finding{
		{Key: "a@card", Fingerprint: "1111111111111111"},
		{Key: "b@card", Fingerprint: "2222222222222222"},
		{Key: "c@card", Fingerprint: "3333333333333333"},
		{Key: "", Fingerprint: ""},
	}
	marks := []entity.TechCardFindingSeen{
		{FindingKey: "a@card", Fingerprint: "1111111111111111", SeenBy: "anna", SeenAt: at},
		{FindingKey: "b@card", Fingerprint: "ffffffffffffffff", SeenBy: "boris", SeenAt: at}, // text changed since
		{FindingKey: "", Fingerprint: "", SeenBy: "nobody", SeenAt: at},
	}
	unseen := ApplySeen(fs, marks)
	if unseen != 3 {
		t.Errorf("unseen = %d, want 3", unseen)
	}
	if !fs[0].Seen || fs[0].SeenBy != "anna" || !fs[0].SeenAt.Equal(at) {
		t.Errorf("matching mark not applied: %+v", fs[0])
	}
	if fs[1].Seen || fs[1].SeenBy != "" {
		t.Errorf("stale mark applied: %+v", fs[1])
	}
	if fs[2].Seen || fs[3].Seen {
		t.Error("unmarked / keyless finding reads as seen")
	}
}

// TestFindingKeysAreUniqueOnGoldenCard: on the real card every machine finding has a code, a key
// and a fingerprint, and the keys are unique WITHOUT the safety net (no "~" suffix) — a check that
// produces twins must set Subject.
func TestFindingKeysAreUniqueOnGoldenCard(t *testing.T) {
	hex16 := regexp.MustCompile(`^[0-9a-f]{16}$`)
	for name, card := range map[string]*entity.TechCard{"draft": card8(), "in_review": gdInReview()} {
		res := RunAudit(card, gdFx)
		if len(res.Findings) == 0 {
			t.Fatalf("%s: no findings", name)
		}
		keys := map[string]string{}
		for _, f := range res.Findings {
			if f.Code == "" {
				t.Errorf("%s: finding without a code: %q", name, f.Title)
			}
			if !hex16.MatchString(f.Fingerprint) {
				t.Errorf("%s: fingerprint %q of %q", name, f.Fingerprint, f.Title)
			}
			if strings.Contains(f.Key, "~") {
				t.Errorf("%s: key %q of %q needed the collision net — give the check a Subject", name, f.Key, f.Title)
			}
			if prev, dup := keys[f.Key]; dup {
				t.Errorf("%s: key %q shared by %q and %q", name, f.Key, prev, f.Title)
			}
			keys[f.Key] = f.Title
		}
	}
}

// TestFindingKeysSurviveRenumbering: inserting a step renumbers everything after it; a key built
// on a unique step shape does not move.
func TestFindingKeysSurviveRenumbering(t *testing.T) {
	before := RunAudit(gdInReview(), gdFx)
	shifted := gdInReview()
	for i := range shifted.Operations {
		if shifted.Operations[i].OperationNumber.Valid {
			shifted.Operations[i].OperationNumber.Int32 += 5
		}
	}
	after := RunAudit(shifted, gdFx)

	stable := func(res AuditResult) map[string]bool {
		out := map[string]bool{}
		for _, f := range res.Findings {
			if !strings.Contains(f.Key, refOpPrefix) { // a number-anchored key may move, by design
				out[f.Key] = true
			}
		}
		return out
	}
	b, a := stable(before), stable(after)
	normalized := 0
	for k := range b {
		if strings.Contains(k, refOpFpPrefix) {
			normalized++
		}
		if !a[k] {
			t.Errorf("key %q did not survive renumbering", k)
		}
	}
	if normalized == 0 {
		t.Fatal("no step-anchored key on the golden card — the test proves nothing")
	}
}

// TestSeenSurvivesRenumberingButNotAChangedDefect walks the whole path a mark takes: every finding
// on the golden card is marked seen, the steps are renumbered (shifted, then spread out), and every
// mark must still hold — the number in «Operation N» is not the defect. Then the defect itself
// changes (the note of op 40 is rewritten) and exactly that finding comes back, under the same key.
func TestSeenSurvivesRenumberingButNotAChangedDefect(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	before := RunAudit(gdInReview(), gdFx)
	marks := make([]entity.TechCardFindingSeen, 0, len(before.Findings))
	for _, f := range before.Findings {
		if f.Key == "" || f.Fingerprint == "" {
			t.Fatalf("finding %q has no key/fingerprint", f.Title)
		}
		marks = append(marks, entity.TechCardFindingSeen{FindingKey: f.Key, Fingerprint: f.Fingerprint, SeenBy: "boris", SeenAt: at})
	}
	if len(marks) < 10 {
		t.Fatalf("only %d findings on the golden card — the test proves little", len(marks))
	}
	opNamed := 0
	for _, f := range before.Findings {
		if opProseRe.MatchString(f.Title + " " + f.Detail) {
			opNamed++
		}
	}
	if opNamed == 0 {
		t.Fatal("no finding names a step by number — the test proves nothing")
	}

	renumber := func(name string, to func(int32) int32) *entity.TechCard {
		c := gdInReview()
		for i := range c.Operations {
			if c.Operations[i].OperationNumber.Valid {
				c.Operations[i].OperationNumber.Int32 = to(c.Operations[i].OperationNumber.Int32)
			}
		}
		after := RunAudit(c, gdFx)
		if len(after.Findings) != len(before.Findings) {
			t.Fatalf("%s: %d findings, want %d", name, len(after.Findings), len(before.Findings))
		}
		if unseen := ApplySeen(after.Findings, marks); unseen != 0 {
			for _, f := range after.Findings {
				if !f.Seen {
					t.Errorf("%s un-saw %s (fp %s): %s — %s", name, f.Key, f.Fingerprint, f.Title, f.Detail)
				}
			}
			t.Fatalf("%s: %d findings unseen, want 0", name, unseen)
		}
		return c
	}
	renumber("shift +5", func(n int32) int32 { return n + 5 })
	renumber("spread x2", func(n int32) int32 { return n * 2 })
	renumber("spread x3 +1", func(n int32) int32 { return n*3 + 1 })

	// The defect changes: op 40 now carries a different note that still names the empty field.
	changed := gdInReview()
	card8OpByNumber(changed, 40).Note = text("Medium thread tension on topstitch")
	after := RunAudit(changed, gdFx)
	unseen := ApplySeen(after.Findings, marks)
	var back []Finding
	for _, f := range after.Findings {
		if !f.Seen {
			back = append(back, f)
		}
	}
	if unseen != 1 || len(back) != 1 {
		t.Fatalf("a changed note: %d unseen (%d listed), want exactly 1", unseen, len(back))
	}
	if back[0].Code != CodeNoteOnlyInstruction || !keyIn(marks, back[0].Key) {
		t.Fatalf("came back %s (%s), want the note finding under its old key", back[0].Key, back[0].Code)
	}
}

func keyIn(marks []entity.TechCardFindingSeen, key string) bool {
	for _, m := range marks {
		if m.FindingKey == key {
			return true
		}
	}
	return false
}

// TestEveryMachineFindingLiteralHasACode reads the check files and fails on any Finding literal
// without a Code: a new check that forgets it would otherwise ship findings nobody can mark seen.
func TestEveryMachineFindingLiteralHasACode(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range []string{"route.go", "bom.go", "readiness.go", "findings.go", "heuristics.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			check := func(c *ast.CompositeLit) {
				for _, e := range c.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Code" {
							return
						}
					}
				}
				t.Errorf("%s: Finding literal without Code", fset.Position(c.Pos()))
			}
			switch typ := lit.Type.(type) {
			case *ast.Ident:
				if typ.Name == "Finding" {
					check(lit)
				}
			case *ast.ArrayType:
				if id, ok := typ.Elt.(*ast.Ident); ok && id.Name == "Finding" {
					for _, e := range lit.Elts {
						if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
							check(inner)
						}
					}
				}
			}
			return true
		})
	}
}

// TestCodesAreSnakeCaseUniqueAndUnreserved pins the code table: snake_case, one value per
// constant, none of the codes reserved for the assembly-schema findings.
func TestCodesAreSnakeCaseUniqueAndUnreserved(t *testing.T) {
	reserved := map[string]bool{
		"edge_missing": true, "partner_missing": true, "step_no_pieces": true, "unit_unused": true,
		"length_mismatch": true, "piece_no_code": true, "inner_target_missing": true,
	}
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	f, err := parser.ParseFile(token.NewFileSet(), "findingkey.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				v, _ := strconv.Unquote(bl.Value)
				if !snake.MatchString(v) {
					t.Errorf("%s = %q is not snake_case", name.Name, v)
				}
				if reserved[v] {
					t.Errorf("%s takes the reserved code %q", name.Name, v)
				}
				if prev, dup := values[v]; dup {
					t.Errorf("%s and %s share the code %q", prev, name.Name, v)
				}
				values[v] = name.Name
			}
		}
	}
	if len(values) < 40 {
		t.Fatalf("parsed only %d codes — the table moved?", len(values))
	}
}

// TestServerCodesListEveryCode keeps serverCodes equal to the Code* const table: a code missing
// from the set would have its marks handed out bare (ClientSeenMarks), one in the set but not in
// the table would swallow a client finding's marks.
func TestServerCodesListEveryCode(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "findingkey.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	table := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok {
					v, _ := strconv.Unquote(bl.Value)
					table[v] = true
				}
			}
		}
	}
	for v := range table {
		if !IsServerCode(v) {
			t.Errorf("code %q is in the const table but not in serverCodes", v)
		}
	}
	for v := range serverCodes {
		if !table[v] {
			t.Errorf("serverCodes holds %q, which is not a Code* const", v)
		}
	}
	for _, client := range []string{"pdf_sa_missing", "asm_edge_missing", "asm_partner_missing"} {
		if IsServerCode(client) {
			t.Errorf("client code %q is taken by the server", client)
		}
	}
	// The namespaces stay the client's: no server code may start with one.
	for v := range table {
		for _, p := range ClientCodePrefixes {
			if strings.HasPrefix(v, p) {
				t.Errorf("server code %q sits in the client namespace %q", v, p)
			}
		}
	}
}

func TestValidFindingKeyChecksOnlyTheShape(t *testing.T) {
	for _, ok := range []string{
		"smv_missing@opfp:aaaaaaaa",
		"pdf_sa_missing@seam:side-seam", // a client code the server never computes
		"edge_missing@op:10,seam:hem#left",
		"x@",
		"unit_key_case_collision@sha256:0123",
	} {
		if !ValidFindingKey(ok) {
			t.Errorf("ValidFindingKey(%q) = false", ok)
		}
	}
	for _, bad := range []string{
		"", "   ", "card", "@card", "Smv@card", "9x@card", "smv-missing@card", " smv@card",
		"k@\xff", "k@" + strings.Repeat("a", MaxFindingKeyBytes),
	} {
		if ValidFindingKey(bad) {
			t.Errorf("ValidFindingKey(%q) = true", bad)
		}
	}
	if got := KeyCode("pdf_sa_missing@seam:hem"); got != "pdf_sa_missing" {
		t.Errorf("KeyCode = %q", got)
	}
}

func TestClientSeenMarksKeepsOnlyCodesTheServerDoesNotCompute(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	marks := []entity.TechCardFindingSeen{
		{FindingKey: "smv_missing@opfp:aaaaaaaa", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
		{FindingKey: "placeholder_price@bom:Shell", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
		{FindingKey: "pdf_sa_missing@seam:hem", Fingerprint: "fedcba9876543210", SeenBy: "boris", SeenAt: at},
		{FindingKey: "asm_edge_missing@op:10,seam:hem", Fingerprint: "00000000000000ff", SeenBy: "boris", SeenAt: at},
		// Reserved for future SERVER checks, outside the client namespaces: never handed out.
		{FindingKey: "edge_missing@op:10,seam:hem", Fingerprint: "00000000000000ff", SeenBy: "boris", SeenAt: at},
		{FindingKey: "no-code-head", Fingerprint: "00000000000000ff", SeenBy: "boris", SeenAt: at},
	}
	got := ClientSeenMarks(marks)
	if len(got) != 2 || got[0].FindingKey != "pdf_sa_missing@seam:hem" || got[1].FindingKey != "asm_edge_missing@op:10,seam:hem" {
		t.Fatalf("client marks = %+v", got)
	}
	if got[0].Fingerprint != "fedcba9876543210" || got[0].SeenBy != "boris" || !got[0].SeenAt.Equal(at) {
		t.Fatalf("client mark lost its fields: %+v", got[0])
	}
	if out := ClientSeenMarks(nil); out == nil || len(out) != 0 {
		t.Fatalf("no marks: %#v", out)
	}

	// Capped like the table: never more than a card may keep.
	many := make([]entity.TechCardFindingSeen, 0, entity.TechCardFindingSeenMaxPerCard+100)
	for i := 0; i < entity.TechCardFindingSeenMaxPerCard+100; i++ {
		many = append(many, entity.TechCardFindingSeen{FindingKey: "pdf_x@card#" + strconv.Itoa(i), Fingerprint: "0123456789abcdef"})
	}
	if out := ClientSeenMarks(many); len(out) != entity.TechCardFindingSeenMaxPerCard || out[0].FindingKey != "pdf_x@card#0" {
		t.Fatalf("capped marks: %d, first %q", len(out), out[0].FindingKey)
	}
}

func TestKnownFindingCodeIsAServerCodeOrAClientNamespace(t *testing.T) {
	for _, ok := range []string{CodeSmvMissing, CodeNotReleaseReady, "pdf_sa_missing", "asm_edge_missing", "pdf_"} {
		if !KnownFindingCode(ok) {
			t.Errorf("KnownFindingCode(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "k", "x1", "edge_missing", "pdf", "pdfx_a", "PDF_x", "asm-x", "pdf_X"} {
		if KnownFindingCode(bad) {
			t.Errorf("KnownFindingCode(%q) = true", bad)
		}
	}
	if IsClientCode(CodeSmvMissing) {
		t.Error("a server code passes as a client one")
	}
}

// TestFindingKeyGoldenVectors pins exact outputs that the admin client asserts too
// (src/components/managers/tech-card/components/finding-key.ts, scripts/finding-seen-check.ts): the
// browser builds the keys of its own findings (pdf_*, assembly-schema) and must agree with this
// file byte for byte. Change a vector here only together with the client.
func TestFindingKeyGoldenVectors(t *testing.T) {
	tok := OpKeyTokens(map[int32]string{10: "aaaaaaaa", 20: "bbbbbbbb", 30: "bbbbbbbb"})
	two := []Finding{
		{Code: "c", Title: "A", Refs: []string{RefCard}},
		{Code: "c", Title: "B", Refs: []string{RefCard}},
		{Code: "c", Title: "B", Refs: []string{RefCard}},
	}
	AssignKeys(two, nil)
	// A ref that spells the net's own suffix: the plain key is reserved, the twins step around it.
	ident := findingIdentity("c", "", "T", []string{"bom:A"}, nil)
	injected := []Finding{
		{Code: "c", Title: "T", Refs: []string{"bom:A"}},
		{Code: "c", Title: "T", Refs: []string{"bom:A"}},
		{Code: "c", Title: "T", Refs: []string{"bom:A~" + ident}},
	}
	AssignKeys(injected, nil)
	for i, c := range []struct{ got, want string }{
		{FindingKey("pdf_sa_missing", []string{"seam:side", "op:10", "seam:hem", "op:10"}, "", tok), "pdf_sa_missing@opfp:aaaaaaaa.1,seam:hem,seam:side"},
		{FindingKey("pdf_sa_missing", []string{"op:20"}, "", tok), "pdf_sa_missing@opfp:bbbbbbbb.1"},
		{FindingKey("pdf_sa_missing", []string{"op:30"}, "", tok), "pdf_sa_missing@opfp:bbbbbbbb.2"},
		{FindingKey("pdf_sa_missing", []string{"op:010", "op:+10"}, "", tok), "pdf_sa_missing@opfp:aaaaaaaa.1"},
		{FindingKey("pdf_sa_missing", []string{"op:x", "op:99999999999"}, "", tok), "pdf_sa_missing@op:99999999999,op:x"},
		{FindingKey("pdf_x", []string{"card"}, "front", tok), "pdf_x@card#front"},
		{FindingKey("pdf_x", nil, "", tok), "pdf_x@"},
		// Byte order, not UTF-16 order: U+E000 sorts before an astral character.
		{FindingKey("pdf_x", []string{"unit:😀", "unit:\uE000", "unit:é", "unit:z"}, "", tok), "pdf_x@unit:z,unit:é,unit:\uE000,unit:😀"},
		{FindingKey("unit_key_case_collision", []string{RefUnit(strings.Repeat("ю", 400))}, "", nil), "unit_key_case_collision@sha256:e6a9631642ae8f89e851d719927f7f76"},
		{FindingFingerprint("Seam allowance missing", "Side seam has no SA", []string{"op:20", "op:10", "seam:hem"}, map[int32]string{10: "bbbbbbbb", 20: "aaaaaaaa"}), "c13246480eda69ca"},
		{FindingFingerprint("Нет припуска", "", nil, nil), "c3de5ec904f08a99"},
		// Step numbers in the text are masked: the same defect under new numbers hashes alike.
		{FindingFingerprint("Operation 20: no SA", "Ops 10, 20 — шаг 10.", []string{"op:20", "op:10"}, map[int32]string{10: "bbbbbbbb", 20: "aaaaaaaa"}), "9129f8c1af5d3ffa"},
		{FindingFingerprint("Operation 7: no SA", "Ops 3, 7 — шаг 3.", []string{"op:7", "op:3"}, map[int32]string{3: "bbbbbbbb", 7: "aaaaaaaa"}), "9129f8c1af5d3ffa"},
		{MaskOpNumbers("Operation 20, op:10, a20, 20.5, 1,20, Шаг20 (20)", []string{"op:20", "op:010", "card"}), "Operation #, op:#, a20, 20.5, 1,20, Шаг# (#)"},
		{ident, "7730bec869d428a5"},
		{two[0].Key, "c@card~b3a895749906c3c2"},
		{two[1].Key, "c@card~30179dd4f82ce934"},
		{two[2].Key, "c@card~30179dd4f82ce934.2"},
		{injected[0].Key, "c@bom:A~7730bec869d428a5.2"},
		{injected[1].Key, "c@bom:A~7730bec869d428a5.3"},
		{injected[2].Key, "c@bom:A~7730bec869d428a5"},
	} {
		if c.got != c.want {
			t.Errorf("vector %d: got %q, want %q", i, c.got, c.want)
		}
	}
}
