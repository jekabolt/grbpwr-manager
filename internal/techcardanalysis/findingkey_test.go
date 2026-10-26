package techcardanalysis

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestFindingKeyNormalizesUniqueOpsToTheirFingerprint(t *testing.T) {
	fps := map[int32]string{10: "aaaaaaaa", 20: "bbbbbbbb", 30: "bbbbbbbb"}
	tokens := OpKeyTokens(fps)

	if got, want := FindingKey("smv_missing", []string{RefOp(10)}, "", tokens), "smv_missing@opfp:aaaaaaaa"; got != want {
		t.Errorf("unique shape: key = %q, want %q", got, want)
	}
	// Two steps of one shape are told apart only by their number: the key keeps it.
	if got, want := FindingKey("smv_missing", []string{RefOp(20)}, "", tokens), "smv_missing@op:20"; got != want {
		t.Errorf("shared shape: key = %q, want %q", got, want)
	}
	// A step the card does not have stays as written.
	if got, want := FindingKey("smv_missing", []string{RefOp(99)}, "", tokens), "smv_missing@op:99"; got != want {
		t.Errorf("unknown step: key = %q, want %q", got, want)
	}

	// Renumbering: the same shape under a new number keeps the key.
	renumbered := OpKeyTokens(map[int32]string{40: "aaaaaaaa", 50: "bbbbbbbb"})
	if a, b := FindingKey("k", []string{RefOp(10)}, "", tokens), FindingKey("k", []string{RefOp(40)}, "", renumbered); a != b {
		t.Errorf("renumbering moved the key: %q vs %q", a, b)
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
	for _, client := range []string{"pdf_sa_missing", "edge_missing", "partner_missing"} {
		if IsServerCode(client) {
			t.Errorf("client code %q is taken by the server", client)
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
		{FindingKey: "edge_missing@op:10,seam:hem", Fingerprint: "00000000000000ff", SeenBy: "boris", SeenAt: at},
		{FindingKey: "no-code-head", Fingerprint: "00000000000000ff", SeenBy: "boris", SeenAt: at},
	}
	got := ClientSeenMarks(marks)
	if len(got) != 2 || got[0].FindingKey != "pdf_sa_missing@seam:hem" || got[1].FindingKey != "edge_missing@op:10,seam:hem" {
		t.Fatalf("client marks = %+v", got)
	}
	if got[0].Fingerprint != "fedcba9876543210" || got[0].SeenBy != "boris" || !got[0].SeenAt.Equal(at) {
		t.Fatalf("client mark lost its fields: %+v", got[0])
	}
	if out := ClientSeenMarks(nil); out == nil || len(out) != 0 {
		t.Fatalf("no marks: %#v", out)
	}
}
