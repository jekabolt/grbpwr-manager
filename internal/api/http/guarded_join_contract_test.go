package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
)

// Compatibility requirement for the proposed sparse command. An older admin gateway must
// never interpret a retired-source decision as a normal full-document replacement.
func TestSavedJoinRequiresSparseUpdateTechCardGuard(t *testing.T) {
	const body = `{"id":156,"expectedLockVersion":9,"guardedPieceJoin":{"scopeKey":"main","patternLineKey":"01JABCDEFGHJKMNPQRSTVWXYZ0","resultLineKey":"01JABCDEFGHJKMNPQRSTVWXYZB"}}`
	stub := &techCardStubServer{}
	server := serveAdmin(t, newAdminServeMux(), stub)
	code, reply := postTechCardUpdate(t, server, body)
	if code != http.StatusBadRequest || !strings.Contains(reply, "guardedPieceJoin") || stub.updateCalls != 0 {
		t.Fatalf("current gateway: code=%d calls=%d reply=%s", code, stub.updateCalls, reply)
	}
	var req pb_admin.UpdateTechCardRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if req.GetTechCard() != nil || req.GetGuardedPieceRename() != nil {
		t.Fatal("legacy document unexpectedly present")
	}
	if _, err := dto.ConvertPbTechCardInsertToEntity(req.GetTechCard()); err == nil {
		t.Fatal("legacy document converter accepted empty update")
	}
}
