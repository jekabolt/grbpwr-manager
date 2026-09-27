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
// (entity.ErrDesignTechnicalSheet). This is the other door: the card save must not put the file of an
// already-replaced picture of the same card onto the sheet. The rule itself — which items, which
// head, which words — is entity.DesignSheetReplacedRefusal; this file only reads, inside the save's
// own transaction.

// techCardSheetReplacedPictures — THE SAME CARD'S REPLACED PICTURES AMONG THE FILES THE INCOMING SHEET
// CARRIES, in one read over the sheet's distinct media ids (the moodboard is never asked). ORDER BY id
// makes the answer independent of the plan; the entity names the oldest picture of a shared file
// anyway.
//
// Plan and locks: idx_design_picture_media (media_id) narrows to the few rows of these files, and
// under the save's SERIALIZABLE wrapper this read takes shared next-key locks on exactly those index
// entries and their rows — which is what serialises it against a concurrent overwrite's stamp
// (UPDATE design_picture SET replaced_by … needs the row exclusively).
const techCardSheetReplacedPictures = `
	SELECT id, tech_card_id, media_id, replaced_by FROM design_picture
	WHERE tech_card_id = :card AND media_id IN (:media) AND replaced_by IS NOT NULL
	ORDER BY id`

// techCardSheetPictureLink — one link of a replacement chain, for the head walk
// (entity.DesignReplacementHead reads id and replaced_by; card and file ride along for the log).
const techCardSheetPictureLink = `
	SELECT id, tech_card_id, media_id, replaced_by FROM design_picture WHERE id = :id`

// techCardSheetReplacedQuery binds the read: the card and the sheet's files, expanded by sqlx.In.
// Separate from the read so the words and the binds are checked without a database.
func techCardSheetReplacedQuery(card int, media []int) (string, []any, error) {
	return storeutil.MakeQuery(techCardSheetReplacedPictures, map[string]any{"card": card, "media": media})
}

// refuseReplacedPicturesOnTheSheet — entity.DesignSheetReplacedRefusal over what this transaction
// reads. db MUST be the save's transaction (rep.DB() inside UpdateTechCardTx): a read on another
// handle would answer from a state the save does not hold a lock on, and the race the guard exists
// for would be open again.
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
	return entity.DesignSheetReplacedRefusal(card, media, replaced, func(id int) (entity.DesignPicture, error) {
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
