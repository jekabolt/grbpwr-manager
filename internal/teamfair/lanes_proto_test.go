package teamfair

import (
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// Every method LaneOf names must exist: a renamed rpc would silently fall back to Light.
func TestLaneNamesExist(t *testing.T) {
	have := map[string]bool{}
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		have[m.MethodName] = true
	}
	heavy, ai := 0, 0
	for name := range have {
		switch LaneOf("/admin.AdminService/" + name) {
		case Heavy:
			heavy++
		case AI:
			ai++
		}
	}
	// The counts of the two lists in lanes.go: all found in the service.
	if heavy != 15 || ai != 17 {
		t.Fatalf("lanes.go names a method the AdminService does not have: heavy %d of 15, ai %d of 17", heavy, ai)
	}
}
