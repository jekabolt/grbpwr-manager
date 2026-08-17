package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// annDec builds the wire form of a normalized annotation coordinate.
func annDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

// annPin is the smallest well-formed annotation, used wherever the figure itself is not the point.
func annPin() []*pb_common.TechCardAnnotation {
	return []*pb_common.TechCardAnnotation{{
		Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
		Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5"), Y: annDec("0.5")}},
	}}
}

// TestTaskMediaAnnotations covers the whole path a drawn instruction travels on a kanban card
// (0313): wire → dto → task_media.annotations → back out through both read paths → and, crucially,
// straight back in again.
//
// It is driven through dto.ConvertPbTaskInsertToEntity rather than a hand-built entity, because
// the conversion is where the card-specific rules live (cut-piece keys cleared, detached sets
// dropped) and where the shared techcard validator decides the figures are drawable at all.
//
// Both read paths are asserted. Not because the board draws annotations — the card is edited on its
// detail page, which reads GetTask — but because Task.task is ONE projection of the content, and a
// field that silently disappears from one of the two reads is exactly how "the editor saved the
// card without it" happens.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates). Cleans up every
// row it inserts, in FK order.
func TestTaskMediaAnnotations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mediaA := insertTestMedia(t, "taskann-a-"+suffix)
	mediaB := insertTestMedia(t, "taskann-b-"+suffix)
	mediaC := insertTestMedia(t, "taskann-c-"+suffix)
	// mediaDetached is a real image that is NOT attached to the card: the client legitimately
	// posts the annotations it read together with the media list it just edited.
	mediaDetached := insertTestMedia(t, "taskann-detached-"+suffix)

	// A unique assignee pins the card into its own single-row list. Without it ListTasks would be
	// asked for a whole board, and on a seeded database the card — appended to the END of its
	// column — falls outside the default 200-row page, failing for a reason unrelated to
	// annotations.
	assignee := "taskann-" + suffix

	var taskID int
	defer func() {
		bg := context.Background()
		if taskID > 0 {
			// task_media / task_label cascade with the card.
			_, _ = testDB.ExecContext(bg, `DELETE FROM task WHERE id = ?`, taskID)
		}
		for _, id := range []int{mediaA, mediaB, mediaC, mediaDetached} {
			_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, id)
		}
	}()

	// A well-formed 26-character cut-piece key: it must survive the shared validator so the test
	// proves the server CLEARS it rather than merely rejecting it.
	const pieceKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	require.Len(t, pieceKey, 26)

	payload := &pb_common.TaskInsert{
		Title:    "указания на снимках " + suffix,
		Assignee: assignee,
		MediaIds: []int32{int32(mediaA), int32(mediaB), int32(mediaC)},
		MediaAnnotations: []*pb_common.TaskMediaAnnotations{
			{
				MediaId: int32(mediaA),
				Annotations: []*pb_common.TechCardAnnotation{
					{
						Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
						Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.25"), Y: annDec("0.4")}},
						Text:   "тут шов кривой",
						LabelX: annDec("0.3"),
						LabelY: annDec("0.45"),
						Color:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED,
					},
					{
						Kind: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
						Points: []*pb_common.TechCardAnnotationPoint{
							{X: annDec("0.1"), Y: annDec("0.1")},
							{X: annDec("0.9"), Y: annDec("0.15")},
						},
						Text:   "отсюда досюда",
						Color:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE,
						Dashed: true,
						// Deliberately set on a card: a task has no cut pieces, so the server
						// must clear this rather than store a key nobody here can resolve.
						PieceLineKeys: []string{pieceKey},
					},
				},
			},
			{
				MediaId: int32(mediaB),
				Annotations: []*pb_common.TechCardAnnotation{
					{
						Kind: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
						Points: []*pb_common.TechCardAnnotationPoint{
							{X: annDec("0.2"), Y: annDec("0.2")},
							{X: annDec("0.8"), Y: annDec("0.2")},
							{X: annDec("0.5"), Y: annDec("0.7")},
						},
						Text:   "эта зона",
						Color:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_GREEN,
						Filled: true,
					},
				},
			},
			{
				// An attached image with an EMPTY set. A client legitimately sends this — it
				// posts what it read for every image — and it must be accepted and stored as [],
				// which means the reply carries fewer sets than the request. That asymmetry is
				// the declared behaviour, not a bug, so it is pinned here.
				MediaId:     int32(mediaC),
				Annotations: nil,
			},
			{
				// Not among media_ids: must vanish without failing the save.
				MediaId:     int32(mediaDetached),
				Annotations: annPin(),
			},
		},
	}

	ti, err := dto.ConvertPbTaskInsertToEntity(payload)
	require.NoError(t, err, "a set on a detached image must be dropped, not rejected")

	// The drop is only observable HERE: the store writes annotations while iterating media_ids, so
	// a set on an unattached image has no row to land in and could not be stored even in principle.
	// Asserting it after a round trip would be theatre.
	convertedIDs := make([]int, 0, len(ti.MediaAnnotations))
	for _, set := range ti.MediaAnnotations {
		convertedIDs = append(convertedIDs, set.MediaId)
	}
	assert.Equal(t, []int{mediaA, mediaB, mediaC}, convertedIDs,
		"the detached image must be gone and the attached ones kept, in order")

	taskID, err = s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: *ti,
		Board:      entity.TaskBoardDevelopment,
		Status:     entity.TaskStatusTodo,
		CreatedBy:  "taskann-test",
	})
	require.NoError(t, err)

	// --- round trip through GetTask ---------------------------------------------------------
	got, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, got.MediaAnnotations, 2,
		"the image drawn on twice and the one drawn on once come back; the empty one does not")

	// Order follows the images' display_order, so the round trip is stable.
	assert.Equal(t, mediaA, got.MediaAnnotations[0].MediaId)
	assert.Equal(t, mediaB, got.MediaAnnotations[1].MediaId)

	first := got.MediaAnnotations[0].Annotations
	require.Len(t, first, 2)

	assert.Equal(t, entity.AnnotationKindPin, first[0].Kind)
	require.Len(t, first[0].Points, 1)
	assert.Equal(t, "0.25", first[0].Points[0].X.String())
	assert.Equal(t, "0.4", first[0].Points[0].Y.String())
	assert.Equal(t, "тут шов кривой", first[0].Text)
	assert.Equal(t, entity.AnnotationColorRed, first[0].Color)
	assert.False(t, first[0].Dashed)

	assert.Equal(t, entity.AnnotationKindDim, first[1].Kind)
	require.Len(t, first[1].Points, 2)
	assert.Equal(t, "0.9", first[1].Points[1].X.String())
	assert.Equal(t, entity.AnnotationColorBlue, first[1].Color)
	assert.True(t, first[1].Dashed, "a dashed measurement must stay dashed: on a drawing the two say different things")
	assert.Empty(t, first[1].PieceLineKey, "a card has no cut pieces — the key must be cleared, not stored")
	assert.Empty(t, first[1].PieceLineKeys)

	second := got.MediaAnnotations[1].Annotations
	require.Len(t, second, 1)
	assert.Equal(t, entity.AnnotationKindPolygon, second[0].Kind)
	require.Len(t, second[0].Points, 3)
	assert.True(t, second[0].Filled, "a hatched area must stay hatched")
	assert.Equal(t, entity.AnnotationColorGreen, second[0].Color)

	// The empty set was stored as [] on its own row — not NULL, and not skipped.
	var rawC []byte
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT annotations FROM task_media WHERE task_id = ? AND media_id = ?`, taskID, mediaC).Scan(&rawC))
	assert.Equal(t, "[]", string(rawC), "an image nobody drew on stores an empty array, not NULL")

	// --- the board list must carry the same projection --------------------------------------
	listed, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: assignee})
	require.NoError(t, err)
	require.Equal(t, 1, total, "the unique assignee must select exactly this card")
	require.Len(t, listed, 1)
	fromList := listed[0]
	require.Equal(t, taskID, fromList.Id)
	require.Len(t, fromList.MediaAnnotations, 2,
		"Task.task is one projection: a field missing from the list read is how a save loses it")
	assert.Equal(t, mediaA, fromList.MediaAnnotations[0].MediaId)
	require.Len(t, fromList.MediaAnnotations[0].Annotations, 2)
	assert.Equal(t, "отсюда досюда", fromList.MediaAnnotations[0].Annotations[1].Text)

	// --- the round trip the contract actually demands of every client ------------------------
	//
	// Read → post straight back → read again. This is what the replace-on-update semantics require
	// of the UI, and it is what exposes any asymmetry between toPb and fromPb: anything the write
	// path cannot accept from the read path's own output erases itself on the next save.
	readBack := dto.ConvertEntityTaskToPb(got)
	require.NotNil(t, readBack.Task)
	require.Len(t, readBack.Task.MediaAnnotations, 2)

	reposted, err := dto.ConvertPbTaskInsertToEntity(readBack.Task)
	require.NoError(t, err, "the server's own output must be accepted back verbatim")
	require.NoError(t, s.Tasks().UpdateTask(ctx, taskID, reposted))

	after, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, len(got.MediaAnnotations), len(after.MediaAnnotations),
		"a save that changed nothing must not lose a set")
	for i := range got.MediaAnnotations {
		before, now := got.MediaAnnotations[i], after.MediaAnnotations[i]
		assert.Equal(t, before.MediaId, now.MediaId)
		require.Equal(t, len(before.Annotations), len(now.Annotations))
		for j := range before.Annotations {
			b, n := before.Annotations[j], now.Annotations[j]
			assert.Equal(t, b.Kind, n.Kind)
			assert.Equal(t, b.Text, n.Text)
			assert.Equal(t, b.Color, n.Color)
			assert.Equal(t, b.Dashed, n.Dashed)
			assert.Equal(t, b.Filled, n.Filled)
			require.Equal(t, len(b.Points), len(n.Points))
			for k := range b.Points {
				assert.True(t, b.Points[k].X.Equal(n.Points[k].X),
					"x drifted on a no-op save: %s -> %s", b.Points[k].X, n.Points[k].X)
				assert.True(t, b.Points[k].Y.Equal(n.Points[k].Y),
					"y drifted on a no-op save: %s -> %s", b.Points[k].Y, n.Points[k].Y)
			}
		}
	}

	// --- a row written before 0313 reads as "nothing was drawn" ------------------------------
	_, err = testDB.ExecContext(ctx,
		`UPDATE task_media SET annotations = NULL WHERE task_id = ? AND media_id = ?`, taskID, mediaB)
	require.NoError(t, err)
	legacy, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, legacy.MediaAnnotations, 1, "a NULL column is an image nobody drew on, not a broken read")
	assert.Equal(t, mediaA, legacy.MediaAnnotations[0].MediaId)
	assert.Len(t, legacy.Media, 3, "the image itself is still attached")

	// --- a duplicate task_media row must not produce an unsavable card -----------------------
	//
	// task_media has no UNIQUE (task_id, media_id) — a retroactive one would halt prod startup —
	// so a second row for one image is reachable (seeder, manual fix, a future writer). The read
	// must collapse it, otherwise the immediate round trip would be a permanent 400.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO task_media (task_id, media_id, display_order, annotations)
		VALUES (?, ?, 0, ?)`, taskID, mediaA, `[{"kind":"pin","points":[{"x":"0.9","y":"0.9"}],"label_x":"0","label_y":"0"}]`)
	require.NoError(t, err)
	dup, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	dupSets := 0
	for _, set := range dup.MediaAnnotations {
		if set.MediaId == mediaA {
			dupSets++
		}
	}
	assert.Equal(t, 1, dupSets, "two rows for one image must read as one set, or the card can never be saved")
	_, err = dto.ConvertPbTaskInsertToEntity(dto.ConvertEntityTaskToPb(dup).Task)
	assert.NoError(t, err, "a card with a duplicated media row must still round-trip")
	_, err = testDB.ExecContext(ctx,
		`DELETE FROM task_media WHERE task_id = ? AND media_id = ? ORDER BY id DESC LIMIT 1`, taskID, mediaA)
	require.NoError(t, err)

	// --- an update WITHOUT annotations erases them (full replacement is the contract) --------
	bare, err := dto.ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
		Title:    "указания сняты " + suffix,
		Assignee: assignee,
		MediaIds: []int32{int32(mediaA), int32(mediaB)},
	})
	require.NoError(t, err)
	require.NoError(t, s.Tasks().UpdateTask(ctx, taskID, bare))

	cleared, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	assert.Empty(t, cleared.MediaAnnotations,
		"an insert without annotations means there are no annotations — same rule as media_ids and labels")
	assert.Len(t, cleared.Media, 2, "clearing the drawings must not detach the images")

	// And the erasure is written as an empty array, not NULL: two ways to say one thing would
	// force every reader to tell them apart (the 0308 argument).
	var raw []byte
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT annotations FROM task_media WHERE task_id = ? AND media_id = ?`, taskID, mediaA).Scan(&raw))
	assert.Equal(t, "[]", string(raw))
}

// requireViolation asserts that err is a field violation naming the expected field path and reason.
// A bare assert.Error would stay green if some unrelated check added EARLIER in the conversion
// started rejecting the fixture first, leaving the rule named in the test unverified.
func requireViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	require.Error(t, err)
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "expected a field violation, got %T: %v", err, err)
	assert.Equal(t, field, ve.Field)
	assert.Equal(t, reason, ve.Reason)
}

// TestTaskMediaAnnotationsRejections locks the payloads that are a client BUG rather than a stale
// read, and pins WHICH rule rejected each one.
func TestTaskMediaAnnotationsRejections(t *testing.T) {
	cases := map[string]struct {
		in            *pb_common.TaskInsert
		field, reason string
	}{
		"media_id 0 is a malformed payload, not a stale one": {
			in: &pb_common.TaskInsert{
				Title:            "x",
				MediaIds:         []int32{7},
				MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 0, Annotations: annPin()}},
			},
			field:  "media_annotations[0].media_id",
			reason: "required",
		},
		"a point outside the frame points at nothing": {
			in: &pb_common.TaskInsert{
				Title:    "x",
				MediaIds: []int32{7},
				MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 7, Annotations: []*pb_common.TechCardAnnotation{{
					Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
					Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("1.5"), Y: annDec("0.5")}},
				}}}},
			},
			field:  "media_annotations[0].annotations[0].points[0].x",
			reason: "out_of_frame",
		},
		"a measurement needs two points": {
			in: &pb_common.TaskInsert{
				Title:    "x",
				MediaIds: []int32{7},
				MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 7, Annotations: []*pb_common.TechCardAnnotation{{
					Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
					Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5"), Y: annDec("0.5")}},
				}}}},
			},
			field:  "media_annotations[0].annotations[0].points",
			reason: "wrong_count",
		},
		"an exponent-encoded coordinate is refused before it can be compared": {
			in: &pb_common.TaskInsert{
				Title:    "x",
				MediaIds: []int32{7},
				MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 7, Annotations: []*pb_common.TechCardAnnotation{{
					Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
					Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5e-500000"), Y: annDec("0.5")}},
				}}}},
			},
			field:  "media_annotations[0].annotations[0].points[0].x",
			reason: "too_precise",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := dto.ConvertPbTaskInsertToEntity(c.in)
			requireViolation(t, err, c.field, c.reason)
		})
	}
}

// TestTaskMediaAnnotationsDuplicateSetIsForgiven pins the OTHER half of the duplicate rule: two
// sets naming one image are collapsed silently, first wins, exactly like labels/media_ids/file_ids
// of the same conversion. Rejecting instead would make a card with a duplicated task_media row
// permanently unsavable — see the store-side collapse.
func TestTaskMediaAnnotationsDuplicateSetIsForgiven(t *testing.T) {
	loud := []*pb_common.TechCardAnnotation{{
		Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
		Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.1"), Y: annDec("0.1")}},
		Text:   "первый",
	}}

	// Attached: both sets name an image on the card.
	ti, err := dto.ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
		Title:    "x",
		MediaIds: []int32{7},
		MediaAnnotations: []*pb_common.TaskMediaAnnotations{
			{MediaId: 7, Annotations: loud},
			{MediaId: 7, Annotations: annPin()},
		},
	})
	require.NoError(t, err)
	require.Len(t, ti.MediaAnnotations, 1, "two sets on one image collapse to one")
	require.Len(t, ti.MediaAnnotations[0].Annotations, 1)
	assert.Equal(t, "первый", ti.MediaAnnotations[0].Annotations[0].Text, "the first set wins")

	// Detached: the same shape must have the same outcome. Before the fix the dedupe mark was set
	// after the attachment check, so an identical payload passed or failed depending on a field
	// that is not in the set at all.
	detached, err := dto.ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
		Title:    "x",
		MediaIds: []int32{7},
		MediaAnnotations: []*pb_common.TaskMediaAnnotations{
			{MediaId: 99, Annotations: loud},
			{MediaId: 99, Annotations: annPin()},
		},
	})
	require.NoError(t, err, "the same shape must not depend on whether the image is attached")
	assert.Empty(t, detached.MediaAnnotations)

	// And media_ids itself already forgave the duplicate, so the two halves now agree.
	both, err := dto.ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
		Title:            "x",
		MediaIds:         []int32{5, 5},
		MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 5}, {MediaId: 5}},
	})
	require.NoError(t, err)
	assert.Equal(t, []int{5}, both.MediaIds)
}
