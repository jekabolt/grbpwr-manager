package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// enrich loads and attaches the size range, linked products, sketch media
// (writable items + resolved MediaFull), callouts and revisions for each card.
func (s *Store) enrich(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.Id)
	}

	sizes, err := s.idListByTechCardIds(ctx, "tech_card_size", "size_id", ids)
	if err != nil {
		return err
	}
	products, err := s.idListByTechCardIds(ctx, "tech_card_product", "product_id", ids)
	if err != nil {
		return err
	}
	mediaItems, mediaFull, err := s.mediaByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	callouts, err := s.calloutsByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	revisions, err := s.revisionsByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].SizeIds = sizes[id]
		cards[i].ProductIds = products[id]
		cards[i].Media = mediaItems[id]
		cards[i].ResolvedMedia = mediaFull[id]
		cards[i].Callouts = callouts[id]
		cards[i].Revisions = revisions[id]
	}
	return nil
}

type techCardIDRow struct {
	TechCardID int `db:"tech_card_id"`
	Value      int `db:"value"`
}

// idListByTechCardIds loads a single int column (e.g. size_id, product_id) from a
// child table, grouped by tech_card_id and ordered by display_order.
func (s *Store) idListByTechCardIds(ctx context.Context, table, column string, ids []int) (map[int][]int, error) {
	if len(ids) == 0 {
		return map[int][]int{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardIDRow](ctx, s.DB, fmt.Sprintf(`
		SELECT tech_card_id, %s AS value
		FROM %s
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, column, table), map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load %s: %w", table, err)
	}
	out := make(map[int][]int, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.Value)
	}
	return out, nil
}

type techCardMediaRow struct {
	TechCardID int                      `db:"tech_card_id"`
	Kind       entity.TechCardMediaKind `db:"kind"`
	entity.MediaFull
}

func (s *Store) mediaByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardMediaItem, map[int][]entity.TechCardMediaFull, error) {
	items := make(map[int][]entity.TechCardMediaItem, len(ids))
	full := make(map[int][]entity.TechCardMediaFull, len(ids))
	if len(ids) == 0 {
		return items, full, nil
	}
	rows, err := storeutil.QueryListNamed[techCardMediaRow](ctx, s.DB, `
		SELECT tcm.tech_card_id, tcm.kind, m.*
		FROM tech_card_media tcm
		JOIN media m ON m.id = tcm.media_id
		WHERE tcm.tech_card_id IN (:ids)
		ORDER BY tcm.tech_card_id, tcm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, nil, fmt.Errorf("can't load tech card media: %w", err)
	}
	for i := range rows {
		tcID := rows[i].TechCardID
		items[tcID] = append(items[tcID], entity.TechCardMediaItem{MediaId: rows[i].Id, Kind: rows[i].Kind})
		full[tcID] = append(full[tcID], entity.TechCardMediaFull{Media: rows[i].MediaFull, Kind: rows[i].Kind})
	}
	return items, full, nil
}

type techCardCalloutRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardCallout
}

func (s *Store) calloutsByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardCallout, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardCallout{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardCalloutRow](ctx, s.DB, `
		SELECT tech_card_id, callout_number, part, description, dimensions
		FROM tech_card_callout
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card callouts: %w", err)
	}
	out := make(map[int][]entity.TechCardCallout, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.TechCardCallout)
	}
	return out, nil
}

type techCardRevisionRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardRevision
}

func (s *Store) revisionsByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardRevision, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardRevision{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardRevisionRow](ctx, s.DB, `
		SELECT tech_card_id, version, revision_date, author, section, change_note
		FROM tech_card_revision
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card revisions: %w", err)
	}
	out := make(map[int][]entity.TechCardRevision, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.TechCardRevision)
	}
	return out, nil
}
