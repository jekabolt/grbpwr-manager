package content

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// defaultLanguageID resolves the default language rather than hardcoding 1: the media
// library shows one label per entity and it should be the one the catalogue calls default,
// even if the seeded row order ever changes.
const defaultLanguageID = `(SELECT l.id FROM language l WHERE l.is_default = 1 LIMIT 1)`

// Shared label/join fragments. A product (a colourway after the 0151 domain merge) and a
// tech card are each pointed at from several different columns, so their name is derived
// once and reused — otherwise the four product slots would drift apart over time.
const (
	productJoin = `LEFT JOIN product_translation pt ON pt.product_id = p.id AND pt.language_id = ` + defaultLanguageID
	// A soft-deleted product (deleted_at IS NOT NULL) is deliberately NOT filtered out: its
	// row still exists, so its FK still refuses the media delete. Hiding it here would show
	// "unused" for a file that cannot actually be deleted — the exact lie this RPC exists to kill.
	productLabel = `COALESCE(NULLIF(pt.name, ''), NULLIF(p.sku, ''), CONCAT('product ', p.id))`

	techCardLabel = `COALESCE(NULLIF(tc.name, ''), NULLIF(tc.style_number, ''), CONCAT('tech card ', tc.id))`
)

// mediaRefSource is one entry of the media reference registry: a table owning a foreign key
// into media(id), plus how to turn that row into something a human can act on.
//
// The registry is written out by hand rather than derived from information_schema on purpose.
// A generic walk of the foreign keys would produce "product_media #417" — a row id in a join
// table, which tells an operator nothing and leads nowhere. The readable name lives one or two
// joins away and the chain differs per entity, so it has to be stated. The explicit list also
// doubles as the answer to "what can reference a media item at all", which nothing else records.
//
// Keeping it in sync is a real obligation: a new column with an FK into media(id) must get a
// row here, or the library will call a referenced file free. TestMediaUsageRegistryCoversSchema
// enforces exactly that against the live schema.
type mediaRefSource struct {
	// kind names the entity space that entityExpr's id belongs to; it is what the client
	// turns into a route.
	kind string
	// table is the table holding the FK into media(id), with its alias.
	table string
	// column is that FK column, alias-qualified. It is both what we select and what we filter
	// on, so the IN (...) predicate always lands on the FK's own (leading, single-column) index.
	column string
	// joins reaches from table to whatever carries a readable name.
	joins string
	// entityExpr is the id the operator should be navigated to — the owning entity, never the
	// join-table row.
	entityExpr string
	// labelExpr is that entity's human name.
	labelExpr string
	// slot says where inside the entity the media sits. slotExpr overrides it when the table
	// stores the slot itself (tech_card_media.kind) or when a number disambiguates it.
	slot     string
	slotExpr string
}

// mediaRefRegistry lists every live foreign key into media(id), verified against both the
// migration history and the deployed schema (17 columns as of 0311).
var mediaRefRegistry = []mediaRefSource{
	// product — the colourway. Four different columns point at media from this one table.
	{
		kind: "product", table: "product p", column: "p.thumbnail_id",
		joins: productJoin, entityExpr: "p.id", labelExpr: productLabel, slot: "thumbnail",
	},
	{
		kind: "product", table: "product p", column: "p.secondary_thumbnail_id",
		joins: productJoin, entityExpr: "p.id", labelExpr: productLabel, slot: "secondary thumbnail",
	},
	{
		kind: "product", table: "product p", column: "p.swatch_media_id",
		joins: productJoin, entityExpr: "p.id", labelExpr: productLabel, slot: "swatch",
	},
	{
		kind: "product", table: "product_media pm", column: "pm.media_id",
		joins:      `JOIN product p ON p.id = pm.product_id ` + productJoin,
		entityExpr: "p.id", labelExpr: productLabel, slot: "gallery",
	},
	{
		// A lab-dip round belongs to the colourway, so that is where the operator is sent; the
		// round number goes in the slot so two rounds of the same colourway stay distinguishable.
		kind: "product", table: "product_lab_dip_round pldr", column: "pldr.swatch_media_id",
		joins:      `JOIN product p ON p.id = pldr.product_id ` + productJoin,
		entityExpr: "p.id", labelExpr: productLabel,
		slotExpr: `CONCAT('lab dip swatch, round ', pldr.round_number)`,
	},

	// archive
	{
		kind: "archive", table: "archive a", column: "a.thumbnail_id",
		joins:      `LEFT JOIN archive_translation atr ON atr.archive_id = a.id AND atr.language_id = ` + defaultLanguageID,
		entityExpr: "a.id",
		labelExpr:  `COALESCE(NULLIF(atr.heading, ''), NULLIF(a.code, ''), NULLIF(a.tag, ''), CONCAT('archive ', a.id))`,
		slot:       "thumbnail",
	},

	// model
	{
		kind: "model", table: "model m", column: "m.thumbnail_id",
		entityExpr: "m.id",
		labelExpr:  `COALESCE(NULLIF(m.name, ''), CONCAT('model ', m.id))`,
		slot:       "thumbnail",
	},
	{
		kind: "model", table: "model_media mm", column: "mm.media_id",
		joins:      `JOIN model m ON m.id = mm.model_id`,
		entityExpr: "m.id",
		labelExpr:  `COALESCE(NULLIF(m.name, ''), CONCAT('model ', m.id))`,
		slot:       "photo",
	},

	// material
	{
		kind: "material", table: "material mt", column: "mt.image_id",
		entityExpr: "mt.id",
		labelExpr:  `COALESCE(NULLIF(mt.name, ''), NULLIF(mt.code, ''), CONCAT('material ', mt.id))`,
		slot:       "image",
	},

	// task
	{
		kind: "task", table: "task_media tm", column: "tm.media_id",
		joins:      `JOIN task t ON t.id = tm.task_id`,
		entityExpr: "t.id",
		labelExpr:  `COALESCE(NULLIF(t.title, ''), CONCAT('task ', t.id))`,
		slot:       "attachment",
	},

	// tech card — four columns, three of them one join away from the card itself.
	{
		kind: "tech_card", table: "tech_card_media tcm", column: "tcm.media_id",
		joins:      `JOIN tech_card tc ON tc.id = tcm.tech_card_id`,
		entityExpr: "tc.id", labelExpr: techCardLabel,
		// The table already records what the picture is (moodboard, front, ...); pass it through
		// instead of flattening every card image to one generic slot name.
		slotExpr: `COALESCE(NULLIF(tcm.kind, ''), 'media')`,
	},
	{
		kind: "tech_card", table: "tech_card_callout tcc", column: "tcc.media_id",
		joins:      `JOIN tech_card tc ON tc.id = tcc.tech_card_id`,
		entityExpr: "tc.id", labelExpr: techCardLabel, slot: "callout",
	},
	{
		kind: "tech_card", table: "tech_card_detail_media tcdm", column: "tcdm.media_id",
		joins: `JOIN tech_card_detail tcd ON tcd.id = tcdm.detail_id ` +
			`JOIN tech_card tc ON tc.id = tcd.tech_card_id`,
		entityExpr: "tc.id", labelExpr: techCardLabel,
		slotExpr: `CONCAT_WS(' ', 'detail', NULLIF(tcd.detail_key, ''))`,
	},
	{
		kind: "tech_card", table: "tech_card_operation_media tcom", column: "tcom.media_id",
		joins: `JOIN tech_card_operation tco ON tco.id = tcom.tech_card_operation_id ` +
			`JOIN tech_card tc ON tc.id = tco.tech_card_id`,
		entityExpr: "tc.id", labelExpr: techCardLabel,
		slotExpr: `CONCAT('operation ', tco.operation_number)`,
	},

	// fitting — a fitting has no name of its own, so it borrows the style's and adds its round.
	{
		kind: "fitting", table: "fitting_media fm", column: "fm.media_id",
		joins:      `JOIN fitting f ON f.id = fm.fitting_id LEFT JOIN tech_card tc ON tc.id = f.tech_card_id`,
		entityExpr: "f.id", labelExpr: fittingLabel, slot: "photo",
	},
	{
		kind: "fitting", table: "fitting_callout fc", column: "fc.media_id",
		joins:      `JOIN fitting f ON f.id = fc.fitting_id LEFT JOIN tech_card tc ON tc.id = f.tech_card_id`,
		entityExpr: "f.id", labelExpr: fittingLabel, slot: "callout",
	},

	// sample
	{
		kind: "sample", table: "sample_media sm", column: "sm.media_id",
		joins:      `JOIN sample s ON s.id = sm.sample_id LEFT JOIN tech_card tc ON tc.id = s.tech_card_id`,
		entityExpr: "s.id",
		labelExpr: `CONCAT_WS(' ', COALESCE(NULLIF(tc.name, ''), NULLIF(tc.style_number, '')), ` +
			`CONCAT('sample ', s.number))`,
		slot: "photo",
	},
}

// fittingLabel is declared after the shared consts because it references techCardLabel through
// the same tc alias; a fitting is identified by its style plus round number.
const fittingLabel = `CONCAT_WS(' ', COALESCE(NULLIF(tc.name, ''), NULLIF(tc.style_number, '')), ` +
	`CONCAT('round ', f.round_number))`

// selectSQL renders one registry entry as a branch of the UNION.
//
// label and slot are wrapped in COALESCE(..., '') so a NULL from any join can never break the
// scan into a non-nullable Go string — a missing translation must degrade to a blank name, not
// to a failed RPC over the whole page.
func (src mediaRefSource) selectSQL() string {
	// A constant slot needs no COALESCE; a computed one can go NULL through its joins.
	slot := "'" + src.slot + "'"
	if src.slotExpr != "" {
		slot = "COALESCE(" + src.slotExpr + ", '')"
	}
	from := src.table
	if src.joins != "" {
		from += " " + src.joins
	}
	return fmt.Sprintf(
		"SELECT %s AS media_id, '%s' AS kind, %s AS entity_id, "+
			"COALESCE(%s, '') AS label, %s AS slot FROM %s WHERE %s IN (:ids)",
		src.column, src.kind, src.entityExpr, src.labelExpr, slot, from, src.column)
}

// MediaRefRegistryTargets returns the "table.column" of every reference the registry covers.
//
// Exported for one reason: an integration test diffs it against the real foreign keys into
// media(id) so that adding a new media-referencing column without a registry row is a failing
// test rather than a library that quietly reports a referenced file as free to delete.
func MediaRefRegistryTargets() []string {
	out := make([]string, 0, len(mediaRefRegistry))
	for _, src := range mediaRefRegistry {
		table, _, _ := strings.Cut(src.table, " ")
		_, column, _ := strings.Cut(src.column, ".")
		out = append(out, table+"."+column)
	}
	return out
}

// mediaUsageQuery is the whole registry as a single UNION ALL, built once at init.
//
// One statement, not one per table: the library asks about a page of ~50 tiles at a time, and
// seventeen round trips per page is what would make the feature too slow to leave switched on.
// UNION ALL, not UNION, because deduplication is neither needed (each branch is a distinct
// table+column) nor wanted (it would cost a sort over the whole result).
//
// Deliberately free of ':' outside bind names and of '--' comments: sqlx's named-parameter
// scanner does not skip comments or string literals, so a stray colon binds as an empty name
// and the query fails at runtime. Explanations stay on this side of the string.
var mediaUsageQuery = buildMediaUsageQuery()

func buildMediaUsageQuery() string {
	branches := make([]string, 0, len(mediaRefRegistry))
	for _, src := range mediaRefRegistry {
		branches = append(branches, src.selectSQL())
	}
	return strings.Join(branches, "\nUNION ALL\n") +
		"\nORDER BY media_id, kind, entity_id, slot"
}

// GetMediaUsage returns, per requested media id, every entity that still references it.
//
// Ids with no references are absent from the map; the caller decides whether that means
// "unused" or "unknown id". Duplicate ids in the input are harmless.
func (s *Store) GetMediaUsage(ctx context.Context, ids []int) (map[int][]entity.MediaUsageRef, error) {
	return mediaUsageOn(ctx, s.DB, ids)
}

// mediaUsageOn is GetMediaUsage against a caller-chosen connection: the pooled one for the
// RPC above, the transaction's own for DeleteMediaByIdIfUnused.
//
// It exists so that "what can reference a media item" is asked in exactly ONE place. The
// registry is the answer to that question and it is maintained by hand (see mediaRefSource);
// a second enumeration of the same columns — one for showing usage, one for deciding a delete —
// would drift the day a new foreign key lands, and it would drift silently in the worse
// direction: the delete path would call a referenced row free.
func mediaUsageOn(ctx context.Context, db dependency.DB, ids []int) (map[int][]entity.MediaUsageRef, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaUsageRef{}, nil
	}
	rows, err := storeutil.QueryListNamed[entity.MediaUsageRef](ctx, db,
		mediaUsageQuery, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to get media usage: %w", err)
	}
	out := make(map[int][]entity.MediaUsageRef, len(rows))
	for _, r := range rows {
		out[r.MediaId] = append(out[r.MediaId], r)
	}
	return out, nil
}
