package design

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// DeletePicture removes a DERIVED picture FOR GOOD with everything derived from it (O-68, D-74).
//
// ROWS ONLY, ONE SERIALIZABLE TRANSACTION (s.txFunc; 1213/1205 are retried by store.Tx, and the
// callback starts every attempt from a blank verdict). What goes, in this order:
//
//  1. the edit layers OF THIS CARD whose base is the media of a picture in the set — a layer is
//     strokes over a concrete image and cannot be opened without it (0343), and its FK to media is
//     RESTRICT, so it goes before anything asks whether the media is free;
//  2. the picture rows, CHILDREN FIRST, level by level from the deepest: bench slots and the vector
//     origin of a layer SET NULL by their FKs (0341, 0350), asset placements CASCADE (0354).
//
// The media rows and the objects behind them are NOT touched here: that is the handler's business
// after the commit (DeleteMediaByIdIfUnused decides per media under its own lock, then the bucket).
// The media rows are READ here, before the picture rows go, so the handler still has their urls.
//
// Refusals: picture_not_found (NotFound — its own token, see entity), picture_is_root
// (FailedPrecondition — derived_from NULL). Hidden pictures and pictures standing in a slot go the
// same way as any other; HidePicture's guards are not consulted (D-74).
//
// actor IS NOT STAMPED: the rows are gone and there is no audit row for the band; it stays in the
// signature because every write of this band is called with it, and the handler logs it.
func (s *Store) DeletePicture(ctx context.Context, pictureID int, actor string) (*entity.DesignPictureDeletion, error) {
	_ = actor
	var out entity.DesignPictureDeletion
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = entity.DesignPictureDeletion{}
		db := rep.DB()
		pic, err := pictureByID(ctx, db, pictureID)
		if err != nil {
			if errors.Is(err, entity.ErrDesignNotFound) {
				return fmt.Errorf("%w: design picture %d", entity.ErrDesignPictureNotFound, pictureID)
			}
			return err
		}
		if err := entity.DesignPictureDeletable(pic); err != nil {
			return err
		}
		// THE BRANCH, LEVEL BY LEVEL. FORCE INDEX for the same reason as the overwrite guard's
		// branch read (layer.go): a plan over PRIMARY under SERIALIZABLE would lock the table.
		// No card predicate: derived_from names rows globally, and a child on another card
		// (corruption) must go with its parent rather than dangle.
		levels, err := entity.DesignCollectSubtree(entity.DesignSubtreeNodeOf(pic),
			func(parents []int) ([]entity.DesignSubtreeNode, error) {
				return storeutil.QueryListNamed[entity.DesignSubtreeNode](ctx, db, `
					SELECT `+entity.DesignSubtreeColumns+`
					FROM design_picture FORCE INDEX (idx_design_picture_derived_from)
					WHERE derived_from IN (:ids)
					ORDER BY derived_from, id`,
					map[string]any{"ids": parents})
			})
		if err != nil {
			return err
		}
		// Deletion order: deepest level first, the named picture last.
		var pictureIDs, mediaIDs []int
		seenMedia := map[int]struct{}{}
		for i := len(levels) - 1; i >= 0; i-- {
			for _, n := range levels[i] {
				pictureIDs = append(pictureIDs, n.Id)
				if n.MediaId == 0 {
					continue
				}
				if _, dup := seenMedia[n.MediaId]; dup {
					continue
				}
				seenMedia[n.MediaId] = struct{}{}
				mediaIDs = append(mediaIDs, n.MediaId)
			}
		}
		// The edit layers over those media, on this card — with the media of their own two
		// channels (raster 0355, vector source 0350), which nobody would hold once the layer is
		// gone; they join the list the handler judges, and DeleteMediaByIdIfUnused still decides.
		if len(mediaIDs) > 0 {
			type layerMedia struct {
				Raster sql.NullInt32 `db:"raster_media_id"`
				Source sql.NullInt32 `db:"source_media_id"`
			}
			layers, err := storeutil.QueryListNamed[layerMedia](ctx, db, `
				SELECT raster_media_id, source_media_id FROM design_edit_layer
				WHERE tech_card_id = :card AND base_media_id IN (:media)`,
				map[string]any{"card": pic.TechCardId, "media": mediaIDs})
			if err != nil {
				return fmt.Errorf("failed to read the edit layers over design picture %d: %w", pictureID, err)
			}
			var layerMediaIDs []int
			for _, l := range layers {
				for _, m := range []sql.NullInt32{l.Raster, l.Source} {
					if !m.Valid || m.Int32 == 0 {
						continue
					}
					if _, dup := seenMedia[int(m.Int32)]; dup {
						continue
					}
					seenMedia[int(m.Int32)] = struct{}{}
					layerMediaIDs = append(layerMediaIDs, int(m.Int32))
				}
			}
			if err := storeutil.ExecNamed(ctx, db, `
				DELETE FROM design_edit_layer WHERE tech_card_id = :card AND base_media_id IN (:media)`,
				map[string]any{"card": pic.TechCardId, "media": mediaIDs}); err != nil {
				return fmt.Errorf("failed to delete the edit layers over design picture %d: %w", pictureID, err)
			}
			mediaIDs = append(mediaIDs, layerMediaIDs...)
		}
		// The media rows, read before the picture rows go.
		media, err := resolveMediaIDs(ctx, rep, mediaIDs)
		if err != nil {
			return fmt.Errorf("failed to read the media of design picture %d: %w", pictureID, err)
		}
		for i := len(levels) - 1; i >= 0; i-- {
			ids := make([]int, 0, len(levels[i]))
			for _, n := range levels[i] {
				ids = append(ids, n.Id)
			}
			n, err := storeutil.ExecNamedRows(ctx, db,
				`DELETE FROM design_picture WHERE id IN (:ids)`, map[string]any{"ids": ids})
			if err != nil {
				return fmt.Errorf("failed to delete design pictures %v: %w", ids, err)
			}
			// The rows were read (and share-locked) in this transaction; fewer gone means the
			// picture of the branch is not the one being deleted — refuse rather than answer
			// with ids that did not go.
			if int(n) != len(ids) {
				return fmt.Errorf("design pictures %v: %d rows deleted, %d expected", ids, n, len(ids))
			}
		}
		out = entity.DesignPictureDeletion{PictureIds: pictureIDs, MediaIds: mediaIDs, Media: media}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
