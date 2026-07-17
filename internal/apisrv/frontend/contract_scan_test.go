package frontend

import (
	"sort"
	"testing"

	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestFrontendServiceHasNoCatalogPKs is the R3 storefront contract-scan test (§0.7, §14.1/§14.2). It
// walks every message transitively reachable from FrontendService (each RPC input + output) and fails
// if any carries a catalogue primary key: a catalogue FK field name (product_id/colorway_id/variant_id/
// size_id) or the PK `id` on a catalogue message (Colorway/Variant/ArchiveList). The storefront's public
// identity is base_sku / variant_sku / archive code, never an internal id.
//
// Documented exceptions (§14.1/§14.2):
//   - field names media_id and category_id (public media + taxonomy),
//   - the order subtree (common/order.proto) — orders are keyed by order_uuid; a public OrderItem never
//     fills the admin-only variant_id,
//   - the hero subtree (common/hero.proto) — hero featured-product blocks still embed common.Colorway.
//     Hero is outside R3 §7 (product + archive) and its storefront projection is a documented follow-up;
//     it is exempted here so this scan enforces exactly the §7 surface (colourway + archive).
func TestFrontendServiceHasNoCatalogPKs(t *testing.T) {
	forbiddenFieldNames := map[string]bool{
		"product_id": true, "colorway_id": true, "variant_id": true, "size_id": true,
	}
	forbiddenPKMessages := map[string]bool{
		"Colorway": true, "Variant": true, "ArchiveList": true,
	}
	exceptFieldNames := map[string]bool{ // §14.1
		"media_id": true, "category_id": true,
	}
	exceptFiles := map[string]bool{ // §14.2 + hero follow-up
		"common/order.proto": true,
		"common/hero.proto":  true,
	}

	svc := pb_frontend.File_frontend_frontend_proto.Services().ByName("FrontendService")
	if svc == nil {
		t.Fatal("FrontendService descriptor not found")
	}

	visited := map[protoreflect.FullName]bool{}
	violations := map[string]struct{}{}

	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if md == nil || visited[md.FullName()] {
			return
		}
		visited[md.FullName()] = true
		// Documented subtree exceptions: do not scan or recurse into them.
		if exceptFiles[md.ParentFile().Path()] {
			return
		}
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			name := string(f.Name())
			if forbiddenFieldNames[name] && !exceptFieldNames[name] {
				violations[string(md.FullName())+"."+name] = struct{}{}
			}
			if name == "id" && forbiddenPKMessages[string(md.Name())] {
				violations[string(md.FullName())+".id"] = struct{}{}
			}
			if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
				walk(f.Message())
			}
		}
	}

	methods := svc.Methods()
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		walk(m.Input())
		walk(m.Output())
	}

	if len(violations) > 0 {
		list := make([]string, 0, len(violations))
		for v := range violations {
			list = append(list, v)
		}
		sort.Strings(list)
		t.Errorf("FrontendService leaks catalogue primary keys (R3 §0.7/§14) — use storefront projections:\n  %v", list)
	}
}
