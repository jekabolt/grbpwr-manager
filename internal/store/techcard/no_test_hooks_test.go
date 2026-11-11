package techcard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NO TEST HOOK IN THE PRODUCTION STORE. A fault injector reachable from production code is a
// process-wide switch: one test that sets it breaks every other test of the binary running at the same
// time, and nothing stops a production caller from setting it. Tests fail the store from outside —
// the store integration tests wrap the DB the store is built with (techcard.New), see
// faultyCards in internal/store/techcard_seam_change_integration_test.go.
//
// Red on a production function named …ForTest, and on a package-level var in the seam gesture's file:
// the gesture is one transaction and keeps no state between calls.
func TestNoTestHookInProductionCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if strings.Contains(d.Name.Name, "ForTest") {
					t.Errorf("%s: %s is a test hook in production code — fail the store from the test side", name, d.Name.Name)
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR || name != "seam_change.go" {
					continue
				}
				for _, sp := range d.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						t.Errorf("%s: package-level var %s — the seam gesture keeps no process-wide state", name, n.Name)
					}
				}
			}
		}
	}
}
