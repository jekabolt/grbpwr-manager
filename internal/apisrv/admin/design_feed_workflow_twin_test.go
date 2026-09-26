package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// B-07 — THE FEED'S run_workflow SQL AGAINST ITS GO TWIN, RUNNABLE.
//
// The store's own shape probes (store/design/design_shape_test.go) are compile-only on a developer
// machine — the store TestMain drops every table of its DSN — so the guarantees that matter are
// proved here, from the text the store actually runs (design.CardOutputsFeed):
//
//  1. TestFeedWorkflowSQLReadsTheTableOfDesignWorkflowOf — no DB: the CASE is parsed into its table
//     and evaluated against entity.DesignWorkflowOf for every kind × preset × fabric-picture.
//  2. TestFeedStampWindowAndCountAreOneExpression — no DB: the builder, fed markers, carries the
//     workflow expression exactly where it is used and nowhere else; the real statements are that
//     builder's output; PARTITION BY spells the FULL expression, never the alias.
//  3. TestFeedWorkflowSQLLiveOnThrowawayMySQL — only with DESIGN_FEED_SQL_DSN pointing at a
//     LOOPBACK MySQL: the expression is executed on frozen params produced by the production encoder
//     (plus hand-written JSON edge cases) and compared row by row with the Go twin, then the real
//     list and count statements run on TEMPORARY tables and are compared with a Go model of the
//     window.

// feedWorkflowTable is designCardOutputsWorkflow read back as data.
type feedWorkflowTable struct {
	freeform     map[string]string // preset → workflow
	freeformElse string
	kinds        map[string]string // kind → workflow (no fabric branch)
	fabricKind   string            // the kind whose tile depends on a fabric picture
	fabricYes    string
	fabricNo     string
	kindElse     string
}

func (w feedWorkflowTable) eval(kind, preset string, fabric bool) string {
	switch {
	case kind == entity.DesignRunKindFreeform:
		if v, ok := w.freeform[preset]; ok {
			return v
		}
		return w.freeformElse
	case kind == w.fabricKind:
		if fabric {
			return w.fabricYes
		}
		return w.fabricNo
	}
	if v, ok := w.kinds[kind]; ok {
		return v
	}
	return w.kindElse
}

func feedWords(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseFeedWorkflowSQL reads the CASE into a table. It is strict on purpose: a shape it does not
// recognise fails the probe instead of being skipped, so a rewrite of the CASE has to come with a
// rewrite of this reader.
func parseFeedWorkflowSQL(t *testing.T, feed design.CardOutputsFeedSQL) feedWorkflowTable {
	t.Helper()
	const head = "CASE COALESCE(r.kind, '') WHEN 'freeform' THEN " +
		"CASE COALESCE(JSON_UNQUOTE(JSON_EXTRACT(r.params, '$.freeform.preset')), '') "
	src := feedWords(feed.Workflow)
	require.True(t, strings.HasPrefix(src, head),
		"the workflow CASE must switch on the run kind and, for freeform, on the frozen preset "+
			"with NULL/missing normalised to '': %s", src)
	fp := feedWords(feed.FabricPicture)
	require.Equal(t, 1, strings.Count(src, fp), "the fabric-picture predicate is used exactly once")
	rest := strings.Replace(src[len(head):], fp, "FABRIC", 1)

	pair := regexp.MustCompile(`^WHEN '([a-z0-9_]*)' THEN '([a-z0-9_]*)' `)
	fabric := regexp.MustCompile(`^WHEN '([a-z0-9_]+)' THEN CASE WHEN FABRIC THEN '([a-z0-9_]+)' ELSE '([a-z0-9_]+)' END `)
	elseEnd := regexp.MustCompile(`^ELSE '([a-z0-9_]*)' END ?`)

	w := feedWorkflowTable{freeform: map[string]string{}, kinds: map[string]string{}}
	for {
		if m := pair.FindStringSubmatch(rest); m != nil {
			require.NotContains(t, w.freeform, m[1], "preset %q twice", m[1])
			w.freeform[m[1]] = m[2]
			rest = rest[len(m[0]):]
			continue
		}
		m := elseEnd.FindStringSubmatch(rest)
		require.NotNil(t, m, "freeform sub-CASE must end in ELSE ... END: %q", rest)
		w.freeformElse, rest = m[1], rest[len(m[0]):]
		break
	}
	for {
		if m := fabric.FindStringSubmatch(rest); m != nil {
			require.Empty(t, w.fabricKind, "one fabric branch")
			w.fabricKind, w.fabricYes, w.fabricNo = m[1], m[2], m[3]
			rest = rest[len(m[0]):]
			continue
		}
		if m := pair.FindStringSubmatch(rest); m != nil {
			require.NotContains(t, w.kinds, m[1], "kind %q twice", m[1])
			w.kinds[m[1]] = m[2]
			rest = rest[len(m[0]):]
			continue
		}
		m := elseEnd.FindStringSubmatch(rest)
		require.NotNil(t, m, "the kind CASE must end in ELSE ... END: %q", rest)
		w.kindElse, rest = m[1], rest[len(m[0]):]
		break
	}
	require.Empty(t, rest, "nothing may follow the CASE")
	return w
}

func feedTwinKinds() []string {
	return []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
		entity.DesignRunKindVector, entity.DesignRunKindDraftIdea, entity.DesignRunKindRecolor,
		entity.DesignRunKindPattern, entity.DesignRunKindFreeform, entity.DesignRunKindCutout,
		"", "unheard_of_kind",
	}
}

func feedTwinPresets() []string {
	return append(entity.FreeformPresetsAll(), "", "free", "unheard_of", "TRYON", "cutout")
}

// MUTATIONS (each measured red): map 'retouch' to 'create_edit' in the SQL; drop the 'threed'
// branch; add a preset to entity.DesignWorkflowOf without adding it to the SQL; swap the recolor
// branch's THEN/ELSE.
func TestFeedWorkflowSQLReadsTheTableOfDesignWorkflowOf(t *testing.T) {
	w := parseFeedWorkflowSQL(t, design.CardOutputsFeed())
	for _, kind := range feedTwinKinds() {
		for _, preset := range feedTwinPresets() {
			for _, fab := range []bool{false, true} {
				want := entity.DesignWorkflowOf(kind, preset, fab)
				require.Equal(t, want, w.eval(kind, preset, fab),
					"kind=%q preset=%q fabricPicture=%v: the feed's SQL stamps another tile than "+
						"entity.DesignWorkflowOf", kind, preset, fab)
				if want != "" {
					require.True(t, entity.IsDesignWorkflow(want), want)
				}
			}
		}
	}
}

// MUTATIONS (each measured red): in designCardOutputsStatements partition by the `run_workflow`
// alias instead of the key; inline a copy of the workflow CASE in the count's GROUP BY; group the
// count by the section-1 key instead of the workflow; drop the stamp.
func TestFeedStampWindowAndCountAreOneExpression(t *testing.T) {
	const (
		scopeS    = "<<SCOPE>>"
		colorwayS = "<<COLORWAY>>"
		sectionS  = "<<SECTION>>"
		workflowS = "<<WORKFLOW>>"
	)
	list, count := design.CardOutputsFeedStatements(scopeS, colorwayS, sectionS, workflowS)
	key := design.CardOutputsWindowKey(sectionS, workflowS)
	require.Equal(t, "CASE WHEN "+sectionS+" = 1 THEN "+workflowS+" ELSE '' END", key,
		"the third window key is the workflow inside section 1 and '' elsewhere")

	require.Equal(t, 2, strings.Count(list, workflowS), "list: the stamp + inside the window key")
	require.Equal(t, 1, strings.Count(list, workflowS+" AS run_workflow"), "list: the stamp")
	require.Equal(t, 1, strings.Count(feedWords(list),
		"PARTITION BY "+colorwayS+", "+sectionS+", "+key+" ORDER BY p.id DESC"),
		"list: the window is cut by colourway, section and the FULL section-1 key")
	require.Equal(t, 2, strings.Count(count, workflowS), "count: the SELECT + the GROUP BY")
	require.Contains(t, feedWords(count), workflowS+" AS workflow,")
	require.True(t, strings.HasSuffix(feedWords(count), "GROUP BY "+colorwayS+", "+sectionS+", "+workflowS),
		"count: grouped by the workflow itself — a refinement of the window key")
	for name, stmt := range map[string]string{"list": list, "count": count} {
		for _, inlined := range []string{"'tryon'", "'swap_fabrics'", "JSON_EXTRACT", "'freeform'", "r.params"} {
			require.NotContains(t, stmt, inlined, "%s carries its own copy of the workflow CASE", name)
		}
	}

	// The statements the store runs are exactly this builder over the real pieces, and the real
	// PARTITION BY names no alias.
	feed := design.CardOutputsFeed()
	gotList, gotCount := design.CardOutputsFeedStatements(feed.Scope, feed.Colorway, feed.Section, feed.Workflow)
	require.Equal(t, feed.List, gotList)
	require.Equal(t, feed.Count, gotCount)
	require.Equal(t, design.CardOutputsWindowKey(feed.Section, feed.Workflow), feed.WindowKey)
	part := feedWords(feed.List)
	part = part[strings.Index(part, "PARTITION BY "):]
	part = part[:strings.Index(part, " ORDER BY")]
	require.NotContains(t, part, "run_workflow", "PARTITION BY must spell the expression, not the alias")
	require.Contains(t, part, feedWords(feed.Workflow))
}

// ───────────────────────── live, on a throwaway MySQL only ─────────────────────────

// feedProbeDB opens DESIGN_FEED_SQL_DSN on ONE connection, refusing anything but loopback. Every
// statement below is a SELECT over literals or over TEMPORARY tables of this session; nothing
// permanent is created or dropped.
func feedProbeDB(t *testing.T) *sql.Conn {
	t.Helper()
	dsn := os.Getenv("DESIGN_FEED_SQL_DSN")
	if dsn == "" {
		t.Skip("DESIGN_FEED_SQL_DSN not set (e.g. docker run -p 127.0.0.1:3399:3306 mysql:8.0)")
	}
	cfg, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(cfg.Addr)
	require.NoError(t, err)
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatalf("DESIGN_FEED_SQL_DSN must point at a loopback MySQL, got %q", cfg.Addr)
	}
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type feedTwinCase struct {
	name string
	kind string
	raw  *string // nil = params NULL
}

func feedTwinCases(t *testing.T) []feedTwinCase {
	t.Helper()
	enc := func(p *pb_common.DesignRunParams) *string {
		b, err := designMarshalJSON(p)
		require.NoError(t, err)
		s := string(b)
		return &s
	}
	lit := func(s string) *string { return &s }
	fab := func(ids ...int32) *pb_common.DesignRunParams {
		c := &pb_common.DesignColourRecipe{Hex: "#112233"}
		for _, id := range ids {
			c.Fabrics = append(c.Fabrics, &pb_common.DesignFabricUse{Name: "cloth", MediaId: id})
		}
		return &pb_common.DesignRunParams{Colour: c}
	}
	var out []feedTwinCase
	for _, preset := range feedTwinPresets() {
		out = append(out, feedTwinCase{"freeform/" + preset, entity.DesignRunKindFreeform,
			enc(&pb_common.DesignRunParams{Freeform: &pb_common.DesignFreeformParams{Preset: preset}})})
	}
	out = append(out,
		feedTwinCase{"freeform/no-freeform", entity.DesignRunKindFreeform, enc(&pb_common.DesignRunParams{})},
		feedTwinCase{"freeform/params-NULL", entity.DesignRunKindFreeform, nil},
		feedTwinCase{"freeform/json-null", entity.DesignRunKindFreeform, lit(`null`)},
		feedTwinCase{"freeform/preset-null", entity.DesignRunKindFreeform, lit(`{"freeform":{"preset":null}}`)},
		feedTwinCase{"freeform/empty-freeform", entity.DesignRunKindFreeform, lit(`{"freeform":{}}`)},
		feedTwinCase{"recolor/no-colour", entity.DesignRunKindRecolor, enc(&pb_common.DesignRunParams{})},
		feedTwinCase{"recolor/params-NULL", entity.DesignRunKindRecolor, nil},
		feedTwinCase{"recolor/no-fabrics", entity.DesignRunKindRecolor, enc(fab())},
		feedTwinCase{"recolor/words-only-cloth", entity.DesignRunKindRecolor, enc(fab(0))},
		feedTwinCase{"recolor/cloth-7", entity.DesignRunKindRecolor, enc(fab(7))},
		feedTwinCase{"recolor/cloths-0-12", entity.DesignRunKindRecolor, enc(fab(0, 12))},
		feedTwinCase{"recolor/cloth-negative", entity.DesignRunKindRecolor, enc(fab(-3))},
		feedTwinCase{"recolor/cloths-negative-10", entity.DesignRunKindRecolor, enc(fab(-3, 10))},
		feedTwinCase{"recolor/cloths-0-0", entity.DesignRunKindRecolor, enc(fab(0, 0))},
		feedTwinCase{"recolor/scalar-only", entity.DesignRunKindRecolor,
			enc(&pb_common.DesignRunParams{Colour: &pb_common.DesignColourRecipe{FabricMediaId: 9}})},
		feedTwinCase{"recolor/explicit-zero", entity.DesignRunKindRecolor, lit(`{"colour":{"fabrics":[{"media_id":0}]}}`)},
		feedTwinCase{"recolor/null-media", entity.DesignRunKindRecolor, lit(`{"colour":{"fabrics":[{"media_id":null}]}}`)},
		feedTwinCase{"recolor/quoted-15", entity.DesignRunKindRecolor, lit(`{"colour":{"fabrics":[{"media_id":"15"}]}}`)},
		feedTwinCase{"recolor/quoted-0", entity.DesignRunKindRecolor, lit(`{"colour":{"fabrics":[{"media_id":"0"}]}}`)},
		feedTwinCase{"recolor/big-id", entity.DesignRunKindRecolor, enc(fab(0, 2000000000))},
		feedTwinCase{"recolor/json-null", entity.DesignRunKindRecolor, lit(`null`)},
	)
	for _, kind := range feedTwinKinds() {
		out = append(out, feedTwinCase{"kind/" + kind, kind, enc(fab(5))}, feedTwinCase{"kind/" + kind + "/NULL", kind, nil})
	}
	return out
}

// goTwin is what the Go side computes from the SAME frozen bytes: decode as the band decodes
// (designUnmarshalJSON), then the door's rule.
func goTwin(t *testing.T, c feedTwinCase) string {
	t.Helper()
	p := &pb_common.DesignRunParams{}
	// A JSON `null` document is not a message protojson can read; the Go side has no params there,
	// which is exactly what SQL NULL params mean — so the twin of `null` is the twin of NULL.
	if c.raw != nil && strings.TrimSpace(*c.raw) != "null" {
		require.NoError(t, designUnmarshalJSON([]byte(*c.raw), p), c.name)
	}
	return entity.DesignWorkflowOf(c.kind, p.GetFreeform().GetPreset(), designAnyClothWithPicture(p.GetColour()))
}

func TestFeedWorkflowSQLLiveOnThrowawayMySQL(t *testing.T) {
	conn := feedProbeDB(t)
	ctx := context.Background()
	feed := design.CardOutputsFeed()

	// 1. The expression alone, over a one-row derived table named like the store's alias.
	q := "SELECT " + feed.Workflow + " AS w FROM (SELECT ? AS kind, CAST(? AS JSON) AS params) AS r"
	for _, c := range feedTwinCases(t) {
		var raw any
		if c.raw != nil {
			raw = *c.raw
		}
		var got string
		require.NoError(t, conn.QueryRowContext(ctx, q, c.kind, raw).Scan(&got), c.name)
		require.Equal(t, goTwin(t, c), got, "%s: SQL and Go disagree on the tile (params %v)", c.name, raw)
	}

	// 2. The real statements on TEMPORARY tables (session-scoped; they shadow nothing permanent and
	// vanish with the connection).
	for _, stmt := range []string{
		"DROP TEMPORARY TABLE IF EXISTS design_picture",
		"DROP TEMPORARY TABLE IF EXISTS design_run",
		`CREATE TEMPORARY TABLE design_run (id INT PRIMARY KEY, kind VARCHAR(32) NOT NULL,
			params JSON NULL, rrev INT NOT NULL DEFAULT 0, colorway_id INT NULL)`,
		`CREATE TEMPORARY TABLE design_picture (id INT PRIMARY KEY, tech_card_id INT NOT NULL,
			run_id INT NULL, kind VARCHAR(32) NOT NULL, colorway_id INT NULL)`,
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	type run struct {
		id   int
		kind string
		raw  *string
		cw   int
	}
	s := func(v string) *string { return &v }
	runs := []run{
		{1, "freeform", s(`{"freeform":{"preset":"free"}}`), 0},
		{2, "freeform", s(`{"freeform":{"preset":"tryon"}}`), 0},
		{3, "cutout", nil, 0},
		{4, "recolor", s(`{"colour":{"fabrics":[{"media_id":0},{"media_id":44}]}}`), 0},
		{5, "recolor", s(`{"colour":{"hex":"#ff0000","fabrics":[{"media_id":0}]}}`), 5},
		{6, "render", s(`{}`), 0},
		{7, "threed", nil, 0},
		{8, "freeform", nil, 0},
		{9, "freeform", s(`{"freeform":{"preset":"retouch"}}`), 5},
	}
	for _, r := range runs {
		var raw any
		if r.raw != nil {
			raw = *r.raw
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO design_run (id, kind, params, rrev, colorway_id) VALUES (?, ?, CAST(? AS JSON), 1, ?)",
			r.id, r.kind, raw, r.cw)
		require.NoError(t, err)
	}
	type pic struct {
		id, card, run int // run 0 = NULL
		kind          string
		cw            int
	}
	pics := []pic{
		{101, 1, 1, "freeform", 0}, {102, 1, 1, "freeform", 0}, {103, 1, 1, "freeform", 0}, {104, 1, 8, "freeform", 0},
		{110, 1, 2, "freeform", 0},
		{111, 1, 3, "cutout", 0}, {112, 1, 3, "cutout", 0}, {113, 1, 3, "cutout", 0},
		{120, 1, 6, "render", 0}, {121, 1, 6, "render", 0}, {122, 1, 7, "threed", 0}, {123, 1, 4, "render", 0},
		{124, 1, 0, "render", 0},
		{130, 1, 5, "render", 5}, {131, 1, 9, "freeform", 5}, {132, 1, 9, "freeform", 5},
		{200, 2, 1, "freeform", 0},
	}
	for _, p := range pics {
		var runID any
		if p.run != 0 {
			runID = p.run
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO design_picture (id, tech_card_id, run_id, kind, colorway_id) VALUES (?, ?, ?, ?, ?)",
			p.id, p.card, runID, p.kind, p.cw)
		require.NoError(t, err)
	}

	// The Go model of the window: stamp by the twin, key = (cw, section, workflow-in-section-1),
	// newest `per` per key.
	const per = 2
	runByID := map[int]run{}
	for _, r := range runs {
		runByID[r.id] = r
	}
	type modelRow struct {
		id       int
		cw       int
		section  int
		workflow string
	}
	var model []modelRow
	for _, p := range pics {
		if p.card != 1 {
			continue
		}
		kind, stamp := "", ""
		if p.run != 0 {
			r := runByID[p.run]
			kind = r.kind
			stamp = goTwin(t, feedTwinCase{kind: r.kind, raw: r.raw})
		}
		section := 0
		if kind == entity.DesignRunKindFreeform || kind == entity.DesignRunKindCutout {
			section = 1
		}
		model = append(model, modelRow{p.id, p.cw, section, stamp})
	}
	sort.Slice(model, func(i, j int) bool { return model[i].id > model[j].id })
	seen := map[[3]any]int{}
	wantList := map[int]string{}
	wantByWorkflow := map[string]int{}
	wantByCw := map[int]int{}
	for _, m := range model {
		key := ""
		if m.section == 1 {
			key = m.workflow
		}
		k := [3]any{m.cw, m.section, key}
		seen[k]++
		if seen[k] <= per {
			wantList[m.id] = m.workflow
		}
		wantByCw[m.cw]++
		if m.workflow != "" {
			wantByWorkflow[m.workflow]++
		}
	}

	bind := func(stmt string) (string, []any) {
		qq, args, err := sqlx.Named(stmt, map[string]any{"card": 1, "per_colorway": per})
		require.NoError(t, err)
		return qq, args
	}
	lq, largs := bind(feed.List)
	rows, err := conn.QueryContext(ctx, lq, largs...)
	require.NoError(t, err)
	cols, err := rows.Columns()
	require.NoError(t, err)
	gotList := map[int]string{}
	var order []int
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := map[string]string{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else if vals[i] != nil {
				row[c] = fmt.Sprint(vals[i])
			}
		}
		id, err := strconv.Atoi(row["id"])
		require.NoError(t, err)
		gotList[id] = row["run_workflow"]
		order = append(order, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, wantList, gotList, "the list window (newest %d per colourway, section, workflow-in-section-1) and its stamps", per)
	require.True(t, sort.SliceIsSorted(order, func(i, j int) bool { return order[i] > order[j] }), "newest first")

	cq, cargs := bind(feed.Count)
	crow, err := conn.QueryContext(ctx, cq, cargs...)
	require.NoError(t, err)
	gotByCw, gotByWorkflow := map[int]int{}, map[string]int{}
	for crow.Next() {
		var cw, section, n int
		var workflow string
		require.NoError(t, crow.Scan(&cw, &section, &workflow, &n))
		gotByCw[cw] += n
		if workflow != "" {
			gotByWorkflow[workflow] += n
		}
	}
	require.NoError(t, crow.Err())
	require.NoError(t, crow.Close())
	require.Equal(t, wantByCw, gotByCw, "per-colourway totals")
	require.Equal(t, wantByWorkflow, gotByWorkflow, "per-workflow totals (band 31)")
}
