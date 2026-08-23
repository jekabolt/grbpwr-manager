package entity

import (
	"database/sql"
	"time"
)

// ДОСТУП К ФАЙЛУ БИБЛИОТЕКИ (0317): три уровня, поимённый список, публичная ссылка и журнал.
//
// Здесь живёт ТОЛЬКО домен доступа. Предикат видимости — «кто вообще видит этот файл» — в типах не
// выражается вовсе и выражаться не должен: он один SQL-фрагмент внутри стора, включаемый в КАЖДУЮ
// выдачу и в каждую запись. Тип, который «несёт видимость», был бы вторым способом её описать, а
// второй способ — это и есть та дыра, ради закрытия которой фаза затевалась.

// LibraryFileAccessLevel is «кому виден файл». Three values, closed by construction: the column is
// an ENUM in the database, so a fourth value cannot be written even by a caller that invents one.
//
// Уровни НЕ упорядочены по строгости, и это не оплошность: `link` открывает файл НАРУЖУ, но внутри
// команды он виден ровно как `team` (уровень открывает, а не закрывает). Строгий здесь один —
// `people`, и только он вырезает файл из выдач.
type LibraryFileAccessLevel string

const (
	// LibraryFileAccessTeam is the default (and the DEFAULT of the column): everybody with
	// files:read sees the file. A file nobody restricted is a file the team shares — that is what
	// the section is for.
	LibraryFileAccessTeam LibraryFileAccessLevel = "team"
	// LibraryFileAccessPeople restricts the file to a named list, PLUS the uploader, the owners and
	// super-admins. The extra three arms are not a courtesy: the circle that manages a file's access
	// must not be able to hide the file from itself.
	LibraryFileAccessPeople LibraryFileAccessLevel = "people"
	// LibraryFileAccessLink additionally opens the file OUTSIDE the panel, through the tokenized
	// public route and nothing else. The S3 object stays private at this level too — publicity is a
	// route, never an ACL.
	LibraryFileAccessLink LibraryFileAccessLevel = "link"
)

// ValidLibraryFileAccessLevels is the accepted set. An unknown level is refused rather than
// interpreted: «непонятный уровень = team» would silently widen access, and «= people» would
// silently lose a file.
var ValidLibraryFileAccessLevels = map[LibraryFileAccessLevel]bool{
	LibraryFileAccessTeam:   true,
	LibraryFileAccessPeople: true,
	LibraryFileAccessLink:   true,
}

// LibraryFileAccessLevelEventPrefix is the ONE machine-readable thing in the journal.
//
// Витрина отвечает на вопрос «кто открыл» actor'ом ПОСЛЕДНЕГО события, установившего ТЕКУЩИЙ
// уровень, — значит, такое событие обязано быть отличимо от «± человек» и «срок» без разбора
// человеческого текста. Формат: `level:<team|people|link>` в начале строки, человеческий хвост
// после него. Отдельной колонки под уровень в таблице нет намеренно — у большинства строк журнала
// она была бы пустой.
const LibraryFileAccessLevelEventPrefix = "level:"

// LibraryFilePublicAccess is the state of the ONE public link a file may carry
// (library_file_public_access, 0317) — the files twin of ProductionRunPackAccess and
// TechCardPatternViewerAccess, down to the column names, because the mechanics are the same:
// the token carries the epoch it was minted at, and `epoch = epoch + 1` kills every link issued so
// far without touching the file, the pepper or anybody else's tokens.
//
// СТРОКА ЖИВЁТ ДОЛЬШЕ УРОВНЯ, НО ССЫЛКА — НЕТ. Переключили файл в `team` — строка остаётся
// (статистика и история никуда не деваются), но получает revoked_at, а возврат в `link` двигает
// epoch и выдаёт НОВЫЙ токен. Прежний адрес мёртв навсегда, а не до следующего включения уровня:
// «вернули уровень» человек не читает как «снова раздали ту же ссылку», и подрядчик, которому её
// когда-то прислали, в круг сегодняшних получателей не входит.
//
// Двух защит здесь не одна, а две, и вторая не лишняя: маршрут ОТДЕЛЬНО сверяет
// `access_level = 'link'` на самой строке файла, поэтому даже совпавшее поколение не открывает
// файл, который сейчас не по ссылке.
type LibraryFilePublicAccess struct {
	FileId int `db:"file_id"`
	// Epoch is the link's generation; +1 is the whole of «отозвать». There is no revocation list
	// and no per-token state — a stateless HMAC cannot be looked up, only out-generationed.
	Epoch int `db:"epoch"`
	// ExpiresAt invalid/NULL = бессрочно (чип «бессрочно»). A past value does NOT change the level:
	// nothing un-shares a file behind its owner's back. The route answers 404 and the UI badges
	// «истёк» — see the note on the field below.
	ExpiresAt sql.NullTime `db:"expires_at"`
	// RevokedAt set = the route answers 404 at ANY epoch. Distinct from a rotation: a rotation
	// replaces the link, this one turns it off.
	RevokedAt sql.NullTime `db:"revoked_at"`
	// LastAccessAt / AccessCount are best-effort statistics folded in outside the read path, so they
	// may lag by a hit. They answer «пользуются ли ссылкой вообще», not accounting. AccessCount is
	// int64 because the column is BIGINT: the counter is bumped by anybody who knows the link, and
	// overflowing an INT is cheaper than it sounds.
	LastAccessAt sql.NullTime `db:"last_access_at"`
	AccessCount  int64        `db:"access_count"`
	CreatedAt    time.Time    `db:"created_at"`
	// UpdatedAt is the operational «когда строку последний раз трогали», statistics included. The
	// question «кто и когда менял доступ» is answered by the journal, never by this field.
	UpdatedAt time.Time `db:"updated_at"`
}

// LibraryFileAccess is the whole «кому виден этот файл» state in one place, so the card and the
// витрина read the same shape instead of each assembling their own.
type LibraryFileAccess struct {
	FileId int
	Level  LibraryFileAccessLevel
	// People is meaningful only at level `people`. It is NOT wiped when the level moves away from
	// it — switching people → team → people must not make a person retype the list. The uploader is
	// always in it: the server puts them there rather than trusting a client to remember, because a
	// person who drops themselves from their own file loses the ability to put themselves back.
	People []AdminRef
	// Link is nil when the file has never been shared by link. Present-but-revoked and
	// present-but-expired are different states, and both are readable from the row.
	Link *LibraryFilePublicAccess
}

// LibraryFileAccessUpdate is one atomic «сделать доступ таким» — the level AND the list that goes
// with it, replaced together. Two calls (set level, then set people) would leave an observable
// window in which the file is restricted to nobody.
type LibraryFileAccessUpdate struct {
	Level LibraryFileAccessLevel
	// AdminIDs is the FULL set for level `people`; read only at that level. Deduped by the caller —
	// a repeat reaches the unique key as a raw 1062.
	AdminIDs []int
	// LinkTTLHours is the life of the public link IN HOURS: 24 / 168 / 720 are the chips of the
	// mockup, 0 = бессрочно. Read only at level `link`. Hours rather than a Duration because the
	// value comes from a fixed set of chips and rides the JSON gateway as a plain number.
	LinkTTLHours int
	// Actor is the username stamped into the journal. A string, not an id: the journal answers
	// questions about a file that outlives its people.
	Actor string
}

// LibraryFileAccessEvent is one line of the journal (library_file_access_event, 0317): кто, когда,
// что. Ровно три поля записи — журнал читается человеком в блоке доступа, а не машиной.
type LibraryFileAccessEvent struct {
	Id     int `db:"id"`
	FileId int `db:"file_id"`
	// Actor is a username string that survives the account's deletion; ActorId is the live link for
	// the avatar and goes NULL with the account. The same split as the file's uploader (0314) and
	// the comment's author (0316).
	Actor   string        `db:"actor"`
	ActorId sql.NullInt64 `db:"actor_id"`
	// What is a rendered description of the change («уровень → по ссылке», «+ kirill», «срок 7
	// дней», «ссылка пересоздана»). Free text on purpose — a code would need a translation table
	// that drifts from the events it names. The ONE exception is the level prefix: see
	// LibraryFileAccessLevelEventPrefix.
	What      string    `db:"what"`
	CreatedAt time.Time `db:"created_at"`
}

// SharedLibraryFile is one row of the витрина: the file plus the answers to «кому открыто» and «кто
// открыл».
type SharedLibraryFile struct {
	// File carries the preview, the name and the level — the витрина draws the same tile the grid
	// does, so it resolves the same shape.
	File LibraryFile
	// People is the named list at level `people`; empty at level `link`, where the truthful answer
	// is «кто угодно со ссылкой» and the screen says exactly that.
	People []AdminRef
	Link   *LibraryFilePublicAccess
	// SharedBy / SharedAt come from the journal: the actor and the time of the last event that
	// established the CURRENT level. Not «кто загрузил» and not «кто трогал последним» — the column
	// answers who is responsible for this file being open right now. Empty when the level predates
	// the journal (files that were already `people` before 0317 cannot have an event).
	SharedBy string
	SharedAt sql.NullTime
}

// SharedLibraryFileFilter narrows the витрина. Server-side because the list is paged: filtering a
// page on the client would print «3 из 40» and mean neither number.
type SharedLibraryFileFilter struct {
	// Level "" = everything special (both `people` and `link`); a level = only that one. `team` is
	// not a valid value here — the витрина is the list of what is NOT the default.
	Level  LibraryFileAccessLevel
	Limit  int
	Offset int
}

// LibraryFileLinkTarget is what a public token resolves to: the narrow read behind
// GET|HEAD /api/f/{token}, and deliberately NOT LibraryFile.
//
// Узкое чтение — это защита, а не экономия. Эндпоинт публичный, RBAC на нём нет и срезать лишнее
// постфактум нечем, поэтому безопасная форма — не читать лишнего вовсе: ни тем, ни владельцев, ни
// автора, ни обсуждения (тот же довод, что в GetPatternViewerManifest и RunPack). Всё, что нужно
// маршруту, — проверить живость ссылки и подписать объект.
type LibraryFileLinkTarget struct {
	FileId      int    `db:"id"`
	FileName    string `db:"file_name"`
	ContentType string `db:"content_type"`
	SizeBytes   int64  `db:"size_bytes"`
	// ObjectKey comes from the file's ROW and from nowhere else — a key is never accepted from a
	// request. That guard is what keeps the presigner from becoming an oracle for arbitrary bucket
	// objects.
	ObjectKey string `db:"object_key"`
	// PreviewObjectKey — отрисованная миниатюра (первая страница pdf, кадр чертежа), если она у
	// файла есть. Лежит здесь по той же причине, по какой здесь лежит ObjectKey: страница ссылки
	// показывает документ ЛИЦОМ, а не именем, и взять картинку ей больше неоткуда — авторизованный
	// GetLibraryFile ей недоступен по построению.
	//
	// ПУСТО — ЭТО НОРМА, А НЕ ОШИБКА: у zip и у файла, чей рендер не удался, миниатюры нет, и
	// маршрут в этом случае просто не кладёт `preview_url` в ответ.
	PreviewObjectKey sql.NullString `db:"preview_object_key"`
	// AccessLevel is checked on the FILE row, not inferred from the presence of a public-access row:
	// a file switched away from `link` must be dead at any epoch.
	AccessLevel LibraryFileAccessLevel `db:"access_level"`
	// The public-access row's state, joined in: epoch to compare against the token, and the two
	// kill switches.
	Epoch     int          `db:"epoch"`
	ExpiresAt sql.NullTime `db:"expires_at"`
	RevokedAt sql.NullTime `db:"revoked_at"`
}
