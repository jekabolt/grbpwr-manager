package entity

import (
	"math"
	"testing"
)

func TestCampaignMetricCountsRates(t *testing.T) {
	counts := CampaignMetricCounts{
		Sent:          100,
		Delivered:     80,
		UniqueOpened:  40,
		UniqueClicked: 10,
		Bounced:       5,
		Complained:    2,
	}
	got := counts.Rates()
	want := CampaignMetricRates{
		DeliveryRate:    0.8,
		OpenRate:        0.5,
		ClickRate:       0.125,
		ClickToOpenRate: 0.25,
		BounceRate:      0.05,
		ComplaintRate:   0.02,
	}
	if math.Abs(got.DeliveryRate-want.DeliveryRate) > 1e-12 ||
		math.Abs(got.OpenRate-want.OpenRate) > 1e-12 ||
		math.Abs(got.ClickRate-want.ClickRate) > 1e-12 ||
		math.Abs(got.ClickToOpenRate-want.ClickToOpenRate) > 1e-12 ||
		math.Abs(got.BounceRate-want.BounceRate) > 1e-12 ||
		math.Abs(got.ComplaintRate-want.ComplaintRate) > 1e-12 {
		t.Fatalf("Rates() = %#v, want %#v", got, want)
	}
}

func TestCampaignMetricCountsRatesZeroDenominators(t *testing.T) {
	if got := (CampaignMetricCounts{}).Rates(); got != (CampaignMetricRates{}) {
		t.Fatalf("zero-denominator Rates() = %#v, want zero value", got)
	}
}
