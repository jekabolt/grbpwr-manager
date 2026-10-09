package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// THE SAVE-SIDE HALF OF THE technical_sheet INVARIANT (27.09, D-57).
//
// FlattenEditLayer refuses to stamp a picture whose file stands on the card's technical sheet
// (entity.ErrDesignTechnicalSheet). This is the other door: the card save must not ADD the file of an
// already-replaced picture of the same card to the sheet. It judges the transition, not the whole
// incoming state (round 3): a sheet that already carries such a file — possible on a card saved before
// this guard existed — keeps saving, and only an occurrence beyond what is stored now is refused. The
// rule itself — which occurrence, which head, which words — is entity.DesignSheetReplacedRefusal;
// this file only reads, inside the save's own transaction, before the DELETE loop rewrites the sheet.

// techCardSheetReplacedPictures — THE SAME CARD'S REPLACED PICTURES AMONG THE FILES THE INCOMING SHEET
// CARRIES, in one read over the sheet's distinct media ids (the moodboard is never asked).
//
// Plan and locks: idx_design_picture_card_media (tech_card_id, media_id, id), added by 0372 for this
// read, is equality on the card plus the IN list on the file — the scan, and so the shared next-key
// locks the save's SERIALIZABLE wrapper takes, stay on this card's pictures of these files. Without it
// the only candidates were (tech_card_id, id) — every picture of the card — and (media_id) — every
// card's use of the file. That lock is what serialises this read against a concurrent overwrite's
// stamp (UPDATE design_picture SET replaced_by … needs the row exclusively).
//
// NO ORDER BY, ON PURPOSE: the entity names the oldest picture of a shared file by id itself, so the
// row order carries nothing — and a sort by id is exactly what would tempt the optimizer back onto
// (tech_card_id, id).
const techCardSheetReplacedPictures = `
	SELECT id, tech_card_id, media_id, replaced_by, undone_at FROM design_picture
	WHERE tech_card_id = :card AND media_id IN (:media) AND replaced_by IS NOT NULL`

// techCardStoredSheetRows — HOW MANY TIMES EACH OF THESE FILES STANDS ON THE CARD'S TECHNICAL SHEET
// NOW: the rows the DELETE loop is about to rewrite, counted per file (a count, not a set — one
// inherited row must not license a second copy). Read only when some file of the incoming sheet
// belongs to a replaced picture; idx_tech_card_media_sheet (tech_card_id, media_id, category) answers
// it by equality on all three columns.
const techCardStoredSheetRows = `
	SELECT media_id, COUNT(*) AS n FROM tech_card_media
	WHERE tech_card_id = :card AND media_id IN (:media) AND category = :technical
	GROUP BY media_id`

// techCardSheetPictureLink — one link of a replacement chain, for the head walk
// (entity.DesignReplacementHead reads id, replaced_by, undone_at and tech_card_id — the walk stops before
// an undone link, T28 v2; a link of another card is
// corruption; the file rides along for the log).
const techCardSheetPictureLink = `
	SELECT id, tech_card_id, media_id, replaced_by, undone_at FROM design_picture WHERE id = :id`

// techCardSheetReplacedQuery binds the picture read: the card and the sheet's files, expanded by
// sqlx.In. Separate from the read so the words and the binds can be checked without a database.
func techCardSheetReplacedQuery(card int, media []int) (string, []any, error) {
	return storeutil.MakeQuery(techCardSheetReplacedPictures, map[string]any{"card": card, "media": media})
}

// techCardStoredSheetQuery binds the stored-sheet read: the card, the sheet's files and the sheet's
// word 'technical' (0092).
func techCardStoredSheetQuery(card int, media []int) (string, []any, error) {
	return storeutil.MakeQuery(techCardStoredSheetRows, map[string]any{
		"card": card, "media": media, "technical": string(entity.TechCardMediaCategoryTechnical),
	})
}

// techCardStoredSheetCounts — file → how many times it stands on the stored sheet. db is the save's
// transaction: the count has to come from the rows this save replaces, under the same locks — a count
// taken anywhere else could still show a row another save has meanwhile taken off, and the file would
// come back.
func techCardStoredSheetCounts(ctx context.Context, db dependency.DB, card int, media []int) (map[int]int, error) {
	query, args, err := techCardStoredSheetQuery(card, media)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		MediaId int `db:"media_id"`
		N       int `db:"n"`
	}
	if err := db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to read the stored technical sheet of tech card %d: %w", card, err)
	}
	stored := make(map[int]int, len(rows))
	for _, r := range rows {
		stored[r.MediaId] = r.N
	}
	return stored, nil
}

// refuseReplacedPicturesOnTheSheet — entity.DesignSheetReplacedRefusal over what this transaction
// reads. db MUST be the save's transaction (rep.DB() inside UpdateTechCardTx), and the call MUST come
// before the DELETE loop: the stored count is the sheet the save is about to replace, and read after
// the rewrite it would be the save's own incoming sheet — every occurrence would look inherited.
//
// A missing chain link is mapped to entity.ErrDesignNotFound, never passed on as sql.ErrNoRows:
// techCardWriteError turns sql.ErrNoRows into «tech card not found», and the card is right there.
// The walk then reports the broken chain without a sentinel (Internal), which is what it is.
func refuseReplacedPicturesOnTheSheet(ctx context.Context, db dependency.DB, card int, media []entity.TechCardMediaItem) error {
	ids := entity.DesignSheetMediaIds(media)
	if len(ids) == 0 {
		return nil
	}
	query, args, err := techCardSheetReplacedQuery(card, ids)
	if err != nil {
		return err
	}
	var replaced []entity.DesignPicture
	if err := db.SelectContext(ctx, &replaced, query, args...); err != nil {
		return fmt.Errorf("failed to read the replaced design pictures on the technical sheet of tech card %d: %w", card, err)
	}
	if len(replaced) == 0 {
		return nil // no file of the incoming sheet can offend, and the stored sheet is not worth a read
	}
	stored, err := techCardStoredSheetCounts(ctx, db, card, ids)
	if err != nil {
		return err
	}
	return entity.DesignSheetReplacedRefusal(card, media, stored, replaced, func(id int) (entity.DesignPicture, error) {
		p, err := storeutil.QueryNamedOne[entity.DesignPicture](ctx, db, techCardSheetPictureLink, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return p, fmt.Errorf("%w: design picture %d", entity.ErrDesignNotFound, id)
			}
			return p, fmt.Errorf("failed to load design picture %d: %w", id, err)
		}
		return p, nil
	})
}
