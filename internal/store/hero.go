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

// Hero returns an object implementing hero interface
func (ms *MYSQLStore) Hero() dependency.Hero {
	return &heroStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) SetHero(ctx context.Context, main *entity.HeroInsert, ads []entity.HeroInsert, productIds []int) error {

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `DELETE FROM hero`
		_, err := rep.DB().ExecContext(ctx, query)
		if err != nil {
			return fmt.Errorf("failed to delete hero: %w", err)
		}

		query = `DELETE FROM hero_product`
		_, err = rep.DB().ExecContext(ctx, query)
		if err != nil {
			return fmt.Errorf("failed to delete hero products: %w", err)
		}

		ms.cache.DeleteHero()

		err = insertHeroProducts(ctx, rep, productIds)
		if err != nil {
			return fmt.Errorf("failed to add hero products: %w", err)
		}

		err = insertHero(ctx, rep, main, ads)
		if err != nil {
			return fmt.Errorf("failed to add hero ads: %w", err)
		}

		hero, err := rep.Hero().GetHero(ctx)
		if err != nil {
			return err
		}

		ms.cache.UpdateHero(&entity.HeroFull{
			Main:             hero.Main,
			Ads:              hero.Ads,
			ProductsFeatured: hero.ProductsFeatured,
		})

		return nil

	})
	if err != nil {
		return fmt.Errorf("failed to add hero: %w", err)
	}

	return nil
}

func insertHeroProducts(ctx context.Context, rep dependency.Repository, productIds []int) error {
	rows := make([]map[string]any, 0, len(productIds))
	for i, productId := range productIds {
		row := map[string]any{
			"product_id":      productId,
			"sequence_number": i,
		}
		rows = append(rows, row)
	}

	return BulkInsert(ctx, rep.DB(), "hero_product", rows)
}

func insertHero(ctx context.Context, rep dependency.Repository, main *entity.HeroInsert, ads []entity.HeroInsert) error {
	rows := make([]map[string]any, 0, len(ads))
	for _, ad := range ads {
		row := map[string]any{
			"media_id":     ad.MediaId,
			"explore_link": ad.ExploreLink,
			"explore_text": ad.ExploreText,
			"main":         false,
		}
		rows = append(rows, row)
	}
	rows = append(rows, map[string]any{
		"media_id":     main.MediaId,
		"explore_link": main.ExploreLink,
		"explore_text": main.ExploreText,
		"main":         true,
	})

	return BulkInsert(ctx, rep.DB(), "hero", rows)
}

type heroRaw struct {
	Id               int       `db:"id"`
	CreatedAt        time.Time `db:"created_at"`
	ExploreLink      string    `db:"explore_link"`
	ExploreText      string    `db:"explore_text"`
	IsMain           bool      `db:"main"`
	MediaId          int       `db:"media_id"`
	MediaCreatedAt   time.Time `db:"media_created_at"`
	FullSize         string    `db:"full_size"`
	FullSizeWidth    int       `db:"full_size_width"`
	FullSizeHeight   int       `db:"full_size_height"`
	Thumbnail        string    `db:"thumbnail"`
	ThumbnailWidth   int       `db:"thumbnail_width"`
	ThumbnailHeight  int       `db:"thumbnail_height"`
	Compressed       string    `db:"compressed"`
	CompressedWidth  int       `db:"compressed_width"`
	CompressedHeight int       `db:"compressed_height"`
}

func (ms *MYSQLStore) GetHero(ctx context.Context) (*entity.HeroFull, error) {
	hf := ms.cache.GetHero()
	if hf != nil {
		// early return if hero is cached
		return hf, nil
	}

	query := `
		SELECT
			h.id,
			h.created_at,
			h.explore_link,
			h.explore_text,
			h.main,
			m.id as media_id,
			m.created_at as media_created_at,
			m.full_size,
			m.full_size_width,
			m.full_size_height,
			m.thumbnail,
			m.thumbnail_width,
			m.thumbnail_height,
			m.compressed,
			m.compressed_width,
			m.compressed_height 
		FROM hero h 
		INNER JOIN media m ON h.media_id = m.id
	`

	heroList, err := QueryListNamed[heroRaw](ctx, ms.db, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero: %w", err)
	}

	if len(heroList) == 0 {
		return nil, sql.ErrNoRows
	}

	hf = &entity.HeroFull{}

	for _, h := range heroList {
		media := &entity.MediaFull{
			Id:        h.MediaId,
			CreatedAt: h.MediaCreatedAt,
			MediaItem: entity.MediaItem{
				FullSizeMediaURL:   h.FullSize,
				FullSizeWidth:      h.FullSizeWidth,
				FullSizeHeight:     h.FullSizeHeight,
				ThumbnailMediaURL:  h.Thumbnail,
				ThumbnailWidth:     h.ThumbnailWidth,
				ThumbnailHeight:    h.ThumbnailHeight,
				CompressedMediaURL: h.Compressed,
				CompressedWidth:    h.CompressedWidth,
				CompressedHeight:   h.CompressedHeight,
			},
		}

		hi := &entity.HeroItem{
			Media:       media,
			ExploreLink: h.ExploreLink,
			ExploreText: h.ExploreText,
			IsMain:      h.IsMain,
		}

		if h.IsMain {
			hf.Main = hi
		} else {
			hf.Ads = append(hf.Ads, *hi)
		}
	}

	hf.ProductsFeatured, err = getProductsByHeroId(ctx, ms)
	if err != nil {
		return nil, err
	}

	ms.cache.UpdateHero(hf)

	return hf, nil
}

func getProductsByHeroId(ctx context.Context, rep dependency.Repository) ([]entity.Product, error) {
	// Query to get the associated products
	query := `
		SELECT p.*
		FROM product AS p
		INNER JOIN hero_product AS hp ON p.id = hp.product_id`

	prds, err := QueryListNamed[entity.Product](ctx, rep.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero products: %w", err)
	}
	return prds, nil
}
