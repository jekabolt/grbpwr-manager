package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
		return nil, fmt.Errorf("failed to unmarshal hero data: %w", err)
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
		case entity.HeroTypeFeaturedProducts:
			if len(e.FeaturedProducts.ProductIDs) == 0 {
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, e.FeaturedProducts.ProductIDs)
			if err != nil {
				return nil, fmt.Errorf("failed to get products by ids: %w", err)
			}			
			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				FeaturedProducts: &entity.HeroFeaturedProducts{
					Products:    products,
					Title:       e.FeaturedProducts.Title,
					ExploreText: e.FeaturedProducts.ExploreText,
					ExploreLink: e.FeaturedProducts.ExploreLink,
				},
			})
		case entity.HeroTypeMainAdd:
			// main add should be only on first position
			if n != 0 {
				continue
			}
			media, err := rep.Media().GetMediaById(ctx, e.MainAdd.SingleAdd.MediaId)
			if err != nil {
				return nil, fmt.Errorf("failed to get media by id: %w", err)
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				MainAdd: &entity.HeroMainAdd{
					SingleAdd: entity.HeroSingleAdd{
						Media:       *media,
						ExploreLink: e.MainAdd.SingleAdd.ExploreLink,
						ExploreText: e.MainAdd.SingleAdd.ExploreText,
					},
				},
			})
		case entity.HeroTypeSingleAdd:
			media, err := rep.Media().GetMediaById(ctx, e.SingleAdd.MediaId)
			if err != nil {
				return nil, fmt.Errorf("failed to get media by id: %w", err)
			}
			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				SingleAdd: &entity.HeroSingleAdd{
					Media:       *media,
					ExploreLink: e.SingleAdd.ExploreLink,
					ExploreText: e.SingleAdd.ExploreText,
				},
			})
		case entity.HeroTypeDoubleAdd:
			leftMedia, err := rep.Media().GetMediaById(ctx, e.DoubleAdd.Left.MediaId)
			if err != nil {
				return nil, fmt.Errorf("failed to get media by id: %w", err)
			}
			rightMedia, err := rep.Media().GetMediaById(ctx, e.DoubleAdd.Right.MediaId)
			if err != nil {
				return nil, fmt.Errorf("failed to get media by id: %w", err)
			}

			entities = append(entities, entity.HeroEntity{
				Type: e.Type,
				DoubleAdd: &entity.HeroDoubleAdd{
					Left: entity.HeroSingleAdd{
						Media:       *leftMedia,
						ExploreLink: e.DoubleAdd.Left.ExploreLink,
						ExploreText: e.DoubleAdd.Left.ExploreText,
					},
					Right: entity.HeroSingleAdd{
						Media:       *rightMedia,
						ExploreLink: e.DoubleAdd.Right.ExploreLink,
						ExploreText: e.DoubleAdd.Right.ExploreText,
					},
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
