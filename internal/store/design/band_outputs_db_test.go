package design_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/stretchr/testify/require"
)

// ЖИВАЯ ПРОБА ВЫХОДОВ КАРТОЧКИ (H-9). Раздел «рендеры этой карточки» читал ПЕРВУЮ СТРАНИЦУ ленты
// и потому терял рендеры по одному: всякий прогон любого рода выталкивал из окна старый, а вместе
// с ним уходили кропы, нарезанные из его листа. Здесь это состояние воспроизводится строками —
// прогон с рендером УТОПЛЕН пятнадцатью более свежими прогонами, — и проверяется, что новое поле
// отвечает про КАРТОЧКУ, а не про страницу.
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там:
// без CI=1 проба пропускается ДО открытия соединения.

// outputsProbeRun кладёт ЗАКРЫТУЮ строку истории заданного рода прямым INSERT. Прямым — потому
// что нужна не машина прогонов, а её след: пятнадцать строк ради одного окна страницы, и StartRun
// на каждую потратил бы ещё и бюджет дня.
func outputsProbeRun(t *testing.T, raw *sql.DB, cardID int, kind string, rrev, cw int) int {
	t.Helper()
	var colorway any
	if cw > 0 {
		colorway = cw
	}
	res, err := raw.Exec(`
		INSERT INTO design_run
			(tech_card_id, kind, status, client_request_id, provider_idempotency_key, rrev, colorway_id)
		VALUES (?, ?, 'done', ?, ?, ?, ?)`,
		cardID, kind, uuid.NewString(), uuid.NewString(), rrev, colorway)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

// outputsProbePicture кладёт кадр прогона. derivedFrom > 0 делает его КРОПОМ: разрез наследует
// run_id родителя, и именно поэтому кроп обязан находиться тем же предикатом, что и лист.
//
// cw пишется в САМ КАДР — так же, как это делает queue.go, закрывая прогон: колорвей строки
// уезжает в её кадры. Это ключ раздела, по которому режется поколорвейное окно потолка.
func outputsProbePicture(t *testing.T, raw *sql.DB, cardID, runID, mediaID, ordinal int,
	kind string, cw, derivedFrom int, hidden bool,
) int {
	t.Helper()
	var parent, colorway any
	if derivedFrom > 0 {
		parent = derivedFrom
	}
	if cw > 0 {
		colorway = cw
	}
	hiddenAt := "NULL"
	if hidden {
		hiddenAt = "UTC_TIMESTAMP(6)"
	}
	res, err := raw.Exec(`
		INSERT INTO design_picture
			(tech_card_id, media_id, run_id, ordinal, kind, colorway_id, derived_from,
			 source_class, hidden_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'generated', `+hiddenAt+`)`,
		cardID, mediaID, runID, ordinal, kind, colorway, parent)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

// ВЫХОДЫ КАРТОЧКИ ЧИТАЮТСЯ ПО ВСЕЙ КАРТОЧКЕ, А ЛЕНТА — ПО СТРАНИЦЕ, И РАСХОЖДЕНИЕ ЭТО СМЫСЛ ПОЛЯ.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНЯТ:
//   - снять LEFT JOIN / предикат рода прогона в designCardOutputsWhere (в список полезут флэты,
//     и счёт разъедется);
//   - дописать `AND p.hidden_at IS NULL` (спрятанный рендер исчезнет — второе, невидимое место,
//     где кадр пропадает);
//   - собирать род из p.kind вместо r.kind (перекрас перестанет отличаться от рендера);
//   - не звать resolveMedia (у выхода будет id и не будет файла — раздел нечем нарисовать).
func TestDesignDBBandOutputsAreWholeCardNotThePage(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	cw := probeColorway(t, raw, card, "BLK")

	// САМЫЙ СТАРЫЙ ПРОГОН — тот, ради которого проба и существует: лист рендера, кроп с него и
	// спрятанный кадр. После пятнадцати более свежих прогонов он гарантированно вне страницы.
	oldRun := outputsProbeRun(t, raw, card, entity.DesignRunKindRender, 7, cw)
	sheet := outputsProbePicture(t, raw, card, oldRun, probeMedia(t, raw), 0,
		entity.DesignPictureKindRender, cw, 0, false)
	crop := outputsProbePicture(t, raw, card, oldRun, probeMedia(t, raw), 1,
		entity.DesignPictureKindRender, cw, sheet, false)
	hiddenPic := outputsProbePicture(t, raw, card, oldRun, probeMedia(t, raw), 2,
		entity.DesignPictureKindRender, cw, 0, true)

	// ПЕРЕКРАС: прогон рода recolor, кадр рода render. Без штампа два раздела слиплись бы.
	recolorRun := outputsProbeRun(t, raw, card, entity.DesignRunKindRecolor, 8, cw)
	recolorPic := outputsProbePicture(t, raw, card, recolorRun, probeMedia(t, raw), 0,
		entity.DesignPictureKindRender, cw, 0, false)

	// ПЯТНАДЦАТЬ ФЛЭТОВЫХ ПРОГОНОВ, каждый со своим кадром: они топят старый рендер и НЕ ИМЕЮТ
	// права попасть в выходы — за флэт денег не платили, и растёт он с каждой перетрассировкой.
	var flatPics []int
	for i := 0; i < 15; i++ {
		run := outputsProbeRun(t, raw, card, entity.DesignRunKindFlat, 0, 0)
		flatPics = append(flatPics, outputsProbePicture(t, raw, card, run, probeMedia(t, raw), 0,
			entity.DesignPictureKindFlat, 0, 0, false))
	}

	// ЗАГРУЖЕННЫЙ РЕНДЕР — тоже рендер этой карточки: пометка «выбран» пишется по id любого кадра,
	// и плита, которой нет в списке, не может быть выбрана.
	uploaded := uploadRenderPlate(t, rep, raw, card, cw)

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ САМОГО ДЕФЕКТА: страница ленты старого прогона НЕ СОДЕРЖИТ. Без этой
	// проверки всё, что ниже, доказывало бы лишь «поле непустое».
	require.Len(t, band.Runs, design.DefaultRunPageLimit)
	for _, r := range band.Runs {
		require.NotEqual(t, oldRun, r.Id,
			"стенд обязан утопить старый прогон, иначе проба не про H-9")
	}

	got := map[int]entity.DesignCardOutput{}
	for _, o := range band.Outputs {
		got[o.Picture.Id] = o
	}
	require.Len(t, band.Outputs, 5,
		"лист, кроп, спрятанный кадр, перекрас и загруженная плита — и ни одного флэта")
	require.Equal(t, 5, band.OutputsTotal,
		"счётчик считается тем же предикатом: разойдясь, он соврал бы про усечение")

	// ⚠ КЛЮЧ РАЗДЕЛА — КОЛОРВЕЙ КАДРА, А НЕ ПРОГОНА, И ЗДЕСЬ ЭТО ИЗМЕРИМО. У загруженной плиты
	// прогона нет вовсе, значит RunColorwayId у неё 0, — но колорвей у неё назван и настоящий.
	// Ключ по прогону разложил бы эти пять кадров как {cw: 4, 0: 1}, то есть выбросил бы плиту
	// колорвея BLK в «неатрибутированный» раздел, откуда её нельзя выбрать в свой.
	//
	// МУТАЦИЯ: заменить designCardOutputsColorway на COALESCE(r.colorway_id, 0).
	require.Equal(t, map[int]int{cw: 5}, band.OutputsTotalByColorway,
		"все пять кадров принадлежат одному колорвею, загруженная плита включительно")

	// КРОП ВНЕ СТРАНИЦЫ — И СО ШТАМПОМ. Ни род, ни ревизия, ни колорвей из самой картинки не
	// выводятся: её прогона в ответе нет вовсе.
	require.Contains(t, got, crop, "кроп листа рендера — такой же выход прогона, как и лист")
	require.Equal(t, oldRun, got[crop].RunId)
	require.Equal(t, entity.DesignRunKindRender, got[crop].RunKind)
	require.Equal(t, 7, got[crop].RunRrev)
	require.Equal(t, cw, got[crop].RunColorwayId,
		"штамп несёт колорвей ПРОГОНА; ключ раздела — колорвей кадра, и у кропа они совпадают, "+
			"потому что разрез наследует у родителя оба")
	require.NotNil(t, got[crop].Picture.Media, "у выхода обязан быть файл, а не только id")
	require.Equal(t, sheet, int(got[crop].Picture.DerivedFrom.Int32))

	require.Contains(t, got, sheet)
	require.Contains(t, got, hiddenPic, "спрятанный кадр не исчезает из ответа")
	require.True(t, got[hiddenPic].Picture.HiddenAt.Valid,
		"он едет СО СВОИМ ФЛАГОМ: фильтрует клиент, а не сервер")

	require.Contains(t, got, recolorPic)
	require.Equal(t, entity.DesignPictureKindRender, got[recolorPic].Picture.Kind,
		"перекрас правда рождает кадр рода render")
	require.Equal(t, entity.DesignRunKindRecolor, got[recolorPic].RunKind,
		"…и только род ПРОГОНА разводит ON MODEL и RENDERS")

	require.Contains(t, got, uploaded, "загруженный рендер — рендер карточки")
	require.Zero(t, got[uploaded].RunId, "прогона у него нет, и штамп говорит это нулём")
	require.Empty(t, got[uploaded].RunKind)
	require.True(t, got[uploaded].Picture.BatchId.Valid, "зато сказано, с какой полки он пришёл")

	for _, id := range flatPics {
		require.NotContains(t, got, id,
			"флэты остаются постраничными: они бесплатны и растут с каждой правкой")
	}
}

// ПОТОЛОК ТРАТИТСЯ ПОКОЛОРВЕЙНО, И ТОЛЬКО ЖИВОЙ СТЕНД ЭТО ДОКАЗЫВАЕТ.
//
// ⚠ ЗАЧЕМ ЭТА ПРОБА ПОЯВИЛАСЬ. Проба выше держит на карточке ПЯТЬ выходов против потолка в
// шестьдесят — то есть усечение в ней не исполняется вовсе. Значит про порядок и потолок она
// доказывала ровно ничего: ни переворот `ORDER BY p.id DESC`, ни понижение числа её не краснили,
// и «цитата + мутация» была наполовину пустой. Здесь строк БОЛЬШЕ ПОТОЛКА, поэтому обе половины
// действительно исполняются.
//
// СТЕНД. Тихий колорвей заводится ПЕРВЫМ — его два кадра получают самые маленькие id на карточке,
// то есть при общем потолке «свежие первыми» они вылетают первыми. Громкий получает потолок плюс
// три. Это ровно та карточка, на которой прежний общий `LIMIT 200` воспроизводил дефект H-9:
// раздел одного колорвея приходил ПУСТЫМ, а карточный счётчик говорил лишь «где-то их больше».
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНЯТ (и каждая — своя строка):
//   - убрать PARTITION BY, вернув общий потолок на карточку → тихий колорвей приходит пустым;
//   - перевернуть `ORDER BY p.id DESC` внутри окна → у громкого остаются САМЫЕ СТАРЫЕ кадры;
//   - понизить MaxCardOutputsPerColorway → у громкого приходит не потолок, а меньше (это и есть
//     доказательство, что чтение связано именно этой константой, а не числом в запросе);
//   - считать поколорвейный итог другим предикатом → подпись усечения разойдётся со списком.
func TestDesignDBBandOutputsAreCappedPerColorwayNotWholeCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	quiet := probeColorway(t, raw, card, "BLK")
	loud := probeColorway(t, raw, card, "WHT")

	// ОДНО МЕДИА НА ВЕСЬ СТЕНД: media(id) не уникален по кадру, а шестьдесят пять строк media
	// стенду ничего не добавили бы, кроме секунд.
	media := probeMedia(t, raw)

	quietRun := outputsProbeRun(t, raw, card, entity.DesignRunKindRender, 1, quiet)
	quietPics := make([]int, 0, 2)
	for i := 0; i < 2; i++ {
		quietPics = append(quietPics, outputsProbePicture(t, raw, card, quietRun, media, i,
			entity.DesignPictureKindRender, quiet, 0, false))
	}

	over := design.MaxCardOutputsPerColorway + 3
	loudRun := outputsProbeRun(t, raw, card, entity.DesignRunKindRender, 2, loud)
	loudPics := make([]int, 0, over)
	for i := 0; i < over; i++ {
		loudPics = append(loudPics, outputsProbePicture(t, raw, card, loudRun, media, i,
			entity.DesignPictureKindRender, loud, 0, false))
	}

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)

	byColorway := map[int][]int{}
	for _, o := range band.Outputs {
		key := entity.DesignColorwayOrNone(o.Picture.ColorwayId)
		byColorway[key] = append(byColorway[key], o.Picture.Id)
	}

	// ─── ГОЛОДАНИЕ НЕВЫРАЗИМО ───
	require.Len(t, byColorway[quiet], 2,
		"тихий колорвей цел, хотя ВСЕ его кадры старше любого кадра громкого: общий потолок "+
			"выкосил бы раздел целиком, и это ровно дефект H-9 на горизонте потолка")

	// ─── ПОТОЛОК СВЯЗАН КОНСТАНТОЙ, И УСЕЧЕНИЕ ДЕЙСТВИТЕЛЬНО ПРОИСХОДИТ ───
	require.Len(t, byColorway[loud], design.MaxCardOutputsPerColorway,
		"громкому колорвею отдаётся ровно потолок: чтение связано этой константой")
	require.Equal(t, over, band.OutputsTotalByColorway[loud],
		"…а подпись говорит, сколько их НА САМОМ ДЕЛЕ — иначе усечение неотличимо от полноты")
	require.Equal(t, 2, band.OutputsTotalByColorway[quiet])
	require.Equal(t, over+2, band.OutputsTotal, "карточный итог — сумма поколорвейных")
	require.Greater(t, band.OutputsTotalByColorway[loud], len(byColorway[loud]),
		"стенд обязан ДЕЙСТВИТЕЛЬНО усекать, иначе про порядок и потолок он ничего не проверяет")

	// ─── УСЕЧЕНИЕ ОСТАВЛЯЕТ СВЕЖИЕ, А НЕ СТАРЫЕ ───
	require.Contains(t, byColorway[loud], loudPics[over-1], "самый свежий кадр остаётся всегда")
	for _, dropped := range loudPics[:3] {
		require.NotContains(t, byColorway[loud], dropped,
			"выброшены САМЫЕ СТАРЫЕ три: перевёрнутый порядок в окне оставил бы ровно их")
	}

	// ─── И ПРИХОДИТ ЭТО СВЕЖИМИ ВПЕРЁД ───
	for i := 1; i < len(band.Outputs); i++ {
		require.Less(t, band.Outputs[i].Picture.Id, band.Outputs[i-1].Picture.Id,
			"выходы едут по убыванию id: раздел рисует свежие первыми")
	}
}
