package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type heroStore struct {
	*MYSQLStore
}

// ParticipateStore returns an object implementing participate interface
func (ms *MYSQLStore) Hero() dependency.Hero {
	return &heroStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) SetHero(ctx context.Context, hero entity.HeroInsert, productIds []int) error {

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `
		INSERT INTO 
		hero (content_link, content_type, explore_link, explore_text) 
		VALUES (:contentLink, :contentType, :exploreLink, :exploreText)`

		heroId, err := ExecNamedLastId(ctx, ms.db, query, map[string]any{
			"contentLink": hero.ContentLink,
			"contentType": hero.ContentType,
			"exploreLink": hero.ExploreLink,
			"exploreText": hero.ExploreText,
		})
		if err != nil {
			return fmt.Errorf("failed to add hero: %w", err)
		}

		query = `
		INSERT INTO 
		hero_product (hero_id, product_id, sequence_number) 
		VALUES (:heroId, :productId, :sequence)`
		for i, productId := range productIds {
			err = ExecNamed(ctx, ms.db, query, map[string]any{
				"heroId":    heroId,
				"productId": productId,
				"sequence":  i,
			})
			if err != nil {
				return fmt.Errorf("failed to add hero product: %w", err)
			}
		}

		return nil

	})
	if err != nil {
		return fmt.Errorf("failed to add hero: %w", err)
	}

	return nil
}

func (ms *MYSQLStore) GetHero(ctx context.Context) (*entity.HeroFull, error) {
	// Query to get the latest hero entry
	query := `
	SELECT id, created_at, content_link, content_type, explore_link, explore_text
	FROM hero
	ORDER BY id DESC	
	LIMIT 1`

	hero, err := QueryNamedOne[entity.HeroFull](ctx, ms.db, query, map[string]any{})

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no hero found: %w", err)
		}
		return nil, fmt.Errorf("failed to query hero: %w", err)
	}

	// Query to get the associated products
	productQuery := `
	SELECT p.*
	FROM product AS p
	INNER JOIN hero_product AS hp ON p.id = hp.product_id
	WHERE hp.hero_id = :heroId`

	products, err := QueryListNamed[entity.Product](ctx, ms.db, productQuery, map[string]any{
		"heroId": hero.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero products: %w", err)
	}

	hero.ProductsFeatured = products

	return &hero, nil
}
