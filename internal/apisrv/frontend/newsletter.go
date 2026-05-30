package frontend

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SubscribeNewsletter stores the email preferences from the public subscribe form
// (name, shopping preference and per-topic opt-ins) on a storefront account — the
// same place and shape used for logged-in accounts — and keeps the legacy
// subscriber list in sync for promo sends.
func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	email := strings.TrimSpace(req.Email)
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}

	// shopping preference (all/men/women) is required by the form.
	if req.ShoppingPreference == pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_UNKNOWN {
		return nil, status.Error(codes.InvalidArgument, "shopping preference is required")
	}
	shoppingPref, err := dto.ConvertPbShoppingPreferenceEnumToEntity(req.ShoppingPreference)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid shopping preference")
	}

	// Store on the storefront account (create a shell account if none exists).
	acc, err := s.repo.StorefrontAccount().GetOrCreateAccountByEmail(ctx, email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get or create account for subscription", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't subscribe")
	}

	firstName := acc.FirstName
	if name := strings.TrimSpace(req.Name); name != "" {
		firstName = name
	}

	if err := s.repo.StorefrontAccount().UpdateAccountProfile(ctx, email,
		firstName, acc.LastName, acc.BirthDate, shoppingPref, acc.Phone,
		req.SubscribeNewsletter, req.SubscribeNewArrivals, req.SubscribeEvents,
		acc.DefaultCountry, acc.DefaultLanguage,
	); err != nil {
		slog.Default().ErrorContext(ctx, "can't update subscription preferences", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't subscribe")
	}

	// Keep the legacy subscriber list (used by promo sends) in sync with the newsletter opt-in.
	wasSubscribed, err := s.repo.Subscribers().UpsertSubscription(ctx, email, req.SubscribeNewsletter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't sync subscriber", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't subscribe")
	}

	// Welcome email on first newsletter opt-in.
	if req.SubscribeNewsletter && !wasSubscribed {
		if err := s.mailer.SendNewSubscriber(ctx, s.repo, email); err != nil {
			slog.Default().ErrorContext(ctx, "can't send new subscriber mail", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't send new subscriber mail")
		}
	}

	return &pb_frontend.SubscribeNewsletterResponse{}, nil
}

func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*pb_frontend.UnsubscribeNewsletterResponse, error) {
	_, err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, false)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't unsubscribe",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't unsubscribe")
	}
	return &pb_frontend.UnsubscribeNewsletterResponse{}, nil
}
