package entity

// РУЛОННЫЙ ТОВАР — THE list of BOM families sold by length, and the projections every consumer of
// that list needs.
//
// The list itself used to live in internal/store/techcard as rollGoodsSectionList, with a header
// explaining that everything around it must be DERIVED from it rather than hand-copied, because
// «proximity is not coupling» and a fifth family added to one copy would leave the other at four.
// That argument is exactly why it moved here: since Ф6 the list has readers OUTSIDE the store —
// the readiness gate resolves pattern and alias bindings, and asks направление ткани, over the
// same four families — and a copy in internal/dto would have been the drift the original header
// forbade, just one package further away. The store now derives its map and its SQL fragment from
// this slice.

// RollGoodsSectionList is THE list: the only rows a раскладка can lay out (so the only ones where
// consumption_source='marker' is meaningful), and the only ones a pattern sheet or a cut-piece alias
// can bind to. Countable sections never gross wastage anyway; thread/trim/decoration are measured
// and MUST keep their gross-up.
var RollGoodsSectionList = []TechCardBomSection{
	BomSectionFabric,
	BomSectionLining,
	BomSectionInterlining,
	BomSectionInsulation,
}

var rollGoodsSectionSet = func() map[TechCardBomSection]bool {
	m := make(map[TechCardBomSection]bool, len(RollGoodsSectionList))
	for _, s := range RollGoodsSectionList {
		m[s] = true
	}
	return m
}()

// IsRollGoodsSection reports whether a BOM section is sold by length.
func IsRollGoodsSection(s TechCardBomSection) bool { return rollGoodsSectionSet[s] }

// RollGoodsLinesOfBom projects a card's loaded BOM onto the shape the ONE binding rule reads
// (ResolveFabricScope): the stable line_key and the назначение, roll goods only.
//
// It takes the loaded slice rather than the card so the rule stays testable without building a
// TechCard, and so the store — which loads the same three columns with a query — can be checked
// against it.
func RollGoodsLinesOfBom(items []TechCardBomItem) []RollGoodsLine {
	out := make([]RollGoodsLine, 0, len(items))
	for i := range items {
		if !IsRollGoodsSection(items[i].Section) {
			continue
		}
		out = append(out, RollGoodsLine{
			LineKey: items[i].LineKey,
			Purpose: items[i].Purpose.String,
		})
	}
	return out
}

// FabricDirectionLinesOfBom projects a card's loaded BOM onto what the направление rules read
// (MarkerFabricScope / ScopeFabricDirection). The counterpart of the store's fabricDirectionLines
// query, for callers that already hold an enriched card and must not pay for a second read.
//
// Index is the line's position in the card's BOM AS THE CLIENT SEES IT — which is what the enriched
// card's slice already is, since the card read orders by (display_order, id). The value only ever
// addresses a form control (`bom_items[i].fabric_direction`), so taking it over roll goods alone
// would pin a message on whichever row happens to sit at that position in the full list.
func FabricDirectionLinesOfBom(items []TechCardBomItem) []FabricDirectionLine {
	out := make([]FabricDirectionLine, 0, len(items))
	for i := range items {
		if !IsRollGoodsSection(items[i].Section) {
			continue
		}
		out = append(out, FabricDirectionLine{
			Index:     i,
			LineKey:   items[i].LineKey,
			Purpose:   items[i].Purpose.String,
			Name:      items[i].Name,
			IsSample:  items[i].IsSample,
			Direction: items[i].FabricDirection.String,
		})
	}
	return out
}
