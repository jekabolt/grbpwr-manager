package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxLibraryFileNameLen mirrors the column width; a longer name is a mistake,
// not something to silently truncate.
const maxLibraryFileNameLen = 255

// maxLibraryTopicNameLen mirrors file_topic.name.
const maxLibraryTopicNameLen = 64

// inlineSafeContentTypes is the allowlist of types the library will hand back an
// IN-PLACE view url for. Everything else — including svg and html — gets a
// download url only.
//
// This is the whole XSS story of the feature. A presigned url points at the
// bucket's own origin, so an inline-rendered svg or html document would execute
// scripts in that origin's context, with whatever that origin is trusted for. A
// library accepts arbitrary file types by design, so the safety cannot live at
// the upload gate — it lives here, at the moment a url is minted.
//
// text/plain БОЛЬШЕ НЕТ В СПИСКЕ (Ф7b). Он лежал здесь, пока url'ы жили только внутри
// панели под RBAC; Ф7 впервые сделала выдачу url'а НЕаутентифицированной (/api/f/{token}),
// и этого достаточно, чтобы тип стал опасным: Chromium досниффливает text/plain до
// text/html, а поставить `nosniff` на presigned url нечем — заголовки ответа принадлежат
// бакету. То есть .txt со <script> внутри исполнился бы на origin бакета у любого, кому
// прислали ссылку. Потеря — предпросмотр .txt в панели; заметки её не касаются, они едут
// text/markdown по RPC (dto/library_note.go) и подписанного url не требуют вовсе.
var inlineSafeContentTypes = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"image/gif":       true,
	"image/avif":      true,
	"video/mp4":       true,
	"video/webm":      true,
}

// IsInlineSafeContentType reports whether a stored content type may be served
// with an inline (viewable) url. Parameters are stripped ("image/png; charset=
// utf-8"), and the comparison is case-insensitive, because both come off a
// client-declared header.
func IsInlineSafeContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return inlineSafeContentTypes[ct]
}

// ValidateLibraryFileName trims and bounds a file name.
func ValidateLibraryFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("file name is required")
	}
	if len([]rune(name)) > maxLibraryFileNameLen {
		return "", fmt.Errorf("file name must be at most %d characters", maxLibraryFileNameLen)
	}
	// A stored name reaches a Content-Disposition header at presign time, where
	// the sanitiser drops separators and control characters. Refusing them here
	// too means the stored value and the served value never disagree.
	if strings.ContainsAny(name, "/\\\"\r\n\x00") {
		return "", fmt.Errorf("file name must not contain slashes, quotes or control characters")
	}
	return name, nil
}

// ValidateLibraryTopicName trims and bounds a topic name.
func ValidateLibraryTopicName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("topic name is required")
	}
	if len([]rune(name)) > maxLibraryTopicNameLen {
		return "", fmt.Errorf("topic name must be at most %d characters", maxLibraryTopicNameLen)
	}
	if strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("topic name must not contain control characters")
	}
	return name, nil
}

// ConvertEntityFileTopicToPb converts one topic label (without a count).
func ConvertEntityFileTopicToPb(t entity.FileTopic) *pb_admin.FileTopic {
	return &pb_admin.FileTopic{
		Id:          int32(t.Id),
		Name:        t.Name,
		Description: t.Description.String,
	}
}

// ConvertEntityFileTopicsWithCountToPb converts the rail: topics with their
// file counts, already ordered by usage.
func ConvertEntityFileTopicsWithCountToPb(topics []entity.FileTopicWithCount) []*pb_admin.FileTopic {
	out := make([]*pb_admin.FileTopic, 0, len(topics))
	for _, t := range topics {
		out = append(out, &pb_admin.FileTopic{
			Id:          int32(t.Id),
			Name:        t.Name,
			Description: t.Description.String,
			FilesCount:  int32(t.FilesCount),
		})
	}
	return out
}

// ConvertEntityLibraryFileToPb converts the stored metadata. It deliberately
// leaves the three url fields EMPTY: minting them needs the bucket and a policy
// decision about inline safety, which belongs to the API layer, not here.
func ConvertEntityLibraryFileToPb(f *entity.LibraryFile) *pb_admin.LibraryFile {
	if f == nil {
		return nil
	}
	topics := make([]*pb_admin.FileTopic, 0, len(f.Topics))
	for _, t := range f.Topics {
		topics = append(topics, ConvertEntityFileTopicToPb(t))
	}
	return &pb_admin.LibraryFile{
		Id:          int32(f.Id),
		FileName:    f.FileName,
		ContentType: f.ContentType,
		SizeBytes:   f.SizeBytes,
		Sha256:      f.Sha256,
		UploadedBy:  f.UploadedBy,
		// 0 значит «аккаунта больше нет», а НЕ «неизвестно кто»: имя загрузившего
		// едет строкой выше и переживает удаление аккаунта. Клиент печатает имя
		// всегда, а ссылку на человека — только когда id ненулевой.
		UploadedById: int32(f.UploadedById.Int64),
		Topics:       topics,
		Owners:       ConvertEntityAdminRefsToPb(f.Owners),
		CreatedAt:    timestamppb.New(f.CreatedAt),
		// Поля фаз обсуждения, доступа и заметок. Заполняются здесь, а не в трёх
		// разных местах: конвертер один, и файл, приехавший из витрины доступа,
		// обязан выглядеть так же, как приехавший из сетки.
		CommentsCount:    int32(f.CommentsCount),
		AccessLevel:      string(f.AccessLevel),
		ContentUpdatedBy: f.ContentUpdatedBy,
		// nullTimeToPb (dto/membership.go) отдаёт nil, а не нулевой Timestamp: у
		// файла, который никто не правил, «правили в первом году» было бы враньём.
		// Незаполненное время приходит на провод явным null (EmitUnpopulated), и
		// клиент отличает отсутствие только по нему.
		ContentUpdatedAt: nullTimeToPb(f.ContentUpdatedAt),
		ContentExcerpt:   f.ContentExcerpt,
	}
}

// ConvertPbTopicFilterToEntity normalises the intersection filter: ids are
// deduped (a repeated chip must not cost a second EXISTS subquery) and bounded.
// Non-positive ids are dropped rather than refused — unlike a topic SELECTION,
// where a zero id means the caller built a broken write, a filter is read-only
// and a stray 0 from a url is not worth an error page over.
func ConvertPbTopicFilterToEntity(topicIDs []int32) ([]int, error) {
	if len(topicIDs) > entity.MaxLibraryTopicFilters {
		return nil, fmt.Errorf("at most %d topics can be combined in one filter", entity.MaxLibraryTopicFilters)
	}
	ids := make([]int, 0, len(topicIDs))
	seen := make(map[int]bool, len(topicIDs))
	for _, id := range topicIDs {
		if id <= 0 || seen[int(id)] {
			continue
		}
		seen[int(id)] = true
		ids = append(ids, int(id))
	}
	return ids, nil
}

// ConvertPbLibraryFileSortToEntity maps the grid's sort control. UNKNOWN means
// the chronological default, which is the only ordering OrderFactor applies to.
func ConvertPbLibraryFileSortToEntity(s pb_admin.LibraryFileSort) entity.LibraryFileSort {
	switch s {
	case pb_admin.LibraryFileSort_LIBRARY_FILE_SORT_NAME:
		return entity.LibraryFileSortName
	case pb_admin.LibraryFileSort_LIBRARY_FILE_SORT_SIZE:
		return entity.LibraryFileSortSize
	default:
		return entity.LibraryFileSortDefault
	}
}

// ConvertPbLibraryPersonFilterToEntity normalises the (person_id, person_role)
// pair into the two fields the store filter carries.
//
// НЕСУЩЕСТВУЮЩИЙ ЧЕЛОВЕК — НЕ ОШИБКА, и здесь он даже не проверяется: id уезжает в запрос
// как есть и просто ничему не совпадает. Проверка существования была бы ОРАКУЛОМ — перебирая
// id и различая «не найден» от «ничего не нашлось», можно пересчитать аккаунты (и по
// InvalidArgument на 1..N узнать, где кончается список сотрудников), ни разу не имея права
// читать admins.
//
// Неположительный id — просто ОТСУТСТВИЕ фильтра, а не ошибка: контрол необязателен, и 0 из
// url'а не стоит страницы отказа. Роль без человека игнорируется по тому же доводу — она одна
// ничего не сужает.
func ConvertPbLibraryPersonFilterToEntity(personID int32, role pb_admin.LibraryFilePersonRole) (int, entity.LibraryFilePersonRole) {
	if personID <= 0 {
		return 0, entity.LibraryFilePersonRoleAny
	}
	switch role {
	case pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_UPLOADED:
		return int(personID), entity.LibraryFilePersonRoleUploaded
	case pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_OWNER:
		return int(personID), entity.LibraryFilePersonRoleOwner
	default:
		// Незнакомое будущее значение енума схлопывается в «любая», а не в отказ: роль
		// СУЖАЕТ выборку, и неузнанное сужение показало бы меньше, чем человек просил,
		// ничего об этом не сказав. Расширение до «где он числится вообще» — честный
		// ответ на «в какой-то роли».
		return int(personID), entity.LibraryFilePersonRoleAny
	}
}

// ConvertPbTopicSelectionToEntity normalises the (topic_ids, new_topics) pair
// that both the upload and the update paths carry: ids are deduped and bounded,
// names are trimmed, validated and deduped case-insensitively so "Brand" typed
// twice does not try to create two topics in one request.
func ConvertPbTopicSelectionToEntity(topicIDs []int32, newTopics []string) ([]int, []string, error) {
	ids := make([]int, 0, len(topicIDs))
	seenID := make(map[int]bool, len(topicIDs))
	for _, id := range topicIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("topic id must be positive")
		}
		if seenID[int(id)] {
			continue
		}
		seenID[int(id)] = true
		ids = append(ids, int(id))
	}
	names := make([]string, 0, len(newTopics))
	seenName := make(map[string]bool, len(newTopics))
	for _, n := range newTopics {
		name, err := ValidateLibraryTopicName(n)
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(name)
		if seenName[key] {
			continue
		}
		seenName[key] = true
		names = append(names, name)
	}
	return ids, names, nil
}
