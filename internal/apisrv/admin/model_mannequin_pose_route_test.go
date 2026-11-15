package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type modelMannequinPoseRouteStub struct {
	pb_admin.UnimplementedAdminServiceServer
	method  string
	modelID int32
}

func (s *modelMannequinPoseRouteStub) GetModelMannequinPose(_ context.Context, req *pb_admin.GetModelMannequinPoseRequest) (*pb_admin.GetModelMannequinPoseResponse, error) {
	s.method, s.modelID = "get", req.GetModelId()
	return &pb_admin.GetModelMannequinPoseResponse{}, nil
}

func (s *modelMannequinPoseRouteStub) PutModelMannequinPose(_ context.Context, req *pb_admin.PutModelMannequinPoseRequest) (*pb_admin.PutModelMannequinPoseResponse, error) {
	s.method, s.modelID = "put", req.GetModelId()
	return &pb_admin.PutModelMannequinPoseResponse{}, nil
}

func TestModelMannequinPoseHTTPRoutes(t *testing.T) {
	stub := &modelMannequinPoseRouteStub{}
	mux := runtime.NewServeMux()
	if err := pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/admin/model/7/mannequin-pose")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || stub.method != "get" || stub.modelID != 7 {
		t.Fatalf("GET route: status=%d method=%q model=%d", response.StatusCode, stub.method, stub.modelID)
	}

	stub.method, stub.modelID = "", 0
	response, err = http.Post(server.URL+"/api/admin/model/8/mannequin-pose", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || stub.method != "put" || stub.modelID != 8 {
		t.Fatalf("POST route: status=%d method=%q model=%d", response.StatusCode, stub.method, stub.modelID)
	}
}
