package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
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
	assignees, err := taskAssigneesFromPb(pb.Assignees, pb.Assignee)
	if err != nil {
		return nil, err
	}
	// ВСЕ глубокие ссылки разом. sample_id тут не хватало с самого его появления: отрицательный
	// id проходил гейт, уезжал в стор как Valid и умирал об внешний ключ — то есть внятный
	// InvalidArgument про знак подменялся общим перечнем полей.
	if pb.TechCardId < 0 || pb.ProductId < 0 || pb.ArchiveId < 0 || pb.FittingId < 0 ||
		pb.ProductionRunId < 0 || pb.SampleId < 0 || pb.ProjectTopicId < 0 {
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

	fileIds := make([]int, 0, len(pb.FileIds))
	seenFile := make(map[int]bool, len(pb.FileIds))
	for _, f := range pb.FileIds {
		if f <= 0 {
			return nil, fmt.Errorf("task file_id must be positive")
		}
		if seenFile[int(f)] {
			continue
		}
		seenFile[int(f)] = true
		fileIds = append(fileIds, int(f))
	}

	mediaAnnotations, err := taskMediaAnnotationsFromPb(seenMedia, pb.MediaAnnotations)
	if err != nil {
		return nil, err
	}

	return &entity.TaskInsert{
		Title:            title,
		Description:      nullStringFromPb(strings.TrimSpace(pb.Description)),
		Assignees:        assignees,
		Priority:         priority,
		DueDate:          dueDate,
		StartDate:        startDate,
		TechCardId:       nullInt32FromPb(pb.TechCardId),
		ProductId:        nullInt32FromPb(pb.ProductId),
		OrderUuid:        nullStringFromPb(orderUUID),
		ArchiveId:        nullInt32FromPb(pb.ArchiveId),
		FittingId:        nullInt32FromPb(pb.FittingId),
		ProductionRunId:  nullInt32FromPb(pb.ProductionRunId),
		SampleId:         nullInt32FromPb(pb.SampleId),
		ProjectTopicId:   nullInt32FromPb(pb.ProjectTopicId),
		Labels:           labels,
		MediaIds:         mediaIds,
		FileIds:          fileIds,
		MediaAnnotations: mediaAnnotations,
	}, nil
}

// taskAssigneesFromPb собирает список исполнителей и СЛИВАЕТ В НЕГО deprecated-алиас поля 3.
//
// ПРАВИЛО СЛИЯНИЯ: непустой assignees выигрывает всегда, алиас при этом игнорируется молча. Пустой
// assignees плюс непустой алиас = список из одного. Иначе — пусто, «задачу никто не взял».
//
// ПОЧЕМУ АЛИАС ВООБЩЕ ЖИВ. Старая вкладка админки шлёт только поле 3, а admin-гейтвей разбирает JSON
// с DiscardUnknown: false — снятие поля превратило бы каждое её сохранение в 400. Названное
// следствие переходного окна: сохранение из СТАРОЙ вкладки оставит одного (первого) исполнителя, она
// шлёт то, что прочла. Окно — минуты между деплоем бека и клиента.
//
// Trim/дедуп/предел длины — дословно как у labels ниже: половина списков карточки не может прощать
// дубль, пока другая половина роняет сохранение.
func taskAssigneesFromPb(list []string, deprecatedAlias string) ([]string, error) {
	merged := list
	if len(merged) == 0 && strings.TrimSpace(deprecatedAlias) != "" {
		merged = []string{deprecatedAlias}
	}
	out := make([]string, 0, len(merged))
	seen := make(map[string]bool, len(merged))
	for _, a := range merged {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if len(a) > maxVarchar255 {
			return nil, fmt.Errorf("task assignee must be at most %d characters", maxVarchar255)
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out, nil
}

// taskAssigneeAlias — обратный ход: чем заполнить deprecated-поле 3 на выходе.
//
// ЗАБЫТЬ ЕГО ЗАПОЛНИТЬ НЕЛЬЗЯ: EmitUnpopulated отдал бы "", и доска СТАРОГО клиента показала бы все
// карточки неназначенными — то есть тихая потеря, а не ошибка. Ровно это ловит негативный контроль
// теста алиаса.
func taskAssigneeAlias(assignees []string) string {
	if len(assignees) == 0 {
		return ""
	}
	return assignees[0]
}

// taskMediaAnnotationsFromPb разбирает указания, нарисованные на вложенных картинках карточки.
//
// ПРОВЕРКА ОБЩАЯ С ТЕХ-КАРТОЙ, а не своя: annotationsFromPb уже сверяет вид с числом точек, держит
// координаты в долях кадра, ограничивает текст, закрывает список цветов и приводит пунктир/штриховку
// к виду фигуры. Второй валидатор того же сообщения разошёлся бы с первым на первой же добавленной
// фигуре, и человек, рисующий одним и тем же жестом, получал бы два разных отказа.
//
// `attached` — картинки, уже прошедшие разбор media_ids ЭТОЙ ЖЕ карточки.
//
// Своего предела на число наборов здесь нет намеренно: набор без прикреплённой картинки
// отбрасывается, поэтому наборов не больше, чем картинок, а на media_ids задачи потолка не стоит.
// Предел на выноски внутри одного снимка — общий, maxAnnotationsPerMedia.
func taskMediaAnnotationsFromPb(attached map[int]bool, in []*pb_common.TaskMediaAnnotations) ([]entity.TaskMediaAnnotations, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]entity.TaskMediaAnnotations, 0, len(in))
	seen := make(map[int]bool, len(in))
	for i, set := range in {
		path := fmt.Sprintf("media_annotations[%d]", i)
		if set == nil {
			continue
		}
		mediaID := int(set.MediaId)
		// Нулевой/отрицательный id — ИСПОРЧЕННЫЙ payload, а не устаревший, поэтому отказ, и
		// проверяется он раньше отбрасывания: иначе такой набор исчезал бы молча вместе с багом,
		// который его прислал.
		if mediaID <= 0 {
			return nil, entity.NewFieldViolation(path+".media_id", "required", "",
				"a set of callouts without a picture means nothing — name the picture of this card")
		}
		// ДУБЛЬ СНИМАЕТСЯ МОЛЧА, ПЕРВЫЙ ВЫИГРЫВАЕТ — как labels, media_ids и file_ids этой же
		// функции: половина списков карточки прощала бы дубль, половина роняла бы сохранение.
		//
		// И это не мягкость, а единственный выход. У task_media НЕТ UNIQUE (task_id, media_id):
		// 0090 её не ставит, 0313 тоже — ретроактивный UNIQUE проверяет всю историю и роняет старт
		// прода. Значит пара строк с одним media_id физически возможна (сидер, правка руками,
		// будущий писатель), чтение вернуло бы два набора с одним id, и НЕМЕДЛЕННЫЙ круговой рейс
		// стал бы вечным 400: карточку нельзя было бы сохранить, пока кто-то не отцепит картинку
		// руками. Несохраняемая карточка дороже потерянного второго набора, которого ни один наш
		// клиент не шлёт.
		//
		// Отметка ДО проверки прикреплённости, а не после: иначе одна и та же форма запроса
		// решалась бы по-разному в зависимости от поля, которого в самом наборе нет.
		if seen[mediaID] {
			continue
		}
		seen[mediaID] = true
		// НАБОР БЕЗ СВОЕЙ КАРТИНКИ ОТБРАСЫВАЕТСЯ МОЛЧА. Снятие картинки и правка содержимого
		// приходят ОДНИМ сохранением, и клиент законно шлёт то, что прочитал; отказ за указание на
		// снимке, которого на карточке уже нет, требовал бы от формы порядка, которого у неё нет.
		// Хранить такой набор тоже нельзя: его не увидеть и не убрать.
		if !attached[mediaID] {
			continue
		}
		anns, err := annotationsFromPb(path, set.Annotations)
		if err != nil {
			return nil, err
		}
		// КЛЮЧИ ДЕТАЛЕЙ КРОЯ ОЧИЩАЮТСЯ: у карточки канбана деталей нет — ни выбрать, ни показать.
		// Доехавшая сюда ссылка на деталь чужой тех-карты это висящий ключ, который однажды
		// напечатают.
		for j := range anns {
			anns[j].PieceLineKey = ""
			anns[j].PieceLineKeys = nil
		}
		out = append(out, entity.TaskMediaAnnotations{MediaId: mediaID, Annotations: anns})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// taskMediaAnnotationsToPb — обратный ход. Порядок наборов задаёт стор (порядок картинок карточки),
// и здесь он сохраняется как есть: круговой рейс обязан вернуть то же, что прочитал, включая
// последовательность.
//
// Ключи деталей не отдаются вовсе — их у карточки не бывает (см. разбор выше).
//
// ИЗВЕСТНОЕ И ОСОЗНАННОЕ: вид, которого нет в словаре (испорченная строка в колонке, откат на
// старый бинарь), отдаётся клиенту нулевым энумом, и следующее сохранение такой карточки —
// вечный 400, потому что на входе неизвестный вид это отказ. Чинить здесь НЕ надо: снимок шага
// сборки (0308) ведёт себя ровно так же, а разойтись двум поверхностям одного примитива хуже
// самой болезни — человек рисует одним жестом и вправе ждать одного поведения.
func taskMediaAnnotationsToPb(in []entity.TaskMediaAnnotations) []*pb_common.TaskMediaAnnotations {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb_common.TaskMediaAnnotations, 0, len(in))
	for _, s := range in {
		anns := make([]*pb_common.TechCardAnnotation, 0, len(s.Annotations))
		for _, a := range s.Annotations {
			anns = append(anns, &pb_common.TechCardAnnotation{
				Kind:   annotationKindToPb[a.Kind],
				Points: calloutPointsToPb(a.Points),
				Text:   a.Text,
				LabelX: pbDecimalFromDecimal(a.LabelX),
				LabelY: pbDecimalFromDecimal(a.LabelY),
				Color:  annotationColorToPb[a.Color],
				Dashed: a.Dashed,
				Filled: a.Filled,
				Caps:   annotationCapsToPb[a.Caps],
			})
		}
		out = append(out, &pb_common.TaskMediaAnnotations{
			MediaId:     int32(s.MediaId),
			Annotations: anns,
		})
	}
	return out
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

	// Only the ids of library attachments travel on common.Task. The resolved
	// files carry presigned urls with a 6-12h life, and this message is reused in
	// places that get persisted — a stored url would be a link that rots.
	fileIds := make([]int32, 0, len(t.FileIds))
	for _, id := range t.FileIds {
		fileIds = append(fileIds, int32(id))
	}

	return &pb_common.Task{
		Id: int32(t.Id),
		Task: &pb_common.TaskInsert{
			Title:       t.Title,
			Description: pbStringFromNull(t.Description),
			Assignees:   t.Assignees,
			// Deprecated-алиас поля 3 ОБЯЗАН быть заполнен, пока он на проводе: старый клиент читает
			// только его, а EmitUnpopulated отдал бы "" и нарисовал бы всю доску неназначенной.
			Assignee:        taskAssigneeAlias(t.Assignees),
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
			ProjectTopicId:  pbInt32FromNull(t.ProjectTopicId),
			FileIds:         fileIds,
			// Указания едут ВМЕСТЕ с содержимым, а не отдельным полем ответа: карточку
			// сохраняет одна форма, и то, что она не прочитала, она сотрёт полной заменой.
			MediaAnnotations: taskMediaAnnotationsToPb(t.MediaAnnotations),
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
		FileIds:    fileIds,
		// Иерархия и связи едут на Task, а НЕ внутри TaskInsert: TaskInsert сохраняется полной
		// заменой, и клиент, не знающий поля, стирал бы родителя каждым сохранением.
		ParentTaskId: pbInt32FromNull(t.ParentTaskId),
		Links:        ConvertEntityTaskLinksToPb(t.Links),
		SubtaskTotal: int32(t.SubtaskTotal),
		SubtaskDone:  int32(t.SubtaskDone),
	}
}

// taskLinkRoleToPb — перспектива хранилища → перспектива контракта. Ролей ТРИ, видов в хранилище ДВА:
// BLOCKED_BY это blocks, прочитанный с другого конца.
var taskLinkRoleToPb = map[entity.TaskLinkRole]pb_common.TaskLinkKind{
	entity.TaskLinkRoleBlocks:    pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKS,
	entity.TaskLinkRoleBlockedBy: pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKED_BY,
	entity.TaskLinkRoleRelates:   pb_common.TaskLinkKind_TASK_LINK_KIND_RELATES,
}

// ConvertPbTaskLinkKindToEntity разворачивает ПЕРСПЕКТИВУ в хранимую строку: возвращает вид и то,
// надо ли поменять концы местами.
//
// ЧИСТАЯ ФУНКЦИЯ И ЖИВЁТ ЗДЕСЬ, А НЕ В СТОРЕ, потому что перспектива — свойство КОНТРАКТА: «ту
// задачу надо закончить раньше этой» и «эта блокирует ту» — один факт, названный с разных концов.
// Нормализация relates (min,max) наоборот живёт в сторе, рядом с CHECK'ом, который её закрепляет.
func ConvertPbTaskLinkKindToEntity(k pb_common.TaskLinkKind) (kind entity.TaskLinkKind, swap bool, err error) {
	switch k {
	case pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKS:
		return entity.TaskLinkKindBlocks, false, nil
	case pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKED_BY:
		// Перевёрнутый blocks: блокером становится ВТОРАЯ задача.
		return entity.TaskLinkKindBlocks, true, nil
	case pb_common.TaskLinkKind_TASK_LINK_KIND_RELATES:
		return entity.TaskLinkKindRelates, false, nil
	default:
		return "", false, fmt.Errorf("unknown or unset task link kind: %v", k)
	}
}

// ConvertEntityTaskLinksToPb converts a card's resolved links.
//
// Неизвестные строки статуса и доски едут нулевым энумом — ровно как в ConvertEntityTaskToPb:
// разойтись двум чтениям одной задачи хуже, чем отдать UNKNOWN.
func ConvertEntityTaskLinksToPb(links []entity.TaskLink) []*pb_common.TaskLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]*pb_common.TaskLink, 0, len(links))
	for _, l := range links {
		out = append(out, &pb_common.TaskLink{
			TaskId:   int32(l.TaskId),
			Kind:     taskLinkRoleToPb[l.Role],
			Title:    l.Title,
			Status:   taskStatusEntityToPb[l.Status],
			Board:    taskBoardEntityToPb[l.Board],
			Archived: l.Archived,
		})
	}
	return out
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

// ConvertEntityLibraryFileTasksToPb converts the task rows a FILE card draws (Ф4).
//
// Проекция, а не common.Task, и это решение контракта, а не экономия: Task несёт содержимое,
// чек-лист, разрешённые медиа и СВОИ вложения, поэтому на каждую задачу, к которой прицеплен файл,
// пришлось бы резолвить ещё один список файлов ради строки с pill-ом, заголовком и сроком.
//
// Неизвестные строки статуса и доски (испорченная колонка, откат на старый бинарь) едут нулевым
// энумом — ровно как в ConvertEntityTaskToPb: разойтись двум чтениям одной и той же задачи хуже,
// чем отдать UNKNOWN, а строка карточки от этого остаётся читаемой.
func ConvertEntityLibraryFileTasksToPb(rows []entity.LibraryFileTask) []*pb_admin.LibraryFileTask {
	out := make([]*pb_admin.LibraryFileTask, 0, len(rows))
	for _, r := range rows {
		out = append(out, &pb_admin.LibraryFileTask{
			TaskId:    int32(r.TaskId),
			Title:     r.Title,
			Status:    taskStatusEntityToPb[r.Status],
			Assignees: r.Assignees,
			// Deprecated-алиас поля 4 = первый исполнитель. Заполняется по той же причине, что и на
			// карточке: старая вкладка читает только его.
			Assignee: taskAssigneeAlias(r.Assignees),
			// Срока может не быть, и тогда поля нет вовсе — «нет срока», а не «сегодня».
			DueDate: pbTimestampFromNullTime(r.DueDate),
			Board:   taskBoardEntityToPb[r.Board],
		})
	}
	return out
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
		Id:     int32(c.Id),
		TaskId: int32(c.TaskId),
		Author: c.Author,
		// Живая ссылка на аккаунт автора; 0 = аккаунта больше нет. По ней клиент решает, рисовать ли
		// кнопку удаления, но ПРОВЕРЯЕТ пару «имя + живая ссылка» сервер.
		AuthorId:  pbInt32FromNull(c.AuthorId),
		Body:      c.Body,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
}
