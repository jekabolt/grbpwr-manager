package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Auto parts (0390): one cached answer per (card, view, flat media, cut revision). The handler
// cleans the model's answer; the store only keeps and returns it.

// partsDoc — the `parts` JSON column.
type partsDoc struct {
	Parts       []entity.DesignPartGroup `json:"parts"`
	SplitNeeded []entity.DesignPartSplit `json:"split_needed"`
}

type partsRow struct {
	Id          int            `db:"id"`
	TechCardId  int            `db:"tech_card_id"`
	ViewKey     string         `db:"view_key"`
	BaseMediaId int            `db:"base_media_id"`
	AlgoRev     string         `db:"algo_rev"`
	Parts       entity.RawJSON `db:"parts"`
	Model       string         `db:"model"`
	CreatedBy   string         `db:"created_by"`
	CreatedAt   time.Time      `db:"created_at"`
}

func (r partsRow) entity() (entity.DesignPartsSuggestion, error) {
	var doc partsDoc
	if len(r.Parts) > 0 {
		if err := json.Unmarshal(r.Parts, &doc); err != nil {
			return entity.DesignPartsSuggestion{}, fmt.Errorf("the parts of suggestion %d do not parse: %w", r.Id, err)
		}
	}
	return entity.DesignPartsSuggestion{
		Id: r.Id, TechCardId: r.TechCardId, View: r.ViewKey, BaseMediaId: r.BaseMediaId,
		AlgoRev: r.AlgoRev, Parts: doc.Parts, SplitNeeded: doc.SplitNeeded,
		Model: r.Model, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}, nil
}

const partsSelect = `
	SELECT ps.id, ps.tech_card_id, ps.view_key, ps.base_media_id, ps.algo_rev, ps.parts, ps.model,
	       ps.created_by, ps.created_at
	FROM design_parts_suggestion ps`

// flatBenchMediaQuery — the media of the plate in each side's FLAT slot (the colourway-less bench).
// The one definition of «the flat of this side», read by the door and by the band alike.
const flatBenchMediaQuery = `
	SELECT s.view_key, p.media_id
	FROM design_bench_slot s
	JOIN design_picture p ON p.id = s.picture_id
	WHERE s.tech_card_id = :card AND s.kind IN ('flat', '') AND s.colorway_id IS NULL
	  AND s.view_key IN (:views)`

func flatBenchMedia(ctx context.Context, db dependency.DB, cardID int) (map[string]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		View  string `db:"view_key"`
		Media int    `db:"media_id"`
	}](ctx, db, flatBenchMediaQuery, map[string]any{"card": cardID, "views": entity.DesignCardinalViews})
	if err != nil {
		return nil, fmt.Errorf("failed to read the flat bench media: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.Media > 0 {
			out[r.View] = r.Media
		}
	}
	return out, nil
}

// FlatBenchMedia returns view → media id of the plate on each side's flat slot (sides without one
// are absent). ErrDesignNotFound when the card does not exist.
func (s *Store) FlatBenchMedia(ctx context.Context, cardID int) (map[string]int, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	if err := refuseUnknownCard(ctx, s.DB, cardID); err != nil {
		return nil, err
	}
	return flatBenchMedia(ctx, s.DB, cardID)
}

// GetPartsSuggestion returns the cached answer for one cut of one flat, or nil when there is none.
func (s *Store) GetPartsSuggestion(ctx context.Context, cardID int, view string, baseMediaID int, algoRev string) (*entity.DesignPartsSuggestion, error) {
	row, err := storeutil.QueryNamedOne[partsRow](ctx, s.DB, partsSelect+`
		WHERE ps.tech_card_id = :card AND ps.view_key = :view AND ps.base_media_id = :base AND ps.algo_rev = :rev`,
		map[string]any{"card": cardID, "view": view, "base": baseMediaID, "rev": algoRev})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the parts suggestion: %w", err)
	}
	out, err := row.entity()
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SavePartsSuggestion upserts the answer on its (card, view, base media, algo rev) key — a forced
// re-ask replaces the old answer — and returns the stored row.
func (s *Store) SavePartsSuggestion(ctx context.Context, in entity.DesignPartsSuggestion) (*entity.DesignPartsSuggestion, error) {
	if err := requireCard(in.TechCardId); err != nil {
		return nil, err
	}
	doc := partsDoc{Parts: in.Parts, SplitNeeded: in.SplitNeeded}
	if doc.Parts == nil {
		doc.Parts = []entity.DesignPartGroup{}
	}
	if doc.SplitNeeded == nil {
		doc.SplitNeeded = []entity.DesignPartSplit{}
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the parts suggestion: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO design_parts_suggestion
			(tech_card_id, view_key, base_media_id, algo_rev, parts, model, created_by, created_at)
		VALUES (:card, :view, :base, :rev, :parts, :model, :by, CURRENT_TIMESTAMP(6))
		ON DUPLICATE KEY UPDATE parts = VALUES(parts), model = VALUES(model),
			created_by = VALUES(created_by), created_at = VALUES(created_at)`,
		map[string]any{
			"card": in.TechCardId, "view": in.View, "base": in.BaseMediaId, "rev": in.AlgoRev,
			"parts": string(body), "model": in.Model, "by": in.CreatedBy,
		}); err != nil {
		return nil, fmt.Errorf("failed to save the parts suggestion: %w", err)
	}
	out, err := s.GetPartsSuggestion(ctx, in.TechCardId, in.View, in.BaseMediaId, in.AlgoRev)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("the parts suggestion vanished after its save")
	}
	return out, nil
}

// partsSuggestionsOfCurrentFlats — the band's half: every cut revision of the flat each side holds
// NOW. A row of a flat that left its slot is not read (the client would paint stale numbers).
func partsSuggestionsOfCurrentFlats(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignPartsSuggestion, error) {
	flats, err := flatBenchMedia(ctx, db, cardID)
	if err != nil {
		return nil, err
	}
	out := []entity.DesignPartsSuggestion{}
	if len(flats) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[partsRow](ctx, db, partsSelect+`
		WHERE ps.tech_card_id = :card ORDER BY ps.view_key, ps.algo_rev, ps.id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list the parts suggestions: %w", err)
	}
	for _, r := range rows {
		if flats[r.ViewKey] != r.BaseMediaId {
			continue
		}
		e, err := r.entity()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
