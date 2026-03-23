package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	decimalpb "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddShipmentCarrier(ctx context.Context, req *pb_admin.AddShipmentCarrierRequest) (*pb_admin.AddShipmentCarrierResponse, error) {
	if err := validateShipmentCarrierRequest(req.Carrier, req.TrackingUrl, req.Prices, req.AllowedRegions); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	carrier := dto.ConvertShipmentCarrierRequestToEntity(req.Carrier, req.TrackingUrl, req.Description, req.ExpectedDeliveryTime, req.Allowed)
	prices := parseShipmentCarrierPrices(req.Prices)
	allowedRegions := dto.ConvertPbShippingRegionsToEntity(req.AllowedRegions)

	id, err := s.repo.Settings().AddShipmentCarrier(ctx, &carrier, prices, allowedRegions)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add shipment carrier: %v", err)
	}
	return &pb_admin.AddShipmentCarrierResponse{Id: int32(id)}, nil
}

func (s *Server) UpdateShipmentCarrier(ctx context.Context, req *pb_admin.UpdateShipmentCarrierRequest) (*pb_admin.UpdateShipmentCarrierResponse, error) {
	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "id must be positive")
	}
	if err := validateShipmentCarrierRequest(req.Carrier, req.TrackingUrl, req.Prices, req.AllowedRegions); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	carrier := dto.ConvertShipmentCarrierRequestToEntity(req.Carrier, req.TrackingUrl, req.Description, req.ExpectedDeliveryTime, req.Allowed)
	prices := parseShipmentCarrierPrices(req.Prices)
	allowedRegions := dto.ConvertPbShippingRegionsToEntity(req.AllowedRegions)

	err := s.repo.Settings().UpdateShipmentCarrier(ctx, int(req.Id), &carrier, prices, allowedRegions)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipment carrier: %v", err)
	}
	return &pb_admin.UpdateShipmentCarrierResponse{}, nil
}

func (s *Server) DeleteShipmentCarrier(ctx context.Context, req *pb_admin.DeleteShipmentCarrierRequest) (*pb_admin.DeleteShipmentCarrierResponse, error) {
	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "id must be positive")
	}
	err := s.repo.Settings().DeleteShipmentCarrier(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete shipment carrier",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete shipment carrier: %v", err)
	}
	return &pb_admin.DeleteShipmentCarrierResponse{}, nil
}

var requiredCurrencies = []string{"EUR", "USD", "GBP", "JPY", "CNY", "KRW"}

func validateShipmentCarrierRequest(carrier, trackingURL string, prices map[string]*decimalpb.Decimal, allowedRegions []pb_common.ShippingRegion) error {
	if strings.TrimSpace(carrier) == "" {
		return fmt.Errorf("carrier name is required")
	}
	if trackingURL == "" {
		return fmt.Errorf("tracking_url is required")
	}
	if !strings.Contains(trackingURL, "%s") {
		return fmt.Errorf("tracking_url must contain %%s placeholder for tracking code")
	}
	provided := make(map[string]bool)
	for currency := range prices {
		provided[strings.ToUpper(currency)] = true
	}
	for _, c := range requiredCurrencies {
		if !provided[c] {
			return fmt.Errorf("missing required currency: %s", c)
		}
	}
	// Validate each price is non-negative (zero allowed for free shipping)
	for currency, pbPrice := range prices {
		if pbPrice == nil {
			continue
		}
		p, err := decimal.NewFromString(pbPrice.GetValue())
		if err != nil {
			return fmt.Errorf("invalid price for %s: %w", currency, err)
		}
		if p.IsNegative() {
			return fmt.Errorf("price for %s cannot be negative", strings.ToUpper(currency))
		}
	}
	for _, r := range allowedRegions {
		if r == pb_common.ShippingRegion_SHIPPING_REGION_UNKNOWN {
			return fmt.Errorf("invalid region: SHIPPING_REGION_UNKNOWN")
		}
	}
	return nil
}

func parseShipmentCarrierPrices(prices map[string]*decimalpb.Decimal) map[string]decimal.Decimal {
	if prices == nil {
		return nil
	}
	out := make(map[string]decimal.Decimal)
	for currency, pbPrice := range prices {
		if pbPrice == nil {
			continue
		}
		p, err := decimal.NewFromString(pbPrice.GetValue())
		if err != nil {
			slog.Warn("parseShipmentCarrierPrices: invalid decimal (should have been caught by validation)",
				slog.String("currency", currency),
				slog.String("value", pbPrice.GetValue()),
				slog.String("err", err.Error()),
			)
			continue
		}
		currencyUpper := strings.ToUpper(currency)
		out[currencyUpper] = dto.RoundForCurrency(p, currencyUpper)
	}
	return out
}
