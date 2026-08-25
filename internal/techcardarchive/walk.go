package techcardarchive

import (
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ─────────────────────────────────────────────────────────────────────────────
// Generic protoreflect traversal used by the export redactor (Ф1.2) and by the
// import identity remapper (Ф2.3).
//
// Both walk a proto message tree and act on fields BY NAME. Name-based, not
// path-based, on purpose: the tech-card contract keeps growing new nested
// messages, and a path list would go stale the moment somebody wraps an
// existing message in one more level. A name list only goes stale when a NEW
// name appears — which is exactly what the field-list guard in walk_test.go
// makes impossible to do silently.
// ─────────────────────────────────────────────────────────────────────────────

// RedactFieldsDeep clears every field whose NAME is in names, anywhere in the message
// tree, leaving every other field intact.
//
// Traversal discipline is copied from redactCostingFieldsDeep
// (internal/apisrv/admin/costing_rbac.go) — the same map/list/message branches — with the
// name list lifted into a parameter and the map branch actually implemented (the original
// could skip it because the analytics responses have no maps; a generic utility cannot).
//
// A matched field is cleared WHOLE and not descended into. That matters for the money
// fields: google.type.Decimal is a message, not a scalar, so "clear the value" can only
// mean m.Clear(fd) — there is no zero scalar to write. It also matters structurally:
// clearing an ancestor such as `costing` removes every money field nested under it,
// including ones nobody has written yet. See MoneyFieldNamesArchive.
func RedactFieldsDeep(m protoreflect.Message, names map[string]bool) {
	if m == nil || !m.IsValid() || len(names) == 0 {
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if names[string(fd.Name())] {
			// Clearing the field currently yielded by Range is the one mutation
			// protoreflect.Message.Range permits.
			m.Clear(fd)
			return true
		}
		switch {
		case fd.IsMap():
			if !isMessageValueMap(fd) {
				return true
			}
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				RedactFieldsDeep(mv.Message(), names)
				return true
			})
		case fd.IsList() && isMessageKind(fd):
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				RedactFieldsDeep(l.Get(i).Message(), names)
			}
		case isMessageKind(fd):
			RedactFieldsDeep(v.Message(), names)
		}
		return true
	})
}

// RemapIntFieldsDeep rewrites the value of every int32/int64 field whose NAME is in names,
// anywhere in the message tree, through mapping (old id → id in the target database).
//
// Rules, all three load-bearing for the import:
//
//   - 0 is NEVER touched and never reported. Across the whole tech-card contract 0 means
//     "unset", and several fields say so out loud: callout.media_id = 0 is a legitimate
//     "not anchored to a picture", swatch_media_id = 0 is "no swatch", grade_base_size_id = 0
//     is "no grade rule authored". Remapping 0 would invent a reference.
//   - A non-zero value missing from mapping is a HOLE: onMiss is called with the field name
//     and the old value, and the field is cleared. Clearing rather than keeping is the whole
//     point — a source-database id left in place would silently point at an unrelated row in
//     the target database, which is worse than an empty field plus a line in the report.
//   - For a repeated int field a missing entry is DROPPED from the list (after onMiss), not
//     replaced by 0: appending 0 to `size_ids` / `media_ids` would fabricate a reference to
//     row 0, and those lists have no "unset" slot semantics.
//
// Map fields are traversed into (message values) but their KEYS and scalar values are never
// remapped: a map entry has no field name to match on, and the archive contract has no
// int-keyed maps. If one ever appears, the field-list guard will surface it as an uncovered
// name and force a decision here.
func RemapIntFieldsDeep(
	m protoreflect.Message,
	names map[string]bool,
	mapping map[int64]int64,
	onMiss func(field string, old int64),
) {
	if m == nil || !m.IsValid() || len(names) == 0 {
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		if names[name] && isIntKind(fd) && !fd.IsMap() {
			if fd.IsList() {
				remapIntList(v.List(), fd, name, mapping, onMiss)
				return true
			}
			old := v.Int()
			if old == 0 {
				return true
			}
			nv, ok := mapping[old]
			if !ok {
				reportMiss(onMiss, name, old)
				m.Clear(fd)
				return true
			}
			m.Set(fd, intValue(fd, nv))
			return true
		}
		switch {
		case fd.IsMap():
			if !isMessageValueMap(fd) {
				return true
			}
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				RemapIntFieldsDeep(mv.Message(), names, mapping, onMiss)
				return true
			})
		case fd.IsList() && isMessageKind(fd):
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				RemapIntFieldsDeep(l.Get(i).Message(), names, mapping, onMiss)
			}
		case isMessageKind(fd):
			RemapIntFieldsDeep(v.Message(), names, mapping, onMiss)
		}
		return true
	})
}

// remapIntList rewrites a repeated int field in place: kept values are remapped, 0 stays as
// authored, and a value missing from mapping is reported and dropped.
func remapIntList(
	l protoreflect.List,
	fd protoreflect.FieldDescriptor,
	name string,
	mapping map[int64]int64,
	onMiss func(field string, old int64),
) {
	kept := make([]int64, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		old := l.Get(i).Int()
		if old == 0 {
			kept = append(kept, 0)
			continue
		}
		nv, ok := mapping[old]
		if !ok {
			reportMiss(onMiss, name, old)
			continue
		}
		kept = append(kept, nv)
	}
	l.Truncate(0)
	for _, v := range kept {
		l.Append(intValue(fd, v))
	}
}

func reportMiss(onMiss func(field string, old int64), name string, old int64) {
	if onMiss != nil {
		onMiss(name, old)
	}
}

// isMessageKind reports whether the field's (element) type is a message. For a list of
// messages Kind() is MessageKind too, so callers must test IsMap()/IsList() first.
func isMessageKind(fd protoreflect.FieldDescriptor) bool {
	k := fd.Kind()
	return k == protoreflect.MessageKind || k == protoreflect.GroupKind
}

// isMessageValueMap reports whether fd is a map whose VALUES are messages (worth descending
// into). A map of scalars has nothing to redact or remap below itself.
func isMessageValueMap(fd protoreflect.FieldDescriptor) bool {
	if !fd.IsMap() {
		return false
	}
	k := fd.MapValue().Kind()
	return k == protoreflect.MessageKind || k == protoreflect.GroupKind
}

// isIntKind reports whether the field's (element) type is a signed integer readable through
// protoreflect.Value.Int(). Unsigned kinds are deliberately excluded: every id in the
// tech-card contract is int32/int64, and silently reading a uint through Int() would be a
// bug waiting for the first uint64 id.
func isIntKind(fd protoreflect.FieldDescriptor) bool {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return true
	default:
		return false
	}
}

// intValue builds a protoreflect.Value of the field's own width. Writing an Int64 value into
// an int32 field panics, so the width has to come from the descriptor, not from the caller.
func intValue(fd protoreflect.FieldDescriptor, v int64) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(v))
	default:
		return protoreflect.ValueOfInt64(v)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Canonical field-name lists.
//
// These are the lists the guard in walk_test.go keeps honest, IN BOTH DIRECTIONS:
// a proto field that looks like an id or like money and is in none of them fails the
// test, and a name listed here that no longer exists in the contract fails it too.
// ─────────────────────────────────────────────────────────────────────────────

// SizeFieldNames are the size-dictionary FKs remapped on import (source size id → target
// size id, resolved through the manifest's id_maps.sizes name table).
var SizeFieldNames = map[string]bool{
	"size_id":             true,
	"size_ids":            true, // repeated (TechCardInsert.size_ids)
	"base_sample_size_id": true,
	"grade_base_size_id":  true, // StyleSizeChart, not the TechCard tree
	"model_wears_size_id": true,
}

// MediaFieldNames are the media FKs remapped on import, after the archived files have been
// re-uploaded and matched by content hash.
//
// NOT just `media_id`, which is what this list said when the phase was planned: the guard
// found two more media references on its first run — `media_ids` (repeated, on
// TechCardDetail) and `swatch_media_id` (on AdminColorwayRef and on the lab-dip round).
// Leaving either out would have pointed an imported card's detail images and colour swatches
// at whatever rows happen to hold those ids in the target database.
var MediaFieldNames = map[string]bool{
	"media_id":        true,
	"media_ids":       true,
	"swatch_media_id": true,
}

// moneyFieldNamesCosting is a VERBATIM COPY of costingRedactedFieldNames from
// internal/apisrv/admin/costing_rbac.go. Copied, not imported: internal/apisrv/admin is a
// handler package and importing it from here would tie the archive format to the API layer
// (and cycle back through it). The copy is analytics-shaped — most of these names do not
// occur in the tech-card contract at all — so it is exempt from the guard's "no dead entry"
// direction; it is here so that a report-shaped payload ever added to the archive is
// redacted by the same denylist the API uses.
var moneyFieldNamesCosting = map[string]bool{
	"unit_cost":                      true,
	"revenue_cost":                   true,
	"gross_margin":                   true,
	"gross_margin_pct":               true,
	"contribution_margin":            true,
	"operating_result":               true,
	"opex_total":                     true,
	"marketing_spend":                true,
	"opex_caveat":                    true,
	"cpo":                            true,
	"blended_cac":                    true,
	"ltv_cac_ratio":                  true,
	"fulfilment_cost_per_order":      true,
	"profit_per_order":               true,
	"payment_fees":                   true,
	"gross_margin_change_pct":        true,
	"gross_margin_pct_change_pp":     true,
	"contribution_margin_change_pct": true,
	"operating_result_change_pct":    true,
}

// moneyFieldNamesTechCard are the money-bearing names of the tech-card contract itself. Every
// one of them must exist in the contract — the guard fails on a dead entry, because a name
// that no longer resolves is a redaction that silently stopped happening.
//
// `costing` is the important one and it is not a leaf. TechCardCosting carries money under
// names no substring rule would ever catch — `materials_per_unit`, `amount`,
// `target_margin_pct`, `defect_percent` — and it will carry more. Redacting the BLOCK instead
// of enumerating its leaves makes "a new money field inside costing leaks into the archive"
// unexpressible rather than merely guarded: RedactFieldsDeep clears a matched field whole and
// never descends into it. The cost of that choice is that `total_sam` (labour minutes, not
// money) goes with it; the per-operation smv/time_norm it sums live outside costing and
// survive, so the number is rebuilt on the far side rather than lost.
var moneyFieldNamesTechCard = map[string]bool{
	"costing":               true, // whole TechCardCosting block, see above
	"unit_price":            true, // TechCardBomItem
	"currency":              true, // currency of a redacted amount
	"latest_price":          true, // Material (materials/index.json)
	"cost_price":            true, // AdminColorwayRef
	"cost_price_source":     true, // provenance of a price that is no longer there
	"cost_price_updated_at": true, // ditto — a timestamp for a removed figure only leaks when it was priced
	"prices":                true, // AdminColorwayRef retail price list
	"net_prices":            true, // AdminColorwayRef, VAT-netted retail
	"line_total":            true, // TechCardColorwayUsage
	"size_run_total":        true, // TechCardColorwayUsage
	"price_source":          true, // TechCardBomItem price provenance
	"price_snapshot_at":     true, // TechCardBomItem price provenance
	"unit_cost":             true, // also in the copied list; kept explicit for the tech-card side
}

// MoneyFieldNamesArchive is the denylist the export redactor runs: everything the API redacts
// for a caller without costing:read, plus the tech-card contract's own money.
var MoneyFieldNamesArchive = unionNames(moneyFieldNamesCosting, moneyFieldNamesTechCard)

func unionNames(sets ...map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for _, s := range sets {
		for k, v := range s {
			if v {
				out[k] = true
			}
		}
	}
	return out
}
