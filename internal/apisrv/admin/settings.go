package admin

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UpdateSettings updates settings
func (s *Server) UpdateSettings(ctx context.Context, req *pb_admin.UpdateSettingsRequest) (*pb_admin.UpdateSettingsResponse, error) {
	for _, sc := range req.ShipmentCarriers {
		err := s.repo.Settings().SetShipmentCarrierAllowance(ctx, sc.Carrier, sc.Allow)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set shipment carrier allowance",
				slog.String("err", err.Error()),
			)
			continue
		}

		// Use prices map
		prices := make(map[string]decimal.Decimal)
		if len(sc.Prices) > 0 {
			// Use the prices map
			for currency, pbPrice := range sc.Prices {
				price, err := decimal.NewFromString(pbPrice.Value)
				if err != nil {
					slog.Default().ErrorContext(ctx, "can't convert string to decimal",
						slog.String("currency", currency),
						slog.String("err", err.Error()),
					)
					continue
				}
				prices[currency] = dto.RoundForCurrency(price, currency)
			}
		}

		if len(prices) > 0 {
			err = s.repo.Settings().SetShipmentCarrierPrices(ctx, sc.Carrier, prices)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't set shipment carrier prices",
					slog.String("err", err.Error()),
				)
				continue
			}
		}
	}

	for _, pm := range req.PaymentMethods {
		pme := dto.ConvertPbPaymentMethodToEntity(pm.PaymentMethod)
		err := s.repo.Settings().SetPaymentMethodAllowance(ctx, pme, pm.Allow)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set payment method allowance",
				slog.String("err", err.Error()),
			)
			continue
		}
	}

	err := s.repo.Settings().SetSiteAvailability(ctx, req.SiteAvailable)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set site availability",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set site availability: %v", err)
	}

	err = s.repo.Settings().SetMaxOrderItems(ctx, int(req.MaxOrderItems))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set max order items",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set max order items: %v", err)
	}

	err = s.repo.Settings().SetBigMenu(ctx, req.BigMenu)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set big menu",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set big menu: %v", err)
	}

	// Convert protobuf announce to entity format
	var announceLink string
	var announceTranslations []entity.AnnounceTranslation
	if req.Announce != nil {
		announceLink = req.Announce.Link
		for _, pbTranslation := range req.Announce.Translations {
			announceTranslations = append(announceTranslations, entity.AnnounceTranslation{
				LanguageId: int(pbTranslation.LanguageId),
				Text:       pbTranslation.Text,
			})
		}
	}

	err = s.repo.Settings().SetAnnounce(ctx, announceLink, announceTranslations)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set announce",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set announce: %v", err)
	}

	err = s.repo.Settings().SetOrderExpirationSeconds(ctx, int(req.OrderExpirationSeconds))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set order expiration seconds",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set order expiration seconds: %v", err)
	}

	err = s.repo.Settings().SetPaymentIsProd(ctx, req.IsProd)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set payment is prod",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set payment mode: %v", err)
	}

	if len(req.ComplimentaryShippingPrices) > 0 {
		prices := make(map[string]decimal.Decimal)
		for currency, pbPrice := range req.ComplimentaryShippingPrices {
			price, err := decimal.NewFromString(pbPrice.Value)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't convert string to decimal for complimentary shipping",
					slog.String("currency", currency),
					slog.String("err", err.Error()),
				)
				continue
			}
			prices[currency] = dto.RoundForCurrency(price, currency)
		}

		if len(prices) > 0 {
			err = s.repo.Settings().SetComplimentaryShippingPrices(ctx, prices)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't set complimentary shipping prices",
					slog.String("err", err.Error()),
				)
				return nil, status.Errorf(codes.Internal, "failed to set complimentary shipping prices: %v", err)
			}
		}
	}

	s.revalidateAsync(&dto.RevalidationData{
		Hero: true,
	})
	return &pb_admin.UpdateSettingsResponse{}, nil
}

const maxBackgroundHeroColorRunes = 128

// GetBackgroundHeroColor returns the persisted hero background CSS color.
func (s *Server) GetBackgroundHeroColor(ctx context.Context, _ *pb_admin.GetBackgroundHeroColorRequest) (*pb_admin.GetBackgroundHeroColorResponse, error) {
	color, err := s.repo.Settings().GetBackgroundHeroColor(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get background hero color",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get background hero color: %v", err)
	}
	return &pb_admin.GetBackgroundHeroColorResponse{Color: color}, nil
}

// SetBackgroundHeroColor updates the hero background color and revalidates the storefront hero cache.
func (s *Server) SetBackgroundHeroColor(ctx context.Context, req *pb_admin.SetBackgroundHeroColorRequest) (*pb_admin.SetBackgroundHeroColorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if utf8.RuneCountInString(req.Color) > maxBackgroundHeroColorRunes {
		return nil, status.Errorf(codes.InvalidArgument, "color must be at most %d characters", maxBackgroundHeroColorRunes)
	}

	err := s.repo.Settings().SetBackgroundHeroColor(ctx, req.Color)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set background hero color",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to set background hero color: %v", err)
	}

	cache.SetBackgroundHeroColor(req.Color)

	s.revalidateAsync(&dto.RevalidationData{
		Hero: true,
	})

	return &pb_admin.SetBackgroundHeroColorResponse{}, nil
}

// UpsertPaymentMethodFees sets the estimated processing-fee model (percent + fixed) per
// payment method. These fees estimate the processing cost of orders that lack a captured
// Stripe fee (bank-invoice, cash, non-EUR-settled, pre-feature) so contribution margin is
// not systematically overstated for them. Fees default to 0, so nothing changes until set.
func (s *Server) UpsertPaymentMethodFees(ctx context.Context, req *pb_admin.UpsertPaymentMethodFeesRequest) (*pb_admin.UpsertPaymentMethodFeesResponse, error) {
	if req == nil || len(req.Fees) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one payment-method fee is required")
	}
	for _, f := range req.Fees {
		pm, ok := dto.ConvertPbToEntityPaymentMethod(f.PaymentMethod)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown payment method %v", f.PaymentMethod)
		}
		feePct, err := parseNonNegativeDecimal(f.FeePct)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "fee_pct for %s: %v", pm, err)
		}
		if feePct.GreaterThan(decimal.NewFromInt(100)) {
			return nil, status.Errorf(codes.InvalidArgument, "fee_pct for %s must be between 0 and 100", pm)
		}
		feeFixed, err := parseNonNegativeDecimal(f.FeeFixed)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "fee_fixed for %s: %v", pm, err)
		}
		if err := s.repo.Settings().SetPaymentMethodFees(ctx, pm, feePct, feeFixed); err != nil {
			slog.Default().ErrorContext(ctx, "can't set payment method fees",
				slog.String("payment_method", string(pm)),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't set payment method fees")
		}
	}
	return &pb_admin.UpsertPaymentMethodFeesResponse{}, nil
}

// parseNonNegativeDecimal reads a google.type.Decimal that must be present and >= 0. A nil
// message is treated as 0 (the field is optional and defaults to no fee).
func parseNonNegativeDecimal(d *pb_decimal.Decimal) (decimal.Decimal, error) {
	if d == nil || d.Value == "" {
		return decimal.Zero, nil
	}
	v, err := decimal.NewFromString(d.Value)
	if err != nil {
		return decimal.Zero, fmt.Errorf("invalid decimal %q", d.Value)
	}
	if v.IsNegative() {
		return decimal.Zero, fmt.Errorf("must not be negative")
	}
	return v, nil
}
