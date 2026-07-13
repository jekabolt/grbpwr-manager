package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/tiermanagement"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SubscribeNewsletter stores the email preferences from the public subscribe form
// (name, shopping preference and per-topic opt-ins) on a storefront account — the
// same place and shape used for logged-in accounts — and keeps the legacy
// subscriber list in sync for promo sends.
func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	email := normalizeEmail(req.Email)
	if email == "" || !v.IsEmail(email) {
		return nil, status.Error(codes.InvalidArgument, "valid email is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckSubscribe(ip, email); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
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

// UnsubscribeNewsletter removes per-topic email opt-ins on the storefront account
// (mirroring the SubscribeNewsletter form) and keeps the legacy subscriber list in
// sync. Each flag set to true unsubscribes from that channel; when all flags are
// false the address is unsubscribed from every channel (the email-footer link
// sends no flags, so it opts out completely). A confirmation email is sent when
// the newsletter opt-in is turned off.
func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*pb_frontend.UnsubscribeNewsletterResponse, error) {
	email := normalizeEmail(req.Email)
	if email == "" || !v.IsEmail(email) {
		return nil, status.Error(codes.InvalidArgument, "valid email is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckSubscribe(ip, email); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	// Which channels to turn off. No flags set => unsubscribe from everything.
	offNewsletter := req.SubscribeNewsletter
	offNewArrivals := req.SubscribeNewArrivals
	offEvents := req.SubscribeEvents
	if !offNewsletter && !offNewArrivals && !offEvents {
		offNewsletter, offNewArrivals, offEvents = true, true, true
	}

	// Update the storefront account preferences if an account exists.
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	switch {
	case err == nil:
		if uerr := s.repo.StorefrontAccount().UpdateAccountProfile(ctx, email,
			acc.FirstName, acc.LastName, acc.BirthDate, acc.ShoppingPreference, acc.Phone,
			acc.SubscribeNewsletter && !offNewsletter,
			acc.SubscribeNewArrivals && !offNewArrivals,
			acc.SubscribeEvents && !offEvents,
			acc.DefaultCountry, acc.DefaultLanguage,
		); uerr != nil {
			slog.Default().ErrorContext(ctx, "can't update unsubscribe preferences", slog.String("err", uerr.Error()))
			return nil, status.Error(codes.Internal, "can't unsubscribe")
		}
	case errors.Is(err, sql.ErrNoRows):
		// No account — fall through to the legacy subscriber list only.
	default:
		slog.Default().ErrorContext(ctx, "can't load account for unsubscribe", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't unsubscribe")
	}

	// Keep the legacy subscriber list (promo sends) in sync with the newsletter opt-in.
	if offNewsletter {
		if _, err := s.repo.Subscribers().UpsertSubscription(ctx, email, false); err != nil {
			slog.Default().ErrorContext(ctx, "can't unsubscribe", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't unsubscribe")
		}
		// Confirmation email when the newsletter opt-in is removed.
		if err := tiermanagement.NewEngine(s.repo, s.mailer).OnNewsletterUnsubscribed(ctx, email); err != nil {
			slog.Default().ErrorContext(ctx, "can't send unsubscribe confirmation", slog.String("err", err.Error()))
		}
	}

	return &pb_frontend.UnsubscribeNewsletterResponse{}, nil
}
