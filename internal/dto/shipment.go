package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func ConvertPbShipmentToEntityShipment(pbShipment *pb_common.Shipment) *entity.Shipment {
	var entShipment entity.Shipment

	entShipment.ID = int(pbShipment.GetId())
	entShipment.CreatedAt = pbShipment.GetCreatedAt().AsTime()
	entShipment.UpdatedAt = pbShipment.GetUpdatedAt().AsTime()
	entShipment.CarrierID = int(pbShipment.GetCarrierId())

	// Handling nullable fields (TrackingCode, ShippingDate, EstimatedArrivalDate)
	if pbShipment.TrackingCode != "" {
		entShipment.TrackingCode = sql.NullString{String: pbShipment.TrackingCode, Valid: true}
	}

	if pbShipment.ShippingDate != nil {
		entShipment.ShippingDate = sql.NullTime{Time: pbShipment.GetShippingDate().AsTime(), Valid: true}
	}

	if pbShipment.EstimatedArrivalDate != nil {
		entShipment.EstimatedArrivalDate = sql.NullTime{Time: pbShipment.GetEstimatedArrivalDate().AsTime(), Valid: true}
	}

	return &entShipment
}
