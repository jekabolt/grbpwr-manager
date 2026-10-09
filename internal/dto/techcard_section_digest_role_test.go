package dto

import (
	"encoding/json"
	"testing"
)

// A role-less picture keeps the pre-0395 DESIGN projection byte-identical; setting a role changes it.
func TestDesignDigestMediaRole(t *testing.T) {
	empty, err := json.Marshal(digestMedia{MediaID: 1, Kind: "moodboard"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"m":1,"k":"moodboard","c":"","g":""}`; string(empty) != want {
		t.Fatalf("role-less media projection changed:\n got %s\nwant %s", empty, want)
	}
	withRole, _ := json.Marshal(digestMedia{MediaID: 1, Kind: "moodboard", Role: "target"})
	if string(withRole) == string(empty) {
		t.Fatal("a role must change the DESIGN projection")
	}
}
