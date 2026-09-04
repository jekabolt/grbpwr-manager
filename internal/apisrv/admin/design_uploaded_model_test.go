package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ═══ E-13: ЗАГРУЖЕННАЯ РУКАМИ МОДЕЛЬ НЕ ВХОДИТ НИ В ОДИН ПРОГОН ═════════════════════════════════
//
// Круг 16 открывает дверь, через которую .glb попадает на карточку кадром рода `threed`
// (UploadContentModel → RegisterDesignUpload). Решение, принятое ВМЕСТЕ с дверью и записанное
// здесь пробой, а не только словами: такую модель можно смотреть, держать и скачивать, и ВХОДОМ
// платного прогона она не становится.
//
// ⚠ ПОЧЕМУ ЭТО НУЖНО ЗАПИРАТЬ ПРОБОЙ, А НЕ ОСТАВИТЬ КОММЕНТАРИЕМ. Дверь постановки плиты уже
// требует совпадения рода кадра и рода слота (store/design/bench.go), поэтому кадр `threed` может
// встать ТОЛЬКО в threed-слот верстака. Значит всё держится на одном факте: отбор входов
// (designSelectBench) спрашивает у верстака род `flat` (флэт/рендер) или `render` (3D) — и НИКОГДА
// `threed`. Факт этот записан одной переменной `want`, и расширить её до третьего значения —
// правка в одну строку, у которой нет ни одного естественного сторожа: провайдер получил бы адрес
// .glb там, где ждёт картинку, и заплачено было бы за отказ.
//
// МУТАЦИЯ, КОТОРУЮ ПРОБА КРАСНИТ: в designSelectBench задать `want = threed` для любого рода —
// или, что вероятнее, снять сравнение `entity.DesignKindOrFlat(slot.Kind) != want` вовсе, отчего
// в оплаченный прогон уедут ВСЕ плиты карточки, .glb включительно.

// benchWithAnUploadedModel — верстак карточки, на которой есть всё сразу: флэт, рендер и
// ЗАГРУЖЕННАЯ РУКАМИ 3D-модель, стоящая в threed-слоте. Плита модели намеренно занимает тот же
// вид `front`, что и две другие: адрес слота — это ПАРА (род, вид), поэтому три плиты на одном
// виде — законное состояние, и отбор, перепутавший ось, возьмёт не ту.
func benchWithAnUploadedModel() []entity.DesignBenchSlot {
	plate := func(id, picID, mediaID int, kind string) entity.DesignBenchSlot {
		return entity.DesignBenchSlot{
			Id: id, TechCardId: designRunCardID, ViewKey: entity.DesignViewFront,
			Kind: kind, ExclusiveKey: entity.DesignViewFront,
			PictureId: sql.NullInt32{Int32: int32(picID), Valid: true},
			Picture: &entity.DesignPicture{
				Id: picID, TechCardId: designRunCardID, MediaId: mediaID, Kind: kind,
				// Ручная загрузка: прогона нет, полка есть. Ровно то, что RegisterBatch пишет.
				BatchId:     sql.NullInt32{Int32: 900, Valid: true},
				SourceClass: entity.DesignSourceUploaded,
			},
		}
	}
	return []entity.DesignBenchSlot{
		plate(1, 10, 1010, entity.DesignPictureKindFlat),
		plate(2, 11, 1011, entity.DesignPictureKindRender),
		plate(3, 12, 1012, entity.DesignPictureKindThreed),
	}
}

// НИ ОДИН РОД ПРОГОНА НЕ БЕРЁТ ПЛИТУ РОДА `threed`.
//
// Проба перебирает ВСЕ рода, которые вообще читают карточку, и на каждом требует двух вещей
// сразу: модели в оплаченном входе нет, а то, что этому роду положено, — есть. Одного отрицания
// было бы мало: пустой ответ на всех родах прошёл бы его молча и означал бы сломанный отбор, а не
// закрытую дверь.
func TestNoRunKindEverTakesAHandUploadedModelAsAnInput(t *testing.T) {
	const modelPlate int32 = 12

	for _, tc := range []struct {
		kind   string
		params *pb_common.DesignRunParams
		want   []int32
		why    string
	}{
		{
			kind:   entity.DesignRunKindFlat,
			params: &pb_common.DesignRunParams{UseFlatSlots: true},
			want:   []int32{10},
			why:    "флэт по просьбе читает флэтовый верстак",
		},
		{
			kind:   entity.DesignRunKindRender,
			params: &pb_common.DesignRunParams{},
			want:   []int32{10},
			why:    "рендер строится из ФЛЭТОВ",
		},
		{
			kind:   entity.DesignRunKindThreed,
			params: &pb_common.DesignRunParams{},
			want:   []int32{11},
			why:    "3D строится из РЕНДЕРОВ, а не из моделей — турнтейбл собирают из видов изделия",
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			_, plates := designSelectBench(designInputSources{
				Kind:   tc.kind,
				Bench:  benchWithAnUploadedModel(),
				Params: tc.params,
			})
			require.Equal(t, tc.want, plates, tc.why)
			require.NotContains(t, plates, modelPlate,
				"загруженная руками .glb-модель не является входом ни одного платного прогона: "+
					"поставщику уехал бы адрес модели там, где он ждёт картинку")
		})
	}
}

// РОД КАДРА, КОТОРЫЙ ДВЕРЬ ЗАГРУЗКИ ПРИНИМАЕТ, И РОД ВЕРСТАКА, КОТОРЫЙ ЕГО ДЕРЖИТ, — ОДНО
// МНОЖЕСТВО, И `threed` В НЁМ ЕСТЬ.
//
// Это вторая половина утверждения «бэкенду для E-13 не нужно ничего, кроме двери байтов»: если бы
// `threed` не был законным родом ЗАГРУЖАЕМОГО кадра, дверь модели упиралась бы в отказ
// RegisterDesignUpload и понадобилась бы ещё одна правка. Проба спрашивает ровно те две функции,
// которыми это решают и хендлер, и стор.
func TestThreedIsALegalKindForBothAHandUploadAndABenchAddress(t *testing.T) {
	require.True(t, entity.IsDesignPictureKind(entity.DesignPictureKindThreed),
		"без этого RegisterDesignUpload отказал бы модели у двери")
	require.True(t, entity.IsDesignBenchKind(entity.DesignPictureKindThreed),
		"…и без этого её некуда было бы поставить на 3D-верстаке")
}
