package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConvertPbTaskInsertToEntity(t *testing.T) {
	due := timestamppb.New(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	valid := &pb_common.TaskInsert{
		Title:       "  Sew the sample  ",
		Description: "cut + assemble",
		Assignee:    "olya",
		Priority:    pb_common.TaskPriority_TASK_PRIORITY_HIGH,
		DueDate:     due,
		Labels:      []string{"urgent", "urgent", " sample ", ""},
		MediaIds:    []int32{11, 12, 11},
		TechCardId:  8,
		ProductId:   0,
		OrderUuid:   "  ",
		ArchiveId:   3,
	}

	got, err := ConvertPbTaskInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Sew the sample" {
		t.Errorf("title not trimmed: %q", got.Title)
	}
	if got.Assignee != "olya" || got.Priority != entity.TaskPriorityHigh {
		t.Errorf("assignee/priority mismatch: %+v", got)
	}
	if !got.DueDate.Valid || !got.DueDate.Time.Equal(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("due date mismatch: %+v", got.DueDate)
	}
	if !got.TechCardId.Valid || got.TechCardId.Int32 != 8 || got.ProductId.Valid || !got.ArchiveId.Valid {
		t.Errorf("deep-link ids mismatch: %+v", got)
	}
	if got.OrderUuid.Valid {
		t.Errorf("blank order_uuid should be NULL, got %+v", got.OrderUuid)
	}
	// labels: trimmed, de-duped, empties dropped -> ["urgent","sample"]
	if len(got.Labels) != 2 || got.Labels[0] != "urgent" || got.Labels[1] != "sample" {
		t.Errorf("labels mismatch: %+v", got.Labels)
	}
	// media: de-duped -> [11,12]
	if len(got.MediaIds) != 2 || got.MediaIds[0] != 11 || got.MediaIds[1] != 12 {
		t.Errorf("media ids mismatch: %+v", got.MediaIds)
	}

	// priority defaults to unknown when unset
	def, err := ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{Title: "x"})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if def.Priority != entity.TaskPriorityUnknown {
		t.Errorf("priority default mismatch: %v", def.Priority)
	}
	if def.DueDate.Valid {
		t.Errorf("unset due date should be NULL")
	}

	bad := []struct {
		name string
		in   *pb_common.TaskInsert
	}{
		{"nil", nil},
		{"empty title", &pb_common.TaskInsert{Title: "   "}},
		{"long title", &pb_common.TaskInsert{Title: string(make([]byte, 256))}},
		{"negative tech_card", &pb_common.TaskInsert{Title: "x", TechCardId: -1}},
		{"negative product", &pb_common.TaskInsert{Title: "x", ProductId: -2}},
		{"zero media id", &pb_common.TaskInsert{Title: "x", MediaIds: []int32{0}}},
		{"long label", &pb_common.TaskInsert{Title: "x", Labels: []string{string(make([]byte, 65))}}},
		{"long order_uuid", &pb_common.TaskInsert{Title: "x", OrderUuid: string(make([]byte, 37))}},
	}
	for _, c := range bad {
		if _, err := ConvertPbTaskInsertToEntity(c.in); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

func TestConvertEntityTaskToPb(t *testing.T) {
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	e := &entity.Task{
		Id: 5,
		TaskInsert: entity.TaskInsert{
			Title:      "Shoot the drop",
			Priority:   entity.TaskPriorityMedium,
			Labels:     []string{"content"},
			TechCardId: nullInt32FromPb(2),
		},
		Board:     entity.TaskBoardContent,
		Status:    entity.TaskStatusInProgress,
		Position:  3,
		CreatedBy: "max",
		CreatedAt: now,
		UpdatedAt: now,
		Media: []entity.MediaFull{
			{Id: 9, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://x/9.jpg"}},
		},
	}
	pb := ConvertEntityTaskToPb(e)
	if pb.Id != 5 || pb.Board != pb_common.TaskBoard_TASK_BOARD_CONTENT ||
		pb.Status != pb_common.TaskStatus_TASK_STATUS_IN_PROGRESS || pb.Position != 3 || pb.CreatedBy != "max" {
		t.Errorf("placement mismatch: %+v", pb)
	}
	if pb.Task.Title != "Shoot the drop" || pb.Task.Priority != pb_common.TaskPriority_TASK_PRIORITY_MEDIUM {
		t.Errorf("content mismatch: %+v", pb.Task)
	}
	if len(pb.Media) != 1 || pb.Media[0].Id != 9 || len(pb.Task.MediaIds) != 1 || pb.Task.MediaIds[0] != 9 {
		t.Errorf("media mismatch: media=%+v ids=%+v", pb.Media, pb.Task.MediaIds)
	}
	if len(pb.Task.Labels) != 1 || pb.Task.Labels[0] != "content" {
		t.Errorf("labels mismatch: %+v", pb.Task.Labels)
	}
}

func TestConvertPbTaskBoardStatus(t *testing.T) {
	if b, err := ConvertPbTaskBoardToEntity(pb_common.TaskBoard_TASK_BOARD_PRODUCTION); err != nil || b != entity.TaskBoardProduction {
		t.Errorf("board convert: %v %v", b, err)
	}
	if _, err := ConvertPbTaskBoardToEntity(pb_common.TaskBoard_TASK_BOARD_UNKNOWN); err == nil {
		t.Errorf("unknown board should error")
	}
	if s, err := ConvertPbTaskStatusToEntity(pb_common.TaskStatus_TASK_STATUS_REVIEW); err != nil || s != entity.TaskStatusReview {
		t.Errorf("status convert: %v %v", s, err)
	}
	if _, err := ConvertPbTaskStatusToEntity(pb_common.TaskStatus_TASK_STATUS_UNKNOWN); err == nil {
		t.Errorf("unknown status should error")
	}
}

func TestConvertPbTaskCommentInsertToEntity(t *testing.T) {
	got, err := ConvertPbTaskCommentInsertToEntity(&pb_common.TaskCommentInsert{TaskId: 4, Body: "  looks good  "})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.TaskId != 4 || got.Body != "looks good" {
		t.Errorf("comment mismatch: %+v", got)
	}
	for _, in := range []*pb_common.TaskCommentInsert{
		nil,
		{TaskId: 0, Body: "x"},
		{TaskId: 1, Body: "   "},
	} {
		if _, err := ConvertPbTaskCommentInsertToEntity(in); err == nil {
			t.Errorf("expected error for %+v", in)
		}
	}
}
