package entity

import "time"

// Recently touched tech cards per account (table tech_card_recent, 0485): the home «recent
// techcards» block.
const (
	// RecentTechCardsKeep is how many rows one account keeps; older ones are trimmed on each touch.
	RecentTechCardsKeep = 50
	// RecentTechCardsDefault is the list size when the request leaves limit at 0.
	RecentTechCardsDefault = 6
	// RecentTechCardsMax caps the requested list size.
	RecentTechCardsMax = 20
)

// RecentTechCard is one card an account opened or wrote, and when it last did.
type RecentTechCard struct {
	Card      TechCard
	TouchedAt time.Time
}

// ClampRecentTechCardsLimit maps a requested limit to the served one: 0 or less is the default,
// anything above the cap is the cap.
func ClampRecentTechCardsLimit(limit int) int {
	switch {
	case limit <= 0:
		return RecentTechCardsDefault
	case limit > RecentTechCardsMax:
		return RecentTechCardsMax
	}
	return limit
}
