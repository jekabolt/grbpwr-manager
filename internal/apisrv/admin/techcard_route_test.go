package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// routeStubServer records which tech-card RPC the gateway dispatched to.
type routeStubServer struct {
	pb_admin.UnimplementedAdminServiceServer
	lastCall string
	lastID   int32
}

func (s *routeStubServer) GetTechCard(ctx context.Context, req *pb_admin.GetTechCardRequest) (*pb_admin.GetTechCardResponse, error) {
	s.lastCall = "Get"
	s.lastID = req.Id
	return &pb_admin.GetTechCardResponse{}, nil
}

func (s *routeStubServer) ListTechCards(ctx context.Context, req *pb_admin.ListTechCardsRequest) (*pb_admin.ListTechCardsResponse, error) {
	s.lastCall = "List"
	return &pb_admin.ListTechCardsResponse{}, nil
}

func (s *routeStubServer) GetTechCardReadiness(ctx context.Context, req *pb_admin.GetTechCardReadinessRequest) (*pb_admin.GetTechCardReadinessResponse, error) {
	s.lastCall = "Readiness"
	s.lastID = req.TechCardId
	return &pb_admin.GetTechCardReadinessResponse{}, nil
}

func (s *routeStubServer) ListTechCardFabricDirectionGaps(ctx context.Context, req *pb_admin.ListTechCardFabricDirectionGapsRequest) (*pb_admin.ListTechCardFabricDirectionGapsResponse, error) {
	s.lastCall = "DirectionGaps"
	s.lastID = req.TechCardId
	return &pb_admin.ListTechCardFabricDirectionGapsResponse{}, nil
}

func (s *routeStubServer) GetOperationWorkCatalog(ctx context.Context, req *pb_admin.GetOperationWorkCatalogRequest) (*pb_admin.GetOperationWorkCatalogResponse, error) {
	s.lastCall = "WorkCatalog"
	return &pb_admin.GetOperationWorkCatalogResponse{}, nil
}

// TestTechCardListRouteNotShadowed pins the grpc-gateway route-ordering invariant
// documented on the proto: GET /tech-card/list must reach ListTechCards, not
// GetTechCard with id="list". The mux prepends handlers and first-match wins, so
// the literal /list route is declared after /{id}; this test fails if that order
// regresses.
func TestTechCardListRouteNotShadowed(t *testing.T) {
	stub := &routeStubServer{}
	mux := runtime.NewServeMux()
	if err := pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/admin/tech-card/list")
	if err != nil {
		t.Fatalf("GET /tech-card/list: %v", err)
	}
	_ = resp.Body.Close()
	if stub.lastCall != "List" {
		t.Fatalf("GET /tech-card/list dispatched to %q (id=%d), want ListTechCards", stub.lastCall, stub.lastID)
	}

	// /tech-card/readiness/{id} carries an extra segment, so /{id} cannot swallow it — but the same
	// prepend-and-first-match ordering decides it, so pin it here rather than trusting the shape.
	stub.lastCall, stub.lastID = "", 0
	respR, err := http.Get(ts.URL + "/api/admin/tech-card/readiness/7")
	if err != nil {
		t.Fatalf("GET /tech-card/readiness/7: %v", err)
	}
	_ = respR.Body.Close()
	if stub.lastCall != "Readiness" || stub.lastID != 7 {
		t.Fatalf("GET /tech-card/readiness/7 dispatched to %q (id=%d), want GetTechCardReadiness id=7", stub.lastCall, stub.lastID)
	}

	// Ф1.8: /tech-card/fabric-direction-gaps is a literal single segment sitting exactly where
	// /tech-card/{id} matches, so it lives or dies by the same ordering — and its failure mode is
	// quiet, a 400 «cannot parse "fabric-direction-gaps" as int32» that reads like a client bug.
	// The route also carries no path parameter, so tech_card_id must arrive from the QUERY.
	stub.lastCall, stub.lastID = "", 0
	respG, err := http.Get(ts.URL + "/api/admin/tech-card/fabric-direction-gaps?tech_card_id=9")
	if err != nil {
		t.Fatalf("GET /tech-card/fabric-direction-gaps: %v", err)
	}
	_ = respG.Body.Close()
	if stub.lastCall != "DirectionGaps" || stub.lastID != 9 {
		t.Fatalf("GET /tech-card/fabric-direction-gaps dispatched to %q (id=%d), want ListTechCardFabricDirectionGaps id=9",
			stub.lastCall, stub.lastID)
	}

	// R3: /tech-card/operation-work/catalog несёт ДВА сегмента, поэтому /tech-card/{id} его не
	// проглатывает — но решает это тот же порядок прибавления обработчиков, а не форма пути, и
	// провал был бы тихим: 400 «cannot parse "operation-work" as int32», читающийся как баг клиента.
	stub.lastCall, stub.lastID = "", 0
	respW, err := http.Get(ts.URL + "/api/admin/tech-card/operation-work/catalog")
	if err != nil {
		t.Fatalf("GET /tech-card/operation-work/catalog: %v", err)
	}
	_ = respW.Body.Close()
	if stub.lastCall != "WorkCatalog" {
		t.Fatalf("GET /tech-card/operation-work/catalog dispatched to %q (id=%d), want GetOperationWorkCatalog",
			stub.lastCall, stub.lastID)
	}

	stub.lastCall, stub.lastID = "", 0
	resp2, err := http.Get(ts.URL + "/api/admin/tech-card/42")
	if err != nil {
		t.Fatalf("GET /tech-card/42: %v", err)
	}
	_ = resp2.Body.Close()
	if stub.lastCall != "Get" || stub.lastID != 42 {
		t.Fatalf("GET /tech-card/42 dispatched to %q (id=%d), want GetTechCard id=42", stub.lastCall, stub.lastID)
	}
}
