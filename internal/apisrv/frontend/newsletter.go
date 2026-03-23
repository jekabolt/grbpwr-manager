package frontend

import (
	"context"
	"log/slog"

	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	// Subscribe the user.
	isSubscribed, err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, true)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't subscribe", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.AlreadyExists, "can't subscribe")
	}
	slog.Default().DebugContext(ctx, "isSubscribed", slog.Bool("isSubscribed", isSubscribed))

	// Send new subscriber mail.
	if !isSubscribed {
		err = s.mailer.SendNewSubscriber(ctx, s.repo, req.Email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't send new subscriber mail",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't send new subscriber mail")
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
