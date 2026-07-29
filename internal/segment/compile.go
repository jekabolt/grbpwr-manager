package segment

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Structural caps. A predicate exceeding any of these is rejected before it can
// build an unbounded query.
const (
	maxDepth     = 6
	maxNodes     = 100
	maxInValues  = 200
	emptyPredSQL = "1=1"
)

// Error sentinels. Callers assert with errors.Is.
var (
	// ErrMalformedNode: a node violates the group-vs-leaf shape rules (a group
	// carrying leaf fields, or a leaf carrying children / no field).
	ErrMalformedNode = errors.New("segment: malformed node")
	// ErrEmptyGroup: an AND/OR group with zero children.
	ErrEmptyGroup = errors.New("segment: empty group")
	// ErrUnknownField: a leaf field is not in the registry allow-list.
	ErrUnknownField = errors.New("segment: unknown field")
	// ErrUnknownOperator: a leaf operator is not allowed on its field.
	ErrUnknownOperator = errors.New("segment: unknown operator")
	// ErrBadValue: a value fails type parsing / range / enum validation.
	ErrBadValue = errors.New("segment: bad value")
	// ErrArity: the number of values does not match the operator's arity.
	ErrArity = errors.New("segment: wrong number of values for operator")
	// ErrTooDeep: predicate nesting exceeds maxDepth.
	ErrTooDeep = errors.New("segment: predicate too deep")
	// ErrTooManyNodes: predicate has more than maxNodes nodes.
	ErrTooManyNodes = errors.New("segment: too many nodes")
	// ErrTooManyInValues: an IN/NOT IN list exceeds maxInValues.
	ErrTooManyInValues = errors.New("segment: too many IN values")
	// ErrUnknownTopic: compliance was asked for an unrecognized topic.
	ErrUnknownTopic = errors.New("segment: unknown topic")
)

// Compiled is the compiled user predicate: a single boolean SQL expression over
// the fixed sa./maa. aliases, with every value bound as a :pN parameter.
type Compiled struct {
	SQL    string
	Params map[string]any
}

// compiler carries the pre-order bind counter and accumulated params/node count.
type compiler struct {
	params  map[string]any
	counter int
	nodes   int
}

// Compile walks pred.Root pre-order and produces a parameterized boolean
// expression. A nil root compiles to the always-true fragment "1=1" (the empty
// predicate = the whole base audience; compliance is still applied downstream by
// BuildAudiencePredicate). Compliance clauses are NEVER emitted here.
func Compile(pred entity.SegmentPredicate) (Compiled, error) {
	if pred.Root == nil {
		return Compiled{SQL: emptyPredSQL, Params: map[string]any{}}, nil
	}
	c := &compiler{params: map[string]any{}}
	sql, err := c.node(pred.Root, 1)
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{SQL: sql, Params: c.params}, nil
}

// bind stores v under the next pre-order :pN name and returns that name (without
// the leading ':').
func (c *compiler) bind(v any) string {
	name := fmt.Sprintf("p%d", c.counter)
	c.counter++
	c.params[name] = v
	return name
}

// node compiles one node at the given 1-based depth.
func (c *compiler) node(n *entity.SegmentNode, depth int) (string, error) {
	if depth > maxDepth {
		return "", fmt.Errorf("%w: depth %d exceeds %d", ErrTooDeep, depth, maxDepth)
	}
	c.nodes++
	if c.nodes > maxNodes {
		return "", fmt.Errorf("%w: exceeds %d", ErrTooManyNodes, maxNodes)
	}

	// Discriminator: a node is a GROUP iff Op is AND or OR.
	if n.Op == entity.SegmentOpAnd || n.Op == entity.SegmentOpOr {
		return c.group(n, depth)
	}
	return c.leaf(n)
}

func (c *compiler) group(n *entity.SegmentNode, depth int) (string, error) {
	if n.Field != "" || n.Operator != "" || len(n.Values) != 0 {
		return "", fmt.Errorf("%w: group node carries leaf fields", ErrMalformedNode)
	}
	if len(n.Children) == 0 {
		return "", ErrEmptyGroup
	}

	sep := " AND "
	if n.Op == entity.SegmentOpOr {
		sep = " OR "
	}

	parts := make([]string, 0, len(n.Children))
	for i := range n.Children {
		s, err := c.node(&n.Children[i], depth+1)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return "(" + strings.Join(parts, sep) + ")", nil
}

func (c *compiler) leaf(n *entity.SegmentNode) (string, error) {
	if len(n.Children) != 0 {
		return "", fmt.Errorf("%w: leaf node carries children", ErrMalformedNode)
	}
	if n.Field == "" {
		return "", fmt.Errorf("%w: leaf node has empty field", ErrMalformedNode)
	}

	fd, ok := registry[n.Field]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownField, n.Field)
	}
	if _, ok := fd.ops[n.Operator]; !ok {
		return "", fmt.Errorf("%w: %q on field %q", ErrUnknownOperator, n.Operator, n.Field)
	}
	os := opTemplates[n.Operator] // guaranteed present (fd.ops ⊆ opTemplates, self-checked in tests)

	switch os.arity {
	case 0:
		if len(n.Values) != 0 {
			return "", fmt.Errorf("%w: %q takes no values, got %d", ErrArity, n.Operator, len(n.Values))
		}
		return fmt.Sprintf(os.tmpl, fd.sqlExpr), nil

	case 1:
		if len(n.Values) != 1 {
			return "", fmt.Errorf("%w: %q takes exactly 1 value, got %d", ErrArity, n.Operator, len(n.Values))
		}
		name, err := c.parseBind(fd, os, n.Values[0])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(os.tmpl, fd.sqlExpr, name), nil

	case 2:
		if len(n.Values) != 2 {
			return "", fmt.Errorf("%w: %q takes exactly 2 values, got %d", ErrArity, n.Operator, len(n.Values))
		}
		lo, err := c.parseBind(fd, os, n.Values[0])
		if err != nil {
			return "", err
		}
		hi, err := c.parseBind(fd, os, n.Values[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(os.tmpl, fd.sqlExpr, lo, hi), nil

	case -1: // list operators: bind the whole slice as ONE param
		if len(n.Values) < 1 {
			return "", fmt.Errorf("%w: %q takes at least 1 value", ErrArity, n.Operator)
		}
		if len(n.Values) > maxInValues {
			return "", fmt.Errorf("%w: %d exceeds %d", ErrTooManyInValues, len(n.Values), maxInValues)
		}
		list := make([]any, 0, len(n.Values))
		for _, raw := range n.Values {
			v, err := parseLeafValue(fd, os, raw)
			if err != nil {
				return "", err
			}
			list = append(list, v)
		}
		name := c.bind(list)
		return fmt.Sprintf(os.tmpl, fd.sqlExpr, name), nil

	default:
		return "", fmt.Errorf("%w: operator %q has invalid arity %d", ErrArity, n.Operator, os.arity)
	}
}

// parseBind parses one raw value and binds it, returning the :pN name.
func (c *compiler) parseBind(fd fieldDef, os opSpec, raw string) (string, error) {
	v, err := parseLeafValue(fd, os, raw)
	if err != nil {
		return "", err
	}
	return c.bind(v), nil
}

// parseLeafValue validates+parses a single raw value. Enum membership is checked
// here (where the enum set is available); everything else defers to parseValue.
func parseLeafValue(fd fieldDef, os opSpec, raw string) (any, error) {
	if fd.vType == vtEnum && !os.relDays {
		if _, ok := fd.enumValues[raw]; !ok {
			return nil, fmt.Errorf("%w: %q is not a valid value for the field", ErrBadValue, raw)
		}
		return raw, nil
	}
	return parseValue(fd.vType, os.relDays, raw)
}

// ComplianceOpts selects the optional per-topic opt-in clause. A nil Topic yields
// no subscribe_* clause (topic-independent preview).
type ComplianceOpts struct {
	Topic *entity.EmailCampaignTopic
}

// BuildAudiencePredicate is the SINGLE compliance choke point. It ANDs the fixed
// compliance clauses AFTER (outside) the user tree, so a segment can never widen
// the audience past active + not-suppressed + opted-in. It always appends:
//
//	AND sa.status = 'active'   -- account is active
//	AND es.id IS NULL          -- not on the suppression list (via the store's LEFT JOIN)
//
// and, when a topic is given, also `AND <topic opt-in column> = 1`. Compliance
// binds no parameters; the returned params are a clone of c.Params.
//
// Compliance is emitted ONLY here (never inside Compile) so every query path —
// topic-independent preview and per-campaign send — reuses the same guarantee.
func BuildAudiencePredicate(c Compiled, opts ComplianceOpts) (where string, params map[string]any, err error) {
	var b strings.Builder
	b.WriteString("(")
	b.WriteString(c.SQL)
	b.WriteString(") AND sa.status = 'active' AND es.id IS NULL")

	if opts.Topic != nil {
		col, cerr := topicColumn(*opts.Topic)
		if cerr != nil {
			return "", nil, cerr
		}
		b.WriteString(" AND ")
		b.WriteString(col)
		b.WriteString(" = 1")
	}

	params = make(map[string]any, len(c.Params))
	for k, v := range c.Params {
		params[k] = v
	}
	return b.String(), params, nil
}
