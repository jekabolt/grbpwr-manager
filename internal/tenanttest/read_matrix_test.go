package tenanttest

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// skipFile lists the read methods the matrix cannot exercise in this harness (their handler
// panics on a dependency the harness leaves nil: bucket, mailer, providers…), one per line,
// "Method # reason". It is a ratchet: a panicking method missing from the list fails, and so does
// a listed method that now runs. TENANTTEST_UPDATE_SKIP=1 rewrites it.
const skipFile = "skip.txt"

func readSkip(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(skipFile)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}
		}
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out[line] = true
		}
	}
	return out
}

func writeSkip(t *testing.T, panics map[string]string) {
	t.Helper()
	names := make([]string, 0, len(panics))
	for n := range panics {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Read methods the tenanttest matrix cannot run (handler panics on a nil harness dependency).\n")
	b.WriteString("# Ratchet: regenerate with TENANTTEST_UPDATE_SKIP=1 only after looking at why.\n")
	for _, n := range names {
		r := strings.ReplaceAll(panics[n], "\n", " ")
		if len(r) > 90 {
			r = r[:90]
		}
		fmt.Fprintf(&b, "%s # %s\n", n, r)
	}
	if err := os.WriteFile(skipFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seed writes the same objects into both teams through the real handlers, titled with the team's
// marker. Fresh tables on both sides: the rows share primary keys, only the content differs.
func seed(t *testing.T, x *harness, c caller, marker string, required bool) {
	t.Helper()
	board := "0"
	if o := x.adminCall("ListTaskBoards", c, "{}"); o.err == nil && o.panic == "" {
		js := respJSON(o.resp)
		if i := strings.Index(js, `"id":`); i >= 0 {
			rest := js[i+5:]
			j := strings.IndexAny(rest, ",}")
			if j > 0 {
				board = strings.Trim(rest[:j], `"`)
			}
		}
	}
	steps := []struct{ name, body string }{
		{"AddTask", `{"task":{"title":"` + marker + `-task"},"boardId":"` + board + `"}`},
		{"AddModel", `{"model":{"name":"` + marker + `-model"}}`},
		{"CreateMaterial", `{"material":{"name":"` + marker + `-material","section":"TECH_CARD_BOM_SECTION_FABRIC"}}`},
	}
	for _, s := range steps {
		o := x.adminCall(s.name, c, s.body)
		if o.err != nil || o.panic != "" {
			if required {
				t.Errorf("seed %s %s: %v %s", marker, s.name, o.err, o.panic)
			} else {
				t.Logf("seed %s %s (B has no migration seed rows): %v %s", marker, s.name, o.err, o.panic)
			}
		}
	}
}

// TestReadMatrixNoForeignRows calls every read method with real handlers as A and as B, first one
// by one (so a statement without a team is attributed to its method), then A and B interleaved
// from parallel workers. No answer may carry the other team's marker, the bound team must be the
// caller's, and no statement may run without a team (MissingCtx).
func TestReadMatrixNoForeignRows(t *testing.T) {
	x := setup(t)
	const markA, markB = "alphamark", "bravomark"
	seed(t, x, x.asA(), markA, true)
	seed(t, x, x.asB(), markB, false)

	skip := readSkip(t)
	panics := map[string]string{}
	var reads []method
	for _, m := range adminMethods() {
		if m.read {
			reads = append(reads, m)
		}
	}

	check := func(m method, who string, o outcome, wantTeam int64, foreign string) {
		if o.panic != "" {
			return
		}
		if o.reached && o.team.ID != wantTeam {
			t.Errorf("%s as %s: bound team %d", m.name, who, o.team.ID)
		}
		if js := respJSON(o.resp); strings.Contains(js, foreign) {
			t.Errorf("%s as %s: answer carries the other team's %q", m.name, who, foreign)
		}
		if o.err != nil && strings.Contains(o.err.Error(), foreign) {
			t.Errorf("%s as %s: error carries the other team's %q", m.name, who, foreign)
		}
	}

	served, errored := 0, 0
	for _, m := range reads {
		before := x.st.TenantStats().MissingCtx
		ob := x.invoke(m, x.adm, x.asB(), false, "")
		oa := x.invoke(m, x.adm, x.asA(), false, "")
		if d := x.st.TenantStats().MissingCtx - before; d != 0 {
			t.Errorf("%s: %d statement(s) ran without a team", m.name, d)
		}
		for _, o := range []outcome{ob, oa} {
			if o.panic != "" {
				panics[m.name] = o.panic
			}
		}
		check(m, "B", ob, 2, markA)
		check(m, "A", oa, 1, markB)
		if ob.err == nil && ob.panic == "" {
			served++
		} else {
			errored++
		}
	}

	// Interleaved: 8 workers, A and B alternating on the same pool.
	before := x.st.TenantStats().MissingCtx
	jobs := make(chan method)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for m := range jobs {
				if panics[m.name] != "" {
					continue
				}
				cs := []caller{x.asA(), x.asB()}
				if w%2 == 1 {
					cs[0], cs[1] = cs[1], cs[0]
				}
				for _, c := range cs {
					o := x.invoke(m, x.adm, c, false, "")
					mu.Lock()
					if c.xteam == slugB {
						check(m, "B (parallel)", o, 2, markA)
					} else {
						check(m, "A (parallel)", o, 1, markB)
					}
					mu.Unlock()
				}
			}
		}(w)
	}
	began := time.Now()
	for _, m := range reads {
		jobs <- m
	}
	close(jobs)
	wg.Wait()
	if d := x.st.TenantStats().MissingCtx - before; d != 0 {
		t.Errorf("parallel pass: %d statement(s) ran without a team", d)
	}

	if os.Getenv("TENANTTEST_UPDATE_SKIP") == "1" {
		writeSkip(t, panics)
	} else {
		for n, p := range panics {
			if !skip[n] {
				t.Errorf("%s panics and is not in %s: %s", n, skipFile, p)
			}
		}
		for n := range skip {
			if _, ok := panics[n]; !ok {
				t.Errorf("%s is in %s but now runs: drop it", n, skipFile)
			}
		}
	}
	t.Logf("read matrix: %d read methods; B served %d, B refused/errored %d, skipped (panic) %d; parallel pass %s",
		len(reads), served, errored, len(panics), time.Since(began).Round(time.Millisecond))
}
