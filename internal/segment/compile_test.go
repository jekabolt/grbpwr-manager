package segment

import (
	"errors"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ---- helpers ------------------------------------------------------------

func leaf(field, op string, vals ...string) entity.SegmentNode {
	return entity.SegmentNode{Field: field, Operator: op, Values: vals}
}

func group(op entity.SegmentOp, children ...entity.SegmentNode) entity.SegmentNode {
	return entity.SegmentNode{Op: op, Children: children}
}

func pred(root entity.SegmentNode) entity.SegmentPredicate {
	return entity.SegmentPredicate{Root: &root}
}

func mustCompile(t *testing.T, p entity.SegmentPredicate) Compiled {
	t.Helper()
	c, err := Compile(p)
	if err != nil {
		t.Fatalf("Compile: unexpected error: %v", err)
	}
	return c
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---- A: injection --------------------------------------------------------

func TestA_Injection(t *testing.T) {
	t.Run("field with SQL payload => ErrUnknownField", func(t *testing.T) {
		_, err := Compile(pred(leaf("email; DROP TABLE storefront_account; --", "eq", "x")))
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("want ErrUnknownField, got %v", err)
		}
	})

	t.Run("field breakout attempt => ErrUnknownField", func(t *testing.T) {
		_, err := Compile(pred(leaf("account_tier) OR 1=1--", "eq", "member")))
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("want ErrUnknownField, got %v", err)
		}
	})

	t.Run("operator with SQL payload => ErrUnknownOperator", func(t *testing.T) {
		_, err := Compile(pred(leaf("account_tier", "= 1 OR 1=1", "member")))
		if !errors.Is(err, ErrUnknownOperator) {
			t.Fatalf("want ErrUnknownOperator, got %v", err)
		}
	})

	// A malicious value on a FREE-TEXT (string) field is bound verbatim as :p0 and
	// never interpolated into SQL text. This is the core "caller bytes never reach
	// SQL text" proof.
	t.Run("malicious value on string field bound verbatim", func(t *testing.T) {
		payload := "zz' OR '1'='1"
		c := mustCompile(t, pred(leaf("default_country", "eq", payload)))
		if c.SQL != "sa.default_country = :p0" {
			t.Fatalf("SQL = %q, want %q", c.SQL, "sa.default_country = :p0")
		}
		if strings.Contains(c.SQL, "OR") || strings.Contains(c.SQL, "1'='1") {
			t.Fatalf("payload leaked into SQL: %q", c.SQL)
		}
		if got := c.Params["p0"]; got != payload {
			t.Fatalf("params[p0] = %#v, want verbatim %q", got, payload)
		}
	})

	// The same payload on an ENUM field is rejected outright (even stronger: the
	// value never reaches SQL and Compile fails typed).
	t.Run("malicious value on enum field => ErrBadValue", func(t *testing.T) {
		_, err := Compile(pred(leaf("account_tier", "eq", "member' OR '1'='1")))
		if !errors.Is(err, ErrBadValue) {
			t.Fatalf("want ErrBadValue, got %v", err)
		}
	})

	t.Run("padded field name => ErrUnknownField (no trimming)", func(t *testing.T) {
		_, err := Compile(pred(leaf(" account_tier ", "eq", "member")))
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("want ErrUnknownField, got %v", err)
		}
	})
}

// ---- B: every operator happy-path ---------------------------------------

func TestB_Operators(t *testing.T) {
	ageExpr := "TIMESTAMPDIFF(YEAR, sa.birth_date, CURDATE())"

	t.Run("comparison operators on age", func(t *testing.T) {
		cases := []struct {
			op   string
			want string
		}{
			{"eq", ageExpr + " = :p0"},
			{"neq", ageExpr + " <> :p0"},
			{"lt", ageExpr + " < :p0"},
			{"lte", ageExpr + " <= :p0"},
			{"gt", ageExpr + " > :p0"},
			{"gte", ageExpr + " >= :p0"},
		}
		for _, tc := range cases {
			c := mustCompile(t, pred(leaf("age", tc.op, "30")))
			if c.SQL != tc.want {
				t.Errorf("%s: SQL = %q, want %q", tc.op, c.SQL, tc.want)
			}
			if c.Params["p0"] != 30 {
				t.Errorf("%s: params[p0] = %#v, want 30", tc.op, c.Params["p0"])
			}
		}
	})

	t.Run("in / not_in on account_tier bind one slice param", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("account_tier", "in", "member", "plus", "hacker")))
		if c.SQL != "sa.account_tier IN (:p0)" {
			t.Fatalf("SQL = %q", c.SQL)
		}
		got, ok := c.Params["p0"].([]any)
		if !ok {
			t.Fatalf("params[p0] is %T, want []any", c.Params["p0"])
		}
		if len(got) != 3 {
			t.Fatalf("slice len = %d, want 3", len(got))
		}
		if !reflect.DeepEqual(got, []any{"member", "plus", "hacker"}) {
			t.Fatalf("slice = %#v", got)
		}

		c2 := mustCompile(t, pred(leaf("account_tier", "not_in", "member", "plus")))
		if c2.SQL != "sa.account_tier NOT IN (:p0)" {
			t.Fatalf("SQL = %q", c2.SQL)
		}
		if s := c2.Params["p0"].([]any); len(s) != 2 {
			t.Fatalf("slice len = %d, want 2", len(s))
		}
	})

	t.Run("between on age binds two ordered params", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("age", "between", "18", "35")))
		if c.SQL != ageExpr+" BETWEEN :p0 AND :p1" {
			t.Fatalf("SQL = %q", c.SQL)
		}
		if c.Params["p0"] != 18 || c.Params["p1"] != 35 {
			t.Fatalf("params = %#v", c.Params)
		}
	})

	t.Run("is_set / is_not_set on last_order_at bind zero params", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("last_order_at", "is_set")))
		if c.SQL != "maa.last_order_at IS NOT NULL" {
			t.Fatalf("SQL = %q", c.SQL)
		}
		if len(c.Params) != 0 {
			t.Fatalf("params = %#v, want empty", c.Params)
		}
		c2 := mustCompile(t, pred(leaf("last_order_at", "is_not_set")))
		if c2.SQL != "maa.last_order_at IS NULL" {
			t.Fatalf("SQL = %q", c2.SQL)
		}
	})

	t.Run("in_last_days / older_than_days on registered_at", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("registered_at", "in_last_days", "30")))
		if c.SQL != "sa.created_at >= DATE_SUB(CURDATE(), INTERVAL :p0 DAY)" {
			t.Fatalf("SQL = %q", c.SQL)
		}
		if c.Params["p0"] != 30 {
			t.Fatalf("params[p0] = %#v, want int 30", c.Params["p0"])
		}
		c2 := mustCompile(t, pred(leaf("registered_at", "older_than_days", "90")))
		if c2.SQL != "sa.created_at < DATE_SUB(CURDATE(), INTERVAL :p0 DAY)" {
			t.Fatalf("SQL = %q", c2.SQL)
		}
		if c2.Params["p0"] != 90 {
			t.Fatalf("params[p0] = %#v, want int 90", c2.Params["p0"])
		}
	})

	t.Run("bool eq true/false on subscribe_newsletter binds 1/0", func(t *testing.T) {
		ctrue := mustCompile(t, pred(leaf("subscribe_newsletter", "eq", "true")))
		if ctrue.SQL != "sa.subscribe_newsletter = :p0" || ctrue.Params["p0"] != 1 {
			t.Fatalf("true: SQL=%q params=%#v", ctrue.SQL, ctrue.Params)
		}
		cfalse := mustCompile(t, pred(leaf("subscribe_newsletter", "eq", "false")))
		if cfalse.Params["p0"] != 0 {
			t.Fatalf("false: params[p0] = %#v, want 0", cfalse.Params["p0"])
		}
	})

	t.Run("decimal value on total_spend_eur parses", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("total_spend_eur", "gt", "100.50")))
		if c.SQL != "COALESCE(maa.total_spend_eur,0) > :p0" {
			t.Fatalf("SQL = %q", c.SQL)
		}
		d, ok := c.Params["p0"].(decimal.Decimal)
		if !ok || !d.Equal(decimal.RequireFromString("100.50")) {
			t.Fatalf("params[p0] = %#v, want decimal 100.50", c.Params["p0"])
		}
	})
}

// ---- C: nesting ----------------------------------------------------------

func TestC_Nesting(t *testing.T) {
	t.Run("single leaf has no wrapping parens", func(t *testing.T) {
		c := mustCompile(t, pred(leaf("account_tier", "eq", "member")))
		if c.SQL != "sa.account_tier = :p0" {
			t.Fatalf("SQL = %q", c.SQL)
		}
	})

	t.Run("AND of two leaves", func(t *testing.T) {
		c := mustCompile(t, pred(group(entity.SegmentOpAnd,
			leaf("account_tier", "eq", "member"),
			leaf("order_count", "gte", "1"),
		)))
		want := "(sa.account_tier = :p0 AND COALESCE(maa.order_count,0) >= :p1)"
		if c.SQL != want {
			t.Fatalf("SQL = %q, want %q", c.SQL, want)
		}
	})

	t.Run("OR of two leaves", func(t *testing.T) {
		c := mustCompile(t, pred(group(entity.SegmentOpOr,
			leaf("account_tier", "eq", "member"),
			leaf("account_tier", "eq", "hacker"),
		)))
		want := "(sa.account_tier = :p0 OR sa.account_tier = :p1)"
		if c.SQL != want {
			t.Fatalf("SQL = %q, want %q", c.SQL, want)
		}
	})

	t.Run("AND(OR(a,b),c) preserves precedence via parens", func(t *testing.T) {
		c := mustCompile(t, pred(group(entity.SegmentOpAnd,
			group(entity.SegmentOpOr,
				leaf("account_tier", "eq", "member"),
				leaf("account_tier", "eq", "plus"),
			),
			leaf("order_count", "gte", "1"),
		)))
		want := "((sa.account_tier = :p0 OR sa.account_tier = :p1) AND COALESCE(maa.order_count,0) >= :p2)"
		if c.SQL != want {
			t.Fatalf("SQL = %q, want %q", c.SQL, want)
		}
	})

	t.Run("nil root => 1=1", func(t *testing.T) {
		c := mustCompile(t, entity.SegmentPredicate{Root: nil})
		if c.SQL != "1=1" {
			t.Fatalf("SQL = %q, want 1=1", c.SQL)
		}
		if len(c.Params) != 0 {
			t.Fatalf("params = %#v, want empty", c.Params)
		}
	})
}

// ---- D: caps -------------------------------------------------------------

// nestedGroups wraps leaf in `levels` nested single-child AND groups. The leaf
// then sits at depth levels+1.
func nestedGroups(levels int, l entity.SegmentNode) entity.SegmentNode {
	n := l
	for i := 0; i < levels; i++ {
		n = group(entity.SegmentOpAnd, n)
	}
	return n
}

func TestD_Caps(t *testing.T) {
	okLeaf := leaf("subscribe_newsletter", "eq", "true")

	t.Run("depth 6 ok, depth 7 too deep", func(t *testing.T) {
		// 5 wrapping groups => leaf at depth 6.
		if _, err := Compile(pred(nestedGroups(5, okLeaf))); err != nil {
			t.Fatalf("depth 6 should compile, got %v", err)
		}
		// 6 wrapping groups => leaf at depth 7.
		if _, err := Compile(pred(nestedGroups(6, okLeaf))); !errors.Is(err, ErrTooDeep) {
			t.Fatalf("depth 7: want ErrTooDeep, got %v", err)
		}
	})

	t.Run("nodes 100 ok, 101 too many", func(t *testing.T) {
		mk := func(children int) entity.SegmentPredicate {
			kids := make([]entity.SegmentNode, children)
			for i := range kids {
				kids[i] = okLeaf
			}
			return pred(group(entity.SegmentOpAnd, kids...))
		}
		// root + 99 leaves = 100 nodes.
		if _, err := Compile(mk(99)); err != nil {
			t.Fatalf("100 nodes should compile, got %v", err)
		}
		// root + 100 leaves = 101 nodes.
		if _, err := Compile(mk(100)); !errors.Is(err, ErrTooManyNodes) {
			t.Fatalf("101 nodes: want ErrTooManyNodes, got %v", err)
		}
	})

	t.Run("IN 200 ok, 201 too many", func(t *testing.T) {
		mk := func(n int) entity.SegmentPredicate {
			vals := make([]string, n)
			for i := range vals {
				vals[i] = strconv.Itoa(i)
			}
			return pred(leaf("default_country", "in", vals...))
		}
		if _, err := Compile(mk(200)); err != nil {
			t.Fatalf("IN 200 should compile, got %v", err)
		}
		if _, err := Compile(mk(201)); !errors.Is(err, ErrTooManyInValues) {
			t.Fatalf("IN 201: want ErrTooManyInValues, got %v", err)
		}
	})

	t.Run("empty AND group => ErrEmptyGroup", func(t *testing.T) {
		if _, err := Compile(pred(group(entity.SegmentOpAnd))); !errors.Is(err, ErrEmptyGroup) {
			t.Fatalf("want ErrEmptyGroup, got %v", err)
		}
	})

	t.Run("node with both Op and Field => ErrMalformedNode", func(t *testing.T) {
		bad := entity.SegmentNode{
			Op:       entity.SegmentOpAnd,
			Field:    "account_tier",
			Children: []entity.SegmentNode{okLeaf},
		}
		if _, err := Compile(pred(bad)); !errors.Is(err, ErrMalformedNode) {
			t.Fatalf("want ErrMalformedNode, got %v", err)
		}
	})

	t.Run("leaf carrying children => ErrMalformedNode", func(t *testing.T) {
		bad := entity.SegmentNode{
			Field:    "account_tier",
			Operator: "eq",
			Values:   []string{"member"},
			Children: []entity.SegmentNode{okLeaf},
		}
		if _, err := Compile(pred(bad)); !errors.Is(err, ErrMalformedNode) {
			t.Fatalf("want ErrMalformedNode, got %v", err)
		}
	})

	t.Run("wrong arity => ErrArity", func(t *testing.T) {
		cases := []entity.SegmentNode{
			leaf("age", "eq"),           // eq with 0 values
			leaf("age", "eq", "1", "2"), // eq with 2 values
			leaf("age", "between", "1"), // between with 1 value
			leaf("account_tier", "in"),  // in with 0 values
			leaf("age", "is_set", "1"),  // arity-0 op with a value
		}
		for i, n := range cases {
			if _, err := Compile(pred(n)); !errors.Is(err, ErrArity) {
				t.Errorf("case %d: want ErrArity, got %v", i, err)
			}
		}
	})
}

// ---- E: value typing -----------------------------------------------------

func TestE_ValueTyping(t *testing.T) {
	cases := []struct {
		name string
		node entity.SegmentNode
	}{
		{"enum out of set", leaf("account_tier", "eq", "gold")},
		{"bool not boolean", leaf("subscribe_newsletter", "eq", "maybe")},
		{"date not a date", leaf("registered_at", "lt", "not-a-date")},
		{"birth_month out of range", leaf("birth_month", "eq", "13")},
		{"decimal not numeric", leaf("total_spend_eur", "gt", "abc")},
		{"age out of range", leaf("age", "eq", "999")},
		{"relative days not int", leaf("registered_at", "in_last_days", "soon")},
		{"relative days negative", leaf("registered_at", "in_last_days", "-5")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(pred(tc.node)); !errors.Is(err, ErrBadValue) {
				t.Fatalf("want ErrBadValue, got %v", err)
			}
		})
	}
}

// ---- F: compliance always present ---------------------------------------

const (
	tokActive     = "sa.status = 'active'"
	tokSuppressed = "es.id IS NULL"
)

func topicPtr(t entity.EmailCampaignTopic) *entity.EmailCampaignTopic { return &t }

func TestF_ComplianceAlwaysPresent(t *testing.T) {
	base := mustCompile(t, pred(leaf("account_tier", "eq", "member")))

	t.Run("newsletter topic adds opt-in + active + suppression + user predicate", func(t *testing.T) {
		where, _, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: topicPtr(entity.EmailCampaignTopicNewsletter)})
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{"sa.subscribe_newsletter = 1", tokActive, tokSuppressed, "sa.account_tier = :p0"} {
			if !strings.Contains(where, tok) {
				t.Errorf("where %q missing %q", where, tok)
			}
		}
	})

	t.Run("events topic", func(t *testing.T) {
		where, _, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: topicPtr(entity.EmailCampaignTopicEvents)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(where, "sa.subscribe_events = 1") {
			t.Errorf("missing events opt-in: %q", where)
		}
	})

	t.Run("new_arrivals topic", func(t *testing.T) {
		where, _, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: topicPtr(entity.EmailCampaignTopicNewArrivals)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(where, "sa.subscribe_new_arrivals = 1") {
			t.Errorf("missing new_arrivals opt-in: %q", where)
		}
	})

	t.Run("nil topic: active+suppression present, no subscribe token", func(t *testing.T) {
		where, _, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: nil})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(where, tokActive) || !strings.Contains(where, tokSuppressed) {
			t.Errorf("missing compliance tokens: %q", where)
		}
		if strings.Contains(where, "subscribe_") {
			t.Errorf("nil topic must not add a subscribe_* clause: %q", where)
		}
	})

	t.Run("nil-root predicate still carries compliance", func(t *testing.T) {
		empty := mustCompile(t, entity.SegmentPredicate{Root: nil})
		where, _, err := BuildAudiencePredicate(empty, ComplianceOpts{Topic: topicPtr(entity.EmailCampaignTopicNewsletter)})
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{"(1=1)", tokActive, tokSuppressed, "sa.subscribe_newsletter = 1"} {
			if !strings.Contains(where, tok) {
				t.Errorf("where %q missing %q", where, tok)
			}
		}
	})

	t.Run("unknown topic => ErrUnknownTopic", func(t *testing.T) {
		bad := entity.EmailCampaignTopic("promotions")
		if _, _, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: &bad}); !errors.Is(err, ErrUnknownTopic) {
			t.Fatalf("want ErrUnknownTopic, got %v", err)
		}
	})

	t.Run("compliance binds no params (clone of user params)", func(t *testing.T) {
		where, params, err := BuildAudiencePredicate(base, ComplianceOpts{Topic: topicPtr(entity.EmailCampaignTopicNewsletter)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(params, base.Params) {
			t.Errorf("params = %#v, want clone of %#v", params, base.Params)
		}
		// mutating the returned map must not affect the source Compiled.
		params["injected"] = 1
		if _, ok := base.Params["injected"]; ok {
			t.Errorf("returned params alias the source map")
		}
		_ = where
	})

	// Property test: for N random VALID predicates, a topic-set audience predicate
	// ALWAYS contains all three compliance tokens. Guards a future refactor that
	// drops compliance.
	t.Run("property: compliance present for random valid predicates", func(t *testing.T) {
		r := rand.New(rand.NewSource(0xC0FFEE))
		topics := []entity.EmailCampaignTopic{
			entity.EmailCampaignTopicNewsletter,
			entity.EmailCampaignTopicNewArrivals,
			entity.EmailCampaignTopicEvents,
		}
		for i := 0; i < 400; i++ {
			root := randTree(r, 3)
			c, err := Compile(pred(root))
			if err != nil {
				t.Fatalf("iter %d: random valid predicate failed to compile: %v\nnode=%#v", i, err, root)
			}
			topic := topics[r.Intn(len(topics))]
			where, _, err := BuildAudiencePredicate(c, ComplianceOpts{Topic: &topic})
			if err != nil {
				t.Fatalf("iter %d: BuildAudiencePredicate: %v", i, err)
			}
			topicCol, _ := topicColumn(topic)
			for _, tok := range []string{tokActive, tokSuppressed, topicCol + " = 1"} {
				if !strings.Contains(where, tok) {
					t.Fatalf("iter %d: where %q missing %q", i, where, tok)
				}
			}
		}
	})
}

// randTree builds a random VALID predicate of bounded depth (root at depth 1,
// leaves no deeper than depth+1), well within all caps.
func randTree(r *rand.Rand, depth int) entity.SegmentNode {
	if depth <= 0 || r.Intn(3) == 0 {
		return randLeaf(r)
	}
	op := entity.SegmentOpAnd
	if r.Intn(2) == 0 {
		op = entity.SegmentOpOr
	}
	n := 1 + r.Intn(3)
	kids := make([]entity.SegmentNode, n)
	for i := range kids {
		kids[i] = randTree(r, depth-1)
	}
	return group(op, kids...)
}

var registryFieldNames = sortedKeys(registry)

func randLeaf(r *rand.Rand) entity.SegmentNode {
	field := registryFieldNames[r.Intn(len(registryFieldNames))]
	fd := registry[field]
	ops := sortedKeys(fd.ops)
	op := ops[r.Intn(len(ops))]
	os := opTemplates[op]

	var vals []string
	switch os.arity {
	case 0:
		vals = nil
	case 1:
		vals = []string{randValue(r, fd, os)}
	case 2:
		vals = []string{randValue(r, fd, os), randValue(r, fd, os)}
	case -1:
		k := 1 + r.Intn(3)
		for i := 0; i < k; i++ {
			vals = append(vals, randValue(r, fd, os))
		}
	}
	return leaf(field, op, vals...)
}

func randValue(r *rand.Rand, fd fieldDef, os opSpec) string {
	if os.relDays {
		return strconv.Itoa(r.Intn(365))
	}
	switch fd.vType {
	case vtBool:
		if r.Intn(2) == 0 {
			return "true"
		}
		return "false"
	case vtInt:
		return strconv.Itoa(r.Intn(50))
	case vtAge:
		return strconv.Itoa(r.Intn(120))
	case vtBirthMonth:
		return strconv.Itoa(1 + r.Intn(12))
	case vtDecimal:
		return "12.50"
	case vtDate:
		return "2021-03-15"
	case vtEnum:
		ks := sortedKeys(fd.enumValues)
		return ks[r.Intn(len(ks))]
	case vtString:
		return "US"
	default:
		return "x"
	}
}

// ---- G: determinism + registry self-check --------------------------------

func TestG_Determinism(t *testing.T) {
	p := pred(group(entity.SegmentOpAnd,
		group(entity.SegmentOpOr,
			leaf("account_tier", "in", "member", "plus"),
			leaf("age", "between", "18", "40"),
		),
		leaf("total_spend_eur", "gte", "50"),
		leaf("last_order_at", "is_set"),
	))
	a := mustCompile(t, p)
	b := mustCompile(t, p)
	if a.SQL != b.SQL {
		t.Fatalf("non-deterministic SQL:\n%q\n%q", a.SQL, b.SQL)
	}
	if !reflect.DeepEqual(a.Params, b.Params) {
		t.Fatalf("non-deterministic params:\n%#v\n%#v", a.Params, b.Params)
	}
	// pre-order counter: p0,p1 from the IN+between-lo, etc. verify names are dense.
	for i := 0; i < len(a.Params); i++ {
		if _, ok := a.Params["p"+strconv.Itoa(i)]; !ok {
			t.Errorf("missing dense param p%d in %#v", i, a.Params)
		}
	}
}

func TestG_RegistrySelfCheck(t *testing.T) {
	// Every op referenced by any field must exist in opTemplates.
	for field, fd := range registry {
		for op := range fd.ops {
			if _, ok := opTemplates[op]; !ok {
				t.Errorf("field %q references op %q missing from opTemplates", field, op)
			}
		}
		if fd.vType == vtEnum && len(fd.enumValues) == 0 {
			t.Errorf("enum field %q has empty enumValues", field)
		}
		if fd.vType != vtEnum && len(fd.enumValues) != 0 {
			t.Errorf("non-enum field %q has enumValues set", field)
		}
		// no sqlExpr may contain a ':' (would break sqlx binding).
		if strings.Contains(fd.sqlExpr, ":") {
			t.Errorf("field %q sqlExpr contains ':': %q", field, fd.sqlExpr)
		}
		if fd.sqlExpr == "" {
			t.Errorf("field %q has empty sqlExpr", field)
		}
	}

	// Every colon in a template must be part of a ':%s' bind placeholder; no
	// stray colon (e.g. inside a SQL comment) is allowed.
	verbCount := map[int]int{0: 1, 1: 2, 2: 3, -1: 2}
	for op, os := range opTemplates {
		stripped := strings.ReplaceAll(os.tmpl, ":%s", "")
		if strings.Contains(stripped, ":") {
			t.Errorf("op %q template has a stray colon: %q", op, os.tmpl)
		}
		if strings.Contains(os.tmpl, "--") {
			t.Errorf("op %q template contains a SQL comment: %q", op, os.tmpl)
		}
		if want, got := verbCount[os.arity], strings.Count(os.tmpl, "%s"); want != got {
			t.Errorf("op %q arity %d: template has %d %%s verbs, want %d (%q)", op, os.arity, got, want, os.tmpl)
		}
	}
}
