// Package tenantbind puts the team of a request into its context at the edge.
//
// Compat is the wave-1 binder: every gRPC call, every stream and every chi request runs as team
// #1, exactly as the single-tenant binary did. B2 replaces it with the binder that resolves the
// team from the token and the x-team header.
package tenantbind

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
)

// Compat binds one fixed team (team #1) to every request.
type Compat struct {
	team tenant.Team
}

// NewCompat returns the binder for team #1.
func NewCompat(team tenant.Team) (*Compat, error) {
	if team.ID != tenant.TeamOneID || team.DBName == "" {
		return nil, fmt.Errorf("tenantbind: compat binder needs team #1 with its database, got team %d", team.ID)
	}
	return &Compat{team: team}, nil
}

func (c *Compat) bind(ctx context.Context) context.Context { return tenant.With(ctx, c.team) }

// Unary binds the team for a unary call.
func (c *Compat) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(c.bind(ctx), req)
	}
}

// Stream binds the team for a stream.
func (c *Compat) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &boundStream{ServerStream: ss, ctx: c.bind(ss.Context())})
	}
}

// Middleware binds the team for a chi / net/http request.
func (c *Compat) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(c.bind(r.Context())))
	})
}

type boundStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *boundStream) Context() context.Context { return s.ctx }
