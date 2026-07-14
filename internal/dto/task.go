package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxTaskText bounds TEXT inputs (description, comment body) so over-length input
// fails as InvalidArgument rather than a MySQL 1406 (data too long) Internal error.
const maxTaskText = 60000

// maxTaskLabel bounds a single label (VARCHAR(64) in task_label).
const maxTaskLabel = 64

// maxOrderUUID bounds the order_uuid deep-link column (VARCHAR(36)).
const maxOrderUUID = 36

var taskBoardPbToEntity = map[pb_common.TaskBoard]entity.TaskBoard{
	pb_common.TaskBoard_TASK_BOARD_DEVELOPMENT: entity.TaskBoardDevelopment,
	pb_common.TaskBoard_TASK_BOARD_DESIGN:      entity.TaskBoardDesign,
	pb_common.TaskBoard_TASK_BOARD_MARKETING:   entity.TaskBoardMarketing,
	pb_common.TaskBoard_TASK_BOARD_PRODUCTION:  entity.TaskBoardProduction,
	pb_common.TaskBoard_TASK_BOARD_SOURCING:    entity.TaskBoardSourcing,
	pb_common.TaskBoard_TASK_BOARD_CONTENT:     entity.TaskBoardContent,
}

var taskBoardEntityToPb = map[entity.TaskBoard]pb_common.TaskBoard{
	entity.TaskBoardDevelopment: pb_common.TaskBoard_TASK_BOARD_DEVELOPMENT,
	entity.TaskBoardDesign:      pb_common.TaskBoard_TASK_BOARD_DESIGN,
	entity.TaskBoardMarketing:   pb_common.TaskBoard_TASK_BOARD_MARKETING,
	entity.TaskBoardProduction:  pb_common.TaskBoard_TASK_BOARD_PRODUCTION,
	entity.TaskBoardSourcing:    pb_common.TaskBoard_TASK_BOARD_SOURCING,
	entity.TaskBoardContent:     pb_common.TaskBoard_TASK_BOARD_CONTENT,
}

var taskStatusPbToEntity = map[pb_common.TaskStatus]entity.TaskStatus{
	pb_common.TaskStatus_TASK_STATUS_BACKLOG:     entity.TaskStatusBacklog,
	pb_common.TaskStatus_TASK_STATUS_TODO:        entity.TaskStatusTodo,
	pb_common.TaskStatus_TASK_STATUS_IN_PROGRESS: entity.TaskStatusInProgress,
	pb_common.TaskStatus_TASK_STATUS_REVIEW:      entity.TaskStatusReview,
	pb_common.TaskStatus_TASK_STATUS_DONE:        entity.TaskStatusDone,
}

var taskStatusEntityToPb = map[entity.TaskStatus]pb_common.TaskStatus{
	entity.TaskStatusBacklog:    pb_common.TaskStatus_TASK_STATUS_BACKLOG,
	entity.TaskStatusTodo:       pb_common.TaskStatus_TASK_STATUS_TODO,
	entity.TaskStatusInProgress: pb_common.TaskStatus_TASK_STATUS_IN_PROGRESS,
	entity.TaskStatusReview:     pb_common.TaskStatus_TASK_STATUS_REVIEW,
	entity.TaskStatusDone:       pb_common.TaskStatus_TASK_STATUS_DONE,
}

var taskPriorityPbToEntity = map[pb_common.TaskPriority]entity.TaskPriority{
	pb_common.TaskPriority_TASK_PRIORITY_LOW:    entity.TaskPriorityLow,
	pb_common.TaskPriority_TASK_PRIORITY_MEDIUM: entity.TaskPriorityMedium,
	pb_common.TaskPriority_TASK_PRIORITY_HIGH:   entity.TaskPriorityHigh,
	pb_common.TaskPriority_TASK_PRIORITY_URGENT: entity.TaskPriorityUrgent,
}

var taskPriorityEntityToPb = map[entity.TaskPriority]pb_common.TaskPriority{
	entity.TaskPriorityUnknown: pb_common.TaskPriority_TASK_PRIORITY_UNKNOWN,
	entity.TaskPriorityLow:     pb_common.TaskPriority_TASK_PRIORITY_LOW,
	entity.TaskPriorityMedium:  pb_common.TaskPriority_TASK_PRIORITY_MEDIUM,
	entity.TaskPriorityHigh:    pb_common.TaskPriority_TASK_PRIORITY_HIGH,
	entity.TaskPriorityUrgent:  pb_common.TaskPriority_TASK_PRIORITY_URGENT,
}

// ConvertPbTaskBoardToEntity maps a proto board to entity, erroring on UNKNOWN or
// any unmapped value. Callers that treat UNKNOWN as "keep current" must pre-check.
func ConvertPbTaskBoardToEntity(b pb_common.TaskBoard) (entity.TaskBoard, error) {
	v, ok := taskBoardPbToEntity[b]
	if !ok {
		return "", fmt.Errorf("unknown or unset task board: %v", b)
	}
	return v, nil
}

// ConvertPbTaskStatusToEntity maps a proto status to entity, erroring on UNKNOWN or
// any unmapped value. Callers that treat UNKNOWN as a default must pre-check.
func ConvertPbTaskStatusToEntity(s pb_common.TaskStatus) (entity.TaskStatus, error) {
	v, ok := taskStatusPbToEntity[s]
	if !ok {
		return "", fmt.Errorf("unknown or unset task status: %v", s)
	}
	return v, nil
}

// ConvertPbTaskInsertToEntity converts task CONTENT (no placement) to entity,
// validating lengths, non-negative deep-link ids, and de-duping labels/media.
func ConvertPbTaskInsertToEntity(pb *pb_common.TaskInsert) (*entity.TaskInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("task insert is nil")
	}

	title := strings.TrimSpace(pb.Title)
	if title == "" {
		return nil, fmt.Errorf("task title is required")
	}
	if len(title) > maxVarchar255 {
		return nil, fmt.Errorf("task title must be at most %d characters", maxVarchar255)
	}
	if len(pb.Description) > maxTaskText {
		return nil, fmt.Errorf("task description must be at most %d characters", maxTaskText)
	}
	if len(pb.Assignee) > maxVarchar255 {
		return nil, fmt.Errorf("task assignee must be at most %d characters", maxVarchar255)
	}
	if pb.TechCardId < 0 || pb.ProductId < 0 || pb.ArchiveId < 0 || pb.FittingId < 0 || pb.ProductionRunId < 0 {
		return nil, fmt.Errorf("task deep-link ids must not be negative")
	}
	orderUUID := strings.TrimSpace(pb.OrderUuid)
	if len(orderUUID) > maxOrderUUID {
		return nil, fmt.Errorf("task order_uuid must be at most %d characters", maxOrderUUID)
	}

	// Priority defaults to unknown when unset; reject any other unmapped value.
	priority := entity.TaskPriorityUnknown
	if pb.Priority != pb_common.TaskPriority_TASK_PRIORITY_UNKNOWN {
		p, ok := taskPriorityPbToEntity[pb.Priority]
		if !ok {
			return nil, fmt.Errorf("unknown task priority: %v", pb.Priority)
		}
		priority = p
	}

	var dueDate sql.NullTime
	if pb.DueDate != nil {
		dueDate = sql.NullTime{Time: pb.DueDate.AsTime().UTC(), Valid: true}
	}
	var startDate sql.NullTime
	if pb.StartDate != nil {
		startDate = sql.NullTime{Time: pb.StartDate.AsTime().UTC(), Valid: true}
	}

	labels := make([]string, 0, len(pb.Labels))
	seenLabel := make(map[string]bool, len(pb.Labels))
	for _, l := range pb.Labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > maxTaskLabel {
			return nil, fmt.Errorf("task label must be at most %d characters", maxTaskLabel)
		}
		if seenLabel[l] {
			continue
		}
		seenLabel[l] = true
		labels = append(labels, l)
	}

	mediaIds := make([]int, 0, len(pb.MediaIds))
	seenMedia := make(map[int]bool, len(pb.MediaIds))
	for _, m := range pb.MediaIds {
		if m <= 0 {
			return nil, fmt.Errorf("task media_id must be positive")
		}
		if seenMedia[int(m)] {
			continue
		}
		seenMedia[int(m)] = true
		mediaIds = append(mediaIds, int(m))
	}

	return &entity.TaskInsert{
		Title:           title,
		Description:     nullStringFromPb(strings.TrimSpace(pb.Description)),
		Assignee:        strings.TrimSpace(pb.Assignee),
		Priority:        priority,
		DueDate:         dueDate,
		StartDate:       startDate,
		TechCardId:      nullInt32FromPb(pb.TechCardId),
		ProductId:       nullInt32FromPb(pb.ProductId),
		OrderUuid:       nullStringFromPb(orderUUID),
		ArchiveId:       nullInt32FromPb(pb.ArchiveId),
		FittingId:       nullInt32FromPb(pb.FittingId),
		ProductionRunId: nullInt32FromPb(pb.ProductionRunId),
		SampleId:        nullInt32FromPb(pb.SampleId),
		Labels:          labels,
		MediaIds:        mediaIds,
	}, nil
}

// ConvertEntityTaskToPb converts an entity.Task to pb_common.Task, including
// placement and resolved media.
func ConvertEntityTaskToPb(t *entity.Task) *pb_common.Task {
	if t == nil {
		return nil
	}

	media := make([]*pb_common.MediaFull, 0, len(t.Media))
	mediaIds := make([]int32, 0, len(t.Media))
	for i := range t.Media {
		media = append(media, ConvertEntityToCommonMedia(&t.Media[i]))
		mediaIds = append(mediaIds, int32(t.Media[i].Id))
	}

	return &pb_common.Task{
		Id: int32(t.Id),
		Task: &pb_common.TaskInsert{
			Title:           t.Title,
			Description:     pbStringFromNull(t.Description),
			Assignee:        t.Assignee,
			Priority:        taskPriorityEntityToPb[t.Priority],
			DueDate:         pbTimestampFromNullTime(t.DueDate),
			StartDate:       pbTimestampFromNullTime(t.StartDate),
			Labels:          t.Labels,
			MediaIds:        mediaIds,
			TechCardId:      pbInt32FromNull(t.TechCardId),
			ProductId:       pbInt32FromNull(t.ProductId),
			OrderUuid:       pbStringFromNull(t.OrderUuid),
			ArchiveId:       pbInt32FromNull(t.ArchiveId),
			FittingId:       pbInt32FromNull(t.FittingId),
			ProductionRunId: pbInt32FromNull(t.ProductionRunId),
			SampleId:        pbInt32FromNull(t.SampleId),
		},
		Board:      taskBoardEntityToPb[t.Board],
		Status:     taskStatusEntityToPb[t.Status],
		Position:   int32(t.Position),
		Media:      media,
		CreatedBy:  t.CreatedBy,
		CreatedAt:  timestamppb.New(t.CreatedAt),
		UpdatedAt:  timestamppb.New(t.UpdatedAt),
		ArchivedAt: pbTimestampFromNullTime(t.ArchivedAt),
		StartedAt:  pbTimestampFromNullTime(t.StartedAt),
		Checklist:  ConvertEntityTaskChecklistToPb(t.Checklist),
	}
}

// ConvertEntityTaskChecklistToPb converts a task's checklist items to proto.
func ConvertEntityTaskChecklistToPb(items []entity.TaskChecklistItem) []*pb_common.TaskChecklistItem {
	out := make([]*pb_common.TaskChecklistItem, 0, len(items))
	for i := range items {
		out = append(out, &pb_common.TaskChecklistItem{
			Id:        int32(items[i].Id),
			TaskId:    int32(items[i].TaskId),
			Content:   items[i].Content,
			IsDone:    items[i].IsDone,
			Position:  int32(items[i].Position),
			CreatedAt: timestamppb.New(items[i].CreatedAt),
		})
	}
	return out
}

// maxChecklistContent bounds a checklist item's content (VARCHAR(512) in both
// task_checklist_item and order_fulfillment_checklist_item).
const maxChecklistContent = 512

// ValidateChecklistContent trims and length-checks a checklist item's content,
// shared by task and fulfillment checklist item creation.
func ValidateChecklistContent(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("checklist item content is required")
	}
	if len(s) > maxChecklistContent {
		return "", fmt.Errorf("checklist item content must be at most %d characters", maxChecklistContent)
	}
	return s, nil
}

// ConvertPbTaskCommentInsertToEntity validates and converts a comment payload.
func ConvertPbTaskCommentInsertToEntity(pb *pb_common.TaskCommentInsert) (*entity.TaskCommentInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("task comment is nil")
	}
	if pb.TaskId <= 0 {
		return nil, fmt.Errorf("task comment task_id is required")
	}
	body := strings.TrimSpace(pb.Body)
	if body == "" {
		return nil, fmt.Errorf("task comment body is required")
	}
	if len(body) > maxTaskText {
		return nil, fmt.Errorf("task comment body must be at most %d characters", maxTaskText)
	}
	return &entity.TaskCommentInsert{TaskId: int(pb.TaskId), Body: body}, nil
}

// ConvertEntityTaskCommentToPb converts a stored comment to proto.
func ConvertEntityTaskCommentToPb(c *entity.TaskComment) *pb_common.TaskComment {
	if c == nil {
		return nil
	}
	return &pb_common.TaskComment{
		Id:        int32(c.Id),
		TaskId:    int32(c.TaskId),
		Author:    c.Author,
		Body:      c.Body,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
}
