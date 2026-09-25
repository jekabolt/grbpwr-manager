package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// EnhanceText (T15) through the PRODUCTION admin mux — newAdminServeMux(), with DiscardUnknown=false —
// carrying the body the admin client sends: camelCase names, enum members by name. The route test in
// apisrv/admin (N-02) proves the literal path is registered; this one proves the real transport takes
// the client's shape, and that a misspelled key is a loud 400 rather than a silently dropped limit.

type enhanceTextStub struct {
	pb_admin.UnimplementedAdminServiceServer
	calls int
	last  *pb_admin.EnhanceTextRequest
}

func (s *enhanceTextStub) EnhanceText(_ context.Context, req *pb_admin.EnhanceTextRequest) (*pb_admin.EnhanceTextResponse, error) {
	s.calls++
	s.last = req
	return &pb_admin.EnhanceTextResponse{Text: "rewritten"}, nil
}

func postEnhanceText(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/admin/ai/enhance-text", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/admin/ai/enhance-text: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(rb)
}

func TestEnhanceTextRouteOnTheProductionAdminMux(t *testing.T) {
	stub := &enhanceTextStub{}
	mux := newAdminServeMux()
	if err := pb_admin.RegisterAdminServiceHandlerServer(context.Background(), mux, stub); err != nil {
		t.Fatalf("register admin handler: %v", err)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	code, body := postEnhanceText(t, ts, `{"text":"boxy jacket, dropped shoulder","mode":"ENHANCE_TEXT_MODE_IMPROVE",`+
		`"field":"ENHANCE_TEXT_FIELD_DESCRIPTION","context":"fit: oversized","maxRunes":2000}`)
	if code != http.StatusOK {
		t.Fatalf("status %d, body %s", code, body)
	}
	if !strings.Contains(body, `"text":"rewritten"`) {
		t.Fatalf("response body %s does not carry the answer", body)
	}
	if stub.calls != 1 {
		t.Fatalf("EnhanceText called %d times, want 1", stub.calls)
	}
	if got := stub.last; got.GetText() != "boxy jacket, dropped shoulder" ||
		got.GetMode() != pb_admin.EnhanceTextMode_ENHANCE_TEXT_MODE_IMPROVE ||
		got.GetField() != pb_admin.EnhanceTextField_ENHANCE_TEXT_FIELD_DESCRIPTION ||
		got.GetContext() != "fit: oversized" || got.GetMaxRunes() != 2000 {
		t.Fatalf("decoded request %+v", got)
	}

	// A misspelled key never reaches the RPC: on this mux an unknown name is a 400, so the client cannot
	// lose its field limit without being told.
	code, body = postEnhanceText(t, ts, `{"text":"x","mode":"ENHANCE_TEXT_MODE_IMPROVE","field":"ENHANCE_TEXT_FIELD_NOTE","maxRune":2000}`)
	if code != http.StatusBadRequest {
		t.Fatalf("misspelled key: status %d, body %s — want 400", code, body)
	}
	if stub.calls != 1 {
		t.Fatalf("a refused body reached EnhanceText (calls=%d)", stub.calls)
	}
}
