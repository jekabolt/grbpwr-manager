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

	hei := []entity.HeroEntityInsert{}
	for _, e := range hero.Entities {
		switch e.Type {
		case entity.HeroTypeFeaturedProducts:
			ids := make([]int, 0, len(e.FeaturedProducts.Products))
			for _, p := range e.FeaturedProducts.Products {
				ids = append(ids, p.Id)
			}
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, FeaturedProducts: entity.HeroFeaturedProductsInsert{
				ProductIDs:  ids,
				Headline:    e.FeaturedProducts.Headline,
				ExploreText: e.FeaturedProducts.ExploreText,
				ExploreLink: e.FeaturedProducts.ExploreLink,
			}})
		case entity.HeroTypeFeaturedProductsTag:
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, FeaturedProductsTag: entity.HeroFeaturedProductsTagInsert{
				Tag:         e.FeaturedProductsTag.Tag,
				Headline:    e.FeaturedProductsTag.Headline,
				ExploreText: e.FeaturedProductsTag.ExploreText,
				ExploreLink: e.FeaturedProductsTag.ExploreLink,
			}})
		case entity.HeroTypeMain:
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, Main: entity.HeroMainInsert{
				Single: entity.HeroSingleInsert{
					MediaId:     e.Main.Single.Media.Id,
					ExploreLink: e.Main.Single.ExploreLink,
					ExploreText: e.Main.Single.ExploreText,
				},
				Tag:         e.Main.Tag,
				Description: e.Main.Description,
			}})
		case entity.HeroTypeSingle:
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, Single: entity.HeroSingleInsert{
				MediaId:     e.Single.Media.Id,
				Headline:    e.Single.Headline,
				ExploreLink: e.Single.ExploreLink,
				ExploreText: e.Single.ExploreText,
			}})
		case entity.HeroTypeDouble:
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, Double: entity.HeroDoubleInsert{
				Left: entity.HeroSingleInsert{
					MediaId:     e.Double.Left.Media.Id,
					ExploreLink: e.Double.Left.ExploreLink,
					ExploreText: e.Double.Left.ExploreText,
					Headline:    e.Double.Left.Headline,
				},
				Right: entity.HeroSingleInsert{
					MediaId:     e.Double.Right.Media.Id,
					ExploreLink: e.Double.Right.ExploreLink,
					ExploreText: e.Double.Right.ExploreText,
					Headline:    e.Double.Right.Headline,
				},
			}})
		case entity.HeroTypeFeaturedArchive:
			hei = append(hei, entity.HeroEntityInsert{Type: e.Type, FeaturedArchive: entity.HeroFeaturedArchiveInsert{
				ArchiveId:   e.FeaturedArchive.Archive.Id,
				Tag:         e.FeaturedArchive.Tag,
				Headline:    e.FeaturedArchive.Headline,
				ExploreText: e.FeaturedArchive.ExploreText,
			}})
		}
	}

	err = hs.SetHero(ctx, hei)
	if err != nil {
		return fmt.Errorf("failed to set hero: %w", err)
	}

	return nil
}

func (hs *heroStore) SetHero(ctx context.Context, heroInsert []entity.HeroEntityInsert) error {
	return hs.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Delete existing hero data
		err := deleteExistingHeroData(ctx, rep)
		if err != nil {
			return fmt.Errorf("failed to delete hero data: %w", err)
		}

		entities, err := buildHeroData(ctx, rep, heroInsert)
		if err != nil {
			return fmt.Errorf("failed to build hero data: %w", err)
		}

		if err := insertHeroData(ctx, rep, entities); err != nil {
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
			return &entity.HeroFull{}, nil
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

func buildHeroData(ctx context.Context, rep dependency.Repository, heroInserts []entity.HeroEntityInsert) ([]entity.HeroEntity, error) {

	entities := make([]entity.HeroEntity, 0, len(heroInserts))
	for n, e := range heroInserts {
		switch e.Type {
		case entity.HeroTypeSingle:
			media, err := rep.Media().GetMediaById(ctx, e.Single.MediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Single.MediaId))
				continue
			}
			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Single: &entity.HeroSingle{
					Media:       *media,
					Headline:    e.Single.Headline,
					ExploreLink: e.Single.ExploreLink,
					ExploreText: e.Single.ExploreText,
				},
			})
		case entity.HeroTypeDouble:
			leftMedia, err := rep.Media().GetMediaById(ctx, e.Double.Left.MediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Double.Left.MediaId))
				continue
			}
			rightMedia, err := rep.Media().GetMediaById(ctx, e.Double.Right.MediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Double.Right.MediaId))
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Double: &entity.HeroDouble{
					Left: entity.HeroSingle{
						Media:       *leftMedia,
						ExploreLink: e.Double.Left.ExploreLink,
						ExploreText: e.Double.Left.ExploreText,
						Headline:    e.Double.Left.Headline,
					},
					Right: entity.HeroSingle{
						Media:       *rightMedia,
						ExploreLink: e.Double.Right.ExploreLink,
						ExploreText: e.Double.Right.ExploreText,
						Headline:    e.Double.Right.Headline,
					},
				},
			})
		case entity.HeroTypeMain:
			// main add should be only on first position
			if n != 0 {
				continue
			}
			media, err := rep.Media().GetMediaById(ctx, e.Main.Single.MediaId)
			if err != nil {
				slog.Error("failed to get media by id",
					slog.String("err", err.Error()),
					slog.Int("media_id", e.Main.Single.MediaId))
				continue
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				Main: &entity.HeroMain{
					Single: entity.HeroSingle{
						Media:       *media,
						ExploreLink: e.Main.Single.ExploreLink,
						ExploreText: e.Main.Single.ExploreText,
						Headline:    e.Main.Single.Headline,
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
				continue
			}

			archive, err := rep.Archive().GetArchiveById(ctx, e.FeaturedArchive.ArchiveId)
			if err != nil {
				slog.Error("failed to get archive by id",
					slog.String("err", err.Error()),
					slog.Int("archive_id", e.FeaturedArchive.ArchiveId))
				continue
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

	return entities, nil
}

func insertHeroData(ctx context.Context, rep dependency.Repository, entities []entity.HeroEntity) error {
	jsonData, err := json.Marshal(entity.HeroFull{Entities: entities})
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
