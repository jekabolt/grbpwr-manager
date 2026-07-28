// Package segment is a PURE, database-free predicate compiler for the email
// segmentation feature. It turns an admin-authored AND/OR predicate tree
// (entity.SegmentPredicate) into a parameterized SQL WHERE fragment plus a bag
// of bound :pN parameters.
//
// Security contract (the whole reason this package exists):
//   - Caller-supplied Field and Operator strings are ONLY ever used as map keys
//     into the compile-time-constant registry / opTemplates below. A miss is a
//     typed error, never string interpolation. No trimming or normalizing is
//     performed: matching is exact.
//   - Every caller-supplied value is bound as a :pN parameter (a monotonic,
//     pre-order-deterministic counter). IN / NOT IN bind the whole list as ONE
//     slice parameter which sqlx.In expands downstream.
//   - The only caller-influenced SQL text is the boolean skeleton (AND/OR/parens)
//     and the generated :pN name sequence.
//
// Alias contract (fixed, the store's FROM/JOINs MUST provide exactly these):
//
//	sa  = storefront_account
//	maa = marketing_account_aggregate   (LEFT JOIN maa ON maa.account_id = sa.id)
//	es  = email_suppression             (LEFT JOIN es  ON es.email = sa.email)
package segment

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// valueType is the parse/validation discriminator for a field's values. Several
// int-like fields carry distinct types (vtAge, vtBirthMonth) so that parseValue
// can range-check them without needing the field name — its signature is fixed
// and does not receive the field.
type valueType int

const (
	vtBool valueType = iota
	vtInt
	vtAge        // int, range-checked 0..150
	vtBirthMonth // int, range-checked 1..12
	vtDecimal
	vtDate
	vtEnum
	vtString
)

// fieldDef is a compile-time-constant description of one queryable field. sqlExpr
// is emitted verbatim (never contains a caller byte and never a ':'). ops is the
// subset of opTemplates keys allowed on this field.
type fieldDef struct {
	sqlExpr    string
	vType      valueType
	enumValues map[string]struct{} // non-empty iff vType == vtEnum
	ops        map[string]struct{}
}

// opSpec is a compile-time-constant operator template.
//
//	arity  1  → exactly one value        (tmpl has 2 %s verbs: expr, param)
//	arity  2  → exactly two values       (tmpl has 3 %s verbs: expr, lo, hi)
//	arity  0  → no values                (tmpl has 1 %s verb:  expr)
//	arity -1  → a list, bound as ONE slice param (tmpl has 2 %s verbs: expr, param)
//
// relDays means the (single) value is a non-negative day count for a relative
// date comparison, parsed as an int regardless of the field's value type.
type opSpec struct {
	tmpl    string
	arity   int
	relDays bool
}

func set(vals ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}

// registry is the fixed field allow-list. Aliases sa./maa. are hard-coded; the
// sqlExprs are verified against the live migrations. No sqlExpr contains a ':'.
var registry = map[string]fieldDef{
	// ---- profile dimensions (storefront_account, alias sa) ----
	"subscribe_newsletter": {
		sqlExpr: "sa.subscribe_newsletter", vType: vtBool, ops: set("eq"),
	},
	"subscribe_new_arrivals": {
		sqlExpr: "sa.subscribe_new_arrivals", vType: vtBool, ops: set("eq"),
	},
	"subscribe_events": {
		sqlExpr: "sa.subscribe_events", vType: vtBool, ops: set("eq"),
	},
	"account_tier": {
		sqlExpr:    "sa.account_tier",
		vType:      vtEnum,
		enumValues: set("member", "plus", "plus_plus", "hacker"),
		ops:        set("eq", "neq", "in", "not_in"),
	},
	"default_country": {
		sqlExpr: "sa.default_country", vType: vtString,
		ops: set("eq", "neq", "in", "not_in", "is_set", "is_not_set"),
	},
	"default_language": {
		sqlExpr: "sa.default_language", vType: vtString,
		ops: set("eq", "neq", "in", "not_in", "is_set", "is_not_set"),
	},
	"shopping_preference": {
		sqlExpr:    "sa.shopping_preference",
		vType:      vtEnum,
		enumValues: set("male", "female", "all"),
		ops:        set("eq", "neq", "in", "not_in"),
	},
	"status": {
		sqlExpr:    "sa.status",
		vType:      vtEnum,
		enumValues: set("active", "frozen", "deleted", "erased"),
		ops:        set("eq", "neq", "in", "not_in"),
	},
	"age": {
		sqlExpr: "TIMESTAMPDIFF(YEAR, sa.birth_date, CURDATE())",
		vType:   vtAge,
		ops:     set("eq", "neq", "lt", "lte", "gt", "gte", "between", "is_set", "is_not_set"),
	},
	"birth_month": {
		sqlExpr: "MONTH(sa.birth_date)",
		vType:   vtBirthMonth,
		ops:     set("eq", "neq", "in", "not_in", "is_set", "is_not_set"),
	},
	"registered_at": {
		sqlExpr: "sa.created_at",
		vType:   vtDate,
		ops:     set("lt", "lte", "gt", "gte", "between", "in_last_days", "older_than_days"),
	},

	// ---- behavioral dimensions (marketing_account_aggregate, alias maa) ----
	// total_spend_eur / order_count use COALESCE so "no orders" reads as 0; is_set
	// is therefore meaningless on them and intentionally NOT in their ops set.
	"total_spend_eur": {
		sqlExpr: "COALESCE(maa.total_spend_eur,0)",
		vType:   vtDecimal,
		ops:     set("eq", "neq", "lt", "lte", "gt", "gte", "between"),
	},
	"order_count": {
		sqlExpr: "COALESCE(maa.order_count,0)",
		vType:   vtInt,
		ops:     set("eq", "neq", "lt", "lte", "gt", "gte", "between"),
	},
	// first/last_order_at stay RAW nullable columns so is_set means "has ever ordered".
	"first_order_at": {
		sqlExpr: "maa.first_order_at",
		vType:   vtDate,
		ops:     set("lt", "lte", "gt", "gte", "between", "in_last_days", "older_than_days", "is_set", "is_not_set"),
	},
	"last_order_at": {
		sqlExpr: "maa.last_order_at",
		vType:   vtDate,
		ops:     set("lt", "lte", "gt", "gte", "between", "in_last_days", "older_than_days", "is_set", "is_not_set"),
	},
}

// opTemplates is the fixed operator allow-list. Every ':' in a template is part
// of a ':%s' bind placeholder (which expands to a valid :pN name); no template
// carries a stray colon or a SQL comment (both break sqlx binding).
var opTemplates = map[string]opSpec{
	"eq":  {tmpl: "%s = :%s", arity: 1},
	"neq": {tmpl: "%s <> :%s", arity: 1},
	"lt":  {tmpl: "%s < :%s", arity: 1},
	"lte": {tmpl: "%s <= :%s", arity: 1},
	"gt":  {tmpl: "%s > :%s", arity: 1},
	"gte": {tmpl: "%s >= :%s", arity: 1},

	"in":     {tmpl: "%s IN (:%s)", arity: -1},
	"not_in": {tmpl: "%s NOT IN (:%s)", arity: -1},

	"between": {tmpl: "%s BETWEEN :%s AND :%s", arity: 2},

	"is_set":     {tmpl: "%s IS NOT NULL", arity: 0},
	"is_not_set": {tmpl: "%s IS NULL", arity: 0},

	"in_last_days":    {tmpl: "%s >= DATE_SUB(CURDATE(), INTERVAL :%s DAY)", arity: 1, relDays: true},
	"older_than_days": {tmpl: "%s < DATE_SUB(CURDATE(), INTERVAL :%s DAY)", arity: 1, relDays: true},
}

// topicColumn maps a validated topic enum to its opt-in column. The topic is a
// closed enum so the column choice is code-controlled and never bound.
func topicColumn(t entity.EmailCampaignTopic) (string, error) {
	switch t {
	case entity.EmailCampaignTopicNewsletter:
		return "sa.subscribe_newsletter", nil
	case entity.EmailCampaignTopicNewArrivals:
		return "sa.subscribe_new_arrivals", nil
	case entity.EmailCampaignTopicEvents:
		return "sa.subscribe_events", nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownTopic, string(t))
	}
}

// parseValue converts one raw string into a bound-parameter value according to
// the field's value type (or a relative-days int when relDays is set). It never
// returns SQL text — only a value destined for a :pN bind. Enum membership is NOT
// checked here (parseValue has no access to the enum set); the compiler performs
// that check before calling parseValue, so a vtEnum value is passed through.
func parseValue(vt valueType, relDays bool, raw string) (any, error) {
	if relDays {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an integer day count", ErrBadValue, raw)
		}
		if n < 0 {
			return nil, fmt.Errorf("%w: negative day count %d", ErrBadValue, n)
		}
		return n, nil
	}

	switch vt {
	case vtBool:
		switch raw {
		case "true", "1":
			return 1, nil
		case "false", "0":
			return 0, nil
		default:
			return nil, fmt.Errorf("%w: %q is not a boolean", ErrBadValue, raw)
		}
	case vtInt:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an integer", ErrBadValue, raw)
		}
		return n, nil
	case vtAge:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an integer", ErrBadValue, raw)
		}
		if n < 0 || n > 150 {
			return nil, fmt.Errorf("%w: age %d out of range 0..150", ErrBadValue, n)
		}
		return n, nil
	case vtBirthMonth:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an integer", ErrBadValue, raw)
		}
		if n < 1 || n > 12 {
			return nil, fmt.Errorf("%w: month %d out of range 1..12", ErrBadValue, n)
		}
		return n, nil
	case vtDecimal:
		d, err := decimal.NewFromString(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a decimal", ErrBadValue, raw)
		}
		return d, nil
	case vtDate:
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC(), nil
		}
		if t, err := time.Parse("2006-01-02", raw); err == nil {
			return t.UTC(), nil
		}
		return nil, fmt.Errorf("%w: %q is not an RFC3339 or YYYY-MM-DD date", ErrBadValue, raw)
	case vtEnum:
		// Membership is validated by the compiler (which holds the enum set);
		// here the already-validated value is passed through to be bound.
		return raw, nil
	case vtString:
		return raw, nil
	default:
		return nil, fmt.Errorf("%w: unknown value type", ErrBadValue)
	}
}
