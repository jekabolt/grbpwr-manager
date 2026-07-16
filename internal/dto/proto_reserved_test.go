package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestColorwayBodyInsertReservesVersion is the contract-governance descriptor test for problem 032: the
// removed product `version` field must be gone from the write contract AND reserved by both number (16)
// and name ("version"), so it can never be silently re-added with a different meaning (no free-text
// stub). The store never persisted it and it did not round-trip, so a client sending it must be able to
// tell the field is unsupported rather than get a discarded success.
func TestColorwayBodyInsertReservesVersion(t *testing.T) {
	md := (&pb_common.ColorwayBodyInsert{}).ProtoReflect().Descriptor()

	// The field must not exist as a live field, by name or by number.
	if f := md.Fields().ByName("version"); f != nil {
		t.Errorf("field \"version\" must be removed, but it is a live field (number %d)", f.Number())
	}
	if f := md.Fields().ByNumber(16); f != nil {
		t.Errorf("field number 16 must be removed, but it is a live field (%q)", f.Name())
	}

	// Number 16 must be reserved.
	reservedNum := false
	rr := md.ReservedRanges()
	for i := 0; i < rr.Len(); i++ {
		r := rr.Get(i)
		if 16 >= r[0] && 16 < r[1] {
			reservedNum = true
			break
		}
	}
	if !reservedNum {
		t.Errorf("field number 16 must be reserved on ColorwayBodyInsert")
	}

	// Name "version" must be reserved.
	reservedName := false
	rn := md.ReservedNames()
	for i := 0; i < rn.Len(); i++ {
		if rn.Get(i) == "version" {
			reservedName = true
			break
		}
	}
	if !reservedName {
		t.Errorf("field name \"version\" must be reserved on ColorwayBodyInsert")
	}
}
