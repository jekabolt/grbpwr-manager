package techcard

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Recently touched tech cards per account (table tech_card_recent, 0485): the home «recent
// techcards» block. NOT A LEG OF UpdateTechCard: a touch is a note about who looked at the card,
// not card content, so it never joins a card write's transaction and never fails one.
//
// accountID is the tenant admins.id; the store cannot know who is asking, the handler passes it.

// TouchTechCardRecent stamps «account touched card now» and trims the account to the latest
// entity.RecentTechCardsKeep rows. Two statements, no transaction: a lost trim only leaves a few
// extra rows until the next touch, and the list never reads past its own limit.
// An unknown card or account fails the FK; callers treat every error as best-effort.
func (s *Store) TouchTechCardRecent(ctx context.Context, accountID, techCardID int) error {
	params := map[string]any{
		"account_id":   accountID,
		"tech_card_id": techCardID,
		"keep":         entity.RecentTechCardsKeep,
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO tech_card_recent (account_id, tech_card_id, touched_at)
		VALUES (:account_id, :tech_card_id, CURRENT_TIMESTAMP(6))
		ON DUPLICATE KEY UPDATE touched_at = VALUES(touched_at)`, params); err != nil {
		return fmt.Errorf("failed to touch tech card %d for account %d: %w", techCardID, accountID, err)
	}
	// The derived table is materialized (it has a LIMIT), so MySQL lets the DELETE read the table it
	// deletes from. Ties on touched_at break by card id, the same order the list uses.
	if err := storeutil.ExecNamed(ctx, s.DB, `
		DELETE r FROM tech_card_recent r
		LEFT JOIN (
			SELECT tech_card_id FROM tech_card_recent
			WHERE account_id = :account_id
			ORDER BY touched_at DESC, tech_card_id DESC
			LIMIT :keep
		) kept ON kept.tech_card_id = r.tech_card_id
		WHERE r.account_id = :account_id AND kept.tech_card_id IS NULL`, params); err != nil {
		return fmt.Errorf("failed to trim recent tech cards of account %d: %w", accountID, err)
	}
	return nil
}

// ListRecentTechCards returns the account's touched cards, latest first, as list headers (the same
// decoration as ListTechCards). Archived (obsolete) and system cards are skipped; a deleted card is
// gone with its row (FK cascade). limit goes through entity.ClampRecentTechCardsLimit.
func (s *Store) ListRecentTechCards(ctx context.Context, accountID, limit int) ([]entity.RecentTechCard, error) {
	type touch struct {
		TechCardID int       `db:"tech_card_id"`
		TouchedAt  time.Time `db:"touched_at"`
	}
	touches, err := storeutil.QueryListNamed[touch](ctx, s.DB, `
		SELECT r.tech_card_id, r.touched_at
		FROM tech_card_recent r
		JOIN tech_card tc ON tc.id = r.tech_card_id
		WHERE r.account_id = :account_id
			AND tc.approval_state <> :obsolete
			AND `+techCardNotSystemSQL+`
		ORDER BY r.touched_at DESC, r.tech_card_id DESC
		LIMIT :limit`,
		map[string]any{
			"account_id":                  accountID,
			"obsolete":                    string(entity.TechCardApprovalObsolete),
			techCardSystemKeyPatternParam: techCardSystemKeyPattern,
			"limit":                       entity.ClampRecentTechCardsLimit(limit),
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list recent tech cards of account %d: %w", accountID, err)
	}
	if len(touches) == 0 {
		return []entity.RecentTechCard{}, nil
	}

	ids := make([]int, len(touches))
	for i, t := range touches {
		ids[i] = t.TechCardID
	}
	rows, err := storeutil.QueryListNamed[entity.TechCard](ctx, s.DB,
		`SELECT * FROM tech_card WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to load recent tech cards of account %d: %w", accountID, err)
	}
	byID := make(map[int]entity.TechCard, len(rows))
	for _, c := range rows {
		byID[c.Id] = c
	}

	// Keep the touch order; a card deleted between the two reads is simply left out.
	cards := make([]entity.TechCard, 0, len(touches))
	at := make([]time.Time, 0, len(touches))
	for _, t := range touches {
		if c, ok := byID[t.TechCardID]; ok {
			cards = append(cards, c)
			at = append(at, t.TouchedAt)
		}
	}
	s.decorateListCards(ctx, cards)

	out := make([]entity.RecentTechCard, len(cards))
	for i := range cards {
		out[i] = entity.RecentTechCard{Card: cards[i], TouchedAt: at[i]}
	}
	return out, nil
}
