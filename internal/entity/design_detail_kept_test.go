package entity

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func keptSlot(id int, view string, pic, run int) DesignBenchSlot {
	sl := DesignBenchSlot{Id: id, ViewKey: view, Kind: DesignPictureKindFlat}
	if pic > 0 {
		sl.PictureId = sql.NullInt32{Int32: int32(pic), Valid: true}
		p := &DesignPicture{Id: pic}
		if run > 0 {
			p.RunId = sql.NullInt32{Int32: int32(run), Valid: true}
		}
		sl.Picture = p
	}
	return sl
}

func TestDesignDetailStaleAgainstTheFrontRun(t *testing.T) {
	bench := []DesignBenchSlot{
		keptSlot(1, DesignViewFront, 100, 50),
		keptSlot(2, DesignViewBack, 101, 50),
		keptSlot(3, DesignViewDetail, 102, 40), // older run → stale
		keptSlot(4, DesignViewDetail, 103, 60), // newer run → fresh
		keptSlot(5, DesignViewDetail, 104, 0),  // upload → never stale
		keptSlot(6, DesignViewDetail, 0, 0),    // empty
	}
	ApplyDesignDetailStaleness(bench)
	require.True(t, bench[2].Stale)
	require.False(t, bench[2].Kept)
	require.Equal(t, 50, bench[2].StaleAgainstRunId)
	require.False(t, bench[3].Stale)
	require.False(t, bench[4].Stale)
	require.False(t, bench[5].Stale)
	require.False(t, bench[0].Stale, "a side is never stale")
	require.Zero(t, bench[0].StaleAgainstRunId)
}

func TestDesignDetailStaleFallsBackToBackAndIgnoresUploads(t *testing.T) {
	// front empty → back's run rules
	bench := []DesignBenchSlot{
		keptSlot(1, DesignViewFront, 0, 0),
		keptSlot(2, DesignViewBack, 101, 70),
		keptSlot(3, DesignViewDetail, 102, 60),
	}
	ApplyDesignDetailStaleness(bench)
	require.True(t, bench[2].Stale)
	require.Equal(t, 70, bench[2].StaleAgainstRunId)

	// an uploaded front (no run): nothing is stale against it, even with a newer back run
	bench = []DesignBenchSlot{
		keptSlot(1, DesignViewFront, 100, 0),
		keptSlot(2, DesignViewBack, 101, 70),
		keptSlot(3, DesignViewDetail, 102, 60),
	}
	ApplyDesignDetailStaleness(bench)
	require.False(t, bench[2].Stale)
}

func TestDesignDetailKeptHoldsOnlyForTheSameViewsAndPlate(t *testing.T) {
	mk := func(keptRun, keptPic int) []DesignBenchSlot {
		d := keptSlot(3, DesignViewDetail, 102, 40)
		d.KeptRunId = sql.NullInt32{Int32: int32(keptRun), Valid: keptRun > 0}
		d.KeptPictureId = sql.NullInt32{Int32: int32(keptPic), Valid: keptPic > 0}
		return []DesignBenchSlot{keptSlot(1, DesignViewFront, 100, 50), d}
	}
	b := mk(50, 102)
	ApplyDesignDetailStaleness(b)
	require.True(t, b[1].Stale)
	require.True(t, b[1].Kept)

	b = mk(45, 102) // kept against older views → views changed again → pill back
	ApplyDesignDetailStaleness(b)
	require.True(t, b[1].Stale)
	require.False(t, b[1].Kept)

	b = mk(50, 99) // kept another plate
	ApplyDesignDetailStaleness(b)
	require.False(t, b[1].Kept)
}

func TestDesignDetailStaleOnlyOnTheFlatBench(t *testing.T) {
	d := keptSlot(3, DesignViewDetail, 102, 40)
	d.Kind = DesignPictureKindRender
	front := keptSlot(1, DesignViewFront, 100, 50)
	front.Kind = DesignPictureKindRender
	bench := []DesignBenchSlot{front, d}
	ApplyDesignDetailStaleness(bench)
	require.False(t, bench[1].Stale)
	require.Zero(t, DesignViewsRunId(bench))
}
