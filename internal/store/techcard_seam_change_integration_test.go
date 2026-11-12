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

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/store/techcard"
	"github.com/jmoiron/sqlx"
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

	t.Run("a failure at any write rolls the whole change back", func(t *testing.T) {
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
		send := func(f *seamFault) error {
			f.err = injected
			c := gesture
			c.TechCardId, c.ExpectedLockVersion, c.By = id, &seed.Version, "ann"
			_, err := faultyCards(st, f).ChangeTechCardSeams(ctx, c)
			return err
		}
		// The whole gesture, refused at commit: counts its writes, and a refused commit keeps nothing.
		dry := &seamFault{atCommit: true}
		require.ErrorIs(t, send(dry), injected)
		require.Equal(t, seed, state(t, id), "a refused commit leaves rows, links and version as they were")
		writes := dry.writes
		require.GreaterOrEqual(t, writes, 5, "at least one write per phase: upsert, link, detach, delete, bump")
		for n := 1; n <= writes; n++ {
			require.ErrorIs(t, send(&seamFault{failWrite: n}), injected, "write %d", n)
			require.Equal(t, seed, state(t, id), "a failure at write %d of %d leaves rows, links and version as they were", n, writes)
		}
		// The response read after the last write (the bump) fails: the bump goes back with the rest.
		require.ErrorIs(t, send(&seamFault{failReadAfter: writes}), injected)
		require.Equal(t, seed, state(t, id), "a failed response read takes the bump with it")

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

		// The same move with the row rewritten: the link on the new step keeps it, no contradiction.
		res = mustChange(t, id, v1+2, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(40), "F#1", "B#1")},
			Link:   []entity.TechCardSeamLink{link(10, key(40))},
			Detach: []entity.TechCardSeamLink{link(30, key(40))},
		})
		require.Empty(t, res.DeletedKeys)
		require.Equal(t, map[int][]string{10: {key(40)}, 20: {key(43)}, 30: {}}, state(t, id).Links)
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
			{"link and detach of one row once the rekey resolved it", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{samePair},
				Link:   []entity.TechCardSeamLink{link(10, key(50))},
				Detach: []entity.TechCardSeamLink{link(10, key(52))},
			}, "detach[0] contradicts_link"},
			{"a row written and its last link detached", entity.TechCardSeamsChange{
				Upsert: []entity.TechCardSeamInput{stored},
				Detach: []entity.TechCardSeamLink{link(10, key(50))},
			}, "detach[0] contradicts_upsert"},
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

	t.Run("rows of one pair and ranges in one request are one row", func(t *testing.T) {
		id := newCard(t, 10, 20, 30)
		v0 := state(t, id).Version
		noted := func(s entity.TechCardSeamInput, note string) entity.TechCardSeamInput { s.Note = note; return s }
		noteOf := func(seams []entity.TechCardSeam, k string) string {
			for _, s := range seams {
				if s.SeamKey == k {
					return s.Note
				}
			}
			return "<no row>"
		}

		// Two new rows of one pair on a card without it: the second lands on the first, its link follows.
		res := mustChange(t, id, v0, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(90), "F#1", "B#1"), noted(edge(key(91), "F#1", "B#1"), "second")},
			Link:   []entity.TechCardSeamLink{link(10, key(91))},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(90), Stored: key(90)}, {Sent: key(91), Stored: key(90)}}, res.KeyMap)
		require.Equal(t, "", noteOf(res.Seams, key(90)), "the first row of the pair keeps its content")
		require.Equal(t, cardState{Version: v0 + 1, Seams: []string{key(90)},
			Links: map[int][]string{10: {key(90)}, 20: {}, 30: {}}}, state(t, id))

		// A stored row sent again stays the target of a new row of its pair, wherever the new row
		// stands in the request; the row sent under its own key keeps its own content.
		res = mustChange(t, id, v0+1, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{noted(edge(key(92), "F#1", "B#1"), "new"), noted(edge(key(90), "F#1", "B#1"), "own")},
			Link:   []entity.TechCardSeamLink{link(20, key(92))},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(92), Stored: key(90)}, {Sent: key(90), Stored: key(90)}}, res.KeyMap)
		require.Equal(t, "own", noteOf(res.Seams, key(90)))
		require.Equal(t, cardState{Version: v0 + 2, Seams: []string{key(90)},
			Links: map[int][]string{10: {key(90)}, 20: {key(90)}, 30: {}}}, state(t, id))

		// Two new rows of a stored pair both land on it.
		res = mustChange(t, id, v0+2, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(93), "F#1", "B#1"), edge(key(94), "F#1", "B#1")},
			Link:   []entity.TechCardSeamLink{link(30, key(94))},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(93), Stored: key(90)}, {Sent: key(94), Stored: key(90)}}, res.KeyMap)
		require.Equal(t, cardState{Version: v0 + 3, Seams: []string{key(90)},
			Links: map[int][]string{10: {key(90)}, 20: {key(90)}, 30: {key(90)}}}, state(t, id))

		// A stored row rewritten to another pair in the same request no longer holds its old pair.
		res = mustChange(t, id, v0+3, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(90), "F#9", "B#9"), edge(key(95), "F#1", "B#1")},
		})
		require.Equal(t, []entity.TechCardSeamKeyMap{{Sent: key(90), Stored: key(90)}, {Sent: key(95), Stored: key(95)}}, res.KeyMap)
		require.Equal(t, sorted(key(90), key(95)), state(t, id).Seams)
	})

	t.Run("the caps hold on the state after the whole change", func(t *testing.T) {
		// 400 rows per card: a MIRROR at the cap (a new row in, the replaced row out) lands; a change
		// that ends one row over does not, and writes nothing.
		id := newCard(t, 10, 20)
		const rowCap = entity.TechCardSeamMaxRowsPerCard
		full := make([]entity.TechCardSeamInput, 0, rowCap)
		for i := 0; i < rowCap; i++ {
			full = append(full, edge(key(1000+i), fmt.Sprintf("F#%d", i), "B#1"))
		}
		mustChange(t, id, state(t, id).Version, entity.TechCardSeamsChange{
			Upsert: full,
			Link:   []entity.TechCardSeamLink{link(10, key(1000)), link(10, key(1001)), link(20, key(1001))},
		})
		seed := state(t, id)
		require.Len(t, seed.Seams, rowCap)
		res := mustChange(t, id, seed.Version, entity.TechCardSeamsChange{
			Upsert: []entity.TechCardSeamInput{edge(key(2000), "F#new", "B#new")},
			Link:   []entity.TechCardSeamLink{link(10, key(2000))},
			Detach: []entity.TechCardSeamLink{link(10, key(1000))},
		})
		require.Equal(t, []string{key(1000)}, res.DeletedKeys)
		require.Len(t, res.Seams, rowCap, "the replaced row went: the card ends at the cap")
		atCap := state(t, id)
		for name, c := range map[string]entity.TechCardSeamsChange{
			"one row more": {Upsert: []entity.TechCardSeamInput{edge(key(2001), "F#more", "B#more")}},
			// key(1001) keeps its link on step 20, so it stays: still one row over.
			"a detach that deletes nothing": {
				Upsert: []entity.TechCardSeamInput{edge(key(2001), "F#more", "B#more")},
				Detach: []entity.TechCardSeamLink{link(10, key(1001))},
			},
		} {
			_, err := change(id, atCap.Version, c)
			require.Equal(t, "upsert too_many", violation(t, err), name)
			require.Equal(t, atCap, state(t, id), "%s: nothing written", name)
		}

		// 64 links per step: replacing one link on a full step lands; a 65th link does not.
		id = newCard(t, 10, 20)
		const linkCap = entity.TechCardOperationMaxSeamKeys
		rows := make([]entity.TechCardSeamInput, 0, linkCap+2)
		links := make([]entity.TechCardSeamLink, 0, linkCap)
		for i := 0; i < linkCap+2; i++ {
			rows = append(rows, edge(key(3000+i), fmt.Sprintf("F#%d", i), "B#2"))
			if i < linkCap {
				links = append(links, link(20, key(3000+i)))
			}
		}
		mustChange(t, id, state(t, id).Version, entity.TechCardSeamsChange{Upsert: rows, Link: links})
		seed = state(t, id)
		require.Len(t, seed.Links[20], linkCap)
		mustChange(t, id, seed.Version, entity.TechCardSeamsChange{
			Link:   []entity.TechCardSeamLink{link(20, key(3000+linkCap))},
			Detach: []entity.TechCardSeamLink{link(20, key(3000))},
		})
		atCap = state(t, id)
		require.Len(t, atCap.Links[20], linkCap)
		require.Equal(t, key(3000+linkCap), atCap.Links[20][linkCap-1], "the new link is appended")
		_, err := change(id, atCap.Version, entity.TechCardSeamsChange{Link: []entity.TechCardSeamLink{
			link(10, key(3000+linkCap+1)), link(20, key(3000+linkCap+1)),
		}})
		require.Equal(t, "link[1] too_many", violation(t, err))
		require.Equal(t, atCap, state(t, id), "nothing written")
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

// seamFault is where faultyCards' transaction fails: at the failWrite-th write statement (1-based),
// at the first read once failReadAfter writes ran, or — atCommit — after the whole change ran, in
// place of its commit. writes counts the write statements of the last attempt.
type seamFault struct {
	err           error
	failWrite     int
	failReadAfter int
	atCommit      bool
	writes        int
}

// seamFaultDB is the transaction's own handle with seamFault applied. Every store write goes through
// ExecContext and every list read through QueryxContext (storeutil), so the count needs no SQL text.
type seamFaultDB struct {
	dependency.DB
	f *seamFault
}

func (d seamFaultDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.f.writes++
	if d.f.writes == d.f.failWrite {
		return nil, d.f.err
	}
	return d.DB.ExecContext(ctx, query, args...)
}

func (d seamFaultDB) QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error) {
	if d.f.failReadAfter > 0 && d.f.writes >= d.f.failReadAfter {
		return nil, d.f.err
	}
	return d.DB.QueryxContext(ctx, query, args...)
}

type seamFaultRepo struct {
	dependency.Repository
	db dependency.DB
}

func (r seamFaultRepo) DB() dependency.DB { return r.db }

// faultyCards is the card store over st whose write transaction fails where f says — the fault is
// the test's, through the store's public constructor; production code carries no hook for it.
func faultyCards(st *MYSQLStore, f *seamFault) *techcard.Store {
	tx := func(ctx context.Context, fn func(context.Context, dependency.Repository) error) error {
		return st.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			f.writes = 0 // per attempt: Tx may re-run the closure
			if err := fn(ctx, seamFaultRepo{Repository: rep, db: seamFaultDB{DB: rep.DB(), f: f}}); err != nil {
				return err
			}
			if f.atCommit {
				return f.err
			}
			return nil
		})
	}
	return techcard.New(storeutil.Base{DB: st.db, Now: st.Now}, tx, st.readTx, func() dependency.Repository { return st })
}
