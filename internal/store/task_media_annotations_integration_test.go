package store

import (
	"context"
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

// dec builds the wire form of a normalized annotation coordinate.
func annDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

// TestTaskMediaAnnotations covers the whole path a drawn instruction travels on a kanban card
// (0313): wire → dto → task_media.annotations → back out through both read paths.
//
// It is driven through dto.ConvertPbTaskInsertToEntity rather than a hand-built entity, because
// three of the four rules under test are conversion rules and only the conversion can break them:
// a set whose image is not attached is dropped silently, the cut-piece keys are cleared (a card has
// no pieces), and the shared techcard validator is what accepts the figures at all.
//
// Both read paths are asserted, not one. The card editor opens straight off the board, so if
// ListTasks omitted the annotations, the very next save would write "no annotations" — and the
// contract is full replacement, so it would erase them.
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
	// mediaDetached is a real image that is NOT attached to the card: the client legitimately
	// posts the annotations it read together with the media list it just edited.
	mediaDetached := insertTestMedia(t, "taskann-detached-"+suffix)

	var taskID int
	defer func() {
		bg := context.Background()
		if taskID > 0 {
			// task_media/task_label cascade with the card.
			_, _ = testDB.ExecContext(bg, `DELETE FROM task WHERE id = ?`, taskID)
		}
		for _, id := range []int{mediaA, mediaB, mediaDetached} {
			_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, id)
		}
	}()

	// A well-formed 26-character cut-piece key: it must survive the shared validator so the test
	// can prove the server CLEARS it rather than merely rejecting it.
	const pieceKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	require.Len(t, pieceKey, 26)

	payload := &pb_common.TaskInsert{
		Title:    "указания на снимках " + suffix,
		MediaIds: []int32{int32(mediaA), int32(mediaB)},
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
				// Not among media_ids: must vanish without failing the save.
				MediaId: int32(mediaDetached),
				Annotations: []*pb_common.TechCardAnnotation{
					{
						Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
						Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5"), Y: annDec("0.5")}},
						Text:   "снятая картинка",
					},
				},
			},
		},
	}

	ti, err := dto.ConvertPbTaskInsertToEntity(payload)
	require.NoError(t, err, "a set on a detached image must be dropped, not rejected")
	require.Len(t, ti.MediaAnnotations, 2, "only sets whose image is attached survive conversion")

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
	require.Len(t, got.MediaAnnotations, 2, "both annotated images must come back")

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

	// The detached set was dropped on the way in, so nothing points at that image.
	for _, set := range got.MediaAnnotations {
		assert.NotEqual(t, mediaDetached, set.MediaId, "a set on a detached image must never be stored")
	}

	// The wire form the client reads back must carry the annotations on the card's CONTENT: it is
	// the same message the next save posts, and what it does not read, it erases.
	pb := dto.ConvertEntityTaskToPb(got)
	require.NotNil(t, pb.Task)
	require.Len(t, pb.Task.MediaAnnotations, 2)
	assert.Equal(t, int32(mediaA), pb.Task.MediaAnnotations[0].MediaId)
	require.Len(t, pb.Task.MediaAnnotations[0].Annotations, 2)
	assert.Equal(t, "тут шов кривой", pb.Task.MediaAnnotations[0].Annotations[0].Text)
	assert.Empty(t, pb.Task.MediaAnnotations[0].Annotations[1].PieceLineKeys)

	// --- the board list must carry them too -------------------------------------------------
	listed, _, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{Board: entity.TaskBoardDevelopment})
	require.NoError(t, err)
	var fromList *entity.Task
	for i := range listed {
		if listed[i].Id == taskID {
			fromList = &listed[i]
			break
		}
	}
	require.NotNil(t, fromList, "the card must appear on its board")
	require.Len(t, fromList.MediaAnnotations, 2,
		"the editor opens off the board: without annotations here the next save would erase them")
	assert.Equal(t, mediaA, fromList.MediaAnnotations[0].MediaId)
	require.Len(t, fromList.MediaAnnotations[0].Annotations, 2)
	assert.Equal(t, "отсюда досюда", fromList.MediaAnnotations[0].Annotations[1].Text)

	// --- a row written before 0313 reads as "nothing was drawn" ------------------------------
	_, err = testDB.ExecContext(ctx,
		`UPDATE task_media SET annotations = NULL WHERE task_id = ? AND media_id = ?`, taskID, mediaB)
	require.NoError(t, err)
	legacy, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, legacy.MediaAnnotations, 1, "a NULL column is an image nobody drew on, not a broken read")
	assert.Equal(t, mediaA, legacy.MediaAnnotations[0].MediaId)
	assert.Len(t, legacy.Media, 2, "the image itself is still attached")

	// --- an update WITHOUT annotations erases them (full replacement is the contract) --------
	bare, err := dto.ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
		Title:    "указания сняты " + suffix,
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

// TestTaskMediaAnnotationsRejections locks the two payloads that are a client BUG rather than a
// stale read, and must not be swallowed: an id that names no image, and two sets fighting over one.
func TestTaskMediaAnnotationsRejections(t *testing.T) {
	pin := []*pb_common.TechCardAnnotation{{
		Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
		Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5"), Y: annDec("0.5")}},
	}}

	cases := map[string]*pb_common.TaskInsert{
		"media_id 0 is a malformed payload, not a stale one": {
			Title:            "x",
			MediaIds:         []int32{7},
			MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 0, Annotations: pin}},
		},
		"two sets on one image are ambiguous": {
			Title:    "x",
			MediaIds: []int32{7},
			MediaAnnotations: []*pb_common.TaskMediaAnnotations{
				{MediaId: 7, Annotations: pin},
				{MediaId: 7, Annotations: pin},
			},
		},
		"a point outside the frame points at nothing": {
			Title:    "x",
			MediaIds: []int32{7},
			MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 7, Annotations: []*pb_common.TechCardAnnotation{{
				Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
				Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("1.5"), Y: annDec("0.5")}},
			}}}},
		},
		"a measurement needs two points": {
			Title:    "x",
			MediaIds: []int32{7},
			MediaAnnotations: []*pb_common.TaskMediaAnnotations{{MediaId: 7, Annotations: []*pb_common.TechCardAnnotation{{
				Kind:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
				Points: []*pb_common.TechCardAnnotationPoint{{X: annDec("0.5"), Y: annDec("0.5")}},
			}}}},
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := dto.ConvertPbTaskInsertToEntity(in)
			assert.Error(t, err)
		})
	}
}
