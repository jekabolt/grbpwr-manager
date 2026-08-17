package entity

import (
	"database/sql"
	"time"
)

// MARKDOWN-ЗАМЕТКА (0318) — обычный файл библиотеки, у которого содержимое правится в панели.
//
// Заметка НЕ отдельная сущность: это строка library_file с content_type text/markdown, и темы,
// владельцы, доступ, обсуждение и задачи достаются ей бесплатно, ровно теми же механизмами. Своего
// здесь ровно две вещи — текст лежит в приватном объекте (а не в колонке) и правка идёт под CAS.

// MaxLibraryNoteBytes caps a note's text. Это заметка, а не книга: потолок — то, что делает
// честной саму идею возить текст ЦЕЛИКОМ по RPC (и туда, и обратно, и ещё раз в ответе конфликта).
// Превышение — InvalidArgument, а не молчаливая обрезка: сохранённый наполовину текст хуже отказа.
const MaxLibraryNoteBytes = 512 * 1024

// LibraryNoteInsert is the writable payload of a NEW note: the file row plus the three note-only
// columns of 0318.
//
// ОТДЕЛЬНЫЙ ТИП, А НЕ ТРИ ПОЛЯ В LibraryFileInsert, И ЭТО НЕ КОСМЕТИКА: Insert — это то, что
// вызывающий ВПРАВЕ задать, а путь загрузки файла не вправе задать ни выдержку, ни «кто правил
// последним» (он там никто и не правил). Лежи эти поля в общем Insert, путь загрузки заполнил бы их
// молча и однажды напечатал бы «правил pasha» на файле, который никто не открывал в редакторе.
type LibraryNoteInsert struct {
	LibraryFileInsert
	// ContentExcerpt is the first lines of the text, for the tile that has no picture to show. It is
	// derived by the CALLER from the same content it just uploaded — the store never reads the
	// bytes, so it has nothing to derive it from.
	ContentExcerpt string `db:"content_excerpt"`
	// ContentUpdatedBy / ContentUpdatedAt stamp «кто правил последним» at creation, because creating
	// a note WITH text is an edit — the person typed it. A note created empty leaves them zero, and
	// the card then falls back to the uploader, which is the truth in that case.
	ContentUpdatedBy string       `db:"content_updated_by"`
	ContentUpdatedAt sql.NullTime `db:"content_updated_at"`
}

// LibraryNote is a note's stored row — everything the note screen needs EXCEPT the text itself.
//
// Текста здесь нет намеренно. Байты лежат в приватном объекте, а стор в бакет не ходит (см.
// комментарий у интерфейса Files): содержимое читает вызывающий по ObjectKey через
// FileStore.GetLibraryObject. Разрез проходит ровно по слою, а не по удобству — иначе стору
// пришлось бы знать бакет ради одного метода.
type LibraryNote struct {
	FileId      int    `db:"id"`
	FileName    string `db:"file_name"`
	ContentType string `db:"content_type"`
	// ObjectKey is the key of the CURRENT text object. It changes on every save — an object key is
	// never reused (0312) — so a client that cached one is holding history, not the note.
	ObjectKey string `db:"object_key"`
	// Sha256 is the fingerprint of exactly that object, and the base an editor sends back when it
	// saves. Without it there is no CAS and every save is a blind overwrite.
	Sha256    string `db:"sha256"`
	SizeBytes int64  `db:"size_bytes"`
	// ContentUpdatedBy / ContentUpdatedAt — «правил {кто} · {когда}» в шапке чтения. Пустые у
	// заметки, залитой файлом и ни разу не сохранённой через редактор; шапка тогда печатает
	// UploadedBy, который для такого файла и есть правда.
	ContentUpdatedBy string       `db:"content_updated_by"`
	ContentUpdatedAt sql.NullTime `db:"content_updated_at"`
	UploadedBy       string       `db:"uploaded_by"`
}

// LibraryNoteSave is one compare-and-set write of a note's text.
//
// БАЙТЫ УЖЕ В БАКЕТЕ, КОГДА ЭТА СТРУКТУРА ДОХОДИТ ДО СТОРА. Порядок обязателен и обратный
// удалению: заливка нового объекта под НОВЫЙ ключ → транзакция со строкой → и только после коммита
// best-effort удаление старого ключа. Упавшая заливка не трогает строку вообще, а осиротевший
// объект стоит дешевле потерянного текста. Поэтому вызывающий приносит сюда ключ, отпечаток и
// размер — стор их не выводит и в бакет не ходит.
type LibraryNoteSave struct {
	FileId int
	// BaseSha256 is the fingerprint the edit started from. Mismatch = somebody saved in between.
	// Пустая строка — это ТОЖЕ база (заметка без содержимого), а не «проверку пропустить»:
	// пропуск проверки называется Force и просится явно.
	BaseSha256 string
	// Force writes over that other version deliberately. Это единственный путь к потерянной правке
	// во всём разделе — ровно поэтому он просится явно и никогда не является умолчанием.
	Force bool
	// ObjectKey / Sha256 / SizeBytes describe the object the caller has already uploaded.
	ObjectKey string
	Sha256    string
	SizeBytes int64
	// ContentExcerpt is derived by the caller from the same text; the store copies it onto the row.
	ContentExcerpt string
	// EditedBy is the username stamped into content_updated_by — a string, like every other «кто» in
	// the library, so it survives the account.
	EditedBy string
}

// LibraryNoteSaveResult is what a CAS write answers. Конфликт — это ДАННЫЕ, а не ошибка: клиент
// обязан построить по нему баннер и три исхода («показать различия», «сохранить отдельной
// заметкой», «всё равно записать поверх»), и статус ошибки стоил бы второго запроса на каждый из них.
type LibraryNoteSaveResult struct {
	// Conflict = true означает, что НЕ ЗАПИСАНО НИЧЕГО, а поля ниже описывают версию, которая лежит.
	Conflict bool
	// CurrentSha256 is what is stored now: the caller's own text after a successful save, the other
	// version after a conflict. Either way it is the base for this editor's NEXT save, so the client
	// never has to re-read to keep editing.
	CurrentSha256 string
	// CurrentObjectKey is filled ONLY on conflict, and it is what the caller reads the other
	// version's text from (the store has no bytes to hand over — see LibraryNote).
	CurrentObjectKey string
	// PreviousObjectKey is filled ONLY on success: the key that was just replaced, for best-effort
	// deletion AFTER the commit. On conflict it is empty and the caller deletes the object IT
	// uploaded — that one is now an orphan, and only the caller knows its key.
	PreviousObjectKey string
	LastEditedBy      string
	LastEditedAt      sql.NullTime
	// UpdatedAt is the row's stamp after a successful write; zero on conflict.
	UpdatedAt time.Time
}
