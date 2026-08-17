package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Ф4 — задачи на карточке файла. Все три RPC классифицированы в секцию TASKS, а не files: в ответе
// заголовки, статусы, исполнители и сроки задач, а мутация — строка принадлежности ЗАДАЧИ
// (task_file каскадит от неё). Секция следует за тем, ЧТО В ОТВЕТЕ — прецедент
// GetMaterialCuttingCoefficientSuggestion.
//
// Названное следствие: у аккаунта без tasks:read блок задач на карточке файла получает
// PermissionDenied, и клиент обязан это пережить надписью «нет доступа к задачам», а не падением.

// libraryFileTaskLinkMissingMsg is what a broken link says. Он называет ОБА конца, потому что
// хендлер физически не знает, какой из них исчез: FK срабатывает на первом же непрошедшем.
const libraryFileTaskLinkMissingMsg = "task or file no longer exists"

// ListLibraryFileTasks returns the tasks holding this file — «где этот файл ещё используется» со
// стороны файла.
//
// Существование файла НЕ ПРОВЕРЯЕТСЯ отдельным чтением: ответ — это строки задач, и для файла,
// которого нет, пустой список и есть правда. Лишнее чтение библиотеки на каждое открытие карточки
// стоило бы дороже различия, которого клиенту всё равно не показать (карточка открыта — значит
// файл уже прочитан).
func (s *Server) ListLibraryFileTasks(ctx context.Context, req *pb_admin.ListLibraryFileTasksRequest) (*pb_admin.ListLibraryFileTasksResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	rows, err := s.repo.Tasks().ListTasksByFileId(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tasks by file id",
			slog.Int("file_id", int(req.Id)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list file tasks")
	}
	return &pb_admin.ListLibraryFileTasksResponse{
		Tasks: dto.ConvertEntityLibraryFileTasksToPb(rows),
	}, nil
}

// AttachLibraryFileToTask links the file to the task. Повтор — не ошибка: карточка не может знать,
// что сделала соседняя вкладка секунду назад.
//
// Исчезнувший конец — NotFound, а не InvalidArgument (в отличие от taskFKViolationMsg на
// AddTask/UpdateTask): там внешним ключом может оказаться любая из семи ссылок ВНУТРИ присланной
// карточки, то есть испорченный payload; здесь id ровно два и оба — предмет запроса, поэтому
// честный ответ «того, к чему цепляем, больше нет», а не «запрос неверен».
func (s *Server) AttachLibraryFileToTask(ctx context.Context, req *pb_admin.AttachLibraryFileToTaskRequest) (*pb_admin.AttachLibraryFileToTaskResponse, error) {
	if req.FileId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	if req.TaskId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if err := s.repo.Tasks().AttachFileToTask(ctx, int(req.FileId), int(req.TaskId)); err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, libraryFileTaskLinkMissingMsg)
		}
		slog.Default().ErrorContext(ctx, "can't attach file to task",
			slog.Int("file_id", int(req.FileId)), slog.Int("task_id", int(req.TaskId)),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't attach file to task")
	}
	return &pb_admin.AttachLibraryFileToTaskResponse{}, nil
}

// DetachLibraryFileFromTask removes the link. Отцепить не прицепленное — успех: двое, убирающие
// одно и то же вложение, оба обязаны услышать «его больше нет».
func (s *Server) DetachLibraryFileFromTask(ctx context.Context, req *pb_admin.DetachLibraryFileFromTaskRequest) (*pb_admin.DetachLibraryFileFromTaskResponse, error) {
	if req.FileId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	if req.TaskId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if err := s.repo.Tasks().DetachFileFromTask(ctx, int(req.FileId), int(req.TaskId)); err != nil {
		slog.Default().ErrorContext(ctx, "can't detach file from task",
			slog.Int("file_id", int(req.FileId)), slog.Int("task_id", int(req.TaskId)),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't detach file from task")
	}
	return &pb_admin.DetachLibraryFileFromTaskResponse{}, nil
}
