package fileslibrary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ПРЕДИКАТ ВИДИМОСТИ ФАЙЛА (Ф7, T-7.3). ЕДИНСТВЕННЫЙ В РЕПОЗИТОРИИ.
//
// Здесь и только здесь собирается SQL-условие «этот человек вправе видеть эту строку
// library_file». Все выдачи библиотеки — сетка, поиск, счётчики тем, карточка, вложения задач,
// подсказка о дубликате, лента обсуждения, витрина, заметка, запись — зовут Viewer.Where или
// Viewer.ExistsFile и НЕ СОБИРАЮТ условие сами.
//
// ПОЧЕМУ ЗАПРЕЩЁН ВТОРОЙ СПОСОБ (пункт 1 «опасных мест» плана). Две реализации одного условия
// расходятся, расходятся молча и расходятся всегда в сторону «показали лишнее»: забытое плечо
// владельца прячет файл у того, кто им распоряжается, забытая точка — показывает ИМЯ файла тому,
// от кого его прятали, а имена в этой библиотеке говорящие. Утечка здесь — это утечка имени, а не
// содержимого: отказ на открытии её не закрывает, файл обязан ПРОПАСТЬ из выдачи.
//
// ЧЕТЫРЕ ПЛЕЧА, И НИ ОДНО НЕ ЛИШНЕЕ:
//
//  1. `access_level <> 'people'` — режется ТОЛЬКО `people`. `link` внутри команды виден как
//     `team`: уровень открывает файл НАРУЖУ, а не закрывает внутрь (0317).
//  2. загрузивший — по ЖИВОЙ ссылке `uploaded_by_id`, а не по строке `uploaded_by`. Строка
//     переживает аккаунт: UNIQUE на admins.username освобождает имя при удалении, и следующий
//     однофамилец унаследовал бы доступ ко всем файлам прежнего. Ту же дыру уже закрыли в Ф3
//     (mayEditLibraryFileOwners) — здесь она закрыта тем же способом.
//  3. поимённый список `library_file_access_people`.
//  4. владельцы `library_file_owner`. Без этого плеча предикат противоречил бы сам себе: круг
//     «загрузивший | владелец | супер» распоряжается доступом файла, и файл, переключённый в
//     `people` без владельца в списке, исчез бы у человека, который его ведёт, ВМЕСТЕ с
//     возможностью это исправить.
//
// Супер (и legacy-токен) обходит предикат целиком: у него условие вырождается в `1 = 1`.
//
// ПРИНЯТОЕ СЛЕДСТВИЕ, А НЕ БАГ: счётчики тем считаются под предикатом, поэтому у разных людей они
// РАЗНЫЕ (контекст §3.4). Число, одинаковое у всех, означало бы «в этой теме есть что-то, чего
// тебе не показывают», то есть ту же утечку, только числом.

// visibilityViewerParam — имя именованного параметра под id смотрящего. Одно на весь пакет:
// вызывающий сливает карты параметров, и совпадение имени с чем-то своим сломало бы bind молча.
const visibilityViewerParam = "visViewerId"

// Viewer — кто смотрит. Собирается ОДИН раз на вызов метода стора и дальше только подставляется.
//
// AdminID = 0 означает «зовущего опознать не удалось» (нет JWT, аккаунт удалён, имя не совпало ни
// с одной строкой admins). Это НЕ ошибка и НЕ полный доступ: три личных плеча предиката просто
// перестают срабатывать, и человек видит ровно то, что видит команда, — предикат fails closed.
type Viewer struct {
	// AdminID — ЖИВАЯ ссылка admins.id, выведенная из username в JWT. Именно id, а не имя:
	// см. плечо 2 в шапке файла.
	AdminID int
	// FullAccess — супер-админ или legacy-токен: предикат вырождается в «видно всё».
	FullAccess bool
}

// ResolveViewer собирает Viewer из контекста запроса ОДНИМ индексированным запросом (username
// уникален). У супера запроса не делается вовсе — ему id не нужен.
//
// Пустое имя и неизвестный аккаунт дают нулевого Viewer'а без ошибки: «я не знаю, кто ты» — это
// законное состояние фонового вызывающего, и отвечать на него отказом значило бы уронить путь,
// который ничего лишнего и не увидит.
func ResolveViewer(ctx context.Context, db dependency.DB) (Viewer, error) {
	if az, ok := authsrv.GetAdminAuthz(ctx); ok && az.FullAccess() {
		return Viewer{FullAccess: true}, nil
	}
	username := authsrv.GetAdminUsername(ctx)
	if username == "" {
		return Viewer{}, nil
	}
	type idRow struct {
		Id int `db:"id"`
	}
	row, err := storeutil.QueryNamedOne[idRow](ctx, db,
		`SELECT a.id FROM admins a WHERE a.username = :username`,
		map[string]any{"username": username})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Viewer{}, nil
		}
		return Viewer{}, fmt.Errorf("failed to resolve library file viewer: %w", err)
	}
	return Viewer{AdminID: row.Id}, nil
}

// viewer — то же самое для методов стора, у которых уже есть хендл.
func (s *Store) viewer(ctx context.Context) (Viewer, error) {
	return ResolveViewer(ctx, s.DB)
}

// Where отдаёт условие видимости для строк library_file под псевдонимом alias и ДОПИСЫВАЕТ в
// params всё, что этому условию нужно. Возвращаемая строка — законный операнд AND: у супера это
// `1 = 1`, поэтому вызывающему не приходится ветвиться.
//
// ДВОЕТОЧИЙ В ТЕКСТЕ УСЛОВИЯ НЕТ НИ ОДНОГО, кроме настоящего именованного параметра: сканер
// sqlx не пропускает строковые литералы и читает ':' как пустое имя (см. storeutil.makeQuery).
// Литералы уровней — 'people' и только они.
//
// `<=>` (NULL-safe equal), А НЕ `=`, И ЭТО НЕ СТИЛЬ. `uploaded_by_id` NULLable (уволенный
// загрузивший), и обычное сравнение дало бы на такой строке NULL. В WHERE NULL ведёт себя как
// ложь, поэтому выдачи были бы правы; но то же условие используется ПОД ОТРИЦАНИЕМ (bulk-проверка
// AssignTopics), а `NOT NULL` — это снова NULL, и невидимый файл с уволенным загрузившим
// проскочил бы мимо отказа. NULL-safe сравнение возвращает 0/1 всегда, и отрицание точно.
func (v Viewer) Where(alias string, params map[string]any) string {
	if v.FullAccess {
		return "1 = 1"
	}
	params[visibilityViewerParam] = v.AdminID
	return fmt.Sprintf(`(%[1]s.access_level <> 'people'
		OR %[1]s.uploaded_by_id <=> :%[2]s
		OR EXISTS (SELECT 1 FROM library_file_access_people lfap_vis
			WHERE lfap_vis.file_id = %[1]s.id AND lfap_vis.admin_id = :%[2]s)
		OR EXISTS (SELECT 1 FROM library_file_owner lfo_vis
			WHERE lfo_vis.file_id = %[1]s.id AND lfo_vis.admin_id = :%[2]s))`,
		alias, visibilityViewerParam)
}

// ExistsFile — то же условие для запросов, в которых самой library_file в FROM нет: лента
// обсуждения, вложения задач. idExpr — выражение с id файла из внешнего запроса
// (`tf.file_id`, `library_file_comment.file_id`), НЕ параметр: коррелированная ссылка держит
// условие в одном запросе и не заставляет вызывающего дублировать id вторым именем.
//
// Существование файла проверяется ЗАОДНО и это правильно: «файла нет» и «файл не виден» обязаны
// быть снаружи неразличимы, иначе код ответа подтверждает существование ограниченного файла.
func (v Viewer) ExistsFile(idExpr string, params map[string]any) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM library_file lf_vis
		WHERE lf_vis.id = %s AND %s)`, idExpr, v.Where("lf_vis", params))
}

// EnsureVisible отвечает sql.ErrNoRows, если файла нет ИЛИ он невидим смотрящему.
//
// ИМЕННО sql.ErrNoRows, А НЕ ПУСТАЯ СТРУКТУРА И НЕ false: хендлеры Ф7 делают из этой ошибки
// NotFound, и «пусто без ошибки» молча открыло бы точку 12 (блок доступа на чужом файле).
//
// Зовётся ВНУТРИ пишущих транзакций — они идут в SERIALIZABLE, поэтому проверка реально
// закрывает гонку с удалением файла и со сменой уровня, а не просто сужает окно.
func EnsureVisible(ctx context.Context, db dependency.DB, v Viewer, fileID int) error {
	params := map[string]any{"id": fileID}
	visible, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM library_file lf WHERE lf.id = :id AND `+v.Where("lf", params), params)
	if err != nil {
		return fmt.Errorf("failed to check library file visibility: %w", err)
	}
	if visible == 0 {
		return sql.ErrNoRows
	}
	return nil
}
