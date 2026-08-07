package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestExpectedLockVersionPresenceOnTheWire pins the claim the whole Ф6.5 fix rests on: over the
// REST gateway's JSON, `"expectedLockVersion": 0` and an omitted field are two different things.
//
// This has to be tested against protojson rather than reasoned about, because the entire safety
// argument for shipping is «today's admin client already sends the version it read — including a
// literal 0 for a fresh run — so it gains the guard with no client change, while any caller that
// omits the field keeps the legacy behaviour». If protojson collapsed a present 0 into absence
// (the way a proto3 field without explicit presence does), the fix would be inert on exactly the
// runs it exists to protect, and the failure would be silent.
func TestExpectedLockVersionPresenceOnTheWire(t *testing.T) {
	t.Run("update: present zero is PRESENT, omitted is absent", func(t *testing.T) {
		var present pb_admin.UpdateProductionRunRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"id":7,"expectedLockVersion":0}`), &present))
		g := entity.LockGuardFromProto(present.ExpectedLockVersion)
		require.True(t, g.Present(), "a client that sent 0 stated an expectation")
		require.Equal(t, 0, g.Version())
		require.True(t, g.Conflicts(1), "holding 0 against a run at 1 is a conflict")
		require.False(t, g.Conflicts(0), "holding 0 against a fresh run is not")

		var omitted pb_admin.UpdateProductionRunRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"id":7}`), &omitted))
		lg := entity.LockGuardFromProto(omitted.ExpectedLockVersion)
		require.False(t, lg.Present(), "a pre-Ф6.5 caller sent nothing")
		require.False(t, lg.Conflicts(0), "the legacy token never conflicts")
		require.False(t, lg.Conflicts(42), "at any stored version")
	})

	t.Run("receipt and reversal carry the same distinction", func(t *testing.T) {
		var rcpt pb_admin.PostProductionRunReceiptRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"runId":4,"expectedLockVersion":0}`), &rcpt))
		require.True(t, entity.LockGuardFromProto(rcpt.ExpectedLockVersion).Present())

		var rcptOmitted pb_admin.PostProductionRunReceiptRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"runId":4}`), &rcptOmitted))
		require.False(t, entity.LockGuardFromProto(rcptOmitted.ExpectedLockVersion).Present())

		var rev pb_admin.ReverseProductionRunReceiptRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"runId":4,"expectedLockVersion":0}`), &rev))
		require.True(t, entity.LockGuardFromProto(rev.ExpectedLockVersion).Present())

		var revOmitted pb_admin.ReverseProductionRunReceiptRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"runId":4}`), &revOmitted))
		require.False(t, entity.LockGuardFromProto(revOmitted.ExpectedLockVersion).Present())
	})

	// Wire compatibility: `optional` on an existing proto3 scalar keeps the field number and varint
	// encoding, so a binary client built against the OLD schema is unaffected. A non-zero value sent
	// by such a client still arrives as a set field.
	t.Run("a non-zero legacy value still arrives set", func(t *testing.T) {
		var req pb_admin.UpdateProductionRunRequest
		require.NoError(t, protojson.Unmarshal([]byte(`{"id":7,"expectedLockVersion":5}`), &req))
		g := entity.LockGuardFromProto(req.ExpectedLockVersion)
		require.True(t, g.Present())
		require.Equal(t, 5, g.Version())
		require.True(t, g.Conflicts(4))
		require.False(t, g.Conflicts(5))
	})
}
