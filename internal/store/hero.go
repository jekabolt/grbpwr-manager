package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
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

func (ms *MYSQLStore) SetHero(ctx context.Context, content, contentType,
	exploreLink, exploreText string) error {
	query := `INSERT INTO hero 
		(time_changed, content_link, content_type, explore_link, explore_text)
		VALUES 
		(NOW(), ?, ?, ?, ?)`
	_, err := ms.DB().ExecContext(ctx, query, content, contentType, exploreLink, exploreText)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MYSQLStore) GetHero(ctx context.Context) (*dto.Hero, error) {
	query := `SELECT 
	time_changed, 
	content_link,
	content_type, 
	explore_link, 
	explore_text 
	FROM hero ORDER BY time_changed DESC LIMIT 1`

	row := ms.DB().QueryRowContext(ctx, query)

	hero := dto.Hero{}
	err := row.Scan(&hero.TimeChanged, &hero.ContentLink, &hero.ContentType, &hero.ExploreLink, &hero.ExploreText)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("hero not found")
		}
		return nil, err
	}
	return &hero, nil
}
