package design_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ КАКИМ ГЛАГОЛОМ КАДР ОТДЕЛИЛСЯ ОТ РОДИТЕЛЯ — 0359, J-1/J-23 ═══════════════════════════════
//
// `derived_from` пишут ДВА глагола, и до этой колонки на проводе их было НЕ РАЗЛИЧИТЬ. Клиент,
// складывающий ленту в колоду «только после разреза», угадывал по `layer_rev` — а кроп КОПИРУЕТ
// ревизию родителя, поэтому кроп отредактированного листа приходит ровно как флэттен.
//
// ⚠ ПОЧЕМУ ОБВЯЗКА ЗДЕСЬ КОНТЕЙНЕРНАЯ, А НЕ МОКОВАЯ. Утверждение целиком про то, ЧТО ЛЕГЛО В
// СТРОКУ: два INSERT в разных файлах пишут одну таблицу, и разойтись они могут только в базе.
// probeRepository пропускает всё без CI=1 и отдельно отказывается работать против базы, чьё имя
// похоже на боевое.

// ГЛАГОЛ РАЗРЕЗА ЗАПИСАН РАЗРЕЗОМ.
func TestDesignDBSplitStampsCrop(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)

	require.Equal(t, entity.DesignDerivationNone, sheet.Derivation,
		"сам лист ни от чего не произведён — у корня глагола нет")
	require.False(t, sheet.DerivedFrom.Valid)

	cropMedia := probeMedia(t, raw)
	crops, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: cropMedia, ViewKey: entity.DesignViewFront}},
	})
	require.NoError(t, err)
	require.Len(t, crops, 1)
	require.Equal(t, entity.DesignDerivationCrop, crops[0].Derivation)
	require.Equal(t, int32(sheet.Id), crops[0].DerivedFrom.Int32)
}

// ГЛАГОЛ ПРАВКИ ЗАПИСАН ПРАВКОЙ — И ТОЛЬКО КОГДА У НЕЁ ЕСТЬ ПОДЛОЖКА.
//
// ⚠ ВТОРАЯ ПОЛОВИНА ОБЯЗАТЕЛЬНА. Проба на один лишь «flatten с базой» прошла бы и на правке,
// которая штампует `flatten` ВСЕГДА, — а тогда рисунок с чистого листа объявил бы себя
// производным от несуществующего родителя, и лента искала бы ему колоду.
func TestDesignDBFlattenStampsFlattenOnlyOverABase(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	// (а) ПРАВКА ПОВЕРХ КАДРА.
	base := probeMedia(t, raw)
	probeBandPicture(t, raw, card, base)
	overBase, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, BaseMediaId: base, ExpectedRev: 0,
		Strokes: []byte(`[{"d":"M0 0 L1 1"}]`), Actor: "probe",
	})
	require.NoError(t, err)
	edited, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: overBase.Id, ExpectedRev: overBase.Rev,
		MediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignDerivationFlatten, edited.Derivation)
	require.True(t, edited.DerivedFrom.Valid, "правка кадра — сиблинг своей подложки")

	// (б) РИСУНОК С ЧИСТОГО ЛИСТА — КОРЕНЬ, А НЕ «НЕИЗВЕСТНО».
	fromNothing, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, ExpectedRev: 0,
		Strokes: []byte(`[{"d":"M0 0 L2 2"}]`), Actor: "probe",
	})
	require.NoError(t, err)
	root, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: fromNothing.Id, ExpectedRev: fromNothing.Rev,
		MediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignDerivationNone, root.Derivation)
	require.False(t, root.DerivedFrom.Valid)
}

// ГЛАГОЛ ПЕРЕЖИВАЕТ ЧТЕНИЕ ПОЛОСЫ, А НЕ ТОЛЬКО ОТВЕТ ПИШУЩЕЙ ДВЕРИ.
//
// ⚠ ЭТО НЕ ПОВТОР ДВУХ ПРОБ ВЫШЕ. Те читают то, что вернула сама функция записи; здесь строка
// поднимается ЗАНОВО, `SELECT *`-ом полосы. Забытый тег `db` или колонка, не попавшая в SELECT,
// прошли бы первые две пробы целиком и отдали бы клиенту пустой глагол.
func TestDesignDBDerivationSurvivesTheBandRead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)

	_, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewBack}},
	})
	require.NoError(t, err)

	band, err := rep.Design().GetBand(ctx, card, 50)
	require.NoError(t, err)

	seen := map[string]int{}
	for _, b := range band.Batches {
		for _, p := range b.Pictures {
			seen[p.Derivation]++
		}
	}
	require.Equal(t, 1, seen[entity.DesignDerivationCrop],
		"кроп обязан приехать с глаголом и через чтение полосы: %v", seen)
	require.Equal(t, 1, seen[entity.DesignDerivationNone],
		"его лист остаётся корнем: %v", seen)
}

// ═══ БЭКФИЛЛ 0359 НА ЗАСЕЯННОЙ ЦЕПОЧКЕ ════════════════════════════════════════════════════════
//
// ⚠ ПРОБА ИСПОЛНЯЕТ ТЕКСТ САМОЙ МИГРАЦИИ, ВЗЯТЫЙ С ДИСКА, А НЕ ЕГО КОПИЮ. Копия запроса в пробе —
// вторая реализация, и разойтись с оригиналом она может молча: проба зеленела бы на исправленном
// в ней же UPDATE, пока прод получал бы старый.
//
// ЦЕПОЧКА — ТА, ЧТО ОПИСАНА В ПЛАНЕ: лист → кроп → флэттен кропа → кроп флэттена, плюс СИРОТА
// (родитель удалён). Кроп КОПИРУЕТ layer_rev родителя, флэттен пишет свой — это и есть
// единственный след, по которому эвристика ещё может отличить их у старых строк.
func TestDesignDBDerivationBackfillClassifiesALegacyChain(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)

	// Строки заводятся НАПРЯМУЮ, минуя стор: цель — воспроизвести состояние ДО миграции, когда
	// писатели глагола ещё не существовали. Через стор такое состояние уже невыразимо.
	mk := func(parent any, layerRev int) int {
		media := probeMedia(t, raw)
		res, err := raw.Exec(`INSERT INTO design_picture
			(tech_card_id, media_id, ordinal, kind, derived_from, derivation, source_class, layer_rev)
			VALUES (?, ?, 0, 'flat', ?, '', 'ai', ?)`, card, media, parent, layerRev)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}

	// ⚠ РЕВИЗИИ ЗДЕСЬ — ТЕ, ЧТО ЖИВЫЕ ПИСАТЕЛИ РЕАЛЬНО ПРОИЗВОДЯТ. В первой редакции пробы стояли
	// 3, 3, 7, 7 — цепочка, которую не порождает ни один путь кода: слой рождается на ревизии 1 и
	// только инкрементируется. Выдуманные числа обходили стороной ровно тот случай, ради которого
	// бэкфилл и писался, и проба зеленела над дефектом.
	sheet := mk(nil, 0)          // загруженный лист: слоя не было, ревизия 0
	crop := mk(sheet, 0)         // разрез КОПИРУЕТ ноль
	flatten := mk(crop, 1)       // первая правка разреза — слой рождается единицей
	cropOfFlat := mk(flatten, 1) // разрез правки снова копирует единицу

	// СИРОТА: родителя нет вовсе. Родитель здесь — заведомо несуществующий id, что законно:
	// design_picture.derived_from объявлен голым KEY без FK (0340).
	orphan := mk(2147483647, 0)

	// Сбросить глагол у ВСЕХ пяти строк — включая те, которым писатели уже что-то поставили бы.
	_, err := raw.Exec(`UPDATE design_picture SET derivation = '' WHERE tech_card_id = ?`, card)
	require.NoError(t, err)

	runMigrationUp(t, raw, "0359_design_picture_derivation.sql")

	got := func(id int) string {
		var d string
		require.NoError(t, raw.QueryRow(`SELECT derivation FROM design_picture WHERE id = ?`, id).Scan(&d))
		return d
	}
	require.Equal(t, entity.DesignDerivationNone, got(sheet), "корень остаётся корнем")
	require.Equal(t, entity.DesignDerivationCrop, got(crop),
		"у родителя ревизия 0, у ребёнка 0 — флэттен принёс бы ≥ 1, значит это разрез")
	require.Equal(t, entity.DesignDerivationFlatten, got(flatten),
		"у родителя ревизия 0, у ребёнка 1 — разрез скопировал бы ноль, значит это правка")
	require.Equal(t, entity.DesignDerivationNone, got(cropOfFlat),
		"родитель ОТРЕДАКТИРОВАН, и правку от разреза здесь не отличить ничем — молчание честнее "+
			"догадки, потому что неверный штамп неисправим")
	require.Equal(t, entity.DesignDerivationNone, got(orphan),
		"строку без родителя классифицировать НЕЧЕМ, и она честно молчит — назвать её "+
			"кропом значило бы вписать в летопись догадку")

	// ПОВТОР БЭКФИЛЛА НИЧЕГО НЕ ПЕРЕПИСЫВАЕТ. Миграции идемпотентны по требованию репозитория, и
	// здесь у идемпотентности есть второе, более острое следствие: повторный прогон файла не имеет
	// права ЗАТЕРЕТЬ правду, которую с этого момента пишут сами глаголы.
	_, err = raw.Exec(`UPDATE design_picture SET derivation = ? WHERE id = ?`,
		entity.DesignDerivationFlatten, crop)
	require.NoError(t, err)
	runMigrationUp(t, raw, "0359_design_picture_derivation.sql")
	require.Equal(t, entity.DesignDerivationFlatten, got(crop),
		"бэкфилл трогает ТОЛЬКО строки с пустым глаголом")

	_ = rep
}

// runMigrationUp исполняет Up-половину названного файла миграции — ПОСТАНОВОЧНО, на одном
// соединении.
//
// ОДНО СОЕДИНЕНИЕ ОБЯЗАТЕЛЬНО: файл строится на пользовательских переменных (`SET @… ; PREPARE …`),
// а они живут в СЕССИИ. Через пул `database/sql` соседние операторы разъехались бы по разным
// соединениям, и `@ddl` во втором из них оказался бы NULL — то есть проба «прошла бы», не
// исполнив ничего.
func runMigrationUp(t *testing.T, raw *sql.DB, name string) {
	t.Helper()
	// ⚠ КАТАЛОГ ПЕРЕОПРЕДЕЛЯЕМ, И ЭТО НЕ УДОБСТВО, А УСЛОВИЕ ИЗМЕРИМОСТИ. Текст миграции читается
	// в РАНТАЙМЕ, поэтому `go -overlay` — которым подменяются исходники — до него не достаёт: игла,
	// возвращающая старую эвристику, оставляла пробу ЗЕЛЁНОЙ, и зелень эта означала «сторож не
	// подключён», а вовсе не «дефекта нет». Через эту переменную игла подсовывает патченную копию
	// каталога, и проба наконец умеет краснеть.
	dir := os.Getenv("DESIGN_SQL_DIR")
	if dir == "" {
		dir = "../sql"
	}
	body, err := os.ReadFile(dir + "/" + name)
	require.NoError(t, err)

	text := string(body)
	up := text[strings.Index(text, "-- +migrate Up"):]
	if i := strings.Index(up, "-- +migrate Down"); i >= 0 {
		up = up[:i]
	}

	conn, err := raw.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()

	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stripSQLComments(stmt))
		if s == "" {
			continue
		}
		_, err := conn.ExecContext(context.Background(), s)
		require.NoErrorf(t, err, "оператор миграции %s не исполнился:\n%s", name, s)
	}
}

// stripSQLComments убирает строки-комментарии, чтобы разбиение по «;» не унесло в оператор
// висящий хвост комментария предыдущего.
func stripSQLComments(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
