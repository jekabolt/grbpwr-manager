package httpapi

import (
	"context"

	"google.golang.org/grpc"
)

// SetTeamFair installs the per-team bulkhead (internal/teamfair, X15) as a unary interceptor
// after admin auth. Call before Start; unset = no per-team limits.
func (s *Server) SetTeamFair(i grpc.UnaryServerInterceptor) {
	s.teamFair = i
}

func (s *Server) teamFairUnary() grpc.UnaryServerInterceptor {
	if s.teamFair == nil {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return h(ctx, req)
		}
	}
	return s.teamFair
}
