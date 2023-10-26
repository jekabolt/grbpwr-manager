package store

// import (
// 	"context"
// 	"database/sql"
// 	"fmt"

// 	"github.com/jekabolt/grbpwr-manager/internal/dependency"
// 	"github.com/jekabolt/grbpwr-manager/internal/dto"
// )

// type heroStore struct {
// 	*MYSQLStore
// }

// // ParticipateStore returns an object implementing participate interface
// func (ms *MYSQLStore) Hero() dependency.Hero {
// 	return &heroStore{
// 		MYSQLStore: ms,
// 	}
// }

// func (ms *MYSQLStore) SetHero(ctx context.Context, left, right dto.HeroElement) error {
// 	query := `
//     INSERT INTO hero
//         (time_changed, content_link_left, content_type_left, explore_link_left, explore_text_left,
//          content_link_right, content_type_right, explore_link_right, explore_text_right)
//     VALUES
//         (NOW(), ?, ?, ?, ?, ?, ?, ?, ?)`

// 	_, err := ms.DB().ExecContext(ctx, query,
// 		left.ContentLink, left.ContentType, left.ExploreLink, left.ExploreText,
// 		right.ContentLink, right.ContentType, right.ExploreLink, right.ExploreText)

// 	if err != nil {
// 		return err
// 	}
// 	return nil
// }

// func (ms *MYSQLStore) GetHero(ctx context.Context) (*dto.Hero, error) {
// 	query := `
//     SELECT
//         time_changed,
//         content_link_left, content_type_left, explore_link_left, explore_text_left,
//         content_link_right, content_type_right, explore_link_right, explore_text_right
//     FROM hero ORDER BY time_changed DESC LIMIT 1`

// 	row := ms.DB().QueryRowContext(ctx, query)

// 	hero := dto.Hero{}
// 	err := row.Scan(
// 		&hero.TimeChanged,
// 		&hero.HeroLeft.ContentLink, &hero.HeroLeft.ContentType, &hero.HeroLeft.ExploreLink, &hero.HeroLeft.ExploreText,
// 		&hero.HeroRight.ContentLink, &hero.HeroRight.ContentType, &hero.HeroRight.ExploreLink, &hero.HeroRight.ExploreText)

// 	if err != nil {
// 		if err == sql.ErrNoRows {
// 			return nil, fmt.Errorf("hero not found")
// 		}
// 		return nil, err
// 	}
// 	return &hero, nil
// }
