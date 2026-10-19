package content

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// MEDIA CITED INSIDE JSON — the half of the usage question the registry cannot ask.
//
// Four documents show media by id with no foreign key behind it: the home page hero (hero.data),
// an archive's timeline (archive.body), an email campaign's body and its content variants
// (email_campaign.body, email_campaign_variant.body) and a card's colour plan
// (design_colour_plan.maps). The owner's rule (MEDIA-MOBILE, 19.10) is that a file something still
// shows is not deletable; without this scan each of these called its files free and the delete
// went through — a blank hero block, a hole in a timeline, a broken image in an email already in
// people's inboxes, a designer's painted map gone.
//
// WHAT EACH DOCUMENT REPORTS AS (kind / entity / label / slot):
//
//	hero        → hero / 0 / "home page" / "slideshow, block 3", "nav men"
//	              (one singleton row whose id changes on every save, so there is no id to navigate to)
//	archive     → archive / archive id / the archive's name / "media line, block 4"
//	              (the same kind and name as the archive's thumbnail ref)
//	email       → email_campaign / campaign id / campaign name / "image, block 2",
//	              "variant B, header logo, block 1"
//	colour plan → tech_card / card id / the card's name / "colour map, front"
//	              (the plan is one per card and keyed by it, so the card is where to go)
//
// Every campaign holds, whatever its status: a sent email keeps loading its pictures from the
// bucket, and only a draft can be deleted to let them go.
//
// WHAT IS DELIBERATELY NOT HERE, next to the registry's own list of omissions (media_usage.go):
//
//   - design_colour_plan.maps[].base_media_id — which flat a map was painted over, kept to tell a
//     stale map; the flat is a design picture that its own row already holds.
//   - design_parts_pieces source_*_media_id / proposal front/back — which plates a pieces list was
//     read from, kept to ask for a re-read; deleting the plate loses no names.
//   - design_parts_suggestion.base_media_id — a cache key for a model's answer.
//   - design_run.inputs, design_joins — provenance and model hints, see media_usage.go.
//
// ONE STATEMENT, PREFILTERED IN SQL, DECIDED IN GO. Every holder table is read once per call, as
// one UNION ALL (the same reason as mediaUsageQuery: a page of tiles must not cost a round trip per
// table). Each branch keeps only the documents that name at least one requested id under one of
// the keys its walker reads:
//
//	JSON_OVERLAPS(JSON_EXTRACT(doc, '$**.k', '$**.k[*]', ...), JSON_ARRAY(ids))
//
// '$**.k' collects every scalar at key k at any depth (two-column email children included) and
// '$**.k[*]' flattens every array at k (an archive media line); a path that matches nothing
// yields NULL, which drops the row. The filter can only be WIDER than the walker, never narrower:
// the key lists live next to the walkers (entity.*MediaKeys) and
// TestMediaJSONHolderKeysCoverWalkers fails when a walker reads a key its list lacks. The walker
// then decides what is actually shown — the hero and the archive only count the payload their
// block type selects — so the filter never has to be exact.
//
// LIKE or JSON_SEARCH would not do: JSON_SEARCH matches string values only and these ids are
// numbers, and a LIKE per id is 500 ORs that also match colours and other numbers. The filter
// spares the transfer and the Go decode of every document that cannot hold a requested id; the
// server still evaluates the paths on each row, which is a scan of four small tables (one row per
// campaign, archive, painted card, plus the hero).
//
// Semantics verified on the cluster's MySQL 8.0.45 (beta and prod share it); JSON_OVERLAPS needs
// 8.0.17+.

// JSON holder discriminators — the first column of mediaJSONHolderQuery. Internal only; the kind
// a ref reports is chosen per holder in mediaSlots.
const (
	jsonHolderHero       = "hero"
	jsonHolderArchive    = "archive"
	jsonHolderEmail      = "email_campaign"
	jsonHolderColourPlan = "colour_plan"
)

// collatedText pins a text expression to one collation, for the reason spelled out at length on
// mediaRefSource.selectSQL: the holder tables span three collation policies, and a UNION that
// lands two of them in one column fails with Error 1271 on a stock MySQL 8 server.
func collatedText(expr string) string {
	return "CAST(COALESCE(" + expr + ", '') AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci"
}

// citesAnyRequested is the SQL prefilter: the document names at least one requested id under one
// of keys, as a scalar or inside an array, at any depth.
func citesAnyRequested(doc string, keys []string) string {
	paths := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		paths = append(paths, "'$**."+k+"'", "'$**."+k+"[*]'")
	}
	return "JSON_OVERLAPS(JSON_EXTRACT(" + doc + ", " + strings.Join(paths, ", ") + "), JSON_ARRAY(:ids))"
}

// jsonHolderBranch renders one UNION branch: (holder, entity_id, label, variant, doc).
func jsonHolderBranch(holder, entityExpr, labelExpr, variantExpr, doc, from string, keys []string) string {
	return fmt.Sprintf(
		"SELECT '%s' AS holder, %s AS entity_id, %s AS label, %s AS variant, %s AS doc FROM %s WHERE %s",
		holder, entityExpr, collatedText(labelExpr), collatedText(variantExpr), doc, from,
		citesAnyRequested(doc, keys))
}

// mediaJSONHolderQuery is every JSON holder as a single UNION ALL, built once at init.
//
// Same rules as mediaUsageQuery: no ':' outside the ':ids' binds (sqlx's named-parameter scanner
// does not skip string literals) and no SQL comments. TestMediaJSONHolderQueryShape holds both.
var mediaJSONHolderQuery = buildMediaJSONHolderQuery()

func buildMediaJSONHolderQuery() string {
	const emailLabel = `COALESCE(NULLIF(c.name, ''), CONCAT('email campaign ', c.id))`
	branches := []string{
		jsonHolderBranch(jsonHolderHero, "0", "'home page'", "''", "h.data",
			"hero h", entity.HeroMediaKeys),
		jsonHolderBranch(jsonHolderArchive, "a.id", archiveLabel, "''", "a.body",
			"archive a "+archiveJoin, entity.ArchiveBodyMediaKeys),
		jsonHolderBranch(jsonHolderEmail, "c.id", emailLabel, "''", "c.body",
			"email_campaign c", entity.EmailBodyMediaKeys),
		jsonHolderBranch(jsonHolderEmail, "c.id", emailLabel, "v.label", "v.body",
			"email_campaign_variant v JOIN email_campaign c ON c.id = v.campaign_id", entity.EmailBodyMediaKeys),
		jsonHolderBranch(jsonHolderColourPlan, "tc.id", techCardLabel, "''", "cp.maps",
			"design_colour_plan cp JOIN tech_card tc ON tc.id = cp.tech_card_id", entity.ColourPlanMediaKeys),
	}
	return strings.Join(branches, "\nUNION ALL\n")
}

// mediaJSONHolderRow is one document that names at least one requested id.
type mediaJSONHolderRow struct {
	Holder   string `db:"holder"`
	EntityId int    `db:"entity_id"`
	Label    string `db:"label"`
	Variant  string `db:"variant"`
	Doc      []byte `db:"doc"`
}

// mediaSlots decodes the document and returns the kind its refs report and every media it shows.
//
// A document that does not decode holds nothing, and that is not leniency: it is what the
// document's own reader does with it — the hero and an archive timeline are served empty
// (getHeroInsert, GetArchiveById) and a campaign that does not decode cannot be opened or sent —
// so nothing of it is on screen to protect.
func (r mediaJSONHolderRow) mediaSlots() (string, []entity.MediaSlot, error) {
	if len(r.Doc) == 0 {
		return "", nil, nil
	}
	switch r.Holder {
	case jsonHolderHero:
		var h entity.HeroFullInsert
		if err := json.Unmarshal(r.Doc, &h); err != nil {
			return "", nil, err
		}
		return "hero", entity.HeroMediaSlots(h), nil
	case jsonHolderArchive:
		var items []entity.ArchiveItemInsert
		if err := json.Unmarshal(r.Doc, &items); err != nil {
			return "", nil, err
		}
		return "archive", entity.ArchiveBodyMediaSlots(items), nil
	case jsonHolderEmail:
		var blocks []entity.EmailBlock
		if err := json.Unmarshal(r.Doc, &blocks); err != nil {
			return "", nil, err
		}
		slots := entity.EmailBodyMediaSlots(blocks)
		if v := strings.TrimSpace(r.Variant); v != "" {
			for i := range slots {
				slots[i].Slot = "variant " + v + ", " + slots[i].Slot
			}
		}
		return "email_campaign", slots, nil
	case jsonHolderColourPlan:
		var maps []entity.DesignColourMap
		if err := json.Unmarshal(r.Doc, &maps); err != nil {
			return "", nil, err
		}
		return "tech_card", entity.ColourPlanMediaSlots(maps), nil
	}
	return "", nil, fmt.Errorf("unknown media json holder %q", r.Holder)
}

// mediaJSONUsage returns a ref for every requested id that a JSON holder shows.
func mediaJSONUsage(ctx context.Context, db dependency.DB, ids []int) ([]entity.MediaUsageRef, error) {
	rows, err := storeutil.QueryListNamed[mediaJSONHolderRow](ctx, db,
		mediaJSONHolderQuery, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to read media json holders: %w", err)
	}
	return mediaJSONRefs(ctx, rows, ids), nil
}

// mediaJSONRefs turns the documents the prefilter kept into refs for the requested ids only: a
// document that shows ten files reports the one that was asked about.
func mediaJSONRefs(ctx context.Context, rows []mediaJSONHolderRow, ids []int) []entity.MediaUsageRef {
	want := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []entity.MediaUsageRef
	for _, r := range rows {
		kind, slots, err := r.mediaSlots()
		if err != nil {
			slog.WarnContext(ctx, "media usage: a JSON holder does not decode, counted as showing nothing",
				slog.String("holder", r.Holder), slog.Int("entity_id", r.EntityId),
				slog.String("err", err.Error()))
			continue
		}
		for _, s := range slots {
			if _, ok := want[s.MediaId]; !ok {
				continue
			}
			out = append(out, entity.MediaUsageRef{
				MediaId: s.MediaId, Kind: kind, EntityId: r.EntityId, Label: r.Label, Slot: s.Slot,
			})
		}
	}
	return out
}
