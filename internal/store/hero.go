package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

func (ms *MYSQLStore) SetHero(ctx context.Context, main entity.HeroInsert, ads []entity.HeroInsert, productIds []int) error {

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `
		INSERT INTO 
		hero (content_link, content_type, explore_link, explore_text) 
		VALUES (:contentLink, :contentType, :exploreLink, :exploreText)`

		heroId, err := ExecNamedLastId(ctx, ms.db, query, map[string]any{
			"contentLink": main.ContentLink,
			"contentType": main.ContentType,
			"exploreLink": main.ExploreLink,
			"exploreText": main.ExploreText,
		})
		if err != nil {
			return fmt.Errorf("failed to add hero: %w", err)
		}

		err = insertHeroProducts(ctx, rep, productIds, heroId)
		if err != nil {
			return fmt.Errorf("failed to add hero products: %w", err)
		}

		err = insertHeroAds(ctx, rep, ads, heroId)
		if err != nil {
			return fmt.Errorf("failed to add hero ads: %w", err)
		}

		return nil

	})
	if err != nil {
		return fmt.Errorf("failed to add hero: %w", err)
	}

	return nil
}

func insertHeroProducts(ctx context.Context, rep dependency.Repository, productIds []int, heroId int) error {
	rows := make([]map[string]any, 0, len(productIds))
	for i, productId := range productIds {
		row := map[string]any{
			"hero_id":         heroId,
			"product_id":      productId,
			"sequence_number": i,
		}
		rows = append(rows, row)
	}

	return BulkInsert(ctx, rep.DB(), "hero_product", rows)
}

func insertHeroAds(ctx context.Context, rep dependency.Repository, ads []entity.HeroInsert, heroId int) error {
	rows := make([]map[string]any, 0, len(ads))
	for _, ad := range ads {
		row := map[string]any{
			"hero_id":      heroId,
			"content_link": ad.ContentLink,
			"content_type": ad.ContentType,
			"explore_link": ad.ExploreLink,
			"explore_text": ad.ExploreText,
		}
		rows = append(rows, row)
	}

	return BulkInsert(ctx, rep.DB(), "hero_ads", rows)
}

type heroRaw struct {
	Id          int       `db:"id"`
	CreatedAt   time.Time `db:"created_at"`
	ContentLink string    `db:"content_link"`
	ContentType string    `db:"content_type"`
	ExploreLink string    `db:"explore_link"`
	ExploreText string    `db:"explore_text"`
}

func (ms *MYSQLStore) GetHero(ctx context.Context) (*entity.HeroFull, error) {
	// Query to get the latest hero entry
	query := `
	SELECT id, created_at, content_link, content_type, explore_link, explore_text
	FROM hero
	ORDER BY id DESC	
	LIMIT 1`

	heroR, err := QueryNamedOne[heroRaw](ctx, ms.db, query, map[string]any{})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no hero found: %w", err)
		}
		return nil, fmt.Errorf("failed to query hero: %w", err)
	}
	hero := entity.HeroFull{
		Id:               heroR.Id,
		CreatedAt:        heroR.CreatedAt,
		Main:             entity.HeroInsert{ContentLink: heroR.ContentLink, ContentType: heroR.ContentType, ExploreLink: heroR.ExploreLink, ExploreText: heroR.ExploreText},
		Ads:              []entity.HeroInsert{},
		ProductsFeatured: []entity.Product{},
	}

	// Query to get the associated products
	query = `
	SELECT p.*
	FROM product AS p
	INNER JOIN hero_product AS hp ON p.id = hp.product_id
	WHERE hp.hero_id = :heroId`

	hero.ProductsFeatured, err = QueryListNamed[entity.Product](ctx, ms.db, query, map[string]any{
		"heroId": hero.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero products: %w", err)
	}

	// Query to get the associated products
	query = `SELECT content_link, content_type, explore_link, explore_text FROM hero_ads WHERE hero_id = :heroId`
	hero.Ads, err = QueryListNamed[entity.HeroInsert](ctx, ms.db, query, map[string]any{
		"heroId": hero.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero ads: %w", err)
	}

	return &hero, nil
}
