package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// DeleteSpecialty removes ONE entry from the shared specialty vocabulary, BY NAME.
//
// ЗАЧЕМ ВООБЩЕ. Словарь пополняет любой аутентифицированный (решение Р1), а весь словарь едет в
// каждом ответе пикера людей. Без удаления первая же опечатка («конструктр») вечна, видна на КАЖДОМ
// экране с назначением, и чинится только руками в проде — у тем файла для этого есть DeleteTopic,
// а грамматика заявлена «та же».
//
// ПО ИМЕНИ, А НЕ ПО ID, потому что наружу едут строки: ListAdmins отдаёт специальности именами,
// id специальности клиенту не показывают вовсе. Коллация словаря регистро- и диакритико-независима
// (ai_ci), поэтому `WHERE name = :name` сам схлопывает «Конструктор» на «конструктор» — искать по
// LOWER() не нужно и вредно: это выключило бы индекс уникального ключа.
//
// ОТКАЗ НАЗЫВАЕТ ЧИСЛО ДЕРЖАТЕЛЕЙ. Внешний ключ связи стоит RESTRICT ровно затем, чтобы удаление
// используемой позиции падало: снести её «заодно со связями» значило бы молча снять специальность
// с живых людей. Но «нельзя» без числа — тупик, поэтому связи считаются заранее и ответ говорит,
// СКОЛЬКО аккаунтов держит позицию: с этим человек может пойти и переназначить их.
func (s *Store) DeleteSpecialty(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("specialty name is empty")
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Обе половины в ОДНОЙ транзакции: между счётом и удалением кто-то ставит себе эту
		// специальность, и RESTRICT ответил бы сырым 1451 вместо внятного отказа. Пишущие
		// транзакции стора идут в SERIALIZABLE, поэтому проверка здесь реально закрывает
		// гонку, а не сужает окно.
		id, found, err := lookupSpecialtyID(ctx, rep.DB(), name)
		if err != nil {
			return err
		}
		if !found {
			// sql.ErrNoRows, а не «успешно удалил»: позиция могла не найтись потому, что имя
			// набрали иначе, чем оно лежит, — «готово» на этом убедило бы, что опечатки больше нет.
			return sql.ErrNoRows
		}
		// COUNT(DISTINCT admin_id), а не COUNT(*): уникальный ключ связи и так даёт одну строку
		// на пару, но в отказ уходит число АККАУНТОВ, и запрос обязан считать именно его.
		used, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(DISTINCT admin_id) FROM admin_specialty_link WHERE specialty_id = :id`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to count accounts carrying specialty %q: %w", name, err)
		}
		if used > 0 {
			return entity.NewErrAdminSpecialtyInUse(used)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM admin_specialty WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("can't delete admin specialty %q: %w", name, err)
		}
		return nil
	})
}

// lookupSpecialtyID resolves a name onto an EXISTING vocabulary entry without creating one, which
// is what separates it from storeutil.UpsertAdminSpecialty. Two callers need exactly this: deletion
// (нечего создавать) and the vocabulary ceiling in SetSpecialties, которому нужно знать, какие из
// присланных имён схлопнутся на существующие записи и потолок не тронут.
//
// Сопоставление отдано коллации (ai_ci): она и только она знает, что «Конструктор», «конструктор»
// и «кОнСтРуКтОр» — одна запись. Складывать имена в Go пришлось бы strings.ToLower, который
// регистр свернёт, а диакритику нет, — и два ответа разошлись бы на именно тех парах, ради которых
// уникальный ключ и сделан нечувствительным.
func lookupSpecialtyID(ctx context.Context, db dependency.DB, name string) (int, bool, error) {
	ids, err := storeutil.QueryScalarListNamed[int](ctx, db,
		`SELECT id FROM admin_specialty WHERE name = :name LIMIT 1`,
		map[string]any{"name": name})
	if err != nil {
		return 0, false, fmt.Errorf("failed to look up admin specialty %q: %w", name, err)
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	return ids[0], true, nil
}
