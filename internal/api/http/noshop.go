package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"strings"

	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// noShopMessage is the fixed answer of every storefront route on an installation without a shop
// (TENANT_STOREFRONT_TEAM_ID=none).
const noShopMessage = "storefront is not available on this installation"

// noShopBody is the fixed 503 body: the grpc-gateway error shape with codes.Unavailable (14).
const noShopBody = `{"code":14,"message":"` + noShopMessage + `"}`

// SetNoShop marks an installation without a shop: FrontendService is not registered and the
// storefront gateway and the shop webhooks are not mounted; their routes answer Unavailable / 503.
// Call before Start.
func (s *Server) SetNoShop(on bool) {
	s.noShop = on
}

// noShopHTTP answers a storefront or webhook route on a no-shop installation.
func noShopHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(noShopBody))
}

// frontendMethodPrefix is the full-method prefix of every FrontendService RPC.
func frontendMethodPrefix() string {
	return "/" + pb_frontend.FrontendService_ServiceDesc.ServiceName + "/"
}

func isFrontendMethod(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, frontendMethodPrefix())
}

// noShopStream stops a FrontendService call before the tenant and auth interceptors see it. An
// unregistered service reaches the server's UnknownServiceHandler through the stream chain only.
func noShopStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	if isFrontendMethod(info.FullMethod) {
		return status.Error(codes.Unavailable, noShopMessage)
	}
	return h(srv, ss)
}

// noShopUnknownService answers calls to services this server does not register: FrontendService
// on a no-shop installation gets Unavailable, anything else grpc-go's own Unimplemented answer.
func noShopUnknownService(_ any, ss grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(ss)
	if isFrontendMethod(method) {
		return status.Error(codes.Unavailable, noShopMessage)
	}
	return status.Errorf(codes.Unimplemented, "unknown service %v", serviceOf(method))
}

func serviceOf(method string) string {
	m := strings.TrimPrefix(method, "/")
	if i := strings.LastIndex(m, "/"); i >= 0 {
		return m[:i]
	}
	return m
}

func passStream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	return h(srv, ss)
}

// noShopStreamInterceptor is noShopStream on a no-shop installation and a pass-through otherwise.
func (s *Server) noShopStreamInterceptor() grpc.StreamServerInterceptor {
	if s.noShop {
		return noShopStream
	}
	return passStream
}

// unknownServiceOption installs noShopUnknownService on a no-shop installation only; with a shop
// grpc-go keeps its own unknown-service answer.
func (s *Server) unknownServiceOption() grpc.ServerOption {
	if s.noShop {
		return grpc.UnknownServiceHandler(noShopUnknownService)
	}
	return grpc.EmptyServerOption{}
}

// mountShopWebhooks mounts the shop webhooks that have a handler (today's routes, unchanged).
func (s *Server) mountShopWebhooks(r chi.Router) {
	if s.webhookHandler != nil {
		r.Post("/api/webhooks/resend", s.webhookHandler.HandleResendEvent)
		r.Post("/api/webhooks/list-unsubscribe/{email_b64}/{token}", s.webhookHandler.HandleListUnsubscribe)
		r.Post("/api/webhooks/list-unsubscribe/{topic}/{email_b64}/{token}", s.webhookHandler.HandleCampaignListUnsubscribe)
	}
	if s.stripeWebhookHandler != nil {
		r.Post("/api/webhooks/stripe", s.stripeWebhookHandler.HandleStripeEvent)
	}
	if s.aftershipWebhookHandler != nil {
		r.Post("/api/webhooks/aftership", s.aftershipWebhookHandler.HandleAftershipEvent)
	}
}
