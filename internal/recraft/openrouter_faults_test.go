package recraft

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/stretchr/testify/require"
)

// TestTheDefaultRouteNamesARejectedKeyAndAnEmptyBalance.
//
// Both faults are SETTINGS faults: retrying changes nothing, and each names its own remedy — re-key
// or top up. Before this test existed they fell through translateORError's default branch into
// ErrProviderFailure, which the worker reads as weather, so a revoked key burned four retries over
// eight minutes and was filed as «the provider is unavailable».
//
// The assertions are two-sided on purpose. Asserting only «Is(ErrUnauthorized)» would still pass on
// a mapping that wrapped BOTH sentinels at once; asserting «not ErrProviderFailure» as well pins
// that the fault stopped being weather, which is the half the worker actually branches on.
func TestTheDefaultRouteNamesARejectedKeyAndAnEmptyBalance(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{"a rejected key", fmt.Errorf("%w: API error (HTTP 401)", orimages.ErrUnauthorized), ErrUnauthorized},
		{"an empty balance", fmt.Errorf("%w: API error (HTTP 402)", orimages.ErrOutOfCredit), ErrInsufficientCredits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := translateORError(tc.in)
			require.ErrorIs(t, got, tc.want,
				"the default vector route must name this fault, not file it as weather")
			require.NotErrorIs(t, got, ErrProviderFailure,
				"a settings fault classified as a provider failure is retried five times against something that will never work")
		})
	}
}

// TestTheDefaultRouteStillCallsWeatherWeather is the negative control: without it the test above is
// satisfied by a mapping that names EVERY fault, which would be the opposite defect.
func TestTheDefaultRouteStillCallsWeatherWeather(t *testing.T) {
	got := translateORError(fmt.Errorf("%w: API error (HTTP 503)", orimages.ErrProviderFailure))
	require.ErrorIs(t, got, ErrProviderFailure)
	require.False(t, errors.Is(got, ErrUnauthorized), "a 5xx is not a key problem")
	require.False(t, errors.Is(got, ErrInsufficientCredits), "a 5xx is not a balance problem")
}
