package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ГРУППИРОВКА ФАЙЛОВ: ПРОЕКТ × РОЛЬ.
//
// Тема получает ТИП (обычный ярлык или проект), а роль файла В ПРОЕКТЕ живёт на СТРОКЕ СВЯЗИ
// `library_file_topic.role_id` — не второй меткой на файле. Разница не косметическая: плоский
// набор меток теряет ПАРУ. Снимок, лежащий в съёмке как «отобранное» и в лукбуке как «референс»,
// нёс бы {съёмка, лукбук, отобранное, референс}, и пересечение «съёмка × референс» находило бы
// его — молча и ложно, потому что выдача выглядит правдоподобной и проверить её нечем.
//
// «Одна роль на файл в проекте» при этом не правило, а форма данных: UNIQUE(file_id, topic_id)
// (0312) даёт ровно одну строку на пару, у строки ровно одно поле роли. Две роли в одном проекте
// НЕВЫРАЗИМЫ, поэтому и запрещать нечего. Невыразимо и другое — роль БЕЗ проекта; это верное
// поведение, а не потеря: «это исходник ничего» не значит ничего.

// FileTopicKind — тип темы. Закрытое множество, и оно закрыто в схеме (ENUM), а не соглашением:
// неизвестное значение отвергается, а не интерпретируется.
type FileTopicKind string

const (
	// FileTopicKindPlain — обычный ярлык. Значение по умолчанию в схеме, поэтому все темы,
	// заведённые до 0320, остаются им без бэкфилла.
	FileTopicKindPlain FileTopicKind = "plain"
	// FileTopicKindProject — проект: даты, архив, разбивка по ролям.
	FileTopicKindProject FileTopicKind = "project"
)

// ParseFileTopicKind принимает то, что приехало с провода. ПУСТАЯ строка — это «plain», а не
// ошибка: тема, сохранённая до появления поля, приезжает без него, и отказывать на ней значило бы
// уронить переименование обычной темы.
func ParseFileTopicKind(s string) (FileTopicKind, error) {
	switch FileTopicKind(s) {
	case "", FileTopicKindPlain:
		return FileTopicKindPlain, nil
	case FileTopicKindProject:
		return FileTopicKindProject, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrFileTopicKindUnknown, s)
	}
}

var (
	// ErrFileTopicKindUnknown — тип темы, которого нет в схеме. InvalidArgument у вызывающего.
	ErrFileTopicKindUnknown = errors.New("unknown file topic kind")
	// ErrFileTopicKindMismatch запрещает слияние тем РАЗНЫХ типов. Слить проект в обычную тему
	// нечем: у проекта есть даты, архив и роли на строках связи, у ярлыка ничего этого нет, и
	// вопрос «что станет с ролями» не имеет хорошего ответа. Хуже того, роли уехали бы на строки
	// темы, которая проектом не является, — то самое состояние, которое стор нигде больше не
	// допускает.
	ErrFileTopicKindMismatch = errors.New("topics of different kinds cannot be merged")
	// ErrRoleNeedsProjectTopic — попытка поставить роль в теме, которая не проект. Проверяется в
	// одном месте (там, где роль ставится); констрейнтом это не выражается без денормализации
	// `kind` в таблицу связи, а денормализация давала бы устаревающие строки при смене типа.
	ErrRoleNeedsProjectTopic = errors.New("roles can only be set inside a project topic")
	// ErrFileRoleArchived — назначить заархивированную роль нельзя (снять — можно). Иначе архив
	// был бы пожеланием: роль пропала бы из пикеров и продолжила бы появляться на файлах.
	ErrFileRoleArchived = errors.New("archived role cannot be assigned")
)

// FileRole — одна запись ЗАКРЫТОГО словаря ролей.
//
// Закрытость держится на форме данных, а не на дисциплине: роли лежат в СВОЕЙ таблице, поэтому ни
// `new_topics` при загрузке, ни модалка вставки, ни массовое проставление тем создать роль не
// могут — они пишут в file_topic. Единственная точка создания — UpsertRole.
//
// Отдельная таблица, а не третье значение `kind`, ещё и потому, что иначе каждый уже отгруженный
// путь, перечисляющий темы со счётчиками (рельс, чипы, четыре пикера), пришлось бы учить
// «исключай роли».
type FileRole struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
	// SortOrder — порядок секций на странице проекта. Равные значения сортируются по имени,
	// чтобы порядок был полным и не зависел от порядка вставки.
	SortOrder int `db:"sort_order"`
	// ArchivedAt — invalid/NULL значит «не в архиве». Архив прячет роль из чипов и пикеров,
	// оставляя её на экране тем и на уже проставленных файлах.
	ArchivedAt sql.NullTime `db:"archived_at"`
	CreatedAt  time.Time    `db:"created_at"`
}

// FileRoleWithCount — роль плюс СКВОЗНОЙ счётчик: сколько файлов несут её в любом проекте.
// Счётчик всегда выводится запросом; колонки-счётчика нет, потому что ей было бы с чем разойтись.
type FileRoleWithCount struct {
	FileRole
	FilesCount int `db:"files_count"`
}

// FileRoleUpsert — запись словаря. Id = 0 создаёт, иначе правит существующую.
type FileRoleUpsert struct {
	Id        int
	Name      string
	SortOrder int
	Archived  bool
}

// LibraryFileRoleRef — «чем этот файл является В ЭТОМ проекте»: ПАРА, а не ярлык.
//
// Именно пара едет на провод и рисуется на плитке («осень 2026 · исходники»). Плоский список
// ролей на файле был бы той самой моделью, ради ухода от которой всё это писалось.
type LibraryFileRoleRef struct {
	ProjectTopicId   int    `db:"project_topic_id"`
	ProjectTopicName string `db:"project_topic_name"`
	RoleId           int    `db:"role_id"`
	RoleName         string `db:"role_name"`
}

// FileTopicMetaUpdate — ПОЛНАЯ замена метаданных темы: тип, даты, архив.
//
// Замена безопасна ровно потому, что RPC новый и старого клиента у него нет — форма всегда
// приезжает целиком. Дописать эти поля в уже отгруженный RenameFileTopic было бы дешевле на один
// RPC и опаснее по существу: клиент, не знающий про `kind`, прислал бы пустой тип и молча понизил
// бы проект до обычной темы при первом же переименовании.
type FileTopicMetaUpdate struct {
	TopicId  int
	Kind     FileTopicKind
	StartsAt sql.NullTime
	EndsAt   sql.NullTime
	Archived bool
}
