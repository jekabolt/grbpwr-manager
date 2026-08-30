package design_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ВЕКТОРНОГО ИМПОРТА: КЛЮЧ ИДЕМПОТЕНТНОСТИ И ГРАНИЦА КАРТОЧКИ.
//
// ПОЧЕМУ ЭТО ОБЯЗАНО ПРОВЕРЯТЬСЯ ЖИВОЙ БАЗОЙ, А НЕ МОКОМ. Оба предмета — свойства ЗАПРОСОВ:
// идемпотентность держится уникальным индексом и чтением внутри SERIALIZABLE-транзакции, а
// принадлежность считается объединением двух таблиц. Мок отвечает то, что ему велели, и доказал бы
// ровно то, что его настроили доказать.
//
// Запуск — тот же одноразовый контейнер, что и у соседних проб (см. шапку wave2_db_test.go); без
// CI=1 каждая проба пропускается ДО открытия соединения.

// probeVectorImport — один импорт с разумными умолчаниями.
func probeVectorImport(card, media int, requestID string) entity.DesignVectorImport {
	return entity.DesignVectorImport{
		TechCardId:      card,
		ClientRequestId: requestID,
		SourceMediaId:   media,
		Origin:          entity.DesignLayerOriginImported,
		Actor:           "probe",
	}
}

// probeCardMedia привязывает медиа к карточке как её собственную картинку (tech_card_media) —
// первый из двух держателей, по которым считается принадлежность.
func probeCardMedia(t *testing.T, raw *sql.DB, card, media int) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, category, display_order)
		VALUES (?, ?, 'moodboard', 0)`, card, media)
	require.NoError(t, err)
}

// probeBandPicture кладёт медиа в полосу карточки (design_picture) — второй держатель. Именно он
// делает положительное правило «обязано лежать в tech_card_media» негодным: картинка полосы в ту
// таблицу не попадает никогда.
func probeBandPicture(t *testing.T, raw *sql.DB, card, media int) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO design_picture (tech_card_id, media_id, ordinal, kind, source_class)
		VALUES (?, ?, 0, 'flat', 'ai')`, card, media)
	require.NoError(t, err)
}

// ─────────────────── идемпотентность по объявленному ключу ───────────────────

// ПОВТОР ОДНОГО ЗАПРОСА ВОЗВРАЩАЕТ ТОТ ЖЕ СЛОЙ.
//
// Ключ — `client_request_id`, ровно как объявляет контракт. До 0351 колонки под него не было
// вовсе: поле требовалось дверью, доезжало в стор и не читалось ничем.
func TestDesignDBImportVectorIsIdempotentOnClientRequestId(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)
	req := uuid.NewString()

	first, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, req))
	require.NoError(t, err)
	second, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, req))
	require.NoError(t, err)
	require.Equal(t, first.Id, second.Id, "повтор обязан вернуть ТОТ ЖЕ слой, а не подшить второй")
	require.Equal(t, req, second.ClientRequestId.String, "ключ обязан храниться, а не только сверяться")

	var rows int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_edit_layer WHERE tech_card_id = ?`, card).Scan(&rows))
	require.Equal(t, 1, rows, "двух строк быть не должно ни при каком повторе")
}

// ТОТ ЖЕ ЗАПРОС, НАЗЫВАЮЩИЙ ДРУГОЙ ФАЙЛ, — ОТКАЗ.
//
// ⚠ ЭТО И ЕСТЬ ТА ПОЛОВИНА, КОТОРОЙ НЕ БЫЛО. Дедупликация шла по паре (карточка, файл), поэтому
// другой файл под тем же запросом спокойно заводил ВТОРОЙ слой: поле, объявленное контрактом
// ключом, не мешало ничему. Молча вернуть первый слой тоже нельзя — клиент считал бы, что подшил
// второй файл.
func TestDesignDBImportVectorRefusesAReusedRequestIdNamingAnotherFile(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	first := probeMedia(t, raw)
	other := probeMedia(t, raw)
	req := uuid.NewString()

	_, err := rep.Design().ImportVector(ctx, probeVectorImport(card, first, req))
	require.NoError(t, err)

	_, err = rep.Design().ImportVector(ctx, probeVectorImport(card, other, req))
	require.Error(t, err)
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)

	var rows int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_edit_layer WHERE tech_card_id = ?`, card).Scan(&rows))
	require.Equal(t, 1, rows, "отвергнутый импорт не смеет оставить строки")
}

// ТОТ ЖЕ ЗАПРОС НА ДРУГОЙ КАРТОЧКЕ — ОТКАЗ.
//
// ⚠ ЭТА ПРОБА РАЗЛИЧАЕТ ДВА КЛЮЧА ТАМ, ГДЕ ОСТАЛЬНЫЕ НЕ МОГУТ. Пояс по паре (карточка, файл) сюда
// не дотягивается по построению — карточка другая, — поэтому зелёной она бывает ровно тогда, когда
// читается ИМЕННО `client_request_id`. Ключ объявлен глобальным (как у прогона и у пачки, 0340):
// один запрос человека — одна запись во всей системе.
func TestDesignDBImportVectorRefusesAReusedRequestIdOnAnotherCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	other := probeCard(t, raw)
	req := uuid.NewString()

	_, err := rep.Design().ImportVector(ctx, probeVectorImport(mine, probeMedia(t, raw), req))
	require.NoError(t, err)

	_, err = rep.Design().ImportVector(ctx, probeVectorImport(other, probeMedia(t, raw), req))
	require.Error(t, err, "один запрос не может завести слой на второй карточке")
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)

	var rows int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_edit_layer WHERE tech_card_id = ?`, other).Scan(&rows))
	require.Zero(t, rows)
}

// ТОТ ЖЕ ФАЙЛ ПОД НОВЫМ ЗАПРОСОМ НЕ ПОДШИВАЕТСЯ ВТОРЫМ СЛОЕМ.
//
// Второй пояс того же обещания, сформулированного контрактом про ФАЙЛ: «a retry after a lost
// response must not file the same SVG as a second layer». Клиент, перевыпустивший идентификатор
// запроса, прошёл бы первую проверку.
func TestDesignDBImportVectorStillDedupesTheSameFile(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	first, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, uuid.NewString()))
	require.NoError(t, err)
	second, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, uuid.NewString()))
	require.NoError(t, err)
	require.Equal(t, first.Id, second.Id)
}

// ─────────────────── граница карточки ───────────────────

// КАРТИНКА ПОЛОСЫ ЧУЖОЙ КАРТОЧКИ НЕ СТАНОВИТСЯ ВЕКТОРНЫМ СЛОЕМ ЭТОЙ.
func TestDesignDBImportVectorRefusesForeignSourceMedia(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	theirs := probeCard(t, raw)
	media := probeMedia(t, raw)
	probeBandPicture(t, raw, theirs, media)

	_, err := rep.Design().ImportVector(ctx, probeVectorImport(mine, media, uuid.NewString()))
	require.Error(t, err)
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia)
}

// ТО ЖЕ ДЛЯ ПОДЛОЖКИ.
//
// base_media_id не проверялся ничем, кроме существования строки медиа, — при том что картинка
// (source_picture_id) проверялась на принадлежность с самого начала. Одна дверь, два соседних
// поля, и правило стояло только на одном.
func TestDesignDBImportVectorRefusesForeignBaseMedia(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	theirs := probeCard(t, raw)
	svg := probeMedia(t, raw)
	base := probeMedia(t, raw)
	probeCardMedia(t, raw, theirs, base)

	in := probeVectorImport(mine, svg, uuid.NewString())
	in.BaseMediaId = base
	_, err := rep.Design().ImportVector(ctx, in)
	require.Error(t, err)
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia)
}

// СВЕЖАЯ ЗАГРУЗКА ПРОХОДИТ — И БЕЗ ЭТОЙ ПРОБЫ ГРАНИЦА БЫЛА БЫ ПРОСТО ПОЛОМКОЙ.
//
// ⚠ ИМЕННО ЗДЕСЬ ВИДНО, ПОЧЕМУ ПРАВИЛО ОТРИЦАТЕЛЬНОЕ. Файл, только что загруженный через
// UploadContentImage, не принадлежит ещё НИ ОДНОЙ карточке — это обычный, главный случай глагола.
// Положительное правило («обязано лежать в tech_card_media этой карточки», как у SetReferenceRole)
// отказало бы каждому импорту.
func TestDesignDBImportVectorAcceptsAFreshUpload(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw) // ни одной карточки за ним не стоит

	layer, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, uuid.NewString()))
	require.NoError(t, err, "ничейный файл — законный вход, а не чужой")
	require.Equal(t, media, int(layer.SourceMediaId.Int32))
}

// СВОЯ КАРТИНКА ПОЛОСЫ ПРОХОДИТ.
func TestDesignDBImportVectorAcceptsItsOwnBandPicture(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)
	probeBandPicture(t, raw, card, media)

	_, err := rep.Design().ImportVector(ctx, probeVectorImport(card, media, uuid.NewString()))
	require.NoError(t, err, "картинка ЭТОЙ карточки не может быть чужой")
}

// ОДИН ФАЙЛ В ДВУХ КАРТОЧКАХ — ЗАКОНЕН.
//
// Правило про то, что картинка НЕ ЧУЖАЯ, а не про то, что она больше нигде не встречается: одна и
// та же ткань лежит в нескольких карточках, и отказывать за это значило бы ломать обычную работу.
func TestDesignDBImportVectorAcceptsMediaSharedWithAnotherCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	theirs := probeCard(t, raw)
	media := probeMedia(t, raw)
	probeCardMedia(t, raw, theirs, media)
	probeCardMedia(t, raw, mine, media)

	_, err := rep.Design().ImportVector(ctx, probeVectorImport(mine, media, uuid.NewString()))
	require.NoError(t, err)
}

// ─────────────────── та же граница, спрошенная дверью ───────────────────

// AssertMediaNotForeign ОТВЕЧАЕТ ТО ЖЕ САМОЕ, ЧТО ПРОВЕРЯЕТ ИМПОРТ.
//
// ⚠ ЭТО И ЕСТЬ ПРЕДМЕТ ПРОБЫ: глагол существует затем, чтобы дверь спрашивала ТО ЖЕ ПРАВИЛО, а не
// заводила своё второе мнение. Раньше дверь считала принадлежность сама — через реестр ссылок
// media, у которого множество держателей ШИРЕ (выноски карточки, плиты версий, примерки), — и два
// ответа разошлись бы на первой же правке одного из них.
func TestDesignDBAssertMediaNotForeignMatchesTheImportRule(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine := probeCard(t, raw)
	theirs := probeCard(t, raw)

	fresh := probeMedia(t, raw)
	own := probeMedia(t, raw)
	probeBandPicture(t, raw, mine, own)
	foreign := probeMedia(t, raw)
	probeBandPicture(t, raw, theirs, foreign)

	require.NoError(t, rep.Design().AssertMediaNotForeign(ctx, mine, []int{fresh, own}),
		"ничейное и своё обязаны проходить")
	require.NoError(t, rep.Design().AssertMediaNotForeign(ctx, mine, nil),
		"пустой список — не повод для запроса и не повод для отказа")
	require.NoError(t, rep.Design().AssertMediaNotForeign(ctx, mine, []int{0, -1}),
		"незаданное — законное состояние, отказывать за него обязан тот, кто его требует")

	err := rep.Design().AssertMediaNotForeign(ctx, mine, []int{fresh, own, foreign})
	require.Error(t, err, "чужой номер обязан быть найден, на каком бы месте он ни стоял")
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia)

	// И ТОТ ЖЕ ВЕРДИКТ ЧЕРЕЗ САМ ИМПОРТ: два ответа на один вопрос обязаны совпадать, иначе дверь
	// и стор снова разъедутся.
	_, ierr := rep.Design().ImportVector(ctx, probeVectorImport(mine, foreign, uuid.NewString()))
	require.True(t, errors.Is(ierr, entity.ErrDesignForeignMedia),
		"импорт обязан отказать ровно тому, что глагол назвал чужим")
}
