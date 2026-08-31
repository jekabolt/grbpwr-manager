package design

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// THE CARD'S ASSET SHELVES (0354, V-11) — cloths, patterns and hardware — and the marks those
// assets leave on the flats.
//
// ONE TABLE WITH A `kind`, NOT THREE, and the whole argument lives in the head of
// 0354_design_asset.sql rather than here. What this file owns is the half of it Go has to enforce,
// because the schema deliberately cannot: `kind` carries no CHECK (a late ADD CONSTRAINT is a full
// table COPY under a hardcoded five-minute migration ceiling, i.e. a halted production start), the
// pattern-only fields carry no CHECK either, and «this asset and this picture are the SAME card's»
// is not expressible as a foreign key at all.
//
// EVERY ONE OF THOSE REFUSALS IS READ INSIDE THE WRITE TRANSACTION. It is already SERIALIZABLE
// (see the package header), so «read, check, write» is honest here — and a guard read outside it
// would be a TOCTOU with a nicer name.

// assetByID reads one shelf row inside the caller's transaction.
func assetByID(ctx context.Context, db dependency.DB, id int) (entity.DesignAsset, error) {
	a, err := storeutil.QueryNamedOne[entity.DesignAsset](ctx, db,
		`SELECT * FROM design_asset WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("%w: design asset %d", entity.ErrDesignNotFound, id)
		}
		return a, fmt.Errorf("failed to read design asset %d: %w", id, err)
	}
	return a, nil
}

// requireAssetOfCard reads the row and refuses one that belongs to a DIFFERENT card.
//
// cardID <= 0 means «the caller does not name a card», and that is a real case rather than a hole:
// DeleteDesignAssetRequest carries the asset id and nothing else, exactly as
// DeleteDesignDetailSlotRequest carries the slot id and nothing else. A minted id already names its
// card; demanding a second copy of that fact from the client would be demanding a fact it could
// only get wrong.
func requireAssetOfCard(ctx context.Context, db dependency.DB, cardID, assetID int) (entity.DesignAsset, error) {
	a, err := assetByID(ctx, db, assetID)
	if err != nil {
		return a, err
	}
	if cardID > 0 && a.TechCardId != cardID {
		return a, fmt.Errorf("%w: design asset %d belongs to tech card %d",
			entity.ErrDesignNotFound, a.Id, a.TechCardId)
	}
	return a, nil
}

// UpsertAsset writes ONE shelf row — creating it when AssetId is 0, replacing it otherwise.
//
// ONE VERB FOR BOTH GESTURES, because the screen has one: a shelf tile is filled in and saved. A
// separate create and update would be a second place to forget the ordinal or the parentage.
//
// WHAT IS CHECKED WHERE. The seven rules that need nothing but the request are in
// entity.DesignAssetUpsert.Validate — they are words of the contract, and a rule that can only be
// exercised against a live database is a rule nobody exercises. The three that need a row are
// here, inside the transaction: the parent exists and is this card's, the media is not another
// card's, and the shelves have not hit their ceiling.
func (s *Store) UpsertAsset(ctx context.Context, req entity.DesignAssetUpsert) (*entity.DesignAsset, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.AssetId < 0 {
		return nil, fmt.Errorf("%w: asset id %d", entity.ErrDesignInvalidArgument, req.AssetId)
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	note := strings.TrimSpace(req.Note)

	var out *entity.DesignAsset
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		// THE SAME NEGATIVE BOUNDARY THE REST OF THE BAND USES — «not another card's», never «this
		// card's». A file freshly uploaded through the media door belongs to no card at all and is
		// a perfectly legal texture; a positive rule would refuse it and force a human to save the
		// whole card before naming a cloth. See refuseForeignMedia for the full argument.
		if req.MediaId != 0 {
			if err := refuseForeignMedia(ctx, db, req.TechCardId, req.MediaId); err != nil {
				return err
			}
		}
		// THE PARENT IS READ, NOT ASSUMED. The foreign key says «some design_asset row», never
		// «one of THIS card's» — so without this read a pattern could be hung off a cloth of a
		// different style and the schema would accept it silently.
		if req.DerivedFromAssetId != 0 {
			if _, err := requireAssetOfCard(ctx, db, req.TechCardId, req.DerivedFromAssetId); err != nil {
				return err
			}
		}

		id := req.AssetId
		params := map[string]any{
			"card":        req.TechCardId,
			"kind":        req.Kind,
			"name":        name,
			"media":       nullInt(req.MediaId),
			"colour_code": nullStr(strings.TrimSpace(req.ColourCode)),
			"colour_hex":  nullStr(strings.TrimSpace(req.ColourHex)),
			"note":        nullStr(note),
			"parent":      nullInt(req.DerivedFromAssetId),
			"repeat_mm":   req.RepeatMm,
			"rotation":    req.RotationDeg,
			"ord":         req.Ordinal,
			"who":         req.Actor,
		}

		if id == 0 {
			// THE CEILING IS COUNTED IN THIS TRANSACTION, not before it. Counted outside, two
			// people adding the fortieth and forty-first cloth at the same moment both see 39.
			n, err := storeutil.QueryCountNamed(ctx, db,
				`SELECT COUNT(*) FROM design_asset WHERE tech_card_id = :card`,
				map[string]any{"card": req.TechCardId})
			if err != nil {
				return fmt.Errorf("failed to count design assets: %w", err)
			}
			if n >= entity.MaxDesignAssetsPerCard {
				return fmt.Errorf("%w: tech card %d already holds %d shelf rows, the ceiling is %d",
					entity.ErrDesignAssetTooMany, req.TechCardId, n, entity.MaxDesignAssetsPerCard)
			}
			newID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_asset
					(tech_card_id, kind, name, media_id, colour_code, colour_hex, note,
					 derived_from_asset_id, repeat_mm, rotation_deg, ordinal,
					 created_by, created_at, updated_at)
				VALUES
					(:card, :kind, :name, :media, :colour_code, :colour_hex, :note,
					 :parent, :repeat_mm, :rotation, :ord,
					 :who, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, params)
			if err != nil {
				return fmt.Errorf("failed to create design asset: %w", err)
			}
			id = newID
		} else {
			// THE ROW IS READ BEFORE IT IS WRITTEN, and the bare UPDATE below could not replace
			// this read. `WHERE id = :id AND tech_card_id = :card` affecting zero rows is
			// ambiguous — «no such asset», «somebody else's asset» and «you saved the tile
			// unchanged» all look identical — and answering the third with NotFound would tell a
			// person their shelf vanished for pressing Save twice.
			if _, err := requireAssetOfCard(ctx, db, req.TechCardId, id); err != nil {
				return err
			}
			params["id"] = id
			// created_by / created_at ARE NOT IN THE SET LIST. Who put the cloth on the shelf is
			// not rewritten by whoever edits its colour later; the editor's name would then be the
			// only name the row carries, and the byline would lie about a row nobody created twice.
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_asset SET
					kind = :kind, name = :name, media_id = :media,
					colour_code = :colour_code, colour_hex = :colour_hex, note = :note,
					derived_from_asset_id = :parent, repeat_mm = :repeat_mm,
					rotation_deg = :rotation, ordinal = :ord,
					updated_at = UTC_TIMESTAMP(6)
				WHERE id = :id AND tech_card_id = :card`, params); err != nil {
				return fmt.Errorf("failed to update design asset %d: %w", id, err)
			}
		}

		saved, err := assetByID(ctx, db, id)
		if err != nil {
			return err
		}
		// The response carries the file, not only its id: the tile that comes back is the tile the
		// screen redraws, and a bare media_id would blank the swatch it just saved.
		//
		// ⚠ THE ROW GOES THROUGH A SLICE AND COMES BACK OUT OF IT. attachAssetMedia fills its
		// argument IN PLACE, so handing it a fresh one-element literal and then returning `saved`
		// would return the copy that was never touched — a silent «no file» on every save.
		one := []entity.DesignAsset{saved}
		if err := attachAssetMedia(ctx, rep, one); err != nil {
			return err
		}
		out = &one[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAsset removes ONE shelf row and reports how many marks on flats went with it.
//
// THE COUNT IS TAKEN BEFORE THE DELETE, and it has to be: the marks go with the row by
// ON DELETE CASCADE, so after the statement there is nothing left to count. The number is not
// decoration — the screen states it before it asks and repeats it after, because a delete that
// silently erased eight markings is a delete nobody could have predicted from what they were
// looking at.
//
// A PATTERN BUILT FROM THIS ASSET SURVIVES, its parentage cleared by the FK's SET NULL. That is
// the schema's decision and it is right: a pattern with a picture and a repeat is a usable
// instruction to a factory after its swatch is gone.
func (s *Store) DeleteAsset(ctx context.Context, techCardID, assetID int) (int, error) {
	if assetID <= 0 {
		return 0, fmt.Errorf("%w: asset id is required", entity.ErrDesignInvalidArgument)
	}
	removed := 0
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		removed = 0
		db := rep.DB()
		asset, err := requireAssetOfCard(ctx, db, techCardID, assetID)
		if err != nil {
			return err
		}
		n, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM design_asset_placement WHERE asset_id = :asset`,
			map[string]any{"asset": asset.Id})
		if err != nil {
			return fmt.Errorf("failed to count the marks of design asset %d: %w", asset.Id, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM design_asset WHERE id = :id`, map[string]any{"id": asset.Id}); err != nil {
			return fmt.Errorf("failed to delete design asset %d: %w", asset.Id, err)
		}
		removed = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// SetAssetPlacement puts ONE mark on ONE flat, or moves an existing one.
//
// BOTH ENDS ARE CHECKED AGAINST THE SAME CARD, in this transaction, and neither check is
// expressible in the schema: design_asset_placement deliberately carries no tech_card_id (a second
// home for one fact drifts from the first at the first move), so the two foreign keys can each be
// satisfied by a row of a DIFFERENT style and the database would see nothing wrong.
func (s *Store) SetAssetPlacement(ctx context.Context, req entity.DesignAssetPlacementSet) (*entity.DesignAssetPlacement, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.PlacementId < 0 {
		return nil, fmt.Errorf("%w: placement id %d", entity.ErrDesignInvalidArgument, req.PlacementId)
	}
	if req.AssetId <= 0 {
		return nil, fmt.Errorf("%w: a placement names the asset it places", entity.ErrDesignInvalidArgument)
	}
	if req.PictureId <= 0 {
		return nil, fmt.Errorf("%w: a placement names the flat it is drawn on", entity.ErrDesignInvalidArgument)
	}
	// AN EMPTY ANNOTATION IS REFUSED RATHER THAN STORED. The column is NOT NULL and the row means
	// «this asset is HERE»; a mark with no geometry is a row that says «here» about nowhere, and
	// the screen would draw nothing while the shelf claimed the flat was marked. JSON `null` is
	// the same emptiness spelled a second way, so it is refused with it.
	ann := bytes.TrimSpace(req.Annotation)
	if len(ann) == 0 || string(ann) == "null" {
		return nil, fmt.Errorf("%w: a placement is a mark on a drawing and needs its geometry",
			entity.ErrDesignInvalidArgument)
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > entity.MaxDesignAssetNoteRunes {
		return nil, fmt.Errorf("%w: a placement note is at most %d characters",
			entity.ErrDesignInvalidArgument, entity.MaxDesignAssetNoteRunes)
	}

	var out *entity.DesignAssetPlacement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		asset, err := requireAssetOfCard(ctx, db, req.TechCardId, req.AssetId)
		if err != nil {
			return err
		}
		pic, err := pictureByID(ctx, db, req.PictureId)
		if err != nil {
			return err
		}
		// foreign_card_plate, the SAME refusal the bench raises for the same fact — a plate of
		// another card. One fact must not grow two machine tokens; the client already knows how to
		// act on this one.
		if pic.TechCardId != req.TechCardId {
			return fmt.Errorf("%w: picture %d belongs to tech card %d",
				entity.ErrDesignForeignCardPlate, pic.Id, pic.TechCardId)
		}

		params := map[string]any{
			"asset": asset.Id,
			"pic":   pic.Id,
			"ann":   []byte(ann),
			"note":  nullStr(note),
			"who":   req.Actor,
		}
		id := req.PlacementId
		if id == 0 {
			newID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_asset_placement
					(asset_id, picture_id, annotation, note, set_by, set_at)
				VALUES (:asset, :pic, :ann, :note, :who, UTC_TIMESTAMP(6))`, params)
			if err != nil {
				return fmt.Errorf("failed to place design asset %d: %w", asset.Id, err)
			}
			id = newID
		} else {
			if _, err := placementOfCard(ctx, db, req.TechCardId, id); err != nil {
				return err
			}
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_asset_placement SET
					asset_id = :asset, picture_id = :pic, annotation = :ann, note = :note,
					set_by = :who, set_at = UTC_TIMESTAMP(6)
				WHERE id = :id`, params); err != nil {
				return fmt.Errorf("failed to move design asset placement %d: %w", id, err)
			}
		}
		saved, err := placementByID(ctx, db, id)
		if err != nil {
			return err
		}
		out = &saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAssetPlacement takes ONE mark off a flat. The asset stays on its shelf: unmarking and
// removing are different acts, exactly as emptying a bench slot is not deleting the plate.
func (s *Store) DeleteAssetPlacement(ctx context.Context, techCardID, placementID int) error {
	if placementID <= 0 {
		return fmt.Errorf("%w: placement id is required", entity.ErrDesignInvalidArgument)
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		pl, err := placementOfCard(ctx, db, techCardID, placementID)
		if err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM design_asset_placement WHERE id = :id`,
			map[string]any{"id": pl.Id}); err != nil {
			return fmt.Errorf("failed to delete design asset placement %d: %w", pl.Id, err)
		}
		return nil
	})
}

// placementByID reads one mark inside the caller's transaction.
func placementByID(ctx context.Context, db dependency.DB, id int) (entity.DesignAssetPlacement, error) {
	p, err := storeutil.QueryNamedOne[entity.DesignAssetPlacement](ctx, db,
		`SELECT * FROM design_asset_placement WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("%w: design asset placement %d", entity.ErrDesignNotFound, id)
		}
		return p, fmt.Errorf("failed to read design asset placement %d: %w", id, err)
	}
	return p, nil
}

// placementOfCard reads one mark THROUGH its asset, which is the only way to scope it by card —
// design_asset_placement carries no tech_card_id by design (0354). cardID <= 0 skips the scoping,
// for the same reason requireAssetOfCard does: the delete verb is addressed by id alone.
func placementOfCard(ctx context.Context, db dependency.DB, cardID, id int) (entity.DesignAssetPlacement, error) {
	if cardID <= 0 {
		return placementByID(ctx, db, id)
	}
	rows, err := storeutil.QueryListNamed[entity.DesignAssetPlacement](ctx, db, `
		SELECT p.* FROM design_asset_placement p
		JOIN design_asset a ON a.id = p.asset_id
		WHERE p.id = :id AND a.tech_card_id = :card`,
		map[string]any{"id": id, "card": cardID})
	if err != nil {
		return entity.DesignAssetPlacement{}, fmt.Errorf("failed to read design asset placement %d: %w", id, err)
	}
	if len(rows) == 0 {
		return entity.DesignAssetPlacement{},
			fmt.Errorf("%w: design asset placement %d on tech card %d", entity.ErrDesignNotFound, id, cardID)
	}
	return rows[0], nil
}

// listAssets reads the whole shelf wall of a card, ordered the way the wall is drawn: shelf by
// shelf, then by the position a person gave the tile, then by birth order so that two tiles left
// at ordinal 0 keep a stable sequence instead of swapping on every read. The ordering matches
// idx_design_asset_card (tech_card_id, kind, ordinal, id) column for column.
func listAssets(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignAsset, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignAsset](ctx, db, `
		SELECT * FROM design_asset WHERE tech_card_id = :card ORDER BY kind, ordinal, id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design assets: %w", err)
	}
	return rows, nil
}

// listAssetPlacements reads every mark those assets left on this card's flats.
//
// ⚠ THE JOIN IS THE SCOPE, not an ornament: design_asset_placement has no tech_card_id at all
// (0354 says why — a second home for one fact diverges from the first), so «this card's marks» is
// reachable only through the asset. Drop the join and the band of every card serves the marks of
// every other.
func listAssetPlacements(ctx context.Context, db dependency.DB, cardID int) ([]entity.DesignAssetPlacement, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignAssetPlacement](ctx, db, `
		SELECT p.* FROM design_asset_placement p
		JOIN design_asset a ON a.id = p.asset_id
		WHERE a.tech_card_id = :card
		ORDER BY p.picture_id, p.id`,
		map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list design asset placements: %w", err)
	}
	return rows, nil
}

// attachAssetMedia resolves the file of every asset in ONE batch read, inside the caller's
// transaction. A missing media row leaves Media nil rather than dropping the asset: «the file
// disappeared» is a fact the shelf must be able to show, exactly as it is for a picture.
func attachAssetMedia(ctx context.Context, rep dependency.Repository, assets []entity.DesignAsset) error {
	ids := make([]int, 0, len(assets))
	for _, a := range assets {
		if a.MediaId.Valid && a.MediaId.Int32 > 0 {
			ids = append(ids, int(a.MediaId.Int32))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byID, err := resolveMediaIDs(ctx, rep, ids)
	if err != nil {
		return fmt.Errorf("failed to resolve design asset media: %w", err)
	}
	for i := range assets {
		if !assets[i].MediaId.Valid {
			continue
		}
		if m, ok := byID[int(assets[i].MediaId.Int32)]; ok {
			mm := m
			assets[i].Media = &mm
		}
	}
	return nil
}
