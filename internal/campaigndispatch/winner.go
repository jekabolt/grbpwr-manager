package campaigndispatch

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func compareEngagementRate(
	leftNumerator, leftDenominator, rightNumerator, rightDenominator int64,
) int {
	if leftDenominator <= 0 {
		leftNumerator, leftDenominator = 0, 1
	}
	if rightDenominator <= 0 {
		rightNumerator, rightDenominator = 0, 1
	}
	return new(big.Rat).
		SetFrac64(leftNumerator, leftDenominator).
		Cmp(new(big.Rat).SetFrac64(rightNumerator, rightDenominator))
}

func compareABVariant(
	dimension entity.ABDimension,
	left, right entity.EmailCampaignABVariantResult,
) int {
	var primary, secondary int
	switch dimension {
	case entity.ABDimensionSubject:
		primary = compareEngagementRate(
			left.UniqueOpened, left.Delivered,
			right.UniqueOpened, right.Delivered,
		)
		secondary = compareEngagementRate(
			left.UniqueClicked, left.Delivered,
			right.UniqueClicked, right.Delivered,
		)
	case entity.ABDimensionContent:
		primary = compareEngagementRate(
			left.UniqueClicked, left.Delivered,
			right.UniqueClicked, right.Delivered,
		)
		secondary = compareEngagementRate(
			left.UniqueOpened, left.Delivered,
			right.UniqueOpened, right.Delivered,
		)
	}
	if primary != 0 {
		return primary
	}
	if secondary != 0 {
		return secondary
	}
	switch {
	case left.VariantID < right.VariantID:
		return 1
	case left.VariantID > right.VariantID:
		return -1
	default:
		return 0
	}
}

// SelectABWinner always returns a deterministic winner. Subject tests rank
// unique-open rate first; content tests rank unique-click rate first. The other
// rate breaks ties and the earliest variant id breaks the final tie. Therefore
// a campaign with zero deliveries/engagement still resolves instead of hanging
// forever in sending.
func SelectABWinner(
	dimension entity.ABDimension,
	variants []entity.EmailCampaignABVariantResult,
) (int, error) {
	if dimension != entity.ABDimensionSubject && dimension != entity.ABDimensionContent {
		return 0, fmt.Errorf("unsupported A/B dimension %q", dimension)
	}
	if len(variants) == 0 {
		return 0, errors.New("A/B campaign has no variants")
	}
	winner := variants[0]
	for i := range variants {
		candidate := variants[i]
		if candidate.VariantID <= 0 ||
			candidate.Delivered < 0 ||
			candidate.UniqueOpened < 0 ||
			candidate.UniqueClicked < 0 {
			return 0, fmt.Errorf("invalid A/B result for variant %d", candidate.VariantID)
		}
		if compareABVariant(dimension, candidate, winner) > 0 {
			winner = candidate
		}
	}
	return winner.VariantID, nil
}
