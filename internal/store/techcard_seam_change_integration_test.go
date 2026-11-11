package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/techcard"
	"github.com/stretchr/testify/require"
)

// TestTechCardSeamChange drives ChangeTechCardSeams through the real store: one transaction for
// upsert + link + detach + delete under lock_version, a failure in any phase leaving rows, links and
// the version untouched, a moved version refused, detach deleting a row only when its last link went
// in the same change, contradictions refused whole, the rekey onto a stored pair, the old seam RPCs
// leaving the version alone, and the card read returning version, links and rows from one snapshot.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardSeamChange(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	const pA, pB, pC = "01SCHGPIECEA00000000000001", "01SCHGPIECEB00000000000002", "01SCHGPIECEC00000000000003"
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	var cards atomic.Int64
	newCard := func(t *testing.T, numbers ...int32) int {
		t.Helper()
		ops := make([]entity.TechCardOperation, 0, len(numbers))
		for i, n := range numbers {
			ops = append(ops, entity.TechCardOperation{
				OperationNumber: sql.NullInt32{Int32: n, Valid: true},
				OperationType:   entity.OpTypeMachine, MachineType: ns("lockstitch"),
				Zone: entity.ZoneOuter, Note: ns(fmt.Sprintf("step %d #%d", n, i)),
			})
		}
		piece := func(key, name string) entity.TechCardPiece {
			return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
		}
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "SEAM CHANGE", Stage: entity.TechCardStageProto,
			StyleNumber:     ns(fmt.Sprintf("SCH-%d-%d", time.Now().UnixNano(), cards.Add(1))),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Pieces:     []entity.TechCardPiece{piece(pA, "front"), piece(pB, "back"), piece(pC, "pocket")},
			Operations: ops,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), id) })
		return id
	}
	key := func(n int) string { return fmt.Sprintf("01SCHGSEAM%016d", n) }
	anchor := func(piece, hint string, from, to float64) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: piece,
			Samples:      []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
			PerimShare:   0.1, LenMm: 100, EdgeHint: hint, RangeFrom: from, RangeTo: to,
		}
	}
	// edge is a seam front hint ↔ back hint over the whole runs (wire 0/0).
	edge := func(k, hintA, hintB string) entity.TechCardSeamInput {
		return entity.TechCardSeamInput{
			SeamKey: k, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual,
			SideA: []entity.TechCardSeamAnchor{anchor(pA, hintA, 0, 0)},
			SideB: []entity.TechCardSeamAnchor{anchor(pB, hintB, 0, 0)},
		}
	}
	pose := func(k string) entity.TechCardSeamInput {
		moved := anchor(pC, "", 0, 0)
		moved.Placement = &entity.TechCardSeamPlacement{XMm: 10, YMm: 20, RotDeg: 0}
		return entity.TechCardSeamInput{
			SeamKey: k, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindPose,
			Direction: entity.TechCardSeamDirectionUnknown, Source: entity.TechCardSeamSourceManual,
			SideA: []entity.TechCardSeamAnchor{moved}, SideB: []entity.TechCardSeamAnchor{anchor(pA, "", 0, 0)},
		}
	}
	link := func(op int, k string) entity.TechCardSeamLink {
		return entity.TechCardSeamLink{OperationNumber: op, SeamKey: k}
	}
	type cardState struct {
		Version int
		Seams   []string         // stored keys, sorted
		Links   map[int][]string // operation_number → keys in sewing order
	}
	state := func(t *testing.T, id int) cardState {
		t.Helper()
		card, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		s := cardState{Version: card.LockVersion, Seams: []string{}, Links: map[int][]string{}}
		for _, seam := range card.Seams {
			s.Seams = append(s.Seams, seam.SeamKey)
		}
		sort.Strings(s.Seams)
		for _, o := range card.Operations {
			keys := append([]string{}, o.SeamKeys...)
			s.Links[int(o.OperationNumber.Int32)] = keys
		}
		return s
	}
	change := func(id, version int, c entity.TechCardSeamsChange) (entity.TechCardSeamsChangeResult, error) {
		c.TechCardId = id
		c.ExpectedLockVersion = &version
		c.By = "ann"
		return T.ChangeTechCardSeams(ctx, c)
	}
	mustChange := func(t *testing.T, id, version int, c entity.TechCardSeamsChange) entity.TechCardSeamsChangeResult {
		t.Helper()
		res, err := change(id, version, c)
		require.NoError(t, err)
		return res
	}
	violation := func(t *testing.T, err error) string {
		t.Helper()
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		return ve.Field + " " + ve.Reason
	}
	sorted := func(xs ...string) []string { sort.Strings(xs); return xs }

	t.Run("upsert and link in one call", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		v0 := state(t, id).Version
		res := mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(1), "F#1", "B#1")},
			Link:   []entity.TechCardSeamLink{link(10, key(1))},
		})
		require.Equal(t, v0+1, res.LockVersion, "one bump per change")
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(1), Stored: key(1)}}, res.KeyMap)
		require.Equal(t, []entity.TechCardOperationSeamKeys{
			{OperationNumber: 10, SeamKeys: []string{key(1)}},
			{OperationNumber: 20, SeamKeys: []string{}},
			{OperationNumber: 30, SeamKeys: []string{}},
		}, res.Links)
		require.Len(t, res.Seams, 1)
		require.Empty(t, res.DeletedKeys)
		got := state(t, id)
		require.Equal(t, cardState{Version: v0 + 1, Seams: []string{key(1)},
			Links: map[int][]string{10: {key(1)}, 20: {}, 30: {}}}, got, "the response is what was committed")

		// A second link on the same step is appended; linking a pair already linked is a no-op.
		res = mustChange(t, id, v0+1, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(2), "F#2", "B#2")},
			Link:   []entity.TechCardSeamLink{link(10, key(1)), link(10, key(2))},
		})
		require.Equal(t, v0+2, res.LockVersion)
		require.Equal(t, []string{key(1), key(2)}, res.Links[0].SeamKeys)
		res = mustChange(t, id, v0+2, entity.TechCardSeamsChange{Link: []entity.TechCardSeamLink{link(10, key(2))}})
		require.Equal(t, v0+2, res.LockVersion, "a change that changed nothing does not move the version")
	})

	t.Run("a failure in any phase rolls the whole change back", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		v0 := state(t, id).Version
		mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(10), "F#1", "B#1"), edge(key(11), "F#2", "B#2")},
			Link:   []entity.TechCardSeamLink{link(20, key(10)), link(30, key(11))},
		})
		seed := state(t, id)
		gesture := entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(12), "F#3", "B#3")},
			Link:   []entity.TechCardSeamLink{link(10, key(12))},
			Detach: []entity.TechCardSeamLink{link(20, key(10))},
			Delete: []string{key(11)},
		}
		injected := errors.New("injected")
		for _, phase := range []string{"upsert", "link", "detach", "delete", "bump"} {
			restore := techcard.SetSeamChangeFaultForTest(func(p string) error {
				if p == phase {
					return injected
				}
				return nil
			})
			_, err := change(id, seed.Version, gesture)
			restore()
			require.ErrorIs(t, err, injected, phase)
			require.Equal(t, seed, state(t, id), "a failure after %s leaves rows, links and version as they were", phase)
		}

		// A real refusal inside the link phase, after the upsert ran: the upsert goes with it.
		_, err := change(id, seed.Version, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(13), "F#4", "B#4")},
			Link:   []entity.TechCardSeamLink{link(10, key(13)), link(10, key(99))},
		})
		require.Equal(t, "link[1].seam_key unknown_seam", violation(t, err))
		require.Equal(t, seed, state(t, id), "the refused link took the upsert with it")

		// Without a fault the same gesture lands whole.
		res := mustChange(t, id, seed.Version, gesture)
		require.Equal(t, seed.Version+1, res.LockVersion)
		require.Equal(t, sorted(key(10), key(11)), sorted(res.DeletedKeys...),
			"the detached row lost its last link; the deleted row went unconditionally")
		require.Equal(t, cardState{Version: seed.Version + 1, Seams: []string{key(12)},
			Links: map[int][]string{10: {key(12)}, 20: {}, 30: {}}}, state(t, id))
	})

	t.Run("a stale or missing expected_lock_version writes nothing", func(t *testing.T) {
		id := newCard(t, 10, 20)
		v0 := state(t, id).Version
		mustChange(t, id, v0, entity.TechCardSeamsChange{Upsert: []entity.TechCardSeamInput{edge(key(20), "F#1", "B#1")}})
		seed := state(t, id)
		require.Equal(t, v0+1, seed.Version)
		gesture := entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(21), "F#2", "B#2")},
			Link:   []entity.TechCardSeamLink{link(10, key(21))},
			Delete: []string{key(20)},
		}
		for _, stale := range []int{v0, seed.Version + 5} {
			_, err := change(id, stale, gesture)
			require.ErrorIs(t, err, entity.ErrTechCardLockVersionMoved)
			require.Equal(t, seed, state(t, id))
		}
		missing := gesture
		missing.TechCardId = id
		_, err := T.ChangeTechCardSeams(ctx, missing)
		require.Equal(t, "expected_lock_version required", violation(t, err))
		require.Equal(t, seed, state(t, id))
	})

	t.Run("a step reordered by a card save after the read is refused", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		v0 := state(t, id).Version
		mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(30), "F#1", "B#1")},
			Link:   []entity.TechCardSeamLink{link(30, key(30))},
		})
		read, err := T.GetTechCardById(ctx, id) // the client's read before its gesture
		require.NoError(t, err)
		// Another tab moves the last step first and saves; the server numbers steps by position.
		edit := read.TechCardInsert
		edit.Operations = []entity.TechCardOperation{read.Operations[2], read.Operations[0], read.Operations[1]}
		for i := range edit.Operations {
			edit.Operations[i].OperationNumber = sql.NullInt32{Int32: int32((i + 1) * 10), Valid: true}
		}
		require.NoError(t, T.UpdateTechCard(ctx, id, &edit, read.LockVersion))
		saved := state(t, id)
		require.Equal(t, []string{key(30)}, saved.Links[10], "the linked step is number 10 now")

		// The gesture still means «the step that was 20» — that step is 30 now; the version says so.
		_, err = change(id, read.LockVersion, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(31), "F#2", "B#2")},
			Link:   []entity.TechCardSeamLink{link(20, key(31))},
		})
		require.ErrorIs(t, err, entity.ErrTechCardLockVersionMoved)
		require.Equal(t, saved, state(t, id), "nothing written")
	})

	t.Run("detach deletes a row only when this change removed its last link", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		v0 := state(t, id).Version
		mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{
				edge(key(40), "F#1", "B#1"), edge(key(41), "F#2", "B#2"), edge(key(42), "F#3", "B#3"),
				pose(key(43)), pose(key(44)), pose(key(45)),
			},
			Link: []entity.TechCardSeamLink{
				link(10, key(40)), link(20, key(40)), link(10, key(41)),
				link(10, key(43)), link(20, key(43)), link(10, key(44)),
			},
		})
		// key(42) and key(45) are review rows never linked to a step.
		v1 := v0 + 1
		res := mustChange(t, id, v1, entity.TechCardSeamsChange{Detach: []entity.TechCardSeamLink{
			link(10, key(40)), link(10, key(41)), link(10, key(42)),
			link(10, key(43)), link(10, key(44)), link(10, key(45)),
		}})
		require.Equal(t, v1+1, res.LockVersion)
		require.Equal(t, sorted(key(41), key(44)), sorted(res.DeletedKeys...),
			"a seam and a pose that lost their only link go; two-link rows and never-linked rows stay")
		require.Equal(t, cardState{Version: v1 + 1, Seams: sorted(key(40), key(42), key(43), key(45)),
			Links: map[int][]string{10: {}, 20: {key(40), key(43)}, 30: {}}}, state(t, id))

		// Detaching links that are not there deletes nothing and changes nothing.
		before := state(t, id)
		res = mustChange(t, id, v1+1, entity.TechCardSeamsChange{Detach: []entity.TechCardSeamLink{
			link(30, key(42)), link(30, key(45)), link(30, key(40)),
		}})
		require.Empty(t, res.DeletedKeys)
		require.Equal(t, v1+1, res.LockVersion, "a no-op detach does not move the version")
		require.Equal(t, before, state(t, id))

		// A row detached from one step and linked to another in the same change stays.
		res = mustChange(t, id, v1+1, entity.TechCardSeamsChange{
			Link:   []entity.TechCardSeamLink{link(30, key(40))},
			Detach: []entity.TechCardSeamLink{link(20, key(40))},
		})
		require.Empty(t, res.DeletedKeys)
		require.Equal(t, map[int][]string{10: {}, 20: {key(43)}, 30: {key(40)}}, state(t, id).Links)
	})

	t.Run("contradictory requests are refused whole", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		// A save renumbers steps by position, so two steps sharing a number only come from a legacy
		// row; plant one directly.
		_, err := testDB.ExecContext(ctx,
			"UPDATE tech_card_operation SET operation_number = 20 WHERE tech_card_id = ? AND operation_number = 30", id)
		require.NoError(t, err)
		v0 := state(t, id).Version
		stored := edge(key(50), "F#1", "B#1")
		mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{stored},
			Link:   []entity.TechCardSeamLink{link(10, key(50))},
		})
		seed := state(t, id)
		fresh := edge(key(51), "F#2", "B#2")
		samePair := edge(key(52), "F#1", "B#1")
		samePair2 := edge(key(53), "F#1", "B#1")
		for _, tc := range []struct {
			name string
			c    entity.TechCardSeamsChange
			want string
		}{
			{"link and detach of one pair", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{fresh},
				Link:   []entity.TechCardSeamLink{link(10, key(51))},
				Detach: []entity.TechCardSeamLink{link(10, key(51))},
			}, "detach[0] contradicts_link"},
			{"a linked key deleted", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{fresh},
				Link:   []entity.TechCardSeamLink{link(10, key(50))},
				Delete: []string{key(50)},
			}, "delete[0] contradicts_link"},
			{"a step not on the card", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{fresh},
				Link:   []entity.TechCardSeamLink{link(99, key(51))},
			}, "link[0].operation_number unknown_operation"},
			{"a step number on two steps", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{fresh},
				Detach: []entity.TechCardSeamLink{link(20, key(50))},
			}, "detach[0].operation_number operation_not_unique"},
			{"two new rows of one stored pair", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{samePair, samePair2},
			}, "upsert[1].seam_key duplicate_pair"},
			{"link and detach of one row once the rekey resolved it", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{samePair},
				Link:   []entity.TechCardSeamLink{link(10, key(50))},
				Detach: []entity.TechCardSeamLink{link(10, key(52))},
			}, "detach[0] contradicts_link"},
		} {
			_, err := change(id, seed.Version, tc.c)
			require.Error(t, err, tc.name)
			require.Equal(t, tc.want, violation(t, err), tc.name)
			require.Equal(t, seed, state(t, id), "%s: nothing written", tc.name)
		}
	})

	t.Run("the old seam RPCs do not move lock_version", func(t *testing.T) {
		id := newCard(t, 10)
		v0 := state(t, id).Version
		_, _, err := T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{TechCardId: id, By: "ann",
			Seams: []entity.TechCardSeamInput{edge(key(60), "F#1", "B#1")}})
		require.NoError(t, err)
		require.Equal(t, v0, state(t, id).Version)
		_, deleted, err := T.DeleteTechCardSeams(ctx, id, []string{key(60)})
		require.NoError(t, err)
		require.Equal(t, 1, deleted)
		require.Equal(t, v0, state(t, id).Version)
	})

	t.Run("a new row of a stored pair is stored under the stored key", func(t *testing.T) {
		id := newCard(t, 10, 20)
		v0 := state(t, id).Version
		mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(70), "F#1", "B#1"), pose(key(72))},
		})
		// The same pair from another client: sides swapped, the whole run sent as an explicit 0/1.
		again := entity.TechCardSeamInput{
			SeamKey: key(71), Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual, Note: "second",
			SideA: []entity.TechCardSeamAnchor{anchor(pB, "B#1", 0, 1)},
			SideB: []entity.TechCardSeamAnchor{anchor(pA, "F#1", 0, 1)},
		}
		res := mustChange(t, id, v0+1, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{again, pose(key(73))},
			Link:   []entity.TechCardSeamLink{link(10, key(71))},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{
			{Sent: key(71), Stored: key(70)},
			{Sent: key(73), Stored: key(73)}, // a pose is never rekeyed
		}, res.KeyMap)
		require.Equal(t, []string{key(70)}, res.Links[0].SeamKeys, "the link names the stored key")
		got := state(t, id)
		require.Equal(t, sorted(key(70), key(72), key(73)), got.Seams, "one pair, one row")
		require.Equal(t, []string{key(70)}, got.Links[10])
		for _, s := range res.Seams {
			if s.SeamKey == key(70) {
				require.Equal(t, "second", s.Note, "the stored row took the new content")
			}
		}

		// MIRROR: a new row replacing a stored row of the same pair on the same step is NOT rekeyed
		// onto the row it detaches — that would detach the new row in the same breath.
		res = mustChange(t, id, v0+2, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(74), "F#1", "B#1")},
			Link:   []entity.TechCardSeamLink{link(10, key(74))},
			Detach: []entity.TechCardSeamLink{link(10, key(70))},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(74), Stored: key(74)}}, res.KeyMap)
		require.Equal(t, []string{key(70)}, res.DeletedKeys)
		require.Equal(t, []string{key(74)}, state(t, id).Links[10])
	})

	t.Run("the card read returns version, links and rows from one snapshot", func(t *testing.T) {
		id := newCard(t, 10)
		v0 := state(t, id).Version
		k := key(80)
		var (
			wg      sync.WaitGroup
			done    atomic.Bool
			writeEr error
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer done.Store(true)
			v := v0
			for i := 0; i < 15; i++ {
				if _, err := change(id, v, entity.TechCardSeamsChange{
					Upsert: []entity.TechCardSeamInput{edge(k, "F#1", "B#1")},
					Link:   []entity.TechCardSeamLink{link(10, k)},
				}); err != nil {
					writeEr = err
					return
				}
				if _, err := change(id, v+1, entity.TechCardSeamsChange{Delete: []string{k}}); err != nil {
					writeEr = err
					return
				}
				v += 2
			}
		}()
		reads := 0
		for !done.Load() || reads == 0 {
			card, err := T.GetTechCardByIdConsistent(ctx, id)
			require.NoError(t, err)
			present := (card.LockVersion-v0)%2 == 1
			require.Equal(t, present, len(card.Seams) == 1, "version %d vs rows %d", card.LockVersion, len(card.Seams))
			if present {
				require.Equal(t, []string{k}, card.Operations[0].SeamKeys)
			} else {
				require.Empty(t, card.Operations[0].SeamKeys)
			}
			reads++
		}
		wg.Wait()
		require.NoError(t, writeEr)
		require.Equal(t, v0+30, state(t, id).Version)
	})
}
