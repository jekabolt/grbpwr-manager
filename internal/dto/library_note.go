package dto

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// MARKDOWN-ЗАМЕТКА НА ТРАНСПОРТЕ (Ф8).
//
// Заметка — обычный файл библиотеки, поэтому здесь нет ни одного своего конвертера файла: сам файл
// едет тем же ConvertEntityLibraryFileToPb, что и всё остальное. Своё у заметки ровно то, что
// связано с ТЕКСТОМ: имя с расширением, потолок, выдержка для плитки и два ответа с содержимым.

const (
	// LibraryNoteContentType is what a note stores as its MIME type. Именно оно, а не text/plain:
	// по нему клиент решает открыть заметку редактором, а не читалкой Ф6.
	LibraryNoteContentType = "text/markdown"
	// LibraryNoteExtension is the extension the SERVER appends. Спрашивать расширение у человека,
	// который пишет заметку, незачем — он пишет текст, а не выбирает формат хранения.
	LibraryNoteExtension = "md"

	// maxLibraryNoteExcerptRunes bounds the tile preview. Колонка держит 500 символов, и запас до
	// неё намеренный: выдержка это несколько первых строк, а не начало текста «сколько влезет».
	maxLibraryNoteExcerptRunes = 400
	// excerptEllipsis marks a preview that was cut. Без него оборванная на полуслове выдержка
	// читается как весь текст заметки.
	excerptEllipsis = "…"
)

// noteFileExtensions are the names that make a file a note regardless of the MIME its uploader
// declared. Браузер для .md присылает то text/markdown, то ничего вовсе, поэтому одного
// content_type мало.
var noteFileExtensions = map[string]bool{".md": true, ".markdown": true}

// noteContentTypes are the MIMEs a note screen may open. text/plain здесь потому, что .md, залитый
// файлом, часто приезжает именно им, и отказ открыть собственный текстовый файл выглядел бы поломкой.
var noteContentTypes = map[string]bool{
	"text/markdown":   true,
	"text/x-markdown": true,
	"text/plain":      true,
}

// LibraryNoteFileName normalises the name a note is created under: the person types a title, the
// server appends the extension. Дважды `.md` не дописывается — человек, набравший его сам, получает
// ровно то, что набрал.
func LibraryNoteFileName(name string) (string, error) {
	base, err := ValidateLibraryFileName(name)
	if err != nil {
		return "", err
	}
	if hasNoteExtension(base) {
		return base, nil
	}
	full := base + "." + LibraryNoteExtension
	// Повторная проверка длины: имя ровно в 255 символов законно само по себе, но с расширением уже
	// не влезает в колонку, и обрезать его молча значило бы переименовать чужую заметку.
	if len([]rune(full)) > maxLibraryFileNameLen {
		return "", fmt.Errorf("file name must be at most %d characters (the server appends .%s)",
			maxLibraryFileNameLen-len("."+LibraryNoteExtension), LibraryNoteExtension)
	}
	return full, nil
}

// IsLibraryNoteFile reports whether this stored file may be opened by the note screen. Проверяется
// И тип, И расширение: ни одного из них по отдельности не хватает.
func IsLibraryNoteFile(contentType, fileName string) bool {
	if hasNoteExtension(strings.TrimSpace(fileName)) {
		return true
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return noteContentTypes[ct]
}

func hasNoteExtension(name string) bool {
	lower := strings.ToLower(name)
	i := strings.LastIndexByte(lower, '.')
	if i < 0 {
		return false
	}
	return noteFileExtensions[lower[i:]]
}

// ValidateLibraryNoteContent bounds the text. Потолок В БАЙТАХ, а не в символах, потому что он про
// то, сколько текста ездит по RPC — туда, обратно и ещё раз в ответе конфликта.
//
// Отказ, а не обрезка: сохранённая половина заметки выглядит как текст, который человек сам
// укоротил, и следующий же сохранённый снимок закрепил бы потерю.
func ValidateLibraryNoteContent(content string) error {
	if len(content) > entity.MaxLibraryNoteBytes {
		return fmt.Errorf("the note is %d bytes, which is more than one holds (limit %d) — this is a note, not a book",
			len(content), entity.MaxLibraryNoteBytes)
	}
	if !utf8.ValidString(content) {
		// Строка протокола обязана быть валидным UTF-8: иначе она не сериализуется в JSON на шлюзе,
		// и отказ приехал бы не отсюда, а из маршалера — без единого намёка, что именно не так.
		return fmt.Errorf("the note must be valid UTF-8 text")
	}
	return nil
}

// LibraryNoteExcerpt derives the tile preview from the text.
//
// У `.md` нет первой страницы, которую можно отрисовать картинкой, поэтому плитка показывает
// НАЧАЛО ТЕКСТА. Разметка при этом снимается: «# Заголовок» на плитке должен читаться как
// «Заголовок», а не как строка с решёткой.
func LibraryNoteExcerpt(content string) string {
	var b strings.Builder
	runes := 0
	fenced := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			// Границы блока кода в превью не нужны, а его содержимое — тем более: плитка,
			// показывающая три строки чужого шелла, не говорит о заметке ничего.
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		line = stripLeadingMarkdownMarkers(line)
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
			runes++
		}
		for _, r := range line {
			if runes >= maxLibraryNoteExcerptRunes {
				return strings.TrimSpace(b.String()) + excerptEllipsis
			}
			b.WriteRune(r)
			runes++
		}
	}
	return strings.TrimSpace(b.String())
}

// stripLeadingMarkdownMarkers removes the leading syntax of one line — headings, quotes, bullets and
// the numbers of an ordered list. Внутренняя разметка (**жирный**, `код`) остаётся: она не мешает
// прочитать строку, а вычищать её значило бы писать половину парсера markdown ради превью.
func stripLeadingMarkdownMarkers(line string) string {
	for {
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(trimmed, "#"):
			trimmed = strings.TrimLeft(trimmed, "#")
		case strings.HasPrefix(trimmed, ">"):
			trimmed = strings.TrimLeft(trimmed, ">")
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "), strings.HasPrefix(trimmed, "+ "):
			trimmed = trimmed[2:]
		default:
			// Горизонтальная линейка — единственная строка, которая после снятия маркеров
			// превращается в пустоту: её и надо выбросить целиком.
			if isHorizontalRule(trimmed) {
				return ""
			}
			return strings.TrimSpace(trimmed)
		}
		line = trimmed
	}
}

// isHorizontalRule reports whether the line is only ---/***/___ .
func isHorizontalRule(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	return strings.Trim(s, string(c)) == ""
}

// ConvertEntityLibraryNoteToPb assembles the read answer: the row's stamps plus the text the caller
// has just read from the bucket. Текст приезжает ПАРАМЕТРОМ, а не полем строки, ровно потому, что
// стор в бакет не ходит — и тип entity.LibraryNote про это честен.
func ConvertEntityLibraryNoteToPb(n *entity.LibraryNote, content string) *pb_admin.GetLibraryNoteContentResponse {
	if n == nil {
		return nil
	}
	return &pb_admin.GetLibraryNoteContentResponse{
		Content: content,
		// Отпечаток берётся ИЗ СТРОКИ, а не считается по прочитанным байтам: сохранение сравнивает
		// присланную базу именно со строкой, и счёт по байтам молча разошёлся бы с ней ровно в тот
		// день, когда объект и строка перестали бы соответствовать друг другу.
		Sha256:       n.Sha256,
		LastEditedBy: n.ContentUpdatedBy,
		LastEditedAt: nullTimeToPb(n.ContentUpdatedAt),
	}
}

// ConvertEntityLibraryNoteSaveResultToPb shapes the CAS answer. currentContent is the OTHER
// version's text and is filled only on conflict; на успехе эхо собственного текста стоило бы второй
// раз того же бюджета в 512 KiB и не сообщало бы ничего.
func ConvertEntityLibraryNoteSaveResultToPb(res *entity.LibraryNoteSaveResult, currentContent string) *pb_admin.SaveLibraryNoteContentResponse {
	if res == nil {
		return nil
	}
	out := &pb_admin.SaveLibraryNoteContentResponse{
		Conflict:      res.Conflict,
		CurrentSha256: res.CurrentSha256,
		LastEditedBy:  res.LastEditedBy,
		LastEditedAt:  nullTimeToPb(res.LastEditedAt),
	}
	if res.Conflict {
		out.CurrentContent = currentContent
	}
	return out
}
