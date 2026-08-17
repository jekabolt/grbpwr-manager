package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxLibraryFileOwners bounds the owner set. Ownership answers ONE question —
// «кого спросить, когда файл устареет» — and an answer naming a dozen people is
// the same as no answer at all. The cap is a sanity bound against a loop, not a
// rule about how a team should work.
const maxLibraryFileOwners = 10

// libraryFileOwnersMsg is what somebody outside the circle is told. It names the
// circle, because «нет прав» without saying whose right it is sends the person to
// telegram — the exact way of working this section exists to replace.
const libraryFileOwnersMsg = "владельцев файла меняет загрузивший, действующий владелец или супер-админ"

// mayEditLibraryFileOwners reports whether the caller belongs to the circle
// allowed to change a file's owners: the uploader, a current owner, or a super.
//
// ПОЧЕМУ КРУГ, А НЕ ПРОСТО files:write. Право files:write есть у всех, кто вообще
// работает с библиотекой. Без круга любой из них назначил бы себя владельцем
// чужого файла — а с приходом уровней доступа (Ф7) тем же движением РАСШИРИЛ БЫ
// СЕБЕ ДОСТУП к файлу, который ему не показывали. Проверка обязана стоять здесь,
// а не в карте прав: карта знает секцию, но не знает, чей это файл.
//
// Fails closed on a context without authorization: без клеймов человек не супер,
// а совпадение по имени проверяется отдельно и на пустое имя не срабатывает.
func mayEditLibraryFileOwners(ctx context.Context, f *entity.LibraryFile) bool {
	if f == nil {
		return false
	}
	if az, ok := authsrv.GetAdminAuthz(ctx); ok && az.FullAccess() {
		return true
	}
	caller := authsrv.GetAdminUsername(ctx)
	if caller == "" {
		return false
	}
	// ДВЕ ПОЛОВИНЫ АВТОРСТВА ТРЕБУЮТСЯ ОБЕ, И ЭТО ГЛАВНАЯ СТРОКА ФУНКЦИИ.
	//
	// `uploaded_by` — исторический факт, который ПЕРЕЖИВАЕТ аккаунт (0314); давать
	// им право значит отдавать его тому, кто когда-нибудь займёт освободившееся имя.
	// UNIQUE на admins.username освобождает имя при удалении, поэтому сценарий не
	// теоретический: аккаунт pasha удалили → uploaded_by_id занулился по SET NULL, а
	// строка «pasha» осталась → завели НОВОГО pasha → он попал бы в круг у всех
	// сорока файлов прежнего и мог бы забрать их себе (а с приходом Ф7 — и доступ
	// к ним). Проверка `UploadedById.Valid` закрывает ровно это: живая ссылка есть
	// только у файла, чей загрузивший ВСЁ ЕЩЁ существует, а username уникален, значит
	// совпадение имени при живой ссылке — это тот же самый аккаунт.
	//
	// Цена: у файла, чей загрузивший уволен, права загрузившего нет НИ У КОГО —
	// и это правильный ответ, а не потеря. Владельцы и супер никуда не делись.
	if f.UploadedById.Valid && f.UploadedBy == caller {
		return true
	}
	for _, o := range f.Owners {
		if o.Username == caller {
			return true
		}
	}
	return false
}

// SetLibraryFileOwners replaces the file's owner set.
func (s *Server) SetLibraryFileOwners(ctx context.Context, req *pb_admin.SetLibraryFileOwnersRequest) (*pb_admin.SetLibraryFileOwnersResponse, error) {
	if req.FileId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	adminIDs := make([]int, 0, len(req.AdminIds))
	seen := make(map[int]bool, len(req.AdminIds))
	for _, id := range req.AdminIds {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "admin id must be positive")
		}
		if seen[int(id)] {
			continue
		}
		seen[int(id)] = true
		adminIDs = append(adminIDs, int(id))
	}
	if len(adminIDs) > maxLibraryFileOwners {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d owners per file", maxLibraryFileOwners)
	}
	current, err := s.repo.Files().GetFileById(ctx, int(req.FileId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file for owner change", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set owners")
	}
	if !mayEditLibraryFileOwners(ctx, current) {
		return nil, status.Error(codes.PermissionDenied, libraryFileOwnersMsg)
	}
	if err := s.repo.Files().SetFileOwners(ctx, int(req.FileId), adminIDs, authsrv.GetAdminUsername(ctx)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			// Несуществующий аккаунт — не 500: запрос понятен, а мир под ним изменился.
			return nil, status.Error(codes.InvalidArgument, "admin_id does not reference an existing account")
		}
		slog.Default().ErrorContext(ctx, "can't set library file owners", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set owners")
	}
	// Перечитываем владельцев: карточка обязана перерисоваться по тому, что ЛЕЖИТ,
	// а не по тому, что она надеялась отправить.
	stored, err := s.repo.Files().GetFileById(ctx, int(req.FileId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "owners saved but could not be read back", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "owners were saved but could not be read back")
	}
	return &pb_admin.SetLibraryFileOwnersResponse{
		Owners: dto.ConvertEntityAdminRefsToPb(stored.Owners),
	}, nil
}
