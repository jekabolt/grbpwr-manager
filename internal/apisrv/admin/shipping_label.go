package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	decimalpb "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PrepareShippingLabel returns the default parcel (weight/box derived from the order's tech-card
// packaging, editable) plus whether label generation is available, so the UI can pre-fill the label
// form. It is read-only and never calls the carrier.
func (s *Server) PrepareShippingLabel(ctx context.Context, req *pb_admin.PrepareShippingLabelRequest) (*pb_admin.PrepareShippingLabelResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order for label prepare", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't prepare shipping label")
	}

	items, err := s.repo.Order().GetOrderParcelItems(ctx, orderFull.Order.Id)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order parcel items", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't prepare shipping label")
	}
	parcel, complete, missing := deriveParcel(items)

	resp := &pb_admin.PrepareShippingLabelResponse{
		Parcel:            toPbParcel(parcel),
		Complete:          complete,
		MissingProductIds: missing,
		AlreadyGenerated:  orderFull.Shipment.HasLabel(),
		LabelUrl:          orderFull.Shipment.LabelURL.String,
		TrackingCode:      orderFull.Shipment.TrackingCode.String,
		LabelsEnabled:     s.labelProvider.Enabled(),
	}
	if carrier, ok := cache.GetShipmentCarrierById(orderFull.Shipment.CarrierId); ok {
		resp.CarrierId = int32(carrier.Id)
		resp.CarrierName = carrier.Carrier
	}
	return resp, nil
}

// GenerateShippingLabel announces the order's parcel with Sendcloud (creating a carrier label +
// tracking number), persists the label, then performs the real shipped transition via the shared
// shipOrder path (tracking code + status + shipped email + packaging consumption). It is idempotent:
// a shipment that already has a carrier label returns that label without re-announcing.
// req.ShippingOptionCode is optional; empty lets Sendcloud shipping rules pick the carrier/contract.
func (s *Server) GenerateShippingLabel(ctx context.Context, req *pb_admin.GenerateShippingLabelRequest) (*pb_admin.GenerateShippingLabelResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	if req.Parcel == nil || req.Parcel.WeightGrams <= 0 {
		return nil, status.Error(codes.InvalidArgument, "parcel weight_grams must be positive")
	}

	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order for label generate", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't generate shipping label")
	}

	// Idempotency guard: a generated label must never be re-created (a second announce would
	// double-charge the carrier account). Return the existing label unchanged.
	if orderFull.Shipment.HasLabel() {
		return &pb_admin.GenerateShippingLabelResponse{
			TrackingCode:       orderFull.Shipment.TrackingCode.String,
			LabelUrl:           orderFull.Shipment.LabelURL.String,
			CarrierShipmentId:  orderFull.Shipment.CarrierShipmentID.String,
			ShippingOptionCode: orderFull.Shipment.LabelServiceType.String,
		}, nil
	}
	// A tracking code without a label means the order was already shipped manually; do not
	// generate a label on top of it (it would allocate a second tracking number).
	if orderFull.Shipment.TrackingCode.Valid && strings.TrimSpace(orderFull.Shipment.TrackingCode.String) != "" {
		return nil, status.Error(codes.FailedPrecondition, "order already shipped with a manual tracking number")
	}
	if !s.labelProvider.Enabled() {
		return nil, status.Error(codes.FailedPrecondition, "shipping labels are not configured")
	}
	if _, ok := cache.GetShipmentCarrierById(orderFull.Shipment.CarrierId); !ok {
		return nil, status.Error(codes.Internal, "can't resolve order shipment carrier")
	}

	shipTo, err := buildShipToAddress(orderFull)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateShipFrom(s.shipFrom); err != nil {
		slog.Default().ErrorContext(ctx, "ship-from address not configured", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}

	// International (cross-border, non-intra-EU) shipments need a customs declaration built from the
	// order lines' product customs data.
	var customs *entity.LabelCustoms
	if needsCustoms(s.shipFrom.CountryISO2, shipTo.CountryISO2) {
		items, err := s.repo.Order().GetOrderParcelItems(ctx, orderFull.Order.Id)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order items for customs", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't generate shipping label")
		}
		customs, err = buildCustoms(items, orderFull.Order.Currency)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
	}

	parcel := fromPbParcel(req.Parcel)
	result, err := s.labelProvider.CreateLabel(ctx, entity.LabelRequest{
		ShippingOptionCode: strings.TrimSpace(req.ShippingOptionCode),
		ShipFrom:           s.shipFrom,
		ShipTo:             shipTo,
		Parcel:             parcel,
		References:         []string{req.OrderUuid},
		Customs:            customs,
	})
	if err != nil {
		if errors.Is(err, entity.ErrLabelsDisabled) {
			return nil, status.Error(codes.FailedPrecondition, "shipping labels are not configured")
		}
		slog.Default().ErrorContext(ctx, "carrier label creation failed",
			slog.String("order_uuid", req.OrderUuid), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't generate shipping label")
	}

	// Sendcloud returns the label inline as base64; store the PDF in our bucket for durable
	// retrieval/printing. Best-effort: on failure the shipment still ships (label recoverable in the
	// Sendcloud panel), but we log loudly.
	labelURL := s.durableLabelURL(ctx, req.OrderUuid, result.LabelPDF)

	// Persist the label before the shipped transition, so the order is never marked shipped without a
	// recorded label + carrier_shipment_id (the idempotency / void handle).
	if err := s.repo.Order().SetShipmentLabel(ctx, req.OrderUuid, entity.ShipmentLabel{
		LabelURL:          labelURL,
		CarrierShipmentID: result.CarrierShipmentID,
		ServiceType:       result.ShippingOptionCode,
		ParcelWeightGrams: parcel.WeightGrams,
		ParcelDimensions:  formatParcelDimensions(parcel),
	}); err != nil {
		slog.Default().ErrorContext(ctx, "can't persist generated label (carrier label already created)",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("carrier_shipment_id", result.CarrierShipmentID),
			slog.String("tracking_code", result.TrackingNumber),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "label created but could not be saved; check order before retrying")
	}

	// Shared ship path: writes tracking_code, stamps shipping_date, transitions to Shipped, sends the
	// shipped email and consumes packaging. The delivery-sync worker then registers the tracking with
	// AfterShip and reconciles delivery.
	if err := s.shipOrder(ctx, req.OrderUuid, result.TrackingNumber); err != nil {
		slog.Default().ErrorContext(ctx, "label saved but shipped transition failed",
			slog.String("order_uuid", req.OrderUuid), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "label created but order could not be marked shipped")
	}

	return &pb_admin.GenerateShippingLabelResponse{
		TrackingCode:       result.TrackingNumber,
		LabelUrl:           labelURL,
		CarrierShipmentId:  result.CarrierShipmentID,
		ShippingOptionCode: result.ShippingOptionCode,
		CarrierName:        result.CarrierName,
	}, nil
}

// durableLabelURL stores the label PDF bytes in our bucket and returns the durable CDN url. It is
// best-effort: on any upload failure it logs and returns "" (the label is still recoverable from
// the Sendcloud panel), so a bucket hiccup never blocks the shipment.
func (s *Server) durableLabelURL(ctx context.Context, orderUUID string, pdf []byte) string {
	if len(pdf) == 0 {
		return ""
	}
	url, _, err := s.bucket.UploadLabelPDF(ctx, pdf, "label-"+orderUUID)
	if err != nil {
		slog.Default().WarnContext(ctx, "can't upload label to bucket; label recoverable in carrier panel",
			slog.String("order_uuid", orderUUID), slog.String("err", err.Error()))
		return ""
	}
	return url
}

// euISO2 is the EU customs-union member set (ISO alpha-2). Intra-EU cross-border shipments need no
// customs declaration, so a label between two EU countries omits customs.
var euISO2 = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true,
	"EE": true, "FI": true, "FR": true, "DE": true, "GR": true, "HU": true, "IE": true,
	"IT": true, "LV": true, "LT": true, "LU": true, "MT": true, "NL": true, "PL": true,
	"PT": true, "RO": true, "SK": true, "SI": true, "ES": true, "SE": true,
}

// needsCustoms reports whether a shipment from origin to destination (both ISO-2) requires a customs
// declaration: the countries differ and they are not both in the EU customs union.
func needsCustoms(fromISO2, toISO2 string) bool {
	if fromISO2 == "" || toISO2 == "" || fromISO2 == toISO2 {
		return false
	}
	if euISO2[fromISO2] && euISO2[toISO2] {
		return false
	}
	return true
}

// buildCustoms assembles the international customs declaration from the order lines. Each item's
// description falls back to its SKU, the declared value is the actual paid unit price (currency =
// order currency), and per-item weight/HS-code/origin come from the tech card / product customs
// data. Returns an error listing products missing HS code or origin, since a carrier rejects an
// international label without them.
func buildCustoms(items []entity.OrderItemParcel, currency string) (*entity.LabelCustoms, error) {
	var missing []int32
	out := &entity.LabelCustoms{Purpose: "merchandise"}
	for _, it := range items {
		// country_of_origin is the core product field (free-text manufacture country, e.g. a name or
		// code); resolve it to the ISO-2 Sendcloud requires. A missing HS code or an unresolvable
		// origin flags the product as lacking customs data.
		originISO2, originOK := entity.ResolveCountryISO2(it.CountryOfOrigin.String)
		if !it.HSCode.Valid || strings.TrimSpace(it.HSCode.String) == "" || !originOK {
			missing = append(missing, int32(it.ProductId))
			continue
		}
		qty := int(it.Quantity.IntPart())
		if qty <= 0 {
			qty = 1
		}
		desc := strings.TrimSpace(it.CustomsDescription.String)
		if desc == "" {
			desc = it.SKU
		}
		weightGrams := 0
		if it.WeightGrossGrams.Valid {
			weightGrams = int(it.WeightGrossGrams.Int32)
		}
		out.Items = append(out.Items, entity.LabelCustomsItem{
			Description:   desc,
			Quantity:      qty,
			PriceAmount:   it.ProductPriceWithSale,
			PriceCurrency: currency,
			WeightGrams:   weightGrams,
			HSCode:        strings.TrimSpace(it.HSCode.String),
			OriginISO2:    originISO2,
			SKU:           it.SKU,
		})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("international shipment requires customs data (HS code + resolvable country of origin) on products %v", missing)
	}
	return out, nil
}

// GetShippingOptions fetches the shipping options (carrier + service + quote) available for the
// order's parcel via Sendcloud, so an operator can pick one before generating a label. Read-only.
func (s *Server) GetShippingOptions(ctx context.Context, req *pb_admin.GetShippingOptionsRequest) (*pb_admin.GetShippingOptionsResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	if req.Parcel == nil || req.Parcel.WeightGrams <= 0 {
		return nil, status.Error(codes.InvalidArgument, "parcel weight_grams must be positive")
	}
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order for options", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get shipping options")
	}
	shipTo, err := buildShipToAddress(orderFull)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateShipFrom(s.shipFrom); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	options, err := s.labelProvider.GetShippingOptions(ctx, entity.OptionsRequest{
		ShipFrom: s.shipFrom,
		ShipTo:   shipTo,
		Parcel:   fromPbParcel(req.Parcel),
	})
	if err != nil {
		if errors.Is(err, entity.ErrLabelsDisabled) {
			return nil, status.Error(codes.FailedPrecondition, "shipping labels are not configured")
		}
		slog.Default().ErrorContext(ctx, "can't get shipping options", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get shipping options")
	}
	return &pb_admin.GetShippingOptionsResponse{Options: toPbOptions(options)}, nil
}

// VoidShippingLabel cancels a generated label with the carrier, then reverts the order
// Shipped -> Confirmed so it can be regenerated. Only a shipment that still has a carrier label can
// be voided.
func (s *Server) VoidShippingLabel(ctx context.Context, req *pb_admin.VoidShippingLabelRequest) (*pb_admin.VoidShippingLabelResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order for void", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't void shipping label")
	}
	if !orderFull.Shipment.HasLabel() {
		return nil, status.Error(codes.FailedPrecondition, "order has no generated label to void")
	}
	labelURL := orderFull.Shipment.LabelURL.String

	// Cancel with the carrier first; only clear local state if the carrier accepted the cancel.
	if err := s.labelProvider.VoidLabel(ctx, orderFull.Shipment.CarrierShipmentID.String); err != nil {
		if errors.Is(err, entity.ErrLabelsDisabled) {
			return nil, status.Error(codes.FailedPrecondition, "shipping labels are not configured")
		}
		slog.Default().ErrorContext(ctx, "carrier label cancel failed",
			slog.String("order_uuid", req.OrderUuid), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't cancel label with carrier")
	}
	if err := s.repo.Order().VoidShipmentLabel(ctx, req.OrderUuid); err != nil {
		slog.Default().ErrorContext(ctx, "label cancelled with carrier but local void failed",
			slog.String("order_uuid", req.OrderUuid), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "label cancelled but order state could not be reverted; check order")
	}
	// Best-effort remove the durable label PDF we uploaded (no-op for an empty url).
	if labelURL != "" {
		if err := s.bucket.DeleteObjects(ctx, labelURL); err != nil {
			slog.Default().WarnContext(ctx, "can't delete voided label from bucket",
				slog.String("order_uuid", req.OrderUuid), slog.String("err", err.Error()))
		}
	}
	return &pb_admin.VoidShippingLabelResponse{}, nil
}

// SchedulePickup books a carrier pickup for the day from the warehouse origin (Sendcloud's
// end-of-day handover equivalent — v3 has no generic manifest API). carrier_code is the Sendcloud
// carrier to collect; date is YYYY-MM-DD.
func (s *Server) SchedulePickup(ctx context.Context, req *pb_admin.SchedulePickupRequest) (*pb_admin.SchedulePickupResponse, error) {
	if strings.TrimSpace(req.CarrierCode) == "" {
		return nil, status.Error(codes.InvalidArgument, "carrier_code is required")
	}
	if strings.TrimSpace(req.Date) == "" {
		return nil, status.Error(codes.InvalidArgument, "date is required (YYYY-MM-DD)")
	}
	if err := validateShipFrom(s.shipFrom); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	quantity := int(req.Quantity)
	if quantity <= 0 {
		quantity = 1
	}
	res, err := s.labelProvider.SchedulePickup(ctx, entity.PickupRequest{
		Address:     s.shipFrom,
		CarrierCode: strings.TrimSpace(req.CarrierCode),
		Date:        strings.TrimSpace(req.Date),
		FromTime:    strings.TrimSpace(req.FromTime),
		ToTime:      strings.TrimSpace(req.ToTime),
		Quantity:    quantity,
	})
	if err != nil {
		if errors.Is(err, entity.ErrLabelsDisabled) {
			return nil, status.Error(codes.FailedPrecondition, "shipping labels are not configured")
		}
		slog.Default().ErrorContext(ctx, "can't schedule pickup", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't schedule pickup")
	}
	return &pb_admin.SchedulePickupResponse{
		PickupId:  res.PickupID,
		Confirmed: res.Confirmed,
		Message:   res.Message,
	}, nil
}

func toPbOptions(options []entity.ShippingOption) []*pb_admin.ShippingOption {
	out := make([]*pb_admin.ShippingOption, 0, len(options))
	for _, o := range options {
		out = append(out, &pb_admin.ShippingOption{
			Code:         o.Code,
			CarrierCode:  o.CarrierCode,
			CarrierName:  o.CarrierName,
			ProductName:  o.ProductName,
			TotalCharge:  &decimalpb.Decimal{Value: o.TotalCharge.String()},
			Currency:     o.Currency,
			TransitDays:  int32(o.TransitDays),
			DeliveryDate: o.DeliveryDate,
		})
	}
	return out
}

// deriveParcel computes a default parcel from the order lines' tech-card packaging: total gross
// weight in grams (weight_gross_grams × quantity) and the largest per-line box. complete is false
// (with the offending product ids) when any line lacks a packaging weight, so the UI requires a
// manual override before generating.
func deriveParcel(items []entity.OrderItemParcel) (entity.LabelParcel, bool, []int32) {
	var totalGrams int
	var missing []int32
	complete := true
	bestVol, bestL, bestW, bestH := 0, 0, 0, 0

	for _, it := range items {
		qty := int(it.Quantity.IntPart())
		if qty <= 0 {
			qty = 1
		}
		if it.WeightGrossGrams.Valid {
			totalGrams += int(it.WeightGrossGrams.Int32) * qty
		} else {
			complete = false
			missing = append(missing, int32(it.ProductId))
		}
		if it.BoxDimensions.Valid {
			if l, w, h, ok := parseBoxDimensions(it.BoxDimensions.String); ok {
				if v := l * w * h; v > bestVol {
					bestVol, bestL, bestW, bestH = v, l, w, h
				}
			}
		}
	}

	return entity.LabelParcel{
		WeightGrams: totalGrams,
		LengthCM:    bestL,
		WidthCM:     bestW,
		HeightCM:    bestH,
		BoxType:     "custom",
	}, complete, missing
}

// parseBoxDimensions extracts three integer centimetre values from a free-text box spec
// (tech_card_packaging.box_dimensions, e.g. "30×22×10 см" or "30 x 22 x 10"). Returns ok=false when
// fewer than three numbers are present, in which case the caller ships without dimensions (weight
// only) and the operator can override.
func parseBoxDimensions(s string) (l, w, h int, ok bool) {
	repl := strings.NewReplacer(
		"×", " ", "х", " ", "Х", " ", "x", " ", "X", " ", "*", " ", ",", ".",
	)
	nums := make([]int, 0, 3)
	for _, f := range strings.Fields(repl.Replace(s)) {
		if n := parseLeadingInt(f); n > 0 {
			nums = append(nums, n)
		}
	}
	if len(nums) < 3 {
		return 0, 0, 0, false
	}
	return nums[0], nums[1], nums[2], true
}

// parseLeadingInt reads the leading run of digits of a token as an int (0 when none), so decimal or
// unit-suffixed tokens like "30.5" or "10cm" yield 30 and 10.
func parseLeadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// buildShipToAddress builds the destination label address from an order, resolving the free-text
// country to ISO alpha-2 (Sendcloud). Returns an error when the country cannot be resolved, so the
// caller rejects the request before calling the carrier.
func buildShipToAddress(o *entity.OrderFull) (entity.LabelAddress, error) {
	iso2, ok := entity.ResolveCountryISO2(o.Shipping.Country)
	if !ok {
		return entity.LabelAddress{}, fmt.Errorf("cannot resolve destination country %q to an ISO-2 code", o.Shipping.Country)
	}
	name := strings.TrimSpace(o.Buyer.FirstName + " " + o.Buyer.LastName)
	return entity.LabelAddress{
		ContactName: name,
		Company:     o.Shipping.Company.String,
		Street1:     o.Shipping.AddressLineOne,
		Street2:     o.Shipping.AddressLineTwo.String,
		City:        o.Shipping.City,
		State:       o.Shipping.State.String,
		PostalCode:  o.Shipping.PostalCode,
		CountryISO2: iso2,
		Phone:       o.Buyer.Phone,
		Email:       o.Buyer.Email,
		Residential: true,
	}, nil
}

// validateShipFrom ensures the configured warehouse origin has the minimum fields a carrier needs.
func validateShipFrom(a entity.LabelAddress) error {
	if strings.TrimSpace(a.Street1) == "" || strings.TrimSpace(a.City) == "" ||
		strings.TrimSpace(a.PostalCode) == "" || strings.TrimSpace(a.CountryISO2) == "" {
		return fmt.Errorf("ship-from address is not configured (set SHIP_FROM_* env vars)")
	}
	return nil
}

func formatParcelDimensions(p entity.LabelParcel) string {
	if p.LengthCM > 0 && p.WidthCM > 0 && p.HeightCM > 0 {
		return fmt.Sprintf("%dx%dx%d cm", p.LengthCM, p.WidthCM, p.HeightCM)
	}
	return ""
}

func toPbParcel(p entity.LabelParcel) *pb_admin.ShippingParcel {
	return &pb_admin.ShippingParcel{
		WeightGrams: int32(p.WeightGrams),
		LengthCm:    int32(p.LengthCM),
		WidthCm:     int32(p.WidthCM),
		HeightCm:    int32(p.HeightCM),
		BoxType:     p.BoxType,
	}
}

func fromPbParcel(p *pb_admin.ShippingParcel) entity.LabelParcel {
	boxType := strings.TrimSpace(p.BoxType)
	if boxType == "" {
		boxType = "custom"
	}
	return entity.LabelParcel{
		WeightGrams: int(p.WeightGrams),
		LengthCM:    int(p.LengthCm),
		WidthCM:     int(p.WidthCm),
		HeightCM:    int(p.HeightCm),
		BoxType:     boxType,
	}
}
