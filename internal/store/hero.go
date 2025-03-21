package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type heroStore struct {
	*MYSQLStore
}

// Hero returns an object implementing hero interface
func (ms *MYSQLStore) Hero() dependency.Hero {
	return &heroStore{
		MYSQLStore: ms,
	}
}

func (hs *heroStore) RefreshHero(ctx context.Context) error {
	hero, err := hs.GetHero(ctx)
	if err != nil {
		return fmt.Errorf("failed to get hero: %w", err)
	}

	heroInsert := entity.HeroFullInsert{
		Entities:    []entity.HeroEntityInsert{},
		NavFeatured: entity.NavFeaturedInsert{},
	}
	for _, e := range hero.Entities {
		switch e.Type {
		case entity.HeroTypeFeaturedProducts:
			ids := make([]int, 0, len(e.FeaturedProducts.Products))
			for _, p := range e.FeaturedProducts.Products {
				ids = append(ids, p.Id)
			}
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, FeaturedProducts: entity.HeroFeaturedProductsInsert{
				ProductIDs:  ids,
				Headline:    e.FeaturedProducts.Headline,
				ExploreText: e.FeaturedProducts.ExploreText,
				ExploreLink: e.FeaturedProducts.ExploreLink,
			}})
		case entity.HeroTypeFeaturedProductsTag:
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, FeaturedProductsTag: entity.HeroFeaturedProductsTagInsert{
				Tag:         e.FeaturedProductsTag.Tag,
				Headline:    e.FeaturedProductsTag.Headline,
				ExploreText: e.FeaturedProductsTag.ExploreText,
				ExploreLink: e.FeaturedProductsTag.ExploreLink,
			}})
		case entity.HeroTypeMain:
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, Main: entity.HeroMainInsert{
				Single: entity.HeroSingleInsert{
					MediaPortraitId:  e.Main.Single.MediaPortrait.Id,
					MediaLandscapeId: e.Main.Single.MediaLandscape.Id,
					ExploreLink:      e.Main.Single.ExploreLink,
					ExploreText:      e.Main.Single.ExploreText,
					Headline:         e.Main.Single.Headline,
				},
				Tag:         e.Main.Tag,
				Description: e.Main.Description,
			}})
		case entity.HeroTypeSingle:
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, Single: entity.HeroSingleInsert{
				MediaPortraitId:  e.Single.MediaPortrait.Id,
				MediaLandscapeId: e.Single.MediaLandscape.Id,
				Headline:         e.Single.Headline,
				ExploreLink:      e.Single.ExploreLink,
				ExploreText:      e.Single.ExploreText,
			}})
		case entity.HeroTypeDouble:
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, Double: entity.HeroDoubleInsert{
				Left: entity.HeroSingleInsert{
					MediaPortraitId:  e.Double.Left.MediaPortrait.Id,
					MediaLandscapeId: e.Double.Left.MediaLandscape.Id,
					ExploreLink:      e.Double.Left.ExploreLink,
					ExploreText:      e.Double.Left.ExploreText,
					Headline:         e.Double.Left.Headline,
				},
				Right: entity.HeroSingleInsert{
					MediaPortraitId:  e.Double.Right.MediaPortrait.Id,
					MediaLandscapeId: e.Double.Right.MediaLandscape.Id,
					ExploreLink:      e.Double.Right.ExploreLink,
					ExploreText:      e.Double.Right.ExploreText,
					Headline:         e.Double.Right.Headline,
				},
			}})
		case entity.HeroTypeFeaturedArchive:
			heroInsert.Entities = append(heroInsert.Entities, entity.HeroEntityInsert{Type: e.Type, FeaturedArchive: entity.HeroFeaturedArchiveInsert{
				ArchiveId:   e.FeaturedArchive.Archive.Id,
				Tag:         e.FeaturedArchive.Tag,
				Headline:    e.FeaturedArchive.Headline,
				ExploreText: e.FeaturedArchive.ExploreText,
			}})
		}
	}

	err = hs.SetHero(ctx, heroInsert)
	if err != nil {
		return fmt.Errorf("failed to set hero: %w", err)
	}

	return nil
}

func isValidHeroType(t entity.HeroType) bool {
	switch t {
	case entity.HeroTypeSingle,
		entity.HeroTypeDouble,
		entity.HeroTypeMain,
		entity.HeroTypeFeaturedProducts,
		entity.HeroTypeFeaturedProductsTag,
		entity.HeroTypeFeaturedArchive:
		return true
	default:
		return false
	}
}

func (hs *heroStore) SetHero(ctx context.Context, hfi entity.HeroFullInsert) error {
	// Validate hero types
	for _, e := range hfi.Entities {
		if !isValidHeroType(e.Type) {
			return fmt.Errorf("invalid hero type: %d", e.Type)
		}
	}

	return hs.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Delete existing hero data
		err := deleteExistingHeroData(ctx, rep)
		if err != nil {
			return fmt.Errorf("failed to delete hero data: %w", err)
		}

		heroFull, err := buildHeroData(ctx, rep, hfi)
		if err != nil {
			return fmt.Errorf("failed to build hero data: %w", err)
		}

		if err := insertHeroData(ctx, rep, heroFull); err != nil {
			return fmt.Errorf("failed to insert hero data: %w", err)
		}

		// Update cache
		hero, err := rep.Hero().GetHero(ctx)
		if err != nil {
			return fmt.Errorf("failed to get hero: %w", err)
		}
		cache.UpdateHero(hero)

		return nil
	})
}

func (hs *heroStore) GetHero(ctx context.Context) (*entity.HeroFull, error) {
	query := `SELECT data FROM hero`

	type hero struct {
		Id        int       `db:"id"`
		CreatedAt time.Time `db:"created_at"`
		Data      []byte    `db:"data"`
	}

	heroRaw, err := QueryNamedOne[hero](ctx, hs.DB(), query, nil)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no hero data found")
		}
		return nil, fmt.Errorf("failed to get hero: %w", err)
	}
	heroFull := entity.HeroFull{}

	err = json.Unmarshal(heroRaw.Data, &heroFull)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal hero data: %w %s", err, string(heroRaw.Data))
	}

	return &heroFull, nil
}

func deleteExistingHeroData(ctx context.Context, rep dependency.Repository) error {
	query := `DELETE FROM hero`
	_, err := rep.DB().ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to delete hero data: %w", err)
	}

	return nil
}

func buildHeroData(ctx context.Context, rep dependency.Repository, heroFullInsert entity.HeroFullInsert) (*entity.HeroFull, error) {

	entities := make([]entity.HeroEntity, 0, len(heroFullInsert.Entities))
	for n, e := range heroFullInsert.Entities {
		switch e.Type {
		case entity.HeroTypeSingle:
			portraitMedia, err := rep.Media().GetMediaById(ctx, e.Single.MediaPortraitId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Single.MediaPortraitId))
				continue
			}
			landscapeMedia, err := rep.Media().GetMediaById(ctx, e.Single.MediaLandscapeId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Single.MediaLandscapeId))
				continue
			}
			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Single: &entity.HeroSingle{
					MediaPortrait:  *portraitMedia,
					MediaLandscape: *landscapeMedia,
					Headline:       e.Single.Headline,
					ExploreLink:    e.Single.ExploreLink,
					ExploreText:    e.Single.ExploreText,
				},
			})
		case entity.HeroTypeDouble:

			leftMediaId := e.Double.Left.MediaPortraitId
			if leftMediaId == 0 {
				leftMediaId = e.Double.Right.MediaPortraitId
			}

			leftMedia, err := rep.Media().GetMediaById(ctx, leftMediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", leftMediaId))
				continue
			}

			rightMediaId := e.Double.Right.MediaPortraitId
			if rightMediaId == 0 {
				rightMediaId = e.Double.Left.MediaPortraitId
			}

			rightMedia, err := rep.Media().GetMediaById(ctx, rightMediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", rightMediaId))
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Double: &entity.HeroDouble{
					Left: entity.HeroSingle{
						MediaPortrait:  *leftMedia,
						MediaLandscape: *rightMedia,
						ExploreLink:    e.Double.Left.ExploreLink,
						ExploreText:    e.Double.Left.ExploreText,
						Headline:       e.Double.Left.Headline,
					},
					Right: entity.HeroSingle{
						MediaPortrait:  *rightMedia,
						MediaLandscape: *leftMedia,
						ExploreLink:    e.Double.Right.ExploreLink,
						ExploreText:    e.Double.Right.ExploreText,
						Headline:       e.Double.Right.Headline,
					},
				},
			})
		case entity.HeroTypeMain:
			// main add should be only on first position
			if n != 0 {
				continue
			}
			portraitMedia, err := rep.Media().GetMediaById(ctx, e.Main.Single.MediaPortraitId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Main.Single.MediaPortraitId))
				continue
			}

			landscapeMedia, err := rep.Media().GetMediaById(ctx, e.Main.Single.MediaLandscapeId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Main.Single.MediaLandscapeId))
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Main: &entity.HeroMain{
					Single: entity.HeroSingle{
						MediaPortrait:  *portraitMedia,
						MediaLandscape: *landscapeMedia,
						ExploreLink:    e.Main.Single.ExploreLink,
						ExploreText:    e.Main.Single.ExploreText,
						Headline:       e.Main.Single.Headline,
					},
					Tag:         e.Main.Tag,
					Description: e.Main.Description,
				},
			})
		case entity.HeroTypeFeaturedProducts:
			if len(e.FeaturedProducts.ProductIDs) == 0 {
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, e.FeaturedProducts.ProductIDs)
			if err != nil {
				return nil, fmt.Errorf("failed to get products by ids: %w", err)
			}
			prds := make([]entity.Product, 0, len(products))
			for _, p := range products {
				if p.Hidden.Valid && p.Hidden.Bool {
					continue
				}
				prds = append(prds, p)
			}

			if len(prds) == 0 {
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				FeaturedProducts: &entity.HeroFeaturedProducts{
					Products:    prds,
					Headline:    e.FeaturedProducts.Headline,
					ExploreText: e.FeaturedProducts.ExploreText,
					ExploreLink: e.FeaturedProducts.ExploreLink,
				},
			})
		case entity.HeroTypeFeaturedProductsTag:
			if e.FeaturedProductsTag.Tag == "" {
				continue
			}

			products, err := rep.Products().GetProductsByTag(ctx, e.FeaturedProductsTag.Tag)
			if err != nil {
				return nil, fmt.Errorf("failed to get products by ids: %w", err)
			}

			prds := make([]entity.Product, 0, len(products))
			for _, p := range products {
				if p.Hidden.Valid && p.Hidden.Bool {
					continue
				}
				prds = append(prds, p)
			}

			if len(prds) == 0 {
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				FeaturedProductsTag: &entity.HeroFeaturedProductsTag{
					Products:    prds,
					Tag:         e.FeaturedProductsTag.Tag,
					Headline:    e.FeaturedProductsTag.Headline,
					ExploreText: e.FeaturedProductsTag.ExploreText,
					ExploreLink: e.FeaturedProductsTag.ExploreLink,
				},
			})
		case entity.HeroTypeFeaturedArchive:
			if e.FeaturedArchive.ArchiveId == 0 {
				slog.Error("failed to get archive by id",
					slog.Int("archive_id", e.FeaturedArchive.ArchiveId))
			}

			archive, err := rep.Archive().GetArchiveById(ctx, e.FeaturedArchive.ArchiveId)
			if err != nil {
				slog.Error("failed to get archive by id",
					slog.String("err", err.Error()),
					slog.Int("archive_id", e.FeaturedArchive.ArchiveId))
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				FeaturedArchive: &entity.HeroFeaturedArchive{
					Archive:     *archive,
					Tag:         e.FeaturedArchive.Tag,
					Headline:    e.FeaturedArchive.Headline,
					ExploreText: e.FeaturedArchive.ExploreText,
				},
			})
		}
	}
	heroFull := entity.HeroFull{
		Entities: entities,
		NavFeatured: entity.NavFeatured{
			Men: entity.NavFeaturedEntity{
				ExploreText:       heroFullInsert.NavFeatured.Men.ExploreText,
				FeaturedTag:       heroFullInsert.NavFeatured.Men.FeaturedTag,
				FeaturedArchiveId: heroFullInsert.NavFeatured.Men.FeaturedArchiveId,
			},
			Women: entity.NavFeaturedEntity{
				ExploreText:       heroFullInsert.NavFeatured.Women.ExploreText,
				FeaturedTag:       heroFullInsert.NavFeatured.Women.FeaturedTag,
				FeaturedArchiveId: heroFullInsert.NavFeatured.Women.FeaturedArchiveId,
			},
		},
	}

	if heroFullInsert.NavFeatured.Men.MediaId != 0 {
		menMedia, err := rep.Media().GetMediaById(ctx, heroFullInsert.NavFeatured.Men.MediaId)
		if err != nil {
			slog.Error("failed to get media by id",
				slog.String("err", err.Error()),
				slog.Int("media_id", heroFullInsert.NavFeatured.Men.MediaId))

		}
		heroFull.NavFeatured.Men.Media = *menMedia
	}

	if heroFullInsert.NavFeatured.Women.MediaId != 0 {
		womenMedia, err := rep.Media().GetMediaById(ctx, heroFullInsert.NavFeatured.Women.MediaId)
		if err != nil {
			slog.Error("failed to get media by id",
				slog.String("err", err.Error()),
				slog.Int("media_id", heroFullInsert.NavFeatured.Women.MediaId))
		}
		heroFull.NavFeatured.Women.Media = *womenMedia
	}

	return &heroFull, nil
}

func insertHeroData(ctx context.Context, rep dependency.Repository, hf *entity.HeroFull) error {
	jsonData, err := json.Marshal(hf)
	if err != nil {
		return fmt.Errorf("failed to marshal hero data: %w", err)
	}
	query := `INSERT INTO hero (data) VALUES (:data)`
	_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
		"data": jsonData,
	})
	if err != nil {
		return fmt.Errorf("failed to insert hero data: %w", err)
	}
	return nil
}
