package ai

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// THESE PROBES NEED NO DATABASE, AND CANNOT REACH ONE. The store talks to its handle through
// ExecContext / GetContext / SelectContext only; recDB below answers exactly those three and embeds a
// NIL dependency.DB for everything else, so any other call panics instead of dialling anything. The
// statements are the store's own constants, bound through the store's own param builders and
// storeutil.MakeQuery — the same path production takes up to the driver.
//
// Every test names the mutation that turns it red.

var fixedNow = time.Date(2026, 9, 27, 21, 30, 0, 0, time.UTC)

// ───────────────────────── the recording fake ─────────────────────────

type recCall struct {
	kind  string // exec | get | select
	query string // as it reached the handle: positional ?
	args  []any
}

type recResult struct{ rows, id int64 }

func (r recResult) LastInsertId() (int64, error) { return r.id, nil }
func (r recResult) RowsAffected() (int64, error) { return r.rows, nil }

type recDB struct {
	dependency.DB // nil: an unexpected method panics
	calls         []recCall
	onExec        func(q string, args []any) (sql.Result, error)
	onGet         func(dest any, q string, args []any) error
	onSelect      func(dest any, q string, args []any) error
}

func (d *recDB) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	d.calls = append(d.calls, recCall{"exec", q, args})
	if d.onExec != nil {
		return d.onExec(q, args)
	}
	return recResult{rows: 1, id: 1}, nil
}

func (d *recDB) GetContext(_ context.Context, dest any, q string, args ...any) error {
	d.calls = append(d.calls, recCall{"get", q, args})
	if d.onGet != nil {
		return d.onGet(dest, q, args)
	}
	return nil
}

func (d *recDB) SelectContext(_ context.Context, dest any, q string, args ...any) error {
	d.calls = append(d.calls, recCall{"select", q, args})
	if d.onSelect != nil {
		return d.onSelect(dest, q, args)
	}
	return nil
}

type recRepo struct {
	dependency.Repository // nil: only DB() may be called
	db                    dependency.DB
}

func (r recRepo) DB() dependency.DB { return r.db }

// newRecStore wires the store exactly as store.go does, with the fake behind both the plain handle and
// the transaction runner.
func newRecStore(db *recDB, txCalls *int) *Store {
	return New(storeutil.Base{DB: db, Now: func() time.Time { return fixedNow }},
		func(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
			if txCalls != nil {
				*txCalls++
			}
			return f(ctx, recRepo{db: db})
		})
}

// ───────────────────────── statement catalogue ─────────────────────────

var namedParamRe = regexp.MustCompile(`:([A-Za-z_]\w*)`)

func paramNames(named string) []string {
	var out []string
	for _, m := range namedParamRe.FindAllStringSubmatch(named, -1) {
		out = append(out, m[1])
	}
	return out
}

// compiled is a named statement as it reaches the handle.
func compiled(t *testing.T, named string) string {
	t.Helper()
	params := map[string]any{}
	for _, n := range paramNames(named) {
		params[n] = nil
	}
	q, _, err := storeutil.MakeQuery(named, params)
	if err != nil {
		t.Fatalf("compile %q: %v", firstLine(named), err)
	}
	return q
}

func firstLine(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, '\n'); i > 0 {
		return q[:i]
	}
	return q
}

// every statement of the package, by name.
var statements = map[string]string{
	"selectAISettings":         selectAISettings,
	"selectAIProviders":        selectAIProviders,
	"selectAIModels":           selectAIModels,
	"selectAIRoutes":           selectAIRoutes,
	"selectBudgetTimezone":     selectBudgetTimezone,
	"bumpConfigVersionChecked": bumpConfigVersionChecked,
	"bumpConfigVersion":        bumpConfigVersion,
	"countAISettings":          countAISettings,
	"ensureAISettings":         ensureAISettings,
	"countAIProvider":          countAIProvider,
	"updateAIProviderEnabled":  updateAIProviderEnabled,
	"setAIProviderAPIKey":      setAIProviderAPIKey,
	"setAIProviderAdminKey":    setAIProviderAdminKey,
	"updateAIDefaults":         updateAIDefaults,
	"deleteAIRoute":            deleteAIRoute,
	"insertAIRouteCandidate":   insertAIRouteCandidate,
	"upsertAIModel":            upsertAIModel,
	"insertAICall":             insertAICall,
	"finishAICall":             finishAICall,
	"priceAcceptedAICall":      priceAcceptedAICall,
	"sweepAIDispatching":       sweepAIDispatching,
	"spendByProvider":          spendByProvider,
	"spendByActor":             spendByActor,
	"upsertAICostDaily":        upsertAICostDaily,
}

// nameOf maps a statement that reached the fake back to its constant's name.
func nameOf(t *testing.T, q string) string {
	t.Helper()
	for name, named := range statements {
		if compiled(t, named) == q {
			return name
		}
	}
	t.Fatalf("a statement that is not one of the package's constants reached the handle: %q", firstLine(q))
	return ""
}

// argOf returns the value bound to :name in a call of the named statement. sqlx binds in order of
// appearance, one ? per occurrence, so the first occurrence's index is the arg's index.
func argOf(t *testing.T, named string, args []any, name string) any {
	t.Helper()
	names := paramNames(named)
	i := slices.Index(names, name)
	if i < 0 {
		t.Fatalf(":%s is not a parameter of %q", name, firstLine(named))
	}
	if len(args) != len(names) {
		t.Fatalf("%q: %d args bound for %d parameters", firstLine(named), len(args), len(names))
	}
	return args[i]
}

// sequence is the names of the statements that reached the fake, in order.
func sequence(t *testing.T, db *recDB) []string {
	t.Helper()
	out := make([]string, len(db.calls))
	for i, c := range db.calls {
		out[i] = nameOf(t, c.query)
	}
	return out
}

func findCall(t *testing.T, db *recDB, name string) recCall {
	t.Helper()
	for _, c := range db.calls {
		if nameOf(t, c.query) == name {
			return c
		}
	}
	t.Fatalf("%s never reached the handle; sequence %v", name, sequence(t, db))
	return recCall{}
}

// ───────────────────────── the builders bind exactly their statements ─────────────────────────

func ptr[T any](v T) *T { return &v }

func sampleStart() entity.AICallStart {
	return entity.AICallStart{
		OccurredAt: fixedNow, DayLocal: "2026-09-27", ProviderKey: "fal", Model: "fal-ai/trellis",
		Purpose: entity.AIPurposeThreed, Actor: "jeka", ActorAdminID: ptr(7), RunID: ptr(41),
		AttemptNo: ptr(2), CallNo: 1, FallbackFrom: "meshy",
	}
}

func sampleEnd() entity.AICallEnd {
	return entity.AICallEnd{
		Status: entity.AICallOK, ErrorCode: "e", HTTPStatus: ptr(200), Engaged: ptr(true),
		RequestID: "req", ModelActual: "m", PromptTokens: ptr(1), CompletionTokens: ptr(2),
		CachedTokens: ptr(3), ReasoningTokens: ptr(4), Units: ptr(decimal.RequireFromString("1.5")),
		Unit: "unit", CostUSD: decimal.NewNullDecimal(decimal.RequireFromString("0.053")),
		CostSource: entity.AICostProvider, PriceVersion: "2026-09-27", LatencyMs: ptr(900),
	}
}

// TestAIStoreShapeEveryStatementBindsExactlyItsParams.
//
// MUTATIONS IT CATCHES: renaming a :param in a statement but not in its builder (sqlx fails the bind
// with «could not find name» — at runtime, on the first paid call); a builder key no statement reads
// (a value the author believes is written and is not); a colon inside a SQL comment (the same
// «could not find name» with an EMPTY name); a parameterless statement that grew a :param.
func TestAIStoreShapeEveryStatementBindsExactlyItsParams(t *testing.T) {
	begin, err := beginCallParams(sampleStart())
	if err != nil {
		t.Fatal(err)
	}
	finish, err := endCallParams(sampleEnd(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	finish["id"] = int64(9)
	priced, err := endCallParams(sampleEnd(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	priced["run_id"], priced["attempt_no"], priced["call_no"] = 41, 2, 1
	cost, err := costDailyParams(entity.AICostDaily{ProviderKey: "openai", Day: "2026-09-26",
		AmountUSD: decimal.RequireFromString("3.5")}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]any{
		"selectAISettings":         nil,
		"selectAIProviders":        nil,
		"selectAIModels":           nil,
		"selectAIRoutes":           nil,
		"selectBudgetTimezone":     nil,
		"countAISettings":          nil,
		"ensureAISettings":         nil,
		"bumpConfigVersionChecked": {"by": "jeka", "expected_version": uint64(3)},
		"bumpConfigVersion":        {"by": "jeka"},
		"countAIProvider":          {"provider_key": "fal"},
		"updateAIProviderEnabled":  {"provider_key": "fal", "enabled": true, "by": "jeka"},
		"setAIProviderAPIKey":      keyWriteParams("fal", []byte{1, 2}, "abcd", "jeka", fixedNow),
		"setAIProviderAdminKey":    keyWriteParams("fal", nil, "", "jeka", fixedNow),
		"updateAIDefaults":         defaultsParams(entity.AIDefaultsPatch{ChatProviderKey: ptr("openai")}, "jeka"),
		"deleteAIRoute":            {"purpose": "vector"},
		"insertAIRouteCandidate":   {"purpose": "vector", "position": 1, "provider_key": "", "model": "", "by": "jeka"},
		"upsertAIModel":            {"provider_key": "fal", "model": "x", "label": "", "kind": "threed", "disabled": false, "by": "jeka"},
		"insertAICall":             begin,
		"finishAICall":             finish,
		"priceAcceptedAICall":      priced,
		"sweepAIDispatching":       {"older_than": fixedNow, "finished_at": fixedNow},
		"spendByProvider":          {"from_day": "2026-09-01", "to_day": "2026-09-27"},
		"spendByActor":             {"from_day": "2026-09-01", "to_day": "2026-09-27"},
		"upsertAICostDaily":        cost,
	}
	if len(cases) != len(statements) {
		t.Fatalf("%d statements, %d cases: a statement is untested", len(statements), len(cases))
	}
	for name, params := range cases {
		named, ok := statements[name]
		if !ok {
			t.Fatalf("case %s names no statement", name)
		}
		used := paramNames(named)
		if params == nil {
			if len(used) != 0 {
				t.Fatalf("%s is bound with no params but reads %v", name, used)
			}
			continue
		}
		q, args, err := storeutil.MakeQuery(named, params)
		if err != nil {
			t.Fatalf("%s does not bind: %v", name, err)
		}
		if got := strings.Count(q, "?"); got != len(args) || got != len(used) {
			t.Fatalf("%s: %d placeholders, %d args, %d named occurrences", name, got, len(args), len(used))
		}
		for k := range params {
			if !slices.Contains(used, k) {
				t.Fatalf("%s: the builder binds :%s, which the statement never reads", name, k)
			}
		}
	}
}

// ───────────────────────── the statements name real columns ─────────────────────────

var createTableRe = regexp.MustCompile(`(?is)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\n\) ENGINE`)

// migrationColumns reads the tables this package touches straight from the migration files.
func migrationColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	tables := map[string]map[string]bool{}
	for _, f := range []string{"0344_design_budget.sql", "0373_ai_providers.sql", "0374_ai_usage.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "sql", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(body), -1) {
			cols := map[string]bool{}
			for _, line := range strings.Split(m[2], "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 {
					continue
				}
				switch strings.ToUpper(fields[0]) {
				case "PRIMARY", "KEY", "UNIQUE", "CONSTRAINT", "INDEX", "FOREIGN":
					continue
				}
				cols[strings.ToLower(fields[0])] = true
			}
			tables[strings.ToLower(m[1])] = cols
		}
	}
	for _, want := range []string{"design_settings", "ai_provider", "ai_model", "ai_route", "ai_settings",
		"ai_usage_event", "ai_provider_cost_daily"} {
		if len(tables[want]) == 0 {
			t.Fatalf("sanity: no columns parsed for %s — the extractor is broken", want)
		}
	}
	return tables
}

var (
	sqlLiteralRe = regexp.MustCompile(`'[^']*'`)
	identRe      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	tableRefRe   = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+(\w+)`)
	onDupRe      = regexp.MustCompile(`(?i)ON\s+DUPLICATE\s+KEY\s+UPDATE`)
	asAliasRe    = regexp.MustCompile(`(?i)\bAS\s+(\w+)`)
	derivedRe    = regexp.MustCompile(`\)\s+(\w+)\s+ON\b|\)\s+(\w+)\s*\n`)
	sqlWords     = map[string]bool{
		"select": true, "from": true, "where": true, "and": true, "or": true, "not": true, "null": true,
		"is": true, "in": true, "as": true, "on": true, "left": true, "join": true, "group": true,
		"by": true, "order": true, "sum": true, "count": true, "max": true, "case": true, "when": true,
		"then": true, "else": true, "end": true, "between": true, "coalesce": true, "if": true,
		"update": true, "set": true, "insert": true, "ignore": true, "into": true, "values": true,
		"delete": true, "duplicate": true, "key": true,
	}
)

// TestAIStoreShapeEveryIdentifierIsAColumnOfItsTables.
//
// MUTATIONS IT CATCHES: a column typo anywhere in a statement (`occured_at`, `api_key_last_4`), a
// statement reading a column 0373/0374 never created (`base_url`, dropped by 02-PLAN A4; `source`,
// dropped by A8), a statement writing a table the migrations do not define.
func TestAIStoreShapeEveryIdentifierIsAColumnOfItsTables(t *testing.T) {
	tables := migrationColumns(t)
	for name, named := range statements {
		text := namedParamRe.ReplaceAllString(sqlLiteralRe.ReplaceAllString(named, "''"), "")
		// The upsert's UPDATE names no table; without this the table extractor would read its first
		// assigned column as one.
		text = onDupRe.ReplaceAllString(text, "ON DUPLICATE KEY")
		allowed := map[string]bool{}
		refs := tableRefRe.FindAllStringSubmatch(text, -1)
		for _, r := range refs {
			tbl := strings.ToLower(r[1])
			cols, ok := tables[tbl]
			if !ok {
				t.Fatalf("%s touches %s, which no migration read here defines", name, tbl)
			}
			allowed[tbl] = true
			for c := range cols {
				allowed[c] = true
			}
		}
		if len(refs) == 0 {
			t.Fatalf("%s names no table", name)
		}
		for _, a := range asAliasRe.FindAllStringSubmatch(text, -1) {
			allowed[strings.ToLower(a[1])] = true
		}
		for _, a := range derivedRe.FindAllStringSubmatch(text, -1) {
			allowed[strings.ToLower(a[1]+a[2])] = true
		}
		for _, id := range identRe.FindAllString(text, -1) {
			low := strings.ToLower(id)
			if !sqlWords[low] && !allowed[low] {
				t.Fatalf("%s names %q, which is neither a column of %v nor an alias", name, id, refs)
			}
		}
	}
}

// selectOutputs is the list of names a SELECT produces, in order.
func selectOutputs(t *testing.T, q string) []string {
	t.Helper()
	up := strings.ToUpper(q)
	start := strings.Index(up, "SELECT")
	if start < 0 {
		t.Fatalf("not a select: %q", firstLine(q))
	}
	depth, end := 0, -1
	for i := start; i < len(q) && end < 0; i++ {
		switch q[i] {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(up[i:], "FROM") && i > 0 && (q[i-1] == ' ' || q[i-1] == '\n' || q[i-1] == '\t') {
				end = i
			}
		}
	}
	list := q[start+len("SELECT") : end]
	var outs []string
	depth, from := 0, 0
	flush := func(expr string) {
		expr = strings.TrimSpace(expr)
		if i := strings.LastIndex(strings.ToUpper(expr), " AS "); i >= 0 {
			outs = append(outs, strings.TrimSpace(expr[i+4:]))
			return
		}
		if i := strings.LastIndexByte(expr, '.'); i >= 0 {
			expr = expr[i+1:]
		}
		outs = append(outs, expr)
	}
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				flush(list[from:i])
				from = i + 1
			}
		}
	}
	flush(list[from:])
	return outs
}

// dbTags lists the db tags a struct scan fills, embedded structs included.
func dbTags(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			out = append(out, dbTags(f.Type)...)
			continue
		}
		if tag := f.Tag.Get("db"); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// TestAIStoreShapeSelectsFillEveryFieldTheyScanInto.
//
// The store's handle is sqlx.Unsafe (store.go): a column with no field is ignored and a field with no
// column is LEFT ZERO, silently. So a SELECT that forgets api_key_last4 does not fail — it reports
// every key as having no last four, and the panel shows every provider as keyless.
//
// MUTATIONS IT CATCHES: dropping or misaliasing a column of a scanned SELECT (`AS api_last4`); a new
// entity field with a db tag its SELECT never produces.
func TestAIStoreShapeSelectsFillEveryFieldTheyScanInto(t *testing.T) {
	for name, c := range map[string]struct {
		q   string
		typ reflect.Type
	}{
		"selectAISettings":  {selectAISettings, reflect.TypeOf(entity.AISettings{})},
		"selectAIProviders": {selectAIProviders, reflect.TypeOf(entity.AIProvider{})},
		"selectAIModels":    {selectAIModels, reflect.TypeOf(entity.AIModel{})},
		"selectAIRoutes":    {selectAIRoutes, reflect.TypeOf(routeRow{})},
		"spendByProvider":   {spendByProvider, reflect.TypeOf(entity.AISpendByProvider{})},
		"spendByActor":      {spendByActor, reflect.TypeOf(entity.AISpendByActor{})},
	} {
		got := selectOutputs(t, c.q)
		want := dbTags(c.typ)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s produces %v, but %s scans %v", name, got, c.typ.Name(), want)
		}
	}
}

// ───────────────────────── the statements say what the contract says ─────────────────────────

// TestAIStoreShapeVersionBumpIsACompareAndSwap.
//
// MUTATION IT CATCHES: dropping `AND config_version = :expected_version` — a stale page would then
// overwrite what somebody else saved a second earlier, and nothing would say so.
func TestAIStoreShapeVersionBumpIsACompareAndSwap(t *testing.T) {
	for name, q := range map[string]string{"checked": bumpConfigVersionChecked, "unchecked": bumpConfigVersion} {
		if !strings.Contains(q, "config_version = config_version + 1") {
			t.Fatalf("the %s bump does not move the version by one", name)
		}
	}
	if !strings.Contains(bumpConfigVersionChecked, "WHERE id = 1 AND config_version = :expected_version") {
		t.Fatal("the checked bump must compare the version it moves")
	}
	if strings.Contains(bumpConfigVersion, ":expected_version") {
		t.Fatal("the unchecked bump (key writes) must not compare: a key write is never blocked by a stale page")
	}
}

// TestAIStoreShapeLedgerUpdatesTouchOnlyTheirState.
//
// MUTATIONS IT CATCHES: dropping `AND status = 'dispatching'` from FinishCall (a second Finish — a
// retry, a late goroutine — would overwrite the first, or un-sweep a swept row); pricing a row that is
// not `accepted` (a de-duplicated collect would price the same call twice); a literal drifting from
// the entity vocabulary (the row would be written in a status nothing else reads).
func TestAIStoreShapeLedgerUpdatesTouchOnlyTheirState(t *testing.T) {
	lit := func(v string) string { return "'" + v + "'" }
	if !strings.Contains(insertAICall, lit(entity.AICallDispatching)+", "+lit(entity.AICostNone)+")") {
		t.Fatal("a ledger row is born dispatching with cost source none")
	}
	if !strings.HasSuffix(strings.TrimSpace(finishAICall), "WHERE id = :id AND status = "+lit(entity.AICallDispatching)) {
		t.Fatal("FinishCall must finalise only a row still dispatching")
	}
	if !strings.HasSuffix(strings.TrimSpace(priceAcceptedAICall),
		"WHERE run_id = :run_id AND attempt_no = :attempt_no AND call_no = :call_no AND status = "+lit(entity.AICallAccepted)) {
		t.Fatal("PriceAcceptedCall must price only the accepted row of (run, attempt, call)")
	}
	for _, want := range []string{
		"SET status = " + lit(entity.AICallUnknown) + ", error_code = " + lit(entity.AICallErrorSweeper),
		"WHERE status = " + lit(entity.AICallDispatching) + " AND occurred_at < :older_than",
	} {
		if !strings.Contains(sweepAIDispatching, want) {
			t.Fatalf("the sweeper lost %q", want)
		}
	}
	// Both finalisations share ONE SET list, so the collect can never write a column the direct finish
	// forgets, or the reverse.
	if !strings.Contains(finishAICall, aiCallEndSet) || !strings.Contains(priceAcceptedAICall, aiCallEndSet) {
		t.Fatal("FinishCall and PriceAcceptedCall must share aiCallEndSet")
	}
	if strings.Contains(aiCallEndSet, "status            = COALESCE") || !strings.Contains(aiCallEndSet, "cost_usd          = COALESCE(:cost_usd, cost_usd)") {
		t.Fatal("status is always written; every nullable column keeps its value when the finish does not name one")
	}
}

// TestAIStoreShapeSpendReportKeepsUnknownUnknown.
//
// MUTATIONS IT CATCHES: COALESCE(SUM(cost_usd), 0) (an unpriced provider would read $0.00 — the
// exact lie 02-PLAN §7 forbids); joining raw ai_provider_cost_daily rows onto raw ledger rows (their
// number multiplied by our call count and ours by their day count); a status dropped from the unpriced
// list; a report without the day_local period.
func TestAIStoreShapeSpendReportKeepsUnknownUnknown(t *testing.T) {
	for name, q := range map[string]string{"by provider": spendByProvider, "by actor": spendByActor} {
		up := strings.ToUpper(q)
		if strings.Contains(up, "COALESCE") || strings.Contains(up, "IFNULL") {
			t.Fatalf("the report %s folds a NULL: unknown must stay unknown", name)
		}
		if !strings.Contains(q, "WHERE day_local BETWEEN :from_day AND :to_day") {
			t.Fatalf("the report %s is not bounded by the day_local period", name)
		}
	}
	if !strings.Contains(spendByProvider,
		"cost_usd IS NULL AND status IN ('"+entity.AICallOK+"','"+entity.AICallChargedFailed+"','"+entity.AICallUnknown+"')") {
		t.Fatal("unpriced = cost_usd IS NULL AND status IN ('ok','charged_failed','unknown') (02-PLAN A1)")
	}
	if !strings.Contains(spendByProvider, "SELECT provider_key, SUM(amount_usd) AS their_usd\n\t\tFROM ai_provider_cost_daily\n\t\tWHERE day BETWEEN :from_day AND :to_day\n\t\tGROUP BY provider_key\n\t) c") {
		t.Fatal("their number must be summed per provider inside its own derived table before the join")
	}
	if strings.Count(spendByProvider, "GROUP BY provider_key") != 2 {
		t.Fatal("both sides of the provider report aggregate per provider before they meet")
	}
	if !strings.Contains(spendByProvider, "LEFT JOIN") {
		t.Fatal("a provider we spent on but that reports nothing must still appear (their number NULL)")
	}
}

// TestAIStoreShapeKeyStatementsTouchOnlyTheirSlot.
//
// MUTATION IT CATCHES: the admin statement writing api_key_* (or the reverse): saving the
// reconciliation key would replace the generation key with a key that cannot generate.
func TestAIStoreShapeKeyStatementsTouchOnlyTheirSlot(t *testing.T) {
	api, ok := keyWriteSQL(entity.AIKeyAPI)
	if !ok || strings.Contains(api, "admin_key") || !strings.Contains(api, "api_key_enc = :enc") {
		t.Fatal("the api key statement must write the api slot and only it")
	}
	adm, ok := keyWriteSQL(entity.AIKeyAdmin)
	if !ok || strings.Contains(adm, "api_key") || !strings.Contains(adm, "admin_key_enc = :enc") {
		t.Fatal("the admin key statement must write the admin slot and only it")
	}
	if _, ok := keyWriteSQL("root"); ok {
		t.Fatal("an unknown key kind must have no statement")
	}
}

// ───────────────────────── behaviour through the fake ─────────────────────────

// TestAIStoreShapeConfigWritesBumpTheVersionFirst.
//
// MUTATIONS IT CATCHES: a config write that forgets the bump (the registry never reloads it); a bump
// that is not the FIRST statement of its transaction (writers then lock rows in different orders and
// deadlock); SetRoute inserting positions the caller sent instead of 1..n.
func TestAIStoreShapeConfigWritesBumpTheVersionFirst(t *testing.T) {
	ctx := context.Background()
	onGet := func(dest any, _ string, _ []any) error { *(dest.(*int)) = 1; return nil }

	type step struct {
		name string
		call func(s *Store) error
		want []string
	}
	for _, st := range []step{
		{"UpdateProvider", func(s *Store) error {
			return s.UpdateProvider(ctx, "fal", entity.AIProviderPatch{Enabled: ptr(false)}, 7, "jeka")
		}, []string{"bumpConfigVersionChecked", "countAIProvider", "updateAIProviderEnabled"}},
		{"SetProviderKey", func(s *Store) error {
			return s.SetProviderKey(ctx, "fal", entity.AIKeyAdmin, []byte{9, 9}, "wxyz", "jeka")
		}, []string{"bumpConfigVersion", "countAIProvider", "setAIProviderAdminKey"}},
		{"SetDefaults", func(s *Store) error {
			return s.SetDefaults(ctx, entity.AIDefaultsPatch{ImageProviderKey: ptr("google")}, 7, "jeka")
		}, []string{"bumpConfigVersionChecked", "updateAIDefaults"}},
		{"SetRoute", func(s *Store) error {
			return s.SetRoute(ctx, entity.AIPurposeThreed, []entity.AIRouteCandidate{
				{Position: 9, ProviderKey: "meshy"}, {Position: 3, ProviderKey: "fal", Model: " fal-ai/trellis "},
			}, 7, "jeka")
		}, []string{"bumpConfigVersionChecked", "deleteAIRoute", "insertAIRouteCandidate", "insertAIRouteCandidate"}},
		{"UpsertModel", func(s *Store) error {
			return s.UpsertModel(ctx, entity.AIModel{ProviderKey: "openrouter", Model: "x/y", Kind: "chat"}, "jeka")
		}, []string{"bumpConfigVersion", "upsertAIModel"}},
	} {
		db := &recDB{onGet: onGet}
		tx := 0
		if err := st.call(newRecStore(db, &tx)); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if tx != 1 {
			t.Fatalf("%s ran %d transactions, want 1", st.name, tx)
		}
		if got := sequence(t, db); !slices.Equal(got, st.want) {
			t.Fatalf("%s sent %v, want %v", st.name, got, st.want)
		}
		if st.want[0] == "bumpConfigVersionChecked" {
			if v := argOf(t, bumpConfigVersionChecked, db.calls[0].args, "expected_version"); v != uint64(7) {
				t.Fatalf("%s compared against %v, want the caller's expected version 7", st.name, v)
			}
		}
		if st.name == "SetRoute" {
			var got []string
			for _, c := range db.calls[2:] {
				got = append(got, argOf(t, insertAIRouteCandidate, c.args, "provider_key").(string)+"@"+
					argOf(t, insertAIRouteCandidate, c.args, "model").(string)+"#"+
					string(rune('0'+argOf(t, insertAIRouteCandidate, c.args, "position").(int))))
			}
			if want := []string{"fal@fal-ai/trellis#1", "meshy@#2"}; !slices.Equal(got, want) {
				t.Fatalf("SetRoute wrote %v, want %v (ordered by Position, renumbered 1..n, trimmed)", got, want)
			}
		}
		if st.name == "SetProviderKey" {
			c := findCall(t, db, "setAIProviderAdminKey")
			if enc := argOf(t, setAIProviderAdminKey, c.args, "enc"); !reflect.DeepEqual(enc, []byte{9, 9}) {
				t.Fatalf("the ciphertext bound is %v", enc)
			}
			if at := argOf(t, setAIProviderAdminKey, c.args, "at"); at != fixedNow {
				t.Fatalf("the key's updated_at is %v, want the store clock", at)
			}
		}
	}
}

// TestAIStoreShapeClearingAKeyWritesNullAndStillSaysWho.
//
// MUTATION IT CATCHES: binding an empty []byte (a "set" key that can never be opened) instead of
// NULL, or forgetting who cleared it.
func TestAIStoreShapeClearingAKeyWritesNullAndStillSaysWho(t *testing.T) {
	for _, enc := range [][]byte{nil, {}} {
		db := &recDB{onGet: func(dest any, _ string, _ []any) error { *(dest.(*int)) = 1; return nil }}
		if err := newRecStore(db, nil).SetProviderKey(context.Background(), "openai", entity.AIKeyAPI, enc, "abcd", "jeka"); err != nil {
			t.Fatal(err)
		}
		c := findCall(t, db, "setAIProviderAPIKey")
		for _, n := range []string{"enc", "last4"} {
			if v := argOf(t, setAIProviderAPIKey, c.args, n); v != nil {
				t.Fatalf("clearing binds :%s = %#v, want NULL", n, v)
			}
		}
		if by := argOf(t, setAIProviderAPIKey, c.args, "by"); by != "jeka" {
			t.Fatalf("clearing must record who cleared, got %v", by)
		}
	}
}

// TestAIStoreShapeStalePageIsAConflictAndWritesNothing.
//
// MUTATION IT CATCHES: treating 0 rows from the checked bump as success (the stale write lands).
func TestAIStoreShapeStalePageIsAConflictAndWritesNothing(t *testing.T) {
	db := &recDB{
		onExec: func(string, []any) (sql.Result, error) { return recResult{rows: 0}, nil },
		onGet:  func(dest any, _ string, _ []any) error { *(dest.(*int)) = 1; return nil }, // the singleton is there
	}
	err := newRecStore(db, nil).UpdateProvider(context.Background(), "fal",
		entity.AIProviderPatch{Enabled: ptr(true)}, 3, "jeka")
	if !errors.Is(err, entity.ErrAIVersionConflict) {
		t.Fatalf("a stale version returned %v, want ErrAIVersionConflict", err)
	}
	if got, want := sequence(t, db), []string{"bumpConfigVersionChecked", "countAISettings"}; !slices.Equal(got, want) {
		t.Fatalf("a stale page sent %v, want %v and nothing after", got, want)
	}
}

// TestAIStoreShapeMissingSingletonIsRecreatedNotAWedge.
//
// MUTATION IT CATCHES: dropping the re-create. GetConfig reads a missing singleton as version 1, so
// without it every write would answer «somebody saved first» forever, and no reload could fix it.
func TestAIStoreShapeMissingSingletonIsRecreatedNotAWedge(t *testing.T) {
	bumps := 0
	db := &recDB{
		onExec: func(q string, _ []any) (sql.Result, error) {
			if strings.HasPrefix(strings.TrimSpace(q), "UPDATE ai_settings SET config_version") {
				bumps++
				if bumps == 1 {
					return recResult{rows: 0}, nil // the row is gone
				}
			}
			return recResult{rows: 1}, nil
		},
		onGet: func(dest any, q string, _ []any) error {
			n := 1
			if strings.Contains(q, "FROM ai_settings") {
				n = 0
			}
			*(dest.(*int)) = n
			return nil
		},
	}
	err := newRecStore(db, nil).SetDefaults(context.Background(),
		entity.AIDefaultsPatch{ChatProviderKey: ptr("anthropic")}, 1, "jeka")
	if err != nil {
		t.Fatalf("a missing singleton wedged the write: %v", err)
	}
	want := []string{"bumpConfigVersionChecked", "countAISettings", "ensureAISettings", "bumpConfigVersionChecked", "updateAIDefaults"}
	if got := sequence(t, db); !slices.Equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

// TestAIStoreShapeRefusalsNeverReachTheDatabase.
//
// MUTATIONS IT CATCHES: dropping a validator (an unknown provider or purpose, a default that cannot
// serve its capability, an empty route, a route to a provider that cannot serve the purpose, a key kind
// that is neither api nor admin) — the first two would write vocabulary the Go side does not know.
func TestAIStoreShapeRefusalsNeverReachTheDatabase(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(s *Store) error{
		"unknown provider": func(s *Store) error {
			return s.UpdateProvider(ctx, "openai2", entity.AIProviderPatch{Enabled: ptr(true)}, 1, "j")
		},
		"empty provider patch": func(s *Store) error { return s.UpdateProvider(ctx, "fal", entity.AIProviderPatch{}, 1, "j") },
		"unknown key kind":     func(s *Store) error { return s.SetProviderKey(ctx, "fal", "root", []byte{1}, "abcd", "j") },
		"key too long": func(s *Store) error {
			return s.SetProviderKey(ctx, "fal", entity.AIKeyAPI, make([]byte, 2049), "abcd", "j")
		},
		"empty defaults": func(s *Store) error { return s.SetDefaults(ctx, entity.AIDefaultsPatch{}, 1, "j") },
		"chat default is meshy": func(s *Store) error {
			return s.SetDefaults(ctx, entity.AIDefaultsPatch{ChatProviderKey: ptr("meshy")}, 1, "j")
		},
		"image default is anth.": func(s *Store) error {
			return s.SetDefaults(ctx, entity.AIDefaultsPatch{ImageProviderKey: ptr("anthropic")}, 1, "j")
		},
		"unknown purpose": func(s *Store) error { return s.SetRoute(ctx, "image.flat", []entity.AIRouteCandidate{{}}, 1, "j") },
		"empty route":     func(s *Store) error { return s.SetRoute(ctx, "vector", nil, 1, "j") },
		"route too long": func(s *Store) error {
			return s.SetRoute(ctx, "vector", make([]entity.AIRouteCandidate, maxRouteCandidates+1), 1, "j")
		},
		"route cannot serve": func(s *Store) error {
			return s.SetRoute(ctx, "image.extend", []entity.AIRouteCandidate{{ProviderKey: "openrouter"}}, 1, "j")
		},
		"route model too long": func(s *Store) error {
			return s.SetRoute(ctx, "vector", []entity.AIRouteCandidate{{Model: strings.Repeat("m", 129)}}, 1, "j")
		},
		"model kind not served": func(s *Store) error {
			return s.UpsertModel(ctx, entity.AIModel{ProviderKey: "anthropic", Model: "x", Kind: "image"}, "j")
		},
		"model kind unknown": func(s *Store) error {
			return s.UpsertModel(ctx, entity.AIModel{ProviderKey: "openai", Model: "x", Kind: "audio"}, "j")
		},
		"model empty": func(s *Store) error {
			return s.UpsertModel(ctx, entity.AIModel{ProviderKey: "openai", Model: "  ", Kind: "chat"}, "j")
		},
		"report bad day":  func(s *Store) error { _, err := s.SpendReport(ctx, "2026-9-1", "2026-09-27"); return err },
		"report reversed": func(s *Store) error { _, err := s.SpendReport(ctx, "2026-09-27", "2026-09-01"); return err },
	} {
		db := &recDB{}
		tx := 0
		err := call(newRecStore(db, &tx))
		var v *entity.ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("%s: got %v, want a field violation", name, err)
		}
		if tx != 0 || len(db.calls) != 0 {
			t.Fatalf("%s reached the database (%d tx, %d statements) before refusing", name, tx, len(db.calls))
		}
	}
}

// ───────────────────────── the ledger ─────────────────────────

// TestAIStoreShapeBeginCallBindsTheRowItPromises.
//
// MUTATIONS IT CATCHES: binding "" for an absent fallback (a chat row would read as a fallback from
// nowhere); call_no 0 reaching the column (the UNIQUE key would then collide with a real call 0);
// the id not coming from LastInsertId; a provider's long model name failing the INSERT instead of
// being clipped (1406 = a paid call with no row).
func TestAIStoreShapeBeginCallBindsTheRowItPromises(t *testing.T) {
	db := &recDB{onExec: func(string, []any) (sql.Result, error) { return recResult{rows: 1, id: 4242}, nil }}
	st := sampleStart()
	st.FallbackFrom, st.CallNo, st.RunID, st.AttemptNo = "", 0, nil, nil
	st.Model = strings.Repeat("é", 200)
	st.OccurredAt = fixedNow.In(time.FixedZone("Warsaw", 2*3600))
	id, err := newRecStore(db, nil).BeginCall(context.Background(), st)
	if err != nil || id != 4242 {
		t.Fatalf("BeginCall = %d, %v; want the inserted id 4242", id, err)
	}
	c := findCall(t, db, "insertAICall")
	checks := map[string]any{
		"fallback_from": nil, "call_no": 1, "run_id": nil, "attempt_no": nil, "actor_admin_id": 7,
		"occurred_at": fixedNow, "day_local": "2026-09-27", "purpose": entity.AIPurposeThreed,
	}
	for n, want := range checks {
		if got := argOf(t, insertAICall, c.args, n); !reflect.DeepEqual(got, want) {
			t.Fatalf(":%s = %#v, want %#v", n, got, want)
		}
	}
	if got := argOf(t, insertAICall, c.args, "occurred_at").(time.Time); got.Location() != time.UTC {
		t.Fatal("occurred_at must be bound in UTC")
	}
	if got := argOf(t, insertAICall, c.args, "model").(string); len([]rune(got)) != widthModel {
		t.Fatalf("model clipped to %d characters, want %d", len([]rune(got)), widthModel)
	}
	for name, bad := range map[string]entity.AICallStart{
		"no time":     {DayLocal: "2026-09-27", ProviderKey: "fal", Purpose: "threed"},
		"no day":      {OccurredAt: fixedNow, ProviderKey: "fal", Purpose: "threed"},
		"no provider": {OccurredAt: fixedNow, DayLocal: "2026-09-27", Purpose: "threed"},
		"call 256":    {OccurredAt: fixedNow, DayLocal: "2026-09-27", ProviderKey: "fal", Purpose: "threed", CallNo: 256},
	} {
		if _, err := beginCallParams(bad); err == nil {
			t.Fatalf("%s: a row the columns cannot hold was accepted", name)
		}
	}
}

// TestAIStoreShapeFinishBindsNullForWhatItDoesNotKnow.
//
// MUTATIONS IT CATCHES: binding "" or 0 for an absent value — COALESCE keeps only on NULL, so an ""
// request id at the collect would ERASE the id the submit wrote, and a 0 cost would turn an unknown
// price into a free call; finishing into `dispatching`; an unknown cost source reaching the column.
func TestAIStoreShapeFinishBindsNullForWhatItDoesNotKnow(t *testing.T) {
	db := &recDB{onExec: func(string, []any) (sql.Result, error) { return recResult{rows: 0}, nil }}
	s := newRecStore(db, nil)
	err := s.FinishCall(context.Background(), 12, entity.AICallEnd{Status: entity.AICallFailed})
	if err != nil {
		t.Fatalf("finishing a row that already left dispatching must be a quiet no-op, got %v", err)
	}
	c := findCall(t, db, "finishAICall")
	for _, n := range []string{"error_code", "http_status", "engaged", "request_id", "model_actual",
		"prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "units", "unit",
		"cost_usd", "cost_source", "price_version", "latency_ms"} {
		if v := argOf(t, finishAICall, c.args, n); v != nil {
			t.Fatalf(":%s = %#v for an absent value, want NULL", n, v)
		}
	}
	if v := argOf(t, finishAICall, c.args, "id"); v != int64(12) {
		t.Fatalf(":id = %#v", v)
	}
	full, err := endCallParams(sampleEnd(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !full["cost_usd"].(decimal.Decimal).Equal(decimal.RequireFromString("0.053")) || full["engaged"] != true {
		t.Fatalf("a known cost / engaged flag must be bound as such: %v %v", full["cost_usd"], full["engaged"])
	}
	for name, end := range map[string]entity.AICallEnd{
		"into dispatching": {Status: entity.AICallDispatching},
		"unknown status":   {Status: "delivered"},
		"unknown source":   {Status: entity.AICallOK, CostSource: "backfill"},
	} {
		if _, err := endCallParams(end, fixedNow); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := s.PriceAcceptedCall(context.Background(), 41, 2, 1, entity.AICallEnd{Status: entity.AICallAccepted}); err == nil {
		t.Fatal("pricing an accepted call into accepted must be refused")
	}
}

// TestAIStoreShapePriceAcceptedAddressesTheDesignCall.
//
// MUTATION IT CATCHES: binding the arguments to the wrong keys (attempt into call_no) — the collect
// would price another call's row, or none.
func TestAIStoreShapePriceAcceptedAddressesTheDesignCall(t *testing.T) {
	db := &recDB{}
	end := sampleEnd()
	if err := newRecStore(db, nil).PriceAcceptedCall(context.Background(), 41, 2, 0, end); err != nil {
		t.Fatal(err)
	}
	c := findCall(t, db, "priceAcceptedAICall")
	for n, want := range map[string]any{"run_id": 41, "attempt_no": 2, "call_no": 1, "status": "ok", "request_id": "req"} {
		if got := argOf(t, priceAcceptedAICall, c.args, n); got != want {
			t.Fatalf(":%s = %#v, want %#v", n, got, want)
		}
	}
}

// TestAIStoreShapeSweepReportsWhatItSwept.
//
// MUTATION IT CATCHES: swapping :older_than and :finished_at (every fresh row would be swept), or
// returning 0 instead of the rows affected (the sweeper's log would never say anything).
func TestAIStoreShapeSweepReportsWhatItSwept(t *testing.T) {
	db := &recDB{onExec: func(string, []any) (sql.Result, error) { return recResult{rows: 3}, nil }}
	cut := fixedNow.Add(-15 * time.Minute)
	n, err := newRecStore(db, nil).SweepDispatching(context.Background(), cut)
	if err != nil || n != 3 {
		t.Fatalf("SweepDispatching = %d, %v; want 3", n, err)
	}
	c := findCall(t, db, "sweepAIDispatching")
	if got := argOf(t, sweepAIDispatching, c.args, "older_than"); got != cut {
		t.Fatalf(":older_than = %v, want %v", got, cut)
	}
	if got := argOf(t, sweepAIDispatching, c.args, "finished_at"); got != fixedNow {
		t.Fatalf(":finished_at = %v, want the store clock", got)
	}
}

// ───────────────────────── reads ─────────────────────────

// TestAIStoreShapeGetConfigReadsTheVersionFirst.
//
// MUTATIONS IT CATCHES: reading ai_settings AFTER the other tables (a write between the reads could
// pair old data with the new version, and the poller would never reload it); failing — or reporting
// version 0 — on a missing singleton; a missing design_settings row read as UTC (the ledger's days
// would move by one or two hours against the design band's); routes not grouped per purpose or not
// ordered by position; providers in table order rather than the panel's.
func TestAIStoreShapeGetConfigReadsTheVersionFirst(t *testing.T) {
	db := &recDB{
		onGet: func(dest any, q string, _ []any) error { return sql.ErrNoRows },
		onSelect: func(dest any, q string, _ []any) error {
			switch d := dest.(type) {
			case *[]entity.AIProvider:
				*d = []entity.AIProvider{{Key: "recraft"}, {Key: "zeta"}, {Key: "openai"}, {Key: "fal"}}
			case *[]routeRow:
				*d = []routeRow{
					{"vector", entity.AIRouteCandidate{Position: 2, ProviderKey: "recraft"}},
					{"vector", entity.AIRouteCandidate{Position: 1}},
					{"chat.note_markdown", entity.AIRouteCandidate{Position: 1, ProviderKey: "openrouter"}},
				}
			}
			return nil
		},
	}
	cfg, err := newRecStore(db, nil).GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := sequence(t, db); got[0] != "selectAISettings" {
		t.Fatalf("GetConfig read %v; the version must be read first", got)
	}
	if cfg.Settings != defaultSettings() || cfg.Settings.ConfigVersion != 1 {
		t.Fatalf("a missing singleton reads as %+v, want 0373's defaults (version 1)", cfg.Settings)
	}
	if cfg.BudgetTimezone != "Europe/Warsaw" {
		t.Fatalf("a missing design_settings row reads as %q, want Europe/Warsaw", cfg.BudgetTimezone)
	}
	var keys []string
	for _, p := range cfg.Providers {
		keys = append(keys, p.Key)
	}
	if want := []string{"openai", "fal", "recraft", "zeta"}; !slices.Equal(keys, want) {
		t.Fatalf("providers %v, want the vocabulary order %v with unknown keys last", keys, want)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Purpose != "chat.note_markdown" || cfg.Routes[1].Purpose != "vector" {
		t.Fatalf("routes %+v, want one per purpose in vocabulary order", cfg.Routes)
	}
	if v := cfg.Routes[1].Candidates; len(v) != 2 || v[0].Position != 1 || v[0].ProviderKey != "" || v[1].ProviderKey != "recraft" {
		t.Fatalf("vector candidates %+v, want primary (default provider) then recraft", v)
	}
}

// TestAIStoreShapeSpendTotalsKeepNull.
//
// MUTATION IT CATCHES: starting the total from a VALID zero — a period with only unpriced calls would
// then report «$0.00 spent» instead of unknown.
func TestAIStoreShapeSpendTotalsKeepNull(t *testing.T) {
	none := spendTotals([]entity.AISpendByProvider{{ProviderKey: "fal", Calls: 2, Unpriced: 2}})
	if none.TotalUSD.Valid {
		t.Fatalf("a period without one priced call totals %v, want unknown", none.TotalUSD.Decimal)
	}
	mixed := spendTotals([]entity.AISpendByProvider{
		{ProviderKey: "fal", OurUSD: decimal.NewNullDecimal(decimal.RequireFromString("1.20")), Calls: 1},
		{ProviderKey: "meshy", Calls: 4, Failed: 1, Unpriced: 3},
		{ProviderKey: "openrouter", OurUSD: decimal.NewNullDecimal(decimal.RequireFromString("0.053")), Calls: 2, Failed: 1},
	})
	if !mixed.TotalUSD.Valid || !mixed.TotalUSD.Decimal.Equal(decimal.RequireFromString("1.253")) {
		t.Fatalf("total %v, want 1.253", mixed.TotalUSD)
	}
	if mixed.Calls != 7 || mixed.Failed != 2 || mixed.Unpriced != 3 {
		t.Fatalf("counts %d/%d/%d, want 7/2/3", mixed.Calls, mixed.Failed, mixed.Unpriced)
	}

	db := &recDB{onSelect: func(dest any, _ string, _ []any) error {
		if d, ok := dest.(*[]entity.AISpendByProvider); ok {
			*d = []entity.AISpendByProvider{{ProviderKey: "fal", Calls: 1, Unpriced: 1}}
		}
		return nil
	}}
	rep, err := newRecStore(db, nil).SpendReport(context.Background(), "2026-09-01", "2026-09-27")
	if err != nil {
		t.Fatal(err)
	}
	if rep.FromDay != "2026-09-01" || rep.ToDay != "2026-09-27" || rep.TotalUSD.Valid || rep.Calls != 1 {
		t.Fatalf("report %+v", rep)
	}
	if got := sequence(t, db); !slices.Equal(got, []string{"spendByProvider", "spendByActor"}) {
		t.Fatalf("the report sent %v", got)
	}
	if got := argOf(t, spendByProvider, db.calls[0].args, "to_day"); got != "2026-09-27" {
		t.Fatalf(":to_day = %v", got)
	}
}

// TestAIStoreShapeCostDailyDefaultsAndRefusals.
//
// MUTATION IT CATCHES: an empty currency reaching CHAR(3) NOT NULL as ” (MySQL stores it; every
// comparison with 'USD' then fails), or a zero fetched_at stored as 0000-00-00.
func TestAIStoreShapeCostDailyDefaultsAndRefusals(t *testing.T) {
	db := &recDB{}
	tx := 0
	err := newRecStore(db, &tx).UpsertCostDaily(context.Background(), []entity.AICostDaily{
		{ProviderKey: "openai", Day: "2026-09-26", AmountUSD: decimal.RequireFromString("3.5")},
		{ProviderKey: "anthropic", Day: "2026-09-26", AmountUSD: decimal.RequireFromString("1"), Currency: "usd"},
	})
	if err != nil || tx != 1 || len(db.calls) != 2 {
		t.Fatalf("UpsertCostDaily: %v, %d tx, %d statements", err, tx, len(db.calls))
	}
	for _, c := range db.calls {
		if got := argOf(t, upsertAICostDaily, c.args, "currency"); got != "USD" {
			t.Fatalf(":currency = %v, want USD", got)
		}
		if got := argOf(t, upsertAICostDaily, c.args, "fetched_at"); got != fixedNow {
			t.Fatalf(":fetched_at = %v, want the store clock", got)
		}
	}
	if err := newRecStore(&recDB{}, nil).UpsertCostDaily(context.Background(), nil); err != nil {
		t.Fatal("no rows is not an error")
	}
	for name, r := range map[string]entity.AICostDaily{
		"unknown provider": {ProviderKey: "aws", Day: "2026-09-26"},
		"bad day":          {ProviderKey: "openai", Day: "26.09.2026"},
		"bad currency":     {ProviderKey: "openai", Day: "2026-09-26", Currency: "EURO"},
	} {
		if _, err := costDailyParams(r, fixedNow); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
