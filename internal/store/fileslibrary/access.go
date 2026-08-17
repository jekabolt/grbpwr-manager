package fileslibrary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Ф7 — ДОСТУП К ФАЙЛУ (0317): уровень, поимённый список, публичная ссылка, журнал и витрина.
//
// ПРЕДИКАТ ВИДИМОСТИ ВОШЁЛ СЮДА ТЕМ ЖЕ БИЛДЕРОМ (Viewer.Where, visibility.go) в ДВЕ точки:
//
//   - GetFileAccess — точка 12: блок доступа не показывается тому, кто не видит файла;
//   - ListSharedFiles — витрина под тем же предикатом, супер видит всё.
//
// Всё остальное в файле предиката не требует:
// SetFileAccess/RotateFileLink зовёт хендлер, уже проверивший круг (загрузивший|владелец|супер)
// на прочитанном файле, а GetFileByPublicLink — единственное чтение библиотеки, у которого
// зовущего нет вовсе (см. dependency.Files).

const (
	// Потолки журнала. Журнал читают, чтобы ответить «кто это открыл», и ответ почти всегда в
	// последних строках — страница на полсотни записей закрывает вопрос, а потолок держит
	// файл с тысячей правок доступа в границах одного ответа.
	defaultAccessEventLimit = 50
	maxAccessEventLimit     = 200

	// maxAccessEventWhatLen — ширина колонки `what` (VARCHAR(255)). Строка собирается из
	// username'ов, то есть из данных, и обрезается ЗДЕСЬ: без обрезки длинный список имён
	// уронил бы запись в strict-режиме, то есть смена доступа провалилась бы целиком из-за
	// журнала — худший из возможных разменов.
	maxAccessEventWhatLen = 255
	// maxAccessEventNames — сколько имён печатается в строке журнала до “and N more”.
	maxAccessEventNames = 8
)

// publicAccessColumns — строка публичной ссылки целиком. Перечислены явно, а не `*`: тип узкий
// и стабильный, а `*` привязал бы скан к порядку колонок в миграции.
const publicAccessColumns = `file_id, epoch, expires_at, revoked_at, last_access_at, access_count, created_at, updated_at`

// levelRow нужен потому, что storeutil.QueryNamedOne сканирует СТРУКТУРУ (StructScan на
// скаляре паникует — см. QueryScalarListNamed). Одноколоночная структура дешевле, чем
// отдельный путь чтения.
type levelRow struct {
	Level entity.LibraryFileAccessLevel `db:"access_level"`
}

// GetFileAccess returns the file's whole access state: level, named people, link row.
//
// ТОЧКА 12 ПРЕДИКАТА. Гейт стоит ОТДЕЛЬНЫМ чтением перед readAccess, а не внутри него, и это
// разделение содержательное: readAccess зовут ещё и после записи, где предикат не нужен и вреден
// (см. его комментарий). Отказ обязан быть ИМЕННО sql.ErrNoRows — хендлер делает из него
// NotFound; отдай мы пустую структуру без ошибки, точка 12 молча открылась бы, а блок доступа
// подтвердил бы существование ограниченного файла самим кодом ответа.
func (s *Store) GetFileAccess(ctx context.Context, fileID int) (*entity.LibraryFileAccess, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	if err := EnsureVisible(ctx, s.DB, v, fileID); err != nil {
		return nil, err // sql.ErrNoRows нетронутым
	}
	return s.readAccess(ctx, s.DB, fileID)
}

// readAccess собирает состояние доступа из трёх чтений. Отдельно от GetFileAccess намеренно:
// сюда зовут пути, которые ТОЛЬКО ЧТО ПИСАЛИ (SetFileAccess перечитывает результат), и предикат
// видимости в них не нужен — писавший файл видел по определению. Предикат идёт в GetFileAccess.
func (s *Store) readAccess(ctx context.Context, db dependency.DB, fileID int) (*entity.LibraryFileAccess, error) {
	lvl, err := storeutil.QueryNamedOne[levelRow](ctx, db,
		`SELECT access_level FROM library_file WHERE id = :id`, map[string]any{"id": fileID})
	if err != nil {
		return nil, err // sql.ErrNoRows проходит нетронутым — карточка на удалённом файле скажет NotFound
	}
	people, err := loadAccessPeople(ctx, db, []int{fileID})
	if err != nil {
		return nil, err
	}
	link, err := readPublicAccess(ctx, db, fileID)
	if err != nil {
		return nil, err
	}
	return &entity.LibraryFileAccess{
		FileId: fileID,
		Level:  lvl.Level,
		People: people[fileID],
		Link:   link,
	}, nil
}

// readPublicAccess отдаёт строку публичной ссылки или nil, если файл ни разу не делили ссылкой.
// Отсутствие строки — не ошибка: у большинства файлов её нет и не будет.
func readPublicAccess(ctx context.Context, db dependency.DB, fileID int) (*entity.LibraryFilePublicAccess, error) {
	row, err := storeutil.QueryNamedOne[entity.LibraryFilePublicAccess](ctx, db,
		`SELECT `+publicAccessColumns+` FROM library_file_public_access WHERE file_id = :id`,
		map[string]any{"id": fileID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read library file public access: %w", err)
	}
	return &row, nil
}

// loadAccessPeople resolves the named lists of a WHOLE page in two queries — links, then the
// specialties of everybody who turned up. Форма ровно как у attachOwners: витрина рисует
// страницу, и запрос на файл был бы round-trip на плитку.
func loadAccessPeople(ctx context.Context, db dependency.DB, fileIDs []int) (map[int][]entity.AdminRef, error) {
	out := map[int][]entity.AdminRef{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	type row struct {
		FileId int `db:"file_id"`
		entity.AdminRef
	}
	rows, err := storeutil.QueryListNamed[row](ctx, db, `
		SELECT lfap.file_id, a.id, a.username, a.is_super
		FROM library_file_access_people lfap
		JOIN admins a ON a.id = lfap.admin_id
		WHERE lfap.file_id IN (:ids)
		ORDER BY a.username`, map[string]any{"ids": fileIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load library file access people: %w", err)
	}
	if len(rows) == 0 {
		return out, nil
	}
	adminIDs := make([]int, 0, len(rows))
	seen := make(map[int]bool, len(rows))
	for _, r := range rows {
		if !seen[r.Id] {
			seen[r.Id] = true
			adminIDs = append(adminIDs, r.Id)
		}
	}
	specialties, err := storeutil.LoadAdminSpecialties(ctx, db, adminIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		p := r.AdminRef
		p.Specialties = specialties[r.Id]
		out[r.FileId] = append(out[r.FileId], p)
	}
	return out, nil
}

// SetFileAccess applies the level AND the list that goes with it in ONE SERIALIZABLE
// transaction, and writes the journal for everything that actually changed.
//
// ЧТО ЗАМЕЩАЕТСЯ, А ЧТО НЕТ. Список людей замещается ТОЛЬКО на уровне `people` — это его
// единственный уровень, и обнуление списка при уходе на `team` заставило бы человека набирать
// его заново после каждого «показать всем на минуту». Строка публичной ссылки ПЕРЕЖИВАЕТ уход с
// уровня — но перестаёт быть той же ссылкой.
//
// УХОД С `link` — ЭТО ОТЗЫВ, А ВОЗВРАТ — НОВАЯ ССЫЛКА (Ф7b). Раньше epoch не двигался ни там, ни
// там, и ссылка бывшего подрядчика оживала ровно в тот момент, когда файл снова открывали по
// ссылке — совсем другим людям и, как правило, годы спустя. «Вернули уровень» человек не читает
// как «снова раздали ту же ссылку», а журнал писал об этом одну строку `level:link`, из которой
// такого вывода не сделать. Теперь: уход с `link` штампует revoked_at (ссылка выключена), возврат
// двигает поколение и заводит НОВЫЙ токен, и обе стороны названы в журнале отдельными строками.
func (s *Store) SetFileAccess(ctx context.Context, fileID int, u entity.LibraryFileAccessUpdate) (*entity.LibraryFileAccess, error) {
	if !entity.ValidLibraryFileAccessLevels[u.Level] {
		// Неизвестный уровень ОТКАЗЫВАЕТ, а не толкуется: «непонятный = team» тихо расширил бы
		// доступ, «= people» — тихо потерял бы файл.
		return nil, fmt.Errorf("unknown library file access level %q", u.Level)
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Существование проверяем ВНУТРИ транзакции: пишущие транзакции стора идут в
		// SERIALIZABLE, поэтому проверка действительно закрывает гонку с удалением файла.
		before, err := storeutil.QueryNamedOne[levelRow](ctx, db,
			`SELECT access_level FROM library_file WHERE id = :id`, map[string]any{"id": fileID})
		if err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE library_file SET access_level = :level WHERE id = :id`,
			map[string]any{"id": fileID, "level": string(u.Level)}); err != nil {
			return fmt.Errorf("failed to set library file access level: %w", err)
		}
		if before.Level != u.Level {
			// СОБЫТИЕ УРОВНЯ — ЕДИНСТВЕННОЕ МАШИНОЧИТАЕМОЕ В ЖУРНАЛЕ (см. миграцию 0317 и
			// entity.LibraryFileAccessLevelEventPrefix): по нему витрина отвечает «кто открыл».
			if err := appendAccessEvent(ctx, db, fileID, u.Actor, levelEventWhat(u.Level)); err != nil {
				return err
			}
		}
		if u.Level == entity.LibraryFileAccessPeople {
			if err := replaceAccessPeople(ctx, db, fileID, u.AdminIDs, u.Actor); err != nil {
				return err
			}
		}
		if u.Level == entity.LibraryFileAccessLink {
			// reissue = «файл ВЕРНУЛСЯ на link». Только в этом случае поколение двигается:
			// правка одного лишь срока на живом уровне обязана оставить выданную ссылку живой,
			// иначе чип «7 дней» убивал бы ссылку, которую только что разослали.
			if err := applyLinkTTL(ctx, db, fileID, u.LinkTTLHours, u.Actor,
				before.Level != entity.LibraryFileAccessLink); err != nil {
				return err
			}
		} else if before.Level == entity.LibraryFileAccessLink {
			// Ушли с `link`: ссылка выключена. Маршрут и так мёртв (он сверяет access_level на
			// строке файла), но штамп — единственное, что делает состояние ЧИТАЕМЫМ: без него
			// колонка revoked_at не заполнялась нигде, `revoked` на проводе был вечным false, а
			// ветка отзыва в маршруте — мёртвым кодом.
			if err := revokePublicLink(ctx, db, fileID, u.Actor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Перечитываем ПОСЛЕ коммита: карточка обязана перерисоваться по тому, что лежит, а не по
	// тому, что она надеялась отправить.
	return s.readAccess(ctx, s.DB, fileID)
}

// replaceAccessPeople замещает поимённый список и пишет журнал по РАЗНИЦЕ, а не по факту
// записи: «сохранил тот же список» не событие, и журнал, полный таких строк, перестают читать.
func replaceAccessPeople(ctx context.Context, db dependency.DB, fileID int, adminIDs []int, actor string) error {
	type personRow struct {
		Id       int    `db:"id"`
		Username string `db:"username"`
	}
	beforeRows, err := storeutil.QueryListNamed[personRow](ctx, db,
		`SELECT a.id, a.username FROM library_file_access_people lfap
		 JOIN admins a ON a.id = lfap.admin_id
		 WHERE lfap.file_id = :id ORDER BY a.username`, map[string]any{"id": fileID})
	if err != nil {
		return fmt.Errorf("failed to read library file access people: %w", err)
	}
	before := make(map[int]string, len(beforeRows))
	for _, r := range beforeRows {
		before[r.Id] = r.Username
	}

	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM library_file_access_people WHERE file_id = :id`,
		map[string]any{"id": fileID}); err != nil {
		return fmt.Errorf("failed to clear library file access people: %w", err)
	}
	after := map[int]string{}
	if len(adminIDs) > 0 {
		rows := make([]map[string]any, 0, len(adminIDs))
		for _, id := range adminIDs {
			rows = append(rows, map[string]any{"file_id": fileID, "admin_id": id, "added_by": actor})
		}
		// Несуществующий аккаунт обязан УПАСТЬ внешним ключом: «доступ выдан» человеку,
		// которого нет, — худший ответ, потому что в списке он будет, а увидеть файл некому.
		if err := storeutil.BulkInsert(ctx, db, "library_file_access_people", rows); err != nil {
			return fmt.Errorf("failed to link library file access people: %w", err)
		}
		afterRows, err := storeutil.QueryListNamed[personRow](ctx, db,
			`SELECT a.id, a.username FROM library_file_access_people lfap
			 JOIN admins a ON a.id = lfap.admin_id
			 WHERE lfap.file_id = :id ORDER BY a.username`, map[string]any{"id": fileID})
		if err != nil {
			return fmt.Errorf("failed to read back library file access people: %w", err)
		}
		for _, r := range afterRows {
			after[r.Id] = r.Username
		}
	}

	var added, removed []string
	for id, name := range after {
		if _, ok := before[id]; !ok {
			added = append(added, name)
		}
	}
	for id, name := range before {
		if _, ok := after[id]; !ok {
			removed = append(removed, name)
		}
	}
	if what := namesEventWhat("+ ", added); what != "" {
		if err := appendAccessEvent(ctx, db, fileID, actor, what); err != nil {
			return err
		}
	}
	if what := namesEventWhat("- ", removed); what != "" {
		if err := appendAccessEvent(ctx, db, fileID, actor, what); err != nil {
			return err
		}
	}
	return nil
}

// applyLinkTTL заводит (или обновляет) строку публичной ссылки под уровень `link`.
//
// revoked_at СНИМАЕТСЯ. Уровень `link` — это явное «файл открыт по ссылке»; строка, оставшаяся
// отозванной, отдавала бы свежесобранный url, который мёртв, и починить это из панели было бы
// нечем (кнопки «снять отзыв» в макете нет — есть «пересоздать»).
//
// reissue ДВИГАЕТ ПОКОЛЕНИЕ, и это главное отличие от прежнего поведения. Он приходит true ровно
// тогда, когда файл ВЕРНУЛСЯ на `link` с другого уровня: всё это время маршрут отвечал 404, ссылку
// у бывшего подрядчика все считали мёртвой, и оживлять её возвратом уровня нельзя. Правка одного
// лишь срока (уровень уже был `link`) поколение не трогает — иначе смена чипа убивала бы ссылку,
// которую только что разослали.
//
// Строки может не быть вовсе — тогда двигать нечего: ни одного токена по ней не выдавали, потому
// что url собирается ИЗ неё (Service.LinkURL по link.Epoch), а не из воздуха.
func applyLinkTTL(ctx context.Context, db dependency.DB, fileID, ttlHours int, actor string, reissue bool) error {
	prev, err := readPublicAccess(ctx, db, fileID)
	if err != nil {
		return err
	}
	var expires sql.NullTime
	if ttlHours > 0 {
		expires = sql.NullTime{Time: time.Now().UTC().Add(time.Duration(ttlHours) * time.Hour), Valid: true}
	}
	bump := 0
	if reissue && prev != nil {
		bump = 1
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO library_file_public_access (file_id, expires_at)
		VALUES (:id, :expires)
		ON DUPLICATE KEY UPDATE expires_at = VALUES(expires_at), revoked_at = NULL,
			epoch = epoch + :bump`,
		map[string]any{"id": fileID, "expires": expires, "bump": bump}); err != nil {
		return fmt.Errorf("failed to upsert library file public access: %w", err)
	}
	if bump == 1 {
		// Строка журнала обязана НАЗЫВАТЬ это: “access by link” рядом ничего не говорит о том,
		// что прежний адрес умер, а именно этого человек и не ожидает от возврата уровня.
		if err := appendAccessEvent(ctx, db, fileID, actor,
			"the link was reissued, the previous one no longer works"); err != nil {
			return err
		}
	}
	if linkTTLChanged(prev, expires) {
		if err := appendAccessEvent(ctx, db, fileID, actor, ttlEventWhat(ttlHours)); err != nil {
			return err
		}
	}
	return nil
}

// revokePublicLink штампует revoked_at, когда файл уходит с уровня `link`.
//
// ЗАЧЕМ ВООБЩЕ, ЕСЛИ МАРШРУТ И ТАК ОТВЕЧАЕТ 404. Затем, что состояние «ссылка была и её выключили»
// иначе нигде не записано: колонка revoked_at до Ф7b только СНИМАЛАСЬ, `revoked` на проводе был
// вечным false, а ветка отзыва в маршруте — недостижимым кодом. Половина механики, которая
// выглядит работающей, хуже её отсутствия.
//
// И штамп теперь ФАКТИЧЕСКИ ВЕРЕН: раз возврат на `link` выдаёт новое поколение, каждый выданный до
// этой минуты токен мёртв НАВСЕГДА, а не до следующего включения уровня. Это и есть отзыв.
//
// Строки может не быть — файл ни разу не делили ссылкой, отзывать нечего.
func revokePublicLink(ctx context.Context, db dependency.DB, fileID int, actor string) error {
	prev, err := readPublicAccess(ctx, db, fileID)
	if err != nil {
		return err
	}
	if prev == nil || prev.RevokedAt.Valid {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db,
		`UPDATE library_file_public_access SET revoked_at = NOW() WHERE file_id = :id`,
		map[string]any{"id": fileID}); err != nil {
		return fmt.Errorf("failed to revoke library file public link: %w", err)
	}
	return appendAccessEvent(ctx, db, fileID, actor, "the link was revoked, the previous one no longer works")
}

// linkTTLChanged отвечает, стоит ли писать строку журнала о сроке.
//
// Сравнение с допуском, а не на равенство: срок вычисляется от ТЕКУЩЕГО времени, поэтому
// повторное сохранение того же чипа сдвигает дату на прошедшие секунды. Без допуска журнал
// заполнялся бы строками «срок 7 дней» при каждом сохранении блока доступа и перестал бы
// отвечать на вопрос, ради которого заведён.
func linkTTLChanged(prev *entity.LibraryFilePublicAccess, next sql.NullTime) bool {
	if prev == nil {
		return true
	}
	if prev.ExpiresAt.Valid != next.Valid {
		return true
	}
	if !next.Valid {
		return false
	}
	d := next.Time.Sub(prev.ExpiresAt.Time)
	if d < 0 {
		d = -d
	}
	return d > time.Minute
}

// RotateFileLink bumps the epoch: every token minted so far dies instantly. Отзыва без
// пересоздания в этой механике не существует — stateless HMAC нечем найти, его можно только
// пережить поколением.
//
// Строки может не быть вовсе (файл ни разу не делили ссылкой) — тогда она заводится с epoch 1:
// «пересоздать» на файле без ссылки означает «выдай ссылку», и отказ здесь был бы ответом на
// вопрос, которого никто не задавал.
func (s *Store) RotateFileLink(ctx context.Context, fileID int, actor string) (*entity.LibraryFilePublicAccess, error) {
	var out *entity.LibraryFilePublicAccess
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		exists, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM library_file WHERE id = :id`, map[string]any{"id": fileID})
		if err != nil {
			return fmt.Errorf("failed to check library file existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		// Одним оператором: SELECT-потом-UPDATE дал бы двум одновременным «пересоздать» один и
		// тот же epoch, то есть вторая кнопка не отозвала бы ничего.
		//
		// revoked_at НЕ СНИМАЕТСЯ, и это не забывчивость. С Ф7b он значит ровно одно — «файл не на
		// уровне link, ссылка выключена», — и снимается ровно там, где это перестаёт быть правдой
		// (applyLinkTTL, возврат на уровень). «Пересоздать» на файле не по ссылке законно (поколение
		// уезжает, старые токены мертвы), но включением уровня оно не является: сняв здесь штамп, мы
		// получили бы `revoked = false` на файле, у которого ссылка мертва по уровню, — то самое
		// состояние, которое врёт панели.
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO library_file_public_access (file_id) VALUES (:id)
			ON DUPLICATE KEY UPDATE epoch = epoch + 1`,
			map[string]any{"id": fileID}); err != nil {
			return fmt.Errorf("failed to rotate library file link: %w", err)
		}
		if err := appendAccessEvent(ctx, db, fileID, actor,
			"the link was recreated, the previous one no longer works"); err != nil {
			return err
		}
		row, err := readPublicAccess(ctx, db, fileID)
		if err != nil {
			return err
		}
		if row == nil {
			return sql.ErrNoRows
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListFileAccessEvents returns one file's journal, newest first.
//
// ПРЕДИКАТ СТОИТ ВНУТРИ МЕТОДА, А НЕ ПЕРЕД НИМ. Сегодня журнал читает ровно один вызывающий —
// GetLibraryFileAccess, уже прошедший точку 12, — и без этой проверки метод безопасен ТОЛЬКО
// поэтому. Но «его зовут в правильном порядке» — свойство вызывающего, а не метода: второй
// вызывающий (витрина, экспорт, отладочная ручка) появится без единого предупреждения и получит
// готовое перечисление того, кому и когда открывали невидимый ему файл — с именами.
func (s *Store) ListFileAccessEvents(ctx context.Context, fileID, limit int) ([]entity.LibraryFileAccessEvent, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	if err := EnsureVisible(ctx, s.DB, v, fileID); err != nil {
		return nil, err // sql.ErrNoRows нетронутым: невидимый и несуществующий неразличимы
	}
	if limit <= 0 {
		limit = defaultAccessEventLimit
	}
	limit = min(limit, maxAccessEventLimit)
	events, err := storeutil.QueryListNamed[entity.LibraryFileAccessEvent](ctx, s.DB, `
		SELECT id, file_id, actor, actor_id, what, created_at
		FROM library_file_access_event
		WHERE file_id = :id
		ORDER BY id DESC
		LIMIT :limit`, map[string]any{"id": fileID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("failed to list library file access events: %w", err)
	}
	return events, nil
}

// ListSharedFiles is the витрина: everything that is `people` or `link` right now.
//
// ТОЧКА 6 ПРЕДИКАТА: фрагмент видимости входит в общий WHERE ниже — и в счёт, и в страницу одним
// и тем же условием (переменная `where`), иначе «3 из 40» не будет значить ни одного из двух
// чисел. Витрина показывает ровно то, что человек и так видит: `link` виден всем, `people` —
// только своему кругу, супер видит всё и лечит там осиротевшие файлы.
func (s *Store) ListSharedFiles(ctx context.Context, f entity.SharedLibraryFileFilter) ([]entity.SharedLibraryFile, int, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, 0, err
	}
	params := map[string]any{}
	where := `lf.access_level IN ('people', 'link')`
	switch f.Level {
	case "":
	case entity.LibraryFileAccessPeople, entity.LibraryFileAccessLink:
		where = `lf.access_level = :level`
		params["level"] = string(f.Level)
	default:
		// `team` — не фильтр витрины, а её отрицание: витрина показывает то, что НЕ по умолчанию.
		return nil, 0, fmt.Errorf("shared files filter accepts only people or link, got %q", f.Level)
	}
	where += ` AND ` + v.Where("lf", params)

	limit := f.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	limit = min(limit, maxPageLimit)
	offset := max(f.Offset, 0)

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM library_file lf WHERE `+where, countParams(params))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count shared library files: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}
	params["limit"] = limit
	params["offset"] = offset
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT lf.* FROM library_file lf WHERE `+where+
			` ORDER BY lf.created_at DESC, lf.id DESC LIMIT :limit OFFSET :offset`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list shared library files: %w", err)
	}
	if err := s.attachRelatedSlice(ctx, files); err != nil {
		return nil, 0, err
	}

	ids := make([]int, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.Id)
	}
	people, err := loadAccessPeople(ctx, s.DB, ids)
	if err != nil {
		return nil, 0, err
	}
	links, err := loadPublicAccessRows(ctx, s.DB, ids)
	if err != nil {
		return nil, 0, err
	}
	shared, err := loadSharedBy(ctx, s.DB, ids)
	if err != nil {
		return nil, 0, err
	}

	out := make([]entity.SharedLibraryFile, 0, len(files))
	for _, file := range files {
		row := entity.SharedLibraryFile{
			File:   file,
			People: people[file.Id],
			Link:   links[file.Id],
		}
		if ev, ok := shared[file.Id]; ok {
			row.SharedBy = ev.Actor
			row.SharedAt = sql.NullTime{Time: ev.CreatedAt, Valid: true}
		}
		out = append(out, row)
	}
	return out, total, nil
}

// countParams отдаёт копию параметров без limit/offset. Считать и выбирать ОДНИМ выражением
// where — единственный способ, чтобы «N из M» описывало одну и ту же выборку; общая карта
// параметров с лишними ключами упала бы на sqlx.In, а не тихо разъехалась.
func countParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// loadPublicAccessRows читает строки ссылок для всей страницы одним запросом.
func loadPublicAccessRows(ctx context.Context, db dependency.DB, fileIDs []int) (map[int]*entity.LibraryFilePublicAccess, error) {
	out := map[int]*entity.LibraryFilePublicAccess{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.LibraryFilePublicAccess](ctx, db,
		`SELECT `+publicAccessColumns+` FROM library_file_public_access WHERE file_id IN (:ids)`,
		map[string]any{"ids": fileIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load library file public access rows: %w", err)
	}
	for i := range rows {
		out[rows[i].FileId] = &rows[i]
	}
	return out, nil
}

// sharedByRow — ответ на «кто открыл»: actor и время ПОСЛЕДНЕГО события, установившего ТЕКУЩИЙ
// уровень файла.
type sharedByRow struct {
	FileId    int       `db:"file_id"`
	Actor     string    `db:"actor"`
	CreatedAt time.Time `db:"created_at"`
}

// loadSharedBy отвечает на «кто открыл» ОДНИМ группированным запросом на страницу — это ровно
// то место, где N+1 заводится незаметно.
//
// Префикс уровня приезжает ПАРАМЕТРОМ, а не литералом: он содержит двоеточие, а sqlx-сканер
// именованных параметров не пропускает строковые литералы SQL и читает ':' как пустое имя
// (см. storeutil.makeQuery). Литерал 'level' + CHAR(58) работал бы тоже, но параметр держит
// формат там же, где он объявлен, — в entity.LibraryFileAccessLevelEventPrefix.
//
// Пусто там, где уровень старше журнала: файл, лежавший в `people` до 0317, события иметь не
// может, и витрина честно показывает пустую колонку вместо выдуманного имени.
func loadSharedBy(ctx context.Context, db dependency.DB, fileIDs []int) (map[int]sharedByRow, error) {
	out := map[int]sharedByRow{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[sharedByRow](ctx, db, `
		SELECT e.file_id, e.actor, e.created_at
		FROM library_file_access_event e
		JOIN (
			SELECT ev.file_id AS file_id, MAX(ev.id) AS last_id
			FROM library_file_access_event ev
			JOIN library_file lf ON lf.id = ev.file_id
			WHERE ev.file_id IN (:ids)
			  AND ev.what LIKE CONCAT(:levelPrefix, lf.access_level, '%')
			GROUP BY ev.file_id
		) last ON last.last_id = e.id`,
		map[string]any{"ids": fileIDs, "levelPrefix": entity.LibraryFileAccessLevelEventPrefix})
	if err != nil {
		return nil, fmt.Errorf("failed to load shared-by events: %w", err)
	}
	for _, r := range rows {
		out[r.FileId] = r
	}
	return out, nil
}

// GetFileByPublicLink is the narrow read behind GET|HEAD /api/f/{token} — единственное чтение
// библиотеки без предиката видимости, потому что у публичного маршрута нет зовущего.
//
// ВОЗВРАЩАЕТ УРОВЕНЬ, А НЕ ФИЛЬТРУЕТ ПО НЕМУ. Проверку `access_level = 'link'` делает маршрут:
// так отказ «уровень сменили» отличим в аудите от «строки нет» и от «epoch устарел», хотя
// наружу все три — один и тот же голый 404. Фильтр в SQL стёр бы эту разницу в логах.
func (s *Store) GetFileByPublicLink(ctx context.Context, fileID int) (*entity.LibraryFileLinkTarget, error) {
	row, err := storeutil.QueryNamedOne[entity.LibraryFileLinkTarget](ctx, s.DB, `
		SELECT lf.id, lf.file_name, lf.content_type, lf.size_bytes, lf.object_key, lf.access_level,
		       pa.epoch, pa.expires_at, pa.revoked_at
		FROM library_file lf
		JOIN library_file_public_access pa ON pa.file_id = lf.id
		WHERE lf.id = :id`, map[string]any{"id": fileID})
	if err != nil {
		return nil, err // sql.ErrNoRows нетронутым: маршрут отвечает на него тем же 404
	}
	return &row, nil
}

// RecordPublicAccess folds a debounced batch of public-route hits into the rows. Best effort:
// потерянная пачка хуже отвечает на вопрос «пользуются ли ссылкой» и не значит больше ничего.
//
// Пишет по строке на файл и возвращает ПЕРВУЮ ошибку, дописав остальные, — как
// RecordRunPackAccess: пачка сбрасывается из фонового тикера, и остановиться на первой
// неудаче значило бы потерять статистику всех остальных файлов из-за одного.
func (s *Store) RecordPublicAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error {
	var firstErr error
	for id, n := range counts {
		if n <= 0 {
			continue
		}
		query := `UPDATE library_file_public_access
			SET access_count = access_count + :n, last_access_at = :last WHERE file_id = :id`
		params := map[string]any{"id": id, "n": n, "last": last[id]}
		if t, ok := last[id]; !ok || t.IsZero() {
			// Нулевого времени в TIMESTAMP быть не должно: strict-режим отвергает
			// '0000-00-00', и весь UPDATE (вместе со счётчиком) не применился бы.
			query = `UPDATE library_file_public_access
				SET access_count = access_count + :n WHERE file_id = :id`
			params = map[string]any{"id": id, "n": n}
		}
		if err := storeutil.ExecNamed(ctx, s.DB, query, params); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to record library file public access: %w", err)
		}
	}
	return firstErr
}

// appendAccessEvent пишет одну строку журнала.
//
// actor_id ВЫВОДИТСЯ ИЗ ИМЕНИ ОДНИМ ОПЕРАТОРОМ — тот же приём и тот же довод, что у
// uploaded_by_id файла (0314) и author_id реплики (0316): две половины авторства не должны
// иметь возможности разойтись, поэтому вызывающему нечем прислать их порознь. NULL, если
// аккаунта с таким именем нет, — строка actor остаётся фактом.
func appendAccessEvent(ctx context.Context, db dependency.DB, fileID int, actor, what string) error {
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO library_file_access_event (file_id, actor, actor_id, what)
		VALUES (:fileId, :actor, (SELECT a.id FROM admins a WHERE a.username = :actor), :what)`,
		map[string]any{"fileId": fileID, "actor": actor, "what": truncateRunes(what, maxAccessEventWhatLen)}); err != nil {
		return fmt.Errorf("failed to append library file access event: %w", err)
	}
	return nil
}

// levelEventWhat собирает МАШИНОЧИТАЕМУЮ голову события уровня и человеческий хвост за ней.
// Голова — контракт с витриной (см. миграцию 0317), хвост — то, что читает человек.
func levelEventWhat(level entity.LibraryFileAccessLevel) string {
	tail := ""
	switch level {
	case entity.LibraryFileAccessTeam:
		tail = " the whole team has access"
	case entity.LibraryFileAccessPeople:
		tail = " access for a named list"
	case entity.LibraryFileAccessLink:
		tail = " access by link"
	}
	return entity.LibraryFileAccessLevelEventPrefix + string(level) + tail
}

// ttlEventWhat печатает срок так, как он выбран чипом, а не в часах: “7 days” — это то, что
// человек нажал, и то, что он будет искать в журнале.
func ttlEventWhat(hours int) string {
	switch {
	case hours <= 0:
		return "link expiry: never"
	case hours == 24:
		return "link expiry: 1 day"
	case hours%24 == 0:
		return fmt.Sprintf("link expiry: %d days", hours/24)
	case hours == 1:
		return "link expiry: 1 hour"
	default:
		return fmt.Sprintf("link expiry: %d hours", hours)
	}
}

// namesEventWhat собирает «+ имя, имя» или «- имя», обрезая длинный список до “and N more”.
// Пустой список — пустая строка, и события не будет вовсе.
func namesEventWhat(prefix string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	// Стабильный порядок имён: разница множеств собирается обходом карты, а он в Go случаен —
	// без сортировки одна и та же правка печаталась бы в журнале каждый раз иначе.
	slices.Sort(names)
	shown := names
	extra := 0
	if len(shown) > maxAccessEventNames {
		extra = len(shown) - maxAccessEventNames
		shown = shown[:maxAccessEventNames]
	}
	what := prefix + strings.Join(shown, ", ")
	if extra > 0 {
		what += fmt.Sprintf(" and %d more", extra)
	}
	return truncateRunes(what, maxAccessEventWhatLen)
}

// truncateRunes режет по РУНАМ, а не по байтам: колонка объявлена в символах, а имена здесь
// кириллические, и байтовая обрезка оставила бы битую руну на хвосте.
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}
