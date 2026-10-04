package design

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ «ПЕРЕЗАПИСИ» ПРАВКОЙ (0369, O-53), НЕ ТРЕБУЮЩИЕ БАЗЫ.
//
// Живая половина — переезд слота, штамп, отказы, повтор, цепочка, чтение полосы — лежит в
// replace_db_test.go и ходит в одноразовый контейнер (CI=1). Решение сторожей и их порядок
// проверены без базы в entity (design_replace_test.go). Здесь — то, что живёт в ФОРМЕ кода: операторы,
// которые держат факт, и отказы, которые обязаны прозвучать до транзакции.

// ШТАМП ПИШЕТСЯ ТОЛЬКО ПОВЕРХ ТОГО, ЧТО ПРОЧИТАЛА ТРАНЗАКЦИЯ: NULL либо отменённый (undone_at,
// 0387) преемник (T28 v2) — `replaced_by <=> :was`, NULL-безопасное равенство.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять `replaced_by <=> :was` с WHERE (или заменить на `=`, при котором NULL
// не совпадает ни с чем и первая перезапись не штампуется вовсе). Тогда вторая перезапись того же
// оригинала, проскочившая чтение (или любой будущий писатель без чтения), молча переписала бы
// указатель на свою правку — и первая правка, уже стоящая в слоте, потеряла бы место в цепочке, не
// потеряв слота.
func TestReplacedByStampIsWrittenOnceOverNothing(t *testing.T) {
	up := strings.ToUpper(designStampReplacedBy)
	require.Equal(t, 1, strings.Count(up, "UPDATE DESIGN_PICTURE"))
	require.Equal(t, 1, strings.Count(up, "WHERE"), "один оператор, один предикат")
	where := designStampReplacedBy[strings.Index(up, "WHERE"):]
	require.Contains(t, where, "id = :id")
	require.Contains(t, where, "replaced_by <=> :was",
		"указатель замены пишется только поверх прочитанного — второй пояс к чтению в транзакции")
	require.Contains(t, designStampReplacedBy, "SET replaced_by = :edit")

	requireNamedQueryBinds(t, designStampReplacedBy, map[string]any{"id": 7, "edit": 12, "was": sql.NullInt32{}})
}

// СТОРОЖ cut_sheet ЧИТАЕТ ВЕТКУ, А НЕ КАРТОЧКУ: ДВА ЗАПРОСА ПО id РОДИТЕЛЕЙ И ЦЕЛЕЙ (O-53 review,
// раунды 4–5).
//
// Уровни, куски по entity.DesignBranchChunk и общий потолок собирает entity.DesignLoadBranch —
// проверено без базы в entity (TestDesignLoadBranch…). Здесь — форма двух запросов, которыми стор
// ему отвечает: колонки — ровно entity.DesignBranchColumns, предикат — только id или derived_from с
// глаголом, у кропов — названный индекс derived_from и порядок самого этого индекса, у обоих — LIMIT,
// связанный ровно с limit чтения (место под потолком плюс одна строка). План оптимизатора и замки
// отсюда не видны — только то, что запрос их не оставляет на волю оценки.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: вернуть предикат карточки (кроп другой карточки с derived_from = лист снова
// пропал бы из ветки молча — MINOR ревью раунда 4); вернуть фильтр видимости или замены (спрятанный
// кусок со стоящей правкой выпал бы из ветки); снять глагол с кропов (в ветку поехали бы правки
// «рядом» и легаси); снять LIMIT с любого из двух (раунд 5: сервер снова сканировал бы, слал и запирал
// всё, что подошло, а потолок держал бы только память Go); снять FORCE INDEX с кропов (`ORDER BY …
// LIMIT` вправе увести оптимизатор в проход PRIMARY — замок на всю таблицу); сортировать кропы по id
// (filesort читает всех детей куска до первой строки, и LIMIT снова не останавливает скан); связать
// LIMIT не с limit (на единицу меньше — и кусок ветки отрезается молча); читать `SELECT *`.
func TestBranchReadsNameParentsNotTheCard(t *testing.T) {
	shape := regexp.MustCompile(`(?s)^\s*SELECT\s+(.+?)\s+FROM\s+(\w+)(?:\s+FORCE INDEX \((\w+)\))?\s+WHERE\s+(.+?)\s+ORDER BY\s+(.+?)\s+LIMIT\s+:(\w+)\s*$`)
	ids := make([]int, entity.DesignBranchChunk)
	for i := range ids {
		ids[i] = 100 + i
	}
	// Самый широкий limit, какой просит чтение ветки: всё место под потолком после листа плюс одна строка.
	const limit = entity.DesignStandingNodesMax
	for _, tc := range []struct {
		name   string
		q      string
		index  string
		where  string
		order  string
		params map[string]any
	}{
		{"цели замен", designBranchByID, "", "id IN (:ids)", "id", map[string]any{"ids": ids}},
		{"кропы уровня", designBranchCropsOf, "idx_design_picture_derived_from",
			"derived_from IN (:ids) AND derivation = :crop", "derived_from, id",
			map[string]any{"ids": ids, "crop": entity.DesignDerivationCrop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := strings.ToUpper(tc.q)
			require.Equal(t, 1, strings.Count(up, "SELECT"), "одно чтение, без подзапросов")
			require.NotContains(t, up, "JOIN")
			require.NotContains(t, up, "TECH_CARD_ID =", "ни одного предиката карточки")

			// Разбор словами, а не поиском подстроки: FROM сидит и внутри derived_from.
			m := shape.FindStringSubmatch(tc.q)
			require.NotNil(t, m, "форма SELECT … FROM … [FORCE INDEX (…)] WHERE … ORDER BY … LIMIT :…: %q", tc.q)
			require.Equal(t, entity.DesignBranchColumns, m[1], "колонки — ровно поля узла обхода")
			require.Equal(t, "design_picture", m[2])
			require.Equal(t, tc.index, m[3], "кропы — по названному индексу derived_from, цели — по PRIMARY без подсказки")
			require.Equal(t, tc.where, m[4])
			require.Equal(t, tc.order, m[5], "порядок — порядок индекса: иначе filesort, и LIMIT не останавливает скан")
			require.Equal(t, "room_plus_one", m[6])

			// Полный кусок связывается целиком: DesignBranchChunk мест в IN, и LIMIT — последним, ровно limit.
			require.NotContains(t, tc.q, "--", "SQL comments do not belong in a named query")
			require.NotContains(t, tc.params, "room_plus_one", "связку LIMIT добавляет designBranchQuery, а не вызывающий")
			expanded, args, err := designBranchQuery(tc.q, tc.params, limit)
			require.NoError(t, err)
			require.Equal(t, strings.Count(expanded, "?"), len(args))
			require.Len(t, args, len(tc.params)-1+entity.DesignBranchChunk+1)
			require.True(t, strings.HasSuffix(strings.TrimSpace(expanded), "LIMIT ?"), "%q", expanded)
			require.Equal(t, limit, args[len(args)-1], "LIMIT — ровно limit чтения: место под потолком плюс одна строка")
		})
	}
}

// ТЕХНИЧЕСКИЙ ЛИСТ ЧИТАЕТСЯ ПО КАРТОЧКЕ, ФАЙЛУ И СЛОВУ ЛИСТА — И ТОЛЬКО ТАК (27.09).
//
// Лист — строки tech_card_media с category = 'technical' (0092): их тех-пакет печатает плитами. Само
// правило (отказ technical_sheet, его место между hidden_picture и cut_sheet) проверено без базы в
// entity; что отказ ничего не подаёт и что мудборд, лист чужой карточки и «save as new» перезапись
// не держат — живой пробой replace_db_test.go (CI=1).
//
// ЭТО СТРУКТУРНАЯ СИГНАЛИЗАЦИЯ ПО ТЕКСТУ ЗАПРОСА, А НЕ ДОКАЗАТЕЛЬСТВО ПОВЕДЕНИЯ: она звенит, когда
// меняются слова и привязки чтения, а что чтение отвечает и что запирает — дело живой пробы.
// Текстовые мутанты, на которые звенит: снять категорию (кадр на мудборде закрыл бы перезапись, хотя
// мудборд плит не печатает); снять карточку (тот же файл на листе ЧУЖОЙ карточки закрыл бы перезапись
// здесь); сравнивать не media_id; связать слово листа не с 'technical'; вернуть COUNT(*) (D-57: ответ
// «да» досчитывал и запирал бы все строки файла на листе).
func TestTechnicalSheetReadNamesTheCardTheFileAndTheSheet(t *testing.T) {
	require.Equal(t,
		"SELECT EXISTS ( SELECT 1 FROM tech_card_media WHERE tech_card_id = :card AND media_id = :media AND category = :technical )",
		strings.Join(strings.Fields(designTechnicalSheetRows), " "))
	query, args, err := designTechnicalSheetQuery(41, 900)
	require.NoError(t, err)
	require.Equal(t,
		"SELECT EXISTS ( SELECT 1 FROM tech_card_media WHERE tech_card_id = ? AND media_id = ? AND category = ? )",
		strings.Join(strings.Fields(query), " "))
	require.Equal(t, []any{41, 900, "technical"}, args, "карточка, файл и слово листа — в этом порядке")
}

// ПОВТОР ИЩЕТСЯ В ПРЕДЕЛАХ КАРТОЧКИ И ПО ТОЧНОМУ КЛЮЧУ (0370).
//
// МУТАЦИИ: потерять карточку в предикате (ключ чужой карточки отвечал бы на повтор этой — и индекс
// (tech_card_id, request_key) перестал бы служить чтению); потерять порядок (при двух кадрах с одним
// ключом — возможных только в обход стора — ответ стал бы броском монеты).
func TestPictureByRequestKeyIsScopedToTheCard(t *testing.T) {
	q := designPictureByRequestKey
	where := q[strings.Index(q, "WHERE"):]
	require.Contains(t, where, "tech_card_id = :card AND request_key = :key")
	require.True(t, strings.HasSuffix(strings.TrimSpace(q), "ORDER BY id LIMIT 1"))
	requireNamedQueryBinds(t, q, map[string]any{"card": 41, "key": "k-1"})
}

// ПУСТОЙ КЛЮЧ НЕ СОВПАДАЕТ НИ С ЧЕМ — И ДАЖЕ НЕ СПРАШИВАЕТ БАЗУ.
//
// МУТАЦИЯ: убрать ранний выход. Тогда пустая строка искалась бы ключом на каждом флэттене без ключа
// (с NULL-колонкой она не совпала бы, и вред казался бы нулевым — до первой строки, где ключ пуст, а
// не NULL). Проба ловит это nil-базой: чтение через неё паникует.
func TestPictureByRequestKeyWithoutAKeyReadsNothing(t *testing.T) {
	_, ok, err := pictureByRequestKey(context.Background(), nil, 41, "")
	require.NoError(t, err)
	require.False(t, ok)
}

// КЛЮЧ МЕРЯЕТСЯ ШИРИНОЙ КОЛОНКИ, В СИМВОЛАХ, И ОТКАЗ ЗВУЧИТ ДО ТРАНЗАКЦИИ.
//
// VARCHAR(64) в utf8mb4 — 64 СИМВОЛА, а не байта: 64 кириллических буквы (128 байт) законны, 65 —
// нет. Пробелы по краям срезаются до меры (колонка _bin хвостовых пробелов не различает).
//
// МУТАЦИИ: мерить байтами (64 «ж» отказываются); не мерить вовсе (65 доходят до транзакции и падают
// там сырым 1406 «Data too long»); мерить до среза пробелов (ключ с отступом отказывается).
func TestFlattenMeasuresTheKeyBeforeTheTransaction(t *testing.T) {
	reached := false
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		reached = true
		return nil
	}}
	flatten := func(key string) error {
		reached = false
		_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
			TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ClientRequestId: key,
		})
		return err
	}

	require.ErrorIs(t, flatten(strings.Repeat("ж", entity.DesignRequestKeyMaxRunes+1)), entity.ErrDesignInvalidArgument)
	require.False(t, reached, "длинный ключ обязан отказаться до транзакции")

	for _, key := range []string{
		"",
		strings.Repeat("ж", entity.DesignRequestKeyMaxRunes),
		"  " + strings.Repeat("k", entity.DesignRequestKeyMaxRunes) + "\t",
	} {
		require.NoError(t, flatten(key))
		require.True(t, reached, "ключ %q обязан дойти до транзакции", key)
	}
}

// ОТРИЦАТЕЛЬНОЕ МЕСТО ОТКАЗЫВАЕТСЯ ДО ТРАНЗАКЦИИ, А НЕ ЧИТАЕТСЯ НУЛЁМ.
//
// МУТАЦИЯ: убрать проверку. Тогда `replace_picture_id: -1` доходит до транзакции и подаётся РЯДОМ —
// то есть OK на просьбу поставить правку на место, которую никто не исполнил. Проба ловит это обоими
// способами: ошибки нет И замыкание достигнуто, не открывая ни одного соединения.
//
// ВТОРАЯ ПОЛОВИНА — контроль на ложную зелень: ноль («рядом») и положительный id доходят до
// транзакции, то есть отказ про ЗНАК, а не про само упоминание поля.
func TestFlattenRefusesANegativeReplaceTargetBeforeTheTransaction(t *testing.T) {
	reached := false
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		reached = true
		return nil
	}}
	_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
		TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ReplacePictureId: -1,
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
	require.False(t, reached, "отрицательное место обязано отказаться до транзакции")

	for _, target := range []int{0, 7} {
		reached = false
		_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
			TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ReplacePictureId: target,
		})
		require.NoError(t, err)
		require.True(t, reached, "replace_picture_id %d обязан дойти до транзакции", target)
	}
}

// requireNamedQueryBinds — ЛОВУШКА ДВОЕТОЧИЯ: sqlx сканирует имена параметров, не пропуская ни
// комментариев, ни литералов, поэтому лишнее ':' связалось бы пустым именем и уронило бы запрос в
// рантайме, а не здесь.
func requireNamedQueryBinds(t *testing.T, q string, params map[string]any) {
	t.Helper()
	require.NotContains(t, q, "--", "SQL comments do not belong in a named query")
	expanded, args, err := storeutil.MakeQuery(q, params)
	require.NoError(t, err, "named-parameter expansion must succeed")
	require.Equal(t, strings.Count(expanded, "?"), len(args))
	require.Len(t, args, len(params), "every parameter is bound exactly once")
}
