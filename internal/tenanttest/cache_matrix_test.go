package tenanttest

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// TestDictionaryCachePerTeam: the in-memory dictionaries and settings are per team.
//
//  1. Each team adds a colour, a collection and a tag named with its own marker through the real
//     handlers; its own write refreshes its own snapshot. GetDictionary as each team carries only
//     its own markers.
//  2. A colour written straight into B's database (what another instance's write looks like here)
//     reaches B's GetDictionary through B's own revision check, and never A's.
//  3. A memory-only setting (max order items) changed by B is B's alone.
func TestDictionaryCachePerTeam(t *testing.T) {
	x := setup(t)
	ctx := context.Background()
	const markA, markB = "alphadict", "bravodict"

	// B has the tables but no migration seed rows: give it the singleton rows a dictionary load
	// (announce id=1) and a dictionary write (the namespace revision rows) need.
	for _, q := range []string{
		"INSERT IGNORE INTO `" + dbTwo + "`.dictionary_revision SELECT * FROM `" + dbOne + "`.dictionary_revision",
		"INSERT IGNORE INTO `" + dbTwo + "`.announce SELECT * FROM `" + dbOne + "`.announce",
	} {
		if _, err := x.root.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed B: %v (%s)", err, q)
		}
	}

	for _, w := range []struct {
		c          caller
		mark, code string
	}{{x.asA(), markA, "QA7"}, {x.asB(), markB, "QB7"}} {
		for _, s := range []struct{ name, body string }{
			{"CreateColor", `{"code":"` + w.code + `","name":"` + w.mark + `-colour","hex":"#123456"}`},
			{"CreateCollection", `{"name":"` + w.mark + `-collection"}`},
			{"CreateTag", `{"name":"` + w.mark + `-tag"}`},
		} {
			if o := x.adminCall(s.name, w.c, s.body); o.err != nil || o.panic != "" {
				t.Fatalf("%s as %s: %v %s", s.name, w.mark, o.err, o.panic)
			}
		}
	}

	dict := func(c caller, who string) (*pb_admin.GetDictionaryResponse, string) {
		t.Helper()
		before := x.st.TenantStats().MissingCtx
		o := x.adminCall("GetDictionary", c, "{}")
		if o.err != nil || o.panic != "" {
			t.Fatalf("GetDictionary as %s: %v %s", who, o.err, o.panic)
		}
		if d := x.st.TenantStats().MissingCtx - before; d != 0 {
			t.Errorf("GetDictionary as %s: %d statement(s) ran without a team", who, d)
		}
		r, ok := o.resp.(*pb_admin.GetDictionaryResponse)
		if !ok || r.GetDictionary() == nil {
			t.Fatalf("GetDictionary as %s: no dictionary in %T", who, o.resp)
		}
		return r, respJSON(o.resp)
	}
	own := func(js, who, mark, foreign string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(js, mark+p) {
				t.Errorf("GetDictionary as %s: its own %q is missing", who, mark+p)
			}
		}
		if strings.Contains(js, foreign) {
			t.Errorf("GetDictionary as %s carries the other team's %q", who, foreign)
		}
	}

	_, ja := dict(x.asA(), "A")
	_, jb := dict(x.asB(), "B")
	own(ja, "A", markA, markB, "-colour", "-collection", "-tag")
	own(jb, "B", markB, markA, "-colour", "-collection", "-tag")

	// Another instance's write: B's database only, B's colour revision bumped.
	for _, q := range []string{
		"INSERT INTO `" + dbTwo + "`.color (code, name, hex) VALUES ('QB8', '" + markB + "-direct', '#654321')",
		"UPDATE `" + dbTwo + "`.dictionary_revision SET revision = revision + 1 WHERE namespace = '" + string(entity.DictNamespaceColor) + "'",
	} {
		if _, err := x.root.ExecContext(ctx, q); err != nil {
			t.Fatalf("direct write to B: %v (%s)", err, q)
		}
	}
	_, jb = dict(x.asB(), "B")
	own(jb, "B", markB, markA, "-direct")
	_, ja = dict(x.asA(), "A")
	own(ja, "A", markA, markB)

	// Memory-only setting: B's change stays B's.
	ra, _ := dict(x.asA(), "A")
	maxA := ra.GetDictionary().GetMaxOrderItems()
	maxB := maxA + 4
	if o := x.adminCall("UpdateSettings", x.asB(), `{"maxOrderItems":`+itoa(int(maxB))+`}`); o.err != nil || o.panic != "" {
		t.Fatalf("UpdateSettings as B: %v %s", o.err, o.panic)
	}
	if rb, _ := dict(x.asB(), "B"); rb.GetDictionary().GetMaxOrderItems() != maxB {
		t.Errorf("B's max order items = %d, want its own %d", rb.GetDictionary().GetMaxOrderItems(), maxB)
	}
	if ra, _ = dict(x.asA(), "A"); ra.GetDictionary().GetMaxOrderItems() != maxA {
		t.Errorf("A's max order items = %d after B set %d, want A's %d", ra.GetDictionary().GetMaxOrderItems(), maxB, maxA)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
