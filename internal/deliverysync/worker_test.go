package deliverysync

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

// seedCarrier replaces the in-memory carrier cache with a single carrier under test.
func seedCarrier(id int, slug string, hours int) {
	cache.UpdateShipmentCarriers([]entity.ShipmentCarrier{{
		Id: id,
		ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
			AftershipSlug:         sql.NullString{String: slug, Valid: slug != ""},
			AutoDeliverAfterHours: hours,
		},
	}})
}

func newWorker(repo *mocks.MockRepository, tracker *mocks.MockTracker, mailer *mocks.MockMailer) *Worker {
	c := DefaultConfig()
	return &Worker{repo: repo, tracker: tracker, mailer: mailer, c: &c}
}

func shippedOrder(uuid string, carrierID int, tracking string, shippedAgo time.Duration) entity.ShipmentToAutoDeliver {
	return entity.ShipmentToAutoDeliver{
		OrderUUID:    uuid,
		CarrierId:    carrierID,
		TrackingCode: sql.NullString{String: tracking, Valid: tracking != ""},
		ShippingDate: time.Now().Add(-shippedAgo),
	}
}

func expectDeliveredEmail(order *mocks.MockOrder, mailer *mocks.MockMailer, uuid string) {
	order.EXPECT().GetOrderFullByUUID(mock.Anything, uuid).Return(&entity.OrderFull{
		Order: entity.Order{UUID: uuid, Currency: "EUR"},
		Buyer: entity.Buyer{BuyerInsert: entity.BuyerInsert{Email: "b@e.com", FirstName: "B"}},
	}, nil)
	mailer.EXPECT().SendOrderDelivered(mock.Anything, mock.Anything, "b@e.com", mock.Anything).Return(nil)
}

// Real AfterShip Delivered signal → transition attributed to aftership + delivered email.
func TestProcessOrderRealDelivered(t *testing.T) {
	seedCarrier(1, "dhl", 336)
	repo := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	tracker := mocks.NewMockTracker(t)
	mailer := mocks.NewMockMailer(t)

	tracker.EXPECT().GetTrackingStatus(mock.Anything, "dhl", "TN1").
		Return(entity.TrackingStatus{Found: true, Delivered: true, Tag: "Delivered"}, nil)
	repo.EXPECT().Order().Return(order)
	order.EXPECT().DeliverOrderWithSource(mock.Anything, "u1", "aftership", mock.Anything).Return(true, nil)
	expectDeliveredEmail(order, mailer, "u1")

	newWorker(repo, tracker, mailer).processOrder(context.Background(), shippedOrder("u1", 1, "TN1", time.Hour), time.Now())
}

// Tracking not yet registered → register it, deliver nothing (checked next tick).
func TestProcessOrderRegistersWhenNotFound(t *testing.T) {
	seedCarrier(1, "dhl", 336)
	repo := mocks.NewMockRepository(t) // Order() must never be called
	tracker := mocks.NewMockTracker(t)

	tracker.EXPECT().GetTrackingStatus(mock.Anything, "dhl", "TN1").
		Return(entity.TrackingStatus{Found: false}, nil)
	tracker.EXPECT().RegisterTracking(mock.Anything, "dhl", "TN1").Return(nil)

	newWorker(repo, tracker, mocks.NewMockMailer(t)).
		processOrder(context.Background(), shippedOrder("u1", 1, "TN1", time.Hour), time.Now())
}

// Tracked, not delivered, still within the window → do nothing.
func TestProcessOrderInTransitWithinWindow(t *testing.T) {
	seedCarrier(1, "dhl", 336) // 14 days
	repo := mocks.NewMockRepository(t)
	tracker := mocks.NewMockTracker(t)

	tracker.EXPECT().GetTrackingStatus(mock.Anything, "dhl", "TN1").
		Return(entity.TrackingStatus{Found: true, Delivered: false, Tag: "InTransit"}, nil)

	newWorker(repo, tracker, mocks.NewMockMailer(t)).
		processOrder(context.Background(), shippedOrder("u1", 1, "TN1", time.Hour), time.Now())
}

// Tracked, not delivered, past the window → silent timer delivery, NO email.
func TestProcessOrderTimerFiresForStuckTracking(t *testing.T) {
	seedCarrier(1, "dhl", 1) // 1-hour window
	repo := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	tracker := mocks.NewMockTracker(t)
	mailer := mocks.NewMockMailer(t) // SendOrderDelivered must never be called

	tracker.EXPECT().GetTrackingStatus(mock.Anything, "dhl", "TN1").
		Return(entity.TrackingStatus{Found: true, Delivered: false, Tag: "InTransit"}, nil)
	repo.EXPECT().Order().Return(order)
	order.EXPECT().DeliverOrderWithSource(mock.Anything, "u1", "system-timeout", mock.Anything).Return(true, nil)

	newWorker(repo, tracker, mailer).
		processOrder(context.Background(), shippedOrder("u1", 1, "TN1", 2*time.Hour), time.Now())
}

// Carrier with no AfterShip slug → never polled; timer-only delivery after the window, NO email.
func TestProcessOrderUntrackableCarrierTimerOnly(t *testing.T) {
	seedCarrier(2, "", 1) // no slug, 1-hour window
	repo := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	tracker := mocks.NewMockTracker(t) // GetTrackingStatus / RegisterTracking must never be called
	mailer := mocks.NewMockMailer(t)

	repo.EXPECT().Order().Return(order)
	order.EXPECT().DeliverOrderWithSource(mock.Anything, "u2", "system-timeout", mock.Anything).Return(true, nil)

	newWorker(repo, tracker, mailer).
		processOrder(context.Background(), shippedOrder("u2", 2, "TNX", 2*time.Hour), time.Now())
}
