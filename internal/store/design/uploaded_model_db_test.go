package design_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/stretchr/testify/require"
)

// ═══ E-13: СВОЯ 3D-МОДЕЛЬ ЛОЖИТСЯ НА КАРТОЧКУ БЕЗ ЕДИНОЙ ПРАВКИ ПОЛОСЫ ══════════════════════════
//
// Владелец: «в 3D в 3D MODELS OF THIS CARD добавь возможность загрузить свою 3d модель». Ответ
// круга 16 — одна дверь байтов (UploadContentModel) и НИЧЕГО больше на бэкенде. Это утверждение,
// а не надежда, и проверяется оно единственным честным способом: настоящей записью через
// настоящий стор и настоящим чтением полосы.
//
// ⚠ ЧТО ИМЕННО ЗАПИРАЕТСЯ, И ПОЧЕМУ ЭТОГО НЕЛЬЗЯ ДОКАЗАТЬ ЧИСТОЙ ПРОБОЙ. Предикат выходов
// карточки живёт в SQL (designCardOutputsWhere) и звучит так: кадр прогона рода
// render|threed|pattern|recolor ЛИБО кадр БЕЗ прогона рода render|threed|pattern. Второй половины
// достаточно, чтобы модель, загруженная руками, приехала в 3D MODELS OF THIS CARD — но проверить
// это можно только выполнив запрос. Сузить его до `p.kind IN ('render','pattern')` — правка в
// одиннадцать символов, после которой загруженная модель исчезает с карточки МОЛЧА: она в базе,
// клиент её не видит, и ни один тип, ни один компилятор об этом не скажет.
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там:
// без CI=1 проба пропускается ДО открытия соединения.

// ⚠ МЕДИА БЕРЁТСЯ ОБЫЧНОЕ (probeMedia), И ЭТО НЕ УПРОЩЕНИЕ. Стор про содержимое файла не знает
// ничего и знать не должен: форму «один объект, три слота url, нулевые размеры» держит
// bucket.UploadContentNonRaster, и она уже проверена там своими пробами (nonraster_test.go).
// Подделывать её здесь значило бы проверять собственную выдумку вместо поведения полосы.

// ЗАГРУЖЕННАЯ РУКАМИ МОДЕЛЬ — ПОЛНОПРАВНЫЙ ВЫХОД КАРТОЧКИ И ВИДИМО НЕ ПОКУПКА.
//
// Проба проходит весь путь: RegisterBatch с родом `threed` (то, что делает RegisterDesignUpload) →
// GetBand → выходы карточки. Утверждений три, и каждое отвечает на свой вопрос клиента:
//
//  1. МОДЕЛЬ ЕСТЬ В СПИСКЕ — иначе раздел 3D MODELS OF THIS CARD её не покажет вовсе.
//  2. ШТАМП ГОВОРИТ «ПРОГОНА НЕТ»: run_id = 0, run_kind = "". Это то самое место, где деньги и
//     происхождение либо названы честно, либо соврут: строка с непустым run_kind читается панелью
//     как ОПЛАЧЕННЫЙ результат.
//  3. ЗАТО СКАЗАНО, ОТКУДА ОНА: batch_id непустой, source_class = uploaded.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНИТ: убрать 'threed' из второй половины designCardOutputsWhere;
// захардкодить род в RegisterBatch обратно в flat; проставить кадру пачки run_kind не из JOIN'а.
func TestDesignDBAHandUploadedModelIsACardOutputWithNoRun(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			// ⚠ НИ ghost_view, НИ КОЛОРВЕЯ. Модель — не сторона изделия и не цвет; ровно это и
			// пошлёт клиент. Если бы дверь чего-то из этого требовала, здесь был бы отказ.
			{MediaId: media, Kind: entity.DesignPictureKindThreed},
		},
	})
	require.NoError(t, err, "род threed обязан быть законной ручной загрузкой")
	require.Len(t, batch.Pictures, 1)
	model := batch.Pictures[0]

	require.Equal(t, entity.DesignPictureKindThreed, model.Kind)
	require.False(t, model.RunId.Valid, "у ручной загрузки прогона нет по построению")
	require.True(t, model.BatchId.Valid, "зато есть полка, с которой она пришла")
	require.Equal(t, entity.DesignSourceUploaded, model.SourceClass)

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)

	var got *entity.DesignCardOutput
	for i := range band.Outputs {
		if band.Outputs[i].Picture.Id == model.Id {
			got = &band.Outputs[i]
			break
		}
	}
	require.NotNil(t, got,
		"загруженная модель обязана стоять в выходах карточки — иначе раздел 3D MODELS OF THIS "+
			"CARD её не покажет, и вся возможность E-13 существует только в базе")

	require.Zero(t, got.RunId, "прогона нет, и штамп говорит это нулём")
	require.Empty(t, got.RunKind,
		"пустой род прогона — ЕДИНСТВЕННОЕ, чем панель отличает ручную загрузку от купленного "+
			"результата: непустой род здесь приписал бы модели деньги, которых никто не платил")
	require.Zero(t, got.RunRrev)
	require.NotNil(t, got.Picture.Media, "у выхода обязан быть файл, а не только id")
	require.Equal(t, entity.DesignSourceUploaded, got.Picture.SourceClass)
}
