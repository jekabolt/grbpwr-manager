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

// GetEditLayer reads ONE layer WITH its strokes. It exists as its own method because GetBand
// deliberately lists layers without them: 512 KB is the cap per LAYER, a card may hold several,
// and shipping them all would make opening the tab cost megabytes to draw a list of thumbnails.
func (s *Store) GetEditLayer(ctx context.Context, cardID, layerID int) (*entity.DesignEditLayer, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	var out entity.DesignEditLayer
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		l, err := layerByID(ctx, rep.DB(), layerID)
		if err != nil {
			return err
		}
		if l.TechCardId != cardID {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, layerID, l.TechCardId)
		}
		out = l
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SaveEditLayer stores a vector layer under compare-and-set on its rev. layer_id = 0 creates one;
// with base_media_id = 0 that is the clean vector base of the «draw it» door, and a card may hold
// several of those — uq_design_edit_layer_base tolerates repeated NULLs.
//
// CAS IS NOT MADE REDUNDANT BY SERIALIZABLE. The isolation level orders two writers; it cannot
// tell that the second one was looking at r3 while r4 already existed.
func (s *Store) SaveEditLayer(ctx context.Context, req entity.DesignEditLayerSave) (*entity.DesignEditLayer, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if len(req.Strokes) > MaxStrokesBytes {
		return nil, fmt.Errorf("%w: %d bytes of strokes, the ceiling is %d",
			entity.ErrDesignStrokesTooLarge, len(req.Strokes), MaxStrokesBytes)
	}
	var out entity.DesignEditLayer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if req.LayerId == 0 {
			if req.ExpectedRev != 0 {
				return fmt.Errorf("%w: a layer that does not exist yet is at rev 0",
					entity.ErrDesignLayerRevMismatch)
			}
			id, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_edit_layer (tech_card_id, base_media_id, rev, strokes, updated_by)
				VALUES (:card, :base, 1, :strokes, :who)`,
				map[string]any{
					"card": req.TechCardId, "base": nullInt(req.BaseMediaId),
					"strokes": jsonOrNil(req.Strokes), "who": req.Actor,
				})
			if err != nil {
				// uq_design_edit_layer_base means a layer over THIS base already exists on this
				// card. That is not a duplicate to swallow: the caller believed it was creating
				// one, so the honest answer is the CAS refusal that makes it reload and continue
				// the existing layer.
				if isDupKey(err) {
					return fmt.Errorf("%w: a layer over this base already exists",
						entity.ErrDesignLayerRevMismatch)
				}
				return fmt.Errorf("failed to create design edit layer: %w", err)
			}
			out, err = layerByID(ctx, db, id)
			return err
		}

		before, err := layerByID(ctx, db, req.LayerId)
		if err != nil {
			return err
		}
		if before.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, req.LayerId, before.TechCardId)
		}
		n, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_edit_layer
			SET strokes = :strokes, rev = rev + 1, updated_by = :who
			WHERE id = :id AND rev = :expected`,
			map[string]any{
				"strokes": jsonOrNil(req.Strokes), "who": req.Actor,
				"id": req.LayerId, "expected": req.ExpectedRev,
			})
		if err != nil {
			return fmt.Errorf("failed to save design edit layer %d: %w", req.LayerId, err)
		}
		if n == 0 {
			after, rerr := layerByID(ctx, db, req.LayerId)
			if rerr != nil {
				return rerr
			}
			return fmt.Errorf("%w: layer is at rev %d, %d was echoed",
				entity.ErrDesignLayerRevMismatch, after.Rev, req.ExpectedRev)
		}
		out, err = layerByID(ctx, db, req.LayerId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ImportVector files an ALREADY-UPLOADED vector file into the band as an edit layer: the media row
// keeps the authoritative SVG, the layer keeps the editable projection of it, and
// design_edit_layer.source_media_id is the edge between them.
//
// IT SPENDS NOTHING, AND THAT IS THE LINE BETWEEN IT AND GENERATION. Vectorising BY MACHINE is a
// paid provider call and goes through StartRun with kind = vector; this files a file that already
// exists. Two doors for the money would be two budget checks, and one of them would eventually be
// the one nobody updated.
//
// THE CLIENT PARSES, THE STORE RECORDS THE PROVENANCE — the same division of labour
// FlattenEditLayer already draws, and for the same reason: there is no SVG parser and no vector
// renderer anywhere in this repository, so the only honest producer of strokes is the canvas that
// is about to draw them. `strokes` may legitimately be empty and then means «file the file»: the
// layer holds the vector without an editable form of it yet.
//
// ⚠ IDEMPOTENCY IS BY (tech_card_id, source_media_id), NOT BY client_request_id, and the reason is
// the table: design_edit_layer (0343) has no request-id column and 0350 did not add one. The retry
// that matters — a lost response — arrives carrying the SAME media id, because the file went up
// through UploadContentImage before this call and the client holds its id. The re-read runs INSIDE
// the SERIALIZABLE transaction, where an ordinary SELECT already blocks, so two concurrent retries
// cannot both insert; the guarantee is a lock, not a hope.
func (s *Store) ImportVector(ctx context.Context, req entity.DesignVectorImport) (*entity.DesignEditLayer, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.SourceMediaId <= 0 {
		return nil, fmt.Errorf("%w: an import needs the vector file it is filing",
			entity.ErrDesignInvalidArgument)
	}
	// `drawn` IS REFUSED, and it is not pedantry: a layer drawn from nothing is born by
	// SaveEditLayer and has no file to import at all. Accepting it would file a layer claiming a
	// provenance its own source_media_id contradicts.
	if !entity.IsDesignImportableLayerOrigin(req.Origin) {
		return nil, fmt.Errorf("%w: origin %q is not imported | vectorised",
			entity.ErrDesignInvalidArgument, req.Origin)
	}
	if len(req.Strokes) > MaxStrokesBytes {
		return nil, fmt.Errorf("%w: %d bytes of strokes, the ceiling is %d",
			entity.ErrDesignStrokesTooLarge, len(req.Strokes), MaxStrokesBytes)
	}
	var out entity.DesignEditLayer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		// ─── the idempotent short-circuit, read under SERIALIZABLE ───
		//
		// It returns the EXISTING layer untouched rather than re-writing it: the second call is a
		// retry of the first, and a retry that overwrote the strokes would throw away whatever
		// editing happened between the two.
		existing, err := storeutil.QueryListNamed[entity.DesignEditLayer](ctx, db, `
			SELECT * FROM design_edit_layer
			WHERE tech_card_id = :card AND source_media_id = :src ORDER BY id LIMIT 1`,
			map[string]any{"card": req.TechCardId, "src": req.SourceMediaId})
		if err != nil {
			return fmt.Errorf("failed to look for an already-imported vector: %w", err)
		}
		if len(existing) > 0 {
			out = existing[0]
			return nil
		}

		// ─── the file must exist, and the raster must belong to THIS card ───
		//
		// The FK would catch a missing media with a raw 1452 naming a constraint; caught here it
		// is an InvalidArgument that names the id the caller sent. The picture check the schema
		// cannot make at all: design_picture(id) is a valid target no matter whose card it is on,
		// and a lineage pointing at somebody else's flat is a lie the band would then draw.
		media, err := resolveMediaIDs(ctx, rep, []int{req.SourceMediaId, req.BaseMediaId})
		if err != nil {
			return fmt.Errorf("failed to resolve the vector source media: %w", err)
		}
		if _, ok := media[req.SourceMediaId]; !ok {
			return fmt.Errorf("%w: media %d does not exist",
				entity.ErrDesignInvalidArgument, req.SourceMediaId)
		}
		if req.BaseMediaId > 0 {
			if _, ok := media[req.BaseMediaId]; !ok {
				return fmt.Errorf("%w: base media %d does not exist",
					entity.ErrDesignInvalidArgument, req.BaseMediaId)
			}
		}
		if req.SourcePictureId > 0 {
			pic, err := pictureByID(ctx, db, req.SourcePictureId)
			if err != nil {
				return err
			}
			if pic.TechCardId != req.TechCardId {
				return fmt.Errorf("%w: picture %d belongs to tech card %d",
					entity.ErrDesignNotFound, req.SourcePictureId, pic.TechCardId)
			}
		}

		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_edit_layer
				(tech_card_id, base_media_id, rev, strokes, origin, source_media_id,
				 source_picture_id, updated_by)
			VALUES (:card, :base, 1, :strokes, :origin, :src, :pic, :who)`,
			map[string]any{
				"card": req.TechCardId, "base": nullInt(req.BaseMediaId),
				"strokes": jsonOrNil(req.Strokes), "origin": req.Origin,
				"src": req.SourceMediaId, "pic": nullInt(req.SourcePictureId),
				"who": req.Actor,
			})
		if err != nil {
			// uq_design_edit_layer_base: a layer over THIS base already exists on this card. Same
			// answer as SaveEditLayer gives, and for the same reason — the caller believed it was
			// creating one, so the honest reply is the CAS refusal that makes it reload and
			// continue the layer that is already there.
			if isDupKey(err) {
				return fmt.Errorf("%w: a layer over this base already exists",
					entity.ErrDesignLayerRevMismatch)
			}
			return fmt.Errorf("failed to import the design vector: %w", err)
		}
		out, err = layerByID(ctx, db, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FlattenEditLayer files an ALREADY-RASTERISED image as a picture of the band, carrying
// derived_from, source_class and layer_rev. The server does not rasterise (Р-2): the client
// produced the raster from base + layer and uploaded it, and the server records the provenance.
//
// expected_rev IS REQUIRED and is not a convenience. Without it a colleague's newer save gets
// materialised under somebody else's intention: the person looked at r3, the file they uploaded
// depicts r3, and the row would claim r4.
//
// THE FLATTEN DOES NOT BUMP THE LAYER'S REV. It materialises a revision, it does not edit one —
// bumping would invalidate every open editor's CAS token for a write that changed no stroke.
func (s *Store) FlattenEditLayer(ctx context.Context, req entity.DesignEditLayerFlatten) (*entity.DesignPicture, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.MediaId <= 0 {
		return nil, fmt.Errorf("%w: a flatten needs the rasterised media", entity.ErrDesignInvalidArgument)
	}
	var out entity.DesignPicture
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		layer, err := layerByID(ctx, db, req.LayerId)
		if err != nil {
			return err
		}
		if layer.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: layer %d belongs to tech card %d",
				entity.ErrDesignNotFound, req.LayerId, layer.TechCardId)
		}
		if layer.Rev != req.ExpectedRev {
			return fmt.Errorf("%w: layer is at rev %d, %d was echoed",
				entity.ErrDesignLayerRevMismatch, layer.Rev, req.ExpectedRev)
		}
		if len(layer.Strokes) == 0 || string(layer.Strokes) == "null" || string(layer.Strokes) == "[]" {
			return fmt.Errorf("%w: layer %d has no strokes", entity.ErrDesignEmptyLayer, req.LayerId)
		}

		// The base picture, when the layer has one, is both the derivation parent and the row the
		// flatten hangs under: a flatten is a SIBLING of what it was traced over, not a run of its
		// own, because no money was spent on it.
		var parent *entity.DesignPicture
		if layer.BaseMediaId.Valid && layer.BaseMediaId.Int32 > 0 {
			rows, err := storeutil.QueryListNamed[entity.DesignPicture](ctx, db, `
				SELECT * FROM design_picture
				WHERE tech_card_id = :card AND media_id = :media ORDER BY id LIMIT 1`,
				map[string]any{"card": req.TechCardId, "media": layer.BaseMediaId.Int32})
			if err != nil {
				return fmt.Errorf("failed to resolve the base picture of layer %d: %w", req.LayerId, err)
			}
			if len(rows) > 0 {
				parent = &rows[0]
			}
		}

		// PROVENANCE. A layer over a picture produces an edit of that picture (ai_edits); a layer
		// over nothing produces a drawing (drawn). Both strings come from the wire vocabulary of
		// DesignPicture.source_class — see entity.DesignSourceAIEdits for why the wire wins over
		// the migration's prose.
		src := entity.DesignSourceDrawn
		ghost, kind := any(nil), entity.DesignPictureKindFlat
		mixed := false
		runID, batchID, derived := any(nil), any(nil), any(nil)
		if parent != nil {
			src = entity.DesignSourceAIEdits
			kind = parent.Kind
			mixed = parent.MixedInput
			runID, batchID, derived = nullInt32(parent.RunId), nullInt32(parent.BatchId), parent.Id
			if parent.GhostView.Valid {
				ghost = parent.GhostView.String
			}
		}
		ord := 0
		if parent != nil {
			if ord, err = nextSiblingOrdinal(ctx, db, *parent); err != nil {
				return err
			}
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_picture
				(tech_card_id, media_id, run_id, batch_id, ordinal, kind, ghost_view,
				 derived_from, source_class, mixed_input, layer_rev)
			VALUES (:card, :media, :run, :batch, :ord, :kind, :ghost, :parent, :src, :mixed, :layer)`,
			map[string]any{
				"card": req.TechCardId, "media": req.MediaId, "run": runID, "batch": batchID,
				"ord": ord, "kind": kind, "ghost": ghost, "parent": derived,
				"src": src, "mixed": mixed, "layer": layer.Rev,
			})
		if err != nil {
			return fmt.Errorf("failed to file the flattened design layer: %w", err)
		}
		out, err = pictureByID(ctx, db, id)
		if err != nil {
			return err
		}
		return resolveMedia(ctx, rep, []*entity.DesignPicture{&out})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func layerByID(ctx context.Context, db dependency.DB, id int) (entity.DesignEditLayer, error) {
	l, err := storeutil.QueryNamedOne[entity.DesignEditLayer](ctx, db,
		`SELECT * FROM design_edit_layer WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return l, fmt.Errorf("%w: design edit layer %d", entity.ErrDesignNotFound, id)
		}
		return l, fmt.Errorf("failed to read design edit layer %d: %w", id, err)
	}
	return l, nil
}
